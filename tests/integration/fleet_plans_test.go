//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
)

func TestInstancePlanPersistsPrivateInput(t *testing.T) {
	f := newFleetAuthorityFixture(t)
	input := f.input("a")
	input.ReleaseID = f.request.ReleaseID
	reserved, err := f.store.ReserveInstance(t.Context(), f.scope, input)
	if err != nil {
		t.Fatal(err)
	}
	release, err := f.store.GetRelease(t.Context(), f.scope, input.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fleets.RenderInstance(*release, reserved.Instance, input)
	if err != nil {
		t.Fatal(err)
	}
	f.request.SnapshotDigest = snapshot.Digest
	authority, err := f.service.AuthorizeOperation(t.Context(), f.principal, f.scope, f.request)
	if err != nil {
		t.Fatal(err)
	}
	env, err := f.projects.GetEnvironment(t.Context(), "org", "proj", reserved.Instance.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	store := planning.NewPgStore(f.db)
	plan := &planning.Plan{ID: "fleet-plan", OrgID: "org", ProjectID: "proj", EnvironmentID: env.ID, Environment: env.Name, Kind: planning.KindInstance, Status: planning.StatusPlanning, CreatedBy: f.principal.UserID, CreatedAt: time.Now(), FleetInput: &planning.InstancePlanInput{Authority: *authority, Snapshot: *snapshot}}
	if _, err = store.CreatePlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetPlan(t.Context(), "org", plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ManagedInstanceID != reserved.Instance.ID || saved.FleetInput == nil || saved.FleetInput.Snapshot.Digest != snapshot.Digest || saved.ManifestID != "" {
		t.Fatalf("lost fleet input: %+v", saved)
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]json.RawMessage
	if err = json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"FleetInput", "fleet_input", "authority", "snapshot"} {
		if _, ok := response[key]; ok {
			t.Fatalf("internal input exposed as %s", key)
		}
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_plan_inputs SET snapshot='{}' WHERE plan_id=$1`, plan.ID); err == nil {
		t.Fatal("immutable plan input changed")
	}
	ordinary := *plan
	ordinary.ID = "legacy-plan"
	ordinary.Kind = planning.KindRelease
	ordinary.FleetInput = nil
	if _, err = store.CreatePlan(t.Context(), &ordinary); !errors.Is(err, projects.ErrFleetManaged) {
		t.Fatalf("legacy store bypass: %v", err)
	}
	saved, err = store.GetPlan(t.Context(), "org", plan.ID)
	if err != nil || saved.Status != planning.StatusPlanning {
		t.Fatalf("blocked write superseded fleet: %+v %v", saved, err)
	}
	svc := planning.NewService(planning.Deps{Store: store, Projects: f.projects, Instances: f.service, Providers: providers.Registry(), Authz: fleetAuth{}})
	if _, approvalErr := svc.Approve(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, plan.ID, planning.DecideInput{}); !errors.Is(approvalErr, projects.ErrFleetManaged) {
		t.Fatalf("direct instance approval: %v", approvalErr)
	}
	status, err := svc.Plan(t.Context(), "org", plan.ID)
	if err != nil || status != planning.StatusBlocked {
		t.Fatalf("artifact planning: %s %v", status, err)
	}
	saved, err = store.GetPlan(t.Context(), "org", plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, problem := range saved.Problems {
		if problem.Code == "instance-isolation" {
			found = true
		}
	}
	if !found || saved.Executable {
		t.Fatal("unqualified isolation did not block the real planner")
	}

	workflow := &instanceAdmissionWorkflow{}
	admission := planning.NewService(planning.Deps{Store: store, Projects: f.projects, Instances: f.service, Workflow: workflow})
	accepted, err := admission.CreateInstancePlan(t.Context(), f.principal, planning.InstancePlanInput{Authority: *authority, Snapshot: *snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.ManagedInstanceID != reserved.Instance.ID || workflow.starts != 1 || accepted.FleetInput == nil {
		t.Fatal("instance entry did not preserve scoped input")
	}
	foreign := f.principal
	foreign.OrgID = "other"
	if _, err = admission.CreateInstancePlan(t.Context(), foreign, planning.InstancePlanInput{Authority: *authority, Snapshot: *snapshot}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("foreign instance entry: %v", err)
	}
}

// The planner workflow is delivered separately; this adapter records admission.
type instanceAdmissionWorkflow struct {
	planning.Workflow
	starts int
}

func (w *instanceAdmissionWorkflow) Start(context.Context, string, string, time.Duration) error {
	w.starts++
	return nil
}
func (*instanceAdmissionWorkflow) Cancel(context.Context, string, string) error { return nil }
