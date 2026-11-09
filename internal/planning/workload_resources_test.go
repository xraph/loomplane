package planning

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/pkg/fleet"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

type disabledFleetCapacity struct{ CapacityProvisioner }

func (disabledFleetCapacity) CheckClusterWorkloadCapacity(context.Context) error { return nil }
func (disabledFleetCapacity) ClusterWorkloadCapacityEnabled() bool               { return false }
func (disabledFleetCapacity) CurrentClusterScalingPolicy(context.Context, string, string, string, string) (*cloudinfra.ClusterScalingPolicy, error) {
	return nil, ErrUnavailable
}

func TestFleetScaleReviewRequiresCapacityAdmission(t *testing.T) {
	plan, cfg := fleetScalingPlanFixture(t)
	cfg.TargetSettings = map[string]string{"managed_attachment_id": "attachment"}
	action := Action{Name: "api", Provider: "kubernetes", Op: Create, Service: cfg}
	service := &Service{store: &PgStore{}, capacity: disabledFleetCapacity{}}
	var block *cloudinfra.CapacityAdmissionError
	if err := service.PrepareClusterWorkloadDemands(t.Context(), plan, &Result{Actions: []Action{action}}); !errors.As(err, &block) || block.Block.Reason != "cluster_capacity_disabled" {
		t.Fatalf("disabled capacity silently skipped fleet reservation: %v", err)
	}
	service.store = nil
	if err := service.PrepareClusterWorkloadDemands(t.Context(), plan, &Result{Actions: []Action{action}}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing capacity store silently skipped fleet reservation: %v", err)
	}
}

