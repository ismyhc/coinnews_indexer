# CoinNews Indexer

A Go indexer for the **CoinNews protocol** — a bulletin board (stories, comments,
votes) encoded entirely in Bitcoin `OP_RETURN` outputs. It scans blocks from a
Bitcoin Core node over JSON-RPC, decodes and signature-verifies CoinNews messages,
persists them to SQLite, and serves a ranked feed / threads / topics over a
read-only HTTP+JSON API. The chain is the only source of truth — no trusted
servers, registries, or pre-shipped databases.

See [`CLAUDE.md`](./CLAUDE.md) for the wire-format spec and
[`docs/coinnews-protocol.md`](./docs/coinnews-protocol.md) for the protocol draft.

## Requirements

- **Go 1.25+** (only to build; the binary is self-contained).
- A **Bitcoin Core**-compatible node with JSON-RPC enabled. Signet / regtest /
  mainnet all work; Drivechain nodes expose the same RPC.
- No CGO — SQLite is pure-Go `modernc.org/sqlite`; the rest is stdlib + `btcec`
  for Schnorr verification.

## Layout

```
codec/     Wire format: envelope, varint, ItemID, TLV, BIP-340 signatures (node-free, fully tested)
rpc/       Minimal Bitcoin Core JSON-RPC client
store/     Store interface + SQLite backend + in-memory backend + ranking
scanner/   Block walk → OP_RETURN extract → decode → verify → persist, with reorg handling
api/       net/http JSON API over the store
main.go    run / index / serve subcommands
```

## Commands

| Command | Use |
|---------|-----|
| **`run`** | **Recommended.** One process that indexes continuously **and** serves the API, sharing one store handle. |
| `index` | Scan into the store, then exit (or follow the tip with `-poll`). |
| `serve` | Serve the read-only API over an existing store. |

Common flags (all commands accept the relevant subset):

| Flag | Default | Meaning |
|------|---------|---------|
| `-rpc` | `http://127.0.0.1:8332` | Bitcoin Core RPC URL |
| `-rpcuser` | `$BITCOIN_RPC_USER` | RPC username (or `__cookie__` for cookie auth) |
| `-rpcpass` | `$BITCOIN_RPC_PASS` | RPC password (or the cookie secret) |
| `-db` | `coinnews.db` | SQLite database path |
| `-addr` | `:8080` | API listen address (`run`, `serve`) |
| `-start` | `0` | Start height — only used on a fresh DB; otherwise resumes from the cursor |
| `-poll` | `15s` (`run`) | Interval to re-scan for new blocks (`index` defaults to `0` = scan once) |

`run`/`index` scan in canonical order, resume from a saved cursor, and detect and
rewind reorgs automatically. A transient RPC error is logged and retried on the
next poll rather than crashing the API.

## Connecting to your node

You need the RPC URL, username, and password.

- Default ports: mainnet `8332`, testnet `18332`, **signet `38332`**, regtest `18443`.
- `rpcuser`/`rpcpassword` in `bitcoin.conf` → use those.
- **Cookie auth** → the `.cookie` file holds `__cookie__:<secret>`; pass
  `__cookie__` as the user and the secret as the password.

> **BitWindow** runs a bundled node on **signet `:38332`** with `user` / `password`
> (see `~/Library/Application Support/bitwindow/bitwindow-bitcoin.conf` on macOS).
> Sanity-check it:
> ```sh
> curl -s --user user:password \
>   --data-binary '{"jsonrpc":"1.0","method":"getblockchaininfo","params":[]}' \
>   http://127.0.0.1:38332/
> ```

## Run without Docker

Build the binary and run it:

```sh
go build -o coinnews-indexer .

./coinnews-indexer run \
  -rpc http://127.0.0.1:38332 -rpcuser user -rpcpass password \
  -db coinnews.db -addr :8080 -poll 15s
```

Backfill and serve separately instead, if you prefer (they share the SQLite file):

```sh
./coinnews-indexer index -rpc http://127.0.0.1:38332 -rpcuser user -rpcpass password -db coinnews.db
./coinnews-indexer serve -db coinnews.db -addr :8080
```

For always-on use, run it under a supervisor. Example systemd unit:

```ini
# /etc/systemd/system/coinnews.service
[Unit]
Description=CoinNews Indexer
After=network-online.target

[Service]
ExecStart=/usr/local/bin/coinnews-indexer run -rpc http://127.0.0.1:38332 -db /var/lib/coinnews/coinnews.db -addr :8080 -poll 15s
Environment=BITCOIN_RPC_USER=user
Environment=BITCOIN_RPC_PASS=password
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

> **WAL note:** a file-backed DB uses WAL mode, so it's three files —
> `coinnews.db`, `-wal`, `-shm` (this lets the API read while the indexer writes).
> Back them up together, or stop the process first.

## Run with Docker

The `Dockerfile` builds a static binary onto Alpine (~32 MB). The DB lives on a
`/data` volume; the default command is `run -db /data/coinnews.db -addr :8080`, so
you only supply RPC flags.

```sh
docker build -t coinnews-indexer .

