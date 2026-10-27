package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"
)

func TestGrantDue(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	g := Grant{Expiry: now.Add(time.Hour)}
	if !g.Due(now, 24*time.Hour) {
		t.Error("a grant expiring in 1h should be due within 24h")
	}
	if g.Due(now, 30*time.Minute) {
		t.Error("a grant expiring in 1h should not be due within 30m")
	}
}

// A Vercel token doesn't expire, so its grant has no expiry and is never
// due for renewal.
func TestGrantDueNeverForAZeroExpiry(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	if (Grant{AccessToken: "at"}).Due(now, 365*24*time.Hour) {
		t.Error("a grant without an expiry is due")
	}
}

func TestNewPKCEIsS256(t *testing.T) {
	verifier, challenge, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := base64.RawURLEncoding.DecodeString(verifier); err != nil || len(raw) != 32 {
		t.Fatalf("verifier %q is not 32 base64url bytes: %v", verifier, err)
	}
	sum := sha256.Sum256([]byte(verifier))
	if challenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatalf("challenge %q is not the verifier's S256", challenge)
	}
	if again, _, _ := NewPKCE(); again == verifier {
		t.Fatal("two verifiers are the same")
	}
}
