package api

import (
	"encoding/hex"

	"github.com/ismyhc/coinnews-indexer/codec"
	"github.com/ismyhc/coinnews-indexer/store"
)

// itemDTO is the JSON shape for a story in feeds and item lookups.
type itemDTO struct {
	ItemID       string  `json:"item_id"`
	Topic        string  `json:"topic"`
	Headline     string  `json:"headline"`
	URL          string  `json:"url,omitempty"`
	Body         string  `json:"body,omitempty"`
	Subtype      int     `json:"subtype"`
	Lang         string  `json:"lang,omitempty"`
	NSFW         bool    `json:"nsfw"`
	AuthorXPK    string  `json:"author_xpk,omitempty"`
	TxID         string  `json:"txid"`
	Vout         uint32  `json:"vout"`
	BlockHeight  int64   `json:"block_height"`
	BlockTime    int64   `json:"block_time"`
	Points       int     `json:"points"`
	CommentCount int     `json:"comment_count"`
	Score        float64 `json:"score"`
}

// commentDTO is the JSON shape for a comment in threads and author listings.
type commentDTO struct {
	ItemID      string  `json:"item_id"`
	ParentID    string  `json:"parent_id"`
	AuthorXPK   string  `json:"author_xpk"`
	Body        string  `json:"body,omitempty"`
	URL         string  `json:"url,omitempty"`
	Lang        string  `json:"lang,omitempty"`
	ReplyQuote  string  `json:"reply_quote,omitempty"`
	BlockHeight int64   `json:"block_height"`
	BlockTime   int64   `json:"block_time"`
	Points      int     `json:"points"`
	Score       float64 `json:"score"`
}

// topicDTO is the JSON shape for a topic. Only created (named) topics are
// listed; topic ids referenced only by stories are not.
type topicDTO struct {
	Topic         string `json:"topic"`
	Name          string `json:"name"`
	StoryCount    int    `json:"story_count"`
	RetentionDays int    `json:"retention_days"`
	CreatedHeight int64  `json:"created_height"`
	TxID          string `json:"txid,omitempty"`
}

func toItem(f store.FeedItem) itemDTO {
	return itemDTO{
		ItemID:       f.ID.String(),
		Topic:        hex.EncodeToString(f.Topic[:]),
		Headline:     f.Headline,
		URL:          f.URL,
		Body:         f.Body,
		Subtype:      int(f.Subtype),
		Lang:         f.Lang,
		NSFW:         f.NSFW,
		AuthorXPK:    authorHex(f.AuthorXPK),
		TxID:         f.TxID,
		Vout:         f.Vout,
		BlockHeight:  f.Height,
		BlockTime:    f.BlockTime,
		Points:       f.Points,
		CommentCount: f.CommentCount,
		Score:        f.Score,
	}
}

func toComment(c store.ThreadComment) commentDTO {
	return commentDTO{
		ItemID:      c.ID.String(),
		ParentID:    c.ParentID.String(),
		AuthorXPK:   hex.EncodeToString(c.AuthorXPK[:]),
		Body:        c.Body,
		URL:         c.URL,
		Lang:        c.Lang,
		ReplyQuote:  c.ReplyQuote,
		BlockHeight: c.Height,
		BlockTime:   c.BlockTime,
		Points:      c.Points,
		Score:       c.Score,
	}
}

// authorHex hex-encodes an x-only pubkey, returning "" for the zero key (a story
// with no comments has no derived author).
func authorHex(xpk codec.XOnlyPubKey) string {
	if xpk == (codec.XOnlyPubKey{}) {
		return ""
	}
	return hex.EncodeToString(xpk[:])
}

func toTopic(t store.Topic) topicDTO {
	return topicDTO{
		Topic:         hex.EncodeToString(t.Topic[:]),
		Name:          t.Name,
		StoryCount:    t.StoryCount,
		RetentionDays: int(t.RetentionDays),
		CreatedHeight: t.CreatedHeight,
		TxID:          t.TxID,
	}
}

func feedResponse(items []store.FeedItem) map[string]any {
	out := make([]itemDTO, 0, len(items))
	for _, f := range items {
		out = append(out, toItem(f))
	}
	return map[string]any{"items": out}
}

func threadResponse(root interface{ String() string }, comments []store.ThreadComment) map[string]any {
	out := make([]commentDTO, 0, len(comments))
	for _, c := range comments {
		out = append(out, toComment(c))
	}
	return map[string]any{"root_id": root.String(), "comments": out}
}
