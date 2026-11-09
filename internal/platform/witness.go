package platform

import (
	"slices"

	witnessforge "github.com/hallproof/witness/forge"
	"github.com/xraph/authsome/rbac"

	"github.com/xraph/loomplane/extensions/aiagent"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/checkpoints"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/issuer"
	"github.com/xraph/loomplane/internal/platformadmin"

	// One adapter for each Forgery library Loomplane runs. Each finds its
	// library in the container once the app has started. Bastion and
	// Sentinel aren't here: Loomplane runs neither.
	_ "github.com/hallproof/witness/authsome"
	_ "github.com/hallproof/witness/chronicle"
	_ "github.com/hallproof/witness/cortex"
	_ "github.com/hallproof/witness/dispatch"
	_ "github.com/hallproof/witness/grove"
	_ "github.com/hallproof/witness/keysmith"
	_ "github.com/hallproof/witness/nexus"
	_ "github.com/hallproof/witness/relay"
	_ "github.com/hallproof/witness/shield"
	_ "github.com/hallproof/witness/trove"
	_ "github.com/hallproof/witness/vault"
	_ "github.com/hallproof/witness/warden"
)

// witnessConfig is the config of Hallproof's witness, which pushes
// evidence to the Hallproof at hallproof.endpoint. It scopes witness to
// staff: Loomplane's own authsome app, the Chronicle tenant staff events
// are recorded under, the platform roles, and the organizations named in
// hallproof.staff_orgs.
func witnessConfig(cfg config.Config) witnessforge.Config {
	adapters := map[string]map[string]any{
		// The platform's own Vault apps. Every other Vault app holds
		// customers' credentials and variables.
		"vault": {"apps": []string{
			connections.PlatformOAuthAppsVaultAppID,
			connections.PlatformCloudVaultAppID,
			issuer.VaultAppID,
			checkpoints.VaultAppID,
		}},
		// Shield's settings for the app the agent scans under
		// ("loomplane"). Its tenants are staff orgs only, below.
		"shield": {"app_id": agentscope.App},
	}
	// Staff orgs name what an organization owns that counts as staff
	// evidence. Every library below keys its rows by the Loomplane
	// organization ID, except Nexus. With none, Keysmith and Cortex collect
	// nothing, Relay sends only its all-tenant posture counts and event
	// catalog, Dispatch only what has no org, Shield only the app-wide rows
	// and Nexus only its gateway-wide collectors. That's the safe side:
	// none of them falls back to customers' keys, webhooks, agents or jobs.
	if staff := cfg.Hallproof.StaffOrgs; len(staff) > 0 {
		// Keysmith keys and Relay endpoints carry the org as their tenant
		// (internal/apikeys, internal/notifications).
		adapters["keysmith"] = map[string]any{"staff_tenants": slices.Clone(staff)}
		adapters["relay"] = map[string]any{"staff_tenants": slices.Clone(staff)}
		// Dispatch jobs carry the org of the session that enqueued them.
		adapters["dispatch"] = map[string]any{"staff_orgs": slices.Clone(staff)}
		// The agent binds Shield to the org as its tenant (agentscope).
		adapters["shield"]["tenants"] = slices.Clone(staff)
		// Cortex scopes start at org=<org ID> (agentscope), and the
		// adapter reads one org plus any more in orgs.
		cortex := map[string]any{"org": staff[0], "scope_key": agentscope.LevelOrg}
		if len(staff) > 1 {
			cortex["orgs"] = slices.Clone(staff[1:])
		}
		adapters["cortex"] = cortex
		// Nexus has one tenant per workspace whose agent is on, under a
		// slug that follows from the org ID. The adapter finds tenants by
		// slug and skips one that doesn't exist yet, so naming the slugs
		// here reads only staff orgs' agent keys and budgets.
		tenants := make([]string, 0, len(staff))
		for _, org := range staff {
			tenants = append(tenants, aiagent.TenantSlug(org))
		}
		adapters["nexus"] = map[string]any{"tenants": tenants}
	}
	return witnessforge.Config{
		Endpoint:      cfg.Hallproof.Endpoint,
		IngestKey:     cfg.Hallproof.IngestKey,
		IngestKeyFile: cfg.Hallproof.IngestKeyFile,
		Scope: witnessforge.ScopeConfig{
			AppID:      cfg.Identity.AppID,
			TenantID:   platformadmin.AuditTenant,
			StaffRoles: []string{rbac.PlatformOwnerSlug, rbac.PlatformAdminSlug},
		},
		Adapters: adapters,
	}
}
