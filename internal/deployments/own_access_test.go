package deployments

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providers"
)

const ownUserURL = "postgres://lp_shop_api:own-password@db.example:5432/api?sslmode=require"

// grantingResolver is the workspace service, and makes each service's own
// user as asked.
type grantingResolver struct {
	workspaceResolver
	granted []catalogconnect.OwnAccess
	revoked []string
}

func (g *grantingResolver) GrantOwnAccess(_ context.Context, orgID, endpointID string, a catalogconnect.OwnAccess, _ catalogconnect.DialFunc) (string, error) {
	if orgID != "org" || endpointID != workspaceServiceID {
		return "", catalogconnect.ErrNotFound
	}
	g.granted = append(g.granted, a)
	return ownUserURL, nil
}

func (g *grantingResolver) RevokeOwnUser(_ context.Context, _, endpointID, user, environmentID string, _ catalogconnect.DialFunc) error {
	g.revoked = append(g.revoked, endpointID+"/"+user+"@"+environmentID)
	return nil
}

func ownUserLinks() []planning.EnvLink {
	base := planning.EnvLink{From: "orders", Subject: planning.ResourceSubject, Access: catalogconnect.AccessOwnUser, Database: "api", User: "lp_shop_api"}
	url, user := base, base
	url.Key = "DATABASE_URL"
	user.Key, user.Part = "PGUSER", "user"
	return []planning.EnvLink{url, user}
}

func TestAServiceReleaseMakesItsOwnUserAndReadsIt(t *testing.T) {
	a := workspaceAttachment()
	a.Op = planning.Keep
	raw, err := json.Marshal(a.Resource)
	if err != nil {
		t.Fatal(err)
	}
	binding := &planning.Binding{Name: "orders", Subject: planning.ResourceSubject, Provider: planning.WorkspaceProvider, Config: raw}
	api := planning.Action{Name: "api", Provider: "server", Service: &planning.ServiceConfig{Target: "conn_box", Env: ownUserLinks()}}
	d := &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}
	job := &Job{Deployment: d, Plan: &planning.Plan{CreatedBy: "dev", Actions: []planning.Action{a}}, Action: api}
	secrets := &memSecrets{m: map[string][]byte{}}
	granter := &grantingResolver{workspaceResolver: workspaceResolver{endpoint: registeredOrders()}}
	r := NewRunner(RunnerDeps{Authz: existingAuth{}, Providers: providers.Registry(), Resources: secrets,
		ExistingEndpoints: &reachChecks{}, ExistingDialer: anyTarget{}, WorkspaceEndpoints: granter})

	if verr := r.verifyExistingServiceAccess(t.Context(), job, map[string]*planning.Binding{"orders": binding}); verr != nil {
		t.Fatal(verr)
	}
	want := catalogconnect.OwnAccess{Access: catalogconnect.AccessOwnUser, Database: "api", User: "lp_shop_api", ProjectID: "project", Service: "api", EnvironmentID: "env"}
	if len(granter.granted) != 1 || granter.granted[0] != want {
		t.Fatalf("granted: %+v", granter.granted)
	}
	links := ownUserLinks()
	if got, _, aerr := r.serviceResourceAddress(t.Context(), d, "api", nil, nil, links[0]); aerr != nil || got != ownUserURL {
		t.Fatalf("url: %q %v", got, aerr)
	}
	if got, _, aerr := r.serviceResourceAddress(t.Context(), d, "api", nil, nil, links[1]); aerr != nil || got != "lp_shop_api" {
		t.Fatalf("user: %q %v", got, aerr)
	}
	// Through the gateway, it's refused.
	gated := links[0]
	gated.Gateway = "eu.db.example.com"
	if _, _, gerr := r.serviceResourceAddress(t.Context(), d, "api", nil, nil, gated); gerr == nil || !strings.Contains(gerr.Error(), "gateway") {
		t.Fatalf("through the gateway: %v", gerr)
	}

	// Released without it, the user is let go of, from the target api ran
	// on, and the kept connection string is forgotten.
	config, err := json.Marshal(api.Service)
	if err != nil {
		t.Fatal(err)
	}
	old := &planning.Binding{Name: "api", Subject: planning.ServiceSubject, Provider: "server", Config: config}
	warnings := r.releaseConsumers(t.Context(), d, "api", old, &planning.ServiceConfig{Target: "conn_box"}, map[string]*planning.Binding{"orders": binding})
	if len(warnings) != 0 || len(granter.revoked) != 1 || granter.revoked[0] != workspaceServiceID+"/lp_shop_api@env" {
		t.Fatalf("warnings %v, revoked %v", warnings, granter.revoked)
	}
	if _, ok := secrets.m[OwnAccessKey("org", "env", "orders", "api")]; ok {
		t.Fatal("the connection string was kept")
	}
	if _, _, err := r.serviceResourceAddress(t.Context(), d, "api", nil, nil, links[0]); err == nil {
		t.Fatal("read a connection that was let go of")
	}
}

func TestAServiceKeepingItsOwnUserIsntRevoked(t *testing.T) {
	secrets := &memSecrets{m: map[string][]byte{}}
	granter := &grantingResolver{}
	r := NewRunner(RunnerDeps{Providers: providers.Registry(), Resources: secrets, ExistingDialer: anyTarget{}, WorkspaceEndpoints: granter})
	d := &Deployment{OrgID: "org", EnvironmentID: "env"}
	raw, _ := json.Marshal(ownAccessGrant{URL: ownUserURL, EndpointID: workspaceServiceID})
	secrets.m[OwnAccessKey("org", "env", "orders", "api")] = raw
	svc := planning.ServiceConfig{Target: "conn_box", Env: ownUserLinks()}
	config, _ := json.Marshal(svc)
	old := &planning.Binding{Name: "api", Subject: planning.ServiceSubject, Config: config}
	if warnings := r.releaseConsumers(t.Context(), d, "api", old, &svc, nil); len(warnings) != 0 || len(granter.revoked) != 0 {
		t.Fatalf("warnings %v, revoked %v", warnings, granter.revoked)
	}
	// Moving to its own database only lets the user go.
	moved := planning.ServiceConfig{Target: "conn_box", Env: []planning.EnvLink{{Key: "DATABASE_URL", From: "orders", Subject: planning.ResourceSubject, Access: catalogconnect.AccessOwnDatabase, Database: "api"}}}
	if warnings := r.releaseConsumers(t.Context(), d, "api", old, &moved, nil); len(warnings) != 0 || len(granter.revoked) != 1 {
		t.Fatalf("warnings %v, revoked %v", warnings, granter.revoked)
	}
}
