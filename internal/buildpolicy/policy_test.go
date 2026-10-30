package buildpolicy

import "testing"

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
