package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type agentCall struct{ method, path, query, body string }

func agentServer(t *testing.T, answer string) (*Client, func() []agentCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []agentCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, agentCall{r.Method, r.URL.Path, r.URL.RawQuery, string(b)})
		mu.Unlock()
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "lp_test"), func() []agentCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]agentCall(nil), calls...)
	}
}

const agentRequestJSON = `{"id":"areq_1","kind":"answer","status":"pending","question":"Which region?","project_id":"proj_1",
"team_id":"team_1","team_name":"Payments","route_source":"named","team_reason":"They own settlement","route_note":"",
"target":{"key":"STRIPE_KEY"},"asked_for":"usr_1","created_at":"2026-11-02T10:00:00Z"}`

func TestClientAgentRequestRoutes(t *testing.T) {
	c, calls := agentServer(t, `{"requests":[`+agentRequestJSON+`]}`)
	ctx := context.Background()
	list, err := c.AgentRequests(ctx, false)
	if err != nil || len(list) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	r := list[0]
	if r.TeamName != "Payments" || r.RouteSource != "named" || r.TeamReason != "They own settlement" || r.TeamID != "team_1" {
		t.Fatalf("route fields were not read: %+v", r)
	}
	if _, err := c.AgentRequests(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AgentRequests(ctx, false, ForMe()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AgentRequests(ctx, true, ForMe()); err != nil {
		t.Fatal(err)
	}

	one, calls1 := agentServer(t, agentRequestJSON)
	if _, err := one.AgentRequest(ctx, "areq_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := one.AnswerAgentRequest(ctx, "areq_1", "eu-west-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := one.ProvideAgentSecret(ctx, "areq_1", "hunter2"); err != nil {
		t.Fatal(err)
	}
	if _, err := one.CompleteAgentRequest(ctx, "areq_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := one.DeclineAgentRequest(ctx, "areq_1", "not now"); err != nil {
		t.Fatal(err)
	}
	if _, err := one.DeclineAgentRequest(ctx, "areq_1", ""); err != nil {
		t.Fatal(err)
	}

	want := []agentCall{
		{"GET", "/api/v1/agent/requests", "status=pending", ""},
		{"GET", "/api/v1/agent/requests", "status=all", ""},
		{"GET", "/api/v1/agent/requests", "for=me&status=pending", ""},
		{"GET", "/api/v1/agent/requests", "for=me&status=all", ""},
	}
	if got := calls(); !equalCalls(got, want) {
		t.Fatalf("list calls %+v", got)
	}
	want1 := []agentCall{
		{"GET", "/api/v1/agent/requests/areq_1", "", ""},
		{"POST", "/api/v1/agent/requests/areq_1/answer", "", `{"text":"eu-west-1"}`},
		{"POST", "/api/v1/agent/requests/areq_1/secret", "", `{"value":"hunter2"}`},
		{"POST", "/api/v1/agent/requests/areq_1/complete", "", ""},
		{"POST", "/api/v1/agent/requests/areq_1/decline", "", `{"reason":"not now"}`},
		{"POST", "/api/v1/agent/requests/areq_1/decline", "", `{}`},
	}
	if got := calls1(); !equalCalls(got, want1) {
		t.Fatalf("calls %+v", got)
	}
}

func equalCalls(a, b []agentCall) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAgentRequestNeverHoldsASecret(t *testing.T) {
	// The answer to a secret names the key, never the value.
	c, _ := agentServer(t, `{"id":"areq_1","kind":"provide_secret","status":"answered","outcome":"provided","target":{"key":"STRIPE_KEY"}}`)
	r, err := c.ProvideAgentSecret(context.Background(), "areq_1", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.Answer, "hunter2") {
		t.Fatalf("answer holds the secret: %+v", r)
	}
}

func TestActorNameNamesTheAgent(t *testing.T) {
	for id, want := range map[string]string{
		"loomplane:org_1": "Loomplane automation",
		"agent:aorg_1":    "Loomplane agent",
		"agent:":          "agent:",
		"usr_1":           "usr_1",
	} {
		if got := ActorName(id); got != want {
			t.Errorf("ActorName(%q) = %q, want %q", id, got, want)
		}
	}
}
