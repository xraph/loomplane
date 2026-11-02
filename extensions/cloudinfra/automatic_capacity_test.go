package cloudinfra

import (
	"testing"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/config"
)

// clouds.automatic_capacity.enabled is the only way pools grow on their own,
// and the capacity policy view reports it so the dashboard can say so.
func TestAutomaticCapacityFollowsConfig(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		svc := newService(config.CloudsConfig{AutomaticCapacity: config.AutomaticCapacityConfig{Enabled: enabled}}, infra.Deps{})
		if got := svc.AutomaticExpansion(); got != enabled {
			t.Fatalf("configured %v, service reports %v", enabled, got)
		}
		if got := (&Extension{svc: svc}).policyView("cloud", nil).AutomaticExpansion; got != enabled {
			t.Fatalf("configured %v, policy view reports %v", enabled, got)
		}
	}
	if config.Defaults().Clouds.AutomaticCapacity.Enabled {
		t.Fatal("automatic capacity is on by default")
	}
}
