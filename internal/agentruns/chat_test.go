package agentruns

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/xraph/cortex/id"
	"github.com/xraph/cortex/session"
)

// A chat continues only a conversation that exists. One that names no
// conversation gets a fresh one of its own (preflight 1.1); one that
// names something that isn't one is refused before anything is written.
func TestChatNeedsAConversation(t *testing.T) {
	w := newWorld(t)
	for _, sid := range []string{"nope", id.NewSessionID().String()} {
		_, err := w.svc.Start(context.Background(), StartInput{
			Kind: KindChat, OrgID: orgA, OnBehalfOf: personPtr(orgA, ada), Input: "What's deployed?", SessionID: sid,
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("SessionID %q: %v, want ErrInvalid", sid, err)
		}
	}
	if n := w.store.count(); n != 0 {
		t.Fatalf("%d tasks written", n)
	}
	if jobs := w.pending(t); len(jobs) != 0 {
		t.Fatalf("%d jobs queued", len(jobs))
	}

	task := w.adaChat(t)
	if _, err := id.ParseSessionID(task.SessionID); err != nil {
		t.Fatalf("a new chat's session %q: %v", task.SessionID, err)
	}
}

func TestStartJobCreatesTheConversationSession(t *testing.T) {
	w := newWorld(t)
	task := w.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), Metadata: map[string]string{MetaTitle: "What's deployed?"}})
	if err := w.runStart(t, task); err != nil {
		t.Fatal(err)
	}
	made := w.engine.sessions.made()
	if len(made) != 1 {
		t.Fatalf("%d sessions made", len(made))
	}
	got := made[0]
	agent := w.engine.agents[agentKey(runCtx(task), ChatSpec().Agent)]
	if agent == nil {
		t.Fatal("no chat agent at the task's scope")
	}
	if got.ID.String() != task.SessionID || got.AgentID != agent.ID || got.IsDefault || got.Title != "What's deployed?" ||
		len(got.Metadata) != 2 || got.Metadata["org_id"] != orgA || got.Metadata["started_by"] != ada {
		t.Fatalf("session %+v", got)
	}
	if got.Scope.Canonical() != agent.Scope.Canonical() {
		t.Fatalf("session at %s, agent at %s", got.Scope.Canonical(), agent.Scope.Canonical())
	}
	if order := w.engine.calledInOrder(); !slices.Equal(order, []string{"create_session", "stream"}) {
		t.Fatalf("calls %v", order)
	}
	streams, _, _, _, _ := w.engine.calls()
	if len(streams) != 1 || streams[0].overrides.SessionID.String() != task.SessionID {
		t.Fatalf("streams %+v", streams)
	}
}

func TestSecondTurnReusesTheSession(t *testing.T) {
	w := newWorld(t)
	first := w.adaChat(t)
	if err := w.runStart(t, first); err != nil {
		t.Fatal(err)
	}
	second := w.start(t, StartInput{OnBehalfOf: personPtr(orgA, ada), SessionID: first.SessionID})
	if err := w.runStart(t, second); err != nil {
		t.Fatal(err)
	}
	if made := w.engine.sessions.made(); len(made) != 1 {
		t.Fatalf("%d sessions made", len(made))
	}
	if order := w.engine.calledInOrder(); !slices.Equal(order, []string{"create_session", "stream", "stream"}) {
		t.Fatalf("calls %v", order)
	}
	streams, _, _, _, _ := w.engine.calls()
	if len(streams) != 2 || streams[1].overrides.SessionID.String() != first.SessionID {
		t.Fatalf("streams %+v", streams)
	}
}

func TestSessionOfAnotherAgentFailsTheTask(t *testing.T) {
	w := newWorld(t)
	task := w.adaChat(t)
	sid, err := id.ParseSessionID(task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	w.engine.sessions.put(runCtx(task), &session.Session{ID: sid, AgentID: id.NewAgentID()})
	if err = w.runStart(t, task); err != nil {
		t.Fatal(err)
	}
	got := w.store.task(t, task.ID)
	if got.Status != StatusFailed || got.Error != "this conversation belongs to another agent" {
		t.Fatalf("task %+v", got)
	}
	if streams, _, _, _, _ := w.engine.calls(); len(streams) != 0 {
		t.Fatalf("streamed %d runs", len(streams))
	}
}

// A conversation whose session a 3a server never made gets it on its
// next continuation, so the run's memory is saved from then on.
func TestContinuationMakesAMissingSession(t *testing.T) {
	w := newWorld(t)
	task := w.adaChat(t)
	if err := w.runStart(t, task); err != nil {
		t.Fatal(err)
	}
	w.engine.sessions.mu.Lock()
	clear(w.engine.sessions.byID)
	w.engine.sessions.mu.Unlock()
	mustMove(t, w, task, StatusWaiting)
	if err := w.runContinue(t, task); err != nil {
		t.Fatal(err)
	}
	made := w.engine.sessions.made()
	if len(made) != 2 || made[1].ID.String() != task.SessionID {
		t.Fatalf("sessions %+v", made)
	}
}
