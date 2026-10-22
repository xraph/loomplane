package deployments

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	"github.com/xraph/vault/store/memory"
)

// The database gateway reads addresses with ResourceURLKey, so it has to be
// the key the runner writes them under.
func TestResourceURLKeyIsTheRunnersKey(t *testing.T) {
	if got, want := ResourceURLKey("org_1", "env_1", "db"), resourceKey("org_1", "env_1", "db", ResourceURL); got != want {
		t.Fatalf("ResourceURLKey = %q, want %q", got, want)
	}
	if got := ResourceURLKey("org_1", "env_1", "db"); got != "org_1/env_1/db/url" {
		t.Fatalf("ResourceURLKey = %q, the stored keys use org/env/name/url", got)
	}
}

// The database gateway polls Version every second to see whether an address
// changed, so it must change on every write, and must never decrypt or count
// as an access.
func TestVaultResourceSecretsVersion(t *testing.T) {
	ctx := t.Context()
	enc, err := crypto.NewEncryptor(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	accesses := 0
	v := NewVaultResourceSecrets(secret.NewService(memory.New(), enc,
		secret.WithOnAccess(func(context.Context, string, string) { accesses++ })))
	const key = "org_1/env_1/db/url"

	if _, err := v.Version(ctx, key); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("Version of a missing key = %v, want ErrNoCredential", err)
	}
	seen := map[string]bool{}
	next := func(what string) {
		t.Helper()
		got, err := v.Version(ctx, key)
		if err != nil {
			t.Fatalf("Version after %s: %v", what, err)
		}
		if seen[got] {
			t.Fatalf("Version after %s = %q, which it was before", what, got)
		}
		seen[got] = true
		again, err := v.Version(ctx, key)
		if err != nil || again != got {
			t.Fatalf("Version again after %s = %q, %v; want %q", what, again, err, got)
		}
	}
	if err := v.Put(ctx, key, []byte("postgres://a")); err != nil {
		t.Fatal(err)
	}
	next("the first write")
	if err := v.Put(ctx, key, []byte("postgres://b")); err != nil {
		t.Fatal(err)
	}
	next("a second write")
	if err := v.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := v.Put(ctx, key, []byte("postgres://b")); err != nil {
		t.Fatal(err)
	}
	next("a delete and the same value written back")
	if accesses != 0 {
		t.Fatalf("Version counted %d accesses, want none", accesses)
	}
}
