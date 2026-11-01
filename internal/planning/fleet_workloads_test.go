package planning

import (
	"testing"

	"github.com/xraph/loomplane/pkg/manifest"
)

func TestPodGroupInstanceDefaultHasNoUnreservedSurge(t *testing.T) {
	input := manifest.Manifest{Version: 1, Services: map[string]manifest.Service{"api": {Settings: manifest.Settings{Size: &manifest.Size{CPU: 0.5, Memory: 256}}}}}
	effective, err := instanceExecutionManifest(input)
	if err != nil {
		t.Fatal(err)
	}
	group := effective.Services["api"].PodGroup
	if group == nil || group.SurgeReplicas != 0 {
		t.Fatal("instance inherited Kubernetes default surge")
	}
	if input.Services["api"].PodGroup != nil {
		t.Fatal("sealed snapshot was mutated")
	}
}
