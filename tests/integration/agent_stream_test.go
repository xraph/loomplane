//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/security"
)

// streamGap is the pause between the fake model's deltas. It is longer
// than the run log's 250ms token coalescing, so each checked segment
// lands in a token event of its own.
const streamGap = 300 * time.Millisecond

// watchAll follows the task from its first event until the watch closes
// (the task finished), for up to a minute.
func watchAll(t *testing.T, runs *agentruns.Service, p security.Principal, id string) []agentruns.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ch, stop, err := runs.Watch(ctx, p, id, 0)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer stop()
	var out []agentruns.Event
	for e := range ch {
		out = append(out, e)
	}
	if ctx.Err() != nil {
		t.Fatalf("the watch never closed; events so far: %v", kindsOf(out))
	}
	return out
}

func kindsOf(evs []agentruns.Event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

// tokenText is a token event's text.
func tokenText(t *testing.T, e agentruns.Event) string {
	t.Helper()
	var d struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	return d.Text
}

// An answer reaches a watcher as the model writes it: several token
// events, each a segment shield checked, before the step's done event.
func TestChatStreamsTokensBeforeTheStepEnds(t *testing.T) {
	fake := agenttest.Anthropic(t, agenttest.Turn{
		Deltas: []string{"Your projects: ", "api. ", "web. ", "That's all.\n"}, DeltaGap: streamGap,
	})
	a := startAgentOrg(t, fake.AIConfig())
	defer a.r.stop()
	a.budget("10")
	a.enable(nil)

	runs, task := a.startChat(t, "List my projects.")
	evs := watchAll(t, runs, a.ownerPrincipal(), task.ID)
	tokens, text, done := 0, "", false
	for _, e := range evs {
		switch e.Kind {
		case "token":
			if done {
				t.Fatalf("a token after done: %v", kindsOf(evs))
			}
			tokens++
			text += tokenText(t, e)
		case "done":
			done = true
		}
	}
	if tokens < 2 || !done {
		t.Fatalf("%d token events, done %v: %v", tokens, done, kindsOf(evs))
	}
	if text != "Your projects: api. web. That's all.\n" {
		t.Fatalf("text %q", text)
	}
	if got := waitFor(t, runs, a.ownerPrincipal(), task.ID, finished); got.Status != agentruns.StatusCompleted {
		t.Fatalf("task %+v", got)
	}
}

// A reply with a credential streams its first sentence, then the guard on
// the whole message stops it: the log ends with safety_block and the
// failed status, the step's text is withheld, and the tool call the reply
// made never runs.
func TestBlockedStreamEndsCleanly(t *testing.T) {
	fake := agenttest.Anthropic(t, agenttest.Turn{
		Deltas:   []string{"Setting it. ", "The key is AKIAIOSFODNN7EXAMPLE.\n"},
		DeltaGap: streamGap,
		ToolUses: []agenttest.ToolUse{{Name: "set_variable", Input: map[string]any{"key": "PORT", "value": "8080"}}},
	})
	a := startAgentOrg(t, fake.AIConfig())
	defer a.r.stop()
	a.budget("10")
	a.enable(nil)

	runs, task := a.startChat(t, "Set PORT to 8080.")
	owner := a.ownerPrincipal()
	evs := watchAll(t, runs, owner, task.ID)
	kinds := kindsOf(evs)
	n := len(kinds)
	if n < 2 || kinds[n-2] != "safety_block" || kinds[n-1] != agentruns.EventStatus {
		t.Fatalf("the log ends %v, want safety_block then the status", kinds)
	}
	var stop struct {
		Message string `json:"message"`
		StepSeq int64  `json:"step_seq"`
	}
	if err := json.Unmarshal(evs[n-2].Data, &stop); err != nil || stop.Message != agentruns.SafetyStopped || stop.StepSeq == 0 {
		t.Fatalf("safety_block %s", evs[n-2].Data)
	}
	got := waitFor(t, runs, owner, task.ID, finished)
	if got.Status != agentruns.StatusFailed || got.Error != agentruns.SafetyStopped {
		t.Fatalf("task %s: %q", got.Status, got.Error)
	}

	// The stored log keeps nothing of the stopped step's text.
	stored, err := runs.Events(context.Background(), owner, task.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range stored {
		switch {
		case e.Kind == "tool_call" || e.Kind == agentruns.EventToolResult:
			t.Fatalf("the blocked reply's tool call reached the log: %s %s", e.Kind, e.Data)
		case e.Kind == "token" && e.Seq > stop.StepSeq && string(e.Data) != `{"text": "", "withheld": true}` && string(e.Data) != `{"text":"","withheld":true}`:
			t.Fatalf("token %d kept its text: %s", e.Seq, e.Data)
		case strings.Contains(string(e.Data), "AKIA"):
			t.Fatalf("event %d %s keeps the key: %s", e.Seq, e.Kind, e.Data)
		}
	}

	// set_variable never ran: no audit entry, and the model was asked
	// once (a run tool would have sent its result back).
	db, err := pgx.Connect(context.Background(), a.cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(context.Background()) }()
	var calls int
	if err = db.QueryRow(context.Background(), `SELECT count(*) FROM chronicle_events
		WHERE action = 'agent.tool.call' AND tenant_id = $1`, a.orgID).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("%d tool calls audited", calls)
	}
	if reqs := fake.Requests(); len(reqs) != 1 {
		t.Fatalf("the model was asked %d times", len(reqs))
	}
}

// A reply with personal data isn't stopped: shield's checked segments
// publish it redacted, and the answer completes.
func TestRedactedAnswerCompletes(t *testing.T) {
	fake := agenttest.Anthropic(t, agenttest.Turn{
		Deltas: []string{"Ask the owner: ", "rex@example.com. ", "Thanks.\n"}, DeltaGap: streamGap,
	})
	a := startAgentOrg(t, fake.AIConfig())
	defer a.r.stop()
	a.budget("10")
	a.enable(nil)

	runs, task := a.startChat(t, "Who owns this?")
	owner := a.ownerPrincipal()
	evs := watchAll(t, runs, owner, task.ID)
	text := ""
	for _, e := range evs {
		if e.Kind == "token" {
			text += tokenText(t, e)
		}
		if e.Kind == "safety_block" {
			t.Fatalf("stopped: %v", kindsOf(evs))
		}
	}
	if strings.Contains(text, "rex@example.com") || !strings.Contains(text, "Ask the owner:") || !strings.HasSuffix(text, "Thanks.\n") {
		t.Fatalf("text %q", text)
	}
	if got := waitFor(t, runs, owner, task.ID, finished); got.Status != agentruns.StatusCompleted {
		t.Fatalf("task %s: %q", got.Status, got.Error)
	}
}
