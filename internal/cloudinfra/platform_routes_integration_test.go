//go:build integration

package cloudinfra

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func pgExec(ctx context.Context, pg *pgdriver.PgDB, query string) error {
	_, err := pg.Exec(ctx, query)
	return err
}

// TestDomainScopeAcceptsPlatformRoutes pins the platform branch of
// loomplane_domain_scope: the route rests on a ready connection, not on a
// server.
func TestDomainScopeAcceptsPlatformRoutes(t *testing.T) {
	pg := routeBindingDatabase(t)
	ctx := t.Context()
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := pg.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('shop','org','Shop','shop','ready','usr',now(),now())`)
	must(`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('prod','org','shop','production','production',now(),now())`)
	must(`INSERT INTO loomplane_dns_zones(id,org_id,name,connection_id,revision,body) VALUES('zone','org','example.com','',1,'{}')`)
	now := time.Now().UTC()
	if err := connections.NewPgStore(pg).CreateConnection(ctx, &connections.Connection{ID: "conn_do", OrgID: "org", Cloud: "digitalocean", Name: "DO", Status: connections.StatusReady, Details: map[string]string{"account_uuid": "account"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	route := func(id, host, connection string) error {
		_, err := pg.Exec(ctx, `INSERT INTO loomplane_domain_routes(id,org_id,project_id,environment_id,cloud_id,server_id,connection_id,zone_id,hostname,status,revision,body,backend) VALUES($1,'org','shop','prod','','',$2,'zone',$3,'pending',1,'{}','platform')`, id, connection, host)
		return err
	}
	uses := func(id string) int {
		t.Helper()
		var n int
		if err := pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_connection_uses WHERE kind='domain_route' AND owner_id=$1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := route("route_p", "p.example.com", "conn_do"); err != nil || uses("route_p") != 1 {
		t.Fatal("platform route on a ready connection", err)
	}
	if err := route("route_gone", "g.example.com", "conn_gone"); err == nil || !strings.Contains(err.Error(), "route connection unavailable") {
		t.Fatal("platform route on a missing connection:", err)
	}
	must(`UPDATE loomplane_cloud_connections SET status='incomplete' WHERE id='conn_do'`)
	if err := route("route_q", "q.example.com", "conn_do"); err == nil || !strings.Contains(err.Error(), "route connection unavailable") {
		t.Fatal("platform route on an incomplete connection:", err)
	}
	if err := pgExec(ctx, pg, `UPDATE loomplane_domain_routes SET status='removed' WHERE id='route_p'`); err != nil || uses("route_p") != 0 {
		t.Fatal("removing a platform route", err)
	}
}

// TestRouteIntentGuardAllowsReceipts pins loomplane_route_intent_guard:
// receipts are written as an operation runs, the rest of the intent is not.
func TestRouteIntentGuardAllowsReceipts(t *testing.T) {
	pg := routeBindingDatabase(t)
	ctx := t.Context()
	body := `{"kind":"apply","status":"running","route":{"status":"pending","hostname":"p.example.com"},"desired_records":[{"name":"p"}]}`
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_route_operations(id,org_id,project_id,route_id,server_id,cloud_id,status,revision,body,principal) VALUES('op_1','org','shop','route_p','','','succeeded',1,$1::jsonb,'{}')`, body); err != nil {
		t.Fatal(err)
	}
	if err := pgExec(ctx, pg, `UPDATE loomplane_route_operations SET body=jsonb_set(body,'{receipts}','[{"id":"r1"}]'::jsonb) WHERE id='op_1'`); err != nil {
		t.Fatal("operation receipts refused:", err)
	}
	if err := pgExec(ctx, pg, `UPDATE loomplane_route_operations SET body=jsonb_set(body,'{route,receipts}','[{"id":"r1"}]'::jsonb) WHERE id='op_1'`); err != nil {
		t.Fatal("route receipts refused:", err)
	}
	err := pgExec(ctx, pg, `UPDATE loomplane_route_operations SET body=jsonb_set(body,'{desired_records}','[{"name":"other"}]'::jsonb) WHERE id='op_1'`)
	if err == nil || !strings.Contains(err.Error(), "route intent is immutable") {
		t.Fatal("desired records changed:", err)
	}
}

