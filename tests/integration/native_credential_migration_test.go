//go:build integration

package integration

import (
	"errors"
	"testing"

	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/database/migrations"
)

func TestNativeCredentialMigrationReplaysAndKeepsRetainedHistory(t *testing.T) {
	f, records, request := nativeRestoreFixture(t)
	step := migrations.NativeCredentialsGroup().Migrations()[0]
	executor := pgmigrate.New(f.db)
	if err := step.Down(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	count := &nativeRestoreMigrationReplies{Executor: executor}
	if err := step.Up(t.Context(), count); err != nil {
		t.Fatal(err)
	}
	for applied := 1; applied <= count.calls; applied++ {
		if err := step.Down(t.Context(), executor); err != nil {
			t.Fatal(err)
		}
		loss := &nativeRestoreMigrationReplies{Executor: executor, failAt: applied}
		if err := step.Up(t.Context(), loss); !errors.Is(err, errNativeMigrationReplyLost) {
			t.Fatal("credential migration did not inject its applied-statement reply loss", applied, err)
		}
		if err := step.Up(t.Context(), executor); err != nil {
			t.Fatal("credential fence could not replay", applied, err)
		}
	}
	if _, err := records.CreateNativeRestore(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	requireNativePointDowngradeRefusal(t, step.Down(t.Context(), executor))
	r, err := records.ClaimNativeRestore(t.Context(), "org", request.ID, "migration-owner")
	if err != nil {
		t.Fatal(err)
	}
	vault := nativeCredentialVault(t, f)
	if _, err = vault.PutNativeVersion(t.Context(), *r, []byte("private-migration-copy-password")); err != nil {
		t.Fatal("refused downgrade disabled the credential fence", err)
	}
	stale := *r
	stale.Token = "previous-worker"
	if _, err = vault.PutNativeVersion(t.Context(), stale, []byte("unexpected")); err == nil {
		t.Fatal("migration replay or refused downgrade removed the credential fence")
	}
}
