//go:build integration

package migrations

import (
	"strings"
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestProjectAutonomyColumns(t *testing.T) {
	// Databases record the version and name, so the filename can't drift.
	if m := migration(t, "project_autonomy"); m.Version != "20261207000300" {
		t.Fatalf("version %q", m.Version)
	}
	conn, ex := scratch(t)
	if _, err := migrate.NewOrchestrator(ex, All()...).Migrate(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}
	// Running the up again changes nothing.
	up(t, ex, "project_autonomy")

	cols := strs(t, conn, `SELECT column_name||' '||data_type||' '||is_nullable||' '||coalesce(column_default,'')
		FROM information_schema.columns
		WHERE table_name='loomplane_projects' AND column_name IN ('autonomous_promotion','min_staging_soak_seconds')
		ORDER BY column_name`)
	want := []string{"autonomous_promotion boolean NO false", "min_staging_soak_seconds bigint NO 0"}
	if strings.Join(cols, "\n") != strings.Join(want, "\n") {
		t.Fatalf("columns:\n%s\nwant\n%s", strings.Join(cols, "\n"), strings.Join(want, "\n"))
	}
	// The check is validated, not left NOT VALID.
	if got := strs(t, conn, `SELECT convalidated::text FROM pg_constraint WHERE conname='loomplane_projects_min_staging_soak'`); len(got) != 1 || got[0] != "true" {
		t.Fatalf("constraint: %v", got)
	}

	// A project starts at "ask first".
	exec(t, conn, `INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p','o','Project','project','ready','u',now(),now())`)
	if got := strs(t, conn, `SELECT autonomous_promotion||'|'||min_staging_soak_seconds FROM loomplane_projects WHERE id='p'`); got[0] != "false|0" {
		t.Fatalf("defaults: %v", got)
	}
	// The soak is a week at most and never negative.
	for _, bad := range []string{"-1", "604801"} {
		if _, err := conn.Exec(t.Context(), `UPDATE loomplane_projects SET min_staging_soak_seconds=`+bad+` WHERE id='p'`); err == nil {
			t.Fatalf("soak %s was accepted", bad)
		}
	}
	exec(t, conn, `UPDATE loomplane_projects SET autonomous_promotion=TRUE, min_staging_soak_seconds=604800 WHERE id='p'`)

	// The realtime project event carries both, under the API's names.
	got := strs(t, conn, `SELECT (data->>'autonomous_promotion')||'|'||(data->>'min_staging_soak_seconds') FROM loomplane_events
		WHERE type='project.updated' AND entity_id='p' ORDER BY sequence DESC LIMIT 1`)
	if len(got) != 1 || got[0] != "true|604800" {
		t.Fatalf("project event: %v", got)
	}

	// The down drops the columns and the check, and the up brings them back.
	down(t, ex, "project_autonomy")
	if got := strs(t, conn, `SELECT count(*)::text FROM information_schema.columns WHERE table_name='loomplane_projects' AND column_name IN ('autonomous_promotion','min_staging_soak_seconds')`); got[0] != "0" {
		t.Fatalf("columns after down: %v", got)
	}
	up(t, ex, "project_autonomy")
	if got := strs(t, conn, `SELECT autonomous_promotion||'|'||min_staging_soak_seconds FROM loomplane_projects WHERE id='p'`); got[0] != "false|0" {
		t.Fatalf("after down and up: %v", got)
	}
}
