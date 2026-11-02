package recovery

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/xraph/loomplane/internal/clouds/enginebackup"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

// Descriptor is a catalog-validated native recovery contract.
type Descriptor struct {
	Engine, Format, Image, Architecture, Revision string
	Kind                                          recipes.Kind
	Component                                     *planning.ComponentConfig
	Catalog                                       *recipes.Recipe
}

func Describe(cfg *planning.ResourceConfig, registry *recipes.Registry) (Descriptor, error) {
	if cfg == nil || !planning.OwnedCloud(cfg.Provider) || cfg.Target == "" {
		return Descriptor{}, errors.New("recovery needs a reviewed connected target")
	}
	if registry == nil {
		registry = recipes.Default()
	}
	if cfg.Catalog != nil && cfg.Component == nil && cfg.Composition == nil {
		if cfg.Provider == "kubernetes" {
			if err := planning.ValidateKubernetesDatabaseSettings(cfg.TargetSettings); err != nil {
				return Descriptor{}, err
			}
		}
		expected, err := recipes.LookupForTarget(cfg.Kind, cfg.Version, cfg.TargetSettings["architecture"], cfg.Provider)
		if err != nil {
			return Descriptor{}, err
		}
		if !reflect.DeepEqual(expected, cfg.Catalog) {
			return Descriptor{}, errors.New("the recovery recipe changed")
		}
		return Descriptor{Engine: expected.Engine(), Format: expected.BackupFormat, Image: expected.Image, Architecture: expected.Architecture, Revision: expected.Revision, Catalog: expected}, nil
	}
	c := cfg.Component
	if c == nil || cfg.Catalog != nil || cfg.Composition != nil || c.Kind != cfg.Kind {
		return Descriptor{}, errors.New("the resource has no native recovery contract")
	}
	kind, composition, err := registry.ResolveRevision(c.Kind, cfg.Version, c.Revision, cfg.TargetSettings["architecture"], cfg.Provider)
	if err != nil {
		return Descriptor{}, err
	}
	if checkErr := kind.ReplayError(); checkErr != nil {
		return Descriptor{}, checkErr
	}
	if kind.Service == nil || !kind.DirectService() || len(composition.Components) != 1 || kind.Service.Recovery != kind.Name || kind.Service.BackupFormat != enginebackup.Format(kind.Name) || enginebackup.Format(kind.Name) == "" {
		return Descriptor{}, errors.New("the service has no qualified native recovery adapter")
	}
	component := composition.Components[0]
	if component.Protection != recipes.ProtectionNative || component.Volume == nil || c.Protection != recipes.ProtectionNative {
		return Descriptor{}, errors.New("the recorded component has no native protection")
	}
	expected := planning.ComponentConfig{Kind: c.Kind, Resource: c.Resource, Revision: composition.Revision, Component: component, Protection: recipes.ProtectionNative}
	actualJSON, err := json.Marshal(c)
	if err != nil {
		return Descriptor{}, err
	}
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		return Descriptor{}, err
	}
	if !bytes.Equal(actualJSON, expectedJSON) {
		return Descriptor{}, errors.New("the recorded recovery component changed")
	}
	return Descriptor{Engine: kind.Service.Recovery, Format: kind.Service.BackupFormat, Image: component.Image, Architecture: cfg.TargetSettings["architecture"], Revision: composition.Revision, Kind: kind, Component: c}, nil
}
