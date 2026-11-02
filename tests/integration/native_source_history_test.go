//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
)

func TestNativeSourceHistoryRetainsEveryPinnedIncarnation(t *testing.T) {
	ctx := t.Context()
	dsn := freshDatabase(t)
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, statement := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('project','org','Native','native','ready','user',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('environment','org','project','production','production',now(),now())`,
	} {
		if _, execErr := conn.Exec(ctx, statement); execErr != nil {
			t.Fatal(execErr)
		}
	}
	db := pgdriver.New()
	if openErr := db.Open(ctx, dsn); openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	store := planning.NewPgStore(db)
	now := time.Now().UTC()
	binding := &planning.Binding{ID: "binding", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", Subject: planning.ResourceSubject, Name: "database", Provider: "rds", Region: "us-east-1", ConnectionID: "account", ExternalID: "database", PhysicalID: "db-original", Config: json.RawMessage(`{"kind":"postgres","version":"17","provider":"rds"}`), CreatedAt: now, UpdatedAt: now}
	if putErr := store.PutBinding(ctx, binding); putErr != nil {
		t.Fatal(putErr)
	}
	binding.PhysicalID = "db-replacement"
	if putErr := store.PutBinding(ctx, binding); putErr != nil {
		t.Fatal(putErr)
	}
	if putErr := store.PutBinding(ctx, binding); putErr != nil {
		t.Fatal(putErr)
	}
	// A failed source receipt must roll back the binding update too.
	for _, statement := range []string{
		`CREATE FUNCTION refuse_native_capture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'capture unavailable'; END $$`,
		`CREATE TRIGGER refuse_native_capture BEFORE INSERT ON loomplane_native_sources FOR EACH ROW EXECUTE FUNCTION refuse_native_capture()`,
	} {
		if _, execErr := conn.Exec(ctx, statement); execErr != nil {
			t.Fatal(execErr)
		}
	}
	binding.PhysicalID = "db-uncaptured"
	if putErr := store.PutBinding(ctx, binding); putErr == nil {
		t.Fatal("binding succeeded without its atomic source receipt")
	}
	current, listBindingsErr := store.ListBindings(ctx, "org", "environment")
	if listBindingsErr != nil || len(current) != 1 || current[0].PhysicalID != "db-replacement" {
		t.Fatalf("failed capture changed the binding: %+v %v", current, listBindingsErr)
	}
	if _, execErr := conn.Exec(ctx, `DROP TRIGGER refuse_native_capture ON loomplane_native_sources`); execErr != nil {
		t.Fatal(execErr)
	}
	if deleteErr := store.DeleteBinding(ctx, "org", binding.ID); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	var count int
	if queryErr := conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_native_sources WHERE org_id='org' AND project_id='project'`).Scan(&count); queryErr != nil {
		t.Fatal(queryErr)
	}
	if count != 2 {
		t.Fatalf("source history lost or duplicated incarnations: %d", count)
	}
	rows, queryErr := conn.Query(ctx, `SELECT source FROM loomplane_native_sources WHERE org_id='org' ORDER BY physical_id`)
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if scanErr := rows.Scan(&raw); scanErr != nil {
			t.Fatal(scanErr)
		}
		var kept planning.Binding
		if decodeErr := json.Unmarshal(raw, &kept); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if kept.PhysicalID == "" || kept.ExternalID != "database" || kept.ConnectionID != "account" || kept.ProjectID != "project" {
			t.Fatalf("incomplete source: %+v", kept)
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		t.Fatal(rowsErr)
	}

	rows.Close()
	native := recovery.NewPgStore(db)
	sources, listErr := native.ListNativeSources(ctx, "org", "project", recovery.NativePageInput{Limit: 1})
	if listErr != nil || len(sources.Sources) != 1 || sources.NextBefore == "" {
		t.Fatalf("source pagination: %+v %v", sources, listErr)
	}
	// Source filtering must happen before pagination, including its cursor.
	if _, execErr := conn.Exec(ctx, `INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('preview','org','project','preview','preview',now(),now())`); execErr != nil {
		t.Fatal(execErr)
	}
	preview := *binding
	preview.ID = "preview-binding"
	preview.EnvironmentID = "preview"
	preview.PhysicalID = "db-preview"
	if putErr := store.PutBinding(ctx, &preview); putErr != nil {
		t.Fatal(putErr)
	}
	filtered, filterErr := native.ListNativeSources(ctx, "org", "project", recovery.NativePageInput{Limit: 1, EnvironmentID: "environment"})
	if filterErr != nil || len(filtered.Sources) != 1 || filtered.Sources[0].EnvironmentID != "environment" {
		t.Fatalf("environment filtered after pagination: %+v %v", filtered, filterErr)
	}
	foreignEnv, filterErr := native.ListNativeSources(ctx, "org", "project", recovery.NativePageInput{EnvironmentID: "preview", Before: filtered.NextBefore})
	if !errors.Is(filterErr, recovery.ErrNotFound) || foreignEnv != nil {
		t.Fatal("foreign environment cursor accepted", filterErr)
	}
	sourceID := sources.Sources[0].ID
	next, nextErr := native.ListNativeSources(ctx, "org", "project", recovery.NativePageInput{Limit: 1, Before: sources.NextBefore})
	if nextErr != nil || len(next.Sources) != 1 || next.Sources[0].ID == sourceID || next.NextBefore != "" {
		t.Fatalf("next source page: %+v %v", next, nextErr)
	}
	if _, foreignErr := native.GetNativeSource(ctx, "foreign", sourceID); !errors.Is(foreignErr, recovery.ErrNotFound) {
		t.Fatalf("source escaped organization: %v", foreignErr)
	}
	point := recovery.NativePoint{ProviderID: "arn:aws:rds:us-east-1:123456789012:snapshot:database", Name: "database", Status: "available", Kind: "manual", Engine: "postgres", Version: "17.2", CreatedAt: now.Add(-time.Hour), Encrypted: true}
	expiry := now.Add(24 * time.Hour).In(time.FixedZone("fixture", 3600))
	point.ExpiresAt = &expiry
	refresh := func(points []recovery.NativePoint, complete bool, cause error) error {
		return native.RefreshNativeInventory(ctx, "org", sourceID, func(context.Context, *recovery.NativeSource) ([]recovery.NativePoint, bool, error) {
			return points, complete, cause
		})
	}
	if refreshErr := refresh([]recovery.NativePoint{point}, true, nil); refreshErr != nil {
		t.Fatal(refreshErr)
	}
	original, readErr := native.ReadNativeInventory(ctx, "org", sourceID, recovery.NativePageInput{})
	if readErr != nil || len(original.Points) != 1 || !original.Points[0].Present || original.Source.ObservedAt == nil || !original.Source.Complete {
		t.Fatalf("inventory not retained: %+v %v", original, readErr)
	}
	originalID := original.Points[0].ID
	if original.Points[0].ExpiresAt == nil || !original.Points[0].ExpiresAt.Equal(expiry) || original.Points[0].ExpiresAt.Location() != time.UTC {
		t.Fatal("stored point lost the provider's absolute expiry", original.Points)
	}
	invalid := point
	invalid.ExpiresAt = &invalid.CreatedAt
	if refreshErr := refresh([]recovery.NativePoint{invalid}, true, nil); !errors.Is(refreshErr, recovery.ErrNativeUnavailable) {
		t.Fatal("invalid backend expiry replaced previously captured metadata", refreshErr)
	}
	if refreshErr := refresh(nil, false, errors.New("private provider credential")); !errors.Is(refreshErr, recovery.ErrNativeUnavailable) {
		t.Fatal(refreshErr)
	}
	failed, readErr := native.ReadNativeInventory(ctx, "org", sourceID, recovery.NativePageInput{})
	if readErr != nil || len(failed.Points) != 1 || failed.Source.State != "failed" || failed.Source.Complete || failed.Source.ObservedAt == nil || !failed.Source.ObservedAt.Equal(*original.Source.ObservedAt) || strings.Contains(failed.Source.Error, "private") {
		t.Fatalf("failed refresh erased history or leaked details: %+v %v", failed, readErr)
	}
	// An incomplete scan cannot claim earlier points disappeared.
	if refreshErr := refresh(nil, false, nil); refreshErr != nil {
		t.Fatal(refreshErr)
	}
	partial, readErr := native.ReadNativeInventory(ctx, "org", sourceID, recovery.NativePageInput{})
	if readErr != nil || partial.Source.Complete || !partial.Points[0].Present {
		t.Fatalf("partial inventory removed a point: %+v %v", partial, readErr)
	}
	// Reusing a snapshot name with another creation time makes another identity.
	point.CreatedAt = point.CreatedAt.Add(30 * time.Minute)
	if refreshErr := refresh([]recovery.NativePoint{point}, true, nil); refreshErr != nil {
		t.Fatal(refreshErr)
	}
	page, readErr := native.ReadNativeInventory(ctx, "org", sourceID, recovery.NativePageInput{Limit: 1})
	if readErr != nil || len(page.Points) != 1 || page.Points[0].ID == originalID || !page.Points[0].Present || page.NextBefore == "" {
		t.Fatalf("snapshot name reuse collapsed identities: %+v %v", page, readErr)
	}
	older, readErr := native.ReadNativeInventory(ctx, "org", sourceID, recovery.NativePageInput{Limit: 1, Before: page.NextBefore})
	if readErr != nil || len(older.Points) != 1 || older.Points[0].ID != originalID || older.Points[0].Present {
		t.Fatalf("old incarnation lost: %+v %v", older, readErr)
	}
	if refreshErr := refresh([]recovery.NativePoint{point, point}, true, nil); !errors.Is(refreshErr, recovery.ErrNativeUnavailable) {
		t.Fatal("duplicate inventory accepted", refreshErr)
	}
	// A fresh store still sees the same source and recovery identities.
	persisted, persistErr := recovery.NewPgStore(db).ReadNativeInventory(ctx, "org", sourceID, recovery.NativePageInput{})
	if persistErr != nil || len(persisted.Points) != 2 {
		t.Fatalf("stored history not readable: %+v %v", persisted, persistErr)
	}
	for _, kept := range persisted.Points {
		if kept.ExpiresAt == nil || !kept.ExpiresAt.Equal(expiry) {
			t.Fatal("failed scans or reopening dropped native expiry", persisted.Points)
		}
	}
	point.ExpiresAt = nil
	if refreshErr := refresh([]recovery.NativePoint{point}, true, nil); refreshErr != nil {
		t.Fatal(refreshErr)
	}
	unknown, readErr := recovery.NewPgStore(db).ReadNativeInventory(ctx, "org", sourceID, recovery.NativePageInput{})
	if readErr != nil || len(unknown.Points) != 2 || unknown.Points[0].ExpiresAt != nil || unknown.Points[1].ExpiresAt == nil {
		t.Fatalf("latest missing expiry changed another point's history: %+v %v", unknown, readErr)
	}
}
