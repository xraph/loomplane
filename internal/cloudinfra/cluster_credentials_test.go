package cloudinfra

import (
	"testing"
	"time"
)

func TestManagedClusterVaultRecoveryRejectsMismatchedExpiry(t *testing.T) {
	secrets := memorySecrets{}
	s := NewService(Deps{Secrets: secrets})
	expiry := time.Now().UTC().Add(time.Hour)
	if err := s.putManagedCredential(t.Context(), "org", "version", []byte("runtime"), expiry); err != nil {
		t.Fatal(err)
	}
	if _, err := s.revealManagedCredential(t.Context(), "org", "version", expiry.Add(time.Second)); err == nil {
		t.Fatal("stale Vault material accepted under newer expiry")
	}
	raw, err := s.revealManagedCredential(t.Context(), "org", "version", expiry)
	if err != nil || string(raw) != "runtime" {
		t.Fatal("matching orphan version not recovered", err)
	}
}
