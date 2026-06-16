# CoinNews Indexer

A Go indexer for the **CoinNews protocol** — a bulletin board (stories, comments, votes)
encoded entirely in Bitcoin `OP_RETURN` outputs. It scans blocks from a Bitcoin Core node
over JSON-RPC, decodes CoinNews messages, persists them to SQLite, and serves a ranked
feed / threads / topics over an HTTP+JSON API. No trusted servers, registries, or
pre-shipped databases are required — the chain is the only source of truth.

Protocol spec: `docs/coinnews-protocol.md` (LayerTwo-Labs, BSD-2-Clause draft).

## Stack & conventions

- **Language:** Go (1.25+). The global "use Bun" instruction does **not** apply here.
- **Block source:** Bitcoin Core JSON-RPC (`getblockcount`, `getblockhash`, `getblock <hash> 2`
  for verbose tx+vout data). Drivechain runs the same RPC surface — treat it as plain Core.
- **Storage:** SQLite via **`modernc.org/sqlite`** (pure-Go, no CGO). File DBs run in **WAL
  mode** so the API reads while the scanner writes in the single-process `run` mode.
- **Crypto:** BIP-340 Schnorr / x-only pubkeys via `github.com/btcsuite/btcd/btcec/v2/schnorr`
  (which uses `decred/dcrd/dcrec/secp256k1/v4`). Do **not** hand-roll secp256k1.
- **API:** plain `net/http` + `encoding/json` (stdlib only — no third-party HTTP/router deps).
- Keep the **codec** (pure byte parsing + crypto, no DB) separate from the **scanner**
  (block iteration + persistence) and the **api** (read-only queries). The codec must be
  unit-testable with no node or DB.

### Implementation map

```
codec/    Wire format + BIP-340 signatures. No node, no DB. The critical test surface.
rpc/      Bitcoin Core JSON-RPC client (BlockSource).
store/    Store interface + SQLiteStore + MemoryStore + ranking. Swap backends for tests.
scanner/  BlockSource → OP_RETURN → codec → verify → store, canonical order + reorg rewind.
api/      net/http JSON server over store.Store.
main.go   run / index / serve subcommands.
```

The `store.Store` interface has two implementations (`OpenSQLite`, `NewMemory`); the scanner
and API depend on the interface, so the in-memory backend backs node-free, DB-free tests.

### Commands

```sh
go build -o coinnews-indexer .
go test ./...                 # codec round-trips/golden vectors + store/scanner/api suites
go run . run   -rpc … -rpcuser … -rpcpass …   # live: index + serve in one process (preferred)
go run . index -rpc … -rpcuser … -rpcpass …   # scan once (or -poll) then exit
go run . serve -db coinnews.db -addr :8080    # serve API over an existing DB
```

See `README.md` for full flag/deploy docs (native + Docker, local vs Linux networking).

## Wire format — the rules the codec MUST enforce

All integers are **little-endian**. Lengths use **Bitcoin compact-size varint**.
Every CoinNews payload is the data pushed by a single `OP_RETURN` (nulldata) output.

### Envelope

```
"CN" (0x43 0x4E)  ||  TypeTag (1 byte)  ||  body…
```

- If the first 2 bytes aren't `CN`, it's **not** a CoinNews payload → silently ignore.
- If the magic matches but the TypeTag is unknown, **ignore the message** but treat it as a
  distinct case from "not CoinNews" (useful as a forward-compat / new-publisher signal).

| TypeTag | Message        | Value |
|---------|----------------|-------|
| Topic Creation | `0x01` | scopes stories |
| Story          | `0x02` | unsigned |
| Comment        | `0x03` | signed |
| Upvote         | `0x04` | signed |
| Downvote       | `0x05` | signed |
| Continuation   | `0x06` | body-overflow chunks |

### Fixed sizes

| Field            | Bytes |
|------------------|-------|
| Magic            | 2 |
| TypeTag          | 1 |
| Topic id         | 4 |
| ItemID           | 12 |
| x-only pubkey    | 32 |
| Schnorr sig      | 64 |
| Full outpoint    | 36 (txid 32 + vout 4) |
| Continuation chunk | ≤ 63 |
| Headline         | ≤ 252 (fits a 1-byte compact size) |
| Reassembled TLV section | ≤ 8192 (8 KiB) |

