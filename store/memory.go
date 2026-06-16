package store

import (
	"context"
	"sync"

	"github.com/ismyhc/coinnews-indexer/codec"
)

// MemoryStore is an in-memory Store backend for tests and ephemeral use. It
// mirrors the SQLite backend's semantics (first-topic-wins, latest-vote-wins,
// reorg rewind) but keeps everything in maps guarded by a single mutex.
type MemoryStore struct {
	mu sync.RWMutex

	items   map[codec.ItemID]Item
	byOut   map[string]codec.ItemID // "txid:vout" -> id
	topics  map[codec.Topic]Topic
	stories map[codec.ItemID]Story
	comments map[codec.ItemID]Comment
	votes   map[voteKey]Vote
	conts   map[codec.ItemID]map[byte]Continuation

	cursorH  int64
	cursorHs string
	hasCur   bool
}

type voteKey struct {
	target codec.ItemID
	author codec.XOnlyPubKey
}

var _ Store = (*MemoryStore)(nil)

// NewMemory returns an empty in-memory store.
func NewMemory() *MemoryStore {
	return &MemoryStore{
		items:    map[codec.ItemID]Item{},
		byOut:    map[string]codec.ItemID{},
		topics:   map[codec.Topic]Topic{},
		stories:  map[codec.ItemID]Story{},
		comments: map[codec.ItemID]Comment{},
		votes:    map[voteKey]Vote{},
		conts:    map[codec.ItemID]map[byte]Continuation{},
	}
}

func (m *MemoryStore) Close() error { return nil }

func outKey(txid string, vout uint32) string {
	return txid + ":" + itoa(vout)
}

func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

func (m *MemoryStore) SaveCursor(_ context.Context, height int64, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursorH, m.cursorHs, m.hasCur = height, hash, true
	return nil
}

func (m *MemoryStore) LoadCursor(context.Context) (int64, string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cursorH, m.cursorHs, m.hasCur, nil
}

func (m *MemoryStore) DeleteFromHeight(_ context.Context, height int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, it := range m.items {
		if it.Height >= height {
			delete(m.items, id)
			delete(m.byOut, outKey(it.TxID, it.Vout))
			delete(m.stories, id)
			delete(m.comments, id)
			delete(m.conts, id)
		}
	}
	for k, v := range m.votes {
		if v.Height >= height {
			delete(m.votes, k)
		}
	}
	for t, top := range m.topics {
		if top.CreatedHeight >= height {
			delete(m.topics, t)
		}
	}
	return nil
}

func (m *MemoryStore) PutItem(_ context.Context, it Item) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[it.ID]; ok {
		return nil
	}
	m.items[it.ID] = it
	m.byOut[outKey(it.TxID, it.Vout)] = it.ID
	return nil
}

func (m *MemoryStore) InsertTopicIfAbsent(_ context.Context, t Topic) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.topics[t.Topic]; ok {
		return false, nil
	}
	m.topics[t.Topic] = t
	return true, nil
}

func (m *MemoryStore) PutStory(_ context.Context, s Story) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.stories[s.ID]; !ok {
		m.stories[s.ID] = s
	}
	return nil
}

func (m *MemoryStore) PutComment(_ context.Context, c Comment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.comments[c.ID]; !ok {
		m.comments[c.ID] = c
	}
	return nil
}

func (m *MemoryStore) PutVote(_ context.Context, v Vote) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := voteKey{v.TargetID, v.AuthorXPK}
	if cur, ok := m.votes[k]; ok && !laterThan(v, cur) {
		return nil // keep the newer existing vote
	}
	m.votes[k] = v
	return nil
}

// laterThan reports whether a comes after b in canonical scan order.
func laterThan(a, b Vote) bool {
	if a.Height != b.Height {
		return a.Height > b.Height
	}
	if a.TxIndex != b.TxIndex {
		return a.TxIndex > b.TxIndex
	}
	return a.VoutIndex > b.VoutIndex
}

func (m *MemoryStore) PutContinuation(_ context.Context, c Continuation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	seqs := m.conts[c.HeadID]
	if seqs == nil {
		seqs = map[byte]Continuation{}
		m.conts[c.HeadID] = seqs
	}
	if _, ok := seqs[c.Seq]; !ok {
		seqs[c.Seq] = c
	}
	return nil
}

func (m *MemoryStore) ItemByOutpoint(_ context.Context, txid string, vout uint32) (Item, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.byOut[outKey(txid, vout)]
	if !ok {
		return Item{}, false, nil
	}
	return m.items[id], true, nil
}

func (m *MemoryStore) ItemByID(_ context.Context, id codec.ItemID) (Item, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	it, ok := m.items[id]
	return it, ok, nil
}

// tally returns upvotes-downvotes for a target.
func (m *MemoryStore) tally(target codec.ItemID) int {
	pts := 0
	for k, v := range m.votes {
		if k.target != target {
			continue
		}
		switch v.Kind {
		case codec.TypeUpvote:
			pts++
		case codec.TypeDownvote:
			pts--
		}
	}
	return pts
}

func (m *MemoryStore) commentCount(parent codec.ItemID) int {
	n := 0
	for _, c := range m.comments {
		if c.ParentID == parent {
			n++
		}
	}
	return n
}

// firstCommentAuthor returns the author of the earliest direct comment on parent
// (canonical order), or the zero key if it has no comments.
func (m *MemoryStore) firstCommentAuthor(parent codec.ItemID) codec.XOnlyPubKey {
	var author codec.XOnlyPubKey
	var best Item
	found := false
	for _, c := range m.comments {
		if c.ParentID != parent {
			continue
		}
		it := m.items[c.ID]
		if !found || itemBefore(it, best) {
			best = it
			author = c.AuthorXPK
			found = true
		}
	}
	return author
}

