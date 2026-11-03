package agentauthz_test

import (
	"context"
	"slices"
	"testing"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/llm"
	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/security"
)

// teamGrantee is a viewer of orgA whose admin rights come only from a
// team's grant on one project.
const teamGrantee = "usr_tg"

func projectID(t *testing.T) string {
	t.Helper()
	id, err := typeid.Generate("proj")
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}

// grantedAuthz is the real Authorizer with teamGrantee holding admin on p
// through a team, and nothing more than a viewer elsewhere.
func grantedAuthz(t *testing.T, p string) *security.Authorizer {
	t.Helper()
	ctx := context.Background()
	store := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(_ context.Context, org, user string) (string, bool, error) {
		if org == orgA && user == teamGrantee {
			return security.MemberViewer, true, nil
		}
		return "", false, nil
	}
	a := security.NewAuthorizer(eng, store, lookup)
	a.SetTeamGrants(func(_ context.Context, org, user string) ([]security.ScopedRole, error) {
		if org == orgA && user == teamGrantee {
			return []security.ScopedRole{{Role: security.RoleAdmin, ProjectID: p, TeamID: "team_payments"}}, nil
		}
		return nil, nil
	})
	if err = a.Sync(ctx, orgA, teamGrantee); err != nil {
		t.Fatal(err)
	}
	if err = a.AssignAgent(ctx, orgA); err != nil {
		t.Fatal(err)
	}
	return a
}

// What the model is shown follows the run's scope: a run about project P
// checks P, so a team's grant on P counts there; a run about the whole
// workspace, or about another project, checks that.
func TestVisibleFollowsTheRunScope(t *testing.T) {
	p, q := projectID(t), projectID(t)
	authz := grantedAuthz(t, p)
	reg := agentops.NewRegistry()
	agentops.Builtins(reg, &agentops.Deps{})
	a := &agentauthz.Authorizer{Registry: reg, Authz: authz}
	tools := make([]llm.Tool, 0, len(reg.Ops()))
	for _, op := range reg.Ops() {
		tools = append(tools, op.Tool)
	}
	me := security.Principal{Kind: security.KindUser, UserID: teamGrantee, OrgID: orgA}
	forMe := security.AgentPrincipal(orgA, &me)
	shown := func(s agentscope.Scope) []string {
		visible := a.Visible(context.Background(), cortex.Subject{Scope: s.Cortex(), Principal: forMe}, tools)
		names := make([]string, 0, len(visible))
		for _, tool := range visible {
			names = append(names, tool.Name)
		}
		return names
	}
	onP := shown(agentscope.Scope{OrgID: orgA, ProjectID: p})
	for _, want := range []string{"create_plan", "list_plans", "whoami"} {
		if !slices.Contains(onP, want) {
			t.Errorf("a run on P doesn't show %s: %v", want, onP)
		}
	}
	if !slices.Contains(shown(agentscope.Scope{OrgID: orgA, ProjectID: p, EnvironmentID: "env_staging"}), "create_plan") {
		t.Error("a run on one of P's environments doesn't show create_plan")
	}
	for name, s := range map[string]agentscope.Scope{
		"the workspace": {OrgID: orgA},
		"project Q":     {OrgID: orgA, ProjectID: q},
	} {
		got := shown(s)
		if slices.Contains(got, "create_plan") {
			t.Errorf("a run on %s shows create_plan", name)
		}
		if !slices.Contains(got, "list_plans") {
			t.Errorf("a run on %s hides what a viewer reads: %v", name, got)
		}
	}
}
