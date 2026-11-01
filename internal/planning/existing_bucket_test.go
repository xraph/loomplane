package planning

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/providerresources"
)

func existingBucketInput(t *testing.T) Input {
	t.Helper()
	in := existingInput(t)
	record := in.ExistingResources[syncedResource].Resource
	*record = providerresources.ProviderResource{ID: syncedResource, Provider: "aws", Kind: "object-storage", Name: "assets-bucket", ExternalID: "arn:aws:s3:::assets-bucket", Region: "us-east-1", Scope: "account/123456789012/regions/us-east-1/managed-resources", ConnectionID: "conn_aws", AccountIdentity: connections.AccountIdentity(connections.AWS, "123456789012", map[string]string{"region": "account-inventory"}), EndpointID: "endpoint-1", EndpointRevision: 1, Revision: 3}
	in.ExistingResources[syncedResource] = ExistingResourceCheck{Resource: record, Endpoint: &catalogconnect.Endpoint{ID: "endpoint-1", Engine: catalogconnect.EngineS3, Revision: 1, Host: "s3.us-east-1.amazonaws.com", Database: "assets-bucket", Port: 443, TLS: catalogconnect.TLSVerifyFull}}
	in.Manifest = parse(t, "version: 1\nresources:\n  assets: {kind: object-storage, existing_resource: "+syncedResource+"}\n")
	in.Connections = []ConnectionOption{{ID: "conn_aws", Cloud: "aws", Ready: true}}
	return in
}
func TestExistingBucketPlansLogicalNonOwningAttachment(t *testing.T) {
	got := Compute(existingBucketInput(t))
	if !got.Executable || len(got.Actions) != 1 || got.Actions[0].Resource.ExistingResource == nil || got.Actions[0].Resource.ExistingResource.Incarnation != "" || got.Actions[0].Provider != "s3" {
		t.Fatalf("bucket not attached by logical identity: %+v", got)
	}
}
func TestExistingBucketsPublishScopedOutputLinks(t *testing.T) {
	in := existingBucketInput(t)
	in.Catalog = false
	in.Graph = infrastructureGraph()
	in.Connections = append(in.Connections, infrastructureConnections(t)...)
	const other = "providerresource_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	second := *in.ExistingResources[syncedResource].Resource
	second.ID = other
	second.Name = "logs-bucket"
	second.ExternalID = "arn:aws:s3:::logs-bucket"
	second.EndpointID = "endpoint-2"
	endpoint := *in.ExistingResources[syncedResource].Endpoint
	endpoint.ID = "endpoint-2"
	endpoint.Database = "logs-bucket"
	in.ExistingResources[other] = ExistingResourceCheck{Resource: &second, Endpoint: &endpoint}
	in.Manifest = parse(t, "version: 1\nresources:\n  assets: {kind: object-storage, existing_resource: "+syncedResource+"}\n  logs: {kind: object-storage, existing_resource: "+other+"}\nservices:\n  api: {source: cmd/api, provider: server, target: "+targetA+", endpoint: {host_port: 8080}, needs: [assets, logs]}\n")
	in.ExistingResourceChecks = map[string]EndpointCheck{"api:assets": {Report: &catalogconnect.Report{}}, "api:logs": {Report: &catalogconnect.Report{}}}
	got := Compute(in)
	if !got.Executable {
		t.Fatalf("bucket links blocked: %+v", got.Problems)
	}
	keys := map[string]EnvLink{}
	for _, action := range got.Actions {
		if action.Name == "api" && action.Service != nil {
			for _, link := range action.Service.Env {
				if _, dup := keys[link.Key]; dup {
					t.Fatalf("colliding bucket variable: %s", link.Key)
				}
				keys[link.Key] = link
			}
		}
	}
	for _, name := range []string{"ASSETS", "LOGS"} {
		for _, field := range catalogconnect.BucketEnvironment(catalogconnect.EngineS3) {
			link, ok := keys[name+"_"+field]
			if !ok || link.From != strings.ToLower(name) {
				t.Fatalf("missing namespaced credential %s_%s: %+v", name, field, keys)
			}
			expected := "output:" + field
			if field == "STORAGE_URL" {
				expected = ""
			}
			if link.Field != expected {
				t.Fatalf("incorrect output mapping: %+v", link)
			}
		}
	}
}
func TestExistingResourceStaticSiteCannotReceiveRuntimeCredentials(t *testing.T) {
	in := existingBucketInput(t)
	in.Catalog = false
	in.Graph = infrastructureGraph()
	in.Connections = append(in.Connections, infrastructureConnections(t)...)
	in.Manifest = parse(t, "version: 1\nresources:\n  assets: {kind: object-storage, existing_resource: "+syncedResource+"}\nservices:\n  api: {source: cmd/api, kind: static-site, provider: server, target: "+targetA+", needs: [assets]}\n")
	in.ExistingResourceChecks = map[string]EndpointCheck{"api:assets": {Report: &catalogconnect.Report{}}}
	got := Compute(in)
	if !hasCode(got.Problems, "external-resource-runtime-required") {
		t.Fatalf("static credentials not rejected explicitly: %+v", got.Problems)
	}
}

