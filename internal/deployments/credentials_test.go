package deployments

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// Keys reads past Vault's pages and returns only the keys under the
// prefix, so an environment's sweep finds all of its own and none of an
// environment whose ID merely starts the same way.
func TestVaultResourceSecretsKeys(t *testing.T) {
	ctx := t.Context()
	enc, err := crypto.NewEncryptor(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	v := NewVaultResourceSecrets(secret.NewService(memory.New(), enc))
	want := map[string]bool{}
	for i := range 2*keysPage + 7 {
		for _, env := range []string{"env_1", "env_10", "env_2"} {
			key := resourceKey("org_1", env, fmt.Sprintf("db%04d", i), ResourceURL)
			if err = v.Put(ctx, key, []byte("x")); err != nil {
				t.Fatal(err)
			}
			if env == "env_1" {
				want[key] = true
			}
		}
	}
	prefix, err := environmentKeys("org_1", "env_1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Keys(ctx, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("Keys found %d keys, want %d", len(got), len(want))
	}
	for _, k := range got {
		if !want[k] {
			t.Fatalf("Keys returned %q, from outside the environment", k)
		}
	}
}

// An ID that's empty or holds a slash would scope a sweep to more than one
// environment.
func TestEnvironmentKeysRefusesWideScopes(t *testing.T) {
	for _, ids := range [][2]string{{"", "env_1"}, {"org_1", ""}, {"org_1", "env_1/x"}, {"org/1", "env_1"}} {
		if prefix, err := environmentKeys(ids[0], ids[1]); err == nil {
			t.Fatalf("environmentKeys(%q, %q) = %q, want an error", ids[0], ids[1], prefix)
		}
	}
}
