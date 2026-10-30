//go:build integration

package migrations

import (
	"strconv"
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestCanaryReleasesMigration(t *testing.T) {
	conn, ex := scratch(t)
	ctx := t.Context()
	// Everything else goes first, so canary_releases is the last one applied
	// and one rollback undoes exactly it, whatever sorts after it.
	historical := migrate.NewGroup(GroupName)
	for _, step := range Group().Migrations() {
		if step.Name != "managed_workload_protocol" {
			historical.MustRegister(step)
		}
	}
	others := migrate.NewGroup(GroupName)
	for _, step := range historical.Migrations() {
		if step.Name != "canary_releases" {
			others.MustRegister(step)
		}
	}
	if _, err := migrate.NewOrchestrator(ex, others).Migrate(ctx); err != nil {
		t.Fatalf("up others: %v", err)
	}
	o := migrate.NewOrchestrator(ex, historical)
	if _, err := o.Migrate(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	// Running the up again changes nothing.
	up(t, ex, "canary_releases", "canary_releases")
	if got := strs(t, conn, `SELECT conname::text FROM pg_constraint WHERE conrelid='loomplane_deployment_workflow_runs'::regclass AND contype='c'`); len(got) != 1 || got[0] != "loomplane_deployment_workflow_runs_build" {
		t.Fatalf("build checks after up: %v", got)
	}

	// The deployment row only anchors the verdicts' foreign key, so skip its
	// own references and triggers.
	exec(t, conn, `SET session_replication_role = replica`,
		`INSERT INTO loomplane_deployments (id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at)
 VALUES ('dep_1','org','proj','env','production','plan_1','abc','running','u',now())`,
		`SET session_replication_role = DEFAULT`)
	exec(t, conn, `UPDATE loomplane_deployments SET canary='{"status":"ramping"}' WHERE id='dep_1'`)

	verdict := func(id, verdict string, weight int) string {
		return `INSERT INTO loomplane_canary_verdicts (id,org_id,project_id,deployment_id,retry,action_id,service,step,attempt,weight,verdict,reason,baseline,candidate,thresholds,window_start,window_end,decided_at)
 VALUES ('` + id + `','org','proj','dep_1',0,'service:api','api',0,0,` + strconv.Itoa(weight) + `,'` + verdict + `','ok','{}','{}','{}',now(),now(),now())`
	}
	exec(t, conn, verdict("dep_1/0/service:api/0/0", "pass", 10))
	if got := strs(t, conn, `SELECT type||'|'||entity_id||'|'||project_id FROM loomplane_events WHERE type LIKE 'canary_verdict.%'`); len(got) != 1 || got[0] != "canary_verdict.updated|dep_1/0/service:api/0/0|proj" {
		t.Fatalf("verdict events: %v", got)
	}
	for _, sql := range []string{verdict("v_maybe", "maybe", 10), verdict("v_heavy", "pass", 101)} {
		if _, err := conn.Exec(ctx, sql); err == nil {
			t.Fatalf("constraint accepted %s", sql)
		}
	}

	run := func(id, build string) string {
		return `INSERT INTO loomplane_deployment_workflow_runs (org_id,plan_id,workflow_id,request) VALUES ('org','plan_1','` + id + `','{"build_id":"` + build + `"}')`
	}
	exec(t, conn, run("wf_v4", "deployment-v4"))
	if _, err := conn.Exec(ctx, run("wf_v5", "deployment-v5")); err == nil {
		t.Fatal("deployment-v5 run accepted")
	}

	canary := migration(t, "canary_releases")
	if err := canary.Down(ctx, ex); err == nil {
		t.Fatal("down dropped the verdicts")
	}
	exec(t, conn, `DELETE FROM loomplane_canary_verdicts`)
	if err := canary.Down(ctx, ex); err == nil {
		t.Fatal("down stranded a deployment-v4 run")
	}
	exec(t, conn, `DELETE FROM loomplane_deployment_workflow_runs`)
	// The orchestrator rolls back the canary's own version.
	res, err := o.Rollback(ctx)
	if err != nil || len(res.Rollback) != 1 || res.Rollback[0].Version != "20261104000100" || res.Rollback[0].Name != "canary_releases" {
		t.Fatalf("rollback: %+v %v", res, err)
	}
	// Running the down again changes nothing.
	down(t, ex, "canary_releases")
	if got := strs(t, conn, `SELECT coalesce(to_regclass('loomplane_canary_verdicts')::text,'gone')`); len(got) != 1 || got[0] != "gone" {
		t.Fatalf("down left the table: %v", got)
	}
	if got := strs(t, conn, `SELECT column_name::text FROM information_schema.columns WHERE table_name='loomplane_deployments' AND column_name='canary'`); len(got) != 0 {
		t.Fatalf("down left the column: %v", got)
	}
	// The two-build check is back under the name Postgres first gave it.
	if got := strs(t, conn, `SELECT conname::text FROM pg_constraint WHERE conrelid='loomplane_deployment_workflow_runs'::regclass AND contype='c'`); len(got) != 1 || got[0] != "loomplane_deployment_workflow_runs_request_check" {
		t.Fatalf("build checks after down: %v", got)
	}
	exec(t, conn, run("wf_v3", "deployment-v3"))
	if _, err = conn.Exec(ctx, run("wf_v4_again", "deployment-v4")); err == nil {
		t.Fatal("down kept accepting deployment-v4 runs")
	}

	// Up again over the restored two-build check.
	if _, err = o.Migrate(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	exec(t, conn, run("wf_v4_after", "deployment-v4"))
	if got := strs(t, conn, `SELECT conname::text FROM pg_constraint WHERE conrelid='loomplane_deployment_workflow_runs'::regclass AND contype='c' ORDER BY conname`); len(got) != 1 || got[0] != "loomplane_deployment_workflow_runs_build" {
		t.Fatalf("build checks after up: %v", got)
	}
}
