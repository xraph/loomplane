package dbgateway

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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

// entry is one key of a version 2 file.
func entry(seed []byte, notBefore, notAfter time.Time) map[string]any {
	return map[string]any{"private_key": base64.StdEncoding.EncodeToString(seed), "not_before": notBefore, "not_after": notAfter}
}

func keyFileV2(entries ...map[string]any) map[string]any {
	return map[string]any{"version": 2, "keys": entries}
}

// A version 2 file holds the old key and its replacement, and a renewal of
// the old one under the same seed, each with its own window, in order.
func TestLoadSigningKeysVersion2(t *testing.T) {
	oldSeed, newSeed := make([]byte, ed25519.SeedSize), make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(oldSeed); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(newSeed); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	windows := [][2]time.Time{
		{now.Add(-48 * time.Hour), now.Add(24 * time.Hour)},
		{now.Add(12 * time.Hour), now.Add(90 * 24 * time.Hour)},
		{now.Add(-time.Hour), now.Add(48 * time.Hour)},
	}
	seeds := [][]byte{oldSeed, newSeed, oldSeed}
	entries := make([]map[string]any, 0, len(windows))
	for i, w := range windows {
		entries = append(entries, entry(seeds[i], w[0], w[1]))
	}
	keys, err := LoadSigningKeys(writeKeyFile(t, keyFileV2(entries...), 0o600))
	if err != nil || len(keys) != 3 {
		t.Fatal(keys, err)
	}
	for i, k := range keys {
		want := ed25519.NewKeyFromSeed(seeds[i])
		if !bytes.Equal(k.Private, want) || !bytes.Equal(k.Trust.PublicKey, want.Public().(ed25519.PublicKey)) ||
			!k.Trust.NotBefore.Equal(windows[i][0]) || !k.Trust.NotAfter.Equal(windows[i][1]) {
			t.Errorf("key %d: %+v", i, k.Trust)
		}
	}
	if keys[0].Trust.ID() != keys[2].Trust.ID() || keys[0].Trust.ID() == keys[1].Trust.ID() {
		t.Fatal("a renewal should share its key's ID, and only it")
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

	keys, err := LoadSigningKeys(path)
	if err != nil || len(keys) != 1 {
		t.Fatal(keys, err)
	}
	priv, trust := keys[0].Private, keys[0].Trust
	want := ed25519.NewKeyFromSeed(seed)
	if !bytes.Equal(priv, want) {
		t.Fatal("private key isn't the one from the seed")
	}
	if !bytes.Equal(trust.PublicKey, want.Public().(ed25519.PublicKey)) ||
		!trust.NotBefore.Equal(notBefore) || !trust.NotAfter.Equal(notAfter) {
		t.Fatalf("trust key %+v", trust)
	}
}

// A key outside its window still loads, with its window, so Loomplane can
// start and say the key has expired rather than refuse to start at all.
func TestLoadSigningKeyOutsideItsWindow(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for name, window := range map[string][2]time.Time{
		"expired":       {now.Add(-2 * time.Hour), now.Add(-time.Hour)},
		"not yet valid": {now.Add(time.Hour), now.Add(2 * time.Hour)},
	} {
		keys, err := LoadSigningKeys(writeKeyFile(t, keyFile(seed, window[0], window[1]), 0o600))
		if err != nil || len(keys) != 1 || !keys[0].Trust.NotBefore.Equal(window[0]) || !keys[0].Trust.NotAfter.Equal(window[1]) {
			t.Errorf("%s: %+v %v", name, keys, err)
		}
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
		{"version 3", with("version", 3), 0o600},
		{"version 2 with a key of its own", with("version", 2), 0o600},
		{"version 1 with a list", with("keys", []any{}), 0o600},
		{"version 2 with no keys", map[string]any{"version": 2, "keys": []any{}}, 0o600},
		{"version 2 with too many keys", keyFileV2(slices.Repeat([]map[string]any{entry(seed, now.Add(-time.Hour), now.Add(time.Hour))}, maxSigningKeys+1)...), 0o600},
		{"version 2 with one bad key", keyFileV2(entry(seed, now.Add(-time.Hour), now.Add(time.Hour)), entry(seed[:16], now, now.Add(time.Hour))), 0o600},
		{"version 2 with an empty window", keyFileV2(entry(seed, now.Add(-time.Hour), now.Add(-time.Hour))), 0o600},
		{"a short seed", with("private_key", base64.StdEncoding.EncodeToString(seed[:16])), 0o600},
		{"a whole private key, not a seed", with("private_key", base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed))), 0o600},
		{"not base64", with("private_key", "%%%"+encoded), 0o600},
		{"an empty window", keyFile(seed, now.Add(-time.Hour), now.Add(-time.Hour)), 0o600},
		{"a window that closes before it opens", keyFile(seed, now.Add(time.Hour), now.Add(-time.Hour)), 0o600},
		{"no window", map[string]any{"version": 1, "private_key": encoded}, 0o600},
		{"an unknown field", with("comment", "x"), 0o600},
		{"readable by others", valid(), 0o644},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeKeyFile(t, tc.body, tc.mode)
			_, err := LoadSigningKeys(path)
			if err == nil {
				t.Fatal("accepted")
			}
			if strings.Contains(err.Error(), encoded) || strings.Contains(err.Error(), path) {
				t.Fatalf("error %q discloses the file", err)
			}
		})
	}
	if _, err := LoadSigningKeys("relative/dbgateway.json"); err == nil {
		t.Fatal("accepted a relative path")
	}
	if _, err := LoadSigningKeys(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("accepted a missing file")
	}
}
