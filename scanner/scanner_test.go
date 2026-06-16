package scanner

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/ismyhc/coinnews-indexer/codec"
	"github.com/ismyhc/coinnews-indexer/rpc"
	"github.com/ismyhc/coinnews-indexer/store"
)

// --- fake block source ---

type fakeSource struct {
	byHash   map[string]*rpc.Block
	hashAt   map[int64]string
	tip      int64
}

func newFakeSource() *fakeSource {
	return &fakeSource{byHash: map[string]*rpc.Block{}, hashAt: map[int64]string{}, tip: -1}
}

func (f *fakeSource) add(b *rpc.Block) {
	f.byHash[b.Hash] = b
	f.hashAt[b.Height] = b.Hash
	if b.Height > f.tip {
		f.tip = b.Height
	}
}

func (f *fakeSource) GetBlockCount(context.Context) (int64, error) { return f.tip, nil }
func (f *fakeSource) GetBlockHash(_ context.Context, h int64) (string, error) {
	return f.hashAt[h], nil
}
func (f *fakeSource) GetBlock(_ context.Context, hash string) (*rpc.Block, error) {
	return f.byHash[hash], nil
}

// --- block/tx builders ---

func txidHex(b byte) string {
	var x [32]byte
	x[0] = b
	return hex.EncodeToString(x[:])
}

// nullDataScript builds a scriptPubKey hex: OP_RETURN <push payload>.
func nullDataScript(payload []byte) string {
	script := []byte{0x6a}
	if len(payload) < 0x4c {
		script = append(script, byte(len(payload)))
	} else {
		script = append(script, 0x4c, byte(len(payload))) // OP_PUSHDATA1
	}
	script = append(script, payload...)
	return hex.EncodeToString(script)
}

func opReturnTx(txid string, payload []byte) rpc.Tx {
	var t rpc.Tx
	t.TxID = txid
	var v rpc.Vout
	v.N = 0
	v.ScriptPubKey.Type = "nulldata"
	v.ScriptPubKey.Hex = nullDataScript(payload)
	t.Vout = []rpc.Vout{v}
	return t
}

func block(height int64, hash, prev string, txs ...rpc.Tx) *rpc.Block {
	return &rpc.Block{Hash: hash, Height: height, Time: height * 600, PreviousBlockHash: prev, Tx: txs}
}

