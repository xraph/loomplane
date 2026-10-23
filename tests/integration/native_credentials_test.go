//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xraph/grove"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	"github.com/xraph/loomplane/internal/deployments"
)

type resourceCredentialVersions interface {
	PutVersion(context.Context, string, []byte) (int64, error)
	GetVersion(context.Context, string, int64) ([]byte, error)
}

func TestNativeCredentialPublicationRejectsUnownedOrExpiredWriters(t *testing.T) {
	f, records, request := nativeRestoreFixture(t)
	if _, err := records.CreateNativeRestore(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	r, err := records.ClaimNativeRestore(t.Context(), "org", request.ID, "private-native-worker-token")
	if err != nil {
		t.Fatal(err)
	}
	vault := nativeCredentialVault(t, f)
	key := "org/environment/native-copy:" + r.ID + "/credentials"
	version, err := vault.PutNativeVersion(t.Context(), *r, []byte("private-copy-password"))
	if err != nil || version != 1 {
		t.Fatal("current credential owner could not publish", version, err)
	}
	var metadata string
	if err = f.sql.QueryRow(t.Context(), `SELECT metadata::text FROM vault_secrets WHERE app_id=$1 AND key=$2`, deployments.ResourceVaultAppID, key).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(metadata, r.Token) {
		t.Error("private worker token leaked into public Vault metadata")
	}
	for _, change := range []string{"token", "org", "environment"} {
		stale := *r
		switch change {
		case "token":
			stale.Token = "stale-worker"
		case "org":
			stale.OrgID = "other"
		case "environment":
			stale.EnvironmentID = "other"
		}
		if _, writeErr := vault.PutNativeVersion(t.Context(), stale, []byte("unexpected")); writeErr == nil {
			t.Fatal("unowned credential publication was accepted", change)
		}
	}
	if _, writeErr := vault.PutVersion(t.Context(), key, []byte("missing-owner")); writeErr == nil {
		t.Fatal("generic Vault update bypassed native ownership")
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET lease_until=now()-interval '1 minute' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, writeErr := vault.PutNativeVersion(t.Context(), *r, []byte("expired-owner")); writeErr == nil {
		t.Fatal("expired credential publisher retained write access")
	}
	kept, err := vault.GetVersion(t.Context(), key, version)
	if err != nil || string(kept) != "private-copy-password" {
		t.Fatal("rejected writes replaced the kept credential", err)
	}
}

func nativeCredentialVault(t *testing.T, f nativePointFixture) *deployments.VaultResourceSecrets {
	t.Helper()
	db, err := grove.Open(f.db)
	if err != nil {
		t.Fatal(err)
	}
	store := vaultpg.New(db)
	if migrateErr := store.Migrate(t.Context()); migrateErr != nil {
		t.Fatal(migrateErr)
	}
	enc, err := crypto.NewEncryptor(bytes.Repeat([]byte{91}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return deployments.NewVaultResourceSecrets(secret.NewService(store, enc))
}

func TestNativeCredentialVersionsSurviveNewerWritesAndReopen(t *testing.T) {
	f := nativePointHistoryFixture(t)
	vault := nativeCredentialVault(t, f)
	versions, ok := any(vault).(resourceCredentialVersions)
	if !ok {
		t.Fatal("native copies cannot retain an immutable Vault credential version")
	}
	key := "org/environment/resource:database/password"
	one := []byte("private-original-copy-password-111111111111111111111")
	two := []byte("private-delayed-worker-password-2222222222222222222")
	version, err := versions.PutVersion(t.Context(), key, one)
	if err != nil || version != 1 {
		t.Fatal("copy credential was not versioned", version, err)
	}
	if _, err = versions.PutVersion(t.Context(), key, two); err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	var algorithm string
	if err = f.sql.QueryRow(t.Context(), `SELECT encrypted_value,encryption_alg FROM vault_secret_versions WHERE secret_key=$1 AND version=1`, key).Scan(&ciphertext, &algorithm); err != nil || bytes.Contains(ciphertext, one) || algorithm == "" {
		t.Fatal("native copy password was not encrypted in Vault history", algorithm, err)
	}
	reopened := nativeCredentialVault(t, f)
	reader := any(reopened).(resourceCredentialVersions)
	kept, err := reader.GetVersion(t.Context(), key, version)
	if err != nil || !bytes.Equal(kept, one) {
		t.Fatal("a later worker replaced the checkpointed copy password", err)
	}
	latest, err := reopened.Get(t.Context(), key)
	if err != nil || !bytes.Equal(latest, two) {
		t.Fatal("version lookup unexpectedly read the latest password", err)
	}
	for _, invalid := range []int64{0, -1, 99} {
		if _, err = reader.GetVersion(t.Context(), key, invalid); !errors.Is(err, deployments.ErrNoCredential) {
			t.Fatal("invalid credential version used an ambient latest value", invalid, err)
		}
	}
	if _, err = reader.GetVersion(t.Context(), "other/environment/native-copy:copy-one/credentials", version); !errors.Is(err, deployments.ErrNoCredential) {
		t.Fatal("copy password escaped its organization key", err)
	}
	if err = reopened.Delete(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.GetVersion(t.Context(), key, version); !errors.Is(err, deployments.ErrNoCredential) {
		t.Fatal("copy cleanup retained a readable password version", err)
	}
	var remaining int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM vault_secret_versions WHERE secret_key=$1`, key).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("copy cleanup left encrypted credential history", remaining, err)
	}
}

func TestNativeCredentialWriteCannotResurrectAfterCleanup(t *testing.T) {
	f, records, request := nativeRestoreFixture(t)
	if _, err := records.CreateNativeRestore(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	claimed, err := records.ClaimNativeRestore(t.Context(), "org", request.ID, "old-worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.CheckNativeLease(t.Context(), claimed, "running"); err != nil {
		t.Fatal(err)
	}
	db, err := grove.Open(f.db)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEncryptor(bytes.Repeat([]byte{91}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := secret.NewService(vaultpg.New(db), enc)
	key := "org/environment/native-copy:" + claimed.ID + "/credentials"
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		select {
		case <-release:
		case <-t.Context().Done():
			done <- t.Context().Err()
			return
		}
		_, writeErr := service.Set(t.Context(), key, []byte("private-delayed-copy-credential"), deployments.ResourceVaultAppID, secret.WithMetadata(map[string]string{"native_restore_id": claimed.ID, "native_org_id": claimed.OrgID, "native_environment_id": claimed.EnvironmentID, "native_worker_token_hash": fmt.Sprintf("%x", sha256.Sum256([]byte(claimed.Token)))}))
		done <- writeErr
	}()
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_native_restores SET lease_until=now()-interval '1 minute' WHERE id=$1`, claimed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.RequestNativeCleanup(t.Context(), "org", claimed.ID, "operator", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	cleanup, err := records.ClaimNativeCleanup(t.Context(), "org", claimed.ID, "cleanup-worker")
	if err != nil {
		t.Fatal(err)
	}
	vault := nativeCredentialVault(t, f)
	if err = vault.Delete(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if err = records.FinishNativeCleanup(t.Context(), cleanup, nil); err != nil {
		t.Fatal(err)
	}
	close(release)
	if writeErr := <-done; writeErr == nil {
		t.Error("stale credential write succeeded after confirmed cleanup")
	}
	var remaining int
	if err = f.sql.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM vault_secrets WHERE app_id=$1 AND key=$2)+(SELECT count(*) FROM vault_secret_versions WHERE app_id=$1 AND secret_key=$2)`, deployments.ResourceVaultAppID, key).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("stale writer resurrected native credentials after deletion", remaining, err)
	}
}
