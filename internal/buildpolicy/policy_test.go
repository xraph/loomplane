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

func TestCheckRunner(t *testing.T) {
	org := func() map[string]string {
		return map[string]string{"isolation": "firecracker", "build_isolation": "ready", "runtime_identity": strings.Repeat("b", 64), "host_id": "runner-1", "pool_scope": "organization"}
	}
	platform := func() map[string]string {
		d := org()
		d["pool_scope"] = "platform"
		d["platform"] = "true"
		d["smt_active"] = "false"
		d["denied_ports"] = "25, 465,587"
		d["per_org_vms"], d["per_org_vcpus"], d["per_org_memory_mib"] = "4", "8", "8192"
		return d
	}
	for _, tc := range []struct {
		name    string
		details map[string]string
		mutate  func(map[string]string)
		allowed bool
	}{
		{"organization pool with microVM evidence", org(), nil, true},
		{"project pool", org(), func(d map[string]string) { d["pool_scope"] = "project" }, true},
		{"no isolation kind", org(), func(d map[string]string) { delete(d, "isolation") }, false},
		{"container isolation is never enough", org(), func(d map[string]string) { d["isolation"] = "container" }, false},
		{"check not passed", org(), func(d map[string]string) { d["build_isolation"] = "pending" }, false},
		{"short runtime identity", org(), func(d map[string]string) { d["runtime_identity"] = "abc" }, false},
		{"no host id", org(), func(d map[string]string) { d["host_id"] = "" }, false},
		{"dedicated docker claim does not help", org(), func(d map[string]string) { d["isolation"] = ""; d["build_workers"] = "dedicated" }, false},
		{"platform pool passing the platform policy", platform(), nil, true},
		{"platform pool with SMT on", platform(), func(d map[string]string) { d["smt_active"] = "true" }, false},
		{"platform pool not reporting SMT", platform(), func(d map[string]string) { delete(d, "smt_active") }, false},
		{"platform pool allowing mail", platform(), func(d map[string]string) { d["denied_ports"] = "25,465" }, false},
		{"platform pool without per-org limits", platform(), func(d map[string]string) { d["per_org_vms"] = "0" }, false},
		{"platform pool not in platform mode", platform(), func(d map[string]string) { d["platform"] = "" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.mutate != nil {
				tc.mutate(tc.details)
			}
			err := CheckBackend(tc.details, "runner", false)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v: %v", tc.allowed, err)
			}
			// A runner's evidence is its own: pooled never changes the answer.
			if pooledErr := CheckBackend(tc.details, "runner", true); (pooledErr == nil) != (err == nil) {
				t.Fatalf("pooled changed the result: %v vs %v", pooledErr, err)
			}
		})
	}
}
