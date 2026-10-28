//go:build integration

package planning

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/releasehealth"
)

// releaseDatabase migrates a scratch database and returns a store over it,
// with a direct connection for rows the test sets up.
func releaseDatabase(t *testing.T) (*PgStore, *pgx.Conn) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the PostgreSQL release test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("plan_release_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	if err = ops.Migrate(ctx, u.String(), migrations.All()); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	// The rows only anchor what the test reads, so skip their references
	// and triggers.
	if _, err = conn.Exec(ctx, `SET session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	return NewPgStore(pg), conn
}

func TestReleaseSurvivesStorageInPostgres(t *testing.T) {
	store, conn := releaseDatabase(t)
	ctx := context.Background()
	for i, release := range []*Release{
		{
			Strategy: "canary", Steps: []int{10, 100}, HoldSeconds: 900,
			Thresholds: releasehealth.Thresholds{ErrorRate: 0.02, ErrorRateDelta: 0.01, P95LatencyRatio: 1.5, MinRequests: 50},
			Canary:     []string{"service:api"},
		},
		{Strategy: "canary", Steps: []int{5, 25, 50, 100}, HoldSeconds: 600, Thresholds: releasehealth.DefaultThresholds()},
		nil,
	} {
		id := fmt.Sprintf("plan_%d", i)
		if _, err := conn.Exec(ctx, `INSERT INTO loomplane_plans (id, org_id, project_id, environment_id, environment, status, manifest_id, manifest_revision, created_by, created_at)
			VALUES ($1, 'org', 'proj', $2, 'production', 'planning', 'man', 1, 'usr', now())`, id, "env_"+id); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		p := &Plan{ID: id, OrgID: "org", Status: StatusReady, Revision: "r1", PlannedAt: &now}
		p.setResult(&Result{Actions: []Action{}, Problems: []Finding{}, Warnings: []Finding{}, Connections: map[string]string{}, Executable: true, Release: release})
		if ok, err := store.FinishPlanning(ctx, p); err != nil || !ok {
			t.Fatalf("finish %s: %v, %v", id, ok, err)
		}
		got, err := store.GetPlan(ctx, "org", id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Release, release) {
			t.Fatalf("%s: read %+v, want %+v", id, got.Release, release)
		}
	}
}

func TestRampingCanaryInPostgres(t *testing.T) {
	store, conn := releaseDatabase(t)
	ctx := context.Background()
	deployment := func(id, org, env, status, canary string, started time.Time) {
		t.Helper()
		if _, err := conn.Exec(ctx, `INSERT INTO loomplane_deployments (id, org_id, project_id, environment_id, environment, plan_id, commit_sha, status, started_by, started_at, canary)
			VALUES ($1, $2, 'proj', $3, 'production', 'plan_' || $1, 'abc', $4, 'usr', $5, $6::jsonb)`, id, org, env, status, started, canary); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	deployment("dep_done", "org", "env", "succeeded", `{"status":"promoted"}`, at)
	deployment("dep_promoted", "org", "env", "running", `{"status":"promoted"}`, at)
	// A canary the workflow gave up on, even on a deployment a cancel left
	// running, no longer holds the environment.
	for _, status := range []string{"rolled_back", "revert_failed", "promote_failed"} {
		deployment("dep_"+status, "org", "env", "running", `{"status":"`+status+`"}`, at)
	}
	deployment("dep_other_env", "org", "env_2", "running", `{"status":"ramping"}`, at)
	deployment("dep_other_org", "org_2", "env", "running", `{"status":"ramping"}`, at)
	if id, err := store.RampingCanary(ctx, "org", "env"); err != nil || id != "" {
		t.Fatalf("nothing ramping: %q, %v", id, err)
	}
	for _, status := range []string{"pending", "ramping", "promoting", "reverting"} {
		at = at.Add(time.Minute)
		id := "dep_" + status
		deployment(id, "org", "env", "running", `{"status":"`+status+`"}`, at)
		if got, err := store.RampingCanary(ctx, "org", "env"); err != nil || got != id {
			t.Fatalf("%s: %q, %v", status, got, err)
		}
	}
}
