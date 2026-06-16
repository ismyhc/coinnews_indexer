package scanner

import (
	"context"
	"log"

	"github.com/ismyhc/coinnews-indexer/codec"
	"github.com/ismyhc/coinnews-indexer/rpc"
	"github.com/ismyhc/coinnews-indexer/store"
)

// BlockSource is the slice of Bitcoin Core RPC the scanner needs. rpc.Client
// satisfies it; tests supply a fake.
type BlockSource interface {
	GetBlockCount(ctx context.Context) (int64, error)
	GetBlockHash(ctx context.Context, height int64) (string, error)
	GetBlock(ctx context.Context, hash string) (*rpc.Block, error)
}

// DefaultReorgDepth is how many blocks the scanner rewinds when it detects the
// chain diverged from what it indexed.
const DefaultReorgDepth = 6

// Scanner walks blocks in canonical order and persists CoinNews messages.
type Scanner struct {
	src        BlockSource
	st         store.Store
	logger     *log.Logger
	ReorgDepth int64
}

// New builds a Scanner. logger may be nil to disable logging.
func New(src BlockSource, st store.Store, logger *log.Logger) *Scanner {
	return &Scanner{src: src, st: st, logger: logger, ReorgDepth: DefaultReorgDepth}
}

func (s *Scanner) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

// Run scans forward from the persisted cursor (or startHeight on a fresh DB)
// up to the current chain tip, then returns. Call it again (e.g. on a timer or
// ZMQ tip notification) to pick up new blocks. It detects reorgs and rewinds.
func (s *Scanner) Run(ctx context.Context, startHeight int64) error {
	next, prevHash, err := s.resume(ctx, startHeight)
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		tip, err := s.src.GetBlockCount(ctx)
		if err != nil {
			return err
		}
		if next > tip {
			return nil // caught up
		}
		hash, err := s.src.GetBlockHash(ctx, next)
		if err != nil {
			return err
		}
		block, err := s.src.GetBlock(ctx, hash)
		if err != nil {
			return err
		}
		// Continuity check: this block must build on the one we just indexed.
		if prevHash != "" && block.PreviousBlockHash != prevHash {
			s.logf("reorg detected at height %d (prev=%s want=%s)", next, block.PreviousBlockHash, prevHash)
			next, prevHash, err = s.rewind(ctx, next-1, startHeight)
			if err != nil {
				return err
			}
			continue
		}
		if err := s.ProcessBlock(ctx, block); err != nil {
			return err
		}
		if err := s.st.SaveCursor(ctx, next, hash); err != nil {
			return err
		}
		prevHash = hash
		next++
	}
}

// resume determines the next height to scan and the hash to chain onto.
func (s *Scanner) resume(ctx context.Context, startHeight int64) (next int64, prevHash string, err error) {
	h, hash, ok, err := s.st.LoadCursor(ctx)
	if err != nil {
		return 0, "", err
	}
	if !ok {
		return startHeight, "", nil
	}
	// Confirm the cursor block is still on the active chain.
	onchain, err := s.src.GetBlockHash(ctx, h)
	if err != nil {
		return 0, "", err
	}
	if onchain == hash {
		return h + 1, hash, nil
	}
	s.logf("cursor block %d diverged from chain; rewinding", h)
	return s.rewind(ctx, h, startHeight)
}

// rewind rolls back ReorgDepth blocks from `from`, deletes the affected data,
// and returns the height/hash to resume scanning at.
func (s *Scanner) rewind(ctx context.Context, from, startHeight int64) (next int64, prevHash string, err error) {
	target := from - s.ReorgDepth
	if target < startHeight {
		target = startHeight
	}
	if err := s.st.DeleteFromHeight(ctx, target); err != nil {
		return 0, "", err
	}
	if target <= startHeight {
		// Re-scan from the configured start; no anchor to chain onto.
		return startHeight, "", nil
	}
	anchor := target - 1
	anchorHash, err := s.src.GetBlockHash(ctx, anchor)
	if err != nil {
		return 0, "", err
	}
	if err := s.st.SaveCursor(ctx, anchor, anchorHash); err != nil {
		return 0, "", err
	}
	s.logf("rewound to height %d", anchor)
	return target, anchorHash, nil
}

// ProcessBlock indexes every CoinNews OP_RETURN in a block, in canonical order
// (transaction index ascending, then output index ascending).
func (s *Scanner) ProcessBlock(ctx context.Context, block *rpc.Block) error {
	for txIndex, tx := range block.Tx {
		for _, vout := range tx.Vout {
			if vout.ScriptPubKey.Type != "nulldata" {
				continue
			}
			payload, ok := OpReturnPayload(vout.ScriptPubKey.Hex)
			if !ok {
				continue
			}
			msg, err := codec.DecodeMessage(payload)
			if err != nil {
				continue // not CoinNews / unknown tag / malformed — skip silently
			}
			itemID, err := codec.ItemIDFromRPC(tx.TxID, vout.N)
			if err != nil {
				continue
			}
			base := store.Item{
				ID: itemID, TxID: tx.TxID, Vout: vout.N,
				Height: block.Height, TxIndex: txIndex, VoutIndex: int(vout.N),
				TypeTag: msg.Type(), BlockTime: block.Time,
			}
			if err := s.persist(ctx, base, msg); err != nil {
				return err
			}
		}
	}
	return nil
}

