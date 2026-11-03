package connections

import (
	"bytes"
	"errors"
	"testing"

	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	"github.com/xraph/vault/store/memory"

	"github.com/xraph/loomplane/internal/repositories"
)

func TestVaultSecrets_missingSecretIsNotFoundToBothPackages(t *testing.T) {
	var err error = secretNotFoundError{}
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("a missing secret isn't ErrNotFound")
	}
	if !errors.Is(err, repositories.ErrSecretNotFound) {
		t.Fatal("a missing secret isn't repositories.ErrSecretNotFound")
	}
	if err.Error() != ErrNotFound.Error() {
		t.Fatalf("a missing secret reads %q, want %q", err.Error(), ErrNotFound.Error())
	}
	if errors.Is(errors.New("vault is down"), repositories.ErrSecretNotFound) {
		t.Fatal("another error reads as a missing secret")
	}
}

func vaultAppSecrets(t *testing.T) (*VaultAppSecrets, *secret.Service) {
	t.Helper()
	enc, err := crypto.NewEncryptor(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc := secret.NewService(memory.New(), enc, secret.WithAppID(PlatformOAuthAppsVaultAppID))
	return NewVaultAppSecrets(svc), svc
}

func TestVaultAppSecretsRoundTripByCloud(t *testing.T) {
	v, svc := vaultAppSecrets(t)
	if err := v.Put(t.Context(), GCP, []byte("g-secret")); err != nil {
		t.Fatal(err)
	}
	got, err := v.Reveal(t.Context(), GCP)
	if err != nil || string(got) != "g-secret" {
		t.Fatalf("reveal %q: %v", got, err)
	}
	// The key is the bare cloud, under the platform's Vault app.
	if sec, getErr := svc.Get(t.Context(), GCP, PlatformOAuthAppsVaultAppID); getErr != nil || string(sec.Value) != "g-secret" {
		t.Fatalf("by key: %v", getErr)
	}
	if err = v.Put(t.Context(), GCP, []byte("rotated")); err != nil {
		t.Fatal(err)
	}
	if got, _ = v.Reveal(t.Context(), GCP); string(got) != "rotated" {
		t.Fatalf("after rotating: %q", got)
	}
	if err = v.Delete(t.Context(), GCP); err != nil {
		t.Fatal(err)
	}
	if _, err = v.Reveal(t.Context(), GCP); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestVaultAppSecretsMissing(t *testing.T) {
	v, _ := vaultAppSecrets(t)
	if _, err := v.Reveal(t.Context(), Azure); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reveal: %v", err)
	}
	if err := v.Delete(t.Context(), Azure); err != nil {
		t.Fatalf("delete: %v", err)
	}
}
