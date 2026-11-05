package fleets

import (
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/fleet"
)

func TestFleetScaleStaleMetricNoDownscale(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	policy := fleet.ScalingPolicy{Version: 1, Revision: 1, Mode: "automatic", MinReplicas: 1, MaxReplicas: 10, CPUUtilization: 70, DownscaleStabilizationSeconds: 300, ScaleUpLimit: 2, CooldownSeconds: 60}
	base := ScalingEvidence{CurrentReplicas: 5, RecommendedReplicas: 2, Generation: 3, ObservedGeneration: 3, PolicyRevision: 1, ObservedAt: now, MetricsKnown: true, Complete: true, StableSince: now.Add(-5 * time.Minute), LastScaleAt: now.Add(-time.Hour)}
	for name, change := range map[string]func(*ScalingEvidence){
		"unknown":             func(e *ScalingEvidence) { e.MetricsKnown = false },
		"partial":             func(e *ScalingEvidence) { e.Complete = false },
		"stale":               func(e *ScalingEvidence) { e.ObservedAt = now.Add(-61 * time.Second) },
		"future":              func(e *ScalingEvidence) { e.ObservedAt = now.Add(time.Second) },
		"old generation":      func(e *ScalingEvidence) { e.ObservedGeneration = 2 },
		"old policy":          func(e *ScalingEvidence) { e.PolicyRevision = 2 },
		"no stabilization":    func(e *ScalingEvidence) { e.StableSince = time.Time{} },
		"short stabilization": func(e *ScalingEvidence) { e.StableSince = now.Add(-299 * time.Second) },
		"cooldown":            func(e *ScalingEvidence) { e.LastScaleAt = now.Add(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			e := base
			change(&e)
			decision, err := EvaluateScaling(policy, e, now)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Allowed || decision.DesiredReplicas != 5 {
				t.Fatalf("unsafe downscale: %+v", decision)
			}
		})
	}
	decision, err := EvaluateScaling(policy, base, now)
	if err != nil || !decision.Allowed || decision.DesiredReplicas != 2 {
		t.Fatalf("stable recommendation blocked: %+v %v", decision, err)
	}
	base.RecommendedReplicas = 10
	decision, err = EvaluateScaling(policy, base, now)
	if err != nil || decision.DesiredReplicas != 7 {
		t.Fatalf("rate not bounded: %+v %v", decision, err)
	}
}

func TestFleetScaleSnapshotBoundsAndQualification(t *testing.T) {
	release, instance, input := renderFixture(t)
	snapshot, err := RenderInstance(release, instance, input)
	if err != nil {
		t.Fatal(err)
	}
	in := fleet.ScaleInput{Service: "api", Policy: fleet.ScalingPolicy{Version: 1, Revision: 1, Mode: "manual", MinReplicas: 1, MaxReplicas: 1}}
	if err = ValidateScalingSnapshot(*snapshot, in); err != nil {
		t.Fatal(err)
	}
	in.Policy.MinReplicas = 4
	in.Policy.MaxReplicas = 4
	if err = ValidateScalingSnapshot(*snapshot, in); err == nil {
		t.Fatal("profile ceiling bypassed")
	}
	in.Policy.MinReplicas = 2
	in.Policy.MaxReplicas = 2
	if err = ValidateScalingSnapshot(*snapshot, in); !errors.Is(err, ErrScalingQualification) {
		t.Fatalf("unqualified parallel replicas accepted: %v", err)
	}
	in.Policy = fleet.ScalingPolicy{Version: 1, Revision: 1, Mode: "automatic", MinReplicas: 1, MaxReplicas: 1, CPUUtilization: 70, DownscaleStabilizationSeconds: 300, ScaleUpLimit: 2, CooldownSeconds: 60}
	if err = ValidateScalingSnapshot(*snapshot, in); !errors.Is(err, ErrScalingQualification) {
		t.Fatalf("unqualified automatic scaler accepted: %v", err)
	}
}

func TestFleetScaleManualRequiresWriterReceipt(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	policy := fleet.ScalingPolicy{Version: 1, Revision: 2, Mode: "manual", MinReplicas: 2, MaxReplicas: 2}
	e := ScalingEvidence{CurrentReplicas: 5, Generation: 3, ObservedGeneration: 3, PolicyRevision: 2, ObservedAt: now}
	d, err := EvaluateScaling(policy, e, now)
	if err != nil || d.Allowed || d.Reason != "writer_transfer_pending" {
		t.Fatalf("manual scaling bypassed HPA relinquishment: %+v %v", d, err)
	}
	e.ManualWriterConfirmed = true
	d, err = EvaluateScaling(policy, e, now)
	if err != nil || !d.Allowed || d.DesiredReplicas != 2 || d.Writer != "manual" {
		t.Fatalf("confirmed writer blocked: %+v %v", d, err)
	}
	e.ObservedGeneration = 2
	d, err = EvaluateScaling(policy, e, now)
	if err != nil || d.Allowed {
		t.Fatalf("old handoff receipt accepted: %+v %v", d, err)
	}
}
