//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
)

func TestSharedSyncCompensationTwoServicesSameEnvironment(t *testing.T) {
	for _, mutation := range []string{"none", "changed", "deleted", "added", "forged-checkpoint"} {
		t.Run(mutation, func(t *testing.T) {
			f := newForwardFixture(t)
			ctx := t.Context()
			retainUnrelatedForwardProject(t, f)
			host, err := f.store.GetServer(ctx, "org", "cloud", "server-1")
			if err != nil {
				t.Fatal(err)
			}
			host.Health.EffectiveCPUMillis = 3000
			if err = f.store.SaveServer(ctx, host); err != nil {
				t.Fatal(err)
			}
			f.pool.Members[0].CPUMillis = 3000
			if err = f.store.SavePool(ctx, f.pool, f.pool.Revision); err != nil {
				t.Fatal(err)
			}
			req := f.request("env", "extra")
			req.CPUMillis = 500
			req.StickyServerID = "server-1"
			placement, err := f.store.PreviewPlacement(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			pin := f.plan(t, "source-extra", req, placement)
			f.accept(t, "source-extra")
			if _, err = f.sql.Exec(ctx, `INSERT INTO loomplane_deployments(id,org_id,project_id,environment_id,environment,plan_id,commit_sha,status,started_by,started_at) VALUES('source-extra','org','proj','env','production','source-extra','reviewed','succeeded','usr',now())`); err != nil {
				t.Fatal(err)
			}
			bindings, err := f.plans.ListBindings(ctx, "org", "env")
			if err != nil {
				t.Fatal(err)
			}
			source := *bindings[0]
			var cfg planning.ServiceConfig
			if err = json.Unmarshal(source.Config, &cfg); err != nil {
				t.Fatal(err)
			}
			cfg.Service = "extra"
			cfg.Size = &manifest.Size{CPU: 0.5, Memory: 512}
			cfg.Target = placement.ConnectionID
			cfg.CloudPlacement = pin
			cfg.TargetSettings = map[string]string{"docker_id": placement.ConnectionID, "architecture": "amd64"}
			source.ID, source.Name, source.ExternalID, source.PlanID, source.ConnectionID = "binding-extra", "extra", "instance-extra", "source-extra", placement.ConnectionID
			source.Config, err = json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.plans.PutBinding(ctx, &source); err != nil {
				t.Fatal(err)
			}
			actor := security.Principal{OrgID: "org", UserID: "usr"}
			preview := reviewPoolChange(t, f.service)
			original, err := f.service.ApplyCloudChange(ctx, actor, "", preview.ID, preview.Revision, "two-forward")
			if err != nil {
				t.Fatal(err)
			}
			if err = f.service.ReconcileCloudSync(ctx, "org", "", original.ID, nil); err == nil {
				t.Fatal("expected original cleanup refusal")
			}
			saved, _, err := f.clouds.GetSync(ctx, "org", "", original.ID)
			if err != nil || saved.Status != cloudprofiles.SyncCleanup {
				t.Fatalf("original did not finish applying: %v %v", saved, err)
			}
			revert, err := f.service.PreviewCloudRevert(ctx, actor, "", original.ID)
			if err != nil || len(revert.Blockers) > 0 {
				t.Fatalf("preview: %v %v", revert, err)
			}
			compensation, err := f.service.ApplyCloudRevert(ctx, actor, "", revert.ID, revert.Revision, "two-restore")
			if err != nil {
				t.Fatal(err)
			}
			f.executor.failCleanup = false
			if _, err = f.sql.Exec(ctx, `CREATE FUNCTION stop_second_restore() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN IF NEW.name='extra' AND NEW.plan_id=TG_ARGV[0] THEN RAISE EXCEPTION 'restart before second restore'; END IF; RETURN NEW; END $$`); err != nil {
				t.Fatal(err)
			}
			if _, err = f.sql.Exec(ctx, `CREATE TRIGGER stop_second_restore BEFORE UPDATE ON loomplane_bindings FOR EACH ROW EXECUTE FUNCTION stop_second_restore('`+compensation.ID+`.env')`); err != nil {
				t.Fatal(err)
			}
			err = f.service.ReconcileCloudSync(ctx, "org", "", compensation.ID, nil)
			if err == nil || !strings.Contains(err.Error(), "restart before second restore") {
				t.Fatal("missing restart interruption", err)
			}
			var first, second string
			if e := f.sql.QueryRow(ctx, `SELECT external_id FROM loomplane_bindings WHERE environment_id='env' AND name='api'`).Scan(&first); e != nil {
				t.Fatal(e)
			}
			if e := f.sql.QueryRow(ctx, `SELECT external_id FROM loomplane_bindings WHERE environment_id='env' AND name='extra'`).Scan(&second); e != nil {
				t.Fatal(e)
			}
			if first != "instance-env" || second != "candidate-env-extra" {
				t.Fatal("restart did not split restores", first, second, err)
			}
			if _, err = f.sql.Exec(ctx, `DROP TRIGGER stop_second_restore ON loomplane_bindings`); err != nil {
				t.Fatal(err)
			}
			if mutation != "none" {
				statements := map[string]string{
					"changed":           `UPDATE loomplane_bindings SET url='https://unrelated.example.com' WHERE environment_id='env' AND name='api'`,
					"forged-checkpoint": `WITH changed AS (UPDATE loomplane_bindings SET url='https://forged.example.com' WHERE environment_id='env' AND name='api' RETURNING *) UPDATE loomplane_cloud_sync_bindings checkpoint SET after_value=to_jsonb(changed)-'observed_at'-'drift_message' FROM changed WHERE checkpoint.sync_id=split_part(changed.plan_id,'.',1) AND checkpoint.binding_id=changed.id`,
					"deleted":           `DELETE FROM loomplane_bindings WHERE environment_id='env' AND name='api'`,
					"added":             `INSERT INTO loomplane_bindings SELECT (jsonb_populate_record(NULL::loomplane_bindings,to_jsonb(b)||'{"id":"unrelated-extra","name":"unrelated"}'::jsonb)).* FROM loomplane_bindings b WHERE environment_id='env' AND name='api'`,
				}
				if _, err = f.sql.Exec(ctx, statements[mutation]); err != nil {
					t.Fatal(err)
				}
			}
			restarted := f.runner()
			f.service.BindCloudSyncRunner(restarted, restarted)
			_, err = f.service.RetryCloudSync(ctx, actor, "", compensation.ID)
			if err == nil {
				var admitted, executable bool
				if checkErr := f.sql.QueryRow(ctx, `SELECT loomplane_accepted_pool_sync(p),loomplane_compensation_execution(p) FROM loomplane_plans p WHERE org_id='org' AND id=$1`, compensation.ID+".env").Scan(&admitted, &executable); checkErr != nil {
					t.Fatal(checkErr)
				}
				if mutation == "none" && admitted || executable != (mutation == "none") {
					t.Fatal("admission/execution ownership mismatch", admitted, executable)
				}
				err = f.service.ReconcileCloudSync(ctx, "org", "", compensation.ID, nil)
			}
			if mutation != "none" {
				if err == nil {
					t.Fatal("unrelated change accepted")
				}
				var restored string
				if e := f.sql.QueryRow(ctx, `SELECT external_id FROM loomplane_bindings WHERE environment_id='env' AND name='extra'`).Scan(&restored); e != nil {
					t.Fatal(e)
				}
				var guardPlan, reservationPlan string
				if e := f.sql.QueryRow(ctx, `SELECT plan_id FROM loomplane_workload_attempts WHERE org_id='org' AND environment_id='env' AND service='extra'`).Scan(&guardPlan); e != nil {
					t.Fatal(e)
				}
				if e := f.sql.QueryRow(ctx, `SELECT plan_id FROM loomplane_capacity_reservations WHERE org_id='org' AND environment_id='env' AND service='extra' AND server_id='server-1'`).Scan(&reservationPlan); e != nil {
					t.Fatal(e)
				}
				if guardPlan != original.ID+".env" || reservationPlan != "source-extra" {
					t.Fatal("conflict transferred guard or reservation", guardPlan, reservationPlan)
				}
				if restored != "candidate-env-extra" {
					t.Fatal("conflict partially restored sibling", restored)
				}
				return
			}
			if err != nil {
				t.Fatal("restart refused exact sibling after-value", err)
			}
			bindings, readErr := f.plans.ListBindings(ctx, "org", "env")
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, b := range bindings {
				t.Logf("service=%s instance=%s plan=%s", b.Name, b.ExternalID, b.PlanID)
			}
			for _, b := range bindings {
				if strings.HasPrefix(b.ExternalID, "candidate-") || len(b.Replaced) != 0 {
					t.Fatal("restore unfinished", b.Name, b.ExternalID, len(b.Replaced))
				}
			}
			saved, _, err = f.clouds.GetSync(ctx, "org", "", compensation.ID)
			if err != nil || saved.Status != cloudprofiles.SyncSucceeded {
				t.Fatal(saved, err)
			}
		})
	}
}
