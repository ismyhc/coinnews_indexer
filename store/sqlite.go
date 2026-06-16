package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"

	"github.com/ismyhc/coinnews-indexer/codec"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// SQLiteStore is the on-disk Store backend.
type SQLiteStore struct {
	db *sql.DB
}

var _ Store = (*SQLiteStore)(nil)

// OpenSQLite opens (or creates) a SQLite database at path and applies the schema.
// Use path ":memory:" for an ephemeral database.
//
// For file-backed databases it enables WAL mode and a busy timeout so the API
// can read concurrently with the scanner's writes (the single-process live mode)
// without hitting "database is locked". These pragmas are set via the DSN so they
// apply to every connection the pool opens.
func OpenSQLite(path string) (*SQLiteStore, error) {
	memory := path == ":memory:" || strings.HasPrefix(path, "file::memory:")
	dsn := path
	if !memory {
		dsn = "file:" + path +
			"?_pragma=journal_mode(WAL)" +
			"&_pragma=busy_timeout(5000)" +
			"&_pragma=synchronous(NORMAL)" +
			"&_pragma=foreign_keys(ON)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if memory {
		// A shared in-memory database lives on a single connection; opening more
		// would each get a fresh, empty database.
		db.SetMaxOpenConns(1)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) SaveCursor(ctx context.Context, height int64, hash string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cn_scanner_cursor(id, last_height, last_hash) VALUES (1, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET last_height=excluded.last_height, last_hash=excluded.last_hash`,
		height, hash)
	return err
}

func (s *SQLiteStore) LoadCursor(ctx context.Context) (int64, string, bool, error) {
	var h int64
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT last_height, last_hash FROM cn_scanner_cursor WHERE id=1`).Scan(&h, &hash)
	if err == sql.ErrNoRows {
		return 0, "", false, nil
	}
	return h, hash, err == nil, err
}

func (s *SQLiteStore) DeleteFromHeight(ctx context.Context, height int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`DELETE FROM cn_stories  WHERE item_id IN (SELECT item_id FROM cn_items WHERE block_height >= ?)`,
		`DELETE FROM cn_comments WHERE item_id IN (SELECT item_id FROM cn_items WHERE block_height >= ?)`,
		`DELETE FROM cn_votes        WHERE block_height >= ?`,
		`DELETE FROM cn_continuations WHERE block_height >= ?`,
		`DELETE FROM cn_topics        WHERE created_height >= ?`,
		`DELETE FROM cn_items         WHERE block_height >= ?`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, height); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) PutItem(ctx context.Context, it Item) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cn_items(item_id, txid, vout, block_height, tx_index, vout_index, type_tag, block_time)
		 VALUES (?,?,?,?,?,?,?,?)
		 ON CONFLICT(item_id) DO NOTHING`,
		it.ID[:], it.TxID, it.Vout, it.Height, it.TxIndex, it.VoutIndex, int(it.TypeTag), it.BlockTime)
	return err
}

func (s *SQLiteStore) InsertTopicIfAbsent(ctx context.Context, t Topic) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO cn_topics(topic, name, retention_days, created_height, txid)
		 VALUES (?,?,?,?,?)
		 ON CONFLICT(topic) DO NOTHING`,
		t.Topic[:], t.Name, int(t.RetentionDays), t.CreatedHeight, t.TxID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *SQLiteStore) PutStory(ctx context.Context, st Story) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cn_stories(item_id, topic, headline, subtype, url, body, lang, nsfw, media_hash, raw_tlv)
		 VALUES (?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(item_id) DO NOTHING`,
		st.ID[:], st.Topic[:], st.Headline, int(st.Subtype),
		nullStr(st.URL), nullStr(st.Body), nullStr(st.Lang), boolInt(st.NSFW), nullBytes(st.MediaHash), st.RawTLV)
	return err
}

func (s *SQLiteStore) PutComment(ctx context.Context, c Comment) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cn_comments(item_id, parent_id, author_xpk, body, url, lang, reply_quote, raw_tlv)
		 VALUES (?,?,?,?,?,?,?,?)
		 ON CONFLICT(item_id) DO NOTHING`,
		c.ID[:], c.ParentID[:], c.AuthorXPK[:],
		nullStr(c.Body), nullStr(c.URL), nullStr(c.Lang), nullStr(c.ReplyQuote), c.RawTLV)
	return err
}

