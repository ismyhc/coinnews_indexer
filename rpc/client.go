// Package rpc is a minimal Bitcoin Core JSON-RPC client — just the calls the
// indexer needs to walk blocks and read OP_RETURN outputs.
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client talks to a Bitcoin Core node over JSON-RPC.
type Client struct {
	url        string
	user, pass string
	http       *http.Client
}

// New builds a client. url is e.g. "http://127.0.0.1:8332".
func New(url, user, pass string) *Client {
	return &Client{url: url, user: user, pass: pass, http: &http.Client{Timeout: 30 * time.Second}}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) call(ctx context.Context, method string, params []any, out any) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "1.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.user, c.pass)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var r rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("decode %s: %w", method, err)
	}
	if r.Error != nil {
		return fmt.Errorf("rpc %s: %s (code %d)", method, r.Error.Message, r.Error.Code)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// GetBlockCount returns the height of the most-work fully-validated chain tip.
func (c *Client) GetBlockCount(ctx context.Context) (int64, error) {
	var h int64
	err := c.call(ctx, "getblockcount", nil, &h)
	return h, err
}

// GetBlockHash returns the block hash at the given height.
func (c *Client) GetBlockHash(ctx context.Context, height int64) (string, error) {
	var hash string
	err := c.call(ctx, "getblockhash", []any{height}, &hash)
	return hash, err
}

// Vout is a transaction output with its scriptPubKey.
type Vout struct {
	N            uint32 `json:"n"`
	ScriptPubKey struct {
		Hex  string `json:"hex"`
		Type string `json:"type"` // "nulldata" for OP_RETURN
		Asm  string `json:"asm"`
	} `json:"scriptPubKey"`
}

// Tx is a transaction within a verbose block.
type Tx struct {
	TxID string `json:"txid"`
	Vout []Vout `json:"vout"`
}

// Block is the result of getblock <hash> 2 (verbose with tx+vout detail).
type Block struct {
	Hash              string `json:"hash"`
	Height            int64  `json:"height"`
	Time              int64  `json:"time"`
	PreviousBlockHash string `json:"previousblockhash"`
	Tx                []Tx   `json:"tx"`
}

// GetBlock fetches a block with full transaction detail (verbosity 2).
func (c *Client) GetBlock(ctx context.Context, hash string) (*Block, error) {
	var b Block
	if err := c.call(ctx, "getblock", []any{hash, 2}, &b); err != nil {
		return nil, err
	}
	return &b, nil
}
