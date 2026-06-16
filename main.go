package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"net/http"

	"github.com/ismyhc/coinnews-indexer/api"
	"github.com/ismyhc/coinnews-indexer/rpc"
	"github.com/ismyhc/coinnews-indexer/scanner"
	"github.com/ismyhc/coinnews-indexer/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "run":
		cmdRun(os.Args[2:])
	case "index":
		cmdIndex(os.Args[2:])
	case "serve":
		cmdServe(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: coinnews-indexer <run|index|serve> [flags]")
	fmt.Fprintln(os.Stderr, "  run    index in the background and serve the API (recommended for live)")
	fmt.Fprintln(os.Stderr, "  index  scan into the store, then exit (or follow with -poll)")
	fmt.Fprintln(os.Stderr, "  serve  serve the read-only API over an existing store")
	os.Exit(2)
}

// cmdRun is the recommended live mode: one process that continuously indexes in
// a background goroutine and serves the API, sharing a single store handle.
func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	url := fs.String("rpc", "http://127.0.0.1:8332", "Bitcoin Core RPC URL")
	user := fs.String("rpcuser", os.Getenv("BITCOIN_RPC_USER"), "RPC username")
	pass := fs.String("rpcpass", os.Getenv("BITCOIN_RPC_PASS"), "RPC password")
	dbPath := fs.String("db", "coinnews.db", "SQLite database path")
	addr := fs.String("addr", ":8080", "API listen address")
	start := fs.Int64("start", 0, "start block height (only used on a fresh DB)")
	poll := fs.Duration("poll", 15*time.Second, "interval to re-scan for new blocks")
	_ = fs.Parse(args)

	if *poll <= 0 {
		*poll = 15 * time.Second
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	st, err := store.OpenSQLite(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// Indexer loop — resilient: a transient RPC error is logged and retried on
	// the next tick rather than taking down the API.
	client := rpc.New(*url, *user, *pass)
	sc := scanner.New(client, st, log.Default())
	go func() {
		for {
			if err := sc.Run(ctx, *start); err != nil && ctx.Err() == nil {
				log.Printf("index: %v (retrying in %s)", err, *poll)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(*poll):
			}
		}
	}()

	// API server.
	srv := &http.Server{Addr: *addr, Handler: api.New(st)}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("indexing %s and serving API on %s (poll %s)", *url, *addr, *poll)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}

// cmdIndex scans the chain into the store, persisting CoinNews messages. With
// -poll it keeps running, picking up new blocks on an interval.
func cmdIndex(args []string) {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	url := fs.String("rpc", "http://127.0.0.1:8332", "Bitcoin Core RPC URL")
	user := fs.String("rpcuser", os.Getenv("BITCOIN_RPC_USER"), "RPC username")
	pass := fs.String("rpcpass", os.Getenv("BITCOIN_RPC_PASS"), "RPC password")
	dbPath := fs.String("db", "coinnews.db", "SQLite database path")
	start := fs.Int64("start", 0, "start block height (used only on a fresh DB)")
	poll := fs.Duration("poll", 0, "if >0, keep scanning on this interval")
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	st, err := store.OpenSQLite(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	client := rpc.New(*url, *user, *pass)
	sc := scanner.New(client, st, log.Default())

	for {
		if err := sc.Run(ctx, *start); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Fatalf("scan: %v", err)
		}
		if *poll <= 0 {
			log.Printf("caught up to chain tip; exiting")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(*poll):
		}
	}
}

// cmdServe exposes the read-only JSON API over the store.
func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := fs.String("db", "coinnews.db", "SQLite database path")
	addr := fs.String("addr", ":8080", "listen address")
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	st, err := store.OpenSQLite(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	srv := &http.Server{Addr: *addr, Handler: api.New(st)}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("serving CoinNews API on %s", *addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}
