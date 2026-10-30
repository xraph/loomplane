//go:build integration

package cloudinfra

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

func TestFirecrackerRouteBindingAndPublication(t *testing.T) {
	pg := routeBindingDatabase(t)
	ctx := t.Context()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pg.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('shop','org','Shop','shop','ready','usr',now(),now())`)
	exec(`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('prod','org','shop','production','production',now(),now())`)
	now := time.Now().UTC()
	conn := &connections.Connection{ID: "conn_fc", OrgID: "org", Cloud: "firecracker", Name: "Host", Status: connections.StatusReady, Details: map[string]string{"runtime_identity": strings.Repeat("a", 64), "ingress_address": "203.0.113.10"}, CreatedAt: now, UpdatedAt: now}
	if err := connections.NewPgStore(pg).CreateConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}
	exec(`CREATE TABLE cp_instances(id TEXT PRIMARY KEY,tenant_id TEXT,services JSONB,state TEXT)`)
	exec(`INSERT INTO cp_instances VALUES('instance','org','[{"name":"main","ports":[{"container":8080}]}]','running')`)
	exec(`INSERT INTO loomplane_bindings(id,org_id,project_id,environment_id,subject,name,provider,engine,external_id,connection_id,config,created_at,updated_at) VALUES('binding','org','shop','prod','service','api','firecracker','http','instance','conn_fc','{}',now(),now())`)
	exec(`INSERT INTO loomplane_dns_zones(id,org_id,name,connection_id,revision,body) VALUES('zone','org','example.com','',1,'{"id":"zone","org_id":"org","name":"example.com","mode":"external","project_ids":["shop"],"proof_name":"_loomplane.example.com","proof_value":"proof","proof_expires_at":"2026-10-30T12:00:00Z","verified_at":"2026-10-29T12:00:00Z","revision":1}')`)
	st := NewPgStore(pg)
	in := RouteInput{ZoneID: "zone", EnvironmentID: "prod", Service: "api", Port: 8080, Hostname: "api.example.com"}
	binding, err := st.ResolveRouteBinding(ctx, "org", "shop", in)
	if err != nil {
		t.Fatal(err)
	}
	if binding.Backend != RouteBackendFirecracker || binding.InstanceID != "instance" || binding.ServerID != "firecracker:conn_fc" {
		t.Fatal(binding)
	}
	for _, change := range []string{"port", "cloud", "environment"} {
		invalid := in
		switch change {
		case "port":
			invalid.Port = 9090
		case "cloud":
			invalid.CloudID = "foreign"
		case "environment":
			invalid.EnvironmentID = "other"
		}
		if _, resolveErr := st.ResolveRouteBinding(ctx, "org", "shop", invalid); !errors.Is(resolveErr, ErrNotFound) {
			t.Fatal(change, resolveErr)
		}
	}
	op := &RouteOperation{ID: "op_fc", Kind: "apply", Status: "preview", Revision: 1, Principal: security.Principal{OrgID: "org", UserID: "usr"}, Route: Route{ID: "route_fc", OrgID: "org", ProjectID: "shop", Input: in, Binding: binding, Status: "pending", OperationID: "op_fc", Revision: 1}}
	if err = st.PreviewRoute(ctx, op); err != nil {
		t.Fatal(err)
	}
	op.Status = "pending"
	if err = st.AcceptRoute(ctx, op); err != nil {
		t.Fatal(err)
	}
	if _, err = st.BeginIngress(ctx, op); err != nil {
		t.Fatal(err)
	}
	op.Route.Status = "ready"
	if err = st.PublishRoute(ctx, op); err != nil {
		t.Fatal(err)
	}
	var uses int
	if err = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_connection_uses WHERE owner_id='route_fc' AND kind='domain_route'`).Scan(&uses); err != nil || uses != 1 {
		t.Fatal("connection not retained", uses, err)
	}
	stale := *op
	if _, err = st.BeginServerIngress(ctx, "org", binding.ServerID); err != nil {
		t.Fatal(err)
	}
	if err = st.PublishRoute(ctx, &stale); !errors.Is(err, ErrConflict) {
		t.Fatal("stale publication accepted", err)
	}
	exec(`UPDATE loomplane_cloud_connections SET status='checking' WHERE id='conn_fc'`)
	if _, err = pg.Exec(ctx, `UPDATE loomplane_domain_routes SET revision=revision+1 WHERE id='route_fc'`); err == nil {
		t.Fatal("unchecked connection retained route")
	}
	exec(`UPDATE loomplane_domain_routes SET status='removed' WHERE id='route_fc'`)
}