docker run -d --name coinnews -p 8080:8080 \
  -v coinnews-data:/data --restart=unless-stopped \
  coinnews-indexer run \
    -rpc <RPC-URL> -rpcuser user -rpcpass password \
    -db /data/coinnews.db -addr :8080 -poll 15s
```

The only thing that changes per environment is `<RPC-URL>`, because inside a
container `127.0.0.1` is the container, not the host:

| Where the node runs | `<RPC-URL>` | Extra `docker run` flags |
|---------------------|-------------|--------------------------|
| Host, Docker Desktop (macOS/Win) | `http://host.docker.internal:38332` | — |
| Host, Docker on Linux | `http://host.docker.internal:38332` | `--add-host=host.docker.internal:host-gateway` |
| Host, Docker on Linux (alt) | `http://127.0.0.1:38332` | `--network=host` (ignores `-p`) |
| Different machine | `http://<node-ip>:38332` | ensure node `rpcallowip` permits it |

Notes:

- **Credentials:** prefer env vars over flags for non-local use (flags show in
  `docker inspect`): `-e BITCOIN_RPC_USER=user -e BITCOIN_RPC_PASS=password`.
- **Persistence:** the `coinnews-data` volume keeps the DB across restarts, so the
  indexer resumes from its cursor instead of re-scanning from genesis.
- Never expose a node's RPC port to the public internet.

### HTTPS with Caddy

To serve the container over HTTPS, put [Caddy](https://caddyserver.com) in front of
it — it obtains and renews a Let's Encrypt certificate automatically.
`scripts/install-caddy.sh` installs Caddy (Ubuntu 24.04 / Debian via apt) and
configures it to reverse-proxy your domain to the container's `:8080`.

Prerequisites: a domain whose DNS points at the host, ports 80/443 open, and the
container published with `-p 8080:8080` (as above).

```sh
sudo ./scripts/install-caddy.sh coinnews.example.com you@example.com
```

That's it — `https://coinnews.example.com` proxies to the indexer, with the
certificate issued on first request. Watch progress with `journalctl -u caddy -f`.

## API

Interactive docs: **`GET /docs`** (Swagger UI) — the OpenAPI 3.1 spec is served at
`GET /openapi.yaml`.

Responses are JSON; ids/keys are hex (`item_id` 12 bytes, `topic` 4 bytes,
`author_xpk` 32 bytes).

| Method & path | Description |
|---|---|
| `GET /v1/frontpage` | Stories ranked by the Hacker News score |
| `GET /v1/new` | Stories newest-first |
| `GET /v1/items/{id}` | A single story by item id |
| `GET /v1/items/{id}/thread` | All comments under an item (nested) |
| `GET /v1/authors/{xpk}/comments` | Comments by an author |
| `GET /v1/topics` | All topics |
| `GET /v1/topics/{topic}/items` | Stories in a topic |
| `GET /healthz` | Liveness check |

Feed query params: `topic` (4-byte hex), `subtype` (0–5), `nsfw` (`1`/`true` to
include; excluded by default), `limit` (default 50), `offset` (default 0).

```sh
curl 'http://localhost:8080/v1/frontpage?limit=20'
curl 'http://localhost:8080/v1/topics'
curl 'http://localhost:8080/v1/items/0123456789abcdef01234567/thread'
```

Story shape:

```json
{
  "item_id": "…", "topic": "01020304", "headline": "…",
  "url": "…", "subtype": 0, "nsfw": false,
  "txid": "…", "vout": 0, "block_height": 12345, "block_time": 1700000000,
  "points": 7, "comment_count": 3, "score": 1.42
}
```

A topic has `created` (whether an on-chain `TopicCreation` named it) and
`story_count`. Topics referenced only by stories appear with `created: false` and
an empty `name`. The reserved zero topic (`00000000`, "no topic") is never listed.

## Test

```sh
go test ./...
```

`codec`, `store`, `scanner`, and `api` all have suites; the store and API tests run
against the in-memory backend, so they need neither a node nor a database file.

## Notes & limitations

- **NSFW** is treated as a presence flag in the TLV decode — confirm against a real
  on-chain vector whether an explicit boolean byte is intended.
- **Continuation** chunks are stored but not yet reassembled into the head item's body.
- **Feed ranking** sorts in Go after loading candidate rows; fine at this scale,
  but push scoring into SQL for very large datasets.
- The API is **read-only**; publishing CoinNews messages is out of scope.