A vote is therefore exactly **111 bytes** on the wire (2 + 1 + 12 + 32 + 64).

### Compact-size varint

- `0x00–0xFC` → that value in one byte.
- `0xFD` + uint16 LE (values 253–65535).
- `0xFE` + uint32 LE (values 65536–2³²−1).
- `0xFF` form is **rejected**.
- **Reject non-canonical encodings** — a value that fits in a smaller form encoded in a
  larger one is invalid input, not a value to accept.

### Message bodies (in read order)

- **Topic Creation:** `topic(4) || retention_days(1) || name_len(varint) || name(utf8)`
  - `retention_days = 0x00` means infinite retention (a pruning *hint*, not a rule).
- **Story:** `topic(4) || headline_len(varint) || headline(utf8) || TLVs…`
- **Comment:** `parent_id(12) || author_xpk(32) || sig(64) || TLVs…`
- **Upvote / Downvote:** `target_id(12) || author_xpk(32) || sig(64)` — **no TLVs**, fixed length.
- **Continuation:** `head_id(12) || seq(1) || chunk(1..63)`

### TLV (metadata)

Tuples of `tag(1 byte) || length(varint) || value(length bytes)`, concatenated until the
slice ends. **Unknown tags are skipped via their length framing** (forward compatible) and
preserved on round-trip — never error on an unknown tag. Stop cleanly at end-of-slice;
error only on a truncated tuple.

Tag namespace:
- `0x01–0x7F` — spec-defined.
- `0x80–0xEF` — out-of-band registry assignments.
- `0xF0–0xFF` — application-private.

Well-known tags:

| Tag | Name        | Value | Used by |
|-----|-------------|-------|---------|
| `0x01` | url         | absolute URI (utf8) | story, comment |
| `0x02` | body        | free text (utf8)    | story, comment |
| `0x03` | lang        | BCP-47 code         | story, comment |
| `0x04` | nsfw        | content flag — **current impl: treated as presence** (confirm bool-byte intent vs a real vector) | story |
| `0x05` | subtype     | story category (see below) | story |
| `0x06` | media_hash  | 32-byte off-chain content hash | story |
| `0x07` | reply_quote | quoted excerpt (utf8) | comment |

Story subtypes: `0 link, 1 text, 2 ask, 3 show, 4 poll, 5 job`.

## Identity & signatures

**ItemID** — every CoinNews output gets one:

```
ItemID = sha256( txid_internal(32) || vout(4, uint32 LE) )[0:12]
```

- `txid_internal` is the **raw/internal** byte order, i.e. the **reverse** of the hex string
  shown by Bitcoin Core / explorers. When you read a txid hex from RPC, reverse it to bytes
  before hashing. Getting this backwards silently breaks every reference lookup — cover it
  with a golden test.

**Schnorr verification** (BIP-340, x-only key = `author_xpk`):

- **Comment:** verify `sig` over `tagged_hash("CoinNews/Comment", parent_id(12) || tlv_blob)`.
- **Vote:** verify `sig` over `tagged_hash("CoinNews/Vote", typetag_byte(1) || target_id(12))`.
  Including the typetag byte domain-separates up- vs down-votes so a signature can't be replayed
  across vote kinds.
