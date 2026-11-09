//go:build integration

package migrations

import (
	"slices"
	"strings"
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestAgentReportTablesAndTrigger(t *testing.T) {
	// Databases record the version and name, so the filename can't drift.
	if m := migration(t, "agent_reports"); m.Version != "20261220000600" {
		t.Fatalf("version %q", m.Version)
	}
	conn, ex := scratch(t)
	ctx := t.Context()
	if _, err := migrate.NewOrchestrator(ex, All()...).Migrate(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	// Running the up again changes nothing.
	up(t, ex, "agent_reports")

	tables := `SELECT coalesce(to_regclass('loomplane_agent_reports')::text,'gone')||'|'||coalesce(to_regclass('loomplane_agent_watchers')::text,'gone')`
	if got := strs(t, conn, tables); len(got) != 1 || got[0] != "loomplane_agent_reports|loomplane_agent_watchers" {
		t.Fatalf("tables: %v", got)
	}
	if got := strs(t, conn, `SELECT column_name FROM information_schema.columns WHERE table_name='loomplane_github_installations' AND column_name IN ('permissions','permissions_checked_at') ORDER BY column_name`); !slices.Equal(got, []string{"permissions", "permissions_checked_at"}) {
		t.Fatalf("installation columns: %v", got)
	}
	for _, index := range []string{
		"loomplane_agent_reports_open", "loomplane_agent_reports_status", "loomplane_agent_reports_project",
		"loomplane_agent_reports_fingerprint", "loomplane_agent_reports_undelivered",
		"loomplane_agent_reports_evidence_deployment", "loomplane_agent_reports_evidence_plan",
	} {
		if got := strs(t, conn, `SELECT count(*)::text FROM pg_indexes WHERE indexname='`+index+`'`); got[0] != "1" {
			t.Fatalf("index %s: %v", index, got)
		}
	}

	report := func(id, fingerprint, status string) string {
		return `INSERT INTO loomplane_agent_reports (id,org_id,project_id,signal,component,fingerprint,status,severity,title,evidence,findings,first_seen_at,last_seen_at,created_at,updated_at)
 VALUES ('` + id + `','org','proj','deployment_failed','deployment','` + fingerprint + `','` + status + `','high','Deployment failed','{"deployment_id":"dep_1"}','{"summary":"s"}',now(),now(),now(),now())`
	}
	exec(t, conn, report("arep_1", "fp_1", "open"))

	events := func() []string {
		return strs(t, conn, `SELECT type||'|'||entity_id||'|'||project_id FROM loomplane_events WHERE type LIKE 'agent_report.%' ORDER BY sequence`)
	}
	if got := events(); len(got) != 1 || got[0] != "agent_report.updated|arep_1|proj" {
		t.Fatalf("report events: %v", got)
	}

	// The event carries exactly the Report's JSON fields, the issue folded
	// into one object, and none of the delivery or lease bookkeeping.
	raw := strs(t, conn, `SELECT data::text FROM loomplane_events WHERE type='agent_report.updated'`)
	want := []string{"closed_by", "component", "created_at", "environment", "environment_id", "evidence", "fingerprint", "findings", "first_seen_at", "id", "investigation", "investigation_note", "issue", "last_seen_at", "occurrences", "opened_by", "org_id", "project_id", "repository", "resolution", "resolved_at", "revision", "severity", "signal", "status", "task_id", "taken_at", "taken_by", "title", "updated_at", "facts"}
	slices.Sort(want)
	keys := keysOf(t, raw[0])
	if !slices.Equal(keys, want) {
		t.Fatalf("report event keys %v, want %v", keys, want)
	}
	for _, private := range []string{
		"occurrence_keys", "delivery_owner", "delivery_until", "delivery_queued_at", "pending_notice", "notice_seq", "taken_notice_seq",
		"issue_comments", "issue_comment_at", "run_id", "opened_by_task", "issue_number", "issue_state", "checked_at",
		"notified_revision", "issue_revision", "last_notice_at", "investigation_started_at",
	} {
		if slices.Contains(keys, private) {
			t.Fatalf("report event carries %s: %s", private, raw[0])
		}
	}
	if got := strs(t, conn, `SELECT keys.k FROM loomplane_events e, LATERAL (SELECT jsonb_object_keys(e.data->'issue') AS k) keys WHERE e.type='agent_report.updated' ORDER BY keys.k`); !slices.Equal(got, []string{"error", "number", "state", "url"}) {
		t.Fatalf("issue keys: %v", got)
	}
	if got := strs(t, conn, `SELECT (data->'issue'->>'state')||'|'||(data->'findings'->>'summary')||'|'||(data->'evidence'->>'deployment_id')||'|'||(data->>'taken_by')||'|'||jsonb_typeof(data->'taken_at') FROM loomplane_events WHERE type='agent_report.updated'`); got[0] != "none|s|dep_1||null" {
		t.Fatalf("event values: %v", got)
	}

	// Lease, queue and cursor columns never produce an event; a column the
	// event shows produces one.
	exec(t, conn,
		`UPDATE loomplane_agent_reports SET delivery_owner='worker', delivery_until=now(), delivery_queued_at=now(), pending_notice='opened', notice_seq=1, taken_notice_seq=1, occurrence_keys='["k"]', issue_comments='["repeated"]', run_id='run_1', checked_at=now(), notified_revision=1,
 investigation_started_at=now(), issue_revision=1, last_notice_at=now(), issue_comment_at=now(), opened_by_task='atask_1' WHERE id='arep_1'`)
	if got := events(); len(got) != 1 {
		t.Fatalf("bookkeeping emitted: %v", got)
	}
	exec(t, conn, `UPDATE loomplane_agent_reports SET title='Deployment failed again' WHERE id='arep_1'`)
	if got := events(); len(got) != 2 {
		t.Fatalf("title change: %v", got)
	}
	exec(t, conn, `UPDATE loomplane_agent_reports SET taken_by='usr_ada', taken_at=now() WHERE id='arep_1'`)
	if got := events(); len(got) != 3 {
		t.Fatalf("taken change: %v", got)
	}
	exec(t, conn, `UPDATE loomplane_agent_reports SET issue_number=7, issue_url='https://example.test/7', issue_state='open' WHERE id='arep_1'`)
	if got := strs(t, conn, `SELECT (data->'issue'->>'number')||'|'||(data->'issue'->>'state') FROM loomplane_events WHERE type='agent_report.updated' ORDER BY sequence DESC LIMIT 1`); got[0] != "7|open" {
		t.Fatalf("issue change: %v", got)
	}

	// An open report is unique by workspace and fingerprint. Once the first
	// resolves, the same fingerprint opens again.
	if _, err := conn.Exec(ctx, report("arep_2", "fp_1", "open")); err == nil || !strings.Contains(err.Error(), "loomplane_agent_reports_open") {
		t.Fatalf("second open report: %v", err)
	}
	exec(t, conn, `UPDATE loomplane_agent_reports SET status='resolved', resolved_at=now() WHERE id='arep_1'`)
	exec(t, conn, report("arep_2", "fp_1", "open"))

	for _, sql := range []string{
		strings.Replace(report("arep_x", "fp_x", "open"), "'deployment_failed'", "'thunderstorm'", 1),
		strings.Replace(report("arep_x", "fp_x", "open"), "'high'", "'urgent'", 1),
		report("arep_x", "fp_x", "maybe"),
		`UPDATE loomplane_agent_reports SET investigation='pondering' WHERE id='arep_2'`,
		`UPDATE loomplane_agent_reports SET issue_state='lost' WHERE id='arep_2'`,
		`UPDATE loomplane_agent_reports SET pending_notice='shouted' WHERE id='arep_2'`,
	} {
		if _, err := conn.Exec(ctx, sql); err == nil {
			t.Fatalf("constraint accepted %s", sql)
		}
	}

	// A new row takes the defaults.
	if got := strs(t, conn, `SELECT occurrences||'|'||revision||'|'||notified_revision||'|'||issue_revision||'|'||investigation||'|'||issue_state||'|['||taken_by||']|'||taken_notice_seq||'|'||(taken_at IS NULL)||'|'||facts::text FROM loomplane_agent_reports WHERE id='arep_2'`); got[0] != "1|1|0|0|none|none|[]|0|true|[]" {
		t.Fatalf("report defaults: %v", got)
	}

	// The watcher row holds no event and starts unpaused at cursor 0.
	exec(t, conn, `INSERT INTO loomplane_agent_watchers (org_id,created_at,updated_at) VALUES ('org',now(),now())`)
	if got := strs(t, conn, `SELECT event_cursor||'|'||paused||'|'||checks::text FROM loomplane_agent_watchers`); got[0] != "0|false|{}" {
		t.Fatalf("watcher defaults: %v", got)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO loomplane_agent_watchers (org_id,created_at,updated_at) VALUES ('org',now(),now())`); err == nil {
		t.Fatal("two watcher rows for one workspace")
	}
	if got := strs(t, conn, `SELECT count(*)::text FROM loomplane_events WHERE type LIKE 'agent_watcher.%'`); got[0] != "0" {
		t.Fatalf("watcher events: %v", got)
	}

	// Deleting a report is an event.
	exec(t, conn, `DELETE FROM loomplane_agent_reports WHERE id='arep_2'`)
	if got := events(); got[len(got)-1] != "agent_report.deleted|arep_2|proj" {
		t.Fatalf("delete events: %v", got)
	}

	// Down leaves no table, function or installation column, and can run again.
	down(t, ex, "agent_reports", "agent_reports")
	if got := strs(t, conn, tables); got[0] != "gone|gone" {
		t.Fatalf("down left tables: %v", got)
	}
	if got := strs(t, conn, `SELECT count(*)::text FROM pg_proc WHERE proname='loomplane_agent_report_event_trigger'`); got[0] != "0" {
		t.Fatalf("down left the trigger function: %v", got)
	}
	if got := strs(t, conn, `SELECT count(*)::text FROM information_schema.columns WHERE table_name='loomplane_github_installations' AND column_name IN ('permissions','permissions_checked_at')`); got[0] != "0" {
		t.Fatalf("down left installation columns: %v", got)
	}
	// Up again gives empty tables back.
	up(t, ex, "agent_reports")
	if got := strs(t, conn, `SELECT (SELECT count(*) FROM loomplane_agent_reports)+(SELECT count(*) FROM loomplane_agent_watchers)||''`); got[0] != "0" {
		t.Fatalf("rows after down and up: %v", got)
	}
}
