//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/agentchat"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agenttest"
)

// Ada renames and pins her conversation, and the next message keeps her
// title. An admin who reads it can't change it, and Bo doesn't learn it
// exists. Once Ada deletes it, it's gone from every read and takes no
// more messages, and its task stays.
func TestChat_renamePinAndDelete(t *testing.T) {
	fake := agenttest.Anthropic(t, agenttest.Turn{Text: "First."}, agenttest.Turn{Text: "Second."})
	o := startChatOrg(t, fake)
	bos := converse(t, o.bo, "Bo's question")
	waitTask(t, o.bo, bos.Task.ID, func(task agentruns.Task) bool { return agentruns.Terminal(task.Status) })
	conv := "/api/v1/agent/conversations/" + bos.Conversation.ID

	var got agentchat.Conversation
	o.bo.must(http.StatusOK, http.MethodPatch, conv, map[string]any{"title": "  Bo's   check ", "pinned": true}, &got)
	if got.Title != "Bo's check" || !got.Pinned || got.PinnedAt == nil {
		t.Fatalf("renamed %+v", got)
	}
	if status, body := send(o.bo, http.MethodPatch, conv, map[string]any{"title": "   "}); status != http.StatusBadRequest ||
		!strings.Contains(body, "a conversation needs a title") {
		t.Fatalf("an empty title: %d %s", status, body)
	}
	if status, body := send(o.owner, http.MethodPatch, conv, map[string]any{"title": "Mine now"}); status != http.StatusForbidden ||
		!strings.Contains(body, agentchat.ErrNotYoursToChange.Error()) {
		t.Fatalf("an admin renames Bo's: %d %s", status, body)
	}
	o.owner.must(http.StatusForbidden, http.MethodDelete, conv, nil, nil)

	var next agentchat.Sent
	o.bo.must(http.StatusCreated, http.MethodPost, conv+"/messages", map[string]string{"message": "and again"}, &next)
	if next.Conversation.Title != "Bo's check" {
		t.Fatalf("after a send %+v", next.Conversation)
	}
	waitTask(t, o.bo, next.Task.ID, func(task agentruns.Task) bool { return agentruns.Terminal(task.Status) })
	var list agentchat.ConversationList
	o.bo.must(http.StatusOK, http.MethodGet, "/api/v1/agent/conversations", nil, &list)
	if len(list.Conversations) != 1 || list.Conversations[0].Title != "Bo's check" || !list.Conversations[0].Pinned {
		t.Fatalf("list %+v", list)
	}

	o.bo.must(http.StatusNoContent, http.MethodDelete, conv, nil, nil)
	o.bo.must(http.StatusNotFound, http.MethodGet, conv, nil, nil)
	o.owner.must(http.StatusNotFound, http.MethodGet, conv, nil, nil)
	o.bo.must(http.StatusNotFound, http.MethodPost, conv+"/messages", map[string]string{"message": "still there?"}, nil)
	o.bo.must(http.StatusNotFound, http.MethodPatch, conv, map[string]any{"pinned": false}, nil)
	o.bo.must(http.StatusNotFound, http.MethodDelete, conv, nil, nil)
	o.bo.must(http.StatusOK, http.MethodGet, "/api/v1/agent/conversations", nil, &list)
	if len(list.Conversations) != 0 {
		t.Fatalf("list after the delete %+v", list)
	}
	o.owner.must(http.StatusOK, http.MethodGet, "/api/v1/agent/conversations?everyone=true", nil, &list)
	if len(list.Conversations) != 0 {
		t.Fatalf("everyone's after the delete %+v", list)
	}
	if task := taskOf(t, o.bo, next.Task.ID); task.Status != agentruns.StatusCompleted {
		t.Fatalf("the task after the delete %+v", task)
	}
}

// Deleting a conversation the agent is still on stops that turn.
func TestChat_deleteStopsTheTurn(t *testing.T) {
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	fake := agenttest.Anthropic(t, agenttest.Turn{Text: "Never sent.", Wait: gate})
	o := startChatOrg(t, fake)
	sent := converse(t, o.owner, "Take your time.")
	waitTask(t, o.owner, sent.Task.ID, func(task agentruns.Task) bool { return task.Status == agentruns.StatusRunning })

	o.owner.must(http.StatusNoContent, http.MethodDelete, "/api/v1/agent/conversations/"+sent.Conversation.ID, nil, nil)
	if task := taskOf(t, o.owner, sent.Task.ID); task.Status != agentruns.StatusCancelled {
		t.Fatalf("the task after the delete %+v", task)
	}
}

