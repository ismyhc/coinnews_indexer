package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ismyhc/coinnews-indexer/codec"
	"github.com/ismyhc/coinnews-indexer/store"
)

func hexBytes(b []byte) string { return hex.EncodeToString(b) }

// This file makes the indexer a drop-in for the reference CoinNews server's
// ConnectRPC service (coinnews.v1.CoinNewsService) WITHOUT any protobuf/codegen.
// The Connect protocol's unary calls are just `POST /<pkg>.<Service>/<Method>`
// with a JSON request body and a JSON response body, so we match it by hand:
// the proto's lowerCamelCase field names, enum-name strings, and RFC3339
// timestamps. Existing Connect clients can point straight at these routes.

const connectPrefix = "/coinnews.v1.CoinNewsService/"

func (s *Server) connectRoutes() {
	s.mux.HandleFunc("POST "+connectPrefix+"ListFrontPage", s.connectFrontPage)
	s.mux.HandleFunc("POST "+connectPrefix+"ListNewFeed", s.connectNewFeed)
	s.mux.HandleFunc("POST "+connectPrefix+"GetItem", s.connectGetItem)
	s.mux.HandleFunc("POST "+connectPrefix+"ListThread", s.connectThread)
	s.mux.HandleFunc("POST "+connectPrefix+"ListByAuthor", s.connectByAuthor)
	s.mux.HandleFunc("POST "+connectPrefix+"ListByTopic", s.connectByTopic)
	s.mux.HandleFunc("POST "+connectPrefix+"ListTopics", s.connectListTopics)
}

const connectDefaultLimit = 50

// --- wire types (protobuf JSON: lowerCamelCase, enum names, RFC3339 time) ---

type connectItem struct {
	ItemIDHex    string  `json:"itemIdHex"`
	TopicHex     string  `json:"topicHex"`
	Headline     string  `json:"headline"`
	URL          string  `json:"url"`
	Body         string  `json:"body"`
	Subtype      string  `json:"subtype"` // enum name, e.g. "SUBTYPE_LINK"
	Lang         string  `json:"lang"`
	NSFW         bool    `json:"nsfw"`
	AuthorXPKHex string  `json:"authorXpkHex"`
	BlockHeight  uint32  `json:"blockHeight"`
	BlockTime    string  `json:"blockTime"` // RFC3339
	Points       int32   `json:"points"`
	CommentCount int32   `json:"commentCount"`
	Score        float64 `json:"score"`
}

type connectComment struct {
	ItemIDHex    string  `json:"itemIdHex"`
	ParentIDHex  string  `json:"parentIdHex"`
	AuthorXPKHex string  `json:"authorXpkHex"`
	Body         string  `json:"body"`
	URL          string  `json:"url"`
	Lang         string  `json:"lang"`
	ReplyQuote   string  `json:"replyQuote"`
	BlockHeight  uint32  `json:"blockHeight"`
	BlockTime    string  `json:"blockTime"`
	Points       int32   `json:"points"`
	Score        float64 `json:"score"`
}

type connectTopic struct {
	TopicHex      string `json:"topicHex"`
	Name          string `json:"name"`
	RetentionDays uint32 `json:"retentionDays"`
	CreatedHeight uint32 `json:"createdHeight"`
	TxID          string `json:"txid"`
}

func toConnectItem(f store.FeedItem) connectItem {
	return connectItem{
		ItemIDHex:    f.ID.String(),
		TopicHex:     hexBytes(f.Topic[:]),
		Headline:     f.Headline,
		URL:          f.URL,
		Body:         f.Body,
		Subtype:      subtypeName(f.Subtype),
		Lang:         f.Lang,
		NSFW:         f.NSFW,
		AuthorXPKHex: authorHex(f.AuthorXPK), // derived from the story's first comment
		BlockHeight:  uint32(f.Height),
		BlockTime:    rfc3339(f.BlockTime),
		Points:       int32(f.Points),
		CommentCount: int32(f.CommentCount),
		Score:        f.Score,
	}
}

func toConnectComment(c store.ThreadComment) connectComment {
	return connectComment{
		ItemIDHex:    c.ID.String(),
		ParentIDHex:  c.ParentID.String(),
		AuthorXPKHex: hexBytes(c.AuthorXPK[:]),
		Body:         c.Body,
		URL:          c.URL,
		Lang:         c.Lang,
		ReplyQuote:   c.ReplyQuote,
		BlockHeight:  uint32(c.Height),
		BlockTime:    rfc3339(c.BlockTime),
		Points:       int32(c.Points),
		Score:        c.Score,
	}
}

func toConnectTopic(t store.Topic) connectTopic {
	return connectTopic{
		TopicHex:      hexBytes(t.Topic[:]),
		Name:          t.Name,
		RetentionDays: uint32(t.RetentionDays),
		CreatedHeight: uint32(t.CreatedHeight),
		TxID:          t.TxID,
	}
}

// --- requests ---

type connectFeedReq struct {
	Limit    uint32          `json:"limit"`
	Offset   uint32          `json:"offset"`
	Subtype  *connectSubtype `json:"subtype"`
	TopicHex *string         `json:"topicHex"`
}

func (r connectFeedReq) toQuery() store.FeedQuery {
	q := store.FeedQuery{
		Limit:       limitOrDefault(r.Limit),
		Offset:      int(r.Offset),
		IncludeNSFW: true, // Connect clients filter NSFW themselves; mirror the feed verbatim
	}
	if r.Subtype != nil {
		st := r.Subtype.val
		q.Subtype = &st
	}
	if r.TopicHex != nil && *r.TopicHex != "" {
		if t, err := parseTopic(*r.TopicHex); err == nil { // invalid topic → ignore filter
			q.Topic = &t
		}
	}
	return q
}

