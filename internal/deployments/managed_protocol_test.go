package deployments

import (
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
