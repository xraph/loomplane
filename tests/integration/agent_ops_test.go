//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/xraph/cortex"
	cortexengine "github.com/xraph/cortex/engine"
	cortexid "github.com/xraph/cortex/id"
	"github.com/xraph/cortex/llm"

	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agenttest"
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

// invoke runs one of the agent's operations the way cortex does, as a's
// agent acting for its owner, at a's workspace's scope.
func (a *agentOrg) invoke(t *testing.T, reg *agentops.Registry, name string, args any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	p := security.AgentPrincipal(a.orgID, &security.Principal{UserID: a.userID(), OrgID: a.orgID})
	return reg.Invoke(context.Background(), cortex.Invocation{
		Subject: cortex.Subject{Scope: agentscope.Scope{OrgID: a.orgID}.Cortex(), Principal: p, RunID: cortexid.NewAgentRunID()},
		Call:    llm.ToolCall{ID: "call_" + name, Name: name, Arguments: string(raw)},
	})
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
		"binding_metrics": {"project": proj.Slug, "environment": "production", "service": "api"},
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
