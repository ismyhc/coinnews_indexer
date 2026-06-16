// Package api serves the CoinNews indexer's read endpoints as JSON over HTTP,
// using only the standard library. All ids/keys are exchanged as hex strings.
package api

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/ismyhc/coinnews-indexer/codec"
	"github.com/ismyhc/coinnews-indexer/store"
)

// Server adapts a store.Store to an http.Handler.
type Server struct {
	st  store.Store
	mux *http.ServeMux
}

// New builds a Server with its routes registered.
func New(st store.Store) *Server {
	s := &Server{st: st, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /v1/frontpage", s.handleFrontPage)
	s.mux.HandleFunc("GET /v1/new", s.handleNewFeed)
	s.mux.HandleFunc("GET /v1/items/{id}", s.handleGetItem)
	s.mux.HandleFunc("GET /v1/items/{id}/thread", s.handleThread)
	s.mux.HandleFunc("GET /v1/authors/{xpk}/comments", s.handleByAuthor)
	s.mux.HandleFunc("GET /v1/topics", s.handleTopics)
	s.mux.HandleFunc("GET /v1/topics/{topic}/items", s.handleByTopic)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	// API docs.
	s.mux.HandleFunc("GET /openapi.yaml", s.handleOpenAPI)
	s.mux.HandleFunc("GET /docs", s.handleDocs)
	// ConnectRPC-compatible endpoints (drop-in for coinnews.v1.CoinNewsService),
	// mounted alongside the REST API above.
	s.connectRoutes()
}

// --- handlers ---

func (s *Server) handleFrontPage(w http.ResponseWriter, r *http.Request) {
	q, err := feedQuery(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	items, err := s.st.FrontPage(r.Context(), q)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, feedResponse(items))
}

func (s *Server) handleNewFeed(w http.ResponseWriter, r *http.Request) {
	q, err := feedQuery(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	items, err := s.st.NewFeed(r.Context(), q)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, feedResponse(items))
}

func (s *Server) handleGetItem(w http.ResponseWriter, r *http.Request) {
	id, err := codec.ParseItemID(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid item id"))
		return
	}
	item, ok, err := s.st.GetItem(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("item not found"))
		return
	}
	writeJSON(w, http.StatusOK, toItem(item))
}

func (s *Server) handleThread(w http.ResponseWriter, r *http.Request) {
	id, err := codec.ParseItemID(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid item id"))
		return
	}
	comments, err := s.st.Thread(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, threadResponse(id, comments))
}

func (s *Server) handleByAuthor(w http.ResponseWriter, r *http.Request) {
	xpk, err := parseXOnly(r.PathValue("xpk"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	limit, offset := pageParams(r)
	comments, err := s.st.ByAuthor(r.Context(), xpk, limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]commentDTO, 0, len(comments))
	for _, c := range comments {
		out = append(out, toComment(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"comments": out})
}

func (s *Server) handleTopics(w http.ResponseWriter, r *http.Request) {
	topics, err := s.st.ListTopics(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]topicDTO, 0, len(topics))
	for _, t := range topics {
		out = append(out, toTopic(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"topics": out})
}

func (s *Server) handleByTopic(w http.ResponseWriter, r *http.Request) {
	topic, err := parseTopic(r.PathValue("topic"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	limit, offset := pageParams(r)
	items, err := s.st.ByTopic(r.Context(), topic, limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, feedResponse(items))
}

// --- request parsing ---

func feedQuery(r *http.Request) (store.FeedQuery, error) {
	limit, offset := pageParams(r)
	q := store.FeedQuery{Limit: limit, Offset: offset, IncludeNSFW: boolParam(r, "nsfw")}

	if v := r.URL.Query().Get("topic"); v != "" {
		t, err := parseTopic(v)
		if err != nil {
			return q, err
		}
		q.Topic = &t
	}
	if v := r.URL.Query().Get("subtype"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 0xff {
			return q, errors.New("invalid subtype")
		}
		st := codec.Subtype(n)
		q.Subtype = &st
	}
	return q, nil
}

func pageParams(r *http.Request) (limit, offset int) {
	limit = atoiDefault(r.URL.Query().Get("limit"), 50)
	offset = atoiDefault(r.URL.Query().Get("offset"), 0)
	if limit < 0 {
		limit = 0
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func boolParam(r *http.Request, key string) bool {
	switch r.URL.Query().Get(key) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func parseTopic(s string) (codec.Topic, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != codec.TopicLen {
		return codec.Topic{}, errors.New("invalid topic (want 4-byte hex)")
	}
	var t codec.Topic
	copy(t[:], b)
	return t, nil
}

func parseXOnly(s string) (codec.XOnlyPubKey, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != codec.XOnlyPubKeyLen {
		return codec.XOnlyPubKey{}, errors.New("invalid author pubkey (want 32-byte hex)")
	}
	var x codec.XOnlyPubKey
	copy(x[:], b)
	return x, nil
}

// --- responses ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
