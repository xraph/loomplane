package agentops

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/llm"
	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/security"
)

// whoami answers for the run's scope: in a run about project P, a team's
// grant on P shows; in a run about the workspace, only workspace rights.
func TestWhoamiFollowsTheRunScope(t *testing.T) {
	ctx := context.Background()
	const org, user = "o", "usr_tg"
	tid, err := typeid.Generate("proj")
	if err != nil {
		t.Fatal(err)
	}
	p := tid.String()
	store := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	authz := security.NewAuthorizer(eng, store, func(_ context.Context, o, u string) (string, bool, error) {
		return security.MemberViewer, o == org && u == user, nil
	})
	authz.SetTeamGrants(func(context.Context, string, string) ([]security.ScopedRole, error) {
		return []security.ScopedRole{{Role: security.RoleAdmin, ProjectID: p, TeamID: "team_payments"}}, nil
	})
	if err = authz.Sync(ctx, org, user); err != nil {
		t.Fatal(err)
	}
	if err = authz.AssignAgent(ctx, org); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	Builtins(r, &Deps{Authz: authz})
	me := security.Principal{Kind: security.KindUser, UserID: user, OrgID: org}
	perms := func(s agentscope.Scope) []string {
		out, err := r.invoke(ctx, cortex.Invocation{
			Subject: cortex.Subject{Scope: s.Cortex(), Principal: security.AgentPrincipal(org, &me)},
			Call:    llm.ToolCall{ID: "c1", Name: "whoami", Arguments: "{}"},
		})
		if err != nil {
			t.Fatal(err)
		}
		var got whoamiOutput
		if err = json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		return got.Permissions
	}
	onP := perms(agentscope.Scope{OrgID: org, ProjectID: p})
	if !slices.Contains(onP, security.DeploymentCreate.Name()) {
		t.Fatalf("on P: %v", onP)
	}
	onOrg := perms(agentscope.Scope{OrgID: org})
	if slices.Contains(onOrg, security.DeploymentCreate.Name()) || !slices.Contains(onOrg, security.ProjectRead.Name()) {
		t.Fatalf("on the workspace: %v", onOrg)
	}
}
