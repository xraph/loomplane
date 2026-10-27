//go:build integration

package cloudinfra

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
)

func overviewDatabase(t *testing.T) *pgdriver.PgDB {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the domain overview queries")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("domain_overview_%d", time.Now().UnixNano())
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
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	for _, stmt := range []string{
		`CREATE TABLE loomplane_projects(id TEXT PRIMARY KEY, org_id TEXT NOT NULL, name TEXT NOT NULL)`,
		`CREATE TABLE loomplane_cloud_servers(id TEXT PRIMARY KEY, org_id TEXT NOT NULL, body JSONB NOT NULL)`,
		`CREATE TABLE loomplane_cloud_connections(id TEXT PRIMARY KEY, org_id TEXT NOT NULL, name TEXT NOT NULL)`,
		`CREATE TABLE loomplane_domain_routes(id TEXT PRIMARY KEY, org_id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL,
 cloud_id TEXT NOT NULL, server_id TEXT NOT NULL, connection_id TEXT NOT NULL, zone_id TEXT NOT NULL,
 hostname TEXT NOT NULL, status TEXT NOT NULL, revision BIGINT NOT NULL, body JSONB NOT NULL)`,
		`CREATE TABLE loomplane_route_operations(id TEXT PRIMARY KEY, org_id TEXT NOT NULL, project_id TEXT NOT NULL, route_id TEXT NOT NULL, server_id TEXT NOT NULL,
 cloud_id TEXT NOT NULL, status TEXT NOT NULL, revision BIGINT NOT NULL, generation BIGINT NOT NULL DEFAULT 0,
 body JSONB NOT NULL, principal JSONB NOT NULL, checkpoint JSONB NOT NULL DEFAULT '{}')`,
	} {
		if _, err = pg.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	return pg
}

func TestDomainOverviewQueries(t *testing.T) {
	pg := overviewDatabase(t)
	ctx := t.Context()
	st := NewPgStore(pg)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pg.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO loomplane_projects VALUES('shop','org','Shop'),('rival','other','Rival')`)
	exec(`INSERT INTO loomplane_cloud_servers VALUES('srv_1','org','{"name":"web-1"}'),('srv_x','other','{"name":"theirs"}')`)
	exec(`INSERT INTO loomplane_cloud_connections VALUES('conn_k8s','org','prod-cluster'),('conn_x','other','theirs')`)
	route := func(org, id, hostname, service, status string) {
		raw, _ := json.Marshal(Route{ID: id, OrgID: org, ProjectID: "shop", Status: status, Input: RouteInput{ZoneID: "zone", EnvironmentID: "prod", Service: service, Hostname: hostname}})
		exec(`INSERT INTO loomplane_domain_routes VALUES($1,$2,'shop','prod','cloud','srv_1','conn','zone',$3,$4,1,$5)`, id, org, hostname, status, raw)
	}
	route("org", "route_a", "api.acme.dev", "octo--gateway", "ready")
	route("org", "route_w", "*.acme.dev", "octo--gateway", "waiting")
	route("org", "route_old", "old.acme.dev", "octo--gateway", "removed")
	route("org", "route_web", "web.acme.dev", "web", "ready")
	route("other", "route_x", "api.other.dev", "octo--gateway", "ready")
	base := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	op := func(id, routeID string, created time.Time) {
		raw, _ := json.Marshal(RouteOperation{ID: id, CreatedAt: created, Kind: "verify", Status: "succeeded", Route: Route{ID: routeID, OrgID: "org"}})
		exec(`INSERT INTO loomplane_route_operations(id,org_id,project_id,route_id,server_id,cloud_id,status,revision,body,principal) VALUES($1,'org','shop',$2,'srv_1','cloud','succeeded',1,$3,'{}')`, id, routeID, raw)
	}
	// As text, 12:00:00Z sorts after 12:00:00.5Z.
	op("op_newest", "route_a", base.Add(500*time.Millisecond))
	op("op_older", "route_a", base)
	op("op_w", "route_w", base)

	routes, err := st.OrgRoutes(ctx, "org")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(routes))
	for _, r := range routes {
		ids = append(ids, r.ID)
	}
	if !slices.Equal(ids, []string{"route_w", "route_a", "route_web"}) {
		t.Fatal("org routes", ids)
	}
	gateway, err := st.ServiceRoutes(ctx, "org", "shop", "prod", "octo--gateway")
	if err != nil || len(gateway) != 2 || gateway[0].ID != "route_w" || gateway[1].ID != "route_a" {
		t.Fatal("service routes", gateway, err)
	}
	latest, err := st.LatestRouteOperations(ctx, "org", []string{"route_a", "route_w", "route_web"})
	if err != nil || len(latest) != 2 || latest["route_a"].ID != "op_newest" || latest["route_w"].ID != "op_w" {
		t.Fatal("latest operations", latest, err)
	}
	if none, noneErr := st.LatestRouteOperations(ctx, "org", nil); noneErr != nil || len(none) != 0 {
		t.Fatal("no routes", none, noneErr)
	}
	projects, err := st.ProjectNames(ctx, "org")
	if err != nil || len(projects) != 1 || projects["shop"] != "Shop" {
		t.Fatal("project names", projects, err)
	}
	runtimes, err := st.RuntimeNames(ctx, "org")
	if err != nil || len(runtimes) != 2 || runtimes["srv_1"] != "web-1" || runtimes["conn_k8s"] != "prod-cluster" {
		t.Fatal("runtime names", runtimes, err)
	}
}