func TestClusterScalingHPAReviewedMaximum(t *testing.T) {
	cfg := &ServiceConfig{Size: &manifest.Size{CPU: .25, Memory: 128}, Scale: &manifest.Scale{Min: 1, Max: 5}}
	v, err := managedWorkloadMaximum(cfg)
	if err != nil || v.CPUMillis != 1250 || v.MemoryBytes != 640<<20 || v.Pods != 5 {
		t.Fatalf("maximum %+v: %v", v, err)
	}
	cfg.Size = nil
	if _, err = managedWorkloadMaximum(cfg); err == nil {
		t.Fatal("missing memory became a zero guarantee")
	}
	raw, err := json.Marshal(ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if _, ok := data["cluster_demand"]; ok {
		t.Fatal("legacy JSON gained demand")
	}
	if _, ok := data["capacity_mode"]; ok {
		t.Fatal("legacy JSON gained capacity mode")
	}
}

func fleetScalingPlanFixture(t *testing.T) (*Plan, *ServiceConfig) {
	t.Helper()
	group := &provider.PodGroup{IsolationRevision: provider.InstanceIsolationRevision, RuntimeStorageMiB: provider.DefaultInstanceContainerStorageMiB}
	cfg := &ServiceConfig{Size: &manifest.Size{CPU: 0.5, Memory: 256}, Scale: &manifest.Scale{Min: 1, Max: 1}, PodGroup: group}
	snapshot := fleets.InstanceSnapshot{InstanceID: "instance", FleetID: "fleet", EnvironmentID: "environment", ProfileID: "small", ReleaseID: "release", Generation: 1, OwnershipEpoch: 1, Template: fleet.Template{Profiles: map[string]fleet.Profile{"small": {Services: map[string]fleet.ServiceProfile{"api": {CPUMillis: 500, MemoryMiB: 256, MinReplicas: 1, MaxReplicas: 5}}}}}}
	snapshot.Manifest = manifest.Manifest{Services: map[string]manifest.Service{"api": {Settings: manifest.Settings{Size: &manifest.Size{CPU: 0.5, Memory: 256}, Scale: &manifest.Scale{Min: 1, Max: 1}}}}}
	digest, err := fleets.SnapshotDigest(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Digest = digest
	authority := fleets.OperationAuthority{Scope: fleets.FleetScope{OrgID: "org", ProjectID: "project", FleetID: "fleet"}, Request: fleets.OperationRequest{SnapshotDigest: digest, ProfileID: "small", ReleaseID: "release"}}
	plan := &Plan{OrgID: "org", ProjectID: "project", EnvironmentID: "environment", Kind: KindInstance, ManagedInstanceID: "instance", FleetInput: &InstancePlanInput{Snapshot: snapshot, Authority: authority}}
	return plan, cfg
}

func TestFleetScalePlanningReservesProfileMaximumWithoutEnablingHPA(t *testing.T) {
	plan, cfg := fleetScalingPlanFixture(t)
	demand, err := managedPlanWorkloadMaximum(plan, "api", cfg)
	if err != nil || demand.CPUMillis != 2500 || demand.MemoryBytes != 1342177280 || demand.Pods != 5 {
		t.Fatalf("profile maximum omitted: %+v %v", demand, err)
	}
	if cfg.Scale.Max != 1 || cfg.Scale.Min != 1 {
		t.Fatal("capacity reservation enabled automatic replica writer")
	}
	cfg.Size.GPU = &manifest.GPU{Kind: "gpu-a", Count: 1}
	if _, err = managedPlanWorkloadMaximum(plan, "api", cfg); err == nil {
		t.Fatal("GPU request became CPU-only cluster reservation")
	}
	cfg.Size.GPU = nil
	if _, err = managedPlanWorkloadMaximum(plan, "missing", cfg); err == nil {
		t.Fatal("missing profile service accepted")
	}
	plan.FleetInput.Snapshot.ProfileID = "other"
	if _, err = managedPlanWorkloadMaximum(plan, "api", cfg); err == nil {
		t.Fatal("tampered snapshot accepted")
	}
	plan.FleetInput.Snapshot.ProfileID = "small"
	plan.FleetInput.Authority.Request.SnapshotDigest = "stale"
	if _, err = managedPlanWorkloadMaximum(plan, "api", cfg); err == nil {
		t.Fatal("stale authority snapshot accepted")
	}
	plan.FleetInput = nil
	if _, err = managedPlanWorkloadMaximum(plan, "api", cfg); err == nil {
		t.Fatal("missing fleet snapshot fell back to replica minimum")
	}
}

func TestFleetScaleReviewAndEffectRejectBestEffort(t *testing.T) {
	for _, mode := range []string{"best_effort", ""} {
		plan, cfg := fleetScalingPlanFixture(t)
		cfg.TargetSettings = map[string]string{"managed_attachment_id": "attachment"}
		cfg.CapacityMode = mode
		cfg.ClusterDemand = &ClusterWorkloadDemand{Mode: "best_effort"}
		action := Action{Name: "api", Provider: "kubernetes", Op: Create, Service: cfg}
		service := &Service{store: &PgStore{}}
		checks := map[string]error{
			"review": service.PrepareClusterWorkloadDemands(t.Context(), plan, &Result{Actions: []Action{action}}),
			"effect": service.resolveClusterWorkload(t.Context(), nil, plan, action),
		}
		for name, err := range checks {
			var block *cloudinfra.CapacityAdmissionError
			if !errors.As(err, &block) || block.Block.Reason != "fleet_capacity_mode_unqualified" {
				t.Fatalf("%s accepted best-effort or reached unrelated authority: %v", name, err)
			}
		}
	}
}

func TestFleetScaleEffectRejectsLowerApprovedMaximum(t *testing.T) {
	plan, cfg := fleetScalingPlanFixture(t)
	cfg.ClusterDemand = &ClusterWorkloadDemand{Mode: "guaranteed", Input: cloudinfra.ClusterWorkloadInput{Maximum: cloudinfra.ClusterWorkloadVector{CPUMillis: 500, MemoryBytes: 256 << 20, Pods: 1, EphemeralStorageBytes: 64 << 20}}}
	service := &Service{}
	if err := service.resolveClusterWorkload(t.Context(), nil, plan, Action{Name: "api", Provider: "kubernetes", Op: Create, Service: cfg}); !errors.Is(err, ErrConflict) {
		t.Fatalf("lower reservation reached execution dependencies: %v", err)
	}
}

func TestFleetScaleCapacityModePreservesOrdinaryApplications(t *testing.T) {
	cfg := &ServiceConfig{CapacityMode: "best_effort", TargetSettings: map[string]string{"managed_attachment_id": "attachment"}, ClusterDemand: &ClusterWorkloadDemand{Mode: "best_effort"}}
	action := Action{Name: "api", Provider: "kubernetes", Op: Create, Service: cfg}
	service := &Service{store: &PgStore{}}
	plan := &Plan{}
	if err := service.PrepareClusterWorkloadDemands(t.Context(), plan, &Result{Actions: []Action{action}}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ordinary review behavior changed: %v", err)
	}
	if err := service.resolveClusterWorkload(t.Context(), nil, plan, action); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ordinary effect behavior changed: %v", err)
	}
	plan.ManagedInstanceID = "instance"
	if err := service.PrepareClusterWorkloadDemands(t.Context(), plan, &Result{Actions: []Action{action}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("managed marker bypassed review guard: %v", err)
	}
	if err := service.resolveClusterWorkload(t.Context(), nil, plan, action); !errors.Is(err, ErrInvalid) {
		t.Fatalf("managed marker bypassed effect guard: %v", err)
	}
}
