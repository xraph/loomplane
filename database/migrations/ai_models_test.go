//go:build integration

package migrations

import (
	"slices"
	"strings"
	"testing"
)

func TestAIModelsMigration(t *testing.T) {
	// Databases record the version and name, so the filename can't drift.
	if m := migration(t, "ai_models"); m.Version != "20261126000200" {
		t.Fatalf("version %q", m.Version)
	}
	conn, db := scratch(t)
	// Running the up again changes nothing.
	up(t, db, "ai_models", "ai_models")

	exec(t, conn, `INSERT INTO loomplane_ai_model_deployments(id, org_id, project_id, environment_id, resource, entry_id, quantization, engine, phase)
		VALUES('aimodel_1', 'org_a', 'proj_1', 'env_1', 'llama', 'meta-llama/Llama-3.1-8B-Instruct', 'bf16', 'vllm', 'planned')`)
	got := strs(t, conn, `SELECT coalesce(monthly_budget_usd::text, 'null')||'|'||coalesce(hourly_usd_override::text, 'null')||'|'||server_cloud_id||'|'||server_operation_id||'|'||server_name||'|'||plan_id||'|'||message
		||'|'||coalesce(request::text, 'null')||'|'||coalesce(requested_by::text, 'null')||'|'||month||'|'||month_cost_usd||'|'||coalesce(accrued_at::text, 'null')
		||'|'||alerted_projected||'|'||alerted_crossed||'|'||revision FROM loomplane_ai_model_deployments`)
	if len(got) != 1 || got[0] != "null|null||||||null|null||0.0000|null|||1" {
		t.Fatalf("defaults: %v", got)
	}

	// Under M1 a deployment has no sharing or alias of its own.
	cols := strs(t, conn, `SELECT column_name||' '||data_type FROM information_schema.columns
		WHERE table_name='loomplane_ai_model_deployments' ORDER BY column_name`)
	for _, want := range []string{"accrued_at timestamp with time zone", "request jsonb", "requested_by jsonb", "month_cost_usd numeric", "server_name text"} {
		if !slices.Contains(cols, want) {
			t.Errorf("no column %q in %v", want, cols)
		}
	}
	for _, col := range cols {
		if name, _, _ := strings.Cut(col, " "); name == "sharing" || name == "alias" || strings.Contains(name, "key") || strings.Contains(name, "token") {
			t.Errorf("column %q shouldn't exist", name)
		}
	}

	for name, stmt := range map[string]string{
		"a duplicate resource": `INSERT INTO loomplane_ai_model_deployments(id, org_id, project_id, environment_id, resource, entry_id, quantization, engine, phase)
			VALUES('aimodel_2', 'org_a', 'proj_1', 'env_1', 'llama', 'e', 'q', 'vllm', 'planned')`,
		"an unknown phase":    `UPDATE loomplane_ai_model_deployments SET phase='running'`,
		"a zero budget":       `UPDATE loomplane_ai_model_deployments SET monthly_budget_usd=0`,
		"a negative override": `UPDATE loomplane_ai_model_deployments SET hourly_usd_override=-0.01`,
	} {
		if _, err := conn.Exec(t.Context(), stmt); err == nil {
			t.Errorf("the table accepted %s", name)
		}
	}
	// The same resource in another environment, or another org, is its own.
	exec(t, conn,
		`INSERT INTO loomplane_ai_model_deployments(id, org_id, project_id, environment_id, resource, entry_id, quantization, engine, phase)
			VALUES('aimodel_3', 'org_a', 'proj_1', 'env_2', 'llama', 'e', 'q', 'vllm', 'waiting_for_server')`,
		`INSERT INTO loomplane_ai_model_deployments(id, org_id, project_id, environment_id, resource, entry_id, quantization, engine, phase)
			VALUES('aimodel_4', 'org_b', 'proj_1', 'env_1', 'llama', 'e', 'q', 'vllm', 'active')`)

	indexes := strs(t, conn, `SELECT indexname||' '||indexdef FROM pg_indexes WHERE tablename='loomplane_ai_model_deployments' ORDER BY indexname`)
	for _, want := range []string{
		"loomplane_ai_model_deployments_project CREATE INDEX loomplane_ai_model_deployments_project ON public.loomplane_ai_model_deployments USING btree (org_id, project_id)",
		"loomplane_ai_model_deployments_waiting CREATE INDEX loomplane_ai_model_deployments_waiting ON public.loomplane_ai_model_deployments USING btree (phase) WHERE (phase = 'waiting_for_server'::text)",
	} {
		if !slices.Contains(indexes, want) {
			t.Errorf("no index %q in %v", want, indexes)
		}
	}

	exec(t, conn, `INSERT INTO loomplane_ai_custom_models(org_id, entry_id, entry) VALUES('org_a', 'hf:acme/tiny@0123456789ab', '{"id":"hf:acme/tiny@0123456789ab"}')`,
		`INSERT INTO loomplane_ai_custom_models(org_id, entry_id, entry) VALUES('org_b', 'hf:acme/tiny@0123456789ab', '{}')`)
	if _, err := conn.Exec(t.Context(), `INSERT INTO loomplane_ai_custom_models(org_id, entry_id, entry) VALUES('org_a', 'hf:acme/tiny@0123456789ab', '{}')`); err == nil {
		t.Error("the custom models table took the same entry twice for one org")
	}
	if got := strs(t, conn, `SELECT data_type FROM information_schema.columns WHERE table_name='loomplane_ai_custom_models' AND column_name='entry'`); len(got) != 1 || got[0] != "jsonb" {
		t.Errorf("custom entry type: %v", got)
	}

	down(t, db, "ai_models")
	if got := strs(t, conn, `SELECT count(*)||'' FROM information_schema.tables WHERE table_name IN ('loomplane_ai_model_deployments', 'loomplane_ai_custom_models')`); len(got) != 1 || got[0] != "0" {
		t.Fatalf("down left tables: %v", got)
	}
	up(t, db, "ai_models")
}
