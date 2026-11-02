//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
)

type nativePointFixture struct {
	store  *recovery.PgStore
	db     *pgdriver.PgDB
	sql    *pgx.Conn
	source recovery.NativeSource
}

func nativePointHistoryFixture(t *testing.T) nativePointFixture {
	t.Helper()
	return nativePointHistoryFixtureSchema(t, migrations.All())
}

func nativePointHistoryFixtureSchema(t *testing.T, groups []*migrate.Group) nativePointFixture {
	t.Helper()
	ctx := t.Context()
	dsn := freshDatabase(t)
	pointDatabase, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("native point fixture database: %s", pointDatabase.Database)
	if migrateErr := ops.Migrate(ctx, dsn, groups); migrateErr != nil {
		t.Fatal(migrateErr)
	}
	sql, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sql.Close(context.Background()) })
	for _, query := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('project','org','Native points','native-points','ready','user',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('environment','org','project','production','production',now(),now())`,
	} {
		if _, execErr := sql.Exec(ctx, query); execErr != nil {
			t.Fatal(execErr)
		}
	}
	db := pgdriver.New()
	if openErr := db.Open(ctx, dsn); openErr != nil {
		t.Fatal(openErr)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC()
	binding := &planning.Binding{ID: "binding", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", Subject: planning.ResourceSubject, Name: "database", Provider: "rds", Region: "us-east-1", ConnectionID: "account", ExternalID: "database", PhysicalID: "db-original", Config: json.RawMessage(`{"kind":"postgres","version":"17","provider":"rds"}`), CreatedAt: now, UpdatedAt: now}
	if putErr := planning.NewPgStore(db).PutBinding(ctx, binding); putErr != nil {
		t.Fatal(putErr)
	}
	store := recovery.NewPgStore(db)
	sources, err := store.ListNativeSources(ctx, "org", "project", recovery.NativePageInput{})
	if err != nil || len(sources.Sources) != 1 {
		t.Fatalf("native source fixture: %+v %v", sources, err)
	}
	return nativePointFixture{store: store, db: db, sql: sql, source: sources.Sources[0]}
}

func (f nativePointFixture) refresh(ctx context.Context, points []recovery.NativePoint) error {
	return f.store.RefreshNativeInventory(ctx, "org", f.source.ID, func(context.Context, *recovery.NativeSource) ([]recovery.NativePoint, bool, error) {
		return points, true, nil
	})
}

func nativeIdentityPoint(t *testing.T, timestamp string) recovery.NativePoint {
	t.Helper()
	created, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		t.Fatal(err)
	}
	return recovery.NativePoint{ProviderID: "arn:aws:rds:us-east-1:123456789012:snapshot:daily", Name: "daily", Kind: "manual", Status: "available", Engine: "postgres", Version: "17.2", CreatedAt: created, Encrypted: true}
}

func TestNativePointIdentityKeepsNanosecondHistoryAndCursorOrder(t *testing.T) {
	f := nativePointHistoryFixture(t)
	// Rounding the SQL uniqueness key must never overwrite an earlier point.
	times := []string{"2026-10-22T00:00:00Z", "2026-10-22T00:00:00.000000001Z", "2026-10-22T00:00:00.000001001Z", "2026-10-22T00:00:00.999999999Z", "2026-10-22T00:00:01Z", "2026-10-22T00:00:01.000000001Z"}
	first := nativeIdentityPoint(t, times[0])
	if err := f.refresh(t.Context(), []recovery.NativePoint{first}); err != nil {
		t.Fatal(err)
	}
	before, err := f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil || len(before.Points) != 1 {
		t.Fatalf("first point: %+v %v", before, err)
	}
	originalID := before.Points[0].ID
	points := make([]recovery.NativePoint, 0, len(times))
	for _, timestamp := range times {
		points = append(points, nativeIdentityPoint(t, timestamp))
	}
	if err = f.refresh(t.Context(), points); err != nil {
		t.Fatal(err)
	}
	reopened := recovery.NewPgStore(f.db)
	all, err := reopened.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil || len(all.Points) != len(times) {
		t.Fatalf("native creation times collapsed: %+v %v", all, err)
	}
	cursor := ""
	seen := map[string]bool{}
	for i := len(times) - 1; i >= 0; i-- {
		page, readErr := reopened.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{Before: cursor, Limit: 1})
		if readErr != nil || len(page.Points) != 1 {
			t.Fatalf("lost exact-time page %d: %+v %v", i, page, readErr)
		}
		point := page.Points[0]
		if point.CreatedAt.UTC().Format(time.RFC3339Nano) != times[i] || seen[point.ID] || !point.Present || i == 0 && point.ID != originalID {
			t.Fatalf("native identity or order changed at %s: %+v", times[i], point)
		}
		seen[point.ID] = true
		cursor = page.NextBefore
		if (i == 0) != (cursor == "") {
			t.Fatal("incorrect exact-time continuation")
		}
	}
	if err = f.refresh(t.Context(), points); err != nil {
		t.Fatal(err)
	}
	after, err := reopened.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil || len(after.Points) != len(times) {
		t.Fatalf("repeat changed history: %+v %v", after, err)
	}
	for _, point := range after.Points {
		if !seen[point.ID] {
			t.Fatal("retry allocated another point identity")
		}
	}
}

func TestNativePointIdentityConcurrentRefreshesKeepEveryCreation(t *testing.T) {
	f := nativePointHistoryFixture(t)
	timestamps := []string{"2026-10-22T00:00:00.000000001Z", "2026-10-22T00:00:00.000000002Z", "2026-10-22T00:00:00.000000003Z", "2026-10-22T00:00:00.000000004Z", "2026-10-22T00:00:00.000000005Z", "2026-10-22T00:00:00.000000006Z", "2026-10-22T00:00:00.000000007Z", "2026-10-22T00:00:00.000000008Z"}
	finished := make(chan error, len(timestamps))
	for _, timestamp := range timestamps {
		point := nativeIdentityPoint(t, timestamp)
		go func() { finished <- f.refresh(t.Context(), []recovery.NativePoint{point}) }()
	}
	for range timestamps {
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil || len(got.Points) != len(timestamps) {
		t.Fatalf("concurrent observations collapsed creation identities: %+v %v", got, err)
	}
	present := 0
	for _, point := range got.Points {
		if point.Present {
			present++
		}
	}
	if present != 1 {
		t.Fatal("serialized complete scans did not retain exactly the last observation", present)
	}
}

func TestNativePointIdentityCursorCannotCrossSourceOrOrganization(t *testing.T) {
	f := nativePointHistoryFixture(t)
	point := nativeIdentityPoint(t, "2026-10-22T00:00:00.000000001Z")
	if err := f.refresh(t.Context(), []recovery.NativePoint{point}); err != nil {
		t.Fatal(err)
	}
	original, err := f.store.ReadNativeInventory(t.Context(), "org", f.source.ID, recovery.NativePageInput{})
	if err != nil || len(original.Points) != 1 {
		t.Fatal("source fixture missing", err)
	}
	binding := f.source.Binding
	binding.ID, binding.Name, binding.PhysicalID = "other-binding", "other-database", "db-other"
	if err = planning.NewPgStore(f.db).PutBinding(t.Context(), &binding); err != nil {
		t.Fatal(err)
	}
	sources, err := f.store.ListNativeSources(t.Context(), "org", "project", recovery.NativePageInput{})
	if err != nil || len(sources.Sources) != 2 {
		t.Fatal("second source fixture missing", err)
	}
	other := ""
	for _, source := range sources.Sources {
		if source.ID != f.source.ID {
			other = source.ID
		}
	}
	if _, err = f.store.ReadNativeInventory(t.Context(), "org", other, recovery.NativePageInput{Before: original.Points[0].ID, Limit: 1}); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("foreign source cursor accepted", err)
	}
	if _, err = f.store.ReadNativeInventory(t.Context(), "foreign", f.source.ID, recovery.NativePageInput{Before: original.Points[0].ID, Limit: 1}); !errors.Is(err, recovery.ErrNotFound) {
		t.Fatal("foreign organization cursor accepted", err)
	}
}
