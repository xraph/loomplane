//go:build integration

package cloudinfra

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/connections"
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
