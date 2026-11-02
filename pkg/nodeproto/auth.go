package nodeproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// MaxSkew is how far IssuedAt may sit from the verifier's clock, either way.
const MaxSkew = 2 * time.Minute

// Errors returned by Verify.
var (
	ErrBadSignature = errors.New("nodeproto: bad signature")
	ErrAudience     = errors.New("nodeproto: wrong audience")
	ErrSkew         = errors.New("nodeproto: challenge outside the allowed clock skew")
)

// Challenge is what a node signs to prove it holds its key.
type Challenge struct {
	NodeID   string    `json:"node_id"`
	Audience string    `json:"audience"`
	Nonce    string    `json:"nonce"`
	IssuedAt time.Time `json:"issued_at"`
}

// Assertion is a signed Challenge.
type Assertion struct {
	Challenge Challenge `json:"challenge"`
	Signature []byte    `json:"signature"`
}

// signingBytes is the canonical JSON of c: fields in declaration order, with
// IssuedAt in RFC 3339 UTC.
func signingBytes(c Challenge) []byte {
	c.IssuedAt = c.IssuedAt.UTC()
	b, err := json.Marshal(c)
	if err != nil {
		// Challenge holds only strings and a time, so this cannot happen.
		panic(err)
	}
	return b
}

// Sign signs c with priv.
func Sign(priv ed25519.PrivateKey, c Challenge) Assertion {
	return Assertion{Challenge: c, Signature: ed25519.Sign(priv, signingBytes(c))}
}

// Verify checks the signature, then the audience, then the clock skew.
func Verify(pub ed25519.PublicKey, a Assertion, audience string, now time.Time) error {
	if len(pub) != ed25519.PublicKeySize || len(a.Signature) != ed25519.SignatureSize ||
		!ed25519.Verify(pub, signingBytes(a.Challenge), a.Signature) {
		return ErrBadSignature
	}
	if a.Challenge.Audience != audience {
		return ErrAudience
	}
	d := now.Sub(a.Challenge.IssuedAt)
	if d < 0 {
		d = -d
	}
	if d > MaxSkew {
		return ErrSkew
	}
	return nil
}

// Fingerprint names a public key as "sha256:" plus lowercase hex.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "sha256:" + hex.EncodeToString(sum[:])
}
