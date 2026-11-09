package ctl

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type reportCtl struct {
	mu     sync.Mutex
	reads  []string
	writes []string
}

func reportRows() string {
	at := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	return strings.ReplaceAll(`{"reports":[
{"id":"arep_1","project_id":"proj_api","environment":"production","status":"open","severity":"critical","title":"Deployment dep_9 failed","occurrences":3,"last_seen_at":"AT","issue":{"state":"open","number":12,"url":"https://github.com/acme/api/issues/12"}},
{"id":"arep_2","project_id":"proj_api","environment":"staging","status":"open","severity":"low","title":"Drift on staging","occurrences":1,"last_seen_at":"AT","issue":{"state":"waiting_for_permission"}}]}`, "AT", at)
}

const reportDetail = `{"id":"arep_1","project_id":"proj_api","environment":"production","signal":"deployment_failed","status":"open","severity":"critical",
"title":"Deployment dep_9 failed","repository":"acme/api","occurrences":3,
"facts":[{"label":"Error","value":"exit status 1\u001b[31m","untrusted":true}],
"investigation":"done",
"findings":{"summary":"The build ran out of memory.","likely_cause":"A new bundle step.","suggestions":["Raise the build memory","Split the bundle"],"confidence":"medium"},
"issue":{"state":"open","number":12,"url":"https://github.com/acme/api/issues/12"}}`

func (s *reportCtl) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Method != http.MethodGet {
			s.writes = append(s.writes, r.Method+" "+r.URL.Path+" "+string(b))
		} else {
			s.reads = append(s.reads, r.URL.RequestURI())
		}
		switch r.URL.Path {
		case "/api/v1/agent/reports":
			_, _ = w.Write([]byte(reportRows()))
		case "/api/v1/agent/reports/arep_1/close":
			_, _ = w.Write([]byte(`{"id":"arep_1","status":"closed"}`))
		case "/api/v1/agent/reports/arep_1":
			_, _ = w.Write([]byte(reportDetail))
		case "/api/v1/agent/reports/arep_x":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"report not found"}`))
		case "/api/v1/projects":
			_, _ = w.Write([]byte(`{"projects":[{"id":"proj_api","name":"Checkout API","slug":"checkout-api"}]}`))
		case "/api/v1/agent/watcher":
			_, _ = w.Write([]byte(`{"paused":true,"paused_reason":"the monthly budget is spent","paused_at":null,"ticked_at":null,
"issues":{"configured":true,"installations":[{"account":"acme","account_type":"Organization","accepted":false,"manage_url":"https://github.com/organizations/acme/settings/installations/1","checked_at":null}]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		}
	}
}

func TestAgentReportsTable(t *testing.T) {
	s := &reportCtl{}
	app, out := agentApp(t, s.handler(), "")
	if err := app.Run([]string{"agent", "reports", "--all", "--project", "checkout-api"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"ID", "SEVERITY", "ENVIRONMENT", "SEEN", "ISSUE", "arep_1", "critical", "checkout-api", "production", "3x, 5m", "#12 (open)", "waiting for permission"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Index(got, "arep_1") > strings.Index(got, "arep_2") {
		t.Errorf("rows out of order:\n%s", got)
	}
	found := false
	for _, r := range s.reads {
		if strings.HasPrefix(r, "/api/v1/agent/reports?") && strings.Contains(r, "status=all") && strings.Contains(r, "project_id=proj_api") {
			found = true
		}
	}
	if !found {
		t.Errorf("reads = %v", s.reads)
	}

	out.Reset()
	if err := app.Run([]string{"agent", "reports", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"id": "arep_2"`) {
		t.Errorf("json = %s", out.String())
	}
}

func TestAgentReportShowPrintsFindings(t *testing.T) {
	s := &reportCtl{}
	app, out := agentApp(t, s.handler(), "")
	if err := app.Run([]string{"agent", "reports", "show", "arep_1"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Deployment dep_9 failed", "checkout-api", "Error: exit status 1", "The build ran out of memory.", "medium confidence", "A new bundle step.", "- Raise the build memory", "https://github.com/acme/api/issues/12"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("an escape sequence reached the terminal:\n%q", got)
	}

	err := app.Run([]string{"agent", "reports", "show", "arep_x"})
	if err == nil || err.Error() != "There's no agent report with that ID." {
		t.Errorf("unknown id error = %v", err)
	}
	err = app.Run([]string{"agent", "reports", "show", "arep_y"})
	if err == nil || err.Error() != agentOff {
		t.Errorf("missing route error = %v", err)
	}
}

func TestAgentReportClosePostsTheReason(t *testing.T) {
	s := &reportCtl{}
	app, out := agentApp(t, s.handler(), "")
	if err := app.Run([]string{"agent", "reports", "close", "arep_1", "--reason", "fixed upstream"}); err != nil {
		t.Fatal(err)
	}
	if len(s.writes) != 1 || s.writes[0] != `POST /api/v1/agent/reports/arep_1/close {"reason":"fixed upstream"}` {
		t.Errorf("writes = %v", s.writes)
	}
	if !strings.Contains(out.String(), "Closed arep_1.") {
		t.Errorf("out = %q", out.String())
	}
}

func TestAgentWatcherSaysWhyIssuesWait(t *testing.T) {
	s := &reportCtl{}
	app, out := agentApp(t, s.handler(), "")
	if err := app.Run([]string{"agent", "watcher"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"The watcher is paused: the monthly budget is spent", "acme", "waiting for you to accept the Issues permission", "settings/installations/1"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
