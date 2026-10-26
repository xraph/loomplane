package planning

import (
	"encoding/json"
	"reflect"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// ManagedDatabaseConfig pins the requested profile and verified account scope.
// ResourceConfig.Region records the reviewed region separately.
type ManagedDatabaseConfig struct {
	Profile         provider.ManagedDatabaseProfile `json:"profile"`
	ConnectionID    string                          `json:"connection_id"`
	AccountIdentity string                          `json:"account_identity"`
	CloudSQLPSC     provider.CloudSQLSourceEndpoint `json:"cloud_sql_psc,omitempty,omitzero"`
}

func (p *planner) configureManagedCatalog(name, base string, resource manifest.Resource, cfg *ResourceConfig, action *Action) {
	binding := p.bindings[action.ID]
	var old ResourceConfig
	if binding != nil {
		if err := json.Unmarshal(binding.Config, &old); err != nil {
			if p.in.Catalog || resource.CloudSQLPSC != nil {
				p.problem(Finding{Code: "managed-profile-invalid", Subject: ResourceSubject, Name: name, Path: base, Message: "The existing database configuration cannot be reviewed. Its data will remain in place."})
			}
			return
		}
		// Live legacy bindings keep their original request contract. A missing
		// legacy database can receive a reviewed profile on standalone creation.
		if old.Managed == nil && (binding.Drift != DriftMissing || !p.in.Catalog) {
			if resource.CloudSQLPSC != nil {
				p.problem(Finding{Code: "database-migration-required", Subject: ResourceSubject, Name: name, Path: base, Message: "A legacy database cannot switch to PSC in place. Create a separate reviewed source."})
			}
			return
		}
	} else if !p.in.Catalog && resource.CloudSQLPSC == nil {
		return
	}
	if cfg.Kind != "postgres" && old.Managed == nil {
		return
	}
	var endpoint provider.CloudSQLSourceEndpoint
	if resource.CloudSQLPSC != nil {
		endpoint = *resource.CloudSQLPSC
		if !endpoint.Valid(cfg.Region) {
			p.problem(Finding{Code: "managed-profile-invalid", Subject: ResourceSubject, Name: name, Path: base + ".cloud_sql_psc", Message: "Choose a canonical manual PSC endpoint in the database region."})
			return
		}
	}
	profile, err := manageddb.PostgresSource(cfg.Provider, cfg.Version, cfg.Region, endpoint)
	if err != nil || cfg.Kind != "postgres" {
		p.problem(Finding{Code: "managed-profile-unavailable", Subject: ResourceSubject, Name: name, Path: base, Message: "This database has no reviewed managed profile. Moving or upgrading it needs a tested restore into a fresh service."})
		return
	}
	prov, err := p.in.Providers.Get(cfg.Provider)
	if err != nil {
		return
	}
	conn, ok := p.connection(prov.Cloud)
	if !ok {
		return // Connection selection reports its own blocking finding.
	}
	if conn.Cloud != prov.Cloud {
		p.problem(Finding{Code: "managed-account-unverified", Subject: ResourceSubject, Name: name, Path: base, Message: "Choose a checked connection for this database's cloud provider."})
		return
	}
	identity := connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details)
	if identity == "" {
		p.problem(Finding{Code: "managed-account-unverified", Subject: ResourceSubject, Name: name, Path: base, Message: "Check this cloud connection's account identity before creating a managed database."})
		return
	}
	cfg.Managed = &ManagedDatabaseConfig{Profile: *profile, ConnectionID: conn.ID, AccountIdentity: identity, CloudSQLPSC: endpoint}
	if old.Managed != nil && (binding.Provider != cfg.Provider || binding.Region != cfg.Region || binding.ConnectionID != conn.ID || old.Kind != cfg.Kind || old.Version != cfg.Version || old.Region != cfg.Region || !reflect.DeepEqual(old.Managed, cfg.Managed)) {
		p.problem(Finding{Code: "database-migration-required", Subject: ResourceSubject, Name: name, Path: base, Message: "Moving or upgrading a persistent database needs a tested restore into a fresh service. Its existing data will remain in place."})
	}
}