// persist records a single decoded message and its item row, enforcing the
// protocol rules: signatures must verify, referenced items must already exist,
// topics are first-wins, and votes keep the latest per (target, author).
func (s *Scanner) persist(ctx context.Context, base store.Item, msg codec.Message) error {
	switch m := msg.(type) {
	case codec.TopicCreation:
		if err := s.st.PutItem(ctx, base); err != nil {
			return err
		}
		_, err := s.st.InsertTopicIfAbsent(ctx, store.Topic{
			Topic: m.Topic, Name: m.Name, RetentionDays: m.RetentionDays,
			CreatedHeight: base.Height, TxID: base.TxID,
		})
		return err

	case codec.Story:
		if err := s.st.PutItem(ctx, base); err != nil {
			return err
		}
		return s.st.PutStory(ctx, storyFromMsg(base.ID, m))

	case codec.Comment:
		if !codec.VerifyComment(&m) {
			s.logf("drop comment %s: bad signature", base.ID)
			return nil
		}
		ok, err := s.exists(ctx, m.ParentID)
		if err != nil {
			return err
		}
		if !ok {
			s.logf("drop comment %s: unknown parent %s", base.ID, m.ParentID)
			return nil
		}
		if err := s.st.PutItem(ctx, base); err != nil {
			return err
		}
		return s.st.PutComment(ctx, commentFromMsg(base.ID, m))

	case codec.Vote:
		if !codec.VerifyVote(&m) {
			s.logf("drop vote %s: bad signature", base.ID)
			return nil
		}
		ok, err := s.exists(ctx, m.TargetID)
		if err != nil {
			return err
		}
		if !ok {
			s.logf("drop vote %s: unknown target %s", base.ID, m.TargetID)
			return nil
		}
		if err := s.st.PutItem(ctx, base); err != nil {
			return err
		}
		return s.st.PutVote(ctx, store.Vote{
			TargetID: m.TargetID, AuthorXPK: m.AuthorXPK, Kind: m.Kind,
			Height: base.Height, TxIndex: base.TxIndex, VoutIndex: base.VoutIndex, BlockTime: base.BlockTime,
		})

	case codec.Continuation:
		if err := s.st.PutItem(ctx, base); err != nil {
			return err
		}
		return s.st.PutContinuation(ctx, store.Continuation{
			HeadID: m.HeadID, Seq: m.Seq, Chunk: m.Chunk, Height: base.Height,
		})
	}
	return nil
}

func (s *Scanner) exists(ctx context.Context, id codec.ItemID) (bool, error) {
	_, ok, err := s.st.ItemByID(ctx, id)
	return ok, err
}

func storyFromMsg(id codec.ItemID, m codec.Story) store.Story {
	st := store.Story{ID: id, Topic: m.Topic, Headline: m.Headline, RawTLV: codec.EncodeTLVs(m.TLVs)}
	if v, ok := codec.FindFirst(m.TLVs, codec.TLVURL); ok {
		st.URL = string(v)
	}
	if v, ok := codec.FindFirst(m.TLVs, codec.TLVBody); ok {
		st.Body = string(v)
	}
	if v, ok := codec.FindFirst(m.TLVs, codec.TLVLang); ok {
		st.Lang = string(v)
	}
	// nsfw is treated as a presence flag (see CLAUDE.md note — confirm against a
	// real on-chain vector whether an explicit bool byte is intended instead).
	if _, ok := codec.FindFirst(m.TLVs, codec.TLVNSFW); ok {
		st.NSFW = true
	}
	if v, ok := codec.FindFirst(m.TLVs, codec.TLVSubtype); ok && len(v) >= 1 {
		st.Subtype = codec.Subtype(v[0])
	}
	if v, ok := codec.FindFirst(m.TLVs, codec.TLVMediaHash); ok {
		st.MediaHash = v
	}
	return st
}

func commentFromMsg(id codec.ItemID, m codec.Comment) store.Comment {
	c := store.Comment{ID: id, ParentID: m.ParentID, AuthorXPK: m.AuthorXPK, RawTLV: m.TLVBlob}
	if v, ok := codec.FindFirst(m.TLVs, codec.TLVBody); ok {
		c.Body = string(v)
	}
	if v, ok := codec.FindFirst(m.TLVs, codec.TLVURL); ok {
		c.URL = string(v)
	}
	if v, ok := codec.FindFirst(m.TLVs, codec.TLVLang); ok {
		c.Lang = string(v)
	}
	if v, ok := codec.FindFirst(m.TLVs, codec.TLVReplyQuote); ok {
		c.ReplyQuote = string(v)
	}
	return c
}
