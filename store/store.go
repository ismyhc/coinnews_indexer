// Package store persists decoded CoinNews data and answers the read queries the
// API needs. The Store interface lets callers swap the SQLite backend for the
// in-memory backend (handy for tests) without changing the scanner or API.
package store

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/ismyhc/coinnews-indexer/codec"
)

// Item is a row in the items index — one per CoinNews OP_RETURN output.
type Item struct {
	ID        codec.ItemID
	TxID      string
	Vout      uint32
	Height    int64
	TxIndex   int
	VoutIndex int
	TypeTag   codec.TypeTag
	BlockTime int64 // unix seconds
}

// Topic is a created (named) topic from a TopicCreation message. Only created
// topics are stored and listed; a topic id referenced by stories without a
// creation message is not a row here. StoryCount is computed by ListTopics (not
// persisted).
type Topic struct {
	Topic         codec.Topic
	Name          string
	RetentionDays byte
	CreatedHeight int64
	TxID          string

	StoryCount int // number of stories in this topic (computed)
}

// Story is the decoded payload of a story item.
type Story struct {
	ID        codec.ItemID
	Topic     codec.Topic
	Headline  string
	Subtype   codec.Subtype
	URL       string
	Body      string
	Lang      string
	NSFW      bool
	MediaHash []byte
	RawTLV    []byte
}

// Comment is the decoded payload of a (verified) comment item.
type Comment struct {
	ID         codec.ItemID
	ParentID   codec.ItemID
	AuthorXPK  codec.XOnlyPubKey
	Body       string
	URL        string
	Lang       string
	ReplyQuote string
	RawTLV     []byte
}

// Vote is the latest vote retained for a (target, author) pair.
type Vote struct {
	TargetID  codec.ItemID
	AuthorXPK codec.XOnlyPubKey
	Kind      codec.TypeTag // TypeUpvote or TypeDownvote
	Height    int64
	TxIndex   int
	VoutIndex int
	BlockTime int64
}

// Continuation is one stored overflow chunk.
type Continuation struct {
	HeadID codec.ItemID
	Seq    byte
	Chunk  []byte
	Height int64
}

// FeedQuery filters and paginates a ranked or recency feed.
type FeedQuery struct {
	Topic       *codec.Topic   // nil == any topic
	Subtype     *codec.Subtype // nil == any subtype
	IncludeNSFW bool
	Limit       int
	Offset      int
	NowUnix     int64 // scoring clock; 0 == time.Now()
}

// FeedItem is a story enriched with vote tally, comment count, and rank score.
type FeedItem struct {
	Item
	Topic        codec.Topic
	Headline     string
	URL          string
	Body         string
	Lang         string
	Subtype      codec.Subtype
	NSFW         bool
	Points       int // upvotes - downvotes
	CommentCount int
	Score        float64
}

// ThreadComment is a comment enriched with its vote tally and score.
type ThreadComment struct {
	ID         codec.ItemID
	ParentID   codec.ItemID
	AuthorXPK  codec.XOnlyPubKey
	Body       string
	URL        string
	Lang       string
	ReplyQuote string
	Height     int64
	BlockTime  int64
	Points     int
	Score      float64
}

// Store is the persistence + query contract. All methods are safe for the
// scanner's single-writer use; read methods may run concurrently.
type Store interface {
	Close() error

	// Cursor tracks how far the scanner has progressed (for resume + reorg).
	SaveCursor(ctx context.Context, height int64, hash string) error
	LoadCursor(ctx context.Context) (height int64, hash string, ok bool, err error)

	// DeleteFromHeight removes everything at block_height >= height (reorg rewind).
	DeleteFromHeight(ctx context.Context, height int64) error

	// Writes — called in canonical scan order.
	PutItem(ctx context.Context, it Item) error
	// InsertTopicIfAbsent records a topic only if its id is unseen (first-wins);
	// it reports whether the row was inserted.
	InsertTopicIfAbsent(ctx context.Context, t Topic) (bool, error)
	PutStory(ctx context.Context, s Story) error
	PutComment(ctx context.Context, c Comment) error
	// PutVote keeps only the latest vote per (target, author) in canonical order.
	PutVote(ctx context.Context, v Vote) error
	PutContinuation(ctx context.Context, c Continuation) error

	// Reference resolution.
	ItemByOutpoint(ctx context.Context, txid string, vout uint32) (Item, bool, error)
	ItemByID(ctx context.Context, id codec.ItemID) (Item, bool, error)

	// Reads for the API.
	FrontPage(ctx context.Context, q FeedQuery) ([]FeedItem, error)
	NewFeed(ctx context.Context, q FeedQuery) ([]FeedItem, error)
	GetItem(ctx context.Context, id codec.ItemID) (FeedItem, bool, error)
	Thread(ctx context.Context, root codec.ItemID) ([]ThreadComment, error)
	ByAuthor(ctx context.Context, author codec.XOnlyPubKey, limit, offset int) ([]ThreadComment, error)
	ByTopic(ctx context.Context, topic codec.Topic, limit, offset int) ([]FeedItem, error)
	ListTopics(ctx context.Context) ([]Topic, error)
}

// --- shared ranking helpers (used by both backends) ---

func resolveNow(nowUnix int64) int64 {
	if nowUnix == 0 {
		return time.Now().Unix()
	}
	return nowUnix
}

// rankScore is the Hacker News formula: (points - 1) / (age_hours + 2)^1.8.
func rankScore(points int, nowUnix, blockTime int64) float64 {
	ageHours := float64(nowUnix-blockTime) / 3600.0
	if ageHours < 0 {
		ageHours = 0
	}
	return float64(points-1) / math.Pow(ageHours+2.0, 1.8)
}

// sortByScore orders highest score first, breaking ties by height descending.
func sortByScore(items []FeedItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].Height > items[j].Height
	})
}

// sortByNewest orders by canonical position descending (newest first).
func sortByNewest(items []FeedItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Height != b.Height {
			return a.Height > b.Height
		}
		if a.TxIndex != b.TxIndex {
			return a.TxIndex > b.TxIndex
		}
		return a.VoutIndex > b.VoutIndex
	})
}

// sortThreadCanonical orders comments by canonical position ascending.
func sortThreadCanonical(cs []ThreadComment) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Height != cs[j].Height {
			return cs[i].Height < cs[j].Height
		}
		return cs[i].ID.String() < cs[j].ID.String()
	})
}

// sortThreadCanonicalDesc orders comments newest first.
func sortThreadCanonicalDesc(cs []ThreadComment) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Height != cs[j].Height {
			return cs[i].Height > cs[j].Height
		}
		return cs[i].ID.String() > cs[j].ID.String()
	})
}

// sortTopics orders topics by creation height.
func sortTopics(ts []Topic) {
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].CreatedHeight < ts[j].CreatedHeight })
}

// page applies limit/offset to an already-sorted slice. A non-positive limit
// means "no limit".
func page[T any](items []T, limit, offset int) []T {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return nil
	}
	items = items[offset:]
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}
	return items
}
