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
