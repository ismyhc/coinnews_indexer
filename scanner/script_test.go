package scanner

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestOpReturnPayload(t *testing.T) {
	// OP_RETURN OP_PUSH(5) "CNxxx"
	payload := []byte{0x43, 0x4e, 0x01, 0x00, 0x00}
	script := append([]byte{opReturn, byte(len(payload))}, payload...)
	got, ok := OpReturnPayload(hex.EncodeToString(script))
	if !ok || !bytes.Equal(got, payload) {
		t.Fatalf("direct push: ok=%v got=%x", ok, got)
	}

	// OP_RETURN OP_PUSHDATA1 80 <80 bytes>
	big := bytes.Repeat([]byte{0xab}, 80)
	script = append([]byte{opReturn, opPushData1, 80}, big...)
	got, ok = OpReturnPayload(hex.EncodeToString(script))
	if !ok || !bytes.Equal(got, big) {
		t.Fatalf("pushdata1: ok=%v len=%d", ok, len(got))
	}

	// Non-OP_RETURN script must be rejected.
	if _, ok := OpReturnPayload("76a914"); ok {
		t.Fatal("p2pkh prefix accepted as OP_RETURN")
	}
}
