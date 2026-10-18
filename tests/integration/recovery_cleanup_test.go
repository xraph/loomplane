//go:build integration

package integration

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/recovery"
)

func TestRecoveryCleanupFailureDoesNotStarveLaterCopies(t *testing.T) {
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
	for _, sql := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p','o','p','p','ready','u',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('e','o','p','prod','production',now(),now())`,
		`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('c','o','server','c','daemon','ready','u',now(),now())`,
		`INSERT INTO loomplane_service_backups(id,org_id,project_id,environment_id,connection_id,name,status,request_id,source,created_by,created_at) VALUES('b','o','p','e','c','db','succeeded','backup','{}','u',now())`,
		`INSERT INTO loomplane_service_restores(id,org_id,project_id,backup_id,status,request_id,created_by,created_at,expires_at) SELECT 'r'||n,'o','p','b','succeeded','request'||n,'u',now()-interval '3 days',now()-interval '2 days'+n*interval '1 minute' FROM generate_series(1,11) n`,
	} {
		if _, err = conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	store := recovery.NewPgStore(pg)
	first, err := store.ExpiredRestores(ctx)
	if err != nil || len(first) != 10 {
		t.Fatalf("initial batch: %d %v", len(first), err)
	}
	for _, r := range first {
		claimed, claimErr := store.ClaimCleanup(ctx, r.OrgID, r.ID)
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		if err = store.FinishCleanup(ctx, claimed, errors.New("target unavailable")); err != nil {
			t.Fatal(err)
		}
	}
	// Reopening the store must preserve backoff so older failures cannot monopolize the batch.
	store = recovery.NewPgStore(pg)
	next, err := store.ExpiredRestores(ctx)
	if err != nil || len(next) != 1 || next[0].ID != "r11" {
		t.Fatalf("failed copies starved later cleanup: %+v %v", next, err)
	}
	// An administrator can retry immediately without waiting for automatic backoff.
	r, err := store.ClaimCleanup(ctx, "o", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishCleanup(ctx, r, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `UPDATE loomplane_service_restores SET lease_until=now()-interval '1 minute' WHERE status='cleanup_failed'`); err != nil {
		t.Fatal(err)
	}
	eligible, err := store.ExpiredRestores(ctx)
	if err != nil || len(eligible) != 10 {
		t.Fatalf("retry eligibility: %d %v", len(eligible), err)
	}
	for _, r := range eligible {
		if r.ID == "r1" {
			t.Fatalf("deleted copy selected: %+v", r)
		}
	}
}