func TestExternalCredentialRotationRedeploysKeptConsumers(t *testing.T) {
	for _, bucket := range []bool{false, true} {
		t.Run(map[bool]string{false: "database", true: "bucket"}[bucket], func(t *testing.T) {
			in := existingInput(t)
			resourceName, kind := "db", "postgres"
			if bucket {
				in = existingBucketInput(t)
				resourceName, kind = "assets", "object-storage"
			}
			in.Catalog = false
			in.Graph = infrastructureGraph()
			in.Connections = append(in.Connections, infrastructureConnections(t)...)
			in.Manifest = parse(t, "version: 1\nresources:\n  "+resourceName+": {kind: "+kind+", existing_resource: "+syncedResource+"}\nservices:\n  api: {source: cmd/api, provider: server, target: "+targetA+", endpoint: {host_port: 8080}, needs: ["+resourceName+"]}\n")
			in.ExistingResourceChecks = map[string]EndpointCheck{"api:" + resourceName: {Report: &catalogconnect.Report{}}}
			first := Compute(in)
			if !first.Executable {
				t.Fatalf("first plan: %+v", first.Problems)
			}
			in.Bindings = deployed(t, first)
			for _, binding := range in.Bindings {
				binding.ConnectionID = action(t, first, actionID(binding.Subject, binding.Name)).ConnectionID
			}
			baseline := Compute(in)
			if action(t, baseline, "resource:"+resourceName).Op != Keep || action(t, baseline, "service:api").Op != Keep {
				t.Fatalf("fixture service not kept: %+v", action(t, baseline, "service:api"))
			}
			check := in.ExistingResources[syncedResource]
			check.Resource.Revision++
			check.Resource.EndpointID = "rotated-endpoint"
			check.Resource.EndpointRevision = 2
			check.Endpoint.ID = "rotated-endpoint"
			check.Endpoint.Revision = 2
			in.ExistingResources[syncedResource] = check
			second := Compute(in)
			if !second.Executable {
				t.Fatalf("rotation plan: %+v", second.Problems)
			}
			for _, action := range second.Actions {
				if action.Name == "api" && action.Op == Keep {
					t.Fatal("service kept the old running credential after reviewed resource rotation")
				}
			}
		})
	}
}

func TestAzureCanaryRequiresDirectReleaseForExternalAccessChanges(t *testing.T) {
	for _, op := range []Op{Keep, Create, Update} {
		for _, ramps := range []bool{false, true} {
			t.Run(string(op)+"/"+map[bool]string{false: "direct-worker", true: "ramping-service"}[ramps], func(t *testing.T) {
				in := releaseInput(t, releaseYAML(canaryRelease))
				in.Connections = []ConnectionOption{{ID: "azure", Cloud: "azure", Ready: true, Details: map[string]string{}}}
				cfg := ServiceConfig{Port: 8080, Env: []EnvLink{{From: "db", Subject: ResourceSubject}}}
				if !ramps {
					cfg.Workload = "worker"
				}
				service := serviceAction("api", "azure-container-apps", Update, cfg)
				dep := Action{ID: "resource:db", Name: "db", Subject: ResourceSubject, Resource: &ResourceConfig{Kind: "postgres", ExistingResource: &ExistingResourceConfig{}}, Op: op}
				result := &Result{Actions: []Action{service, dep}, Executable: true, Connections: map[string]string{"azure": "azure"}}
				planRelease(in, result)
				if result.Release.Ramps(service.ID) != ramps {
					t.Fatalf("fixture selection differs from actual release: %+v", result.Release)
				}
				if hasCode(result.Problems, "canary-external-resource-access") != (ramps && op != Keep) {
					t.Fatalf("resolved canary gate for %s ramps=%v: %+v", op, ramps, result.Problems)
				}
			})
		}
	}
}
