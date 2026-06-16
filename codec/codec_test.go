package codec

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

func TestVarintCanonical(t *testing.T) {
	// Round-trip representative values.
	for _, v := range []uint64{0, 1, 0xfc, 0xfd, 0xffff, 0x10000, 0xffffffff} {
		enc := putCompactSize(nil, v)
		got, n, err := readCompactSize(enc)
		if err != nil || got != v || n != len(enc) {
			t.Fatalf("roundtrip %d: got %d n=%d err=%v", v, got, n, err)
		}
	}
	// Reject the 0xff form.
	if _, _, err := readCompactSize([]byte{0xff, 1, 0, 0, 0, 0, 0, 0, 0}); err != ErrVarintRange {
		t.Fatalf("0xff form: want ErrVarintRange, got %v", err)
	}
	// Reject non-canonical: 0xfd encoding a value <= 0xfc.
	if _, _, err := readCompactSize([]byte{0xfd, 0x10, 0x00}); err != ErrNonCanonical {
		t.Fatalf("non-canonical fd: want ErrNonCanonical, got %v", err)
	}
	// Reject non-canonical: 0xfe encoding a value <= 0xffff.
	if _, _, err := readCompactSize([]byte{0xfe, 0xff, 0xff, 0x00, 0x00}); err != ErrNonCanonical {
		t.Fatalf("non-canonical fe: want ErrNonCanonical, got %v", err)
	}
}

func TestComputeItemIDByteOrder(t *testing.T) {
	// txid display hex -> internal order is the reverse; both paths must agree.
	txidHex := "0000000000000000000000000000000000000000000000000000000000000001"
	raw, _ := hex.DecodeString(txidHex)
	var internal [32]byte
	for i := 0; i < 32; i++ {
		internal[i] = raw[31-i]
	}
	want := ComputeItemID(internal, 7)
	got, err := ItemIDFromRPC(txidHex, 7)
	if err != nil || got != want {
		t.Fatalf("ItemIDFromRPC mismatch: got %s want %s err=%v", got, want, err)
	}
	// Reversing the txid must change the id (guards against order bugs).
	if ComputeItemID(raw32(raw), 7) == want {
		t.Fatal("display-order and internal-order produced same ItemID")
	}
	// Stable hex round-trip.
	parsed, err := ParseItemID(got.String())
	if err != nil || parsed != got {
		t.Fatalf("ParseItemID round-trip failed: %v", err)
	}
}

func raw32(b []byte) [32]byte {
	var a [32]byte
	copy(a[:], b)
	return a
}

func TestTLVUnknownAndTruncated(t *testing.T) {
	in := []TLV{
		{Tag: TLVURL, Value: []byte("https://example.com")},
		{Tag: 0xf5, Value: []byte("private")}, // unknown/private tag must survive
	}
	enc := EncodeTLVs(in)
	out, err := DecodeTLVs(enc)
	if err != nil || len(out) != 2 {
		t.Fatalf("decode: err=%v len=%d", err, len(out))
	}
	if v, ok := FindFirst(out, 0xf5); !ok || string(v) != "private" {
		t.Fatal("unknown tag not preserved")
	}
	// Truncated: declare length longer than remaining bytes.
	if _, err := DecodeTLVs([]byte{TLVBody, 0x05, 'a', 'b'}); err != ErrTruncatedTLV {
		t.Fatalf("want ErrTruncatedTLV, got %v", err)
	}
}

func TestEnvelopeErrors(t *testing.T) {
	if _, err := DecodeMessage([]byte{0x00, 0x00}); err != ErrNotCoinNews {
		t.Fatalf("want ErrNotCoinNews, got %v", err)
	}
	// Valid magic, unknown tag.
	if _, err := DecodeMessage([]byte{Magic[0], Magic[1], 0x7f}); err != ErrUnknownTypeTag {
		t.Fatalf("want ErrUnknownTypeTag, got %v", err)
	}
}

