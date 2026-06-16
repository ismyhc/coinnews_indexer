package store

import (
	"context"
	"testing"

	"github.com/ismyhc/coinnews-indexer/codec"
)

// backends returns the Store implementations every test runs against.
func backends(t *testing.T) map[string]Store {
	t.Helper()
	sq, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sq.Close() })
	return map[string]Store{
		"memory": NewMemory(),
		"sqlite": sq,
	}
}

func id(b byte) codec.ItemID {
	var x codec.ItemID
	x[0] = b
	return x
}

func topic(b byte) codec.Topic { return codec.Topic{b, b, b, b} }

func author(b byte) codec.XOnlyPubKey {
	var x codec.XOnlyPubKey
	x[0] = b
	return x
}

func eachBackend(t *testing.T, fn func(t *testing.T, s Store)) {
	for name, s := range backends(t) {
		t.Run(name, func(t *testing.T) { fn(t, s) })
	}
}

func TestCursorRoundTrip(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if _, _, ok, _ := s.LoadCursor(ctx); ok {
			t.Fatal("expected no cursor initially")
		}
		if err := s.SaveCursor(ctx, 42, "deadbeef"); err != nil {
			t.Fatal(err)
		}
		h, hash, ok, err := s.LoadCursor(ctx)
		if err != nil || !ok || h != 42 || hash != "deadbeef" {
			t.Fatalf("cursor: h=%d hash=%s ok=%v err=%v", h, hash, ok, err)
		}
	})
}

func TestTopicFirstWins(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		ins, err := s.InsertTopicIfAbsent(ctx, Topic{Topic: topic(1), Name: "first", CreatedHeight: 10, TxID: "a"})
		if err != nil || !ins {
			t.Fatalf("first insert: ins=%v err=%v", ins, err)
		}
		ins, err = s.InsertTopicIfAbsent(ctx, Topic{Topic: topic(1), Name: "second", CreatedHeight: 11, TxID: "b"})
		if err != nil || ins {
			t.Fatalf("collision should not insert: ins=%v err=%v", ins, err)
		}
		topics, _ := s.ListTopics(ctx)
		if len(topics) != 1 || topics[0].Name != "first" {
			t.Fatalf("expected first-wins, got %+v", topics)
		}
	})
}

func TestVoteLatestWins(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		target := id(7)
		a := author(3)
		// Upvote first...
		mustVote(t, s, Vote{TargetID: target, AuthorXPK: a, Kind: codec.TypeUpvote, Height: 100, TxIndex: 1})
		// ...then a later downvote supersedes it.
		mustVote(t, s, Vote{TargetID: target, AuthorXPK: a, Kind: codec.TypeDownvote, Height: 101, TxIndex: 0})
		// An out-of-order older upvote must NOT win.
		mustVote(t, s, Vote{TargetID: target, AuthorXPK: a, Kind: codec.TypeUpvote, Height: 99, TxIndex: 0})

		seedStory(t, s, target, 100)
		f, ok, err := s.GetItem(ctx, target)
		if err != nil || !ok {
			t.Fatalf("get item: ok=%v err=%v", ok, err)
		}
		if f.Points != -1 {
			t.Fatalf("expected net -1 (latest downvote), got %d", f.Points)
		}
	})
}

func TestFrontPageRankingAndNSFW(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := int64(1_000_000_000)
		// Two stories same age; A has more upvotes so should rank first.
		seedStoryAt(t, s, id(1), topic(9), "A", 1000, now-3600, false)
		seedStoryAt(t, s, id(2), topic(9), "B", 1001, now-3600, false)
		seedStoryAt(t, s, id(3), topic(9), "NSFW", 1002, now-3600, true)
		for i := 0; i < 5; i++ {
			mustVote(t, s, Vote{TargetID: id(1), AuthorXPK: author(byte(i)), Kind: codec.TypeUpvote, Height: 1100, TxIndex: i})
		}
		mustVote(t, s, Vote{TargetID: id(2), AuthorXPK: author(20), Kind: codec.TypeUpvote, Height: 1100})

		fp, err := s.FrontPage(ctx, FeedQuery{NowUnix: now})
		if err != nil {
			t.Fatal(err)
		}
		if len(fp) != 2 { // NSFW excluded by default
			t.Fatalf("expected 2 SFW stories, got %d", len(fp))
		}
		if fp[0].Headline != "A" {
			t.Fatalf("expected A ranked first, got %q", fp[0].Headline)
		}
		// Including NSFW yields all three.
		fp, _ = s.FrontPage(ctx, FeedQuery{NowUnix: now, IncludeNSFW: true})
		if len(fp) != 3 {
			t.Fatalf("expected 3 with NSFW, got %d", len(fp))
		}
	})
}

func TestThreadAndByAuthor(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		root := id(50)
		seedStory(t, s, root, 10)
		// c1 replies to story; c2 replies to c1 (nested).
		seedComment(t, s, id(51), root, author(1), 11)
		seedComment(t, s, id(52), id(51), author(2), 12)
		// unrelated comment on another item
		seedComment(t, s, id(53), id(99), author(1), 13)

		th, err := s.Thread(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		if len(th) != 2 {
			t.Fatalf("expected 2 comments in thread (nested included), got %d", len(th))
		}
		mine, _ := s.ByAuthor(ctx, author(1), 0, 0)
		if len(mine) != 2 { // c1 and c3
			t.Fatalf("expected 2 comments by author 1, got %d", len(mine))
		}
	})
}