func (s *SQLiteStore) PutVote(ctx context.Context, v Vote) error {
	// Latest-in-canonical-order wins. The WHERE guard makes replays / out-of-order
	// inserts idempotent: an older vote never overwrites a newer one.
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cn_votes(target_id, author_xpk, kind, block_height, tx_index, vout_index, block_time)
		 VALUES (?,?,?,?,?,?,?)
		 ON CONFLICT(target_id, author_xpk) DO UPDATE SET
		     kind=excluded.kind, block_height=excluded.block_height,
		     tx_index=excluded.tx_index, vout_index=excluded.vout_index, block_time=excluded.block_time
		 WHERE excluded.block_height > cn_votes.block_height
		    OR (excluded.block_height = cn_votes.block_height AND excluded.tx_index > cn_votes.tx_index)
		    OR (excluded.block_height = cn_votes.block_height AND excluded.tx_index = cn_votes.tx_index
		        AND excluded.vout_index > cn_votes.vout_index)`,
		v.TargetID[:], v.AuthorXPK[:], int(v.Kind), v.Height, v.TxIndex, v.VoutIndex, v.BlockTime)
	return err
}

func (s *SQLiteStore) PutContinuation(ctx context.Context, c Continuation) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cn_continuations(head_id, seq, chunk, block_height) VALUES (?,?,?,?)
		 ON CONFLICT(head_id, seq) DO NOTHING`,
		c.HeadID[:], int(c.Seq), c.Chunk, c.Height)
	return err
}

func (s *SQLiteStore) ItemByOutpoint(ctx context.Context, txid string, vout uint32) (Item, bool, error) {
	return s.scanItem(s.db.QueryRowContext(ctx, itemSelect+` WHERE txid=? AND vout=?`, txid, vout))
}

func (s *SQLiteStore) ItemByID(ctx context.Context, id codec.ItemID) (Item, bool, error) {
	return s.scanItem(s.db.QueryRowContext(ctx, itemSelect+` WHERE item_id=?`, id[:]))
}

const itemSelect = `SELECT item_id, txid, vout, block_height, tx_index, vout_index, type_tag, block_time FROM cn_items`

func (s *SQLiteStore) scanItem(row *sql.Row) (Item, bool, error) {
	var it Item
	var idb []byte
	var tag int
	err := row.Scan(&idb, &it.TxID, &it.Vout, &it.Height, &it.TxIndex, &it.VoutIndex, &tag, &it.BlockTime)
	if err == sql.ErrNoRows {
		return Item{}, false, nil
	}
	if err != nil {
		return Item{}, false, err
	}
	copy(it.ID[:], idb)
	it.TypeTag = codec.TypeTag(tag)
	return it, true, nil
}

// feedSelect returns story rows joined to items with vote tally + comment count.
const feedSelect = `
SELECT s.item_id, i.txid, i.vout, i.block_height, i.tx_index, i.vout_index, i.block_time,
       s.topic, s.headline, s.subtype,
       COALESCE(s.url,''), COALESCE(s.body,''), COALESCE(s.lang,''), s.nsfw,
       COALESCE((SELECT SUM(CASE WHEN v.kind=? THEN 1 ELSE 0 END) - SUM(CASE WHEN v.kind=? THEN 1 ELSE 0 END)
                 FROM cn_votes v WHERE v.target_id=s.item_id), 0) AS points,
       COALESCE((SELECT COUNT(*) FROM cn_comments c WHERE c.parent_id=s.item_id), 0) AS ccount,
       (SELECT c.author_xpk FROM cn_comments c JOIN cn_items ci ON ci.item_id=c.item_id
        WHERE c.parent_id=s.item_id
        ORDER BY ci.block_height, ci.tx_index, ci.vout_index LIMIT 1) AS author_xpk
FROM cn_stories s JOIN cn_items i ON i.item_id=s.item_id`

