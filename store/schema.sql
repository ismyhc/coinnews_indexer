-- CoinNews indexer SQLite schema. Ids/keys are stored as BLOBs; the API layer
-- encodes them as hex. block_time is unix seconds.

CREATE TABLE IF NOT EXISTS cn_items (
    item_id      BLOB PRIMARY KEY,
    txid         TEXT    NOT NULL,
    vout         INTEGER NOT NULL,
    block_height INTEGER NOT NULL,
    tx_index     INTEGER NOT NULL,
    vout_index   INTEGER NOT NULL,
    type_tag     INTEGER NOT NULL,
    block_time   INTEGER NOT NULL,
    UNIQUE (txid, vout)
);
CREATE INDEX IF NOT EXISTS idx_cn_items_order ON cn_items (block_height, tx_index, vout_index);
CREATE INDEX IF NOT EXISTS idx_cn_items_type  ON cn_items (type_tag, block_height DESC);

CREATE TABLE IF NOT EXISTS cn_topics (
    topic          BLOB    PRIMARY KEY,
    name           TEXT    NOT NULL,
    retention_days INTEGER NOT NULL DEFAULT 0,
    created_height INTEGER NOT NULL,
    txid           TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS cn_stories (
    item_id    BLOB    PRIMARY KEY REFERENCES cn_items(item_id),
    topic      BLOB    NOT NULL,
    headline   TEXT    NOT NULL,
    subtype    INTEGER NOT NULL DEFAULT 0,
    url        TEXT,
    body       TEXT,
    lang       TEXT,
    nsfw       INTEGER NOT NULL DEFAULT 0,
    media_hash BLOB,
    raw_tlv    BLOB
);
CREATE INDEX IF NOT EXISTS idx_cn_stories_topic   ON cn_stories (topic);
CREATE INDEX IF NOT EXISTS idx_cn_stories_subtype ON cn_stories (subtype);

CREATE TABLE IF NOT EXISTS cn_comments (
    item_id     BLOB    PRIMARY KEY REFERENCES cn_items(item_id),
    parent_id   BLOB    NOT NULL,
    author_xpk  BLOB    NOT NULL,
    body        TEXT,
    url         TEXT,
    lang        TEXT,
    reply_quote TEXT,
    raw_tlv     BLOB
);
CREATE INDEX IF NOT EXISTS idx_cn_comments_parent ON cn_comments (parent_id);
CREATE INDEX IF NOT EXISTS idx_cn_comments_author ON cn_comments (author_xpk);

CREATE TABLE IF NOT EXISTS cn_votes (
    target_id    BLOB    NOT NULL,
    author_xpk   BLOB    NOT NULL,
    kind         INTEGER NOT NULL,
    block_height INTEGER NOT NULL,
    tx_index     INTEGER NOT NULL,
    vout_index   INTEGER NOT NULL,
    block_time   INTEGER NOT NULL,
    PRIMARY KEY (target_id, author_xpk)
);
CREATE INDEX IF NOT EXISTS idx_cn_votes_target ON cn_votes (target_id);

CREATE TABLE IF NOT EXISTS cn_continuations (
    head_id      BLOB    NOT NULL,
    seq          INTEGER NOT NULL,
    chunk        BLOB    NOT NULL,
    block_height INTEGER NOT NULL,
    PRIMARY KEY (head_id, seq)
);

CREATE TABLE IF NOT EXISTS cn_scanner_cursor (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    last_height INTEGER NOT NULL,
    last_hash   TEXT    NOT NULL
);
