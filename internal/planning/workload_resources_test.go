package planning

import (
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/pkg/manifest"
)

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
