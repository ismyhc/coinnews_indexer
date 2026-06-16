package codec

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
)

// ItemID is a 12-byte reference to a CoinNews output, derived from its outpoint.
type ItemID [ItemIDLen]byte

var ErrBadItemID = errors.New("coinnews: invalid item id")

// ComputeItemID derives the ItemID for an outpoint:
//
//	sha256( txidInternal(32) || vout(4, little-endian uint32) )[0:12]
//
// txidInternal MUST be in raw/internal byte order — i.e. the REVERSE of the hex
// string shown by Bitcoin Core RPC and block explorers. Use ItemIDFromRPC when
// you have the display-hex txid.
func ComputeItemID(txidInternal [32]byte, vout uint32) ItemID {
	var buf [OutpointLen]byte
	copy(buf[:32], txidInternal[:])
	binary.LittleEndian.PutUint32(buf[32:], vout)
	sum := sha256.Sum256(buf[:])
	var id ItemID
	copy(id[:], sum[:ItemIDLen])
	return id
}

// ItemIDFromRPC derives an ItemID from a display-order txid hex string (as
// returned by Bitcoin Core) and a vout, handling the byte reversal.
func ItemIDFromRPC(txidHex string, vout uint32) (ItemID, error) {
	raw, err := hex.DecodeString(txidHex)
	if err != nil || len(raw) != 32 {
		return ItemID{}, ErrBadItemID
	}
	var internal [32]byte
	for i := 0; i < 32; i++ {
		internal[i] = raw[31-i] // reverse display order -> internal order
	}
	return ComputeItemID(internal, vout), nil
}

// String returns the hex encoding used for logs and database keys.
func (id ItemID) String() string { return hex.EncodeToString(id[:]) }

// ParseItemID parses the hex encoding produced by String.
func ParseItemID(s string) (ItemID, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != ItemIDLen {
		return ItemID{}, ErrBadItemID
	}
	var id ItemID
	copy(id[:], b)
	return id, nil
}
