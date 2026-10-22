//go:build integration

package cloudinfra

import (
	"errors"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

func TestKubernetesRouteBindingQuery(t *testing.T) {
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
	details := map[string]string{"namespace": "shop", "namespace_uid": "ns-uid", "ingress_class": "nginx", "ingress_address": "203.0.113.10", "endpoint": "https://cluster.example.test"}
	if err := connections.NewPgStore(pg).CreateConnection(ctx, &connections.Connection{ID: "conn_k8s", OrgID: "org", Cloud: "kubernetes", Name: "Cluster", Status: connections.StatusReady, Details: details, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	profiles := cloudprofiles.NewPgStore(pg)
	if err := profiles.CreateProfile(ctx, &cloudprofiles.Profile{ID: "cloud_1", OrgID: "org", ProjectID: "shop", Name: "cloud_1", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := profiles.CreateTarget(ctx, &cloudprofiles.Target{ID: "target_k8s", OrgID: "org", ProjectID: "shop", CloudID: "cloud_1", Name: "cluster", DisplayName: "Cluster", Provider: "kubernetes", ConnectionID: "conn_k8s", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	exec(`CREATE TABLE cp_instances(id TEXT PRIMARY KEY,tenant_id TEXT,current_release TEXT,service_refs JSONB,services JSONB,labels JSONB,state TEXT)`)
	exec(`CREATE TABLE cp_deployments(id TEXT PRIMARY KEY,tenant_id TEXT,instance_id TEXT,release_id TEXT,state TEXT,created_at TIMESTAMPTZ DEFAULT now())`)
	placed, loose, fallback := id.New(id.PrefixInstance), id.New(id.PrefixInstance), id.New(id.PrefixInstance)
	for _, instance := range []id.ID{placed, loose, fallback} {
		exec(`INSERT INTO cp_instances VALUES($1,'org',NULL,'{}',jsonb_build_array(jsonb_build_object('name','gateway','ports','[{"container":8080},{"container":9090}]'::jsonb)),'{}','running')`, instance.String())
	}
	binding := func(name string, instance id.ID, config string) {
		t.Helper()
		exec(`INSERT INTO loomplane_bindings(id,org_id,project_id,environment_id,subject,name,provider,engine,external_id,connection_id,config,created_at,updated_at) VALUES($1,'org','shop','prod','resource',$2,'kubernetes','http',$3,'conn_k8s',$4,now(),now())`, "b_"+name, name, instance.String(), config)
	}
	binding("octo--gateway", placed, `{"routable_ports":[8080],"cloud_placement":{"cloud_id":"cloud_1","target_id":"target_k8s"}}`)
	binding("loose--gateway", loose, `{"routable_ports":[8080]}`)
	binding("default--gateway", fallback, `{"routable_ports":[8080],"cloud_placement":{"cloud_id":"cloud_1","default":true}}`)
	st := NewPgStore(pg)

	check := func(name string, in RouteInput, instance id.ID) RouteBinding {
		t.Helper()
		got, err := st.ResolveRouteBinding(ctx, "org", "shop", in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Backend != "kubernetes" || got.ServiceName != kubernetes.WorkloadName(instance) || got.Address != "203.0.113.10" || got.RuntimeID != "ns-uid" || got.Namespace != "shop" ||
			got.IngressClass != "nginx" || got.ConnectionID != "conn_k8s" || got.InstanceID != instance.String() || got.ReleaseID != "" || got.ContainerID != "" || got.ServerID != "" {
			t.Fatalf("%s: %+v", name, got)
		}
		port := in
		port.Port = 9090
		if b, portErr := st.ResolveRouteBinding(ctx, "org", "shop", port); !errors.Is(portErr, ErrNotFound) {
			t.Fatalf("%s resolved port 9090: %+v %v", name, b, portErr)
		}
		return got
	}
	gateway := RouteInput{EnvironmentID: "prod", Service: "octo--gateway", CloudID: "cloud_1", Port: 8080}
	granted := check("under a Cloud grant", gateway, placed)
	if granted.TargetID != "target_k8s" {
		t.Fatal("target", granted.TargetID)
	}
	inferred := gateway
	cloud, err := st.RouteBindingCloud(ctx, "org", "shop", "prod", "octo--gateway")
	if err != nil || cloud != "cloud_1" {
		t.Fatal("inferred Cloud", cloud, err)
	}
	inferred.CloudID = cloud
	check("under a Cloud grant with the Cloud inferred", inferred, placed)
	environment := RouteInput{EnvironmentID: "prod", Service: "loose--gateway", Port: 8080}
	if cloud, err = st.RouteBindingCloud(ctx, "org", "shop", "prod", "loose--gateway"); err != nil || cloud != "" {
		t.Fatal("no placement", cloud, err)
	}
	check("on the environment's connection", environment, loose)
	check("by the Cloud's default", RouteInput{EnvironmentID: "prod", Service: "default--gateway", CloudID: "cloud_1", Port: 8080}, fallback)

	for name, refused := range map[string]RouteInput{
		"a placed binding without its Cloud":       {EnvironmentID: "prod", Service: "octo--gateway", Port: 8080},
		"an unplaced binding under a Cloud":        {EnvironmentID: "prod", Service: "loose--gateway", CloudID: "cloud_1", Port: 8080},
		"a placed binding under another Cloud":     {EnvironmentID: "prod", Service: "octo--gateway", CloudID: "cloud_other", Port: 8080},
		"a binding in another environment":         {EnvironmentID: "staging", Service: "octo--gateway", CloudID: "cloud_1", Port: 8080},
		"a port the component doesn't list at all": {EnvironmentID: "prod", Service: "octo--gateway", CloudID: "cloud_1", Port: 7070},
	} {
		if b, resolveErr := st.ResolveRouteBinding(ctx, "org", "shop", refused); !errors.Is(resolveErr, ErrNotFound) {
			t.Fatalf("%s resolved: %+v %v", name, b, resolveErr)
		}
	}

	// A grant limited to other targets covers neither the target nor the default.
	exec(`UPDATE loomplane_cloud_project_grants SET target_ids='{target_other}',revision=revision+1 WHERE org_id='org' AND cloud_id='cloud_1' AND project_id='shop'`)
	for _, in := range []RouteInput{gateway, {EnvironmentID: "prod", Service: "default--gateway", CloudID: "cloud_1", Port: 8080}} {
		if b, resolveErr := st.ResolveRouteBinding(ctx, "org", "shop", in); !errors.Is(resolveErr, ErrNotFound) {
			t.Fatalf("%s resolved under a target-limited grant: %+v %v", in.Service, b, resolveErr)
		}
	}
	exec(`UPDATE loomplane_cloud_project_grants SET target_ids='{target_k8s}',revision=revision+1 WHERE org_id='org' AND cloud_id='cloud_1' AND project_id='shop'`)
	check("under a grant naming its target", gateway, placed)

	// Preview and apply recheck the same binding. Apply stores the backend and
	// the empty Cloud of an environment-connection route.
	exec(`INSERT INTO loomplane_dns_zones(id,org_id,name,connection_id,revision,body) VALUES('zone','org','example.com','',1,$1)`, `{"id":"zone","org_id":"org","name":"example.com","mode":"external","project_ids":["shop"],"verified_at":"2026-11-05T12:00:00Z","revision":1}`)
	looseBinding := check("before apply", environment, loose)
	input := environment
	input.ZoneID, input.Hostname = "zone", "api.example.com"
	op := &RouteOperation{ID: "routeop_k", Kind: "apply", Status: "preview", Revision: 1, Principal: security.Principal{OrgID: "org", UserID: "usr"},
		Route: Route{ID: "route_k", OrgID: "org", ProjectID: "shop", Input: input, Binding: looseBinding, Status: "pending", OperationID: "routeop_k", Revision: 1}}
	if err = st.PreviewRoute(ctx, op); err != nil {
		t.Fatal(err)
	}
	op.Status = "pending"
	if err = st.AcceptRoute(ctx, op); err != nil {
		t.Fatal(err)
	}
	var routeBackend, operationBackend, cloudID string
	if err = pg.QueryRow(ctx, `SELECT r.backend,o.backend,r.cloud_id FROM loomplane_domain_routes r JOIN loomplane_route_operations o ON o.route_id=r.id WHERE r.id='route_k'`).Scan(&routeBackend, &operationBackend, &cloudID); err != nil {
		t.Fatal(err)
	}
	if routeBackend != "kubernetes" || operationBackend != "kubernetes" || cloudID != "" {
		t.Fatal("stored route", routeBackend, operationBackend, cloudID)
	}
	op.Status = "running"
	state, err := st.BeginIngress(ctx, op)
	if err != nil || state.Generation != 0 || state.Current != nil {
		t.Fatal("Kubernetes ingress state", state, err)
	}
	op.Route.Status = "waiting"
	if err = st.PublishRoute(ctx, op); err != nil {
		t.Fatal(err)
	}
	var configs int
	if err = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_ingress_configs`).Scan(&configs); err != nil || configs != 0 {
		t.Fatal("a Kubernetes route wrote a server ingress config", configs, err)
	}

	// A revoked grant hides the placed binding.
	exec(`UPDATE loomplane_cloud_project_grants SET revoked=true,revision=revision+1 WHERE org_id='org' AND cloud_id='cloud_1' AND project_id='shop'`)
	if b, resolveErr := st.ResolveRouteBinding(ctx, "org", "shop", gateway); !errors.Is(resolveErr, ErrNotFound) {
		t.Fatalf("resolved under a revoked grant: %+v %v", b, resolveErr)
	}
	exec(`UPDATE loomplane_cloud_connections SET status='checking' WHERE id='conn_k8s'`)
	if b, resolveErr := st.ResolveRouteBinding(ctx, "org", "shop", environment); !errors.Is(resolveErr, ErrNotFound) {
		t.Fatalf("resolved on a connection that isn't ready: %+v %v", b, resolveErr)
	}
}

// TestDomainScopeChecksRouteBackend pins loomplane_domain_scope after the
// kubernetes_routes migration: each backend needs its own ready runtime.
func TestDomainScopeChecksRouteBackend(t *testing.T) {
	pg := routeBindingDatabase(t)
	ctx := t.Context()
	exec := func(query string, args ...any) error {
		_, err := pg.Exec(ctx, query, args...)
		return err
	}
	must := func(query string, args ...any) {
		t.Helper()
		if err := exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('shop','org','Shop','shop','ready','usr',now(),now())`)
	must(`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('prod','org','shop','production','production',now(),now())`)
	must(`INSERT INTO loomplane_dns_zones(id,org_id,name,connection_id,revision,body) VALUES('zone','org','example.com','',1,'{}')`)
	now := time.Now().UTC()
	store := connections.NewPgStore(pg)
	for _, c := range []*connections.Connection{
		{ID: "conn_k8s", OrgID: "org", Cloud: "kubernetes", Name: "Cluster", Status: connections.StatusReady, CreatedAt: now, UpdatedAt: now},
		{ID: "conn_srv", OrgID: "org", Cloud: "server", Name: "Runtime", Status: connections.StatusReady, Details: map[string]string{"docker_id": "daemon"}, CreatedAt: now, UpdatedAt: now},
	} {
		if err := store.CreateConnection(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := cloudprofiles.NewPgStore(pg).CreateProfile(ctx, &cloudprofiles.Profile{ID: "cloud_1", OrgID: "org", ProjectID: "shop", Name: "cloud_1", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := NewPgStore(pg).SaveServer(ctx, &Server{ID: "srv_1", OrgID: "org", CloudID: "cloud_1", Name: "web-1", Ownership: "adopted", Provider: "server", AccountID: "daemon", ResourceID: "daemon", ConnectionID: "conn_srv", RuntimeConnectionID: "conn_srv", Revision: 1, Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	route := func(id, host, backend, cloud, server, connection string) error {
		return exec(`INSERT INTO loomplane_domain_routes(id,org_id,project_id,environment_id,cloud_id,server_id,connection_id,zone_id,hostname,status,revision,body,backend) VALUES($1,'org','shop','prod',$2,$3,$4,'zone',$5,'pending',1,'{}',$6)`,
			id, cloud, server, connection, host, backend)
	}
	uses := func(id string) int {
		t.Helper()
		var n int
		if err := pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_connection_uses WHERE kind='domain_route' AND owner_id=$1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := route("route_k", "k.example.com", "kubernetes", "", "", "conn_k8s"); err != nil || uses("route_k") != 1 {
		t.Fatal("Kubernetes route on a ready connection", err)
	}
	if err := route("route_s", "s.example.com", "server", "cloud_1", "srv_1", "conn_srv"); err != nil || uses("route_s") != 1 {
		t.Fatal("server route on a ready server", err)
	}
	// A route without a backend reads as a server route.
	if err := exec(`INSERT INTO loomplane_domain_routes(id,org_id,project_id,environment_id,cloud_id,server_id,connection_id,zone_id,hostname,status,revision,body) VALUES('route_old','org','shop','prod','cloud_1','srv_1','conn_srv','zone','old.example.com','pending',1,'{}')`); err != nil {
		t.Fatal("route without a backend", err)
	}
	for name, err := range map[string]error{
		"a Kubernetes route on a server connection":  route("route_x1", "x1.example.com", "kubernetes", "", "", "conn_srv"),
		"a Kubernetes route on a missing connection": route("route_x2", "x2.example.com", "kubernetes", "", "", "conn_gone"),
		"a server route on a Kubernetes connection":  route("route_x3", "x3.example.com", "server", "", "", "conn_k8s"),
		"a server route on a missing server":         route("route_x4", "x4.example.com", "server", "cloud_1", "srv_gone", "conn_srv"),
	} {
		if err == nil {
			t.Fatal(name, "accepted")
		}
	}
	must(`UPDATE loomplane_cloud_connections SET status='checking' WHERE id='conn_k8s'`)
	must(`UPDATE loomplane_cloud_servers SET status='unhealthy' WHERE id='srv_1'`)
	if err := exec(`UPDATE loomplane_domain_routes SET revision=2 WHERE id='route_k'`); err == nil {
		t.Fatal("Kubernetes route kept on a connection that isn't ready")
	}
	if err := exec(`UPDATE loomplane_domain_routes SET revision=2 WHERE id='route_s'`); err == nil {
		t.Fatal("server route kept on a server that isn't ready")
	}
	// Removal still goes through and releases the connection.
	for _, id := range []string{"route_k", "route_s"} {
		if err := exec(`UPDATE loomplane_domain_routes SET status='removed' WHERE id=$1`, id); err != nil || uses(id) != 0 {
			t.Fatal("removing", id, err)
		}
	}
}
