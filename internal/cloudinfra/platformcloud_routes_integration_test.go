//go:build integration

package cloudinfra

import (
	"errors"
	"testing"

	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/security"
)

// A Loomplane Cloud service resolves to the platform host its running
// placement is on, by that host's public ingress address only, and its route
// goes through review, acceptance and publication like any other.
func TestPlatformCloudRouteBinding(t *testing.T) {
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
	exec(`INSERT INTO loomplane_platform_regions(id,name,display_name,status,revision,created_at,updated_at) VALUES('reg_1','eu-west','EU West','active',1,now(),now())`)
	exec(`INSERT INTO loomplane_platform_hosts(id,region_id,endpoint,host_id,status,evidence,allotment,revision,created_at,updated_at)
VALUES('host_1','reg_1','https://192.0.2.1:7443','hw-1','ready','{"ingress_address":"203.0.113.9","runtime_identity":"secret-ish"}','{}',1,now(),now())`)
	exec(`INSERT INTO loomplane_platform_placements(id,org_id,project_id,environment_id,resource,kind,region_id,host_id,vcpus,memory_mib,disk_mib,state,instance_id,created_at,updated_at)
VALUES('plc_1','org','shop','prod','api','service','reg_1','host_1',1,512,4096,'running','instance',now(),now())`)
	exec(`CREATE TABLE cp_instances(id TEXT PRIMARY KEY,tenant_id TEXT,services JSONB,state TEXT)`)
	exec(`INSERT INTO cp_instances VALUES('instance','org','[{"name":"main","ports":[{"container":8080}]}]','running')`)
	exec(`INSERT INTO loomplane_bindings(id,org_id,project_id,environment_id,subject,name,provider,engine,external_id,connection_id,config,created_at,updated_at)
VALUES('binding','org','shop','prod','service','api','platform','http','instance','','{"cloud_placement":{"cloud_id":"cloud_platform","platform":{"region":"eu-west"}}}',now(),now())`)
	exec(`INSERT INTO loomplane_dns_zones(id,org_id,name,connection_id,revision,body) VALUES('zone','org','example.com','',1,'{"id":"zone","org_id":"org","name":"example.com","mode":"external","project_ids":["shop"],"proof_name":"_loomplane.example.com","proof_value":"proof","proof_expires_at":"2026-10-30T12:00:00Z","verified_at":"2026-10-29T12:00:00Z","revision":1}')`)
	st := NewPgStore(pg)
	in := RouteInput{ZoneID: "zone", EnvironmentID: "prod", Service: "api", Port: 8080, Hostname: "api.example.com"}
	binding, err := st.ResolveRouteBinding(ctx, "org", "shop", in)
	if err != nil {
		t.Fatal(err)
	}
	want := RouteBinding{Backend: RouteBackendFirecracker, BindingID: "binding", ConnectionID: fc.PlatformConnection, InstanceID: "instance", ServerID: "firecracker:platform", Address: "203.0.113.9"}
	if binding != want {
		t.Fatalf("binding %+v", binding)
	}
	for _, change := range []string{"port", "cloud", "environment", "draining", "stopped"} {
		invalid := in
		switch change {
		case "port":
			invalid.Port = 9090
		case "cloud":
			invalid.CloudID = "foreign"
		case "environment":
			invalid.EnvironmentID = "other"
		case "draining":
			exec(`UPDATE loomplane_platform_hosts SET status='draining' WHERE id='host_1'`)
		case "stopped":
			exec(`UPDATE loomplane_platform_hosts SET status='ready' WHERE id='host_1'`)
			exec(`UPDATE loomplane_platform_placements SET state='stopped' WHERE id='plc_1'`)
		}
		if _, resolveErr := st.ResolveRouteBinding(ctx, "org", "shop", invalid); !errors.Is(resolveErr, ErrNotFound) {
			t.Fatal(change, resolveErr)
		}
	}
	exec(`UPDATE loomplane_platform_placements SET state='running' WHERE id='plc_1'`)
	op := &RouteOperation{ID: "op_pc", Kind: "apply", Status: "preview", Revision: 1, Principal: security.Principal{OrgID: "org", UserID: "usr"}, Route: Route{ID: "route_pc", OrgID: "org", ProjectID: "shop", Input: in, Binding: binding, Status: "pending", OperationID: "op_pc", Revision: 1}}
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

	// Once the service moves, the reviewed route no longer matches it.
	exec(`UPDATE loomplane_platform_hosts SET evidence='{"ingress_address":"203.0.113.10"}' WHERE id='host_1'`)
	moved, err := st.ResolveRouteBinding(ctx, "org", "shop", in)
	if err != nil || moved == binding {
		t.Fatalf("after the move %+v %v", moved, err)
	}
}
