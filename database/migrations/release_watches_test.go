//go:build integration

package migrations

import (
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestReleaseWatchesMigration(t *testing.T) {
	conn, ex := scratch(t)
	ctx := t.Context()
	if _, err := migrate.NewOrchestrator(ex, Group()).Migrate(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	// Running the up again changes nothing.
	up(t, ex, "release_watches")

	exec(t, conn, `SET session_replication_role = replica`,
		`INSERT INTO loomplane_deployments (id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at)
 VALUES ('dep_1','org','proj','env','production','plan_1','abc','succeeded','u',now())`,
		`INSERT INTO loomplane_environments (id,org_id,project_id,name,kind,created_at,updated_at) VALUES ('env','org','proj','production','production',now(),now())`,
		`SET session_replication_role = DEFAULT`)
	watch := func(id, deployment, status string) string {
		return `INSERT INTO loomplane_release_watches (id,org_id,project_id,environment_id,environment,deployment_id,plan_id,status,thresholds,services,starts_at,ends_at,bound_at,created_at,updated_at)
 VALUES ('` + id + `','org','proj','env','production','` + deployment + `','plan_1','` + status + `','{}','[]',now(),now(),now(),now(),now())`
	}
	exec(t, conn, watch("watch_1", "dep_1", "watching"))
	if got := strs(t, conn, `SELECT type||'|'||entity_id||'|'||project_id FROM loomplane_events WHERE type LIKE 'release_watch.%'`); len(got) != 1 || got[0] != "release_watch.updated|watch_1|proj" {
		t.Fatalf("watch events: %v", got)
	}
	if got := strs(t, conn, `SELECT unwatched::text||'|'||held::text||'|'||held_released::text||'|'||unverified::text||'|'||revision::text||'|'||rollback_plan_id FROM loomplane_release_watches`); len(got) != 1 || got[0] != "[]|[]|false|false|1|" {
		t.Fatalf("defaults: %v", got)
	}
	// A claim stamps checked_at without a save and emits nothing; a save
	// bumps the revision and emits.
	exec(t, conn, `UPDATE loomplane_release_watches SET checked_at=now() WHERE id='watch_1'`)
	exec(t, conn, `UPDATE loomplane_release_watches SET status='passed', revision=revision+1 WHERE id='watch_1'`)
	if got := strs(t, conn, `SELECT data->>'status' FROM loomplane_events WHERE type='release_watch.updated' ORDER BY sequence`); len(got) != 2 || got[0] != "watching" || got[1] != "passed" {
		t.Fatalf("update events: %v", got)
	}
	for _, sql := range []string{watch("watch_again", "dep_1", "watching"), watch("watch_odd", "dep_2", "maybe")} {
		if _, err := conn.Exec(ctx, sql); err == nil {
			t.Fatalf("constraint accepted %s", sql)
		}
	}
	exec(t, conn, `DELETE FROM loomplane_deployments WHERE id='dep_1'`)
	if got := strs(t, conn, `SELECT count(*)::text FROM loomplane_release_watches`); got[0] != "0" {
		t.Fatalf("a deleted deployment left %s watches", got[0])
	}
	if got := strs(t, conn, `SELECT count(*)::text FROM loomplane_events WHERE type='release_watch.deleted'`); got[0] != "1" {
		t.Fatalf("deleted events: %v", got)
	}

	down(t, ex, "release_watches", "release_watches")
	if got := strs(t, conn, `SELECT count(*)::text FROM pg_trigger WHERE tgname IN ('loomplane_state_event','loomplane_state_event_update') AND tgrelid=to_regclass('loomplane_release_watches')`); len(got) != 1 || got[0] != "0" {
		t.Fatalf("down left triggers: %v", got)
	}
	if got := strs(t, conn, `SELECT coalesce(to_regclass('loomplane_release_watches')::text,'gone')`); got[0] != "gone" {
		t.Fatalf("down left the table: %v", got)
	}
	up(t, ex, "release_watches")
}
