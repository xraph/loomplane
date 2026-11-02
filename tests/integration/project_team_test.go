//go:build integration

package integration

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/projects"
)

// projectPermissions is GET /projects/:id/permissions as c.
func projectPermissions(c *client, projectID string) []string {
	c.t.Helper()
	var body struct {
		Permissions []string `json:"permissions"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+projectID+"/permissions", nil, &body)
	return body.Permissions
}

// joinAs invites the address with role and accepts as c.
func (w *teamsWorld) joinAs(c *client, email, role string) {
	w.t.Helper()
	var created struct {
		AcceptPath string `json:"accept_path"`
	}
	w.ada.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations", map[string]string{"email": email, "role": role}, &created)
	u, err := url.Parse(created.AcceptPath)
	if err != nil {
		w.t.Fatal(err)
	}
	if code := c.do(http.MethodPost, "/authsome/v1/orgs/invitations/accept", map[string]string{"token": u.Query().Get("token")}, nil); code != http.StatusOK && code != http.StatusCreated {
		w.t.Fatalf("%s accepts: status %d", email, code)
	}
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": w.org}, nil)
}

// A project names the team responsible for it, which anyone who may update
// the project sets, and reports what the caller may do on it.
func TestProjects_responsibleTeamAndPermissions(t *testing.T) {
	w := newTeamsWorld(t)
	p, q := w.project("P"), w.project("Q")
	payments := w.team("Payments", w.graceID)
	w.grant(payments.ID, "developer", p)

	var got projects.Project
	w.ada.must(http.StatusOK, http.MethodPut, "/api/v1/projects/"+p+"/team", map[string]string{"team_id": payments.ID}, &got)
	if got.ResponsibleTeamID != payments.ID {
		t.Fatalf("after Ada sets it: %q", got.ResponsibleTeamID)
	}
	w.ada.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+p, nil, &got)
	if got.ResponsibleTeamID != payments.ID {
		t.Fatalf("GET reads %q", got.ResponsibleTeamID)
	}

	// A viewer may not.
	if code, body := send(w.vi, http.MethodPut, "/api/v1/projects/"+p+"/team", map[string]any{"team_id": nil}); code != http.StatusForbidden {
		t.Fatalf("Vi clears: %d %s, want 403", code, body)
	}

	// A developer member may, and null clears it.
	dev := w.signUp("dev@example.com")
	w.joinAs(dev, "dev@example.com", "developer")
	dev.must(http.StatusOK, http.MethodPut, "/api/v1/projects/"+p+"/team", map[string]any{"team_id": nil}, &got)
	if got.ResponsibleTeamID != "" {
		t.Fatalf("after the developer clears it: %q", got.ResponsibleTeamID)
	}

	// Grace may on P through her team's developer grant, and not on Q.
	w.grace.must(http.StatusOK, http.MethodPut, "/api/v1/projects/"+p+"/team", map[string]string{"team_id": payments.ID}, &got)
	if code, body := send(w.grace, http.MethodPut, "/api/v1/projects/"+q+"/team", map[string]string{"team_id": payments.ID}); code != http.StatusForbidden {
		t.Fatalf("Grace on Q: %d %s, want 403", code, body)
	}

	// Another workspace's team, or none, is refused. Ada makes the other
	// workspace herself: authsome takes three sign-ups a minute from one
	// address, and the world has used them.
	var other struct {
		ID string `json:"id"`
	}
	w.ada.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Looms", "slug": "looms"}, &other)
	w.ada.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": other.ID}, nil)
	var theirs struct {
		ID string `json:"id"`
	}
	w.ada.must(http.StatusCreated, http.MethodPost, "/api/v1/teams", map[string]string{"name": "Weavers"}, &theirs)
	var theirProject projects.Project
	w.ada.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Loom"}, &theirProject)
	w.ada.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": w.org}, nil)
	for _, team := range []string{theirs.ID, "team_01kxxxxxxxxxxxxxxxxxxxxxxx"} {
		if code, body := send(w.ada, http.MethodPut, "/api/v1/projects/"+p+"/team", map[string]string{"team_id": team}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "isn't in this workspace") {
			t.Fatalf("team %s: %d %s, want 422", team, code, body)
		}
	}
	w.ada.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+p, nil, &got)
	if got.ResponsibleTeamID != payments.ID {
		t.Fatalf("a refused change left %q", got.ResponsibleTeamID)
	}
	// From Engines, Ada can't reach Looms' project, nor give it her team.
	if code, body := send(w.ada, http.MethodPut, "/api/v1/projects/"+theirProject.ID+"/team", map[string]string{"team_id": payments.ID}); code != http.StatusNotFound {
		t.Fatalf("another org's project: %d %s, want 404", code, body)
	}

	// Each change is audited with the team it set.
	rows, err := w.sql.Query(t.Context(), `SELECT action||'|'||resource_id||'|'||user_id||'|'||tenant_id||'|'||coalesce(metadata->>'team_id','')
		FROM chronicle_events WHERE action='project.team.set' ORDER BY sequence`)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	devID := w.userID("dev@example.com")
	want := []string{
		"project.team.set|" + p + "|" + w.adaID + "|" + w.org + "|" + payments.ID,
		"project.team.set|" + p + "|" + devID + "|" + w.org + "|",
		"project.team.set|" + p + "|" + w.graceID + "|" + w.org + "|" + payments.ID,
	}
	if !slices.Equal(audit, want) {
		t.Fatalf("audit\n got %q\nwant %q", audit, want)
	}

	// Grace is a viewer, and a developer on P through Payments.
	onP := projectPermissions(w.grace, p)
	for _, perm := range []string{"project:read", "project:update", "deployment:create"} {
		if !slices.Contains(onP, perm) {
			t.Errorf("Grace on P lacks %s: %v", perm, onP)
		}
	}
	if slices.Contains(onP, "deployment:approve") {
		t.Errorf("Grace on P has deployment:approve: %v", onP)
	}
	onQ := projectPermissions(w.grace, q)
	if slices.Contains(onQ, "project:update") || slices.Contains(onQ, "deployment:create") || !slices.Contains(onQ, "project:read") {
		t.Errorf("Grace on Q: %v", onQ)
	}
	// Ada owns the workspace, so she holds every project permission, in order.
	if onP := projectPermissions(w.ada, p); len(onP) != 22 || onP[0] != "project:read" || onP[len(onP)-1] != "ai:manage" {
		t.Errorf("Ada on P: %v", onP)
	}
	// Another workspace's project is not found.
	if code, body := send(w.grace, http.MethodGet, "/api/v1/projects/"+theirProject.ID+"/permissions", nil); code != http.StatusNotFound {
		t.Fatalf("another org's project: %d %s, want 404", code, body)
	}
}
