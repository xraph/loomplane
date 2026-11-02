//go:build integration

package migrations

import (
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestNodeEventsIgnoreHeartbeats(t *testing.T) {
	conn, ex := scratch(t)
	if _, err := migrate.NewOrchestrator(ex, Group()).Migrate(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}
	up(t, ex, "nodes", "node_events")

	exec(t, conn, `INSERT INTO loomplane_nodes (id,org_id,name,public_key,key_fingerprint,created_at,updated_at) VALUES ('node_1','org','web-1','\x01','fp',now(),now())`)
	count := func() string {
		return strs(t, conn, `SELECT count(*)::text FROM loomplane_events WHERE type='node.updated'`)[0]
	}
	if got := count(); got != "1" {
		t.Fatalf("insert events: %s", got)
	}
	for range 10 {
		exec(t, conn, `UPDATE loomplane_nodes SET last_seen_at=now(), usage='{"cpu_percent":3}', updated_at=now() WHERE id='node_1'`)
	}
	if got := count(); got != "1" {
		t.Fatalf("heartbeats emitted events: %s", got)
	}
	exec(t, conn, `UPDATE loomplane_nodes SET status='online' WHERE id='node_1'`)
	if got := count(); got != "2" {
		t.Fatalf("status change events: %s", got)
	}
	if got := strs(t, conn, `SELECT data->>'status' FROM loomplane_events WHERE type='node.updated' ORDER BY sequence DESC LIMIT 1`); got[0] != "online" {
		t.Fatalf("payload: %v", got)
	}

	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_nodes (id,org_id,name,public_key,key_fingerprint,role,edge_scope,created_at,updated_at) VALUES ('node_2','org','web-2','\x01','fp','node','org',now(),now())`); err == nil {
		t.Fatal("edge scope accepted on a plain node")
	}
	down(t, ex, "node_events")
	if got := strs(t, conn, `SELECT count(*)::text FROM pg_trigger WHERE tgname='loomplane_node_event'`); got[0] != "0" {
		t.Fatalf("down left the trigger: %v", got)
	}
	if err := migration(t, "nodes").Down(t.Context(), ex); err == nil {
		t.Fatal("down dropped tables that hold nodes")
	}
}

func TestNodeNoncesAreSingleUse(t *testing.T) {
	conn, ex := scratch(t)
	if _, err := migrate.NewOrchestrator(ex, Group()).Migrate(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}
	up(t, ex, "node_nonces")
	exec(t, conn, `INSERT INTO loomplane_node_nonces (nonce,expires_at) VALUES ('n1',now())`)
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_node_nonces (nonce,expires_at) VALUES ('n1',now())`); err == nil {
		t.Fatal("a nonce was stored twice")
	}
	down(t, ex, "node_nonces")
	if got := strs(t, conn, `SELECT count(*)::text FROM pg_tables WHERE tablename='loomplane_node_nonces'`); got[0] != "0" {
		t.Fatalf("down left the table: %v", got)
	}
}
