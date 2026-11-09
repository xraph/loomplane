//go:build integration

package migrations

import (
	"strings"
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestReleaseReportsTableAndTrigger(t *testing.T) {
	// Databases record the version and name, so the filename can't drift.
	if m := migration(t, "release_reports"); m.Version != "20261220000700" {
		t.Fatalf("version %q", m.Version)
	}
	conn, ex := scratch(t)
	ctx := t.Context()
	if _, err := migrate.NewOrchestrator(ex, All()...).Migrate(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	// Running the up again changes nothing.
	up(t, ex, "release_reports")

	table := `SELECT coalesce(to_regclass('loomplane_release_reports')::text,'gone')`
	if got := strs(t, conn, table); got[0] != "loomplane_release_reports" {
		t.Fatalf("table: %v", got)
	}
	for _, index := range []string{"loomplane_release_reports_active", "loomplane_release_reports_project", "loomplane_release_reports_plan", "loomplane_release_reports_unsettled"} {
		if got := strs(t, conn, `SELECT count(*)::text FROM pg_indexes WHERE indexname='`+index+`'`); got[0] != "1" {
			t.Fatalf("index %s: %v", index, got)
		}
	}

	report := func(id, to, status string) string {
		return `INSERT INTO loomplane_release_reports (id,org_id,project_id,to_environment_id,status,state,requested_by,created_at,updated_at)
 VALUES ('` + id + `','org','proj','` + to + `','` + status + `','` + status + `','usr_ada',now(),now())`
	}
	exec(t, conn, report("rrep_1", "env_prod", "planning"))
	if got := strs(t, conn, `SELECT scope::text||'|'||gates::text||'|'||concerns::text||'|'||(evidence IS NULL)||'|'||preview||'|'||reused_plan||'|['||notified_state||']' FROM loomplane_release_reports WHERE id='rrep_1'`); got[0] != "[]|[]|[]|true|false|false|[]" {
		t.Fatalf("defaults: %v", got)
	}

	// One preparation in progress per environment.
	if _, err := conn.Exec(ctx, report("rrep_2", "env_prod", "reviewing")); err == nil || !strings.Contains(err.Error(), "loomplane_release_reports_active") {
		t.Fatalf("second active report: %v", err)
	}
	exec(t, conn, `UPDATE loomplane_release_reports SET status='done', state='promoted' WHERE id='rrep_1'`)
	exec(t, conn, report("rrep_2", "env_prod", "planning"))

	for _, sql := range []string{
		report("rrep_x", "env_x", "thinking"),
		strings.Replace(report("rrep_x", "env_x", "planning"), "'usr_ada'", "''", 1),
		strings.Replace(report("rrep_x", "env_x", "planning"), "'proj'", "''", 1),
		strings.Replace(report("rrep_x", "env_x", "planning"), "'env_x'", "''", 1),
		strings.Replace(report("rrep_x", "env_x", "planning"), ",'planning','usr_ada'", ",'pondering','usr_ada'", 1),
		`UPDATE loomplane_release_reports SET decision='maybe' WHERE id='rrep_2'`,
	} {
		if _, err := conn.Exec(ctx, sql); err == nil {
			t.Fatalf("constraint accepted %s", sql)
		}
	}

	// The event carries no evidence, concerns or notice bookkeeping, and a
	// change to only those emits nothing.
	events := func() []string {
		return strs(t, conn, `SELECT type||'|'||entity_id||'|'||project_id FROM loomplane_events WHERE type LIKE 'release_report.%' ORDER BY sequence`)
	}
	before := len(events())
	if before != 3 {
		t.Fatalf("events: %v", events())
	}
	if got := strs(t, conn, `SELECT count(*)::text FROM loomplane_events WHERE type LIKE 'release_report.%' AND (data ? 'evidence' OR data ? 'concerns' OR data ? 'notified_state' OR data ? 'swept_at')`); got[0] != "0" {
		t.Fatalf("events with private keys: %v", got)
	}
	exec(t, conn, `UPDATE loomplane_release_reports SET evidence='{"diffs":[]}', concerns='[{"text":"x"}]', notified_state='promoted', swept_at=now() WHERE id='rrep_2'`)
	if got := events(); len(got) != before {
		t.Fatalf("private columns emitted: %v", got)
	}
	exec(t, conn, `UPDATE loomplane_release_reports SET state_reason='r' WHERE id='rrep_2'`)
	if got := events(); len(got) != before+1 {
		t.Fatalf("state reason change: %v", got)
	}
	exec(t, conn, `DELETE FROM loomplane_release_reports WHERE id='rrep_2'`)
	if got := events(); got[len(got)-1] != "release_report.deleted|rrep_2|proj" {
		t.Fatalf("delete events: %v", got)
	}

	// Down leaves no table or function, and can run again.
	down(t, ex, "release_reports", "release_reports")
	if got := strs(t, conn, table); got[0] != "gone" {
		t.Fatalf("down left the table: %v", got)
	}
	if got := strs(t, conn, `SELECT count(*)::text FROM pg_proc WHERE proname='loomplane_release_report_event_trigger'`); got[0] != "0" {
		t.Fatalf("down left the trigger function: %v", got)
	}
	up(t, ex, "release_reports")
	if got := strs(t, conn, `SELECT count(*)::text FROM loomplane_release_reports`); got[0] != "0" {
		t.Fatalf("rows after down and up: %v", got)
	}
}
