package ctl

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// teamsServer answers the routes the teams commands use and records every
// write as "METHOD path body".
type teamsServer struct {
	mu     sync.Mutex
	writes []string
}

func (s *teamsServer) handle(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	if r.Method != http.MethodGet {
		s.mu.Lock()
		s.writes = append(s.writes, r.Method+" "+r.URL.Path+" "+string(b))
		s.mu.Unlock()
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/teams":
		_, _ = w.Write([]byte(`{"teams":[{"id":"team_1","name":"Payments","slug":"payments","members":[{"user_id":"ausr_grace","email":"grace@example.com","name":"Grace"}],"grants":[{"id":"g_ws","role":"admin","project_id":""},{"id":"g_api","role":"developer","project_id":"proj_api"}]}]}`))
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects":
		_, _ = w.Write([]byte(`{"projects":[{"id":"proj_api","name":"Checkout API","slug":"checkout-api"},{"id":"proj_app","name":"App","slug":"app"}]}`))
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/members":
		_, _ = w.Write([]byte(`{"members":[{"id":"amem_1","user_id":"ausr_grace","email":"grace@example.com","name":"Grace","role":"viewer"}]}`))
	case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/teams/team_1":
		w.WriteHeader(http.StatusNoContent)
	default:
		_, _ = w.Write([]byte(`{"id":"team_1","name":"Payments","slug":"payments","members":[],"grants":[]}`))
	}
}

func TestTeamsAddMapsEmailToUser(t *testing.T) {
	s := &teamsServer{}
	app, _ := recoveryCLI(t, s.handle)
	if err := app.Run([]string{"teams", "add", "payments", "Grace@Example.com"}); err != nil {
		t.Fatal(err)
	}
	if len(s.writes) != 1 || s.writes[0] != "PUT /api/v1/teams/team_1/members/ausr_grace " {
		t.Fatalf("writes %q", s.writes)
	}
	s.writes = nil
	err := app.Run([]string{"teams", "add", "payments", "nobody@example.com"})
	if err == nil || !strings.Contains(err.Error(), "nobody with that email is in this workspace") || len(s.writes) != 0 {
		t.Fatalf("unknown email: %v, writes %q", err, s.writes)
	}
}

func TestTeamsDeleteNeedsYes(t *testing.T) {
	s := &teamsServer{}
	app, _ := recoveryCLI(t, s.handle)
	if err := app.Run([]string{"teams", "delete", "payments"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("without --yes: %v", err)
	}
	if len(s.writes) != 0 {
		t.Fatalf("sent %q without --yes", s.writes)
	}
	if err := app.Run([]string{"teams", "delete", "payments", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if len(s.writes) != 1 || !strings.HasPrefix(s.writes[0], "DELETE /api/v1/teams/team_1") {
		t.Fatalf("writes %q", s.writes)
	}
}

func TestTeamsRevokeFindsTheGrant(t *testing.T) {
	s := &teamsServer{}
	app, _ := recoveryCLI(t, s.handle)
	if err := app.Run([]string{"teams", "revoke", "payments", "--project", "checkout-api"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"teams", "revoke", "payments"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"DELETE /api/v1/teams/team_1/grants/g_api ", "DELETE /api/v1/teams/team_1/grants/g_ws "}
	if strings.Join(s.writes, "|") != strings.Join(want, "|") {
		t.Fatalf("writes %q, want %q", s.writes, want)
	}
	s.writes = nil
	err := app.Run([]string{"teams", "revoke", "payments", "--project", "app"})
	if err == nil || !strings.Contains(err.Error(), "the team has no grant there") || len(s.writes) != 0 {
		t.Fatalf("no grant: %v, writes %q", err, s.writes)
	}
}

func TestTeamsGrantSendsTheScope(t *testing.T) {
	s := &teamsServer{}
	app, _ := recoveryCLI(t, s.handle)
	if err := app.Run([]string{"teams", "grant", "payments", "developer", "--project", "app"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"teams", "grant", "payments", "viewer"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"teams", "grant", "payments", "owner"}); err == nil {
		t.Fatal("owner must be refused")
	}
	want := `PUT /api/v1/teams/team_1/grants {"role":"developer","project_id":"proj_app"}|PUT /api/v1/teams/team_1/grants {"role":"viewer"}`
	if got := strings.Join(s.writes, "|"); got != want {
		t.Fatalf("writes %q", got)
	}
}

func TestProjectsTeamNone(t *testing.T) {
	s := &teamsServer{}
	app, _ := recoveryCLI(t, s.handle)
	if err := app.Run([]string{"projects", "team", "app", "--none"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"projects", "team", "app", "payments"}); err != nil {
		t.Fatal(err)
	}
	want := `PUT /api/v1/projects/proj_app/team {"team_id":null}|PUT /api/v1/projects/proj_app/team {"team_id":"team_1"}`
	if got := strings.Join(s.writes, "|"); got != want {
		t.Fatalf("writes %q", got)
	}
}

func TestTeamsListJSON(t *testing.T) {
	s := &teamsServer{}
	app, out := recoveryCLI(t, s.handle)
	if err := app.Run([]string{"teams", "--json"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); !strings.HasPrefix(got, "[") || !strings.Contains(got, `"slug": "payments"`) {
		t.Fatalf("output %s", got)
	}
	out.Reset()
	if err := app.Run([]string{"teams"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Payments", "payments", "developer on checkout-api", "admin on the workspace"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, out.String())
		}
	}
}