func (s *SQLiteStore) queryFeed(ctx context.Context, where string, args []any, nowUnix int64) ([]FeedItem, error) {
	full := append([]any{int(codec.TypeUpvote), int(codec.TypeDownvote)}, args...)
	rows, err := s.db.QueryContext(ctx, feedSelect+where, full...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := resolveNow(nowUnix)
	var out []FeedItem
	for rows.Next() {
		var f FeedItem
		var idb, topicb, authorb []byte
		var subtype, nsfw int
		if err := rows.Scan(&idb, &f.TxID, &f.Vout, &f.Height, &f.TxIndex, &f.VoutIndex, &f.BlockTime,
			&topicb, &f.Headline, &subtype, &f.URL, &f.Body, &f.Lang, &nsfw, &f.Points, &f.CommentCount, &authorb); err != nil {
			return nil, err
		}
		copy(f.ID[:], idb)
		copy(f.Topic[:], topicb)
		if len(authorb) == codec.XOnlyPubKeyLen {
			copy(f.AuthorXPK[:], authorb)
		}
		f.Item.TypeTag = codec.TypeStory
		f.Subtype = codec.Subtype(subtype)
		f.NSFW = nsfw != 0
		f.Score = rankScore(f.Points, now, f.BlockTime)
		out = append(out, f)
	}
	return out, rows.Err()
}

// buildFilter assembles the WHERE clause shared by the feed queries.
func buildFilter(topic *codec.Topic, subtype *codec.Subtype, includeNSFW bool) (string, []any) {
	where := " WHERE 1=1"
	var args []any
	if topic != nil {
		where += " AND s.topic=?"
		args = append(args, topic[:])
	}
	if subtype != nil {
		where += " AND s.subtype=?"
		args = append(args, int(*subtype))
	}
	if !includeNSFW {
		where += " AND s.nsfw=0"
	}
	return where, args
}

func (s *SQLiteStore) FrontPage(ctx context.Context, q FeedQuery) ([]FeedItem, error) {
	where, args := buildFilter(q.Topic, q.Subtype, q.IncludeNSFW)
	items, err := s.queryFeed(ctx, where, args, q.NowUnix)
	if err != nil {
		return nil, err
	}
	sortByScore(items)
	return page(items, q.Limit, q.Offset), nil
}

func (s *SQLiteStore) NewFeed(ctx context.Context, q FeedQuery) ([]FeedItem, error) {
	where, args := buildFilter(q.Topic, q.Subtype, q.IncludeNSFW)
	items, err := s.queryFeed(ctx, where, args, q.NowUnix)
	if err != nil {
		return nil, err
	}
	sortByNewest(items)
	return page(items, q.Limit, q.Offset), nil
}

func (s *SQLiteStore) ByTopic(ctx context.Context, topic codec.Topic, limit, offset int) ([]FeedItem, error) {
	return s.NewFeed(ctx, FeedQuery{Topic: &topic, IncludeNSFW: true, Limit: limit, Offset: offset})
}

func (s *SQLiteStore) ItemsByAuthor(ctx context.Context, author codec.XOnlyPubKey, limit, offset int) ([]FeedItem, error) {
	all, err := s.queryFeed(ctx, "", nil, 0)
	if err != nil {
		return nil, err
	}
	var out []FeedItem
	for _, f := range all {
		if f.AuthorXPK == author {
			out = append(out, f)
		}
	}
	sortByNewest(out)
	return page(out, limit, offset), nil
}

func (s *SQLiteStore) GetItem(ctx context.Context, id codec.ItemID) (FeedItem, bool, error) {
	items, err := s.queryFeed(ctx, " WHERE s.item_id=?", []any{id[:]}, 0)
	if err != nil {
		return FeedItem{}, false, err
	}
	if len(items) == 0 {
		return FeedItem{}, false, nil
	}
	return items[0], true, nil
}

const commentSelect = `
SELECT c.item_id, c.parent_id, c.author_xpk,
       COALESCE(c.body,''), COALESCE(c.url,''), COALESCE(c.lang,''), COALESCE(c.reply_quote,''),
       i.block_height, i.block_time,
       COALESCE((SELECT SUM(CASE WHEN v.kind=? THEN 1 ELSE 0 END) - SUM(CASE WHEN v.kind=? THEN 1 ELSE 0 END)
                 FROM cn_votes v WHERE v.target_id=c.item_id), 0) AS points
FROM cn_comments c JOIN cn_items i ON i.item_id=c.item_id`

func (s *SQLiteStore) scanComments(rows *sql.Rows) ([]ThreadComment, error) {
	defer rows.Close()
	now := resolveNow(0)
	var out []ThreadComment
	for rows.Next() {
		var c ThreadComment
		var idb, pidb, xpk []byte
		if err := rows.Scan(&idb, &pidb, &xpk, &c.Body, &c.URL, &c.Lang, &c.ReplyQuote,
			&c.Height, &c.BlockTime, &c.Points); err != nil {
			return nil, err
		}
		copy(c.ID[:], idb)
		copy(c.ParentID[:], pidb)
		copy(c.AuthorXPK[:], xpk)
		c.Score = rankScore(c.Points, now, c.BlockTime)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) Thread(ctx context.Context, root codec.ItemID) ([]ThreadComment, error) {
	// Recursive walk: all comments whose parent chain leads back to root.
	q := `WITH RECURSIVE thread(item_id) AS (
	          SELECT item_id FROM cn_comments WHERE parent_id=?
	          UNION
	          SELECT c.item_id FROM cn_comments c JOIN thread t ON c.parent_id=t.item_id
	      )` + commentSelect + ` WHERE c.item_id IN (SELECT item_id FROM thread)
	      ORDER BY i.block_height, i.tx_index, i.vout_index`
	rows, err := s.db.QueryContext(ctx, q, root[:], int(codec.TypeUpvote), int(codec.TypeDownvote))
	if err != nil {
		return nil, err
	}
	return s.scanComments(rows)
}

func (s *SQLiteStore) ByAuthor(ctx context.Context, author codec.XOnlyPubKey, limit, offset int) ([]ThreadComment, error) {
	q := commentSelect + ` WHERE c.author_xpk=? ORDER BY i.block_height DESC, i.tx_index DESC, i.vout_index DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, max(offset, 0))
	}
	rows, err := s.db.QueryContext(ctx, q, int(codec.TypeUpvote), int(codec.TypeDownvote), author[:])
	if err != nil {
		return nil, err
	}
	return s.scanComments(rows)
}

func (s *SQLiteStore) ListTopics(ctx context.Context) ([]Topic, error) {
	byID := map[codec.Topic]*Topic{}
	var order []codec.Topic

	// Only explicitly created (named) topics are listed.
	rows, err := s.db.QueryContext(ctx, `SELECT topic, name, retention_days, created_height, txid FROM cn_topics`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t Topic
		var topicb []byte
		var ret int
		if err := rows.Scan(&topicb, &t.Name, &ret, &t.CreatedHeight, &t.TxID); err != nil {
			rows.Close()
			return nil, err
		}
		copy(t.Topic[:], topicb)
		if t.Topic.IsZero() {
			continue // zero topic is reserved ("no topic") — never list
		}
		t.RetentionDays = byte(ret)
		cp := t
		byID[t.Topic] = &cp
		order = append(order, t.Topic)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Attach story counts for the listed topics.
	rows, err = s.db.QueryContext(ctx, `SELECT topic, COUNT(*) FROM cn_stories GROUP BY topic`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var topicb []byte
		var cnt int
		if err := rows.Scan(&topicb, &cnt); err != nil {
			rows.Close()
			return nil, err
		}
		var id codec.Topic
		copy(id[:], topicb)
		if t, ok := byID[id]; ok {
			t.StoryCount = cnt
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]Topic, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	sortTopics(out)
	return out, nil
}

// --- small SQL null helpers ---

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
