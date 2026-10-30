package deployments

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
)

func TestManagedProtocolDirectGuard(t *testing.T) {
	p := &planning.Plan{RequiredRuntimeProtocol: orchestration.ManagedWorkloadProtocol}
	if err := legacyCapacityGuard(t.Context(), p); err == nil {
		t.Fatal("direct runner accepted managed protocol")
	}
	if err := legacyCapacityGuard(orchestration.CapacityContext(t.Context()), p); err == nil {
		t.Fatal("legacy capacity context accepted managed protocol")
	}
	if err := legacyCapacityGuard(orchestration.ManagedWorkloadContext(t.Context()), p); err != nil {
		t.Fatal(err)
	}
	p.RequiredRuntimeProtocol = "unknown"
	if err := legacyCapacityGuard(orchestration.ManagedWorkloadContext(t.Context()), p); err == nil {
		t.Fatal("accepted unknown protocol")
	}
	if err := legacyCapacityGuard(t.Context(), &planning.Plan{}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedProtocolRunnerStartRejectsLegacy(t *testing.T) {
	p := &planning.Plan{ID: "managed", OrgID: "org", Status: planning.StatusApproved, Executable: true, RequiredRuntimeProtocol: orchestration.ManagedWorkloadProtocol}
	r := NewRunner(RunnerDeps{Plans: &fakePlans{plan: p}})
	if _, _, err := r.Start(t.Context(), p.OrgID, p.ID); err == nil {
		t.Fatal("direct Start accepted managed plan")
	}
	if _, _, err := r.Start(orchestration.CapacityContext(t.Context()), p.OrgID, p.ID); err == nil {
		t.Fatal("v3 Start accepted managed plan")
	}
}

func TestManagedCleanupDirectGuard(t *testing.T) {
	for _, demand := range []bool{false, true} {
		action := planning.Action{CleanupTargets: []planning.ManagedCleanupTarget{{SchemaVersion: 1}}}
		if demand {
			action.Service = &planning.ServiceConfig{ClusterDemand: &planning.ClusterWorkloadDemand{}}
		}
		p := &planning.Plan{RequiredRuntimeProtocol: orchestration.ManagedCleanupProtocol, Actions: []planning.Action{action}}
		for _, ctx := range []context.Context{t.Context(), orchestration.CapacityContext(t.Context()), orchestration.ManagedWorkloadContext(t.Context())} {
			if err := legacyCapacityGuard(ctx, p); !errors.Is(err, ErrManagedCleanupProtocol) {
				t.Fatalf("old context accepted cleanup: %v", err)
			}
		}
		if err := legacyCapacityGuard(orchestration.ManagedCleanupContext(t.Context()), p); err != nil {
			t.Fatal(err)
		}
		p.RequiredRuntimeProtocol = orchestration.ManagedWorkloadProtocol
		if err := legacyCapacityGuard(orchestration.ManagedCleanupContext(t.Context()), p); !errors.Is(err, ErrManagedCleanupReviewRequired) {
			t.Fatalf("historical plan skipped fresh review: %v", err)
		}
	}
}
