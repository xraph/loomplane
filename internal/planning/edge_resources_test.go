package planning

import (
	"os"
	"testing"

	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"

	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestWorkerSettingsPinModuleAndCompatibility(t *testing.T) {
	p := &planner{}
	got := p.workerSettings("api", "services.api", manifest.Settings{Worker: &provider.WorkerSettings{Main: "worker.js"}}, &provider.Provider{Name: "cloudflare-workers"})
	if got == nil || got.Main != "worker.js" || got.CompatibilityDate != "2026-11-01" {
		t.Fatal("worker runtime was not pinned")
	}
	bad := p.workerSettings("api", "services.api", manifest.Settings{Worker: &provider.WorkerSettings{Main: "../foreign.js"}}, &provider.Provider{Name: "cloudflare-workers"})
	if bad != nil || len(p.problems) == 0 {
		t.Fatal("worker could read outside reviewed source")
	}
}

func TestProviderResourceSettingsRequireKnownReviewedOptions(t *testing.T) {
	p := &planner{in: Input{Providers: providers.Registry()}}
	offer := provider.Provisions{Kind: "test", Settings: []provider.ResourceSetting{{Name: "dimensions", Title: "Dimensions", Pattern: `[1-9][0-9]*`, Required: true}}}
	got := p.providerResourceSettings("vectors", "resources.vectors", manifest.Resource{Settings: map[string]string{"dimensions": "384"}}, offer)
	if got["dimensions"] != "384" || len(p.problems) != 0 {
		t.Fatal("reviewed option lost")
	}
	p.providerResourceSettings("vectors", "resources.vectors", manifest.Resource{Settings: map[string]string{"token": "secret"}}, offer)
	if len(p.problems) < 2 {
		t.Fatal("unknown credential setting or missing required option accepted")
	}
}

func TestManagedR2PortableConsumerGetsSDKOutputs(t *testing.T) {
	p := &planner{}
	a := &Action{Provider: "vercel", Service: &ServiceConfig{Workload: provider.WebApp, Execution: provider.Serverless}}
	dep := &Action{Resource: &ResourceConfig{Provider: "cloudflare-workers", Kind: "object-storage", Settings: map[string]string{"application_endpoint": "endpoint"}}}
	p.linkEdgeOutputs("api", a, "assets", "services.api.needs", dep, "cloudflare-r2", nil)
	found := false
	for _, link := range a.Service.Env {
		if link.Key == "AWS_SECRET_ACCESS_KEY" && link.Field == "output:AWS_SECRET_ACCESS_KEY" {
			found = true
		}
	}
	if !found || len(p.problems) > 0 {
		t.Fatal("managed R2 application credentials never reach the portable consumer")
	}
}

func TestComputeWorkerBindsManagedResourcesWithinReviewedAccount(t *testing.T) {
	in := Input{Environment: "production", Graph: infrastructureGraph(), Providers: providers.Registry(), Connections: []ConnectionOption{
		{ID: "conn_01m4vewksnea2bwdxwdy5aekhn", Cloud: "cloudflare", Ready: true, Details: map[string]string{"account_id": "account-a"}},
		{ID: "conn_01m4va49zwe9pvk24sm9h8v0ty", Cloud: "cloudflare", Ready: true, Details: map[string]string{"account_id": "account-b"}},
	}}
	manifestDoc := `version: 1
services:
  api:
    source: cmd/api
    provider: cloudflare-workers
    target: conn_01m4vewksnea2bwdxwdy5aekhn
    worker: {main: worker.js}
    needs: [db, assets]
resources:
  db: {kind: cloudflare-d1, provider: cloudflare-workers, target: conn_01m4vewksnea2bwdxwdy5aekhn}
  assets: {kind: object-storage, provider: cloudflare-workers, target: conn_01m4vewksnea2bwdxwdy5aekhn}
`
	in.Manifest = parse(t, manifestDoc)
	result := Compute(in)
	if !result.Executable {
		t.Fatalf("native resource plan blocked: %+v", result.Problems)
	}
	worker := action(t, result, "service:api")
	if worker.Service.Worker == nil || worker.Service.Source != "cmd/api" || worker.Service.Image != "" || worker.Service.Build != "" || len(worker.Service.Env) != 2 {
		t.Fatalf("Worker module or native outputs lost: %+v", worker.Service)
	}
	for _, link := range worker.Service.Env {
		if link.Field != "output:CLOUDFLARE_BINDING" {
			t.Fatalf("native resource used portable credentials: %+v", link)
		}
	}
	db := in.Manifest.Resources["db"]
	db.Target = "conn_01m4va49zwe9pvk24sm9h8v0ty"
	in.Manifest.Resources["db"] = db
	result = Compute(in)
	if result.Executable {
		t.Fatal("native binding crossed accounts")
	}
	found := false
	for _, problem := range result.Problems {
		if problem.Code == "resource-across-accounts" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing account compatibility explanation: %+v", result.Problems)
	}
}

func TestMarketplaceProvisioningMetadataIsReviewedAndCannotContainCredentials(t *testing.T) {
	var offer provider.Provisions
	for _, target := range providers.Catalog() {
		if target.Name == "vercel" {
			for _, provision := range target.Provisions {
				if provision.Kind == "vercel-integration" {
					offer = provision
				}
			}
		}
	}
	settings := map[string]string{"project_id": "prj_bridge", "integration_configuration_id": "icfg_install", "integration_product_id": "postgres", "metadata": `{"region":"iad1","highAvailability":true}`}
	p := &planner{}
	got := p.providerResourceSettings("database", "resources.database", manifest.Resource{Kind: "vercel-integration", Settings: settings}, offer)
	if got["metadata"] == "" || len(p.problems) != 0 {
		t.Fatalf("reviewed provisioning metadata rejected: %+v", p.problems)
	}
	settings["metadata"] = `{"password":"private"}`
	p = &planner{}
	p.providerResourceSettings("database", "resources.database", manifest.Resource{Kind: "vercel-integration", Settings: settings}, offer)
	if len(p.problems) == 0 {
		t.Fatal("application credentials entered public provisioning metadata")
	}
}

func TestCommittedWorkerExampleCanBeAnalyzedAndPlanned(t *testing.T) {
	repository := os.DirFS("../../examples/cloudflare-worker")
	analysis, err := discovery.Analyze(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile("../../examples/cloudflare-worker/loomplane.yaml")
	if err != nil {
		t.Fatal(err)
	}
	result := Compute(Input{Environment: "production", Graph: graph.Build(analysis), Manifest: parse(t, string(manifestBytes)), Providers: providers.Registry(), Connections: []ConnectionOption{{ID: targetA, Cloud: "cloudflare", Ready: true, Details: map[string]string{"account_id": "example-account"}}}})
	if !result.Executable {
		t.Fatalf("committed Worker example cannot deploy: %+v", result.Problems)
	}
	app := action(t, result, "service:app")
	if app.Service.Worker == nil || len(app.Service.Worker.DurableObjects) != 1 || len(app.Service.Env) != 2 {
		t.Fatalf("example lost its Worker classes or bindings: %+v", app.Service)
	}
}
