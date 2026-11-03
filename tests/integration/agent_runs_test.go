//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	cortexengine "github.com/xraph/cortex/engine"
	cortexid "github.com/xraph/cortex/id"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/nexus"
	"github.com/xraph/nexus/tenant"

	"github.com/xraph/loomplane/internal/agentrequests"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/security"

	"github.com/xraph/cortex"
)

// ownerPrincipal is a's owner as the agent's services see them.
func (a *agentOrg) ownerPrincipal() security.Principal {
	return security.Principal{Kind: security.KindUser, UserID: a.userID(), OrgID: a.orgID}
}

// startChat starts the owner's chat through the running server's run
// driver; the server's Dispatch worker runs it.
func (a *agentOrg) startChat(t *testing.T, input string) (*agentruns.Service, *agentruns.Task) {
	t.Helper()
	runs := inject[*agentruns.Service](t, a.container())
	owner := a.ownerPrincipal()
	task, err := runs.Start(context.Background(), agentruns.StartInput{Kind: agentruns.KindChat, OrgID: a.orgID, OnBehalfOf: &owner, Input: input})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return runs, task
}

// waitFor polls the task until done says so, for up to a minute.
func waitFor(t *testing.T, runs *agentruns.Service, p security.Principal, id string, done func(*agentruns.Task) bool) *agentruns.Task {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		task, err := runs.Get(context.Background(), p, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if done(task) {
			return task
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s stayed %s: %s", id, task.Status, task.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func finished(task *agentruns.Task) bool { return agentruns.Terminal(task.Status) }

// A chat runs end to end on the server: Start queues it, the agent's queue
// runs it through nexus to the model, and the task completes with its
// events in the log.
func TestRunEndToEnd(t *testing.T) {
	fake := agenttest.Anthropic(t, agenttest.Turn{Text: "hello"})
	a := startAgentOrg(t, fake.AIConfig())
	defer a.r.stop()
	a.budget("10")
	a.enable(nil)

	runs, task := a.startChat(t, "Say hello.")
	owner := a.ownerPrincipal()
	done := waitFor(t, runs, owner, task.ID, finished)
	if done.Status != agentruns.StatusCompleted || done.Tokens == 0 || done.FinishedAt == nil {
		t.Fatalf("task %+v", done)
	}
	evs, err := runs.Events(context.Background(), owner, task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, 0, len(evs))
	text := ""
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
		if e.Kind == "token" {
			var d struct {
				Text string `json:"text"`
			}
			if err = json.Unmarshal(e.Data, &d); err != nil {
				t.Fatal(err)
			}
			text += d.Text
		}
	}
	for _, want := range []string{"run_started", "token", "done", agentruns.EventStatus} {
		if !slices.Contains(kinds, want) {
			t.Errorf("no %s event in %v", want, kinds)
		}
	}
	// The task reads finished only once its log is whole: done, then the
	// final status, last.
	if n := len(kinds); n < 2 || kinds[n-2] != "done" || kinds[n-1] != agentruns.EventStatus {
		t.Errorf("the log of a finished task ends %v, want done then its status", kinds)
	}
	if text != "hello" {
		t.Errorf("text %q", text)
	}
	reqs := fake.Requests()
	if len(reqs) != 1 || !strings.Contains(reqs[0].System, "infrastructure engineer") || !strings.Contains(reqs[0].System, a.orgID) {
		t.Fatalf("the model saw %+v", reqs)
	}
	// Someone else in the workspace can't read the owner's chat.
	viewer := joinAs(t, a, "grace@example.com", "viewer")
	var me struct {
		UserID string `json:"user_id"`
	}
	viewer.must(http.StatusOK, http.MethodGet, "/api/v1/session", nil, &me)
	if _, err = runs.Get(context.Background(), security.Principal{Kind: security.KindUser, UserID: me.UserID, OrgID: a.orgID}, task.ID); err == nil {
		t.Fatal("a viewer read the owner's chat")
	}
}

// A workspace on its own Anthropic key whose value Vault can't give back
// fails the run with the message an admin can act on, and the model is
// never called on the server's key instead.
func TestRunFailsClearlyWhenTheKeyCannotBeRead(t *testing.T) {
	fake := agenttest.Anthropic(t, agenttest.Turn{Text: "never sent"})
	a := startAgentOrg(t, fake.AIConfig())
	defer a.r.stop()
	a.owner.must(http.StatusOK, http.MethodPut, "/api/v1/ai/settings/provider-keys/claude", map[string]any{
		"type": "anthropic", "fields": map[string]string{"api_key": "sk-ant-workspace-0001"},
	}, nil)
	a.budget("10")
	a.enable(map[string]any{"provider_key": "claude"})

	// The key's value is still in Vault, but can't be decrypted.
	db, err := pgx.Connect(context.Background(), a.cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(context.Background()) }()
	tag, err := db.Exec(context.Background(), `UPDATE vault_secrets SET encrypted_value = '\x00'::bytea WHERE app_id = $1`, aisettings.VaultAppID)
	if err != nil || tag.RowsAffected() == 0 {
		t.Fatalf("corrupt the key: %v (%d rows)", err, tag.RowsAffected())
	}

	runs, task := a.startChat(t, "Say hello.")
	done := waitFor(t, runs, a.ownerPrincipal(), task.ID, finished)
	if done.Status != agentruns.StatusFailed || !strings.Contains(done.Error, "AI key couldn't be read") {
		t.Fatalf("task %+v", done)
	}
	if n := len(fake.Requests()); n != 0 {
		t.Fatalf("the model got %d requests", n)
	}
}

// waitForRequests polls the owner's requests until there are n.
func (a *agentOrg) waitForRequests(t *testing.T, n int) []agentrequests.Request {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		var list agentrequests.RequestList
		a.owner.must(http.StatusOK, http.MethodGet, "/api/v1/agent/requests", nil, &list)
		if len(list.Requests) >= n {
			out := make([]agentrequests.Request, 0, len(list.Requests))
			for _, r := range list.Requests {
				out = append(out, *r)
			}
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d requests, want %d", len(list.Requests), n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Turning the agent off cancels every open run and its pending requests,
// disables the workspace's nexus tenant, and reports the agent off; the
// model hears nothing more.
func TestDisableCancelsRunsAndRequests(t *testing.T) {
	fake := agenttest.Anthropic(t, agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "ask_person", Input: map[string]string{"question": "Which region?"}}}})
	a := startAgentOrg(t, fake.AIConfig())
	defer a.r.stop()
	a.budget("10")
	on := a.enable(nil)
	owner := a.ownerPrincipal()

	runs, waiting := a.startChat(t, "Deploy to the right region.")
	waitFor(t, runs, owner, waiting.ID, func(task *agentruns.Task) bool { return task.Status == agentruns.StatusWaiting || finished(task) })
	asked := a.waitForRequests(t, 1)[0]
	if asked.Kind != agentrequests.KindAnswer || asked.Status != agentrequests.StatusPending || asked.Question != "Which region?" ||
		asked.AskedFor != owner.UserID || asked.TaskID != waiting.ID {
		t.Fatalf("request %+v", asked)
	}

	// A running task, as one a worker is in the middle of looks.
	pg := pgdriver.New()
	if err := pg.Open(context.Background(), a.cfg.Database.URL); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pg.Close() }()
	store := agentruns.NewPgStore(pg)
	now := time.Now().UTC()
	running := &agentruns.Task{ID: "atask_running", OrgID: a.orgID, Kind: agentruns.KindChat, RunID: cortexid.NewAgentRunID().String(),
		Status: agentruns.StatusRunning, RequestedBy: owner.UserID, MaxTokens: 1000, CreatedAt: now, UpdatedAt: now}
	if err := store.Create(context.Background(), running, agentruns.Record{Principal: []byte(`{}`), Input: "x"}); err != nil {
		t.Fatal(err)
	}

	a.owner.must(http.StatusOK, http.MethodPatch, "/api/v1/ai/settings/agent", map[string]any{"enabled": false, "revision": a.agent().Revision}, nil)

	for _, id := range []string{waiting.ID, running.ID} {
		task, err := store.Get(context.Background(), a.orgID, id)
		if err != nil || task.Status != agentruns.StatusCancelled {
			t.Fatalf("%s: %+v, %v", id, task, err)
		}
	}
	req, err := agentrequests.NewPgStore(pg).Get(context.Background(), a.orgID, asked.ID)
	if err != nil || req.Status != agentrequests.StatusCancelled || req.Outcome != agentrequests.OutcomeCancelled {
		t.Fatalf("request after the agent went off: %+v, %v", req, err)
	}
	// Its routes are gone with the agent.
	a.owner.must(http.StatusConflict, http.MethodPost, "/api/v1/agent/requests/"+asked.ID+"/answer", map[string]string{"text": "eu-west-1"}, nil)
	var st struct {
		Enabled bool `json:"enabled"`
	}
	a.owner.must(http.StatusOK, http.MethodGet, "/api/v1/agent/status", nil, &st)
	if st.Enabled {
		t.Fatal("the status still says the agent is on")
	}
	gw := inject[*nexus.Gateway](t, a.container())
	tn, err := gw.Tenants().Get(context.Background(), on.NexusTenantID)
	if err != nil || tn.Status == tenant.StatusActive {
		t.Fatalf("the nexus tenant: %+v, %v", tn, err)
	}

	// The cancelled run takes no answer, and the model hears nothing more.
	eng := inject[*cortexengine.Engine](t, a.container())
	scoped := cortex.WithScope(context.Background(), agentscope.Scope{OrgID: a.orgID}.Cortex())
	runID, err := cortexid.ParseAgentRunID(waiting.RunID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := eng.GetRun(scoped, runID)
	if err != nil || r.State != "cancelled" {
		t.Fatalf("cortex run %+v, %v", r, err)
	}
	if err = eng.ProvideResults(scoped, runID, cortexengine.ResumeInput{ToolResults: []cortexengine.ToolResult{{ToolCallID: "x", Content: "eu-west-1"}}}); err == nil {
		t.Fatal("a cancelled run took an answer")
	}
	time.Sleep(500 * time.Millisecond)
	if n := len(fake.Requests()); n != 1 {
		t.Fatalf("the model got %d requests", n)
	}
}

// A question the agent asks in a chat becomes a request the owner answers
// in Loomplane; the run goes on with the answer, and a second answer is
// refused.
func TestAnAnsweredRequestResumesTheRun(t *testing.T) {
	fake := agenttest.Anthropic(t,
		agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "ask_person", Input: map[string]string{"question": "Which region?"}}}},
		agenttest.Turn{Text: "Deploying to eu-west-1."},
	)
	a := startAgentOrg(t, fake.AIConfig())
	defer a.r.stop()
	a.budget("10")
	a.enable(nil)
	owner := a.ownerPrincipal()

	runs, task := a.startChat(t, "Deploy to the right region.")
	asked := a.waitForRequests(t, 1)[0]
	var got agentrequests.Request
	a.owner.must(http.StatusOK, http.MethodPost, "/api/v1/agent/requests/"+asked.ID+"/answer", map[string]string{"text": "eu-west-1"}, &got)
	if got.Status != agentrequests.StatusAnswered || got.AnsweredBy != owner.UserID {
		t.Fatalf("answered %+v", got)
	}
	a.owner.must(http.StatusConflict, http.MethodPost, "/api/v1/agent/requests/"+asked.ID+"/answer", map[string]string{"text": "us-east-1"}, nil)

	done := waitFor(t, runs, owner, task.ID, finished)
	if done.Status != agentruns.StatusCompleted {
		t.Fatalf("task %+v", done)
	}
	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("the model got %d requests", len(reqs))
	}
	last := string(reqs[1].Messages[len(reqs[1].Messages)-1])
	if !strings.Contains(last, "eu-west-1") || strings.Contains(last, "us-east-1") {
		t.Fatalf("the model read %s", last)
	}
	evs, err := runs.Events(context.Background(), owner, task.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	logged := false
	for _, e := range evs {
		logged = logged || (e.Kind == agentruns.EventRequest && strings.Contains(string(e.Data), asked.ID))
	}
	if !logged {
		t.Fatal("the run's log doesn't name the request")
	}
	// The notice went to the owner, under agent:use.
	pg := pgdriver.New()
	if err = pg.Open(context.Background(), a.cfg.Database.URL); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pg.Close() }()
	var n int
	if err = pg.QueryRow(context.Background(), `SELECT count(*) FROM loomplane_notification_outbox WHERE kind = 'agent.request' AND subject_id = $1 AND user_id = $2 AND payload->>'need' = 'agent:use'`,
		asked.ID, owner.UserID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d notices, %v", n, err)
	}
}
