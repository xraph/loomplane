//go:build integration

package agentruns_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agenttest"
)

func pgStore(t *testing.T) (*agentruns.PgStore, *pgdriver.PgDB) {
	t.Helper()
	dsn := agenttest.FreshDatabase(t)
	pg := pgdriver.New()
	if err := pg.Open(context.Background(), dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return agentruns.NewPgStore(pg), pg
}

func newTask(org, id, runID string, at time.Time) *agentruns.Task {
	return &agentruns.Task{
		ID: id, OrgID: org, Kind: agentruns.KindChat, RunID: runID, SessionID: "sess_" + id, Status: agentruns.StatusQueued,
		RequestedBy: "usr_ada", MaxTokens: 1000, Metadata: map[string]string{"source": "chat"},
		ReleasePlanID: "plan_1", ReleaseRevision: "r1",
		CreatedAt: at, UpdatedAt: at,
	}
}

// The Postgres store keeps each workspace's tasks to itself, moves a task
// only from the statuses given, claims a continuation once per period,
// numbers events without gaps under concurrent writers, and finds what
// the sweep must queue again.
func TestPgStoreKeepsTasksAndEvents(t *testing.T) {
	ctx := context.Background()
	st, pg := pgStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	a := newTask("o", "atask_a", "arun_a", now.Add(-10*time.Minute))
	b := newTask("o2", "atask_b", "arun_b", now)
	for _, task := range []*agentruns.Task{a, b} {
		if err := st.Create(ctx, task, agentruns.Record{Principal: []byte(`{"kind":"agent"}`), Input: "hi " + task.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Get(ctx, "o", b.ID); !errors.Is(err, agentruns.ErrNotFound) {
		t.Fatalf("o reads o2's task: %v", err)
	}
	if _, err := st.ByRun(ctx, "o2", a.RunID); !errors.Is(err, agentruns.ErrNotFound) {
		t.Fatalf("o2 reads o's run: %v", err)
	}
	got, err := st.ByRun(ctx, "o", a.RunID)
	if err != nil || got.ID != a.ID || got.ReleasePlanID != "plan_1" || got.ReleaseRevision != "r1" ||
		got.Metadata["source"] != "chat" || len(got.Metadata) != 1 || got.SessionID != a.SessionID {
		t.Fatalf("by run %+v, %v", got, err)
	}
	rec, err := st.Record(ctx, "o", a.ID)
	if err != nil || rec.Input != "hi atask_a" || !strings.Contains(string(rec.Principal), "agent") {
		t.Fatalf("record %+v, %v", rec, err)
	}
	if s, serr := st.Session(ctx, "o", a.SessionID); serr != nil || s.ID != a.ID {
		t.Fatalf("session %+v, %v", s, serr)
	}

	// Transitions.
	moved, err := st.Transition(ctx, "o", a.ID, agentruns.Transition{From: []string{"running"}, Status: "waiting"})
	if err != nil || moved {
		t.Fatalf("moved from the wrong status: %v %v", moved, err)
	}
	if moved, err = st.Transition(ctx, "o2", a.ID, agentruns.Transition{From: []string{"queued"}, Status: "running"}); err != nil || moved {
		t.Fatalf("o2 moved o's task: %v %v", moved, err)
	}
	if moved, err = st.Transition(ctx, "o", a.ID, agentruns.Transition{From: []string{"queued"}, Status: "running",
		Event: &agentruns.NewEvent{Kind: agentruns.EventStatus, Data: json.RawMessage(`{"status":"running"}`)}}); err != nil || !moved {
		t.Fatalf("queued to running: %v %v", moved, err)
	}

	// Continuations are claimed once per period.
	if ok, cerr := st.ClaimContinue(ctx, "o", a.ID, now, 5*time.Minute); cerr != nil || !ok {
		t.Fatalf("first claim: %v %v", ok, cerr)
	}
	if ok, _ := st.ClaimContinue(ctx, "o", a.ID, now.Add(time.Minute), 5*time.Minute); ok {
		t.Fatal("claimed twice in a minute")
	}
	if ok, _ := st.ClaimContinue(ctx, "o", a.ID, now.Add(6*time.Minute), 5*time.Minute); !ok {
		t.Fatal("not claimed after six minutes")
	}

	// The sweep finds a's stale continuation and b, queued long ago.
	if _, err = pg.Exec(ctx, `UPDATE loomplane_agent_tasks SET enqueued_at = $1 WHERE id = $2`, now.Add(-time.Hour), b.ID); err != nil {
		t.Fatal(err)
	}
	stale, err := st.Stale(ctx, now.Add(-2*time.Minute), now.Add(7*time.Minute), 10)
	if err != nil || len(stale) != 2 {
		t.Fatalf("stale %+v, %v", stale, err)
	}
	for _, s := range stale {
		if (s.ID == b.ID) != s.Queued {
			t.Fatalf("stale %+v", s)
		}
	}

	// Concurrent writers never share a number.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if _, aerr := st.AppendEvents(ctx, "o", a.ID, []agentruns.NewEvent{
				{Kind: "token", Data: json.RawMessage(`{"text":"x"}`)}, {Kind: "step", Data: json.RawMessage(`{"index":` + string(rune('0'+i)) + `}`)},
			}); aerr != nil {
				t.Error(aerr)
			}
		})
	}
	wg.Wait()
	evs, err := st.Events(ctx, "o", a.ID, 0, 100)
	if err != nil || len(evs) != 17 {
		t.Fatalf("events %d, %v", len(evs), err)
	}
	for i, e := range evs {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
	}
	if evs, _ = st.Events(ctx, "o2", a.ID, 0, 100); len(evs) != 0 {
		t.Fatal("o2 reads o's events")
	}
	if got, _ = st.Get(ctx, "o", a.ID); got.EventSeq != 17 {
		t.Fatalf("event_seq %d", got.EventSeq)
	}

	// Finishing sets finished_at and the tokens; a finished task stays.
	tokens := 42
	if moved, err = st.Transition(ctx, "o", a.ID, agentruns.Transition{From: []string{"queued", "running", "waiting"}, Status: "failed", Error: "boom", Tokens: &tokens}); err != nil || !moved {
		t.Fatal(moved, err)
	}
	if moved, _ = st.Transition(ctx, "o", a.ID, agentruns.Transition{From: []string{"queued", "running", "waiting"}, Status: "completed"}); moved {
		t.Fatal("a failed task completed")
	}
	got, _ = st.Get(ctx, "o", a.ID)
	if got.Status != "failed" || got.Error != "boom" || got.Tokens != 42 || got.FinishedAt == nil {
		t.Fatalf("finished %+v", got)
	}
	if ok, _ := st.ClaimContinue(ctx, "o", a.ID, now.Add(time.Hour), 5*time.Minute); ok {
		t.Fatal("a finished task was claimed")
	}
	open, err := st.Open(ctx, "o")
	if err != nil || len(open) != 0 {
		t.Fatalf("open %+v, %v", open, err)
	}

	list, err := st.List(ctx, "o", agentruns.StoreFilter{Kind: agentruns.KindChat, RequestedBy: "usr_ada"})
	if err != nil || len(list) != 1 {
		t.Fatalf("list %+v, %v", list, err)
	}
}

