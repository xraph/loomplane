package dbgateway

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeKeyFile writes a signing key file with mode and returns its path.
func writeKeyFile(t *testing.T, body any, mode os.FileMode) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "dbgateway.json")
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func keyFile(seed []byte, notBefore, notAfter time.Time) map[string]any {
	return map[string]any{
		"version": 1, "private_key": base64.StdEncoding.EncodeToString(seed),
		"not_before": notBefore, "not_after": notAfter,
	}
}

func TestLoadSigningKey(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	notBefore, notAfter := now.Add(-time.Hour).UTC().Truncate(time.Second), now.Add(24*time.Hour).UTC().Truncate(time.Second)
	path := writeKeyFile(t, keyFile(seed, notBefore, notAfter), 0o600)

	priv, trust, err := LoadSigningKey(path)
	if err != nil {
		t.Fatal(err)
	}
	want := ed25519.NewKeyFromSeed(seed)
	if !bytes.Equal(priv, want) {
		t.Fatal("private key isn't the one from the seed")
	}
	if !bytes.Equal(trust.PublicKey, want.Public().(ed25519.PublicKey)) ||
		!trust.NotBefore.Equal(notBefore) || !trust.NotAfter.Equal(notAfter) {
		t.Fatalf("trust key %+v", trust)
	}
}

func TestLoadSigningKeyRefuses(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(seed)
	now := time.Now()
	valid := func() map[string]any { return keyFile(seed, now.Add(-time.Hour), now.Add(time.Hour)) }
	with := func(k string, v any) map[string]any {
		m := valid()
		m[k] = v
		return m
	}
	for _, tc := range []struct {
		name string
		body any
		mode os.FileMode
	}{
		{"version 2", with("version", 2), 0o600},
		{"a short seed", with("private_key", base64.StdEncoding.EncodeToString(seed[:16])), 0o600},
		{"a whole private key, not a seed", with("private_key", base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed))), 0o600},
		{"not base64", with("private_key", "%%%"+encoded), 0o600},
		{"expired", keyFile(seed, now.Add(-2*time.Hour), now.Add(-time.Hour)), 0o600},
		{"not yet valid", keyFile(seed, now.Add(time.Hour), now.Add(2*time.Hour)), 0o600},
		{"an empty window", keyFile(seed, now.Add(-time.Hour), now.Add(-time.Hour)), 0o600},
		{"no window", map[string]any{"version": 1, "private_key": encoded}, 0o600},
		{"an unknown field", with("comment", "x"), 0o600},
		{"readable by others", valid(), 0o644},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeKeyFile(t, tc.body, tc.mode)
			_, _, err := LoadSigningKey(path)
			if err == nil {
				t.Fatal("accepted")
			}
			if strings.Contains(err.Error(), encoded) || strings.Contains(err.Error(), path) {
				t.Fatalf("error %q discloses the file", err)
			}
		})
	}
	if _, _, err := LoadSigningKey("relative/dbgateway.json"); err == nil {
		t.Fatal("accepted a relative path")
	}
	if _, _, err := LoadSigningKey(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("accepted a missing file")
	}
}