func TestRoundTripTopicAndStory(t *testing.T) {
	tc := TopicCreation{Topic: Topic{1, 2, 3, 4}, RetentionDays: 0, Name: "general"}
	got, err := DecodeMessage(EncodeTopicCreation(tc))
	if err != nil {
		t.Fatal(err)
	}
	if g := got.(TopicCreation); g != tc {
		t.Fatalf("topic round-trip: %+v != %+v", g, tc)
	}

	st := Story{
		Topic:    Topic{1, 2, 3, 4},
		Headline: "Hello CoinNews",
		TLVs:     []TLV{{Tag: TLVURL, Value: []byte("https://x.test")}, {Tag: TLVSubtype, Value: []byte{byte(SubtypeLink)}}},
	}
	got, err = DecodeMessage(EncodeStory(st))
	if err != nil {
		t.Fatal(err)
	}
	gs := got.(Story)
	if gs.Topic != st.Topic || gs.Headline != st.Headline || len(gs.TLVs) != 2 {
		t.Fatalf("story round-trip mismatch: %+v", gs)
	}
}

func TestVoteSignAndVerify(t *testing.T) {
	priv, _ := btcec.NewPrivateKey()
	xpk := schnorr.SerializePubKey(priv.PubKey())

	target := ComputeItemID([32]byte{9, 9, 9}, 0)
	digest := VoteSigHash(TypeUpvote, target)
	sig, err := schnorr.Sign(priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}

	var v Vote
	v.Kind = TypeUpvote
	v.TargetID = target
	copy(v.AuthorXPK[:], xpk)
	copy(v.Sig[:], sig.Serialize())

	if !VerifyVote(&v) {
		t.Fatal("valid vote failed verification")
	}
	// A vote is exactly 111 bytes on the wire.
	if enc := EncodeVote(v); len(enc) != 111 {
		t.Fatalf("vote wire length = %d, want 111", len(enc))
	}
	// Downvote replay: same sig under a different kind must fail (domain sep).
	v.Kind = TypeDownvote
	if VerifyVote(&v) {
		t.Fatal("vote signature replayed across kind — domain separation broken")
	}
}

func TestCommentSignAndVerify(t *testing.T) {
	priv, _ := btcec.NewPrivateKey()
	xpk := schnorr.SerializePubKey(priv.PubKey())

	parent := ComputeItemID([32]byte{1}, 0)
	tlvs := []TLV{{Tag: TLVBody, Value: []byte("nice post")}}
	blob := EncodeTLVs(tlvs)
	digest := CommentSigHash(parent, blob)
	sig, _ := schnorr.Sign(priv, digest[:])

	var c Comment
	c.ParentID = parent
	copy(c.AuthorXPK[:], xpk)
	copy(c.Sig[:], sig.Serialize())
	c.TLVs = tlvs

	dec, err := DecodeMessage(EncodeComment(c))
	if err != nil {
		t.Fatal(err)
	}
	gc := dec.(Comment)
	if !bytes.Equal(gc.TLVBlob, blob) {
		t.Fatal("decoded TLV blob differs from signed bytes")
	}
	if !VerifyComment(&gc) {
		t.Fatal("valid comment failed verification")
	}
}

func TestContinuationBounds(t *testing.T) {
	m := Continuation{HeadID: ComputeItemID([32]byte{2}, 1), Seq: 0, Chunk: bytes.Repeat([]byte{0xab}, ContinuationChunk)}
	dec, err := DecodeMessage(EncodeContinuation(m))
	if err != nil {
		t.Fatal(err)
	}
	if gc := dec.(Continuation); !bytes.Equal(gc.Chunk, m.Chunk) || gc.Seq != 0 {
		t.Fatal("continuation round-trip mismatch")
	}
	// Over-size chunk must be rejected.
	over := EncodeContinuation(Continuation{HeadID: m.HeadID, Chunk: bytes.Repeat([]byte{1}, ContinuationChunk+1)})
	if _, err := DecodeMessage(over); err != ErrMalformed {
		t.Fatalf("oversize chunk: want ErrMalformed, got %v", err)
	}
}
