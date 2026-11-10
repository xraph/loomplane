//go:build integration

package planning

import (
	"testing"
)

// A release carries only server routes. A platform route follows its app in
// place, so the runner must not see it as a retained handoff.
func TestCurrentReleaseRoutesLeavesPlatformRoutesOut(t *testing.T) {
	store, conn := releaseDatabase(t)
	ctx := t.Context()
	for _, q := range []struct{ id, host, backend, body string }{
		{"route_srv", "srv.example.com", "server", `{"id":"route_srv","status":"ready","input":{"service":"web","environment_id":"prod","hostname":"srv.example.com"},"binding":{"server_id":"srv_1"}}`},
		{"route_app", "app.example.com", "platform", `{"id":"route_app","status":"ready","input":{"service":"web","environment_id":"prod","hostname":"app.example.com"},"binding":{"backend":"platform","connection_id":"conn_do"}}`},
	} {
		if _, err := conn.Exec(ctx, `INSERT INTO loomplane_domain_routes(id,org_id,project_id,environment_id,cloud_id,server_id,connection_id,zone_id,hostname,status,revision,body,backend) VALUES($1,'org','shop','prod','','','','zone',$2,'ready',1,$3::jsonb,$4)`, q.id, q.host, q.body, q.backend); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.CurrentReleaseRoutes(ctx, "org", "shop", "prod", "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "route_srv" {
		t.Fatalf("release routes: %+v", got)
	}
}

// A composed component's server route is a release's to carry, under the
// component's action name, as a service's is.
func TestCurrentReleaseRoutesCarriesAComponentsServerRoute(t *testing.T) {
	store, conn := releaseDatabase(t)
	ctx := t.Context()
	body := `{"id":"route_gw","status":"ready","input":{"service":"gw--gateway","environment_id":"prod","hostname":"api.example.com"},"binding":{"server_id":"srv_1","binding_id":"b_gw--gateway","instance_id":"inst_gw"}}`
	if _, err := conn.Exec(ctx, `INSERT INTO loomplane_domain_routes(id,org_id,project_id,environment_id,cloud_id,server_id,connection_id,zone_id,hostname,status,revision,body,backend) VALUES('route_gw','org','shop','prod','','srv_1','','zone','api.example.com','ready',1,$1::jsonb,'server')`, body); err != nil {
		t.Fatal(err)
	}
	got, err := store.CurrentReleaseRoutes(ctx, "org", "shop", "prod", "gw--gateway")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "route_gw" || got[0].Binding.InstanceID != "inst_gw" {
		t.Fatalf("release routes: %+v", got)
	}
	if others, err := store.CurrentReleaseRoutes(ctx, "org", "shop", "prod", "gw"); err != nil || len(others) != 0 {
		t.Fatalf("the composed resource's own routes: %+v %v", others, err)
	}
}
