//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xraph/cortex"
	cortexengine "github.com/xraph/cortex/engine"
	cortexid "github.com/xraph/cortex/id"
	"github.com/xraph/cortex/llm"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// secondAgentOrg signs email up on a's server and makes them the owner of
// a new workspace there.
func secondAgentOrg(t *testing.T, a *agentOrg, email, name, slug string) *agentOrg {
	t.Helper()
	b := &agentOrg{r: a.r, cfg: a.cfg, owner: &client{t: t, base: a.r.srv.URL, key: a.cfg.Identity.PublishableKey}}
	account(t, b.owner, email)
	var org struct {
		ID string `json:"id"`
	}
	b.owner.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": name, "slug": slug}, &org)
	b.owner.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	b.orgID = org.ID
	return b
}

// userID is the signed-in owner's user ID.
func (a *agentOrg) userID() string {
	var s struct {
		UserID string `json:"user_id"`
	}
	a.owner.must(http.StatusOK, http.MethodGet, "/api/v1/session", nil, &s)
	return s.UserID
}

// invoke runs one of the agent's operations the way cortex does, through
// the agent's tool authorizer, as a's agent acting for its owner, at a's
// workspace's scope.
func (a *agentOrg) invoke(t *testing.T, reg *agentops.Registry, name string, args any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	p := security.AgentPrincipal(a.orgID, &security.Principal{UserID: a.userID(), OrgID: a.orgID})
	return reg.Dispatch(context.Background(), cortex.Invocation{
		Subject: cortex.Subject{Scope: agentscope.Scope{OrgID: a.orgID}.Cortex(), Principal: p, RunID: cortexid.NewAgentRunID()},
		Call:    llm.ToolCall{ID: "call_" + name, Name: name, Arguments: string(raw)},
	}, false)
}

