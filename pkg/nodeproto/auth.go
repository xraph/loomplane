package nodeproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
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

// Challenge is what a node signs to prove it holds its key. Its encoding is
// frozen for protocol Version 1: do not reorder, rename or add fields.
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

// ChallengeContext is prefixed to the canonical JSON of a Challenge before it
// is signed, so a signature made here cannot be replayed as any other message.
const ChallengeContext = "loomplane-node-challenge-v1\n"

// signingBytes is ChallengeContext followed by the canonical JSON of c:
// fields in declaration order, with IssuedAt in RFC 3339 UTC. It fails when
// IssuedAt is outside the years JSON can encode.
func signingBytes(c Challenge) ([]byte, error) {
	c.IssuedAt = c.IssuedAt.UTC()
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return append([]byte(ChallengeContext), b...), nil
}

// Sign signs c with priv. It fails when c cannot be encoded.
func Sign(priv ed25519.PrivateKey, c Challenge) (Assertion, error) {
	msg, err := signingBytes(c)
	if err != nil {
		return Assertion{}, err
	}
	return Assertion{Challenge: c, Signature: ed25519.Sign(priv, msg)}, nil
}

// Verify checks the signature, then the audience, then the clock skew.
func Verify(pub ed25519.PublicKey, a Assertion, audience string, now time.Time) error {
	if len(pub) != ed25519.PublicKeySize || len(a.Signature) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	msg, err := signingBytes(a.Challenge)
	if err != nil || !ed25519.Verify(pub, msg, a.Signature) {
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

// AuthScheme is the Authorization scheme a node connects with:
// "LoomplaneNode <base64url(JSON Assertion)>" on GET /node/v1/connect.
const AuthScheme = "LoomplaneNode"

// MaxAuthorizationSize bounds the whole header value.
const MaxAuthorizationSize = 8 << 10

// ErrAuthorization is returned for an Authorization value that is not a node
// assertion.
var ErrAuthorization = errors.New("nodeproto: malformed node authorization")

// EncodeAuthorization is the Authorization header value that carries a.
func EncodeAuthorization(a Assertion) (string, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	return AuthScheme + " " + base64.RawURLEncoding.EncodeToString(b), nil
}

// DecodeAuthorization reads an Authorization header value made by
// EncodeAuthorization. It does not check the assertion.
func DecodeAuthorization(v string) (Assertion, error) {
	raw, ok := strings.CutPrefix(v, AuthScheme+" ")
	if !ok || raw == "" || len(v) > MaxAuthorizationSize {
		return Assertion{}, ErrAuthorization
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return Assertion{}, ErrAuthorization
	}
	var a Assertion
	if err = json.Unmarshal(b, &a); err != nil {
		return Assertion{}, ErrAuthorization
	}
	return a, nil
}
