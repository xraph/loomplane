package plane

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
)

func newSealKey(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func TestSealRoundTrip(t *testing.T) {
	key := newSealKey(t)
	plaintext := []byte("postgres://user:pw@host/db")
	ctx := []byte("org/env/db")

	s, err := Seal(key.PublicKey(), plaintext, ctx)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(s.Ciphertext, plaintext) {
		t.Fatal("ciphertext contains the plaintext")
	}
	got, err := Open(key, s, ctx)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("Open = %q, want %q", got, plaintext)
	}
}

func TestSealEmptyPlaintextAndContext(t *testing.T) {
	key := newSealKey(t)
	s, err := Seal(key.PublicKey(), nil, nil)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := Open(key, s, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Open = %q, want empty", got)
	}
}

func TestSealIsRandomised(t *testing.T) {
	key := newSealKey(t)
	a, err := Seal(key.PublicKey(), []byte("same"), []byte("ctx"))
	if err != nil {
		t.Fatalf("Seal a: %v", err)
	}
	b, err := Seal(key.PublicKey(), []byte("same"), []byte("ctx"))
	if err != nil {
		t.Fatalf("Seal b: %v", err)
	}
	if bytes.Equal(a.Ephemeral, b.Ephemeral) || bytes.Equal(a.Nonce, b.Nonce) || bytes.Equal(a.Ciphertext, b.Ciphertext) {
		t.Fatal("two seals of the same input share a field")
	}
}

func TestSealWrongContext(t *testing.T) {
	key := newSealKey(t)
	s, err := Seal(key.PublicKey(), []byte("secret"), []byte("one"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := Open(key, s, []byte("two")); err != ErrSealed {
		t.Fatalf("Open with another context = %v, want ErrSealed", err)
	}
}

func TestSealWrongKey(t *testing.T) {
	s, err := Seal(newSealKey(t).PublicKey(), []byte("secret"), []byte("ctx"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := Open(newSealKey(t), s, []byte("ctx")); err != ErrSealed {
		t.Fatalf("Open with another key = %v, want ErrSealed", err)
	}
}

func TestSealTamper(t *testing.T) {
	key := newSealKey(t)
	ctx := []byte("ctx")
	fresh := func() Sealed {
		s, err := Seal(key.PublicKey(), []byte("secret"), ctx)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		return s
	}

	tests := []struct {
		name   string
		mutate func(*Sealed)
	}{
		{"ciphertext byte", func(s *Sealed) { s.Ciphertext[0] ^= 1 }},
		{"ephemeral byte", func(s *Sealed) { s.Ephemeral[0] ^= 1 }},
		{"nonce byte", func(s *Sealed) { s.Nonce[0] ^= 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := fresh()
			tt.mutate(&s)
			if _, err := Open(key, s, ctx); err != ErrSealed {
				t.Fatalf("Open = %v, want ErrSealed", err)
			}
		})
	}
}

func TestOpenBadSizes(t *testing.T) {
	key := newSealKey(t)
	ctx := []byte("ctx")
	good, err := Seal(key.PublicKey(), []byte("secret"), ctx)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	tests := []struct {
		name string
		s    Sealed
	}{
		{"zero value", Sealed{}},
		{"short ephemeral", Sealed{good.Ephemeral[:31], good.Nonce, good.Ciphertext}},
		{"long ephemeral", Sealed{append(bytes.Clone(good.Ephemeral), 0), good.Nonce, good.Ciphertext}},
		{"all-zero ephemeral", Sealed{make([]byte, 32), good.Nonce, good.Ciphertext}},
		{"short nonce", Sealed{good.Ephemeral, good.Nonce[:11], good.Ciphertext}},
		{"long nonce", Sealed{good.Ephemeral, append(bytes.Clone(good.Nonce), 0), good.Ciphertext}},
		{"ciphertext shorter than the tag", Sealed{good.Ephemeral, good.Nonce, good.Ciphertext[:15]}},
		{"empty ciphertext", Sealed{good.Ephemeral, good.Nonce, nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Open(key, tt.s, ctx); err != ErrSealed {
				t.Fatalf("Open = %v, want exactly ErrSealed", err)
			}
		})
	}
}

func TestOpenNilKey(t *testing.T) {
	key := newSealKey(t)
	s, err := Seal(key.PublicKey(), []byte("secret"), nil)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := Open(nil, s, nil); err != ErrSealed {
		t.Fatalf("Open(nil key) = %v, want ErrSealed", err)
	}
}

func TestSealRejectsBadRecipient(t *testing.T) {
	if _, err := Seal(nil, []byte("x"), nil); err == nil {
		t.Fatal("Seal to a nil key succeeded")
	}
	p256, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate P-256 key: %v", err)
	}
	_, err = Seal(p256.PublicKey(), []byte("x"), nil)
	if err == nil {
		t.Fatal("Seal to a P-256 key succeeded")
	}
	if errors.Is(err, ErrSealed) {
		t.Fatalf("Seal error %v should not be ErrSealed", err)
	}
}

func TestSealedJSON(t *testing.T) {
	key := newSealKey(t)
	s, err := Seal(key.PublicKey(), []byte("secret"), []byte("ctx"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var fields map[string]string
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("Unmarshal into map: %v", err)
	}
	for _, name := range []string{"ephemeral", "nonce", "ciphertext"} {
		if fields[name] == "" {
			t.Errorf("json field %q missing in %s", name, raw)
		}
	}
	var back Sealed
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got, err := Open(key, back, []byte("ctx"))
	if err != nil {
		t.Fatalf("Open after JSON round trip: %v", err)
	}
	if string(got) != "secret" {
		t.Fatalf("Open = %q, want secret", got)
	}
}
