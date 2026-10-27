//go:build integration

package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/ops"
)

// routeBindingDatabase applies the real migrations to a disposable database.
// Ctrl Plane owns cp_instances and cp_deployments, so they are created here.
func routeBindingDatabase(t *testing.T) *pgdriver.PgDB {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the route binding queries")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("route_binding_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	if err = ops.Migrate(ctx, u.String(), []*migrate.Group{migrations.Group()}); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return pg
}

func TestCatalogGatewayBindingResolves(t *testing.T) {
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
	if err := connections.NewPgStore(pg).CreateConnection(ctx, &connections.Connection{ID: "runtime", OrgID: "org", Cloud: "server", Name: "Runtime", Status: connections.StatusReady, Details: map[string]string{"host": "192.0.2.42", "docker_id": "daemon"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	profiles := cloudprofiles.NewPgStore(pg)
	for _, cloud := range []string{"cloud_1", "cloud_other"} {
		if err := profiles.CreateProfile(ctx, &cloudprofiles.Profile{ID: cloud, OrgID: "org", ProjectID: "shop", Name: cloud, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := profiles.CreateTarget(ctx, &cloudprofiles.Target{ID: "target_1", OrgID: "org", ProjectID: "shop", CloudID: "cloud_1", Name: "servers", DisplayName: "Servers", Provider: "server", ConnectionID: "runtime", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	st := NewPgStore(pg)
	if err := st.SaveServer(ctx, &Server{ID: "srv_1", OrgID: "org", CloudID: "cloud_1", Name: "web-1", Ownership: "adopted", Provider: "server", AccountID: "daemon", ResourceID: "daemon", ConnectionID: "runtime", RuntimeConnectionID: "runtime", Revision: 1, Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	exec(`CREATE TABLE cp_instances(id TEXT PRIMARY KEY,tenant_id TEXT,current_release TEXT,service_refs JSONB,services JSONB,labels JSONB,state TEXT)`)
	exec(`CREATE TABLE cp_deployments(id TEXT PRIMARY KEY,tenant_id TEXT,instance_id TEXT,release_id TEXT,state TEXT,created_at TIMESTAMPTZ DEFAULT now())`)
	instance := func(id, service, ports string) {
		t.Helper()
		exec(`INSERT INTO cp_instances VALUES($1,'org',NULL,jsonb_build_object($2::text,$1::text||'-container'),jsonb_build_array(jsonb_build_object('name',$2::text,'ports',$3::jsonb)),jsonb_build_object('loomplane.revision',$1::text||'/release'),'running')`, id, service, ports)
	}
	// A component's instance runs one service, main, with every recipe port.
	instance("gw", "main", `[{"container":8080,"protocol":"tcp"},{"container":9090,"protocol":"tcp"}]`)
	instance("pg", "postgres", `[{"container":5432}]`)
	instance("web", "main", `[{"container":8080}]`)
	instance("side", "sidecar", `[{"container":8080}]`)
	// A component whose routable port sits on a service other than main.
	exec(`INSERT INTO cp_instances VALUES('split','org',NULL,'{"main":"split-main","api":"split-api"}',` +
		`'[{"name":"main","ports":[{"container":9000,"protocol":"tcp"}]},{"name":"api","ports":[{"container":8080,"protocol":"tcp"}]}]','{"loomplane.revision":"split/release"}','running')`)
	placement := `"cloud_placement":{"cloud_id":"cloud_1","target_id":"target_1","server_id":"srv_1"}`
	binding := func(id, subject, name, instanceID, config string) {
		t.Helper()
		exec(`INSERT INTO loomplane_bindings(id,org_id,project_id,environment_id,subject,name,provider,engine,external_id,connection_id,config,created_at,updated_at) VALUES($1,'org','shop','prod',$2,$3,'server','http',$4,'runtime',$5,now(),now())`, id, subject, name, instanceID, config)
	}
	// The gateway component, its composed resource and the web service are
	// bound as Plan 0 writes them for a direct target whose connection runs
	// one Cloud server: the whole ResourceConfig or ServiceConfig, with that
	// server's ID and no revision in the placement.
	planned := `"cloud_placement":{"server_id":"srv_1","grant_revision":1,"cloud_id":"cloud_1","cloud_revision":1,"assignment_revision":1,"target_id":"target_1","target_revision":1,"target_name":"servers","provider":"server","connection_id":"runtime","account_identity":"daemon","region_source":"default"}`
	settings := `"target":"runtime","target_settings":{"docker_id":"daemon","host":"192.0.2.42"},"provider":"server"`
	binding("b_gw", "resource", "octo--gateway", "gw", `{`+settings+`,"kind":"octopus","version":"1",`+
		`"component":{"kind":"octopus","resource":"octo","revision":"1","component":{"name":"gateway","role":"process","ports":[{"name":"http","container":8080,"protocol":"http","routable":true},{"name":"admin","container":9090,"protocol":"http","control":true}],"protection":"none","image":"octopus/gateway:1","step":0},"protection":"none"},`+
		`"routable_ports":[8080],`+planned+`}`)
	binding("b_octo", "resource", "octo", "gw", `{`+settings+`,"kind":"octopus","version":"1","composition":{"kind":"octopus","version":"1","revision":"1"},`+planned+`}`)
	binding("b_split", "resource", "octo--split", "split", `{`+settings+`,"kind":"octopus","version":"1",`+
		`"component":{"kind":"octopus","resource":"octo","revision":"1","component":{"name":"split","role":"process","ports":[{"name":"health","container":9000,"protocol":"http"},{"name":"http","container":8080,"protocol":"http","routable":true}],"protection":"none","image":"octopus/split:1","step":1},"protection":"none"},`+
		`"routable_ports":[8080],`+planned+`}`)
	binding("b_pg", "resource", "postgres", "pg", `{`+placement+`}`)
	binding("b_web", "service", "web", "web", `{`+planned+`,`+settings+`,"health_path":"/_/health","workload":"http-service","execution":"container","service":"svc:go:cmd/web","source":"cmd/web","image":"Dockerfile","port":8080,"public":false}`)
	// A service binding keeps routing only its main service, whatever its config says.
	binding("b_side", "service", "side", "side", `{"routable_ports":[8080],`+placement+`}`)
	binding("b_loose", "resource", "loose", "gw", `{"routable_ports":[8080]}`)

	in := RouteInput{EnvironmentID: "prod", Service: "octo--gateway", CloudID: "cloud_1", Port: 8080}
	got, err := st.ResolveRouteBinding(ctx, "org", "shop", in)
	if err != nil {
		t.Fatal(err)
	}
	if got.BackendName() != RouteBackendServer || got.Address != "192.0.2.42" || got.BindingID != "b_gw" || got.ServerID != "srv_1" || got.InstanceID != "gw" || got.ContainerID != "gw-container" || got.ReleaseID != "gw/release" {
		t.Fatalf("gateway binding: %+v", got)
	}
	web, err := st.ResolveRouteBinding(ctx, "org", "shop", RouteInput{EnvironmentID: "prod", Service: "web", CloudID: "cloud_1", Port: 8080})
	if err != nil || web.BindingID != "b_web" || web.ContainerID != "web-container" {
		t.Fatalf("service binding: %+v %v", web, err)
	}
	for name, refused := range map[string]RouteInput{
		"a port missing from routable_ports":  {EnvironmentID: "prod", Service: "octo--gateway", CloudID: "cloud_1", Port: 9090},
		"a database without routable_ports":   {EnvironmentID: "prod", Service: "postgres", CloudID: "cloud_1", Port: 5432},
		"another Cloud":                       {EnvironmentID: "prod", Service: "octo--gateway", CloudID: "cloud_other", Port: 8080},
		"a service port outside main":         {EnvironmentID: "prod", Service: "side", CloudID: "cloud_1", Port: 8080},
		"a binding without a Cloud placement": {EnvironmentID: "prod", Service: "loose", CloudID: "cloud_1", Port: 8080},
		"the composed resource itself":        {EnvironmentID: "prod", Service: "octo", CloudID: "cloud_1", Port: 8080},
	} {
		if b, resolveErr := st.ResolveRouteBinding(ctx, "org", "shop", refused); !errors.Is(resolveErr, ErrNotFound) {
			t.Fatalf("%s resolved: %+v %v", name, b, resolveErr)
		}
	}

	// A revoked grant takes the Cloud's routes away from the project.
	exec(`UPDATE loomplane_cloud_project_grants SET revoked=true,revision=revision+1 WHERE org_id='org' AND cloud_id='cloud_1' AND project_id='shop'`)
	for _, revoked := range []RouteInput{in, {EnvironmentID: "prod", Service: "web", CloudID: "cloud_1", Port: 8080}} {
		if b, resolveErr := st.ResolveRouteBinding(ctx, "org", "shop", revoked); !errors.Is(resolveErr, ErrNotFound) {
			t.Fatalf("%s resolved under a revoked grant: %+v %v", revoked.Service, b, resolveErr)
		}
	}
	exec(`UPDATE loomplane_cloud_project_grants SET revoked=false,revision=revision+1 WHERE org_id='org' AND cloud_id='cloud_1' AND project_id='shop'`)

	// Rollback, handoff and release check the accepted container of the same
	// routed service. A service binding still checks only main.
	gateway := Route{ID: "route_gw", OrgID: "org", ProjectID: "shop", Input: in, Binding: got}
	if checkErr := st.ValidateRetainedRoute(ctx, gateway); checkErr != nil {
		t.Fatal("retained gateway route", checkErr)
	}
	if checkErr := st.ValidateRouteCleanup(ctx, gateway); checkErr != nil {
		t.Fatal("gateway route cleanup", checkErr)
	}
	webRoute := Route{ID: "route_web", OrgID: "org", ProjectID: "shop", Input: RouteInput{EnvironmentID: "prod", Service: "web", CloudID: "cloud_1", Port: 8080}, Binding: web}
	if checkErr := st.ValidateRetainedRoute(ctx, webRoute); checkErr != nil {
		t.Fatal("retained service route", checkErr)
	}
	// The split component routes its api service, the one declaring the port.
	splitIn := RouteInput{EnvironmentID: "prod", Service: "octo--split", CloudID: "cloud_1", Port: 8080}
	split, err := st.ResolveRouteBinding(ctx, "org", "shop", splitIn)
	if err != nil || split.BindingID != "b_split" || split.InstanceID != "split" || split.ContainerID != "split-api" || split.ReleaseID != "split/release" {
		t.Fatalf("component routed on a service other than main: %+v %v", split, err)
	}
	splitRoute := Route{ID: "route_split", OrgID: "org", ProjectID: "shop", Input: splitIn, Binding: split}
	if checkErr := st.ValidateRetainedRoute(ctx, splitRoute); checkErr != nil {
		t.Fatal("retained route on a service other than main", checkErr)
	}
	if checkErr := st.ValidateRouteCleanup(ctx, splitRoute); checkErr != nil {
		t.Fatal("cleanup on a service other than main", checkErr)
	}
	splitMain := splitRoute
	splitMain.Binding.ContainerID = "split-main"
	if checkErr := st.ValidateRetainedRoute(ctx, splitMain); !errors.Is(checkErr, ErrNotFound) {
		t.Fatal("retained route on the main container, which doesn't declare the port", checkErr)
	}
	side := gateway
	side.Input.Service, side.Binding.BindingID, side.Binding.InstanceID, side.Binding.ContainerID, side.Binding.ReleaseID = "side", "b_side", "side", "side-container", "side/release"
	wrongContainer := gateway
	wrongContainer.Binding.ContainerID = "web-container"
	notRoutable, missingPort := gateway, gateway
	notRoutable.Input.Port, missingPort.Input.Port = 9090, 7070
	for name, refused := range map[string]Route{"a service container outside main": side, "another container": wrongContainer, "a port missing from routable_ports": notRoutable, "a port no service declares": missingPort} {
		if checkErr := st.ValidateRetainedRoute(ctx, refused); !errors.Is(checkErr, ErrNotFound) {
			t.Fatalf("retained %s: %v", name, checkErr)
		}
	}
	// A port that stops being routable can't be rolled back to, but its route
	// can still be cleaned up.
	exec(`UPDATE loomplane_bindings SET config=config-'routable_ports' WHERE id='b_gw'`)
	if checkErr := st.ValidateRetainedRoute(ctx, gateway); !errors.Is(checkErr, ErrNotFound) {
		t.Fatal("retained route on a port that is no longer routable", checkErr)
	}
	if checkErr := st.ValidateRouteCleanup(ctx, gateway); checkErr != nil {
		t.Fatal("cleanup after the port stopped being routable", checkErr)
	}
	exec(`UPDATE loomplane_bindings SET config=config||'{"routable_ports":[8080]}' WHERE id='b_gw'`)

	for name, want := range map[string]string{"octo--gateway": "cloud_1", "postgres": "cloud_1", "web": "cloud_1", "loose": ""} {
		if cloud, cloudErr := st.RouteBindingCloud(ctx, "org", "shop", "prod", name); cloudErr != nil || cloud != want {
			t.Fatalf("%s Cloud: %q %v", name, cloud, cloudErr)
		}
	}
	for _, missing := range [][4]string{{"other", "shop", "prod", "octo--gateway"}, {"org", "shop", "staging", "octo--gateway"}, {"org", "shop", "prod", "nothing"}} {
		if cloud, cloudErr := st.RouteBindingCloud(ctx, missing[0], missing[1], missing[2], missing[3]); !errors.Is(cloudErr, ErrNotFound) {
			t.Fatalf("%v: %q %v", missing, cloud, cloudErr)
		}
	}
}
