//go:build integration

package migrations

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestAgentTablesAndTriggers(t *testing.T) {
	// Databases record the version and name, so the filename can't drift.
	if m := migration(t, "agent_foundation"); m.Version != "20261207000200" {
		t.Fatalf("version %q", m.Version)
	}
	conn, ex := scratch(t)
	ctx := t.Context()
	if _, err := migrate.NewOrchestrator(ex, All()...).Migrate(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	// Running the up again changes nothing.
	up(t, ex, "agent_foundation")

	tables := `SELECT coalesce(to_regclass('loomplane_agent_tasks')::text,'gone')||'|'||coalesce(to_regclass('loomplane_agent_run_events')::text,'gone')||'|'||coalesce(to_regclass('loomplane_agent_requests')::text,'gone')`
	if got := strs(t, conn, tables); len(got) != 1 || got[0] != "loomplane_agent_tasks|loomplane_agent_run_events|loomplane_agent_requests" {
		t.Fatalf("tables: %v", got)
	}
	// The agent's settings are columns on loomplane_ai_settings, not a table.
	if got := strs(t, conn, `SELECT coalesce(to_regclass('loomplane_agent_settings')::text,'gone')`); got[0] != "gone" {
		t.Fatalf("agent settings table: %v", got)
	}

	task := func(id, run, kind, status string, maxTokens string) string {
		return `INSERT INTO loomplane_agent_tasks (id,org_id,project_id,kind,run_id,status,requested_by,principal,input,max_tokens,metadata,created_at,updated_at)
 VALUES ('` + id + `','org','proj','` + kind + `','` + run + `','` + status + `','usr_ada','{"user_id":"usr_ada","org_id":"org","roles":["viewer"]}','please read my private notes',` + maxTokens + `,'{"origin":"chat"}',now(),now())`
	}
	exec(t, conn, task("atask_1", "run_1", "chat", "queued", "4000"))

	// The task event names the task and never carries the chat message,
	// the principal or the metadata.
	type event struct{ typ, entity, project string }
	events := func(like string) []event {
		rows, err := conn.Query(ctx, `SELECT type,entity_id,project_id FROM loomplane_events WHERE type LIKE $1 ORDER BY sequence`, like)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []event
		for rows.Next() {
			var e event
			if err := rows.Scan(&e.typ, &e.entity, &e.project); err != nil {
				t.Fatal(err)
			}
			out = append(out, e)
		}
		return out
	}
	if got := events("agent_task.%"); len(got) != 1 || got[0] != (event{"agent_task.updated", "atask_1", "proj"}) {
		t.Fatalf("task events: %v", got)
	}
	raw := strs(t, conn, `SELECT data::text FROM loomplane_events WHERE type='agent_task.updated'`)
	var data map[string]any
	if err := json.Unmarshal([]byte(raw[0]), &data); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"created_at", "environment_id", "error", "event_seq", "finished_at", "id", "kind", "max_tokens", "org_id", "project_id", "requested_by", "run_id", "status", "tokens", "updated_at"}
	if !slices.Equal(keys, want) {
		t.Fatalf("task event keys %v, want %v", keys, want)
	}
	for _, private := range []string{"input", "principal", "metadata", "session_id"} {
		if _, ok := data[private]; ok {
			t.Fatalf("task event carries %s: %s", private, raw[0])
		}
	}
	if got := strs(t, conn, `SELECT environment_id||'|'||session_id||'|'||tokens||'|'||event_seq||'|['||error||']|'||(continue_queued_at IS NULL)||'|'||(enqueued_at IS NULL)||'|'||(finished_at IS NULL) FROM loomplane_agent_tasks`); len(got) != 1 || got[0] != "||0|0|[]|true|true|true" {
		t.Fatalf("task defaults: %v", got)
	}

	// A change only to what the event leaves out emits nothing; a change to
	// what it shows emits once.
	exec(t, conn, `UPDATE loomplane_agent_tasks SET input='more private words', metadata='{"a":"b"}', enqueued_at=now() WHERE id='atask_1'`)
	exec(t, conn, `UPDATE loomplane_agent_tasks SET status='running', event_seq=1 WHERE id='atask_1'`)
	if got := strs(t, conn, `SELECT data->>'status' FROM loomplane_events WHERE type='agent_task.updated' ORDER BY sequence`); len(got) != 2 || got[1] != "running" {
		t.Fatalf("task update events: %v", got)
	}

	exec(t, conn, `INSERT INTO loomplane_agent_run_events (task_id,seq,kind,data) VALUES ('atask_1',1,'message','{"text":"hi"}')`)
	if got := strs(t, conn, `SELECT (created_at IS NOT NULL)::text FROM loomplane_agent_run_events`); got[0] != "true" {
		t.Fatalf("run event created_at: %v", got)
	}

	request := func(id, run, call, kind string) string {
		return `INSERT INTO loomplane_agent_requests (id,org_id,project_id,task_id,run_id,kind,status,question,tool_call_id,expires_at,created_at,updated_at)
 VALUES ('` + id + `','org','proj','atask_1','` + run + `','` + kind + `','pending','May I deploy?','` + call + `',now()+interval '1 day',now(),now())`
	}
	exec(t, conn, request("areq_1", "run_1", "call_1", "approve_plan"))
	if got := events("agent_request.%"); len(got) != 1 || got[0] != (event{"agent_request.updated", "areq_1", "proj"}) {
		t.Fatalf("request events: %v", got)
	}
	if got := strs(t, conn, `SELECT environment_id||'|'||context::text||'|'||options::text||'|'||target::text||'|'||suspension_id||'|'||checkpoint_id||'|'||asked_for||'|'||answered_by||'|'||answer||'|'||outcome||'|'||(answered_at IS NULL) FROM loomplane_agent_requests`); len(got) != 1 || got[0] != "|[]|[]|{}|||||||true" {
		t.Fatalf("request defaults: %v", got)
	}
	// The teams routing columns start empty; teams fills them later.
	if got := strs(t, conn, `SELECT '['||team_id||']['||team_name||']['||route_source||']['||team_reason||']['||route_note||']' FROM loomplane_agent_requests`); len(got) != 1 || got[0] != "[][][][][]" {
		t.Fatalf("route defaults: %v", got)
	}

	for _, sql := range []string{
		request("areq_again", "run_1", "call_1", "answer"), // same run and tool call
		request("areq_odd", "run_1", "call_2", "guess"),
		`INSERT INTO loomplane_agent_requests (id,org_id,task_id,run_id,kind,status,question,tool_call_id,expires_at,created_at,updated_at) VALUES ('areq_x','org','atask_1','run_1','answer','maybe','?','call_3',now(),now(),now())`,
		`INSERT INTO loomplane_agent_requests (id,org_id,task_id,run_id,kind,status,question,tool_call_id,expires_at,created_at,updated_at) VALUES ('areq_y','org','atask_none','run_1','answer','pending','?','call_4',now(),now(),now())`,
		task("atask_2", "run_1", "chat", "queued", "4000"), // same run
		task("atask_3", "run_3", "poetry", "queued", "4000"),
		task("atask_4", "run_4", "chat", "sleeping", "4000"),
		task("atask_5", "run_5", "chat", "queued", "0"),
		`INSERT INTO loomplane_agent_run_events (task_id,seq,kind,data) VALUES ('atask_1',1,'message','{}')`, // same seq
		`INSERT INTO loomplane_agent_run_events (task_id,seq,kind,data) VALUES ('atask_none',1,'message','{}')`,
	} {
		if _, err := conn.Exec(ctx, sql); err == nil {
			t.Fatalf("constraint accepted %s", sql)
		}
	}
	// Another tool call in the same run is a request of its own.
	exec(t, conn, request("areq_2", "run_1", "call_2", "answer"))

	// Deleting a task takes its run events and requests with it, and each
	// request's deletion is an event.
	exec(t, conn, `DELETE FROM loomplane_agent_tasks WHERE id='atask_1'`)
	if got := strs(t, conn, `SELECT (SELECT count(*) FROM loomplane_agent_run_events)+(SELECT count(*) FROM loomplane_agent_requests)||''`); got[0] != "0" {
		t.Fatalf("a deleted task left %s rows", got[0])
	}
	if got := strs(t, conn, `SELECT type||'|'||entity_id FROM loomplane_events WHERE type LIKE 'agent_%.deleted' ORDER BY type,entity_id`); len(got) != 3 || got[0] != "agent_request.deleted|areq_1" || got[1] != "agent_request.deleted|areq_2" || got[2] != "agent_task.deleted|atask_1" {
		t.Fatalf("deleted events: %v", got)
	}

	down(t, ex, "agent_foundation", "agent_foundation")
	if got := strs(t, conn, tables); got[0] != "gone|gone|gone" {
		t.Fatalf("down left tables: %v", got)
	}
	if got := strs(t, conn, `SELECT count(*)::text FROM pg_proc WHERE proname='loomplane_agent_task_event_trigger'`); got[0] != "0" {
		t.Fatalf("down left the task trigger function: %v", got)
	}
	// Up again after a down gives empty tables back.
	up(t, ex, "agent_foundation")
	if got := strs(t, conn, `SELECT (SELECT count(*) FROM loomplane_agent_tasks)+(SELECT count(*) FROM loomplane_agent_run_events)+(SELECT count(*) FROM loomplane_agent_requests)||''`); got[0] != "0" {
		t.Fatalf("rows after down and up: %v", got)
	}
}
