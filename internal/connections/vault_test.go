package connections

import (
	"errors"
	"testing"

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
