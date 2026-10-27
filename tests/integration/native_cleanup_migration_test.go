//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
)

func nativeCleanupMigration(t *testing.T) *migrate.Migration {
	t.Helper()
	for _, step := range migrations.Group().Migrations() {
		if step.Version == "20261101000200" {
			return step
		}
	}
	t.Fatal("native cleanup request migration is not registered")
	return nil
}

func TestNativeCleanupMigrationKeepsRequestsAcrossEveryLostReply(t *testing.T) {
	step := nativeCleanupMigration(t)
	f, records, request := nativeRestoreFixture(t)
	store := nativeCleanupStore(t, f)
	r, err := records.CreateNativeRestore(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := store.RequestNativeCleanup(t.Context(), "org", r.ID, "replacement-admin", func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	executor := pgmigrate.New(f.db)
	count := &nativeRestoreMigrationReplies{Executor: executor}
	if err = step.Up(t.Context(), count); err != nil {
		t.Fatal(err)
	}
	for applied := 1; applied <= count.calls; applied++ {
		loss := &nativeRestoreMigrationReplies{Executor: executor, failAt: applied}
		if err = step.Up(t.Context(), loss); !errors.Is(err, errNativeMigrationReplyLost) {
			t.Fatal("cleanup migration did not inject an applied-statement reply loss", applied, err)
		}
		if err = step.Up(t.Context(), executor); err != nil {
			t.Fatal("cleanup migration could not replay", applied, err)
		}
	}
	requireNativePointDowngradeRefusal(t, step.Down(t.Context(), executor))
	got, err := store.GetNativeRestore(t.Context(), "org", r.ID)
	if err != nil || got.Status != "cleanup_queued" || got.CleanupBy != "replacement-admin" || got.CleanupRequestedAt == nil || !got.CleanupRequestedAt.Equal(*queued.CleanupRequestedAt) || got.Source.PhysicalID != "db-original" {
		t.Fatal("migration or refused downgrade changed cleanup intent", got, err)
	}
}

func TestNativeCleanupMigrationDowngradesWithoutLosingCopyHistory(t *testing.T) {
	step := nativeCleanupMigration(t)
	f, records, request := nativeRestoreFixture(t)
	r, err := records.CreateNativeRestore(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	executor := pgmigrate.New(f.db)
	if err = step.Down(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	var source, status string
	if err = f.sql.QueryRow(t.Context(), `SELECT source->>'physical_id',status FROM loomplane_native_restores WHERE id=$1`, r.ID).Scan(&source, &status); err != nil || source != "db-original" || status != "queued" {
		t.Fatal("cleanup downgrade changed original copy history", source, status, err)
	}
	if err = step.Up(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	got, err := records.GetNativeRestore(t.Context(), "org", r.ID)
	if err != nil || got.CleanupBy != "" || got.CleanupRequestedAt != nil || got.Source.PhysicalID != "db-original" {
		t.Fatal("cleanup upgrade changed copy history", got, err)
	}
}
