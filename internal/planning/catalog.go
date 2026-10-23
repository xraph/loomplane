package planning

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/manifest"
)

func (p *planner) configureCatalog(name, base string, resource manifest.Resource, cfg *ResourceConfig, action *Action) {
	if p.kinds().Composed(cfg.Kind) {
		return // planComposition pins each of its components
	}
	if !OwnedCloud(cfg.Provider) {
		if resource.Target != "" {
			p.problem(Finding{Code: "resource-target-unsupported", Subject: ResourceSubject, Name: name, Path: base + ".target", Message: "This resource provider does not accept a target connection."})
		}
		p.configureManagedCatalog(name, base, cfg, action)
		return
	}
	connection := p.targetConnection(name, base+".target", "target", cfg.Provider, resource.Target)
	if connection == nil {
		return
	}
	if cfg.Provider == "kubernetes" {
		if err := ValidateKubernetesDatabaseSettings(connection.Details); err != nil {
			p.problem(Finding{Code: "catalog-storage-unavailable", Subject: ResourceSubject, Name: name, Path: base, Message: err.Error()})
			return
		}
		if connection.Details["postgres_recovery_permissions"] != "ready" {
			what := "database"
			if cfg.Kind == "redis" || cfg.Kind == "valkey" {
				what = "cache"
			}
			p.problem(Finding{Code: "catalog-recovery-unavailable", Subject: ResourceSubject, Name: name, Path: base, Message: "Check pod exec, network-policy and temporary-volume cleanup permissions before deploying this " + what + "."})
			return
		}
	}
	cfg.Target = connection.ID
	cfg.TargetSettings = ReviewedConnectionSettings(connection.Cloud, connection.Details)
	action.ConnectionID = connection.ID
	recipe, err := recipes.LookupForTarget(cfg.Kind, cfg.Version, connection.Details["architecture"], cfg.Provider)
	if err != nil {
		p.problem(Finding{Code: "catalog-unavailable", Subject: ResourceSubject, Name: name, Path: base, Message: err.Error()})
		return
	}
	cfg.Catalog = recipe
	if binding := p.bindings[action.ID]; binding != nil {
		var old ResourceConfig
		if json.Unmarshal(binding.Config, &old) != nil || binding.Provider != cfg.Provider || binding.ConnectionID != cfg.Target || old.Kind != cfg.Kind || old.Version != cfg.Version || (old.Catalog != nil && !reflect.DeepEqual(old.Catalog, recipe)) || (cfg.Provider == "kubernetes" && !reflect.DeepEqual(old.TargetSettings, cfg.TargetSettings)) {
			p.problem(Finding{Code: "database-migration-required", Subject: ResourceSubject, Name: name, Path: base, Message: "Moving or upgrading a persistent database needs a tested restore into a fresh service. Its existing data will remain in place."})
		}
	}
	if cfg.Provider == "kubernetes" && recipe.Cache() {
		// Only a composition's dependency gets here: a cluster doesn't
		// run a standalone cache.
		title := cfg.Kind
		if engine, ok := p.kinds().Engine(cfg.Kind); ok {
			title = engine.Title
		}
		p.warn(Finding{Code: "retained-cluster-storage", Subject: ResourceSubject, Name: name, Path: base, Message: fmt.Sprintf("This %s cache requests a retained %d MiB PVC on %s. Loomplane doesn't back up %s on Kubernetes yet. Storage replication and zone-loss recovery depend on your storage driver.", title, recipe.VolumeSizeMB, connection.Details["storage_class"], title)})
		return
	}
	if cfg.Provider == "kubernetes" {
		p.warn(Finding{Code: "retained-cluster-storage", Subject: ResourceSubject, Name: name, Path: base, Message: fmt.Sprintf("This standalone PostgreSQL instance requests a retained %d MiB PVC on %s. It needs a NetworkPolicy-enforcing CNI for recovery copies. Storage replication and zone-loss recovery depend on your storage driver; keep backups outside this cluster.", recipe.VolumeSizeMB, connection.Details["storage_class"])})
		return
	}
	p.warn(Finding{Code: "shared-host-storage", Subject: ResourceSubject, Name: name, Path: base, Message: "This database uses a retained local Docker volume with shared host capacity. Back it up to a separate store for host-loss recovery."})
}

func catalogOnly(m *manifest.Manifest) bool {
	return m.CatalogOnly()
}