// TestPlatformRouteBindingQuery pins platformRouteBindingQuery: a public
// DigitalOcean service on a ready connection resolves to its app, and only on
// its public port. Apply rechecks the same binding.
func TestPlatformRouteBindingQuery(t *testing.T) {
	pg := routeBindingDatabase(t)
	ctx := t.Context()
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := pg.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('shop','org','Shop','shop','ready','usr',now(),now())`)
	must(`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('prod','org','shop','production','production',now(),now())`)
	now := time.Now().UTC()
	conns := connections.NewPgStore(pg)
	for id, status := range map[string]string{"conn_do": connections.StatusReady, "conn_off": "incomplete"} {
		if err := conns.CreateConnection(ctx, &connections.Connection{ID: id, OrgID: "org", Cloud: "digitalocean", Name: id, Status: status, Details: map[string]string{"account_uuid": "account"}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	var generation int64
	if err := pg.QueryRow(ctx, `SELECT generation FROM loomplane_cloud_connections WHERE id='conn_do'`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	binding := func(subject, name, external, connection, config string) {
		t.Helper()
		must(`INSERT INTO loomplane_bindings(id,org_id,project_id,environment_id,subject,name,provider,engine,external_id,connection_id,config,commit_sha,created_at,updated_at) VALUES($1,'org','shop','prod',$2,$3,'digitalocean','node',$4,$5,$6,'abc123',now(),now())`, "b_"+name, subject, name, external, connection, config)
	}
	binding("service", "web", "app-1", "conn_do", `{"port":8080,"public":true}`)
	binding("service", "worker", "app-2", "conn_do", `{"port":8080,"public":false}`)
	binding("service", "api", "app-3", "conn_off", `{"port":8080,"public":true}`)
	binding("resource", "db", "db/cluster-1", "conn_do", `{"port":25060,"public":true}`)
	st := NewPgStore(pg)
	in := RouteInput{EnvironmentID: "prod", Service: "web", Port: 8080}

	got, err := st.ResolveRouteBinding(ctx, "org", "shop", in)
	want := RouteBinding{Backend: RouteBackendPlatform, Provider: "digitalocean", BindingID: "b_web", ExternalID: "app-1", ConnectionID: "conn_do", ConnectionGeneration: generation}
	if err != nil || got != want {
		t.Fatalf("public service: %+v %v", got, err)
	}
	if commit, commitErr := st.RouteBindingCommit(ctx, "org", "b_web"); commitErr != nil || commit != "abc123" {
		t.Fatal("commit", commit, commitErr)
	}
	if _, commitErr := st.RouteBindingCommit(ctx, "other", "b_web"); !errors.Is(commitErr, ErrNotFound) {
		t.Fatal("another organization's commit", commitErr)
	}
	refused := func(name string, in RouteInput, sentence string) {
		t.Helper()
		b, resolveErr := st.ResolveRouteBinding(ctx, "org", "shop", in)
		if !errors.Is(resolveErr, ErrInvalid) || resolveErr.Error() != ErrInvalid.Error()+": "+sentence {
			t.Fatalf("%s: %+v %v", name, b, resolveErr)
		}
	}
	other := in
	other.Port = 3000
	refused("another port", other, "App Platform routes a hostname only to this service's public port, 8080.")
	private := in
	private.Service = "worker"
	refused("a private service", private, "This service is private on App Platform, so no hostname can reach it. Make it public in the manifest and deploy it first.")
	for _, service := range []string{"api", "db"} {
		missing := in
		missing.Service = service
		if b, resolveErr := st.ResolveRouteBinding(ctx, "org", "shop", missing); !errors.Is(resolveErr, ErrNotFound) {
			t.Fatalf("%s resolved: %+v %v", service, b, resolveErr)
		}
	}

	zone := &Zone{ID: "zone", OrgID: "org", Name: "example.com", Mode: "external", ProjectIDs: []string{"shop"}, ProofName: "_loomplane.example.com", ProofValue: "proof",
		ProofExpiresAt: now.Add(24 * time.Hour), VerifiedAt: now, Revision: 1}
	if err = st.CreateZone(ctx, zone); err != nil {
		t.Fatal(err)
	}
	accept := func(id string, binding RouteBinding) error {
		input := in
		input.ZoneID, input.Hostname = "zone", id+".example.com"
		op := &RouteOperation{ID: "routeop_" + id, Kind: "apply", Status: "preview", Revision: 1, Principal: security.Principal{OrgID: "org", UserID: "usr"},
			DesiredRecords: []provider.DNSRecord{{Name: id, Type: "CNAME", Value: "shop-abc.ondigitalocean.app", TTL: 60}},
			Route:          Route{ID: "route_" + id, OrgID: "org", ProjectID: "shop", Input: input, Binding: binding, Status: "pending", OperationID: "routeop_" + id, Revision: 1}}
		if previewErr := st.PreviewRoute(ctx, op); previewErr != nil {
			return previewErr
		}
		op.Status = "pending"
		return st.AcceptRoute(ctx, op)
	}
	if err = accept("www", got); err != nil {
		t.Fatal(err)
	}
	var routeBackend, connection, serverID string
	if err = pg.QueryRow(ctx, `SELECT backend,connection_id,server_id FROM loomplane_domain_routes WHERE id='route_www'`).Scan(&routeBackend, &connection, &serverID); err != nil {
		t.Fatal(err)
	}
	if routeBackend != "platform" || connection != "conn_do" || serverID != "" {
		t.Fatal("stored route", routeBackend, connection, serverID)
	}
	// A service made private after the preview is refused at apply.
	must(`UPDATE loomplane_bindings SET config='{"port":8080,"public":false}' WHERE id='b_web'`)
	if err = accept("shop", got); !errors.Is(err, ErrInvalid) {
		t.Fatal("private at apply", err)
	}
	// So is a binding that moved to another app.
	must(`UPDATE loomplane_bindings SET config='{"port":8080,"public":true}',external_id='app-9' WHERE id='b_web'`)
	if err = accept("app", got); !errors.Is(err, ErrConflict) {
		t.Fatal("another app at apply", err)
	}
}
