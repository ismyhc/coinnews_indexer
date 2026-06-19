# CoinNews Indexer

Go indexer for the **CoinNews protocol** (stories/comments/votes in Bitcoin
`OP_RETURN`). Scans a Bitcoin Core node over JSON-RPC, decodes + signature-verifies
messages, stores them in SQLite, and serves a ranked feed / threads / topics via
HTTP. The chain is the only source of truth.

Full protocol spec: `docs/coinnews-protocol.md`. Deploy/run docs: `README.md`.

## Stack

- **Go 1.25+**, no CGO. SQLite via `modernc.org/sqlite` (WAL mode). Schnorr via
  `btcd/btcec/v2/schnorr`. API is stdlib `net/http` + `encoding/json` only.
- The global "use Bun" instruction does **not** apply here.

## Layout

```
codec/    Wire format + BIP-340 signatures. No node/DB. The critical test surface.
rpc/      Bitcoin Core JSON-RPC client (BlockSource).
store/    Store interface + SQLiteStore + MemoryStore + ranking. Swap for tests.
scanner/  BlockSource → OP_RETURN → codec → verify → store; canonical order + reorg.
api/      REST (/v1/*) + ConnectRPC-compatible (/coinnews.v1.CoinNewsService/*) + Swagger (/docs).
main.go   run / index / serve.
```

`scanner` and `api` depend on the `store.Store` interface, so `MemoryStore` backs
node-free, DB-free tests.

## Commands

```sh
go build -o coinnews-indexer .
go test ./...
go run . run -rpc … -rpcuser … -rpcpass …   # live: index + serve (preferred)
```

## Invariants (enforced in code — keep them true)

- **Envelope:** `"CN"` (0x43 0x4E) + 1-byte TypeTag. Unknown magic → ignore; known
  magic + unknown tag → ignore distinctly. Exact sizes/layout: `codec/constants.go`,
  `codec/message.go`.
- **Little-endian; compact-size varints** (reject `0xFF` and non-canonical).
- **ItemID** = `sha256(txid_internal || vout_LE)[:12]`. `txid_internal` is the
  REVERSE of RPC/explorer hex — getting it backwards breaks every lookup.
- **Signatures** (BIP-340, x-only): comment over
  `tagged_hash("CoinNews/Comment", parent_id || tlv_blob)`; vote over
  `tagged_hash("CoinNews/Vote", typetag || target_id)`. Fail → drop, don't store.
- **Canonical scan order:** height ↑, tx_index ↑, vout_index ↑. References
  (comment parent, vote target) must already be indexed or the message is dropped.
- **Topic creation first-wins; vote latest-per-(target,author) wins.**
- **Cursor** (height+hash) persisted; reorgs detected via prev-hash mismatch →
  rewind `ReorgDepth` (default 6).
- **Ranking:** HN gravity `points/(age_hours+2)^1.8` (no `-1` — CoinNews has no
  self-vote, so unvoted posts score 0 and tie-break to newest, not invert). Computed
  in Go with an injectable clock. Topics list only created; zero topic never listed.
- **Story authorship** is layered from the story's earliest direct comment
  (`author_xpk`); `ListByAuthor` returns stories so authored.

## Status / known gaps

- **nsfw** TLV is treated as a presence flag — confirm bool-byte intent vs a real vector.
- **Continuation** chunks are stored but not yet reassembled into the head body (8 KiB cap on reassembly).
