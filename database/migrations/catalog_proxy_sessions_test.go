//go:build integration

package migrations

import "testing"

func TestCatalogProxySessionsMigration(t *testing.T) {
	conn := scratch(t)
	// Central migrations are not transactional, so the up statements repeat.
	exec(t, conn, catalogProxySessionsUp...)
	exec(t, conn, catalogProxySessionsUp...)
	exec(t, conn, `INSERT INTO loomplane_catalog_proxy_sessions(token_hash,org_id,project_id,environment_id,resource,kind,user_id,created_at,last_used_at,expires_at)
		VALUES('h1','o','p','e','gateway','octopus','u',now(),now(),now()+interval '30 minutes')`)
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_catalog_proxy_sessions(token_hash,org_id,project_id,environment_id,resource,kind,user_id,created_at,last_used_at,expires_at)
		VALUES('h1','o','p','e','gateway','octopus','u',now(),now(),now())`); err == nil {
		t.Fatal("a second session with the same token hash")
	}
	if got := strs(t, conn, `SELECT indexname FROM pg_indexes WHERE tablename = 'loomplane_catalog_proxy_sessions' ORDER BY indexname`); len(got) != 3 {
		t.Fatalf("indexes: %v", got)
	}
	exec(t, conn, catalogProxySessionsDown...)
	exec(t, conn, catalogProxySessionsDown...)
	if got := strs(t, conn, `SELECT coalesce(to_regclass('loomplane_catalog_proxy_sessions')::text,'gone')`); len(got) != 1 || got[0] != "gone" {
		t.Fatalf("down left the table: %v", got)
	}
}
