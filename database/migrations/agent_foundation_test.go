//go:build integration

package migrations

import (
	"encoding/json"
	"slices"
	"strings"
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
	want := []string{"created_at", "environment_id", "error", "event_seq", "finished_at", "id", "kind", "max_tokens", "org_id", "project_id", "requested_by", "run_id", "status", "tokens", "updated_at"}
	keys := keysOf(t, raw[0])
	if !slices.Equal(keys, want) {
		t.Fatalf("task event keys %v, want %v", keys, want)
	}
	for _, private := range []string{"input", "principal", "metadata", "session_id"} {
		if slices.Contains(keys, private) {
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

	exec(t, conn, `INSERT INTO loomplane_agent_run_events (task_id,org_id,seq,kind,data) VALUES ('atask_1','org',1,'message','{"text":"hi"}')`)
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
		`INSERT INTO loomplane_agent_run_events (task_id,org_id,seq,kind,data) VALUES ('atask_1','org',1,'message','{}')`, // same seq
		`INSERT INTO loomplane_agent_run_events (task_id,org_id,seq,kind,data) VALUES ('atask_none','org',1,'message','{}')`,
		`INSERT INTO loomplane_agent_run_events (task_id,seq,kind,data) VALUES ('atask_1',2,'message','{}')`, // no workspace
	} {
		if _, err := conn.Exec(ctx, sql); err == nil {
			t.Fatalf("constraint accepted %s", sql)
		}
	}
	// Another tool call in the same run is a request of its own.
	exec(t, conn, request("areq_2", "run_1", "call_2", "answer"))

	// A request event says who it is for and where it stands, and never
	// what was asked or answered. Saving only an answer emits nothing.
	exec(t, conn, `INSERT INTO loomplane_agent_requests (id,org_id,project_id,task_id,run_id,kind,status,question,context,options,target,suspension_id,checkpoint_id,tool_call_id,asked_for,team_id,team_name,route_source,team_reason,route_note,expires_at,created_at,updated_at)
 VALUES ('areq_3','org','proj','atask_1','run_1','provide_secret','pending','QUESTION-MARKER','[{"label":"CONTEXT-MARKER","path":"/"}]','["OPTION-MARKER"]','{"key":"TARGET-MARKER"}','SUSPENSION-MARKER','CHECKPOINT-MARKER','TOOLCALL-MARKER','usr_ada','team_1','Payments','project','REASON-MARKER','NOTE-MARKER',now()+interval '1 day',now(),now())`,
		`UPDATE loomplane_agent_requests SET answer='ANSWER-MARKER', answered_by='ANSWERER-MARKER' WHERE id='areq_3'`,
		`UPDATE loomplane_agent_requests SET status='answered', outcome='provided', answered_at=now() WHERE id='areq_3'`)
	if got := strs(t, conn, `SELECT data->>'status' FROM loomplane_events WHERE type='agent_request.updated' AND entity_id='areq_3' ORDER BY sequence`); len(got) != 2 || got[0] != "pending" || got[1] != "answered" {
		t.Fatalf("areq_3 events: %v", got)
	}
	requestKeys := []string{"asked_for", "created_at", "id", "kind", "org_id", "project_id", "route_source", "status", "team_id", "team_name", "updated_at"}
	for _, payload := range strs(t, conn, `SELECT data::text FROM loomplane_events WHERE type LIKE 'agent_request.%' ORDER BY sequence`) {
		if keys := keysOf(t, payload); !slices.Equal(keys, requestKeys) {
			t.Fatalf("request event keys %v, want %v", keys, requestKeys)
		}
		if strings.Contains(payload, "MARKER") {
			t.Fatalf("request event carries private text: %s", payload)
		}
	}
	if got := strs(t, conn, `SELECT data->>'asked_for'||'|'||(data->>'team_id')||'|'||(data->>'team_name')||'|'||(data->>'route_source') FROM loomplane_events WHERE type='agent_request.updated' AND entity_id='areq_3' ORDER BY sequence LIMIT 1`); got[0] != "usr_ada|team_1|Payments|project" {
		t.Fatalf("areq_3 routing: %v", got)
	}

	// Deleting a task takes its run events and requests with it, and each
	// request's deletion is an event.
	exec(t, conn, `DELETE FROM loomplane_agent_tasks WHERE id='atask_1'`)
	if got := strs(t, conn, `SELECT (SELECT count(*) FROM loomplane_agent_run_events)+(SELECT count(*) FROM loomplane_agent_requests)||''`); got[0] != "0" {
		t.Fatalf("a deleted task left %s rows", got[0])
	}
	if got := strs(t, conn, `SELECT type||'|'||entity_id FROM loomplane_events WHERE type LIKE 'agent_%.deleted' ORDER BY type,entity_id`); len(got) != 4 || got[0] != "agent_request.deleted|areq_1" || got[1] != "agent_request.deleted|areq_2" || got[2] != "agent_request.deleted|areq_3" || got[3] != "agent_task.deleted|atask_1" {
		t.Fatalf("deleted events: %v", got)
	}

	down(t, ex, "agent_foundation", "agent_foundation")
	if got := strs(t, conn, tables); got[0] != "gone|gone|gone" {
		t.Fatalf("down left tables: %v", got)
	}
	if got := strs(t, conn, `SELECT count(*)::text FROM pg_proc WHERE proname='loomplane_agent_event_trigger'`); got[0] != "0" {
		t.Fatalf("down left the agent trigger function: %v", got)
	}
	// Up again after a down gives empty tables back.
	up(t, ex, "agent_foundation")
	if got := strs(t, conn, `SELECT (SELECT count(*) FROM loomplane_agent_tasks)+(SELECT count(*) FROM loomplane_agent_run_events)+(SELECT count(*) FROM loomplane_agent_requests)||''`); got[0] != "0" {
		t.Fatalf("rows after down and up: %v", got)
	}
}

// keysOf is the sorted top-level keys of a JSON object.
func keysOf(t *testing.T, raw string) []string {
	t.Helper()
	var data map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