// itemIDOf mirrors how the scanner derives an item id from an outpoint.
func itemIDOf(t *testing.T, txid string) codec.ItemID {
	t.Helper()
	id, err := codec.ItemIDFromRPC(txid, 0)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestScannerEndToEnd(t *testing.T) {
	ctx := context.Background()
	src := newFakeSource()
	st := store.NewMemory()
	sc := New(src, st, nil)

	// Signing identity for the comment + votes.
	priv, _ := btcec.NewPrivateKey()
	xpk := schnorr.SerializePubKey(priv.PubKey())

	// Block 0: a topic and a story.
	topicTx := txidHex(0x10)
	storyTx := txidHex(0x11)
	topicMsg := codec.EncodeTopicCreation(codec.TopicCreation{Topic: codec.Topic{1, 2, 3, 4}, Name: "general"})
	storyMsg := codec.EncodeStory(codec.Story{Topic: codec.Topic{1, 2, 3, 4}, Headline: "Hello"})
	src.add(block(0, "h0", "", opReturnTx(topicTx, topicMsg), opReturnTx(storyTx, storyMsg)))

	storyID := itemIDOf(t, storyTx)

	// Block 1: a valid comment on the story, plus a valid upvote on the story.
	commentTx := txidHex(0x20)
	cTLV := []codec.TLV{{Tag: codec.TLVBody, Value: []byte("nice")}}
	cBlob := codec.EncodeTLVs(cTLV)
	cDigest := codec.CommentSigHash(storyID, cBlob)
	cSig, _ := schnorr.Sign(priv, cDigest[:])
	var comment codec.Comment
	comment.ParentID = storyID
	copy(comment.AuthorXPK[:], xpk)
	copy(comment.Sig[:], cSig.Serialize())
	comment.TLVs = cTLV

	voteTx := txidHex(0x21)
	vDigest := codec.VoteSigHash(codec.TypeUpvote, storyID)
	vSig, _ := schnorr.Sign(priv, vDigest[:])
	var vote codec.Vote
	vote.Kind = codec.TypeUpvote
	vote.TargetID = storyID
	copy(vote.AuthorXPK[:], xpk)
	copy(vote.Sig[:], vSig.Serialize())

	// A vote on a NON-existent target must be dropped.
	orphanVoteTx := txidHex(0x22)
	bogusTarget := codec.ItemID{0xff}
	ovDigest := codec.VoteSigHash(codec.TypeUpvote, bogusTarget)
	ovSig, _ := schnorr.Sign(priv, ovDigest[:])
	var orphanVote codec.Vote
	orphanVote.Kind = codec.TypeUpvote
	orphanVote.TargetID = bogusTarget
	copy(orphanVote.AuthorXPK[:], xpk)
	copy(orphanVote.Sig[:], ovSig.Serialize())

	src.add(block(1, "h1", "h0",
		opReturnTx(commentTx, codec.EncodeComment(comment)),
		opReturnTx(voteTx, codec.EncodeVote(vote)),
		opReturnTx(orphanVoteTx, codec.EncodeVote(orphanVote)),
	))

	if err := sc.Run(ctx, 0); err != nil {
		t.Fatal(err)
	}

	// Topic recorded.
	if topics, _ := st.ListTopics(ctx); len(topics) != 1 || topics[0].Name != "general" {
		t.Fatalf("topic not indexed: %+v", topics)
	}
	// Story present with 1 point (the upvote) and 1 comment.
	f, ok, _ := st.GetItem(ctx, storyID)
	if !ok {
		t.Fatal("story not indexed")
	}
	if f.Points != 1 {
		t.Fatalf("expected 1 point from upvote, got %d", f.Points)
	}
	if f.CommentCount != 1 {
		t.Fatalf("expected 1 comment, got %d", f.CommentCount)
	}
	// Orphan vote (unknown target) must not have created an item.
	if _, ok, _ := st.ItemByID(ctx, itemIDOf(t, orphanVoteTx)); ok {
		t.Fatal("orphan vote should have been dropped, not indexed")
	}
	// Cursor advanced to tip.
	if h, _, ok, _ := st.LoadCursor(ctx); !ok || h != 1 {
		t.Fatalf("cursor = %d, want 1", h)
	}
}

func TestScannerDropsBadSignature(t *testing.T) {
	ctx := context.Background()
	src := newFakeSource()
	st := store.NewMemory()
	sc := New(src, st, nil)

	storyTx := txidHex(0x11)
	src.add(block(0, "h0", "",
		opReturnTx(storyTx, codec.EncodeStory(codec.Story{Topic: codec.Topic{1}, Headline: "S"}))))
	storyID := itemIDOf(t, storyTx)

	// Vote signed by key A but claiming key B's pubkey -> verification fails.
	privA, _ := btcec.NewPrivateKey()
	_, pubB := btcec.PrivKeyFromBytes([]byte(strings.Repeat("\x02", 32)))
	digest := codec.VoteSigHash(codec.TypeUpvote, storyID)
	sig, _ := schnorr.Sign(privA, digest[:])
	var v codec.Vote
	v.Kind = codec.TypeUpvote
	v.TargetID = storyID
	copy(v.AuthorXPK[:], schnorr.SerializePubKey(pubB)) // wrong author
	copy(v.Sig[:], sig.Serialize())

	src.add(block(1, "h1", "h0", opReturnTx(txidHex(0x30), codec.EncodeVote(v))))
	if err := sc.Run(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if f, _, _ := st.GetItem(ctx, storyID); f.Points != 0 {
		t.Fatalf("bad-signature vote should be dropped, points=%d", f.Points)
	}
}

func TestScannerReorgRewind(t *testing.T) {
	ctx := context.Background()
	src := newFakeSource()
	st := store.NewMemory()
	sc := New(src, st, nil)
	sc.ReorgDepth = 1

	// Heights 100..102, a distinct story per block.
	s100 := txidHex(0x40)
	s101 := txidHex(0x41)
	s102old := txidHex(0x42)
	src.add(block(100, "a100", "", opReturnTx(s100, codec.EncodeStory(codec.Story{Topic: codec.Topic{1}, Headline: "100"}))))
	src.add(block(101, "a101", "a100", opReturnTx(s101, codec.EncodeStory(codec.Story{Topic: codec.Topic{1}, Headline: "101"}))))
	src.add(block(102, "a102", "a101", opReturnTx(s102old, codec.EncodeStory(codec.Story{Topic: codec.Topic{1}, Headline: "102old"}))))

	if err := sc.Run(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.GetItem(ctx, itemIDOf(t, s102old)); !ok {
		t.Fatal("expected old tip story before reorg")
	}

	// Reorg: height 102 is replaced by a different block (new hash, new content).
	s102new := txidHex(0x43)
	src.add(block(102, "b102", "a101", opReturnTx(s102new, codec.EncodeStory(codec.Story{Topic: codec.Topic{1}, Headline: "102new"}))))

	if err := sc.Run(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.GetItem(ctx, itemIDOf(t, s102old)); ok {
		t.Fatal("old tip story should be gone after reorg")
	}
	if _, ok, _ := st.GetItem(ctx, itemIDOf(t, s102new)); !ok {
		t.Fatal("new tip story should be indexed after reorg")
	}
	if _, ok, _ := st.GetItem(ctx, itemIDOf(t, s100)); !ok {
		t.Fatal("pre-fork story should survive reorg")
	}
}
