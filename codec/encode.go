package codec

// header returns the 3-byte envelope prefix for a type tag.
func header(tag TypeTag) []byte { return []byte{Magic[0], Magic[1], byte(tag)} }

// EncodeTopicCreation serializes a TopicCreation message to a full payload.
func EncodeTopicCreation(m TopicCreation) []byte {
	out := header(TypeTopicCreation)
	out = append(out, m.Topic[:]...)
	out = append(out, m.RetentionDays)
	out = putCompactSize(out, uint64(len(m.Name)))
	return append(out, m.Name...)
}

// EncodeStory serializes a Story message to a full payload.
func EncodeStory(m Story) []byte {
	out := header(TypeStory)
	out = append(out, m.Topic[:]...)
	out = putCompactSize(out, uint64(len(m.Headline)))
	out = append(out, m.Headline...)
	return append(out, EncodeTLVs(m.TLVs)...)
}

// EncodeComment serializes a Comment. The TLV blob is taken from m.TLVs.
func EncodeComment(m Comment) []byte {
	out := header(TypeComment)
	out = append(out, m.ParentID[:]...)
	out = append(out, m.AuthorXPK[:]...)
	out = append(out, m.Sig[:]...)
	return append(out, EncodeTLVs(m.TLVs)...)
}

// EncodeVote serializes an Upvote/Downvote.
func EncodeVote(m Vote) []byte {
	out := header(m.Kind)
	out = append(out, m.TargetID[:]...)
	out = append(out, m.AuthorXPK[:]...)
	return append(out, m.Sig[:]...)
}

// EncodeContinuation serializes a Continuation chunk.
func EncodeContinuation(m Continuation) []byte {
	out := header(TypeContinuation)
	out = append(out, m.HeadID[:]...)
	out = append(out, m.Seq)
	return append(out, m.Chunk...)
}
