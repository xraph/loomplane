//go:build integration

package migrations

import (
	"strings"
	"testing"

	"github.com/xraph/grove/migrate"
)

func TestAISettingsAgentColumns(t *testing.T) {
	// Databases record the version and name, so the filename can't drift.
	if m := migration(t, "ai_settings_agent"); m.Version != "20261207000100" {
		t.Fatalf("version %q", m.Version)
	}
	conn, ex := scratch(t)
	if _, err := migrate.NewOrchestrator(ex, All()...).Migrate(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}
	// Running the up again changes nothing.
	up(t, ex, "ai_settings_agent")

	cols := strs(t, conn, `SELECT column_name||' '||data_type||' '||is_nullable||' '||coalesce(column_default,'')
		FROM information_schema.columns
		WHERE table_name='loomplane_ai_settings' AND (column_name LIKE 'agent_%' OR column_name='nexus_tenant_id')
		ORDER BY column_name`)
	want := []string{
		"agent_enabled boolean NO false",
		"agent_knowledge boolean NO false",
		"agent_model text NO ''::text",
		"agent_provider_key text NO ''::text",
		"nexus_tenant_id text NO ''::text",
	}
	if strings.Join(cols, "\n") != strings.Join(want, "\n") {
		t.Fatalf("columns:\n%s\nwant\n%s", strings.Join(cols, "\n"), strings.Join(want, "\n"))
	}

	// A workspace row written before the agent existed reads as the agent
	// off, on the server's key, with no tenant.
	exec(t, conn, `INSERT INTO loomplane_ai_settings (org_id) VALUES ('org_a'), ('org_b'), ('org_c')`)
	if got := strs(t, conn, `SELECT agent_enabled||'|'||agent_provider_key||'|'||agent_model||'|'||agent_knowledge||'|'||nexus_tenant_id FROM loomplane_ai_settings WHERE org_id='org_a'`); got[0] != "false|||false|" {
		t.Fatalf("defaults: %v", got)
	}

	// Two workspaces never share a nexus tenant; any number have none.
	exec(t, conn, `UPDATE loomplane_ai_settings SET nexus_tenant_id='tenant_1' WHERE org_id='org_a'`)
	if _, err := conn.Exec(t.Context(), `UPDATE loomplane_ai_settings SET nexus_tenant_id='tenant_1' WHERE org_id='org_b'`); err == nil {
		t.Fatal("two workspaces share a nexus tenant")
	}
	if got := strs(t, conn, `SELECT count(*)::text FROM loomplane_ai_settings WHERE nexus_tenant_id=''`); got[0] != "2" {
		t.Fatalf("workspaces without a tenant: %v", got)
	}

	// The down drops the columns and the index, and the up brings them back.
	down(t, ex, "ai_settings_agent")
	if got := strs(t, conn, `SELECT count(*)::text FROM information_schema.columns WHERE table_name='loomplane_ai_settings' AND (column_name LIKE 'agent_%' OR column_name='nexus_tenant_id')`); got[0] != "0" {
		t.Fatalf("columns after down: %v", got)
	}
	if got := strs(t, conn, `SELECT coalesce(to_regclass('loomplane_ai_settings_nexus_tenant')::text,'gone')`); got[0] != "gone" {
		t.Fatalf("index after down: %v", got)
	}
	up(t, ex, "ai_settings_agent")
}
