package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientTeamRoutes(t *testing.T) {
	type call struct{ method, path, body string }
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.Method, r.URL.Path, string(b)})
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/teams/team_1":
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/v1/teams" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"teams":[{"id":"team_1","name":"Payments","slug":"payments"}]}`))
		case r.URL.Path == "/api/v1/members":
			_, _ = w.Write([]byte(`{"members":[{"id":"amem_1","user_id":"ausr_grace","email":"g@x.io","name":"Grace","role":"viewer"}]}`))
		case r.URL.Path == "/api/v1/projects/p/permissions":
			_, _ = w.Write([]byte(`{"permissions":["project:update"]}`))
		case r.URL.Path == "/api/v1/projects/p/team":
			_, _ = w.Write([]byte(`{"id":"p","responsible_team_id":"team_1"}`))
		default:
			_, _ = w.Write([]byte(`{"id":"team_1","name":"Payments","slug":"payments"}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "lp_live_k")
	ctx := context.Background()
	name, desc := "Pay", "d"
	team := "team_1"

	steps := []struct {
		run              func() error
		method, path, in string
	}{
		{func() error { _, err := c.Teams(ctx); return err }, "GET", "/api/v1/teams", ""},
		{func() error { _, err := c.Team(ctx, "team_1"); return err }, "GET", "/api/v1/teams/team_1", ""},
		{func() error { _, err := c.CreateTeam(ctx, "Payments", "d"); return err }, "POST", "/api/v1/teams", `{"name":"Payments","description":"d"}`},
		{func() error { _, err := c.UpdateTeam(ctx, "team_1", &name, &desc); return err }, "PATCH", "/api/v1/teams/team_1", `{"name":"Pay","description":"d"}`},
		{func() error { return c.DeleteTeam(ctx, "team_1") }, "DELETE", "/api/v1/teams/team_1", ""},
		{func() error { _, err := c.AddTeamMember(ctx, "team_1", "ausr_grace"); return err }, "PUT", "/api/v1/teams/team_1/members/ausr_grace", ""},
		{func() error { _, err := c.RemoveTeamMember(ctx, "team_1", "ausr_grace"); return err }, "DELETE", "/api/v1/teams/team_1/members/ausr_grace", ""},
		{func() error { _, err := c.SetTeamGrant(ctx, "team_1", "developer", "proj_1"); return err }, "PUT", "/api/v1/teams/team_1/grants", `{"role":"developer","project_id":"proj_1"}`},
		{func() error { _, err := c.SetTeamGrant(ctx, "team_1", "viewer", ""); return err }, "PUT", "/api/v1/teams/team_1/grants", `{"role":"viewer"}`},
		{func() error { _, err := c.RemoveTeamGrant(ctx, "team_1", "grant_1"); return err }, "DELETE", "/api/v1/teams/team_1/grants/grant_1", ""},
		{func() error { _, err := c.SetProjectTeam(ctx, "p", &team); return err }, "PUT", "/api/v1/projects/p/team", `{"team_id":"team_1"}`},
		{func() error { _, err := c.SetProjectTeam(ctx, "p", nil); return err }, "PUT", "/api/v1/projects/p/team", `{"team_id":null}`},
		{func() error { _, err := c.ProjectPermissions(ctx, "p"); return err }, "GET", "/api/v1/projects/p/permissions", ""},
		{func() error { _, err := c.Members(ctx); return err }, "GET", "/api/v1/members", ""},
	}
	for _, s := range steps {
		calls = nil
		if err := s.run(); err != nil {
			t.Errorf("%s %s: %v", s.method, s.path, err)
			continue
		}
		if len(calls) != 1 || calls[0].method != s.method || calls[0].path != s.path || calls[0].body != s.in {
			t.Errorf("want %s %s %q, got %+v", s.method, s.path, s.in, calls)
		}
	}

	perms, err := c.ProjectPermissions(ctx, "p")
	if err != nil || len(perms) != 1 || perms[0] != "project:update" {
		t.Errorf("permissions %v, %v", perms, err)
	}
	ms, err := c.Members(ctx)
	if err != nil || len(ms) != 1 || ms[0].UserID != "ausr_grace" {
		t.Errorf("members %+v, %v", ms, err)
	}
	var p Project
	if err = json.Unmarshal([]byte(`{"responsible_team_id":"team_1"}`), &p); err != nil || p.ResponsibleTeamID != "team_1" {
		t.Errorf("project %+v, %v", p, err)
	}
	for _, ref := range []string{"team_1", "payments", "PAYMENTS"} {
		if got, ferr := c.FindTeam(ctx, ref); ferr != nil || got.ID != "team_1" {
			t.Errorf("FindTeam(%q) = %+v, %v", ref, got, ferr)
		}
	}
	if _, err = c.FindTeam(ctx, "ops"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown team: %v", err)
	}
}
