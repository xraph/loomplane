package platform

import (
	witnessforge "github.com/hallproof/witness/forge"
	"github.com/xraph/authsome/rbac"

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
// are recorded under, and the platform roles.
func witnessConfig(cfg config.Config) witnessforge.Config {
	return witnessforge.Config{
		Endpoint:      cfg.Hallproof.Endpoint,
		IngestKey:     cfg.Hallproof.IngestKey,
		IngestKeyFile: cfg.Hallproof.IngestKeyFile,
		Scope: witnessforge.ScopeConfig{
			AppID:      cfg.Identity.AppID,
			TenantID:   platformadmin.AuditTenant,
			StaffRoles: []string{rbac.PlatformOwnerSlug, rbac.PlatformAdminSlug},
		},
		// Keysmith, Relay, Cortex and Dispatch get no staff lists yet,
		// because Loomplane has no staff organization to name. Until it
		// does, Keysmith and Cortex collect nothing, Relay sends only its
		// all-tenant posture counts and event catalog, and Dispatch only
		// what needs no org. That's the safe side: none of them falls back
		// to customers' keys, webhooks, agents or jobs.
		//
		// Dispatch and Cortex stay out for now. Their adapters resolve the
		// engine from the container, and the container starts an engine
		// provided by a constructor the first time anyone resolves it. Their
		// extensions start their engines themselves, so that would be a
		// second Start: a second set of Dispatch workers and cron, and a
		// LISTEN connection nothing stops, which holds shutdown until its
		// deadline. Put them back once witness reads those engines without
		// starting them.
		Exclude: []string{"cortex", "dispatch"},
		Adapters: map[string]map[string]any{
			// The platform's own Vault apps. Every other Vault app holds
			// customers' credentials and variables.
			"vault": {"apps": []string{
				connections.PlatformOAuthAppsVaultAppID,
				connections.PlatformCloudVaultAppID,
				issuer.VaultAppID,
				checkpoints.VaultAppID,
			}},
			// Shield's settings for the app the agent scans under
			// ("loomplane"), and no tenant's: each tenant is a customer.
			"shield": {"app_id": agentscope.App},
		},
	}
}
