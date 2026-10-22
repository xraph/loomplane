package plane

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLastGoodRoundTrip(t *testing.T) {
	key := newSealKey(t)
	path := filepath.Join(t.TempDir(), "last-good")
	trust, priv := newTrustKey(t)
	want := signed(t, sampleSnapshot(5), trust, priv)

	if err := SaveLastGood(path, key, want); err != nil {
		t.Fatalf("SaveLastGood: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	got, err := LoadLastGood(path, key)
	if err != nil {
		t.Fatalf("LoadLastGood: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed the snapshot:\n got %+v\nwant %+v", got, want)
	}
	// A snapshot that came back from disk still verifies.
	if err := VerifySnapshot(got, []TrustKey{trust}, "gw-1", snapshotNow); err != nil {
		t.Fatalf("VerifySnapshot after load: %v", err)
	}
}

func TestLastGoodIsNotPlaintext(t *testing.T) {
	key := newSealKey(t)
	path := filepath.Join(t.TempDir(), "last-good")
	if err := SaveLastGood(path, key, sampleSnapshot(1)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"gw-1", "10.0.0.1", "orders"} {
		if bytes.Contains(raw, []byte(leak)) {
			t.Fatalf("last-good file contains %q in the clear", leak)
		}
	}
}

func TestLastGoodWrongKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "last-good")
	if err := SaveLastGood(path, newSealKey(t), sampleSnapshot(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLastGood(path, newSealKey(t)); !errors.Is(err, ErrSealed) {
		t.Fatalf("LoadLastGood = %v, want ErrSealed", err)
	}
}

func TestLastGoodCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "last-good")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLastGood(path, newSealKey(t)); err == nil {
		t.Fatal("LoadLastGood accepted garbage")
	}
}

func TestLastGoodMissingFile(t *testing.T) {
	_, err := LoadLastGood(filepath.Join(t.TempDir(), "absent"), newSealKey(t))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("LoadLastGood = %v, want fs.ErrNotExist", err)
	}
}

func TestLastGoodOverwriteLeavesOneFile(t *testing.T) {
	key := newSealKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "last-good")
	for v := uint64(1); v <= 3; v++ {
		if err := SaveLastGood(path, key, sampleSnapshot(v)); err != nil {
			t.Fatalf("SaveLastGood v%d: %v", v, err)
		}
	}
	got, err := LoadLastGood(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 3 {
		t.Fatalf("Version = %d, want 3", got.Version)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory has %d entries, want only the last-good file", len(entries))
	}
}

func TestLastGoodFailedSaveLeavesOldFile(t *testing.T) {
	key := newSealKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "last-good")
	if err := SaveLastGood(path, key, sampleSnapshot(1)); err != nil {
		t.Fatal(err)
	}
	// A nil key fails before anything is written.
	if err := SaveLastGood(path, nil, sampleSnapshot(2)); err == nil {
		t.Fatal("SaveLastGood accepted a nil key")
	}
	got, err := LoadLastGood(path, key)
	if err != nil || got.Version != 1 {
		t.Fatalf("old file not intact: v%d, %v", got.Version, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory has %d entries after a failed save, want 1", len(entries))
	}
}
