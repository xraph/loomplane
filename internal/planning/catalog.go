package planning

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/manifest"
)

// configureCatalog pins a catalog engine resource to its target. resource
// is the resource as the plan's environment runs it.
func (p *planner) configureCatalog(name, base string, resource manifest.Resource, cfg *ResourceConfig, action *Action) {
	if p.kinds().Composed(cfg.Kind) {
		return // planComposition pins each of its components
	}
	targetPath := overrideField(resource, base, p.in.Environment, "target")
	if !OwnedCloud(cfg.Provider) {
		if resource.Target != "" {
			prov, err := p.in.Providers.Get(cfg.Provider)
			if err != nil {
				return
			}
			i := slices.IndexFunc(p.in.Connections, func(c ConnectionOption) bool { return c.ID == resource.Target && c.Cloud == prov.Cloud })
			if i < 0 {
				p.problem(Finding{Code: "resource-target-unsupported", Subject: ResourceSubject, Name: name, Path: targetPath, Message: "Choose a checked account for this resource's provider."})
				return
			}
			c := p.in.Connections[i]
			if !c.Ready {
				p.problem(Finding{Code: "resource-account-not-ready", Subject: ResourceSubject, Name: name, Path: targetPath, Message: "Check this resource's cloud account before creating it."})
				return
			}
			cfg.AccountIdentity = connections.AccountIdentity(c.Cloud, c.Account, c.Details)
			if cfg.AccountIdentity == "" {
				p.problem(Finding{Code: "resource-account-unverified", Subject: ResourceSubject, Name: name, Path: targetPath, Message: "Check this cloud account's identity before creating a resource."})
				return
			}
			action.ConnectionID = c.ID
		}
		p.configureManagedCatalog(name, base, resource, cfg, action)
		return
	}
	connection := p.targetConnection(name, targetPath, "target", cfg.Provider, resource.Target)
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
	recipe, err := p.kinds().LookupEngine(cfg.Kind, cfg.Version, connection.Details["architecture"], cfg.Provider)
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
