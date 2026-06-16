package codec

import "errors"

var (
	// ErrNotCoinNews means the payload does not start with the "CN" magic.
	ErrNotCoinNews = errors.New("coinnews: not a CoinNews payload")
	// ErrUnknownTypeTag means the magic matched but the TypeTag is unknown.
	// Callers should ignore the message but may treat this distinctly from
	// ErrNotCoinNews (a rise in unknown tags signals a newer publisher).
	ErrUnknownTypeTag = errors.New("coinnews: unknown type tag")
	// ErrMalformed means a known message type failed to parse.
	ErrMalformed = errors.New("coinnews: malformed message")
)

// Message is implemented by every decoded CoinNews message type.
type Message interface{ Type() TypeTag }

type Topic [TopicLen]byte
type XOnlyPubKey [XOnlyPubKeyLen]byte
type SchnorrSig [SchnorrSigLen]byte

// ZeroTopic (0x00000000) is reserved by the spec for "no topic" / global items
// that don't slot into a category. It is a valid topic field on a Story but must
// never be presented as a named topic in a topic listing.
var ZeroTopic Topic

// IsZero reports whether t is the reserved "no topic" value.
func (t Topic) IsZero() bool { return t == ZeroTopic }

// TopicCreation (0x01) establishes a topic's canonical name.
type TopicCreation struct {
	Topic         Topic
	RetentionDays byte // 0 == infinite (RetentionInfinite)
	Name          string
}

func (TopicCreation) Type() TypeTag { return TypeTopicCreation }

// Story (0x02) is an unsigned headline scoped to a topic, with optional TLVs.
type Story struct {
	Topic    Topic
	Headline string
	TLVs     []TLV
}

func (Story) Type() TypeTag { return TypeStory }

// Comment (0x03) is a signed reply to a parent item.
type Comment struct {
	ParentID  ItemID
	AuthorXPK XOnlyPubKey
	Sig       SchnorrSig
	TLVBlob   []byte // raw TLV bytes, exactly as signed
	TLVs      []TLV
}

func (Comment) Type() TypeTag { return TypeComment }

// Vote (0x04 upvote / 0x05 downvote) is a signed vote on a target item.
type Vote struct {
	Kind      TypeTag // TypeUpvote or TypeDownvote
	TargetID  ItemID
	AuthorXPK XOnlyPubKey
	Sig       SchnorrSig
}

func (v Vote) Type() TypeTag { return v.Kind }

// Continuation (0x06) carries an overflow chunk of a larger message body.
type Continuation struct {
	HeadID ItemID
	Seq    byte
	Chunk  []byte
}

func (Continuation) Type() TypeTag { return TypeContinuation }

// DecodeMessage parses a raw OP_RETURN payload into a CoinNews message.
// It returns ErrNotCoinNews when the magic is absent and ErrUnknownTypeTag for
// an unrecognized type — both of which a scanner ignores without failing.
func DecodeMessage(data []byte) (Message, error) {
	if len(data) < MagicLen+TypeTagLen {
		return nil, ErrNotCoinNews
	}
	if data[0] != Magic[0] || data[1] != Magic[1] {
		return nil, ErrNotCoinNews
	}
	tag := TypeTag(data[2])
	body := data[MagicLen+TypeTagLen:]

	switch tag {
	case TypeTopicCreation:
		return decodeTopicCreation(body)
	case TypeStory:
		return decodeStory(body)
	case TypeComment:
		return decodeComment(body)
	case TypeUpvote, TypeDownvote:
		return decodeVote(tag, body)
	case TypeContinuation:
		return decodeContinuation(body)
	default:
		return nil, ErrUnknownTypeTag
	}
}

func decodeTopicCreation(b []byte) (Message, error) {
	if len(b) < TopicLen+1 {
		return nil, ErrMalformed
	}
	var m TopicCreation
	copy(m.Topic[:], b[:TopicLen])
	b = b[TopicLen:]
	m.RetentionDays = b[0]
	b = b[1:]
	nameLen, used, err := readCompactSize(b)
	if err != nil {
		return nil, err
	}
	b = b[used:]
	if uint64(len(b)) != nameLen {
		return nil, ErrMalformed
	}
	m.Name = string(b)
	return m, nil
}

func decodeStory(b []byte) (Message, error) {
	if len(b) < TopicLen {
		return nil, ErrMalformed
	}
	var m Story
	copy(m.Topic[:], b[:TopicLen])
	b = b[TopicLen:]
	hLen, used, err := readCompactSize(b)
	if err != nil {
		return nil, err
	}
	b = b[used:]
	if hLen > HeadlineMaxLen || uint64(len(b)) < hLen {
		return nil, ErrMalformed
	}
	m.Headline = string(b[:hLen])
	b = b[hLen:]
	tlvs, err := DecodeTLVs(b)
	if err != nil {
		return nil, err
	}
	m.TLVs = tlvs
	return m, nil
}

func decodeComment(b []byte) (Message, error) {
	const fixed = ItemIDLen + XOnlyPubKeyLen + SchnorrSigLen
	if len(b) < fixed {
		return nil, ErrMalformed
	}
	var m Comment
	copy(m.ParentID[:], b[:ItemIDLen])
	b = b[ItemIDLen:]
	copy(m.AuthorXPK[:], b[:XOnlyPubKeyLen])
	b = b[XOnlyPubKeyLen:]
	copy(m.Sig[:], b[:SchnorrSigLen])
	b = b[SchnorrSigLen:]
	m.TLVBlob = append([]byte(nil), b...) // commit the exact signed bytes
	tlvs, err := DecodeTLVs(b)
	if err != nil {
		return nil, err
	}
	m.TLVs = tlvs
	return m, nil
}

func decodeVote(tag TypeTag, b []byte) (Message, error) {
	const fixed = ItemIDLen + XOnlyPubKeyLen + SchnorrSigLen
	if len(b) != fixed { // votes are fixed-length: no TLVs
		return nil, ErrMalformed
	}
	var m Vote
	m.Kind = tag
	copy(m.TargetID[:], b[:ItemIDLen])
	b = b[ItemIDLen:]
	copy(m.AuthorXPK[:], b[:XOnlyPubKeyLen])
	b = b[XOnlyPubKeyLen:]
	copy(m.Sig[:], b[:SchnorrSigLen])
	return m, nil
}

func decodeContinuation(b []byte) (Message, error) {
	if len(b) < ItemIDLen+1 {
		return nil, ErrMalformed
	}
	var m Continuation
	copy(m.HeadID[:], b[:ItemIDLen])
	b = b[ItemIDLen:]
	m.Seq = b[0]
	b = b[1:]
	if len(b) < 1 || len(b) > ContinuationChunk {
		return nil, ErrMalformed
	}
	m.Chunk = append([]byte(nil), b...)
	return m, nil
}
