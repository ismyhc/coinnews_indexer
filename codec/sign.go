package codec

import (
	"crypto/sha256"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// taggedHash implements BIP-340 tagged hashing:
//
//	sha256( sha256(tag) || sha256(tag) || m... )
func taggedHash(tag string, parts ...[]byte) [32]byte {
	th := sha256.Sum256([]byte(tag))
	h := sha256.New()
	h.Write(th[:])
	h.Write(th[:])
	for _, p := range parts {
		h.Write(p)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// CommentSigHash is the digest a comment signature commits to:
//
//	tagged_hash("CoinNews/Comment", parent_id || tlv_blob)
func CommentSigHash(parent ItemID, tlvBlob []byte) [32]byte {
	return taggedHash(TagComment, parent[:], tlvBlob)
}

// VoteSigHash is the digest a vote signature commits to. The typetag byte
// (Upvote vs Downvote) is included so a signature cannot be replayed across
// vote kinds:
//
//	tagged_hash("CoinNews/Vote", typetag || target_id)
func VoteSigHash(typetag TypeTag, target ItemID) [32]byte {
	return taggedHash(TagVote, []byte{byte(typetag)}, target[:])
}

// verifySchnorr checks a BIP-340 signature of digest under x-only pubkey xpk.
func verifySchnorr(xpk []byte, sig []byte, digest [32]byte) bool {
	pub, err := schnorr.ParsePubKey(xpk)
	if err != nil {
		return false
	}
	s, err := schnorr.ParseSignature(sig)
	if err != nil {
		return false
	}
	return s.Verify(digest[:], pub)
}

// VerifyComment reports whether c carries a valid signature over its TLV blob.
func VerifyComment(c *Comment) bool {
	return verifySchnorr(c.AuthorXPK[:], c.Sig[:], CommentSigHash(c.ParentID, c.TLVBlob))
}

// VerifyVote reports whether v carries a valid signature over its target.
func VerifyVote(v *Vote) bool {
	return verifySchnorr(v.AuthorXPK[:], v.Sig[:], VoteSigHash(v.Kind, v.TargetID))
}
