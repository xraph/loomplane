package aiagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/agentsettings/agentsettingstest"
	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// roleAuthz allows what the principal's role allows in security.Catalog.
// The principal's UserID names its role.
type roleAuthz struct{}

func (roleAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, _ string) error {
	for _, role := range security.Catalog() {
		if role.Slug == p.UserID && slices.Contains(role.Permissions, perm) {
			return nil
		}
	}
	return fmt.Errorf("%w (%s)", security.ErrForbidden, perm.Name())
}

type noProjects struct{}

func (noProjects) GetProject(context.Context, string, string) (*projects.Project, error) {
	return nil, projects.ErrNotFound
}

// fakeAutonomy is the projects service, as far as autonomy goes.
type fakeAutonomy struct {
	got []projects.AutonomyInput
	err error
}

func (f *fakeAutonomy) SetAutonomy(_ context.Context, p security.Principal, projectID string, in projects.AutonomyInput) (*projects.Project, error) {
	if f.err != nil {
		return nil, f.err
	}
	if p.UserID != security.RoleAdmin {
		return nil, security.ErrForbidden
	}
	if projectID != "proj_1" {
		return nil, projects.ErrNotFound
	}
	f.got = append(f.got, in)
	return &projects.Project{ID: projectID, OrgID: p.OrgID, AutonomousPromotion: in.AutonomousPromotion != nil && *in.AutonomousPromotion}, nil
}

type routesRig struct {
	r        forge.Router
	autonomy *fakeAutonomy
	settings *agentsettings.Service
}

// newRoutesRig mounts the routes over the real agent settings, with the
// agent off in every workspace.
func newRoutesRig(t *testing.T, guard identity.Guard) *routesRig {
	t.Helper()
	store := agentsettingstest.NewStore()
	ai := aisettings.NewService(aisettings.Deps{Store: store, Keys: agentsettingstest.NewKeys(), Authz: roleAuthz{}, Projects: noProjects{}})
	rig := &routesRig{autonomy: &fakeAutonomy{}, r: forge.NewRouter()}
	rig.settings = agentsettings.NewService(agentsettings.Deps{
		Store: store, AI: ai, Authz: roleAuthz{}, Config: serverAI(), Environment: config.EnvProduction,
	})
	if err := mountRoutes(rig.r, guard, rig.settings, rig.autonomy); err != nil {
		t.Fatal(err)
	}
	return rig
}

func serve(t *testing.T, r forge.Router, method, path, body string, p *security.Principal) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if p != nil {
		req = req.WithContext(security.WithPrincipal(req.Context(), *p))
	}
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}

func as(role string) *security.Principal { return &security.Principal{OrgID: "org_1", UserID: role} }

