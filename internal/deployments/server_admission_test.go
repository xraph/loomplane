package deployments

import (
	"testing"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

// Only direct actions hold the daemon's admission lock. A pooled action
// leaves it to its pool reservation even when it asks for GPUs: the
// server runtime serializes device picking with its own lock, which waits
// on no other lock.
func TestHoldsServerAdmission(t *testing.T) {
	pool := func(gpus int64) *planning.CloudPlacement {
		return &planning.CloudPlacement{PoolID: "pool", ServerID: "server", GPUs: gpus}
	}
	gpu := &provider.GPUPlacement{GPURequest: provider.GPURequest{Kind: "nvidia-l4", Count: 1}}
	for _, tc := range []struct {
		name string
		a    planning.Action
		want bool
	}{
		{"direct CPU", planning.Action{Provider: "server", Op: planning.Create, Service: &planning.ServiceConfig{}}, true},
		{"direct GPU", planning.Action{Provider: "server", Op: planning.Update, Service: &planning.ServiceConfig{GPU: gpu}}, true},
		{"direct resource", planning.Action{Provider: "server", Op: planning.Create}, true},
		{"pooled CPU", planning.Action{Provider: "server", Op: planning.Create, Service: &planning.ServiceConfig{CloudPlacement: pool(0)}}, false},
		{"pooled GPU label", planning.Action{Provider: "server", Op: planning.Create, Service: &planning.ServiceConfig{GPU: gpu, CloudPlacement: pool(0)}}, false},
		{"pooled GPU reservation", planning.Action{Provider: "server", Op: planning.Replace, Service: &planning.ServiceConfig{CloudPlacement: pool(1)}}, false},
		{"pooled zero GPUs", planning.Action{Provider: "server", Op: planning.Create, Service: &planning.ServiceConfig{GPU: &provider.GPUPlacement{}, CloudPlacement: pool(0)}}, false},
		{"pooled GPU delete", planning.Action{Provider: "server", Op: planning.Delete, Service: &planning.ServiceConfig{GPU: gpu, CloudPlacement: pool(1)}}, false},
		{"direct keep", planning.Action{Provider: "server", Op: planning.Keep, Service: &planning.ServiceConfig{GPU: gpu}}, false},
		{"kubernetes GPU", planning.Action{Provider: "kubernetes", Op: planning.Create, Service: &planning.ServiceConfig{GPU: gpu}}, false},
	} {
		if got := holdsServerAdmission(tc.a); got != tc.want {
			t.Errorf("%s: holdsServerAdmission = %v, want %v", tc.name, got, tc.want)
		}
	}
}
