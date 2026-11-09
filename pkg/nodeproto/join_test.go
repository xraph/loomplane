package nodeproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestVerifyJoin(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPub, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sig := SignJoin(priv, "lpj_live_abc", "web-1")
	flipped := append([]byte(nil), sig...)
	flipped[0] ^= 0xff

	tests := []struct {
		name        string
		pub         ed25519.PublicKey
		token, node string
		sig         []byte
		want        error
	}{
		{"valid", pub, "lpj_live_abc", "web-1", sig, nil},
		{"empty name signed as empty", pub, "lpj_live_abc", "", SignJoin(priv, "lpj_live_abc", ""), nil},
		{"flipped signature", pub, "lpj_live_abc", "web-1", flipped, ErrBadSignature},
		{"another token", pub, "lpj_live_xyz", "web-1", sig, ErrBadSignature},
		{"another name", pub, "lpj_live_abc", "web-2", sig, ErrBadSignature},
		{"name trimmed by the server is not the one signed", pub, "lpj_live_abc", " web-1", sig, ErrBadSignature},
		// Someone with a token who copies another machine's public key can't
		// sign for it: their own signature doesn't verify under that key.
		{"squatted key", otherPub, "lpj_live_abc", "web-1", sig, ErrBadSignature},
		{"signed by the squatter", pub, "lpj_live_abc", "web-1", SignJoin(otherPriv, "lpj_live_abc", "web-1"), ErrBadSignature},
		{"no signature", pub, "lpj_live_abc", "web-1", nil, ErrBadSignature},
		{"short key", pub[:8], "lpj_live_abc", "web-1", sig, ErrBadSignature},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := VerifyJoin(tt.pub, tt.token, tt.node, tt.sig); !errors.Is(err, tt.want) {
				t.Fatalf("VerifyJoin = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestJoinSigningBytesFrozen pins the exact bytes a v1 join signs, so a
// change to the encoding shows up as a broken protocol, not a passing test.
func TestJoinSigningBytesFrozen(t *testing.T) {
	got := string(joinSigningBytes("lpj_live_abc", "web-1", ed25519.PublicKey{1, 2, 3}))
	want := "loomplane-node-join-v1\n" + `{"token":"lpj_live_abc","name":"web-1","public_key":"AQID"}`
	if got != want {
		t.Fatalf("join signing bytes:\n got %q\nwant %q", got, want)
	}
}

// TestJoinAndChallengeSignaturesDontCross checks domain separation both ways.
func TestJoinAndChallengeSignaturesDontCross(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Sign(priv, Challenge{NodeID: "n", Audience: "a", Nonce: "x", IssuedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if VerifyJoin(pub, "n", "a", a.Signature) == nil {
		t.Fatal("a connect signature passed as a join signature")
	}
	if !strings.HasPrefix(string(joinSigningBytes("t", "n", pub)), JoinContext) || JoinContext == ChallengeContext {
		t.Fatal("join bytes are not separated from challenge bytes")
	}
}
