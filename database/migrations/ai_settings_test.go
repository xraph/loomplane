//go:build integration

package migrations

import "testing"

func TestAISettingsMigration(t *testing.T) {
	// Databases record the version and name, so the filename can't drift.
	if m := migration(t, "ai_settings"); m.Version != "20261125000100" {
		t.Fatalf("version %q", m.Version)
	}
	conn, db := scratch(t)
	// Running the up again changes nothing.
	up(t, db, "ai_settings", "ai_settings")

	exec(t, conn, `INSERT INTO loomplane_ai_settings(org_id) VALUES('o')`)
	if got := strs(t, conn, `SELECT monthly_budget_usd::text||'|'||(embeddings->>'mode')||'|'||(embeddings->>'model')||'|'||(embeddings->>'dimensions')||'|'||revision||'|['||updated_by||']'
		FROM loomplane_ai_settings`); len(got) != 1 || got[0] != "0.00|local|granite-embedding-107m-multilingual|384|1|[]" {
		t.Fatalf("settings defaults: %v", got)
	}

	exec(t, conn, `INSERT INTO loomplane_ai_provider_keys(id,org_id,name,type,created_by,updated_by) VALUES('aikey_1','o','anthropic','anthropic','u','u')`)
	if got := strs(t, conn, `SELECT fields::text||'|'||models::text||'|['||key_hint||']|'||cardinality(projects)||'|'||revision FROM loomplane_ai_provider_keys`); len(got) != 1 || got[0] != "{}|[]|[]|0|1" {
		t.Fatalf("provider key defaults: %v", got)
	}

	exec(t, conn, `INSERT INTO loomplane_ai_budget_allocations(org_id,scope,label,monthly_budget_usd) VALUES('o','agent','Agent',12.345)`)
	if got := strs(t, conn, `SELECT monthly_budget_usd::text||'|['||project_id||']['||environment_id||']' FROM loomplane_ai_budget_allocations`); len(got) != 1 || got[0] != "12.35|[][]" {
		t.Fatalf("allocation defaults: %v", got)
	}

	for _, sql := range []string{
		`UPDATE loomplane_ai_settings SET monthly_budget_usd=-1`,
		`INSERT INTO loomplane_ai_provider_keys(id,org_id,name,type,created_by,updated_by) VALUES('aikey_2','o','anthropic','openai','u','u')`, // same org and name
		`INSERT INTO loomplane_ai_budget_allocations(org_id,scope,label,monthly_budget_usd) VALUES('o','gateway:e/ai','shop / staging',0)`,
		`INSERT INTO loomplane_ai_budget_allocations(org_id,scope,label,monthly_budget_usd) VALUES('o','agent','Agent again',1)`, // same scope
	} {
		if _, err := conn.Exec(t.Context(), sql); err == nil {
			t.Fatalf("constraint accepted %s", sql)
		}
	}
	// Another workspace may use the same key name and scope.
	exec(t, conn,
		`INSERT INTO loomplane_ai_provider_keys(id,org_id,name,type,created_by,updated_by) VALUES('aikey_3','p','anthropic','anthropic','u','u')`,
		`INSERT INTO loomplane_ai_budget_allocations(org_id,scope,label,monthly_budget_usd) VALUES('p','agent','Agent',1)`,
	)

	down(t, db, "ai_settings", "ai_settings")
	gone := `SELECT coalesce(to_regclass('loomplane_ai_settings')::text,'gone')||'|'||coalesce(to_regclass('loomplane_ai_provider_keys')::text,'gone')||'|'||coalesce(to_regclass('loomplane_ai_budget_allocations')::text,'gone')`
	if got := strs(t, conn, gone); len(got) != 1 || got[0] != "gone|gone|gone" {
		t.Fatalf("down left tables: %v", got)
	}
	// Up again after a down gives empty tables back.
	up(t, db, "ai_settings")
	if got := strs(t, conn, `SELECT (SELECT count(*) FROM loomplane_ai_settings)+(SELECT count(*) FROM loomplane_ai_provider_keys)+(SELECT count(*) FROM loomplane_ai_budget_allocations)||''`); len(got) != 1 || got[0] != "0" {
		t.Fatalf("rows after down and up: %v", got)
	}
}
