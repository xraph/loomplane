//go:build integration

package cloudinfra

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestEdgeMigrationAndAcceptance(t *testing.T) {
	pg := routeBindingDatabase(t)
	ctx := t.Context()
	st := NewPgStore(pg)
	for _, q := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('shop','org','Shop','shop','ready','usr',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('prod','org','shop','production','production',now(),now())`,
		`INSERT INTO loomplane_dns_zones(id,org_id,name,connection_id,revision,body) VALUES('zone','org','example.com','',1,'{}')`,
	} {
		if _, err := pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := connections.NewPgStore(pg).CreateConnection(ctx, &connections.Connection{ID: "cluster", OrgID: "org", Cloud: "kubernetes", Name: "Cluster", Status: connections.StatusReady, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	route := Route{ID: "route", OrgID: "org", ProjectID: "shop", Status: "ready", Revision: 1, Input: RouteInput{EnvironmentID: "prod", Service: "web", Hostname: "app.example.com", ZoneID: "zone"}, Binding: RouteBinding{Backend: "kubernetes", ConnectionID: "cluster"}}
	raw, err := json.Marshal(route)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.Exec(ctx, `INSERT INTO loomplane_domain_routes(id,org_id,project_id,environment_id,cloud_id,server_id,connection_id,zone_id,hostname,status,revision,body,backend) VALUES('route','org','shop','prod','','','cluster','zone','app.example.com','ready',1,$1,'kubernetes')`, raw); err != nil {
		t.Fatal(err)
	}
	state := &EdgeState{RouteID: "route", OrgID: "org", ProjectID: "shop", Status: "pending", Kind: "configure", Policy: provider.EdgePolicy{Enabled: true}, Principal: security.Principal{OrgID: "org", UserID: "user"}}
	if err = st.AcceptEdge(ctx, &route, state); err != nil {
		t.Fatal(err)
	}
	if state.Revision != 1 {
		t.Fatal(state.Revision)
	}
	loaded, err := st.Edge(ctx, "org", "route")
	if err != nil || loaded.Principal.UserID != "user" {
		t.Fatal(loaded, err)
	}
	if _, err = st.Edge(ctx, "other", "route"); err == nil {
		t.Fatal("cross-workspace read")
	}
	op := &RouteOperation{Route: route, Kind: "remove"}
	if err = st.AcceptRoute(ctx, op); err == nil {
		t.Fatal("route changed during CDN operation")
	}
	route.Receipt = &provider.DNSRecord{Proxied: true}
	state.Status = "ready"
	state.LastPurgeAt = time.Now().UTC()
	if err = st.PublishEdge(ctx, &route, state); err != nil {
		t.Fatal(err)
	}
	stored, err := st.Route(ctx, "org", "shop", "route")
	if err != nil || !stored.Receipt.Proxied || stored.Revision != 2 {
		t.Fatal(stored, err)
	}
	stale := *state
	stale.Revision = 0
	if err = st.AcceptEdge(ctx, &route, &stale); err == nil {
		t.Fatal("stale state overwritten")
	}
}

func TestEdgeInvalidationOutboxScopesSuccessfulServices(t *testing.T) {
	pg := overviewDatabase(t)
	ctx := t.Context()
	st := NewPgStore(pg)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pg.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE loomplane_route_edge(org_id text,route_id text,project_id text,revision bigint,body jsonb,principal jsonb,PRIMARY KEY(org_id,route_id));
 CREATE TABLE loomplane_edge_invalidations(org_id text,route_id text,project_id text,deployment_id text,done boolean DEFAULT false,attempts int DEFAULT 0,next_attempt timestamptz DEFAULT now(),message text DEFAULT '',PRIMARY KEY(org_id,route_id,deployment_id));
 CREATE TABLE loomplane_deployments(id text,org_id text,project_id text,environment_id text,status text,finished_at timestamptz,steps jsonb)`)
	raw, _ := json.Marshal(Route{ID: "r", OrgID: "org", ProjectID: "shop", Input: RouteInput{EnvironmentID: "prod", Service: "web"}})
	exec(`INSERT INTO loomplane_domain_routes VALUES('r','org','shop','prod','','','','','app.example.com','ready',1,$1)`, raw)
	state := EdgeState{RouteID: "r", OrgID: "org", ProjectID: "shop", Status: "failed", Policy: provider.EdgePolicy{Enabled: true, InvalidateOnDeploy: true}, AppliedAt: time.Now().Add(-time.Minute)}
	raw, _ = json.Marshal(state)
	exec(`INSERT INTO loomplane_route_edge VALUES('org','r','shop',1,$1,'{}')`, raw)
	for _, row := range []struct{ id, org, project, env, status, name string }{{"ok", "org", "shop", "prod", "succeeded", "web"}, {"failed", "org", "shop", "prod", "failed", "web"}, {"other_org", "other", "shop", "prod", "succeeded", "web"}, {"other_project", "org", "other", "prod", "succeeded", "web"}, {"other_env", "org", "shop", "dev", "succeeded", "web"}, {"other_service", "org", "shop", "prod", "succeeded", "api"}, {"running", "org", "shop", "prod", "running", "web"}} {
		step, _ := json.Marshal([]map[string]string{{"name": row.name, "subject": "service", "status": "succeeded", "op": "update"}})
		exec(`INSERT INTO loomplane_deployments VALUES($1,$2,$3,$4,$5,now(),$6)`, row.id, row.org, row.project, row.env, row.status, step)
	}
	if err := st.QueueEdgeInvalidations(ctx); err != nil {
		t.Fatal(err)
	}
	if jobs, err := st.EdgeInvalidations(ctx); err != nil || len(jobs) != 0 {
		t.Fatal("failed CDN configuration queued deployments", jobs, err)
	}
	exec(`UPDATE loomplane_route_edge SET body=jsonb_set(body,'{status}','"ready"')`)
	for range 2 {
		if err := st.QueueEdgeInvalidations(ctx); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := st.EdgeInvalidations(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].Deployment != "ok" {
		t.Fatal(jobs, err)
	}
	if err = st.FinishEdgeInvalidation(ctx, jobs[0], "rate limited"); err != nil {
		t.Fatal(err)
	}
	if due, readErr := st.EdgeInvalidations(ctx); readErr != nil || len(due) != 0 {
		t.Fatal("retry did not back off", due, readErr)
	}
	exec(`UPDATE loomplane_edge_invalidations SET next_attempt=now()`)
	jobs, err = st.EdgeInvalidations(ctx)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	if err = st.FinishEdgeInvalidation(ctx, jobs[0], ""); err != nil {
		t.Fatal(err)
	}
	if err = st.QueueEdgeInvalidations(ctx); err != nil {
		t.Fatal(err)
	}
	if jobs, err = st.EdgeInvalidations(ctx); err != nil || len(jobs) != 0 {
		t.Fatal("acknowledged purge replayed", jobs, err)
	}
}
