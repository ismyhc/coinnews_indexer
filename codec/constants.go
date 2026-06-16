// Package codec implements encoding and decoding of CoinNews protocol messages
// carried in Bitcoin OP_RETURN outputs. It depends only on standard library +
// secp256k1/Schnorr; it has no knowledge of a node, RPC, or database so it can
// be unit-tested against golden vectors in isolation.
//
// See docs/coinnews-protocol.md for the wire format this package enforces.
package codec

// Magic prefix carried at the start of every CoinNews payload: ASCII "CN".
var Magic = [2]byte{0x43, 0x4E}

// TypeTag identifies the message category (the byte after the magic).
type TypeTag byte

const (
	TypeTopicCreation TypeTag = 0x01
	TypeStory         TypeTag = 0x02
	TypeComment       TypeTag = 0x03
	TypeUpvote        TypeTag = 0x04
	TypeDownvote      TypeTag = 0x05
	TypeContinuation  TypeTag = 0x06
)

// Fixed field widths, in bytes.
const (
	MagicLen          = 2
	TypeTagLen        = 1
	TopicLen          = 4
	ItemIDLen         = 12
	XOnlyPubKeyLen    = 32
	SchnorrSigLen     = 64
	OutpointLen       = 36 // txid(32) || vout(4)
	ContinuationChunk = 63 // max chunk bytes per continuation
	HeadlineMaxLen    = 252
	MaxReassembledLen = 8192 // 8 KiB cap on a reassembled TLV section
)

// TLV tag bytes in the spec-defined region (0x01-0x7f).
const (
	TLVURL        byte = 0x01 // absolute URI (utf8)
	TLVBody       byte = 0x02 // free text (utf8)
	TLVLang       byte = 0x03 // BCP-47 code
	TLVNSFW       byte = 0x04 // content flag
	TLVSubtype    byte = 0x05 // story category (see Subtype)
	TLVMediaHash  byte = 0x06 // 32-byte off-chain content hash
	TLVReplyQuote byte = 0x07 // quoted excerpt (utf8)
)

// TLV namespace region boundaries.
const (
	TLVSpecMax     byte = 0x7f // 0x01-0x7f spec-defined
	TLVRegistryMax byte = 0xef // 0x80-0xef out-of-band registry
	// 0xf0-0xff is application-private.
)

// Subtype classifies a Story (TLVSubtype value).
type Subtype byte

const (
	SubtypeLink Subtype = 0
	SubtypeText Subtype = 1
	SubtypeAsk  Subtype = 2
	SubtypeShow Subtype = 3
	SubtypePoll Subtype = 4
	SubtypeJob  Subtype = 5
)

// RetentionInfinite is the retention_days value meaning "keep forever".
const RetentionInfinite byte = 0x00

// Tagged-hash domain tags for BIP-340 signatures.
const (
	TagComment = "CoinNews/Comment"
	TagVote    = "CoinNews/Vote"
)
