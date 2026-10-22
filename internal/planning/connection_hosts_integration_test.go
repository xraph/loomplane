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
)

// ConnectionHosts reads one read for pooling and Cloud servers: a pool
// member reached through a shared daemon pools a connection, but only a
// server whose runtime connection it is counts as its Cloud server.
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
	u.Path = "/" + name
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	for _, stmt := range []string{
		`CREATE TABLE loomplane_cloud_connections (id text PRIMARY KEY, org_id text NOT NULL, cloud text NOT NULL, details jsonb NOT NULL DEFAULT '{}')`,
		`CREATE TABLE loomplane_cloud_servers (id text PRIMARY KEY, org_id text NOT NULL, cloud_id text NOT NULL, runtime_connection_id text, status text NOT NULL, body jsonb NOT NULL DEFAULT '{}')`,
		`CREATE TABLE loomplane_server_pool_members (pool_id text NOT NULL, server_id text NOT NULL)`,
		`INSERT INTO loomplane_cloud_connections VALUES
			('direct','org','server','{}'),
			('shared','org','server','{"docker_id":"daemon"}'),
			('twice','org','server','{}'),
			('gone','org','server','{}'),
			('cluster','org','kubernetes','{}'),
			('other','org-2','server','{}')`,
		`INSERT INTO loomplane_cloud_servers VALUES
			('server_1','org','cloud_1','direct','ready','{}'),
			('server_2','org','cloud_1','pool-runtime','ready','{"health":{"docker_id":"daemon"}}'),
			('server_3','org','cloud_1','twice','ready','{}'),
			('server_4','org','cloud_2','twice','provisioning','{}'),
			('server_5','org','cloud_1','gone','deleted','{}'),
			('server_6','org-2','cloud_9','other','ready','{}')`,
		`INSERT INTO loomplane_server_pool_members VALUES ('pool','server_2')`,
	} {
		if _, err = pg.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	store := NewPgStore(pg)
	hosts, err := store.ConnectionHosts(ctx, "org")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]ConnectionHost{
		"direct": {Servers: []CloudServer{{ID: "server_1", CloudID: "cloud_1"}}},
		"shared": {Pooled: true},
		"twice":  {Servers: []CloudServer{{ID: "server_3", CloudID: "cloud_1"}, {ID: "server_4", CloudID: "cloud_2"}}},
	}
	if !reflect.DeepEqual(hosts, want) {
		t.Fatalf("hosts: %+v", hosts)
	}
	pooled, err := store.PooledConnections(ctx, "org")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pooled, map[string]bool{"shared": true}) {
		t.Fatalf("pooled: %+v", pooled)
	}
}