// With AI on, cortex has every operation of the registry as a tool, the
// reads marked retry-safe, and the operations reach Loomplane's own
// services as the agent acting for a person: one workspace's agent finds
// nothing of another's, by ID, slug or name, while that workspace's own
// agent reads it.
func TestAgentOperationsKeepToTheirWorkspace(t *testing.T) {
	fake := agenttest.Anthropic(t)
	a := startAgentOrg(t, fake.AIConfig())
	defer a.r.stop()
	a.budget("10")
	a.enable(nil)
	b := secondAgentOrg(t, a, "bob@example.com", "Ledger Co", "ledger-co")
	b.budget("5")
	b.enable(nil)

	var proj struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	b.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Ledger"}, &proj)
	b.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+proj.ID+"/variables", map[string]string{"key": "LEDGER_MODE", "value": "strict"}, nil)

	reg := inject[*agentops.Registry](t, a.container())
	eng := inject[*cortexengine.Engine](t, a.container())
	tools := map[string]cortexengine.ToolInfo{}
	for _, info := range eng.Tools() {
		tools[info.Def.Name] = info
	}
	for _, op := range reg.Ops() {
		info, ok := tools[op.Def.Name]
		if !ok {
			t.Errorf("cortex has no %s", op.Def.Name)
			continue
		}
		if info.RetrySafe != op.Def.ReadOnly || (info.Kind == cortexengine.ToolExternal) != op.Def.External {
			t.Errorf("%s: %+v", op.Def.Name, info)
		}
	}

	reads := []string{"status", "list_variables", "list_deployments", "list_plans", "drift"}
	for _, ref := range []string{proj.ID, proj.Slug, proj.Name} {
		for _, op := range reads {
			res, err := a.invoke(t, reg, op, map[string]string{"project": ref})
			if err == nil || agentops.ErrorKind(err) != "not_found" {
				t.Errorf("Engines' agent %s(%q): %q, %v; want not found", op, ref, res, err)
			}
		}
		res, err := a.invoke(t, reg, "deployed", map[string]string{"project": ref, "environment": "production"})
		if err == nil || agentops.ErrorKind(err) != "not_found" {
			t.Errorf("Engines' agent deployed(%q): %q, %v; want not found", ref, res, err)
		}
	}
	evidence := map[string]map[string]string{
		"recent_canaries": {"project": proj.Slug, "environment": "production"},
		"rollback_target": {"project": proj.Slug, "environment": "production"},
		"binding_metrics": {"project": proj.Slug, "environment": "production", "service": "svc:ledger:node:apps/api"},
	}
	for op, args := range evidence {
		res, err := a.invoke(t, reg, op, args)
		if err == nil || agentops.ErrorKind(err) != "not_found" {
			t.Errorf("Engines' agent %s: %q, %v; want not found", op, res, err)
		}
	}
	res, err := a.invoke(t, reg, "list_projects", defs.NoInput{})
	if err != nil || strings.Contains(res, proj.ID) {
		t.Fatalf("Engines' agent lists %s, %v", res, err)
	}

	for _, op := range reads {
		if _, readErr := b.invoke(t, reg, op, map[string]string{"project": proj.Slug}); readErr != nil {
			t.Errorf("Ledger's agent %s: %v", op, readErr)
		}
	}
	res, err = b.invoke(t, reg, "recent_canaries", evidence["recent_canaries"])
	if err != nil || res != `{"canaries":[]}` {
		t.Fatalf("Ledger's agent recent_canaries: %s, %v", res, err)
	}
	// Ledger runs no api yet: the real planning service and the binding
	// lookup find nothing, after the project's infrastructure:read passed.
	if res, err = b.invoke(t, reg, "binding_metrics", evidence["binding_metrics"]); agentops.ErrorKind(err) != "not_found" || !strings.Contains(err.Error(), "doesn't run") {
		t.Fatalf("Ledger's agent binding_metrics: %s, %v", res, err)
	}
	res, err = b.invoke(t, reg, "list_variables", map[string]string{"project": "ledger"})
	if err != nil || !strings.Contains(res, `"key":"LEDGER_MODE"`) || strings.Contains(res, "strict") {
		t.Fatalf("Ledger's agent list_variables: %s, %v", res, err)
	}
	res, err = b.invoke(t, reg, "whoami", defs.NoInput{})
	if err != nil || !strings.Contains(res, security.AgentSubject(b.orgID)) || !strings.Contains(res, `"project:read"`) || strings.Contains(res, `"secret:manage"`) {
		t.Fatalf("Ledger's agent whoami: %s, %v", res, err)
	}
}

// The agent's tool authorizer reads plans and projects through the real
// services as the agent: one workspace's agent can't reach another's
// production plan, even by its ID, while that workspace's own agent is
// asked to leave the plan to a person, since its autonomy is off.
func TestAgentAuthorizerKeepsToItsWorkspace(t *testing.T) {
	fake := agenttest.Anthropic(t)
	a := startAgentOrg(t, fake.AIConfig())
	defer a.r.stop()
	a.budget("10")
	a.enable(nil)
	b := secondAgentOrg(t, a, "bob@example.com", "Ledger Co", "ledger-co")
	b.budget("5")
	b.enable(nil)

	var proj struct {
		ID string `json:"id"`
	}
	b.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Ledger"}, &proj)
	var manifest struct {
		ID       string `json:"id"`
		Revision int    `json:"revision"`
	}
	b.owner.must(http.StatusCreated, http.MethodPut, "/api/v1/projects/"+proj.ID+"/manifest", map[string]string{"yaml": "version: 1\n"}, &manifest)
	ctx := context.Background()
	bob := security.Principal{UserID: b.userID(), OrgID: b.orgID}
	envs, err := inject[*projects.Service](t, a.container()).Environments(ctx, bob, proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	var production string
	for _, e := range envs {
		if e.Kind == projects.KindProduction {
			production = e.ID
		}
	}
	plan := &planning.Plan{
		ID: "plan_ledger_production", OrgID: b.orgID, ProjectID: proj.ID, EnvironmentID: production, Environment: "production",
		Status: planning.StatusReady, ManifestID: manifest.ID, ManifestRevision: manifest.Revision, CreatedBy: bob.UserID, CreatedAt: time.Now().UTC(),
	}
	if _, err = inject[*planning.PgStore](t, a.container()).CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}

	authz := inject[*agentauthz.Authorizer](t, a.container())
	approve := llm.ToolCall{ID: "call_approve", Name: "approve_plan", Arguments: `{"plan_id":"` + plan.ID + `","revision":"r1"}`}
	engines := security.AgentPrincipal(a.orgID, &security.Principal{UserID: a.userID(), OrgID: a.orgID})
	err = authz.Authorize(ctx, cortex.Subject{Scope: agentscope.Scope{OrgID: a.orgID}.Cortex(), Principal: engines, RunID: cortexid.NewAgentRunID()}, approve)
	if err == nil || errors.Is(err, cortex.ErrRequiresApproval) || agentops.ErrorKind(err) != "not_found" {
		t.Fatalf("Engines' agent approving Ledger's plan: %v", err)
	}
	ledger := security.AgentPrincipal(b.orgID, &bob)
	err = authz.Authorize(ctx, cortex.Subject{Scope: agentscope.Scope{OrgID: b.orgID}.Cortex(), Principal: ledger, RunID: cortexid.NewAgentRunID()}, approve)
	var ar cortex.ApprovalRequired
	if !errors.As(err, &ar) || !strings.Contains(ar.Reason, "autonomous promotion is off") {
		t.Fatalf("Ledger's agent approving its own plan: %v", err)
	}
}

