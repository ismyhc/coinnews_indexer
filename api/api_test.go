package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ismyhc/coinnews-indexer/codec"
	"github.com/ismyhc/coinnews-indexer/store"
)

func id(b byte) codec.ItemID {
	var x codec.ItemID
	x[0] = b
	return x
}

func seed(t *testing.T, s *store.MemoryStore) {
	t.Helper()
	ctx := context.Background()
	tp := codec.Topic{1, 2, 3, 4}
	mustErr(t, s.PutItem(ctx, store.Item{ID: id(1), TxID: "tx1", Height: 10, BlockTime: 6000, TypeTag: codec.TypeStory}))
	mustErr(t, s.PutStory(ctx, store.Story{ID: id(1), Topic: tp, Headline: "First", Subtype: codec.SubtypeLink}))
	mustErr(t, s.PutItem(ctx, store.Item{ID: id(2), TxID: "tx2", Height: 11, BlockTime: 6600, TypeTag: codec.TypeStory}))
	mustErr(t, s.PutStory(ctx, store.Story{ID: id(2), Topic: tp, Headline: "Second"}))
	_, err := s.InsertTopicIfAbsent(ctx, store.Topic{Topic: tp, Name: "general", CreatedHeight: 9, TxID: "tx0"})
	mustErr(t, err)
	// A comment on story 1.
	var author codec.XOnlyPubKey
	author[0] = 0xaa
	mustErr(t, s.PutItem(ctx, store.Item{ID: id(3), TxID: "tx3", Height: 12, BlockTime: 7200, TypeTag: codec.TypeComment}))
	mustErr(t, s.PutComment(ctx, store.Comment{ID: id(3), ParentID: id(1), AuthorXPK: author, Body: "hi"}))
}

func mustErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, h http.Handler, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	h.ServeHTTP(rec, req)
	var body map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec.Code, body
}

func TestFrontPageAndNewFeed(t *testing.T) {
	s := store.NewMemory()
	seed(t, s)
	srv := New(s)

	code, body := get(t, srv, "/v1/frontpage")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 stories, got %d", len(items))
	}

	code, body = get(t, srv, "/v1/new")
	items, _ = body["items"].([]any)
	first := items[0].(map[string]any)
	if first["headline"] != "Second" { // newest first
		t.Fatalf("new feed not newest-first: %v", first["headline"])
	}
}

func TestGetItemAndThread(t *testing.T) {
	s := store.NewMemory()
	seed(t, s)
	srv := New(s)

	code, body := get(t, srv, "/v1/items/"+id(1).String())
	if code != http.StatusOK || body["headline"] != "First" {
		t.Fatalf("get item: code=%d body=%v", code, body)
	}

	code, body = get(t, srv, "/v1/items/"+id(1).String()+"/thread")
	if code != http.StatusOK {
		t.Fatalf("thread status %d", code)
	}
	comments, _ := body["comments"].([]any)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment in thread, got %d", len(comments))
	}

	// Unknown item -> 404.
	if code, _ := get(t, srv, "/v1/items/"+id(99).String()); code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown item, got %d", code)
	}
	// Malformed id -> 400.
	if code, _ := get(t, srv, "/v1/items/nothex"); code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad id, got %d", code)
	}
}

func TestTopicsAndByTopic(t *testing.T) {
	s := store.NewMemory()
	seed(t, s)
	srv := New(s)

	_, body := get(t, srv, "/v1/topics")
	topics, _ := body["topics"].([]any)
	if len(topics) != 1 {
		t.Fatalf("expected 1 topic, got %d", len(topics))
	}

	topicHex := hex.EncodeToString([]byte{1, 2, 3, 4})
	code, body := get(t, srv, "/v1/topics/"+topicHex+"/items")
	if code != http.StatusOK {
		t.Fatalf("by-topic status %d", code)
	}
	if items, _ := body["items"].([]any); len(items) != 2 {
		t.Fatalf("expected 2 stories in topic, got %d", len(items))
	}

	// Bad topic length -> 400.
	if code, _ := get(t, srv, "/v1/topics/aabb/items"); code != http.StatusBadRequest {
		t.Fatalf("expected 400 for short topic, got %d", code)
	}
}

func TestDocsRoutes(t *testing.T) {
	srv := New(store.NewMemory())

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/openapi.yaml status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "openapi: 3.1.0") {
		t.Fatal("/openapi.yaml did not serve the spec")
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "swagger-ui") {
		t.Fatalf("/docs did not serve Swagger UI (status %d)", rec.Code)
	}
}

func postJSON(t *testing.T, h http.Handler, path, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

func TestConnectEndpoints(t *testing.T) {
	s := store.NewMemory()
	seed(t, s)
	srv := New(s)

	// ListTopics (Empty request body).
	code, body := postJSON(t, srv, "/coinnews.v1.CoinNewsService/ListTopics", "{}")
	if code != http.StatusOK {
		t.Fatalf("ListTopics status %d", code)
	}
	topics, _ := body["topics"].([]any)
	if len(topics) != 1 {
		t.Fatalf("expected 1 topic, got %d", len(topics))
	}

	// ListFrontPage returns Connect-shaped items (camelCase, enum-name subtype).
	code, body = postJSON(t, srv, "/coinnews.v1.CoinNewsService/ListFrontPage", `{"limit":10}`)
	if code != http.StatusOK {
		t.Fatalf("ListFrontPage status %d", code)
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	first := items[0].(map[string]any)
	if _, ok := first["itemIdHex"]; !ok {
		t.Fatalf("expected camelCase itemIdHex field, got %v", first)
	}
	if first["subtype"] != "SUBTYPE_LINK" {
		t.Fatalf("expected enum-name subtype, got %v", first["subtype"])
	}
	if _, ok := first["blockTime"].(string); !ok {
		t.Fatalf("expected RFC3339 string blockTime, got %v", first["blockTime"])
	}

	// GetItem on a missing id → Connect not_found.
	code, body = postJSON(t, srv, "/coinnews.v1.CoinNewsService/GetItem",
		`{"itemIdHex":"`+id(99).String()+`"}`)
	if code != http.StatusNotFound || body["code"] != "not_found" {
		t.Fatalf("expected not_found, got status=%d body=%v", code, body)
	}
}

func TestByAuthor(t *testing.T) {
	s := store.NewMemory()
	seed(t, s)
	srv := New(s)

	author := make([]byte, 32)
	author[0] = 0xaa
	code, body := get(t, srv, "/v1/authors/"+hex.EncodeToString(author)+"/comments")
	if code != http.StatusOK {
		t.Fatalf("by-author status %d", code)
	}
	if comments, _ := body["comments"].([]any); len(comments) != 1 {
		t.Fatalf("expected 1 comment by author, got %d", len(comments))
	}
}
