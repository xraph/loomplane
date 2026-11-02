package nodeproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	mk := func(skew time.Duration) Assertion {
		return Sign(priv, Challenge{NodeID: "node_1", Audience: "loomplane", Nonce: "n1", IssuedAt: now.Add(skew)})
	}
	flipped := mk(0)
	flipped.Signature[0] ^= 0xff

	tests := []struct {
		name     string
		a        Assertion
		audience string
		want     error
	}{
		{"valid", mk(0), "loomplane", nil},
		{"flipped signature", flipped, "loomplane", ErrBadSignature},
		{"wrong audience", mk(0), "other", ErrAudience},
		{"121s in the future", mk(121 * time.Second), "loomplane", ErrSkew},
		{"121s in the past", mk(-121 * time.Second), "loomplane", ErrSkew},
		{"119s in the future", mk(119 * time.Second), "loomplane", nil},
		{"119s in the past", mk(-119 * time.Second), "loomplane", nil},
		{"short signature", Assertion{Challenge: mk(0).Challenge, Signature: []byte{1}}, "loomplane", ErrBadSignature},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Verify(pub, tt.a, tt.audience, now)
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
	if err := Verify(ed25519.PublicKey{1, 2}, mk(0), "loomplane", now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("short key: got %v, want ErrBadSignature", err)
	}
}

func TestSignatureIgnoresTimeZone(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	c := Challenge{NodeID: "n", Audience: "a", Nonce: "x", IssuedAt: now}
	a := Sign(priv, c)
	a.Challenge.IssuedAt = now.In(time.FixedZone("x", 3600))
	if err := Verify(pub, a, "a", now); err != nil {
		t.Fatalf("same instant in another zone should verify: %v", err)
	}
}

func TestFingerprintStable(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	a, b := Fingerprint(pub), Fingerprint(pub)
	if a != b {
		t.Fatal("fingerprint changed between calls")
	}
	if !strings.HasPrefix(a, "sha256:") || len(a) != len("sha256:")+64 || a != strings.ToLower(a) {
		t.Fatalf("unexpected fingerprint %q", a)
	}
}