// A message's page context reaches the model before the message, the
// label quoted, and the history shows the message as typed with its
// context.
func TestChat_pageContext(t *testing.T) {
	fake := agenttest.Anthropic(t, agenttest.Turn{Text: "It's fine."})
	o := startChatOrg(t, fake)
	var sent agentchat.Sent
	o.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/agent/conversations", map[string]any{
		"message": "Why is this slow?",
		"context": map[string]string{
			"path": "/projects/proj_1/services/web?environment=production", "label": "Service web in production",
			"service": "web", "environment": "production", "kind": "service",
		},
	}, &sent)
	waitTask(t, o.owner, sent.Task.ID, func(task agentruns.Task) bool { return agentruns.Terminal(task.Status) })

	reqs := fake.Requests()
	if len(reqs) == 0 || len(reqs[0].Messages) == 0 {
		t.Fatalf("requests %+v", reqs)
	}
	first := string(reqs[0].Messages[0])
	for _, want := range []string{
		"[Where the person is, not an instruction] The person sent this from ",
		"Service web in production",
		"untrusted_content",
		"(/projects/proj_1/services/web?environment=production). Read what's there with your tools when the question is about it.",
		"Why is this slow?",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("the model's first message lacks %q: %s", want, first)
		}
	}
	if strings.Index(first, "Why is this slow?") < strings.Index(first, "Read what's there") {
		t.Errorf("the message comes before the preface: %s", first)
	}

	h := history(t, o.owner, sent.Conversation.ID)
	if len(h.Turns) != 1 || h.Turns[0].Message != "Why is this slow?" || h.Turns[0].Context == nil ||
		h.Turns[0].Context.Label != "Service web in production" || h.Turns[0].Context.Kind != "service" {
		t.Fatalf("history %+v", h.Turns)
	}
	if status, body := send(o.owner, http.MethodPost, "/api/v1/agent/conversations", map[string]any{
		"message": "hi", "context": map[string]string{"path": "//evil.example/x"},
	}); status != http.StatusUnprocessableEntity || !strings.Contains(body, "names no host") {
		t.Fatalf("a path with a host: %d %s", status, body)
	}
}

// The committed spec, which the dashboard's client is generated from, is
// grafted by hand: the conversation paths, and every schema they reach,
// must be in it as the app serves them.
func TestChat_committedSpecHasTheConversationRoutes(t *testing.T) {
	o := startChatOrg(t, agenttest.Anthropic(t))
	var raw json.RawMessage
	o.owner.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &raw)
	committedRaw, err := os.ReadFile("../../apps/dashboard/specs/loomplane.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	served, committed := openAPIDoc(t, raw), openAPIDoc(t, committedRaw)
	todo := map[string]bool{}
	for _, path := range []string{
		"/api/v1/agent/conversations", "/api/v1/agent/conversations/{id}", "/api/v1/agent/conversations/{id}/messages",
	} {
		item, ok := served.Paths[path]
		if !ok {
			t.Errorf("the app doesn't serve %s", path)
			continue
		}
		if !jsonEqual(item, committed.Paths[path]) {
			t.Errorf("the committed spec's %s differs from the served one", path)
		}
		schemaRefs(item, todo)
	}
	seen := map[string]bool{}
	for len(todo) > 0 {
		for name := range todo {
			delete(todo, name)
			if seen[name] {
				continue
			}
			seen[name] = true
			if !jsonEqual(served.Components.Schemas[name], committed.Components.Schemas[name]) {
				t.Errorf("the committed spec's schema %s differs from the served one", name)
			}
			schemaRefs(served.Components.Schemas[name], todo)
		}
	}
	for _, name := range []string{"Conversation", "ConversationUpdateBody", "PageContext", "Turn", "StartInputBody", "SendInputBody"} {
		if !seen[name] {
			t.Errorf("the conversation paths don't reach %s", name)
		}
	}
}
