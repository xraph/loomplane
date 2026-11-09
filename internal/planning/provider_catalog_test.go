package planning

import (
	"testing"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/providers"
)

func TestProviderCatalogUsesExplicitCheckedAccount(t *testing.T) {
	for _, tc := range []struct{ target, cloud, kind string }{
		{"digitalocean", "digitalocean", "postgres"},
		{"digitalocean", "digitalocean", "mysql"},
		{"digitalocean", "digitalocean", "redis"},
		{"azure-database", "azure", "postgres"},
		{"azure-database", "azure", "mysql"},
		{"azure-managed-redis", "azure", "redis"},
		{"render", "render", "postgres"},
		{"render", "render", "redis"},
	} {
		t.Run(tc.target+"/"+tc.kind, func(t *testing.T) {
			m := parse(t, "version: 1\nresources:\n  data: {kind: "+tc.kind+", provider: "+tc.target+", target: conn_selected}\n")
			details := map[string]string{"account_uuid": "account", "subscription_id": "subscription", "tenant_id": "tenant", "owner_id": "owner"}
			in := Input{Catalog: true, Environment: "production", Graph: &graph.Graph{}, Manifest: m, Providers: providers.Registry(), Connections: []ConnectionOption{{ID: "conn_other", Cloud: tc.cloud, Ready: true, Details: details}, {ID: "conn_selected", Cloud: tc.cloud, Ready: true, Details: details}}}
			result := Compute(in)
			if !result.Executable || len(result.Actions) != 1 || result.Actions[0].ConnectionID != "conn_selected" {
				t.Fatalf("native resource account not pinned: %+v", result)
			}
			in.Connections[1].Ready = false
			if Compute(in).Executable {
				t.Fatal("unready explicit account accepted")
			}
			in.Connections[1].Ready, in.Connections[1].Cloud = true, "aws"
			if Compute(in).Executable {
				t.Fatal("account from another provider accepted")
			}
		})
	}
}