- `tagged_hash(tag, m) = sha256( sha256(tag) || sha256(tag) || m )`.
- A message that fails verification is **dropped**, not stored. Stories are unsigned (authorship,
  if desired, is layered via the author's first comment).

## Indexing rules

1. **Canonical scan order** is mandatory: `block_height ASC, tx_index ASC, vout_index ASC`.
   Process strictly in this order so that any referenced item is already indexed before the
   referrer. Same-block references are valid only when the target appears earlier in scan order.
2. Maintain two bidirectional lookups (the SQLite `cn_items` table + its unique index serve both):
   `items_by_outpoint (txid,vout) → ItemID` and `items_by_id ItemID → item`.
3. **Topic creation:** first confirmed creation for a topic id wins; later collisions are discarded.
   Topic `0x00000000` is **reserved** ("no topic"/global) — valid on a Story, never listed as a topic.
4. **Votes:** keep only the **latest vote per `(author, target)`** in canonical order — a later
   vote (including a flip) supersedes the earlier one. Model as upsert on PK `(target_id, author_xpk)`.
5. **References must resolve:** a Comment whose `parent_id` or a Vote whose `target_id` is not
   already indexed is **dropped** (logged). Canonical order guarantees valid same-block refs resolve.
6. **Continuations:** stored per `(head_id, seq)`. NOTE: reassembly into the head item's TLV body
   is **not yet implemented** — chunks land in `cn_continuations`; the 8 KiB cap applies on reassembly.
7. Ignore unknown TypeTags and unknown TLV tags rather than failing the block.
8. Persist a **cursor** (last height + hash) and resume from it; reorgs are detected by a
   `previousblockhash`/cursor mismatch and handled by rewinding `ReorgDepth` blocks (default 6).

## Persistence (SQLite)

Tables to maintain (names/columns reflect the indexer's needs; this is our schema, design it
to fit the queries above):

- `cn_items` — `item_id PK`, `txid`, `vout`, `block_height`, `tx_index`, `vout_index`,
  `type_tag`, `block_time`; `UNIQUE(txid, vout)`; index on `(block_height, tx_index, vout_index)`
  and on `(type_tag, block_height DESC)`.
- `cn_topics` — `topic PK(4)`, `name`, `retention_days`, `created_height`, `txid`.
- `cn_stories` — `item_id PK → cn_items`, `topic`, `headline`, `subtype`, `url`, `body`, `lang`,
  `nsfw`, `media_hash`, `raw_tlv`; index on `topic` and `subtype`.
- `cn_comments` — `item_id PK → cn_items`, `parent_id`, `author_xpk`, `body`, `url`, `lang`,
  `reply_quote`, `raw_tlv`; index on `parent_id` and `author_xpk`.
- `cn_votes` — PK `(target_id, author_xpk)`, `kind`, `block_height`, `tx_index`, `vout_index`,
  `block_time`; index on `target_id`.
- `cn_continuations` — PK `(head_id, seq)`, `chunk`, `block_height`.
- `cn_scanner_cursor` — single row (`id=1`), `last_height`, `last_hash`.

Store ids/keys as `BLOB` internally; expose them as hex at the API boundary.

## Ranking

Front-page and thread ordering use the Hacker News formula, with age from `block_time`:

```
score = (upvotes − downvotes − 1) / (age_hours + 2)^1.8
```

Tally votes per target (`SUM(kind=upvote) − SUM(kind=downvote)`), join to items, then sort by
score (newest sort: `block_height DESC, tx_index DESC, vout_index DESC`). Items with a negative
vote differential may be hidden from the feed but stay retrievable by id/thread.

> Current impl computes the score and sorts in Go after loading candidate rows (so it doesn't
> depend on SQLite's optional `pow()`), with an injectable `NowUnix` clock for deterministic
> tests. Fine at this scale; push scoring into SQL for very large datasets.

## HTTP / API surface

Read-only endpoints over the SQLite store (mirror the reference server's capabilities):

- **Front page** — ranked feed; params: `limit`, `offset`, optional `subtype`, optional `topic`.
- **New feed** — same params, recency-sorted.
- **Get item** — by `item_id`.
- **Thread** — all comments under a root id, assembled into a tree by `parent_id`,
  each node carrying its own vote tally + score.
- **List by author** — comments by `author_xpk` (`/comments`) or stories authored by
  them (`/items`, attributed via each story's first comment).
- **List by topic** — by `topic` (`limit`/`offset`).
- **List topics** — includes topics referenced only by stories (`created:false`, empty `name`,
  `story_count`); the reserved zero topic is excluded.

Item payloads expose: `item_id`, `topic`, `headline`, `url`, `body`, `subtype`, `lang`,
`nsfw`, `block_height`, `block_time`, `points (up−down)`, `comment_count`, `score`. Ids/keys
are hex at the API boundary, BLOBs in SQLite.

## Testing priorities

1. **Codec golden vectors** — encode→decode round-trips for every message type, plus
   known-answer ItemID (watch the txid byte-reversal) and signature digests.
2. Compact-size varint canonicality (reject `0xFF` and over-long encodings).
3. TLV unknown-tag skip + truncated-tuple error.
4. Vote supersession and same-block reference ordering.
5. Continuation reassembly and the 8 KiB cap.