// A task's realtime event carries its allowlist and wakes the workspace
// when its log grows.
func TestPgStoreEventsWakeTheWorkspace(t *testing.T) {
	ctx := context.Background()
	st, pg := pgStore(t)
	task := newTask("o", "atask_w", "arun_w", time.Now().UTC())
	if err := st.Create(ctx, task, agentruns.Record{Principal: []byte(`{}`), Input: "secret input"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendEvents(ctx, "o", task.ID, []agentruns.NewEvent{{Kind: "token", Data: json.RawMessage(`{"text":"x"}`)}}); err != nil {
		t.Fatal(err)
	}
	var n int
	var payload string
	err := pg.QueryRow(ctx, `SELECT count(*), max(data::text) FROM loomplane_events WHERE org_id = 'o' AND type = 'agent_task.updated'`).Scan(&n, &payload)
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 || strings.Contains(payload, "secret input") || strings.Contains(payload, "metadata") {
		t.Fatalf("%d events, last %s", n, payload)
	}
}

// A chat message's preface and page context are kept beside its input and
// read back with the record; a task without them stores NULL and "". The
// realtime event carries neither.
func TestPgStoreKeepsThePrefaceAndPageContext(t *testing.T) {
	ctx := context.Background()
	st, pg := pgStore(t)
	with := newTask("o", "atask_p", "arun_p", time.Now().UTC())
	if err := st.Create(ctx, with, agentruns.Record{Principal: []byte(`{}`), Input: "Why is this slow?",
		Preface: "where: secret page", PageContext: json.RawMessage(`{"path":"/projects/secret_page"}`)}); err != nil {
		t.Fatal(err)
	}
	rec, err := st.Record(ctx, "o", with.ID)
	if err != nil || rec.Input != "Why is this slow?" || rec.Preface != "where: secret page" ||
		!strings.Contains(string(rec.PageContext), `"/projects/secret_page"`) {
		t.Fatalf("record %+v, %v", rec, err)
	}
	without := newTask("o", "atask_q", "arun_q", time.Now().UTC())
	if err = st.Create(ctx, without, agentruns.Record{Principal: []byte(`{}`), Input: "hi"}); err != nil {
		t.Fatal(err)
	}
	if rec, err = st.Record(ctx, "o", without.ID); err != nil || rec.Preface != "" || rec.PageContext != nil {
		t.Fatalf("record %+v, %v", rec, err)
	}
	var stored *string
	if err = pg.QueryRow(ctx, `SELECT page_context::text FROM loomplane_agent_tasks WHERE id = 'atask_q'`).Scan(&stored); err != nil || stored != nil {
		t.Fatalf("page_context %v, %v", stored, err)
	}
	var payloads []string
	rows, err := pg.Query(ctx, `SELECT data::text FROM loomplane_events WHERE org_id = 'o' AND type LIKE 'agent_task.%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err = rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, p)
	}
	for _, p := range payloads {
		if strings.Contains(p, "secret page") || strings.Contains(p, "secret_page") {
			t.Fatalf("an event carries the context: %s", p)
		}
	}
}

// A safety stop withholds the text of the step it stopped, and only that
// step's, in the write that records the stop, and only in its workspace.
func TestPgStoreWithholdsTheStoppedStep(t *testing.T) {
	ctx := context.Background()
	st, _ := pgStore(t)
	task := newTask("o", "atask_s", "arun_s", time.Now().UTC())
	if err := st.Create(ctx, task, agentruns.Record{Principal: []byte(`{}`), Input: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendEvents(ctx, "o", task.ID, []agentruns.NewEvent{
		{Kind: "step", Data: json.RawMessage(`{"index":1}`)},
		{Kind: "token", Data: json.RawMessage(`{"text":"Hello"}`)},
		{Kind: "tool_call", Data: json.RawMessage(`{"tool":"list_projects","call_id":"c1"}`)},
		{Kind: "step", Data: json.RawMessage(`{"index":2}`)},
		{Kind: "token", Data: json.RawMessage(`{"text":"AKIA"}`)},
		{Kind: "token", Data: json.RawMessage(`{"text":"..."}`)},
	}); err != nil {
		t.Fatal(err)
	}
	stop := agentruns.NewEvent{Kind: "safety_block", Data: json.RawMessage(`{"message":"blocked"}`)}
	if _, err := st.WithholdStep(ctx, "o2", task.ID, stop); !errors.Is(err, agentruns.ErrNotFound) {
		t.Fatalf("o2 withheld o's text: %v", err)
	}
	seq, err := st.WithholdStep(ctx, "o", task.ID, stop)
	if err != nil || seq != 7 {
		t.Fatalf("withhold: %d, %v", seq, err)
	}
	evs, err := st.Events(ctx, "o", task.ID, 0, 100)
	if err != nil || len(evs) != 7 {
		t.Fatalf("events %+v, %v", evs, err)
	}
	want := []string{
		`{"index": 1}`, `{"text": "Hello"}`, `{"tool": "list_projects", "call_id": "c1"}`, `{"index": 2}`,
		`{"text": "", "withheld": true}`, `{"text": "", "withheld": true}`, `{"message": "blocked", "step_seq": 4}`,
	}
	for i, e := range evs {
		var got, w any
		if json.Unmarshal(e.Data, &got) != nil || json.Unmarshal([]byte(want[i]), &w) != nil {
			t.Fatalf("event %d %s", e.Seq, e.Data)
		}
		g, _ := json.Marshal(got)
		ww, _ := json.Marshal(w)
		if string(g) != string(ww) {
			t.Errorf("event %d: %s, want %s", e.Seq, g, ww)
		}
	}
	if evs[6].Kind != "safety_block" {
		t.Fatalf("last event %s", evs[6].Kind)
	}
	if got, _ := st.Get(ctx, "o", task.ID); got.EventSeq != 7 {
		t.Fatalf("event_seq %d", got.EventSeq)
	}
}
