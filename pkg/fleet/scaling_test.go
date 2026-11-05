package fleet

import (
	"math"
	"testing"
)

func TestFleetScalePolicy(t *testing.T) {
	valid := ScalingPolicy{Version: 1, Revision: 1, Mode: "automatic", MinReplicas: 1, MaxReplicas: 5, CPUUtilization: 70, DownscaleStabilizationSeconds: 300, ScaleUpLimit: 2, CooldownSeconds: 60}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*ScalingPolicy){
		"unversioned":        func(p *ScalingPolicy) { p.Version = 0 },
		"unreviewed":         func(p *ScalingPolicy) { p.Revision = 0 },
		"unknown mode":       func(p *ScalingPolicy) { p.Mode = "elastic" },
		"zero replicas":      func(p *ScalingPolicy) { p.MinReplicas = 0 },
		"reversed bounds":    func(p *ScalingPolicy) { p.MinReplicas = 6 },
		"unbounded replicas": func(p *ScalingPolicy) { p.MaxReplicas = 101 },
		"no target":          func(p *ScalingPolicy) { p.CPUUtilization = 0 },
		"no stabilization":   func(p *ScalingPolicy) { p.DownscaleStabilizationSeconds = 0 },
		"unbounded rate":     func(p *ScalingPolicy) { p.ScaleUpLimit = 0 },
		"no cooldown":        func(p *ScalingPolicy) { p.CooldownSeconds = 0 },
		"unqualified signal": func(p *ScalingPolicy) { p.Signals = []ScalingSignalRef{{Name: "queue", Revision: 1}} },
	} {
		t.Run(name, func(t *testing.T) {
			p := valid
			change(&p)
			if p.Validate() == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
	manual := ScalingPolicy{Version: 1, Revision: 2, Mode: "manual", MinReplicas: 3, MaxReplicas: 3}
	if err := manual.Validate(); err != nil {
		t.Fatal(err)
	}
	manual.MaxReplicas = 4
	if manual.Validate() == nil {
		t.Fatal("manual range leaves competing replica writers")
	}
}

func TestFleetScalePolicyRevision(t *testing.T) {
	in := ScaleInput{Service: "api", ExpectedPolicyRevision: 2, Policy: ScalingPolicy{Version: 1, Revision: 3, Mode: "manual", MinReplicas: 1, MaxReplicas: 1}}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []int64{-1, 0, 1, 3, math.MaxInt64} {
		in.ExpectedPolicyRevision = revision
		if in.Validate() == nil {
			t.Fatalf("stale revision %d accepted", revision)
		}
	}
}
