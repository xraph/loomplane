//go:build integration

package migrations

import "testing"

func TestAIHuggingFaceTokenMigration(t *testing.T) {
	// Databases record the version and name, so the filename can't drift.
	if m := migration(t, "ai_huggingface_token"); m.Version != "20261126000100" {
		t.Fatalf("version %q", m.Version)
	}
	conn, db := scratch(t)
	up(t, db, "ai_settings")
	exec(t, conn, `INSERT INTO loomplane_ai_settings(org_id) VALUES('before')`)
	// Running the up again changes nothing.
	up(t, db, "ai_huggingface_token", "ai_huggingface_token")

	exec(t, conn, `INSERT INTO loomplane_ai_settings(org_id) VALUES('after')`)
	if got := strs(t, conn, `SELECT org_id||'|['||huggingface_hint||']['||huggingface_account||']|'||coalesce(huggingface_set_at::text,'null')||'|'||huggingface_revision
		FROM loomplane_ai_settings ORDER BY org_id`); len(got) != 2 || got[0] != "after|[][]|null|0" || got[1] != "before|[][]|null|0" {
		t.Fatalf("token defaults: %v", got)
	}
	if got := strs(t, conn, `SELECT data_type FROM information_schema.columns WHERE table_name='loomplane_ai_settings' AND column_name='huggingface_set_at'`); len(got) != 1 || got[0] != "timestamp with time zone" {
		t.Fatalf("huggingface_set_at type: %v", got)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE loomplane_ai_settings SET huggingface_revision=-1`); err == nil {
		t.Fatal("constraint accepted a negative revision")
	}

	down(t, db, "ai_huggingface_token")
	if got := strs(t, conn, `SELECT count(*)||'' FROM information_schema.columns WHERE table_name='loomplane_ai_settings' AND column_name LIKE 'huggingface_%'`); len(got) != 1 || got[0] != "0" {
		t.Fatalf("down left columns: %v", got)
	}
	// The settings rows survive the down, and up again adds the columns back.
	up(t, db, "ai_huggingface_token")
	if got := strs(t, conn, `SELECT count(*)||'' FROM loomplane_ai_settings WHERE huggingface_revision=0`); len(got) != 1 || got[0] != "2" {
		t.Fatalf("rows after down and up: %v", got)
	}
}
