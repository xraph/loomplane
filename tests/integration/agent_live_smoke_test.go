//go:build integration

package integration

import (
	"context"
	"math/big"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/agentrequests"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/config"
)

// The live smoke: one run on a real claude-sonnet-5-5 through a workspace
// key, with a budget of 2 USD. The agent lists the workspace's projects
// and asks Ada one question; she hears about it in her inbox, answers it,
// and the run completes with its spend under the workspace. It calls a
// real model and costs money, so it runs only with LOOMPLANE_LIVE_AI=1 and
// the workspace's Anthropic key in LOOMPLANE_LIVE_AI_KEY.
// LOOMPLANE_LIVE_AI_BASE_URL, when set, is the key's base URL.
// liveMaxTokens is the live runs' task budget. Cortex sends what's left of
// a task's budget as each call's max_tokens, and the chat's default of
// 200000 is above the 128000 output tokens claude-sonnet-5-5 accepts, so
// a default chat fails its first call. Until the chat caps a call's
// output at the model's limit, the live runs stay under it.
const liveMaxTokens = 120_000

func TestLiveSmoke(t *testing.T) {
	if os.Getenv("LOOMPLANE_LIVE_AI") != "1" {
		t.Skip("set LOOMPLANE_LIVE_AI=1 and LOOMPLANE_LIVE_AI_KEY to run the live smoke (it calls a real model)")
	}
	key := os.Getenv("LOOMPLANE_LIVE_AI_KEY")
	if key == "" {
		t.Fatal("LOOMPLANE_LIVE_AI_KEY is the workspace's Anthropic key for the live smoke")
	}
	ai := config.Defaults().AI
	ai.Enabled, ai.Provider, ai.Model, ai.APIKey, ai.BaseURL = true, config.AIProviderAnthropic, config.DefaultAIModel, "", ""
	a := startAgentOrg(t, ai)
	defer a.r.stop()

	keyFields := map[string]string{"api_key": key}
	if base := os.Getenv("LOOMPLANE_LIVE_AI_BASE_URL"); base != "" {
		keyFields["base_url"] = base
	}
	a.owner.must(http.StatusOK, http.MethodPut, "/api/v1/ai/settings/provider-keys/claude", map[string]any{
		"type": "anthropic", "fields": keyFields,
	}, nil)
	// The workspace and the agent both get 2 USD.
	var ws struct {
		Revision int64 `json:"revision"`
	}
	a.owner.must(http.StatusOK, http.MethodGet, "/api/v1/ai/settings", nil, &ws)
	a.owner.must(http.StatusOK, http.MethodPut, "/api/v1/ai/settings/budget", map[string]any{"monthly_budget_usd": "2", "revision": ws.Revision}, nil)
	a.owner.must(http.StatusOK, http.MethodPut, "/api/v1/ai/settings/agent/budget", map[string]any{"monthly_budget_usd": "2", "revision": a.agent().Revision}, nil)
	if on := a.enable(map[string]any{"provider_key": "claude", "model": config.DefaultAIModel}); !on.Enabled || on.Budget.LimitUSD != "2.00" {
		t.Fatalf("agent %+v", on)
	}
	a.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono"}, nil)

	db, err := pgx.Connect(context.Background(), a.cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(context.Background()) }()

	ada := a.ownerPrincipal()
	runs := inject[*agentruns.Service](t, a.container())
	task, err := runs.Start(context.Background(), agentruns.StartInput{
		Kind: agentruns.KindChat, OrgID: a.orgID, OnBehalfOf: &ada, MaxTokens: liveMaxTokens,
		Input: "List the projects in this workspace. Then use ask_person to ask me which project you should look at first, " +
			"and once I answer, tell me in one sentence which one I picked. Don't change anything.",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The agent asks, and the run waits on Ada.
	var asked agentrequests.Request
	deadline := time.Now().Add(3 * time.Minute)
	for asked.ID == "" {
		var list agentrequests.RequestList
		a.owner.must(http.StatusOK, http.MethodGet, "/api/v1/agent/requests", nil, &list)
		for _, r := range list.Requests {
			if r.TaskID == task.ID && r.Kind == agentrequests.KindAnswer && r.Status == agentrequests.StatusPending {
				asked = *r
			}
		}
		if asked.ID != "" {
			break
		}
		if tk, gerr := runs.Get(context.Background(), ada, task.ID); gerr == nil && finished(tk) {
			t.Fatalf("the run finished without asking: %+v", tk)
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pending question for %s: %+v", task.ID, list.Requests)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("the agent asked: %q", asked.Question)
	waitForLong(t, runs, ada, task.ID, time.Minute, func(tk *agentruns.Task) bool {
		return tk.Status == agentruns.StatusWaiting || finished(tk)
	})

	// Ada hears about it in her inbox.
	var reached []string
	deadline = time.Now().Add(time.Minute)
	for len(reached) == 0 {
		rows, qerr := db.Query(context.Background(), `SELECT i.user_id FROM herald_inbox i JOIN loomplane_notification_outbox o ON o.id = i.metadata->>'outbox_id'
			WHERE o.kind = 'agent.request' AND o.subject_id = $1`, asked.ID)
		if qerr != nil {
			t.Fatal(qerr)
		}
		if reached, qerr = pgx.CollectRows(rows, pgx.RowTo[string]); qerr != nil {
			t.Fatal(qerr)
		}
		if len(reached) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(reached) != 1 || reached[0] != ada.UserID {
		t.Fatalf("the question reached %v, want Ada (%s)", reached, ada.UserID)
	}

	// Ada answers, and the run completes.
	var answered agentrequests.Request
	a.owner.must(http.StatusOK, http.MethodPost, "/api/v1/agent/requests/"+asked.ID+"/answer", map[string]string{"text": "Mono, please."}, &answered)
	if answered.Status != agentrequests.StatusAnswered {
		t.Fatalf("answered %+v", answered)
	}
	done := waitForLong(t, runs, ada, task.ID, 3*time.Minute, finished)
	if done.Status != agentruns.StatusCompleted {
		t.Fatalf("task %+v", done)
	}

	// The calls it made: it listed the projects, asked, and changed nothing.
	rows, err := db.Query(context.Background(), `SELECT metadata->>'tool'||' '||coalesce(metadata->>'risk','')||' '||coalesce(metadata->>'outcome','')
		FROM chronicle_events WHERE action = 'agent.tool.call' AND tenant_id = $1 AND metadata->>'run_id' = $2 ORDER BY sequence`, a.orgID, done.RunID)
	if err != nil {
		t.Fatal(err)
	}
	calls, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "; ")
	if !strings.Contains(joined, "list_projects") || !strings.Contains(joined, "ask_person") {
		t.Errorf("calls %v, want list_projects and ask_person", calls)
	}

	// The run's spend shows under the workspace's agent budget.
	settings := inject[*agentsettings.Service](t, a.container())
	var budget agentsettings.BudgetState
	deadline = time.Now().Add(30 * time.Second)
	for {
		if budget, err = settings.Budget(context.Background(), a.orgID); err != nil {
			t.Fatal(err)
		}
		if spent, ok := new(big.Rat).SetString(budget.SpentUSD); ok && spent.Sign() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no spend under the workspace: %+v", budget)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if budget.LimitUSD != "2.00" || budget.Exhausted {
		t.Errorf("budget %+v", budget)
	}
	t.Logf("task %s: %s, %d tokens, spent %s of %s USD, calls %v", done.ID, done.Status, done.Tokens, budget.SpentUSD, budget.LimitUSD, calls)
}