// connectSubtype accepts either an enum name ("SUBTYPE_TEXT") or a number.
type connectSubtype struct{ val codec.Subtype }

func (s *connectSubtype) UnmarshalJSON(b []byte) error {
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		s.val = codec.Subtype(n)
		return nil
	}
	var name string
	if err := json.Unmarshal(b, &name); err == nil {
		s.val = subtypeFromName(name)
	}
	return nil
}

// --- handlers ---

func (s *Server) connectFrontPage(w http.ResponseWriter, r *http.Request) {
	var req connectFeedReq
	if !decodeConnect(w, r, &req) {
		return
	}
	items, err := s.st.FrontPage(r.Context(), req.toQuery())
	if err != nil {
		connectErr(w, "internal", http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": connectItems(items)})
}

func (s *Server) connectNewFeed(w http.ResponseWriter, r *http.Request) {
	var req connectFeedReq
	if !decodeConnect(w, r, &req) {
		return
	}
	items, err := s.st.NewFeed(r.Context(), req.toQuery())
	if err != nil {
		connectErr(w, "internal", http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": connectItems(items)})
}

func (s *Server) connectGetItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ItemIDHex string `json:"itemIdHex"`
	}
	if !decodeConnect(w, r, &req) {
		return
	}
	id, err := codec.ParseItemID(req.ItemIDHex)
	if err != nil {
		connectErr(w, "invalid_argument", http.StatusBadRequest, "invalid item id")
		return
	}
	item, ok, err := s.st.GetItem(r.Context(), id)
	if err != nil {
		connectErr(w, "internal", http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		connectErr(w, "not_found", http.StatusNotFound, "item not found")
		return
	}
	ci := toConnectItem(item)
	writeJSON(w, http.StatusOK, map[string]any{"item": ci})
}

func (s *Server) connectThread(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RootIDHex string `json:"rootIdHex"`
	}
	if !decodeConnect(w, r, &req) {
		return
	}
	id, err := codec.ParseItemID(req.RootIDHex)
	if err != nil {
		connectErr(w, "invalid_argument", http.StatusBadRequest, "invalid root id")
		return
	}
	comments, err := s.st.Thread(r.Context(), id)
	if err != nil {
		connectErr(w, "internal", http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"comments": connectComments(comments)})
}

func (s *Server) connectByAuthor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AuthorXPKHex string `json:"authorXpkHex"`
		Limit        uint32 `json:"limit"`
		Offset       uint32 `json:"offset"`
	}
	if !decodeConnect(w, r, &req) {
		return
	}
	xpk, err := parseXOnly(req.AuthorXPKHex)
	if err != nil {
		connectErr(w, "invalid_argument", http.StatusBadRequest, err.Error())
		return
	}
	// Stories authored by this key (author = the story's first comment author).
	feed, err := s.st.ItemsByAuthor(r.Context(), xpk, int(limitOrDefault(req.Limit)), int(req.Offset))
	if err != nil {
		connectErr(w, "internal", http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": connectItems(feed)})
}

func (s *Server) connectByTopic(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TopicHex string `json:"topicHex"`
		Limit    uint32 `json:"limit"`
		Offset   uint32 `json:"offset"`
	}
	if !decodeConnect(w, r, &req) {
		return
	}
	topic, err := parseTopic(req.TopicHex)
	if err != nil {
		connectErr(w, "invalid_argument", http.StatusBadRequest, err.Error())
		return
	}
	items, err := s.st.ByTopic(r.Context(), topic, int(limitOrDefault(req.Limit)), int(req.Offset))
	if err != nil {
		connectErr(w, "internal", http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": connectItems(items)})
}

func (s *Server) connectListTopics(w http.ResponseWriter, r *http.Request) {
	topics, err := s.st.ListTopics(r.Context())
	if err != nil {
		connectErr(w, "internal", http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]connectTopic, 0, len(topics))
	for _, t := range topics {
		out = append(out, toConnectTopic(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"topics": out})
}

// --- helpers ---

func connectItems(items []store.FeedItem) []connectItem {
	out := make([]connectItem, 0, len(items))
	for _, f := range items {
		out = append(out, toConnectItem(f))
	}
	return out
}

func connectComments(cs []store.ThreadComment) []connectComment {
	out := make([]connectComment, 0, len(cs))
	for _, c := range cs {
		out = append(out, toConnectComment(c))
	}
	return out
}

// decodeConnect reads a JSON request body (an empty body is treated as {}).
func decodeConnect(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength == 0 {
		return true
	}
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		connectErr(w, "invalid_argument", http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// connectErr writes a Connect-style error: HTTP status + {"code","message"}.
func connectErr(w http.ResponseWriter, code string, status int, msg string) {
	writeJSON(w, status, map[string]string{"code": code, "message": msg})
}

func limitOrDefault(n uint32) int {
	if n == 0 {
		return connectDefaultLimit
	}
	return int(n)
}

func pageConnect(items []connectItem, limit, offset int) []connectItem {
	if offset >= len(items) {
		return []connectItem{}
	}
	items = items[offset:]
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}
	return items
}

var subtypeNames = []string{
	"SUBTYPE_LINK", "SUBTYPE_TEXT", "SUBTYPE_ASK", "SUBTYPE_SHOW", "SUBTYPE_POLL", "SUBTYPE_JOB",
}

func subtypeName(s codec.Subtype) string {
	if int(s) < len(subtypeNames) {
		return subtypeNames[s]
	}
	return subtypeNames[0]
}

func subtypeFromName(name string) codec.Subtype {
	for i, n := range subtypeNames {
		if n == name {
			return codec.Subtype(i)
		}
	}
	return 0
}

func rfc3339(unix int64) string {
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}
