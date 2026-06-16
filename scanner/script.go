// Package scanner walks blocks in canonical order, extracts OP_RETURN payloads,
// decodes CoinNews messages, and (eventually) persists them. This file holds the
// node-free script parsing so it can be unit-tested with golden scripts.
package scanner

import "encoding/hex"

const (
	opReturn    = 0x6a
	opPushData1 = 0x4c
	opPushData2 = 0x4d
	opPushData4 = 0x4e
)

// OpReturnPayload returns the data pushed by a nulldata (OP_RETURN) script, or
// (nil,false) if the script is not a single-push OP_RETURN. scriptHex is the
// scriptPubKey hex from a getblock verbosity-2 vout.
func OpReturnPayload(scriptHex string) ([]byte, bool) {
	b, err := hex.DecodeString(scriptHex)
	if err != nil || len(b) < 1 || b[0] != opReturn {
		return nil, false
	}
	return parsePush(b[1:])
}

// parsePush decodes the first data push at the front of b.
func parsePush(b []byte) ([]byte, bool) {
	if len(b) < 1 {
		return nil, false
	}
	op := b[0]
	b = b[1:]
	var n int
	switch {
	case op >= 0x01 && op <= 0x4b: // direct push of op bytes
		n = int(op)
	case op == opPushData1:
		if len(b) < 1 {
			return nil, false
		}
		n = int(b[0])
		b = b[1:]
	case op == opPushData2:
		if len(b) < 2 {
			return nil, false
		}
		n = int(b[0]) | int(b[1])<<8
		b = b[2:]
	default: // OP_PUSHDATA4 or non-push opcode: not a CoinNews payload
		return nil, false
	}
	if len(b) < n {
		return nil, false
	}
	return b[:n], true
}