// itemBefore reports whether a precedes b in canonical scan order.
func itemBefore(a, b Item) bool {
	if a.Height != b.Height {
		return a.Height < b.Height
	}
	if a.TxIndex != b.TxIndex {
		return a.TxIndex < b.TxIndex
	}
	return a.VoutIndex < b.VoutIndex
}

func (m *MemoryStore) buildFeed(now int64) []FeedItem {
	out := make([]FeedItem, 0, len(m.stories))
	for id, s := range m.stories {
		it, ok := m.items[id]
		if !ok {
			continue
		}
		pts := m.tally(id)
		f := FeedItem{
			Item:         it,
			Topic:        s.Topic,
			Headline:     s.Headline,
			URL:          s.URL,
			Body:         s.Body,
			Lang:         s.Lang,
			Subtype:      s.Subtype,
			NSFW:         s.NSFW,
			AuthorXPK:    m.firstCommentAuthor(id),
			Points:       pts,
			CommentCount: m.commentCount(id),
			Score:        rankScore(pts, now, it.BlockTime),
		}
		out = append(out, f)
	}
	return out
}

func (m *MemoryStore) filteredFeed(q FeedQuery) []FeedItem {
	now := resolveNow(q.NowUnix)
	all := m.buildFeed(now)
	out := all[:0]
	for _, f := range all {
		if q.Topic != nil && f.Topic != *q.Topic {
			continue
		}
		if q.Subtype != nil && f.Subtype != *q.Subtype {
			continue
		}
		if !q.IncludeNSFW && f.NSFW {
			continue
		}
		out = append(out, f)
	}
	return out
}

func (m *MemoryStore) FrontPage(_ context.Context, q FeedQuery) ([]FeedItem, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := m.filteredFeed(q)
	sortByScore(items)
	return page(items, q.Limit, q.Offset), nil
}

func (m *MemoryStore) NewFeed(_ context.Context, q FeedQuery) ([]FeedItem, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := m.filteredFeed(q)
	sortByNewest(items)
	return page(items, q.Limit, q.Offset), nil
}

func (m *MemoryStore) ByTopic(ctx context.Context, topic codec.Topic, limit, offset int) ([]FeedItem, error) {
	return m.NewFeed(ctx, FeedQuery{Topic: &topic, IncludeNSFW: true, Limit: limit, Offset: offset})
}

func (m *MemoryStore) GetItem(_ context.Context, id codec.ItemID) (FeedItem, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.stories[id]
	if !ok {
		return FeedItem{}, false, nil
	}
	it := m.items[id]
	pts := m.tally(id)
	return FeedItem{
		Item: it, Topic: s.Topic, Headline: s.Headline, URL: s.URL, Body: s.Body, Lang: s.Lang,
		Subtype: s.Subtype, NSFW: s.NSFW, AuthorXPK: m.firstCommentAuthor(id),
		Points: pts, CommentCount: m.commentCount(id),
		Score: rankScore(pts, resolveNow(0), it.BlockTime),
	}, true, nil
}

func (m *MemoryStore) ItemsByAuthor(_ context.Context, author codec.XOnlyPubKey, limit, offset int) ([]FeedItem, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	all := m.buildFeed(resolveNow(0))
	var out []FeedItem
	for _, f := range all {
		if f.AuthorXPK == author {
			out = append(out, f)
		}
	}
	sortByNewest(out)
	return page(out, limit, offset), nil
}

func (m *MemoryStore) threadComment(c Comment, now int64) ThreadComment {
	it := m.items[c.ID]
	pts := m.tally(c.ID)
	return ThreadComment{
		ID: c.ID, ParentID: c.ParentID, AuthorXPK: c.AuthorXPK,
		Body: c.Body, URL: c.URL, Lang: c.Lang, ReplyQuote: c.ReplyQuote,
		Height: it.Height, BlockTime: it.BlockTime, Points: pts,
		Score: rankScore(pts, now, it.BlockTime),
	}
}

func (m *MemoryStore) Thread(_ context.Context, root codec.ItemID) ([]ThreadComment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := resolveNow(0)
	// Collect all descendants via BFS over parent links.
	want := map[codec.ItemID]bool{root: true}
	var out []ThreadComment
	for changed := true; changed; {
		changed = false
		for _, c := range m.comments {
			if want[c.ID] {
				continue
			}
			if want[c.ParentID] {
				want[c.ID] = true
				changed = true
			}
		}
	}
	for _, c := range m.comments {
		if c.ID != root && want[c.ID] {
			out = append(out, m.threadComment(c, now))
		}
	}
	sortThreadCanonical(out)
	return out, nil
}

func (m *MemoryStore) ByAuthor(_ context.Context, author codec.XOnlyPubKey, limit, offset int) ([]ThreadComment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := resolveNow(0)
	var out []ThreadComment
	for _, c := range m.comments {
		if c.AuthorXPK == author {
			out = append(out, m.threadComment(c, now))
		}
	}
	sortThreadCanonicalDesc(out)
	return page(out, limit, offset), nil
}

func (m *MemoryStore) ListTopics(_ context.Context) ([]Topic, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	byID := map[codec.Topic]*Topic{}
	for _, t := range m.topics {
		if t.Topic.IsZero() {
			continue // zero topic is reserved ("no topic") — never list
		}
		cp := t
		byID[t.Topic] = &cp
	}
	// Attach story counts for the listed (created) topics only.
	for _, s := range m.stories {
		if t, ok := byID[s.Topic]; ok {
			t.StoryCount++
		}
	}
	out := make([]Topic, 0, len(byID))
	for _, t := range byID {
		out = append(out, *t)
	}
	sortTopics(out)
	return out, nil
}
