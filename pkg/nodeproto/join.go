package nodeproto

import (
	"crypto/ed25519"
	"encoding/json"
)

// JoinContext is prefixed to the canonical JSON of a join proof before it is
// signed. It differs from ChallengeContext, so a join signature can never
// pass as a connect assertion, or the other way round.
const JoinContext = "loomplane-node-join-v1\n"

// joinProof is what a joining machine signs with its new key, to show it
// holds the private half of the public key it asks to join with. Its encoding
// is frozen for protocol Version 1: do not reorder, rename or add fields.
type joinProof struct {
	Token     string `json:"token"`
	Name      string `json:"name"`
	PublicKey []byte `json:"public_key"`
}

// joinSigningBytes is JoinContext followed by the canonical JSON of the
// proof: fields in declaration order, the key in standard base64. Name is
// the name exactly as the join request carries it, empty included.
func joinSigningBytes(token, name string, pub ed25519.PublicKey) []byte {
	b, _ := json.Marshal(joinProof{Token: token, Name: name, PublicKey: pub}) //nolint:errcheck,errchkjson // strings and bytes always marshal
	return append([]byte(JoinContext), b...)
}

// SignJoin signs a join request for token and name with priv. The public key
// the request carries must be priv's.
func SignJoin(priv ed25519.PrivateKey, token, name string) []byte {
	pub, _ := priv.Public().(ed25519.PublicKey) //nolint:errcheck // an ed25519 private key's public half is always one
	return ed25519.Sign(priv, joinSigningBytes(token, name, pub))
}

// VerifyJoin checks that sig is pub's signature over a join with token and
// name. It returns ErrBadSignature for anything else.
func VerifyJoin(pub ed25519.PublicKey, token, name string, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	if !ed25519.Verify(pub, joinSigningBytes(token, name, pub), sig) {
		return ErrBadSignature
	}
	return nil
}
