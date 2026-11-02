//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"
	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/recovery"
)

func nativePointIdentityMigration(t *testing.T) *migrate.Migration {
	t.Helper()
	for _, step := range migrations.Group().Migrations() {
		if step.Version == "20261031000100" {
			return step
		}
	}
	t.Fatal("native point creation identity migration is not centrally registered")
	return nil
}

var errNativeMigrationReplyLost = errors.New("native migration response lost after execution")

type nativeMigrationReplyLoss struct{ migrate.Executor }

func (e nativeMigrationReplyLoss) Exec(ctx context.Context, query string, args ...any) (driver.Result, error) {
	result, err := e.Executor.Exec(ctx, query, args...)
	if err != nil {
		return result, err
	}
	return result, errNativeMigrationReplyLost
}

func TestNativePointIdentityMigrationPreservesExistingIDAndReplays(t *testing.T) {
	target := nativePointIdentityMigration(t)
	groups := migrations.All()
	older := migrate.NewGroup(migrations.GroupName)
	for _, step := range groups[0].Migrations() {
		if step.Version != target.Version {
			older.MustRegister(step)
		}
	}
	groups[0] = older
	f := nativePointHistoryFixtureSchema(t, groups)
	point := nativeIdentityPoint(t, "2026-10-22T00:00:00.123456789Z")
	raw, err := json.Marshal(point)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_native_points(id,org_id,source_id,provider_id,created_at,body,last_seen_at) VALUES('legacy-point','org',$1,$2,$3,$4,now())`, f.source.ID, point.ProviderID, point.CreatedAt, raw); err != nil {
		t.Fatal(err)
	}
	var originalBody string
	if err = f.sql.QueryRow(t.Context(), `SELECT body::text FROM loomplane_native_points WHERE id='legacy-point'`).Scan(&originalBody); err != nil {
		t.Fatal(err)
	}
	executor := pgmigrate.New(f.db)
	if err = target.Up(t.Context(), nativeMigrationReplyLoss{Executor: executor}); !errors.Is(err, errNativeMigrationReplyLost) {
		t.Fatal("did not inject an applied migration with a lost response", err)
	}
	if err = target.Up(t.Context(), executor); err != nil {
		t.Fatal("migration could not replay after an applied statement", err)
	}
	var keptBody, key string
	if err = f.sql.QueryRow(t.Context(), `SELECT body::text,creation_identity FROM loomplane_native_points WHERE id='legacy-point'`).Scan(&keptBody, &key); err != nil {
		t.Fatal(err)
	}
	if originalBody != keptBody || key != "2026-10-22T00:00:00.123456789Z" {
		t.Fatal("migration changed a retained point's exact timestamp or metadata", key)
	}
	other := nativeIdentityPoint(t, "2026-10-22T00:00:00.12345679Z")
	if err = f.refresh(t.Context(), []recovery.NativePoint{point, other}); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil || len(got.Points) != 2 || got.Points[1].ID != "legacy-point" {
		t.Fatalf("upgrade lost the old identity or collapsed the new one: %+v %v", got, err)
	}
	requireNativePointDowngradeRefusal(t, target.Down(t.Context(), executor))
	if err = target.Up(t.Context(), executor); err != nil {
		t.Fatal("refused downgrade damaged forward replay", err)
	}
	got, err = f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil || len(got.Points) != 2 || got.Points[1].ID != "legacy-point" {
		t.Fatal("refused downgrade changed recovery history", err)
	}
}

func requireNativePointDowngradeRefusal(t *testing.T, err error) {
	t.Helper()
	var native *pgconn.PgError
	if !errors.As(err, &native) || native.Code != "55000" {
		t.Fatal("retained creation identities did not block downgrade", err)
	}
}

func TestNativePointIdentityMigrationEmptyRoundTrip(t *testing.T) {
	f := nativePointHistoryFixture(t)
	target := nativePointIdentityMigration(t)
	executor := pgmigrate.New(f.db)
	for range 2 {
		if err := target.Down(t.Context(), executor); err != nil {
			t.Fatal("empty downgrade did not replay", err)
		}
	}
	if err := target.Up(t.Context(), executor); err != nil {
		t.Fatal(err)
	}
	if err := f.refresh(t.Context(), []recovery.NativePoint{nativeIdentityPoint(t, "2026-10-22T00:00:00.000000001Z")}); err != nil {
		t.Fatal("upgrade after empty rollback cannot save exact time", err)
	}
}

func TestNativePointIdentityDowngradePreservesConcurrentWriter(t *testing.T) {
	f := nativePointHistoryFixture(t)
	target := nativePointIdentityMigration(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	writer, err := f.sql.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(context.Background())
	point := nativeIdentityPoint(t, "2026-10-22T00:00:00.000000001Z")
	raw, err := json.Marshal(point)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Exec(ctx, `INSERT INTO loomplane_native_points(id,org_id,source_id,provider_id,created_at,body,last_seen_at) VALUES('concurrent-point','org',$1,$2,$3,$4,now())`, f.source.ID, point.ProviderID, point.CreatedAt, raw); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- target.Down(ctx, pgmigrate.New(f.db)) }()
	monitor, err := pgx.Connect(ctx, f.sql.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close(context.Background())
	for {
		var waiting bool
		if err = monitor.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%native_point_identity_down%')`).Scan(&waiting); err != nil {
			t.Fatal("did not observe the downgrade waiting on this writer", err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	requireNativePointDowngradeRefusal(t, <-finished)
	got, err := f.store.ReadNativeInventory(ctx, "org", f.source.ID, recovery.NativePageInput{})
	if err != nil || len(got.Points) != 1 || got.Points[0].ID != "concurrent-point" {
		t.Fatal("downgrade erased the newly committed point", err)
	}
}
