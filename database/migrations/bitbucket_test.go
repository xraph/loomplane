//go:build integration

package migrations

import (
	"strings"
	"testing"
)

func TestBitbucketMigration(t *testing.T) {
	// Databases record the version and name, so the filename can't drift.
	if m := migration(t, "bitbucket"); m.Version != "20261209000200" {
		t.Fatalf("version %q", m.Version)
	}
	conn, db := scratch(t)
	// A rerun changes nothing.
	up(t, db, "bitbucket", "bitbucket")

	// No column holds a token or a secret.
	for _, col := range strs(t, conn, `SELECT column_name FROM information_schema.columns WHERE table_name LIKE 'loomplane_bitbucket_%'`) {
		if strings.Contains(col, "token") || strings.Contains(col, "secret") {
			t.Fatalf("column %q looks like a secret", col)
		}
	}

	exec(t, conn,
		`INSERT INTO loomplane_bitbucket_workspaces(id,org_id,workspace_uuid,workspace_slug,bitbucket_user_uuid,created_by,created_at,updated_at) VALUES('w1','o','{a}','Acme','{u}','u',now(),now())`,
		`INSERT INTO loomplane_bitbucket_workspaces(id,org_id,workspace_uuid,workspace_slug,bitbucket_user_uuid,created_by,created_at,updated_at) VALUES('w2','p','{a}','Acme','{u}','u',now(),now())`,
		`INSERT INTO loomplane_bitbucket_webhooks(hook_id,org_id,connection_id,repository,status,created_at,updated_at) VALUES('h1','o','w1','bitbucket.org/ACME/api','off',now(),now())`,
		`INSERT INTO loomplane_bitbucket_webhooks(hook_id,org_id,connection_id,repository,status,created_at,updated_at) VALUES('h2','p','w2','bitbucket.org/ACME/api','on',now(),now())`,
	)
	for _, sql := range []string{
		`INSERT INTO loomplane_bitbucket_workspaces(id,org_id,workspace_uuid,workspace_slug,bitbucket_user_uuid,created_by,created_at,updated_at) VALUES('w3','o','{a}','other','{u}','u',now(),now())`,
		`INSERT INTO loomplane_bitbucket_workspaces(id,org_id,workspace_uuid,workspace_slug,bitbucket_user_uuid,status,created_by,created_at,updated_at) VALUES('w3','o','{b}','b','{u}','gone','u',now(),now())`,
		`INSERT INTO loomplane_bitbucket_webhooks(hook_id,org_id,connection_id,repository,status,created_at,updated_at) VALUES('h3','o','w1','bitbucket.org/acme/API','on',now(),now())`,
		`INSERT INTO loomplane_bitbucket_webhooks(hook_id,org_id,connection_id,repository,status,created_at,updated_at) VALUES('h3','o','w1','bitbucket.org/acme/web','paused',now(),now())`,
	} {
		if _, err := conn.Exec(t.Context(), sql); err == nil {
			t.Fatalf("constraint accepted %s", sql)
		}
	}

	down(t, db, "bitbucket")
	if got := strs(t, conn, `SELECT table_name FROM information_schema.tables WHERE table_name LIKE 'loomplane_bitbucket_%'`); len(got) != 0 {
		t.Fatalf("down left %v", got)
	}
	up(t, db, "bitbucket")
}
