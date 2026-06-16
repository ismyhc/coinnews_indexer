package codec

import "errors"

var ErrTruncatedTLV = errors.New("coinnews: truncated TLV tuple")

// TLV is a single tag-length-value tuple.
type TLV struct {
	Tag   byte
	Value []byte
}

// EncodeTLVs serializes tuples as: tag(1) || varint length || value, concatenated.
func EncodeTLVs(tlvs []TLV) []byte {
	var out []byte
	for _, t := range tlvs {
		out = append(out, t.Tag)
		out = putCompactSize(out, uint64(len(t.Value)))
		out = append(out, t.Value...)
	}
	return out
}

// DecodeTLVs reads tuples until the slice is exhausted. Unknown tags are
// returned as-is (callers ignore tags they don't recognize); a tuple whose
// declared length runs past the end of the buffer is an error.
func DecodeTLVs(b []byte) ([]TLV, error) {
	var out []TLV
	for len(b) > 0 {
		tag := b[0]
		b = b[1:]
		n, used, err := readCompactSize(b)
		if err != nil {
			return nil, err
		}
		b = b[used:]
		if uint64(len(b)) < n {
			return nil, ErrTruncatedTLV
		}
		val := make([]byte, n)
		copy(val, b[:n])
		b = b[n:]
		out = append(out, TLV{Tag: tag, Value: val})
	}
	return out, nil
}

// FindFirst returns the value of the first TLV with the given tag, or nil/false.
func FindFirst(tlvs []TLV, tag byte) ([]byte, bool) {
	for _, t := range tlvs {
		if t.Tag == tag {
			return t.Value, true
		}
	}
	return nil, false
}

// FindAll returns the values of every TLV with the given tag.
func FindAll(tlvs []TLV, tag byte) [][]byte {
	var out [][]byte
	for _, t := range tlvs {
		if t.Tag == tag {
			out = append(out, t.Value)
		}
	}
	return out
}
