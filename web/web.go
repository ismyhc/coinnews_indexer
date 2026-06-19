// Package web serves the read-only browser frontend for the CoinNews indexer.
//
// It is intentionally decoupled from the api package: it embeds a single static
// HTML page that calls the public HTTP API from the browser — it does not import
// the store, the api, or any indexer internals. To remove the frontend entirely,
// delete this package and the one mount in main.go (or pass -web=false at runtime).
package web

import (
	_ "embed"
	"net/http"
)

//go:embed index.html
var indexHTML []byte

// Handler serves the single-page read-only frontend.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
}
