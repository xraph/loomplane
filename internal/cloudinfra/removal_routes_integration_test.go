//go:build integration

package cloudinfra

import (
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// A reviewed delete's removal is stored under the ID its plan, action and
// route give it, so a retry finds it, and the same removal can't be
// accepted twice.
func TestReviewedRemovalIsStoredUnderItsOwnID(t *testing.T) {
	pg := routeBindingDatabase(t)
	ctx := t.Context()
	for _, q := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('shop','org','Shop','shop','ready','usr',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('prod','org','shop','production','production',now(),now())`,
		`INSERT INTO loomplane_bindings(id,org_id,project_id,environment_id,subject,name,provider,engine,external_id,connection_id,config,commit_sha,created_at,updated_at) VALUES('b_web','org','shop','prod','service','web','digitalocean','node','app-1','conn_do','{"port":8080,"public":true}','abc123',now(),now())`,
	} {
		if _, err := pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	if err := connections.NewPgStore(pg).CreateConnection(ctx, &connections.Connection{ID: "conn_do", OrgID: "org", Cloud: "digitalocean", Name: "DO", Status: connections.StatusReady, Details: map[string]string{"account_uuid": "account"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	st := NewPgStore(pg)
	if err := st.CreateZone(ctx, &Zone{ID: "zone", OrgID: "org", Name: "example.com", Mode: "external", ProjectIDs: []string{"shop"}, ProofName: "_loomplane.example.com", ProofValue: "proof", ProofExpiresAt: now.Add(24 * time.Hour), VerifiedAt: now, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	in := RouteInput{EnvironmentID: "prod", Service: "web", Port: 8080, ZoneID: "zone", Hostname: "www.example.com"}
	binding, err := st.ResolveRouteBinding(ctx, "org", "shop", in)
	if err != nil {
		t.Fatal(err)
	}
	manager := security.Principal{OrgID: "org", UserID: "usr"}
	apply := &RouteOperation{ID: "routeop_www", Kind: "apply", Status: "preview", Revision: 1, Principal: manager,
		DesiredRecords: []provider.DNSRecord{{Name: "www", Type: "CNAME", Value: "shop-abc.ondigitalocean.app", TTL: 60}},
		Route:          Route{ID: "route_www", OrgID: "org", ProjectID: "shop", Input: in, Binding: binding, Status: "pending", OperationID: "routeop_www", Revision: 1}}
	if err = st.PreviewRoute(ctx, apply); err != nil {
		t.Fatal(err)
	}
	apply.Status = "pending"
	if err = st.AcceptRoute(ctx, apply); err != nil {
		t.Fatal(err)
	}
	svc := NewService(Deps{Store: st, Domains: st, Authz: cleanupTestAuth{}})
	id := RouteRemovalID("plan/service:web", "route_www")
	if _, err = svc.removeRoute(ctx, manager, "shop", "route_www", 1, id); err != nil {
		t.Fatal(err)
	}
	stored, err := st.RouteOperation(ctx, "org", id)
	if err != nil || stored.Kind != "remove" || stored.Route.ID != "route_www" {
		t.Fatalf("the removal under its ID: %+v %v", stored, err)
	}
	if _, err = svc.removeRoute(ctx, manager, "shop", "route_www", 1, id); err == nil {
		t.Fatal("the same removal was accepted twice")
	}
	if _, err = st.RouteOperation(ctx, "org", RouteRemovalID("plan/service:web", "route_other")); !errors.Is(err, ErrNotFound) {
		t.Fatal("another route's removal", err)
	}
}
