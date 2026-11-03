package cloudinfra

import (
	"errors"
	"testing"

	"github.com/xraph/warden"
	wardenmemory "github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/internal/security"
)

// A deployment the agent approved hands its routes off as the agent alone.
// That's an infrastructure write, which the agent may make only while the
// deployment runs.
func TestAgentHandsOffRoutesOnlyInADeploymentRun(t *testing.T) {
	svc, st, _ := subdomainFixture()
	in := subdomainInput()
	in.CloudID = "cloud_1"
	st.routes = append(st.routes, &Route{ID: "route_app", OrgID: "org", ProjectID: "shop", Status: "ready", Revision: 1, Input: in,
		Binding: RouteBinding{BindingID: "binding_old", ServerID: "srv_old", Address: "192.0.2.9"}})
	svc.Resolver = proofResolver{[]string{"proof"}}
	store := wardenmemory.New()
	eng, err := warden.NewEngine(warden.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	authz := security.NewAuthorizer(eng, store, nil)
	if err = authz.AssignAgent(t.Context(), "org"); err != nil {
		t.Fatal(err)
	}
	svc.Authz = authz
	agent := security.AgentPrincipal("org", nil)
	handoff := HandoffInput{Revision: 1, DestinationCloudID: "cloud_1"}
	if _, err = svc.PrepareRouteHandoff(t.Context(), agent, "shop", "route_app", handoff); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("an agent handoff outside a deployment run: %v, want ErrForbidden", err)
	}
	if len(st.accepted) != 0 {
		t.Fatal("a refused handoff was accepted", st.accepted)
	}
	op, err := svc.PrepareRouteHandoff(security.WithDeploymentRun(t.Context()), agent, "shop", "route_app", handoff)
	if err != nil || op.Kind != "prepare" || len(st.accepted) != 1 {
		t.Fatalf("an agent handoff in a deployment run: %+v %v", op, err)
	}
}
