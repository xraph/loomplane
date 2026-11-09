package planning

import (
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

func inventoryCompositionInput(t *testing.T) Input {
	t.Helper()
	in := existingInput(t)
	in.Manifest = parse(t, "version: 1\nresources:\n  db: {kind: postgres, existing_resource: "+syncedResource+"}\n  app: {kind: sample, provider: server, target: "+targetA+", uses: {postgres: {resource: db}}}\n")
	in.Graph = infrastructureGraph()
	in.Kinds = recipestest.Registry(t)
	in.Connections = append(in.Connections, composedConnections(t)...)
	kind, _ := in.Kinds.Kind("sample")
	dep := kind.Dependencies[0]
	in.Endpoints = map[string]EndpointCheck{endpointKey("endpoint-1", targetA, *dep.External, catalogconnect.ConsumerOf(kind, dep.Name)): {ID: "endpoint-1", Found: true, Engine: "postgres", Report: &catalogconnect.Report{Version: "17.2"}}}
	return in
}
func TestInventoryCompositionDependencyUsesCheckedEndpointAndAttachmentOrder(t *testing.T) {
	in := inventoryCompositionInput(t)
	result := Compute(in)
	if !result.Executable {
		t.Fatalf("inventory composition blocked: %+v", result.Problems)
	}
	parent := findAction(result, "resource:app")
	if parent == nil || parent.Resource.Composition == nil {
		t.Fatal("no composed resource")
	}
	choice := parent.Resource.Composition.Dependencies["postgres"]
	if choice.Mode != DependencyExternal || choice.External != "endpoint-1" || choice.Resource != "db" {
		t.Fatalf("dependency not normalized: %+v", choice)
	}
	if findAction(result, "resource:app--postgres") != nil {
		t.Fatal("inventory reference provisioned another database")
	}
	migrate := findAction(result, "resource:app--migrate")
	if migrate == nil || !slices.Contains(migrate.DependsOn, "resource:db") {
		t.Fatalf("component not fenced by attachment: %+v", migrate)
	}
}
func TestInventoryCompositionDependencyRequiresExactRequirementAndTargetCheck(t *testing.T) {
	for _, scenario := range []string{"missing", "plain-id-only", "wrong-target", "missing-extension", "wrong-tls"} {
		t.Run(scenario, func(t *testing.T) {
			in := inventoryCompositionInput(t)
			kind, _ := in.Kinds.Kind("sample")
			dep := kind.Dependencies[0]
			key := endpointKey("endpoint-1", targetA, *dep.External, catalogconnect.ConsumerOf(kind, dep.Name))
			check := in.Endpoints[key]
			switch scenario {
			case "missing":
				in.Endpoints = nil
			case "plain-id-only":
				in.Endpoints = map[string]EndpointCheck{"endpoint-1": check}
			case "wrong-target":
				in.Endpoints = map[string]EndpointCheck{endpointKey("endpoint-1", targetB, *dep.External, catalogconnect.ConsumerOf(kind, dep.Name)): check}
			case "missing-extension":
				check.Report = &catalogconnect.Report{Version: "17.2", Missing: []string{"timescaledb"}}
				in.Endpoints[key] = check
			case "wrong-tls":
				check.Report = &catalogconnect.Report{Version: "17.2", Problems: []string{"TLS verification failed"}}
				in.Endpoints[key] = check
			}
			result := Compute(in)
			if result.Executable || findCode(result.Problems, "catalog-endpoint-unavailable") == nil && findCode(result.Problems, "catalog-endpoint-incompatible") == nil {
				t.Fatalf("unverified inventory dependency accepted: %+v", result.Problems)
			}
		})
	}
}

func TestInventoryCompositionAccessRotationKeepsPhysicalDependency(t *testing.T) {
	in := inventoryCompositionInput(t)
	first := Compute(in)
	if !first.Executable {
		t.Fatal(first.Problems)
	}
	in.Bindings = boundFrom(t, first)
	observed := in.ExistingResources[syncedResource]
	record := *observed.Resource
	endpoint := *observed.Endpoint
	record.EndpointID = "rotated-endpoint"
	record.EndpointRevision++
	record.Revision++
	endpoint.ID = record.EndpointID
	endpoint.Revision = record.EndpointRevision
	observed.Resource = &record
	observed.Endpoint = &endpoint
	in.ExistingResources[syncedResource] = observed
	kind, _ := in.Kinds.Kind("sample")
	dep := kind.Dependencies[0]
	in.Endpoints = map[string]EndpointCheck{endpointKey(record.EndpointID, targetA, *dep.External, catalogconnect.ConsumerOf(kind, dep.Name)): {ID: record.EndpointID, Found: true, Engine: "postgres", Report: &catalogconnect.Report{Version: "17.2"}}}
	next := Compute(in)
	if !next.Executable {
		t.Fatalf("reviewed access rotation treated as migration: %+v", next.Problems)
	}
	choice := findAction(next, "resource:app").Resource.Composition.Dependencies["postgres"]
	if choice.ExistingResource == nil || choice.ExistingResource.EndpointID != record.EndpointID {
		t.Fatalf("new access revision not pinned: %+v", choice)
	}
}

func TestInventoryCompositionDependencyWorksAcrossKubernetesAndProviderAccount(t *testing.T) {
	in := inventoryCompositionInput(t)
	in.Manifest = parse(t, "version: 1\nresources:\n  db: {kind: postgres, existing_resource: "+syncedResource+"}\n  app: {kind: sample, provider: kubernetes, target: "+targetA+", uses: {postgres: {resource: db}}}\n")
	cluster := clusterWorkloadConnection(t)
	cluster.Details["jobs"] = "ready"
	in.Connections = append(in.Connections[:1:1], cluster)
	result := Compute(in)
	if !result.Executable {
		t.Fatalf("verified inventory endpoint rejected for Kubernetes: %+v", result.Problems)
	}
	choice := findAction(result, "resource:app").Resource.Composition.Dependencies["postgres"]
	attached := findAction(result, "resource:db")
	if choice.ExistingResource == nil || attached == nil || attached.Resource.ExistingResource == nil || *choice.ExistingResource != *attached.Resource.ExistingResource {
		t.Fatalf("inventory proof not preserved: choice=%+v attachment=%+v", choice, attached)
	}
}
