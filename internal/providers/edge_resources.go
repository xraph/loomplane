package providers

import (
	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/pkg/provider"
)

func edgeResourceProviders(all []provider.Provider) []provider.Provider {
	setting := func(name, title, pattern, def string, required bool) provider.ResourceSetting {
		return provider.ResourceSetting{Name: name, Title: title, Pattern: pattern, Default: def, Required: required}
	}
	id := `[A-Za-z0-9][A-Za-z0-9_-]{0,254}`
	endpoint := setting("application_endpoint", "Application endpoint ID", id, "", false)
	cloudflareTarget := provider.Provider{Name: "cloudflare-workers", Title: "Cloudflare Workers", Cloud: "cloudflare", Infrastructure: cloudflare.InfrastructureCapabilities(), Model: provider.Build, Available: true, BuildLogs: "unavailable", Description: "Deploys reviewed JavaScript modules with native Cloudflare resource bindings.", Runs: []provider.Runs{{Workload: provider.HTTPService, Execution: provider.Serverless, Creates: "Cloudflare Worker"}, {Workload: provider.WebApp, Execution: provider.Serverless, Creates: "Cloudflare Worker"}}, Provisions: []provider.Provisions{
		{Kind: "cloudflare-d1", Creates: "Cloudflare D1 database", Settings: []provider.ResourceSetting{setting("location", "Location hint", `[a-z]{2,8}`, "", false), endpoint}},
		{Kind: "cloudflare-kv", Creates: "Cloudflare KV namespace", Settings: []provider.ResourceSetting{endpoint}},
		{Kind: "object-storage", Creates: "Cloudflare R2 bucket", Settings: []provider.ResourceSetting{setting("jurisdiction", "Jurisdiction", `default|eu|us|fedramp|fedramp-high`, "default", true), setting("location", "Location hint", `[a-z]{2,8}`, "", false), endpoint}},
		{Kind: "cloudflare-queue", Creates: "Cloudflare Queue", Settings: []provider.ResourceSetting{endpoint}},
		{Kind: "cloudflare-vectorize", Creates: "Cloudflare Vectorize index", Settings: []provider.ResourceSetting{setting("dimensions", "Vector dimensions", `[1-9][0-9]{0,3}`, "", true), setting("metric", "Similarity metric", `cosine|euclidean|dot-product`, "cosine", true), endpoint}},
		{Kind: "cloudflare-hyperdrive", Creates: "Cloudflare Hyperdrive connection", Settings: []provider.ResourceSetting{setting("origin_endpoint", "Verified origin endpoint ID", id, "", true), setting("ca_certificate_id", "Existing Cloudflare CA certificate ID", `[A-Fa-f0-9-]{36}`, "", true)}},
		{Kind: "cloudflare-durable-object", Creates: "Durable Object namespace through its Worker class", ImportOnly: true},
	}}
	for i := range all {
		if all[i].Name == "vercel" {
			all[i].Provisions = append(all[i].Provisions, []provider.Provisions{
				{Kind: "vercel-blob", Creates: "Vercel Blob store", Settings: []provider.ResourceSetting{setting("project_id", "Credential bridge project ID", id, "", true), setting("access", "Access", `private|public`, "private", true)}},
				{Kind: "vercel-global-config", Creates: "Vercel Global Config"},
				{Kind: "vercel-integration", Creates: "Vercel Marketplace resource", Settings: []provider.ResourceSetting{setting("project_id", "Credential bridge project ID", id, "", true), setting("integration_configuration_id", "Integration installation ID", id, "", true), setting("integration_product_id", "Integration product ID or slug", id, "", true), setting("billing_plan_id", "Billing plan ID", id, "", false), setting("metadata", "Provisioning options (JSON)", `\{[\s\S]*\}`, "{}", false), setting("output_keys", "Application output names", `[A-Z][A-Z0-9_]*(,[A-Z][A-Z0-9_]*)*`, "DATABASE_URL", true)}},
			}...)
		}
	}
	return append(all, cloudflareTarget)
}
