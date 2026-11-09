//go:build integration

package integration

import (
	"testing"

	"github.com/xraph/loomplane/internal/orchestration"
)

func TestManagedProtocolPersistence(t *testing.T) {
	f, svc, plan, result, _ := managedDemandFixture(t)
	if err := svc.PrepareClusterWorkloadDemands(t.Context(), plan, result); err != nil {
		t.Fatal(err)
	}
	saveManagedDemandPlan(t, f, plan, result)
	saved, err := f.plans.GetPlan(t.Context(), "org", plan.ID)
	if err != nil || saved.RequiredRuntimeProtocol != orchestration.ManagedWorkloadProtocol {
		t.Fatalf("protocol lost: %+v %v", saved, err)
	}
	for _, build := range []string{"deployment-v2", "deployment-v3", "deployment-v4"} {
		if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployment_workflow_runs(org_id,plan_id,workflow_id,request) VALUES('org',$1,$2,jsonb_build_object('build_id',$3::text))`, plan.ID, build, build); err == nil {
			t.Fatalf("accepted %s", build)
		}
	}
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployment_workflow_runs(org_id,plan_id,workflow_id,request) VALUES('org',$1,'managed-run','{"build_id":"deployment-v5"}')`, plan.ID); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE loomplane_plans SET result=result-'required_runtime_protocol' WHERE id='managed-plan'`,
		`UPDATE loomplane_deployment_workflow_runs SET request='{"build_id":"deployment-v4"}' WHERE workflow_id='managed-run'`,
	} {
		if _, err := f.sql.Exec(t.Context(), query); err == nil {
			t.Fatal("accepted incompatible rewrite", query)
		}
	}
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployment_workflow_runs(org_id,plan_id,workflow_id,request) VALUES('org','legacy-plan','legacy-run','{"build_id":"deployment-v4"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployment_workflow_participants(org_id,plan_id,workflow_id) VALUES('org',$1,'legacy-run')`, plan.ID); err == nil {
		t.Fatal("attached managed plan to legacy execution")
	}
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_deployment_workflow_participants(org_id,plan_id,workflow_id) VALUES('org',$1,'managed-run')`, plan.ID); err != nil {
		t.Fatal(err)
	}
}