func TestRoutesRegistered(t *testing.T) {
	guarded := 0
	guard := identity.Guard{func(forge.Handler) forge.Handler {
		return func(forge.Context) error {
			guarded++
			return forge.NewHTTPError(http.StatusTeapot, "the guard")
		}
	}}
	rig := newRoutesRig(t, guard)
	want := map[string]string{
		"agentStatus.get":        "GET /api/v1/agent/status",
		"projectAutonomy.update": "PUT /api/v1/projects/:id/autonomy",
	}
	got := map[string]string{}
	for _, info := range rig.r.Routes() {
		if _, ours := want[info.Name]; !ours {
			continue
		}
		got[info.Name] = info.Method + " " + info.Path
		if !slices.Contains(info.Tags, "Agent") {
			t.Errorf("%s has tags %v", info.Name, info.Tags)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("routes %v", got)
	}
	for name, route := range want {
		if got[name] != route {
			t.Errorf("%s is %q, want %q", name, got[name], route)
		}
	}
	for _, c := range [][2]string{{http.MethodGet, "/api/v1/agent/status"}, {http.MethodPut, "/api/v1/projects/proj_1/autonomy"}} {
		if out := serve(t, rig.r, c[0], c[1], `{}`, as(security.RoleAdmin)); out.Code != http.StatusTeapot {
			t.Errorf("%s %s skipped the guard: %d", c[0], c[1], out.Code)
		}
	}
	if guarded != 2 {
		t.Fatalf("the guard ran %d times", guarded)
	}
}

// With the agent off, which is where every workspace starts, a member
// still reads the status, and an admin still sets a project's autonomy:
// neither route sits behind the binder, which needs the agent on.
func TestRoutesWorkWithTheAgentOff(t *testing.T) {
	rig := newRoutesRig(t, nil)
	out := serve(t, rig.r, http.MethodGet, "/api/v1/agent/status", "", as(security.RoleViewer))
	var st agentsettings.Status
	if err := json.Unmarshal(out.Body.Bytes(), &st); err != nil || out.Code != http.StatusOK {
		t.Fatalf("status: %d %s", out.Code, out.Body.String())
	}
	if !st.Server || st.Enabled {
		t.Fatalf("status %+v", st)
	}
	if out = serve(t, rig.r, http.MethodGet, "/api/v1/agent/status", "", nil); out.Code != http.StatusUnauthorized {
		t.Errorf("no principal: %d", out.Code)
	}

	out = serve(t, rig.r, http.MethodPut, "/api/v1/projects/proj_1/autonomy", `{"autonomous_promotion":true,"min_staging_soak_seconds":1800}`, as(security.RoleAdmin))
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"autonomous_promotion":true`) {
		t.Fatalf("admin: %d %s", out.Code, out.Body.String())
	}
	if len(rig.autonomy.got) != 1 || rig.autonomy.got[0].MinStagingSoakSeconds == nil || *rig.autonomy.got[0].MinStagingSoakSeconds != 1800 {
		t.Fatalf("passed %+v", rig.autonomy.got)
	}
}

func TestAutonomyErrors(t *testing.T) {
	rig := newRoutesRig(t, nil)
	for _, c := range []struct {
		name   string
		who    *security.Principal
		path   string
		body   string
		err    error
		status int
	}{
		{"developer", as(security.RoleDeveloper), "/api/v1/projects/proj_1/autonomy", `{"autonomous_promotion":true}`, nil, http.StatusForbidden},
		{"missing project", as(security.RoleAdmin), "/api/v1/projects/nope/autonomy", `{"autonomous_promotion":true}`, nil, http.StatusNotFound},
		{"invalid", as(security.RoleAdmin), "/api/v1/projects/proj_1/autonomy", `{}`, projects.ErrInvalid, http.StatusUnprocessableEntity},
		{"not json", as(security.RoleAdmin), "/api/v1/projects/proj_1/autonomy", `{`, nil, http.StatusBadRequest},
	} {
		rig.autonomy.err = c.err
		if out := serve(t, rig.r, http.MethodPut, c.path, c.body, c.who); out.Code != c.status {
			t.Errorf("%s: %d %s, want %d", c.name, out.Code, out.Body.String(), c.status)
		}
	}
}

func TestFailMapsTheAgentsErrors(t *testing.T) {
	for err, status := range map[error]int{
		projects.ErrNotFound:                             http.StatusNotFound,
		agentsettings.ErrNotFound:                        http.StatusNotFound,
		projects.ErrInvalid:                              http.StatusUnprocessableEntity,
		agentsettings.ErrInvalid:                         http.StatusUnprocessableEntity,
		security.ErrForbidden:                            http.StatusForbidden,
		agentsettings.ErrDisabled:                        http.StatusConflict,
		agentsettings.ErrBudgetExhausted:                 http.StatusConflict,
		fmt.Errorf("wrapped: %w", security.ErrForbidden): http.StatusForbidden,
	} {
		var sc interface{ StatusCode() int }
		got := fail(err)
		if !errors.As(got, &sc) || sc.StatusCode() != status {
			t.Errorf("%v: %v, want %d", err, got, status)
		}
	}
}

// The promotion gate isn't in this version: every production promotion
// still asks a person, and the autonomy route says so.
func TestAutonomyRouteSaysPromotionStillAsks(t *testing.T) {
	if !strings.Contains(autonomyDescription, "still waits for a person") ||
		!strings.Contains(autonomyDescription, "when the promotion gate ships") {
		t.Fatalf("description %q", autonomyDescription)
	}
	if strings.Contains(autonomySummary, "without asking first, after") {
		t.Fatalf("summary %q", autonomySummary)
	}
}
