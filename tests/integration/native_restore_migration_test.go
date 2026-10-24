//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/grove/driver"
	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
)

func nativeRestoreMigration(t *testing.T) *migrate.Migration {
	t.Helper()
	for _, step := range migrations.Group().Migrations() {
		if step.Version == "20261101000100" {
			return step
		}
	}
	t.Fatal("native restore migration is not centrally registered")
	return nil
}

type nativeRestoreMigrationReplies struct {
	migrate.Executor
	calls  int
	failAt int
}

func (e *nativeRestoreMigrationReplies) Exec(ctx context.Context, query string, args ...any) (driver.Result, error) {
	result, err := e.Executor.Exec(ctx, query, args...)
	if err != nil {
		return result, err
	}
	e.calls++
	if e.calls == e.failAt {
		return result, errNativeMigrationReplyLost
	}
	return result, nil
}

func TestNativeRestoreMigrationReplaysEveryAppliedStatement(t *testing.T) {
	f, store, request := nativeRestoreFixture(t)
	r, err := store.CreateNativeRestore(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	step := nativeRestoreMigration(t)
	executor := pgmigrate.New(f.db)
	count := &nativeRestoreMigrationReplies{Executor: executor}
	if err = step.Up(t.Context(), count); err != nil {
		t.Fatal(err)
	}
	for applied := 1; applied <= count.calls; applied++ {
		loss := &nativeRestoreMigrationReplies{Executor: executor, failAt: applied}
		if err = step.Up(t.Context(), loss); !errors.Is(err, errNativeMigrationReplyLost) {
			t.Fatal("migration did not inject a lost response after applied SQL", applied, err)
		}
		if err = step.Up(t.Context(), executor); err != nil {
			t.Fatal("native copy migration could not replay", applied, err)
		}
	}
	requireNativePointDowngradeRefusal(t, step.Down(t.Context(), executor))
	got, err := store.GetNativeRestore(t.Context(), "org", r.ID)
	if err != nil || got.Source.PhysicalID != "db-original" || got.PointID != r.PointID || got.Target.ConnectionID != "recovery-account" {
		t.Fatal("migration replay or refused downgrade changed native copy history", got, err)
	}
}

func TestNativeRestoreMigrationRemovesPartialEmptyInstall(t *testing.T) {
	f := nativePointHistoryFixture(t)
	// Dependent native deletion references must be removed first, as the
	// migration runner does when rolling back from the newest schema.
	for _, version := range []string{"20261102000200", "20261102000100"} {
		for _, child := range migrations.Group().Migrations() {
			if child.Version == version {
				if childErr := child.Down(t.Context(), pgmigrate.New(f.db)); childErr != nil {
					t.Fatal(childErr)
				}
			}
		}
	}

	step := nativeRestoreMigration(t)
	executor := pgmigrate.New(f.db)
	if err := step.Down(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	loss := &nativeRestoreMigrationReplies{Executor: executor, failAt: 1}
	if err := step.Up(t.Context(), loss); !errors.Is(err, errNativeMigrationReplyLost) {
		t.Fatal("partial migration was not interrupted after its first applied index", err)
	}
	if err := step.Down(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	var remains bool
	if err := f.sql.QueryRow(t.Context(), `SELECT to_regclass('loomplane_native_sources_restore_scope') IS NOT NULL OR to_regclass('loomplane_native_points_restore_scope') IS NOT NULL OR to_regclass('loomplane_native_restores') IS NOT NULL`).Scan(&remains); err != nil || remains {
		t.Fatal("empty partial downgrade left native copy schema behind", remains, err)
	}
	if err := step.Up(t.Context(), executor); err != nil {
		t.Fatal("empty migration could not apply again", err)
	}
}
