//go:build integration

package migrations

import (
	"slices"
	"testing"
)

// Endpoints Loomplane made before the model budget events existed get
// them, once; others are left alone, and a database without Relay's table
// yet is fine.
func TestAIModelBudgetWebhooksMigration(t *testing.T) {
	if m := migration(t, "ai_model_budget_webhooks"); m.Version != "20261126000300" {
		t.Fatalf("version %q", m.Version)
	}
	conn, db := scratch(t)
	up(t, db, "ai_model_budget_webhooks")
	exec(t, conn,
		`CREATE TABLE relay_endpoints (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, url TEXT NOT NULL, event_types TEXT[] NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`INSERT INTO relay_endpoints (id, tenant_id, url, event_types) VALUES
			('ep_old', 'org_1', 'https://example.com/a', ARRAY['deployment.succeeded','deployment.failed','connection.sign_in_needed','connection.keyless_refused']),
			('ep_half', 'org_1', 'https://example.com/b', ARRAY['deployment.succeeded','deployment.failed','model.budget_crossed']),
			('ep_other', 'org_1', 'https://example.com/c', ARRAY['invoice.*'])`)
	up(t, db, "ai_model_budget_webhooks", "ai_model_budget_webhooks")
	want := []string{
		"ep_half deployment.succeeded,deployment.failed,model.budget_crossed,model.budget_projected",
		"ep_old deployment.succeeded,deployment.failed,connection.sign_in_needed,connection.keyless_refused,model.budget_projected,model.budget_crossed",
		"ep_other invoice.*",
	}
	if got := strs(t, conn, `SELECT id||' '||array_to_string(event_types, ',') FROM relay_endpoints ORDER BY id`); !slices.Equal(got, want) {
		t.Fatalf("after up:\n%v\nwant\n%v", got, want)
	}
	down(t, db, "ai_model_budget_webhooks")
	if got := strs(t, conn, `SELECT id||' '||array_to_string(event_types, ',') FROM relay_endpoints ORDER BY id`); !slices.Equal(got, []string{
		"ep_half deployment.succeeded,deployment.failed",
		"ep_old deployment.succeeded,deployment.failed,connection.sign_in_needed,connection.keyless_refused",
		"ep_other invoice.*",
	}) {
		t.Fatalf("after down: %v", got)
	}
}
