package cloudinfra

import (
	"errors"
	"slices"
	"testing"

	"github.com/xraph/forge"
	"github.com/xraph/grove/drivers/pgdriver"

	fleetext "github.com/xraph/loomplane/extensions/fleets"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/currentauthority"
	"github.com/xraph/loomplane/internal/fleets"
)

func TestFleetNamespaceCompositionRequiresTypedDependencies(t *testing.T) {
	for _, name := range []string{"missing authority", "nil authority", "missing fleet", "nil fleet", "nil store", "missing app"} {
		t.Run(name, func(t *testing.T) {
			c := forge.NewContainer()
			coordinator := &currentauthority.Coordinator{}
			if name != "missing authority" {
				authority := coordinator.FleetAuthority()
				if name == "nil authority" {
					authority = nil
				}
				if err := forge.ProvideValue(c, authority); err != nil {
					t.Fatal(err)
				}
			}
			if name != "missing fleet" {
				svc := fleets.NewService(fleets.Deps{})
				if name == "nil fleet" {
					svc = nil
				}
				if err := forge.ProvideValue(c, svc); err != nil {
					t.Fatal(err)
				}
			}
			store := cloudinfra.NewPgStore(pgdriver.New())
			if name == "nil store" {
				store = nil
			}
			appID := "app"
			if name == "missing app" {
				appID = ""
			}
			if _, _, err := fleetNamespaceDependencies(c, store, appID); !errors.Is(err, currentauthority.ErrUnavailable) {
				t.Fatalf("accepted missing dependency: %v", err)
			}
		})
	}
}

func TestFleetNamespaceCompositionSharesServiceWithoutQualification(t *testing.T) {
	c := forge.NewContainer()
	coordinator := &currentauthority.Coordinator{}
	authority := coordinator.FleetAuthority()
	instances := fleets.NewService(fleets.Deps{})
	if err := forge.ProvideValue(c, authority); err != nil {
		t.Fatal(err)
	}
	if err := forge.ProvideValue(c, instances); err != nil {
		t.Fatal(err)
	}
	store := cloudinfra.NewPgStore(pgdriver.New())
	got, namespaces, err := fleetNamespaceDependencies(c, store, "app")
	if err != nil || got != instances || namespaces == nil {
		t.Fatalf("composition=%p %p err=%v", got, namespaces, err)
	}
	svc := newService(config.CloudsConfig{}, cloudinfra.Deps{Store: store, Clusters: store, InstanceAuthority: got, InstanceNamespaces: namespaces})
	if svc.InstanceAuthority != instances || svc.InstanceNamespaces != namespaces || svc.Store != store || svc.Clusters != store {
		t.Fatal("namespace dependencies were duplicated")
	}
	if svc.InstanceIsolation != nil {
		t.Fatal("composition invented namespace qualification")
	}
	if !slices.Contains((&Extension{}).Dependencies(), fleetext.Name) {
		t.Fatal("fleet service is not a lifecycle dependency")
	}
	if err := namespaces.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}
