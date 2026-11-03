package buildpolicy

import (
	"strings"
	"testing"
)

func TestSourceBuildPolicy(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		details                     map[string]string
		kubernetes, pooled, allowed bool
	}{
		{"unknown", nil, true, false, false},
		{"shared", map[string]string{"build_workers": "shared"}, true, false, false},
		{"dedicated", map[string]string{"build_workers": "dedicated"}, false, false, true},
		{"pool overrides dedicated", map[string]string{"build_workers": "dedicated"}, false, true, false},
		{"unchecked opt in", map[string]string{"build_workers": "dedicated", "build_runtime_class": "kata"}, true, false, false},
		{"kata", map[string]string{"build_runtime_class": "kata", "build_isolation": "ready", "build_runtime_identity": "identity"}, true, false, true},
		{"docker cannot claim kata", map[string]string{"build_runtime_class": "kata", "build_isolation": "ready", "build_runtime_identity": "identity"}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Check(tc.details, tc.kubernetes, tc.pooled); (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v: %v", tc.allowed, err)
			}
		})
	}
}

func TestCheckPlatform(t *testing.T) {
	good := func() PlatformEvidence {
		return PlatformEvidence{Isolation: true, RuntimeIdentity: strings.Repeat("a", 64), HostID: "host-1", Platform: true, DeniedPorts: []int{25, 465, 587}, PerOrgVMs: 4, PerOrgVCPUs: 8, PerOrgMemoryMiB: 8192}
	}
	if err := CheckPlatform(good()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*PlatformEvidence){
		"no isolation":  func(e *PlatformEvidence) { e.Isolation = false },
		"short id":      func(e *PlatformEvidence) { e.RuntimeIdentity = strings.Repeat("a", 63) },
		"no host id":    func(e *PlatformEvidence) { e.HostID = "" },
		"not platform":  func(e *PlatformEvidence) { e.Platform = false },
		"smt on":        func(e *PlatformEvidence) { e.SMTActive = true },
		"mail open":     func(e *PlatformEvidence) { e.DeniedPorts = []int{25, 587} },
		"no vcpu limit": func(e *PlatformEvidence) { e.PerOrgVCPUs = 0 },
	} {
		e := good()
		mutate(&e)
		if CheckPlatform(e) == nil {
			t.Fatal(name, "accepted")
		}
	}
}
