package nodeproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// zeroSig is 64 zero bytes in base64, as JSON carries a []byte.
var zeroSig = base64.StdEncoding.EncodeToString(make([]byte, 64))

func TestVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	mk := func(skew time.Duration) Assertion {
		a, err := Sign(priv, Challenge{NodeID: "node_1", Audience: "loomplane", Nonce: "n1", IssuedAt: now.Add(skew)})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	var overflow, underflow Assertion
	if err := json.Unmarshal([]byte(`{"challenge":{"issued_at":"9999-12-31T23:59:59-23:59"},"signature":"`+zeroSig+`"}`), &overflow); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"challenge":{"issued_at":"0000-01-01T00:00:00+23:59"},"signature":"`+zeroSig+`"}`), &underflow); err != nil {
		t.Fatal(err)
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
		{"UTC year 10000", overflow, "loomplane", ErrBadSignature},
		{"UTC year -1", underflow, "loomplane", ErrBadSignature},
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
	a, err := Sign(priv, c)
	if err != nil {
		t.Fatal(err)
	}
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

func TestSign_rejectsUnencodableTime(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	c := Challenge{IssuedAt: time.Date(9999, 12, 31, 23, 59, 59, 0, time.FixedZone("x", -(24*3600-60)))}
	if _, err := Sign(priv, c); err == nil {
		t.Fatal("expected an error")
	}
}

func TestSigningBytesGolden(t *testing.T) {
	got, err := signingBytes(Challenge{
		NodeID: "n1", Audience: "https://lp.example.com", Nonce: "abc",
		IssuedAt: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "loomplane-node-challenge-v1\n" +
		`{"node_id":"n1","audience":"https://lp.example.com","nonce":"abc","issued_at":"2026-11-02T10:00:00Z"}`
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
