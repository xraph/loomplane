package deployments

import (
	"testing"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/manifest"
)

func TestClusterScalingRuntimeRequestEquivalence(t *testing.T) {
	for _, tc := range []struct {
		name        string
		size        *manifest.Size
		scale       *manifest.Scale
		cpu, memory int
	}{
		{name: "legacy_without_requests"},
		{name: "legacy_elastic", scale: &manifest.Scale{Min: 1, Max: 5}, cpu: 100},
		{name: "guaranteed_elastic", size: &manifest.Size{CPU: .25, Memory: 128}, scale: &manifest.Scale{Min: 1, Max: 5}, cpu: 250, memory: 128},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &planning.ServiceConfig{Size: tc.size, Scale: tc.scale}
			job := &Job{Deployment: &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "environment"}, Connection: &connections.Connection{ID: "connection", Cloud: "kubernetes"}, Action: planning.Action{Provider: "kubernetes", Name: "api", Service: cfg}}
			sp, _ := ownedSpec(job, "image", ownedState{})
			if sp.Resources.CPUMillis != tc.cpu || sp.Resources.MemoryMB != tc.memory {
				t.Fatalf("runtime changed request semantics: %+v", sp.Resources)
			}
		})
	}
}
