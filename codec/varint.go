package codec

import (
	"encoding/binary"
	"errors"
)

var (
	ErrShortBuffer  = errors.New("coinnews: buffer too short")
	ErrNonCanonical = errors.New("coinnews: non-canonical compact-size encoding")
	ErrVarintRange  = errors.New("coinnews: compact-size 0xff form not allowed")
)

// putCompactSize appends a Bitcoin compact-size varint for n to dst.
// Only the 1/3/5-byte forms are produced; the 0xff (9-byte) form is never used.
func putCompactSize(dst []byte, n uint64) []byte {
	switch {
	case n <= 0xfc:
		return append(dst, byte(n))
	case n <= 0xffff:
		var b [3]byte
		b[0] = 0xfd
		binary.LittleEndian.PutUint16(b[1:], uint16(n))
		return append(dst, b[:]...)
	default: // <= 0xffffffff; protocol lengths never exceed uint32
		var b [5]byte
		b[0] = 0xfe
		binary.LittleEndian.PutUint32(b[1:], uint32(n))
		return append(dst, b[:]...)
	}
}

// readCompactSize decodes a compact-size varint from the front of b and returns
// the value and the number of bytes consumed. It rejects the 0xff form and any
// non-canonical (over-long) encoding.
func readCompactSize(b []byte) (val uint64, n int, err error) {
	if len(b) < 1 {
		return 0, 0, ErrShortBuffer
	}
	switch b[0] {
	case 0xff:
		return 0, 0, ErrVarintRange
	case 0xfe:
		if len(b) < 5 {
			return 0, 0, ErrShortBuffer
		}
		v := uint64(binary.LittleEndian.Uint32(b[1:5]))
		if v <= 0xffff {
			return 0, 0, ErrNonCanonical
		}
		return v, 5, nil
	case 0xfd:
		if len(b) < 3 {
			return 0, 0, ErrShortBuffer
		}
		v := uint64(binary.LittleEndian.Uint16(b[1:3]))
		if v <= 0xfc {
			return 0, 0, ErrNonCanonical
		}
		return v, 3, nil
	default:
		return uint64(b[0]), 1, nil
	}
}
