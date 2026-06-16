# --- build stage ---
FROM golang:1.25-alpine AS build
WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

# Build a static binary. modernc.org/sqlite is pure Go, so CGO can stay off.
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/coinnews-indexer .

# --- runtime stage ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app

# Database lives here; mount a volume to persist it.
RUN mkdir -p /data && chown app /data
VOLUME /data
WORKDIR /data
USER app

COPY --from=build /out/coinnews-indexer /usr/local/bin/coinnews-indexer

EXPOSE 8080

# Defaults are overridable via `docker run ... <flags>` or env vars
# (BITCOIN_RPC_USER / BITCOIN_RPC_PASS). The DB path points at the volume.
ENTRYPOINT ["coinnews-indexer"]
CMD ["run", "-db", "/data/coinnews.db", "-addr", ":8080"]