// list_teams reads the real teams service as the agent acting for a
// person: each workspace's agent lists its own teams with a member count,
// never a member's email, and never the description as it was typed.
func TestAgentListsOnlyItsWorkspacesTeams(t *testing.T) {
	fake := agenttest.Anthropic(t)
	a := startAgentOrg(t, fake.AIConfig())
	defer a.r.stop()
	a.budget("10")
	a.enable(nil)
	b := secondAgentOrg(t, a, "bob@example.com", "Ledger Co", "ledger-co")
	b.budget("5")
	b.enable(nil)

	var team struct {
		ID string `json:"id"`
	}
	b.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/teams", map[string]string{"name": "Ledger crew", "description": "Keeps the books"}, &team)
	b.owner.must(http.StatusOK, http.MethodPut, "/api/v1/teams/"+team.ID+"/members/"+b.userID(), nil, nil)
	a.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/teams", map[string]string{"name": "Engines"}, nil)

	reg := inject[*agentops.Registry](t, a.container())
	res, err := a.invoke(t, reg, "list_teams", defs.NoInput{})
	if err != nil || !strings.Contains(res, `"slug":"engines"`) || strings.Contains(res, "ledger") || strings.Contains(res, "books") {
		t.Fatalf("Engines' agent list_teams: %s, %v", res, err)
	}
	res, err = b.invoke(t, reg, "list_teams", defs.NoInput{})
	if err != nil {
		t.Fatalf("Ledger's agent list_teams: %v", err)
	}
	var out struct {
		Teams []struct {
			Slug        string `json:"slug"`
			Name        string `json:"name"`
			Description string `json:"description"`
			MemberCount int    `json:"member_count"`
		} `json:"teams"`
	}
	if err := json.Unmarshal([]byte(res), &out); err != nil {
		t.Fatal(err)
	}
	// This call has no shield tenant on its context, which a run has, so the scan
	// fails and the description is withheld. Either way it never reaches
	// the model as typed.
	if len(out.Teams) != 1 || out.Teams[0].Slug != "ledger-crew" || out.Teams[0].Name != "Ledger crew" || out.Teams[0].MemberCount != 1 ||
		out.Teams[0].Description == "Keeps the books" || out.Teams[0].Description == "" {
		t.Fatalf("Ledger's agent list_teams: %s", res)
	}
	if strings.Contains(res, "@") || strings.Contains(res, b.userID()) || strings.Contains(res, team.ID) {
		t.Fatalf("list_teams names the members: %s", res)
	}
}
