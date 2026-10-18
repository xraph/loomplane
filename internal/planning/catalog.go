package planning

import (
	"encoding/json"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/manifest"
)

func (p *planner) configureCatalog(name, base string, resource manifest.Resource, cfg *ResourceConfig, action *Action) {
	if cfg.Provider != "server" {
		if resource.Target != "" {
			p.problem(Finding{Code: "resource-target-unsupported", Subject: ResourceSubject, Name: name, Path: base + ".target", Message: "This resource provider does not accept a target connection."})
		}
		return
	}
	connection := p.targetConnection(name, base+".target", "target", "server", resource.Target)
	if connection == nil {
		return
	}
	cfg.Target = connection.ID
	cfg.TargetSettings = ReviewedConnectionSettings(connection.Cloud, connection.Details)
	action.ConnectionID = connection.ID
	recipe, err := recipes.Postgres(cfg.Version, connection.Details["architecture"])
	if err != nil {
		p.problem(Finding{Code: "catalog-unavailable", Subject: ResourceSubject, Name: name, Path: base, Message: err.Error()})
		return
	}
	cfg.Catalog = recipe
	if binding := p.bindings[action.ID]; binding != nil {
		var old ResourceConfig
		if json.Unmarshal(binding.Config, &old) != nil || binding.Provider != cfg.Provider || binding.ConnectionID != cfg.Target || old.Version != cfg.Version || (old.Catalog != nil && old.Catalog.Image != recipe.Image) {
			p.problem(Finding{Code: "database-migration-required", Subject: ResourceSubject, Name: name, Path: base, Message: "Moving or upgrading a persistent database needs a tested restore into a fresh service. Its existing data will remain in place."})
		}
	}
	p.warn(Finding{Code: "shared-host-storage", Subject: ResourceSubject, Name: name, Path: base, Message: "This PostgreSQL instance uses a retained local Docker volume with shared host capacity. Back it up to a separate store for host-loss recovery."})
}

func catalogOnly(m *manifest.Manifest) bool {
	return m != nil && m.Version == manifest.Version && len(m.Services) == 0 && len(m.Gateways) == 0 && len(m.Links) == 0
}
