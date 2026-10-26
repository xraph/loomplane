//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/database/migrations"
)

func TestNativeRetirementKeepsSourceCredentialReference(t *testing.T) {
	f, store, request := nativeRetirementFixture(t)
	r, err := store.AdmitNativeRetirement(t.Context(), request, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var uses int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_connection_uses WHERE org_id='org' AND connection_id='account' AND kind='native_retirement' AND owner_id=$1`, r.ID).Scan(&uses); err != nil || uses != 1 {
		t.Fatal("native deletion can lose its cloud credential while pending", uses, err)
	}
}

func TestNativeRetirementDowngradeRefusesConcurrentAdmission(t *testing.T) {
	f, store, request := nativeRetirementFixture(t)
	r, err := store.AdmitNativeRetirement(t.Context(), request, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `DELETE FROM loomplane_native_retirement_events;DELETE FROM loomplane_native_retirements`); err != nil {
		t.Fatal(err)
	}
	raw := make([][]byte, 4)
	for i, v := range []any{r.Policy, r.Source, r.Point, r.Witness} {
		raw[i], err = json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
	}
	writer, err := f.sql.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	if _, err = writer.Exec(t.Context(), `INSERT INTO loomplane_native_retirements(id,org_id,project_id,source_id,point_id,witness_id,request_id,created_by,status,policy,source,point,witness) VALUES($1,'org','project',$2,$3,$4,'concurrent-admission','operator','queued',$5,$6,$7,$8)`, r.ID, r.SourceID, r.PointID, r.WitnessID, raw[0], raw[1], raw[2], raw[3]); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	for _, step := range migrations.Group().Migrations() {
		if step.Name == "native_retirements" {
			go func() { finished <- step.Down(ctx, pgmigrate.New(f.db)) }()
			break
		}
	}
	waiting := false
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); {
		if err = f.sql.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%native_retirements_down%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("did not observe native downgrade waiting on the writer")
	}
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	requireNativePointDowngradeRefusal(t, <-finished)
	var count int
	if err = f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_native_retirements`).Scan(&count); err != nil || count != 1 {
		t.Fatal("native downgrade erased accepted deletion", count, err)
	}
}
