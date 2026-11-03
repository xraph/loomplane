package nodeproto

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAuthorizationRoundTrip(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	a, err := Sign(priv, Challenge{NodeID: "node_1", Audience: "https://loom.example.com", Nonce: "n", IssuedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	v, err := EncodeAuthorization(a)
	if err != nil || !strings.HasPrefix(v, AuthScheme+" ") || strings.ContainsAny(v[len(AuthScheme)+1:], "+/= ") {
		t.Fatalf("encoded %q (%v)", v, err)
	}
	got, err := DecodeAuthorization(v)
	if err != nil || got.Challenge.NodeID != "node_1" || string(got.Signature) != string(a.Signature) || !got.Challenge.IssuedAt.Equal(a.Challenge.IssuedAt) {
		t.Fatalf("decoded %+v (%v)", got, err)
	}
}

func TestDecodeAuthorizationRefuses(t *testing.T) {
	for name, v := range map[string]string{
		"empty": "", "other scheme": "Bearer abc", "no value": AuthScheme, "not base64": AuthScheme + " !!!",
		"not json": AuthScheme + " aGVsbG8", "too long": AuthScheme + " " + strings.Repeat("A", MaxAuthorizationSize+1),
	} {
		if _, err := DecodeAuthorization(v); !errors.Is(err, ErrAuthorization) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func FuzzDecodeAuthorization(f *testing.F) {
	f.Add(AuthScheme + " eyJjaGFsbGVuZ2UiOnt9fQ")
	f.Add("")
	f.Add(AuthScheme + " ")
	f.Fuzz(func(_ *testing.T, v string) {
		_, _ = DecodeAuthorization(v)
	})
}
