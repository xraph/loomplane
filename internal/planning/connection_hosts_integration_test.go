//go:build integration

package planning

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
)

// ConnectionHosts is one read for pooling and Cloud servers, against the
// real schema. A pool member reached through a shared daemon pools a
// connection, but only a live server in the connection's organization
// whose runtime connection it is counts as its Cloud server.
func TestPgStore_connectionHosts(t *testing.T) {
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the PostgreSQL connection hosts test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("connection_hosts_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatal("LOOMPLANE_TEST_DATABASE_URL must be a PostgreSQL URL")
	}
	u.Path = "/" + name
	if err = ops.Migrate(ctx, u.String(), migrations.All()); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	connection := func(id, org, cloud, details string) string {
		return fmt.Sprintf(`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,details,status,created_by,created_at,updated_at) VALUES('%s','%s','%s','%s','%s','%s','ready','usr',now(),now())`, id, org, cloud, id, id, details)
	}
	server := func(id, org, cloud, runtime, status, body string) string {
		return fmt.Sprintf(`INSERT INTO loomplane_cloud_servers(id,org_id,cloud_id,connection_id,runtime_connection_id,provider,account_id,ownership,revision,status,body) VALUES('%s','%s','%s','provider-%s','%s','hetzner','account','managed',1,'%s','%s')`, id, org, cloud, org, runtime, status, body)
	}
	for _, stmt := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('proj','org','One','one','ready','usr',now(),now()),('proj-2','org-2','Two','two','ready','usr',now(),now())`,
		`INSERT INTO loomplane_cloud_profiles(id,org_id,project_id,name,revision,created_at,updated_at) VALUES('cloud_1','org','proj','One',1,now(),now()),('cloud_2','org','proj','Two',1,now(),now()),('cloud_9','org-2','proj-2','Nine',1,now(),now())`,
		connection("provider-org", "org", "hetzner", `{}`),
		connection("provider-org-2", "org-2", "hetzner", `{}`),
		connection("direct", "org", "server", `{}`),
		connection("elsewhere", "org", "server", `{}`),
		connection("shared", "org", "server", `{"docker_id":"daemon"}`),
		connection("pool-runtime", "org", "server", `{}`),
		connection("gone", "org", "server", `{}`),
		connection("cluster", "org", "kubernetes", `{}`),
		server("server_1", "org", "cloud_1", "direct", "ready", `{}`),
		server("server_2", "org", "cloud_1", "pool-runtime", "ready", `{"health":{"docker_id":"daemon"}}`),
		server("server_3", "org", "cloud_2", "elsewhere", "ready", `{}`),
		server("server_5", "org", "cloud_1", "gone", "deleted", `{}`),
		`INSERT INTO loomplane_server_pools(id,org_id,cloud_id,revision,body) VALUES('pool','org','cloud_1',1,'{}')`,
		`INSERT INTO loomplane_server_pool_members(org_id,pool_id,server_id,cpu_millis,memory_bytes) VALUES('org','pool','server_2',1000,536870912)`,
		// The connection-uses foreign key stops another organization's
		// server naming this one's connection. With its trigger set aside,
		// the row tests the read's own organization check.
		`ALTER TABLE loomplane_cloud_servers DISABLE TRIGGER loomplane_server_connection_uses`,
		server("server_6", "org-2", "cloud_9", "direct", "ready", `{}`),
		`ALTER TABLE loomplane_cloud_servers ENABLE TRIGGER loomplane_server_connection_uses`,
	} {
		if _, err = pg.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	store := NewPgStore(pg)
	hosts, err := store.ConnectionHosts(ctx, "org")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]ConnectionHost{
		"direct":       {Servers: []CloudServer{{ID: "server_1", CloudID: "cloud_1"}}},
		"elsewhere":    {Servers: []CloudServer{{ID: "server_3", CloudID: "cloud_2"}}},
		"shared":       {Pooled: true},
		"pool-runtime": {Pooled: true, Servers: []CloudServer{{ID: "server_2", CloudID: "cloud_1"}}},
	}
	if !reflect.DeepEqual(hosts, want) {
		t.Fatalf("hosts: %+v", hosts)
	}
	pooled, err := store.PooledConnections(ctx, "org")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pooled, map[string]bool{"shared": true, "pool-runtime": true}) {
		t.Fatalf("pooled: %+v", pooled)
	}
	// A second live server on one runtime connection is what would make a
	// placement ambiguous; the schema refuses it.
	if _, err = pg.Exec(ctx, server("server_7", "org", "cloud_1", "direct", "ready", `{}`)); err == nil {
		t.Fatal("two live servers share a runtime connection")
	}
}