func TestOnlyCreatedTopicsListed(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		// One created topic (with a story), one story in an UNcreated topic,
		// and one story in the reserved zero topic.
		if _, err := s.InsertTopicIfAbsent(ctx, Topic{Topic: topic(7), Name: "Named", CreatedHeight: 9, TxID: "t"}); err != nil {
			t.Fatal(err)
		}
		seedStoryAt(t, s, id(1), topic(7), "InNamed", 10, 10, false)
		seedStoryAt(t, s, id(2), topic(8), "Uncreated", 11, 11, false)
		seedStoryAt(t, s, id(3), codec.ZeroTopic, "Global", 12, 12, false)

		topics, _ := s.ListTopics(ctx)
		if len(topics) != 1 {
			t.Fatalf("expected only the created topic listed, got %d: %+v", len(topics), topics)
		}
		if topics[0].Name != "Named" || topics[0].StoryCount != 1 {
			t.Fatalf("expected Named with story_count 1, got %+v", topics[0])
		}
		// Uncreated and zero-topic stories are still in the feed and queryable by topic.
		fp, _ := s.FrontPage(ctx, FeedQuery{IncludeNSFW: true})
		if len(fp) != 3 {
			t.Fatalf("expected all 3 stories in feed, got %d", len(fp))
		}
		if uncreated, _ := s.ByTopic(ctx, topic(8), 0, 0); len(uncreated) != 1 {
			t.Fatalf("expected 1 story when querying the uncreated topic, got %d", len(uncreated))
		}
	})
}

func TestFirstCommentAuthorship(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		story := id(1)
		seedStory(t, s, story, 10)
		// Two comments by different authors; author(5)'s is earlier in canonical order.
		seedComment(t, s, id(2), story, author(5), 11) // first
		seedComment(t, s, id(3), story, author(9), 12) // later

		// The story's author is the earliest comment's author.
		f, ok, _ := s.GetItem(ctx, story)
		if !ok || f.AuthorXPK != author(5) {
			t.Fatalf("expected author(5) from first comment, got %x (ok=%v)", f.AuthorXPK, ok)
		}
		// ItemsByAuthor attributes the story to author(5), not author(9).
		mine, _ := s.ItemsByAuthor(ctx, author(5), 0, 0)
		if len(mine) != 1 || mine[0].ID != story {
			t.Fatalf("expected story attributed to author(5), got %+v", mine)
		}
		if others, _ := s.ItemsByAuthor(ctx, author(9), 0, 0); len(others) != 0 {
			t.Fatalf("author(9) only commented, should author nothing, got %d", len(others))
		}
	})
}

func TestReorgRewind(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		seedStory(t, s, id(1), 100)
		seedStory(t, s, id(2), 200)
		mustVote(t, s, Vote{TargetID: id(1), AuthorXPK: author(1), Kind: codec.TypeUpvote, Height: 200})

		if err := s.DeleteFromHeight(ctx, 150); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.GetItem(ctx, id(2)); ok {
			t.Fatal("item at height 200 should be gone after rewind to 150")
		}
		if _, ok, _ := s.GetItem(ctx, id(1)); !ok {
			t.Fatal("item at height 100 should survive rewind to 150")
		}
		f, _, _ := s.GetItem(ctx, id(1))
		if f.Points != 0 {
			t.Fatalf("vote at height 200 should be rewound, points=%d", f.Points)
		}
	})
}

// --- seed helpers ---

func mustVote(t *testing.T, s Store, v Vote) {
	t.Helper()
	if err := s.PutVote(context.Background(), v); err != nil {
		t.Fatal(err)
	}
}

func seedStory(t *testing.T, s Store, itemID codec.ItemID, height int64) {
	seedStoryAt(t, s, itemID, topic(1), "h", height, height, false)
}

func seedStoryAt(t *testing.T, s Store, itemID codec.ItemID, tp codec.Topic, headline string, height, blockTime int64, nsfw bool) {
	t.Helper()
	ctx := context.Background()
	if err := s.PutItem(ctx, Item{ID: itemID, TxID: itemID.String(), Vout: 0, Height: height, BlockTime: blockTime, TypeTag: codec.TypeStory}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutStory(ctx, Story{ID: itemID, Topic: tp, Headline: headline, NSFW: nsfw}); err != nil {
		t.Fatal(err)
	}
}

func seedComment(t *testing.T, s Store, itemID, parent codec.ItemID, a codec.XOnlyPubKey, height int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.PutItem(ctx, Item{ID: itemID, TxID: itemID.String(), Vout: 0, Height: height, BlockTime: height, TypeTag: codec.TypeComment}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutComment(ctx, Comment{ID: itemID, ParentID: parent, AuthorXPK: a, Body: "hi"}); err != nil {
		t.Fatal(err)
	}
}
