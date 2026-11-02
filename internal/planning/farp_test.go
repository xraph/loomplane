package planning

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/pkg/discovery"
	"github.com/xraph/loomplane/pkg/manifest"
)

// octopusStub is a kind named like Octopus, with fake digests, for planning
// manifests that hold an Octopus resource without the real release.
func octopusStub() recipes.Kind {
	image := recipes.Image{Repository: "registry.example/octopus", Digests: map[string]string{
		"amd64": "sha256:" + strings.Repeat("1", 64),
		"arm64": "sha256:" + strings.Repeat("2", 64),
	}}
	return recipes.Kind{
		Name:        recipes.OctopusKind,
		Title:       "Octopus",
		Description: "A test stand-in for the Octopus gateway.",
		Placement:   recipes.PlacementAppProject,
		Targets:     []string{"server", "kubernetes"},
		Releases: []recipes.Release{{
			Version: "1", Product: "1.0.0", Revision: "1",
			Images: map[string]recipes.Image{recipes.OctopusGateway: image},
		}},
		Components: []recipes.Component{{
			Name:    recipes.OctopusGateway,
			Role:    recipes.RoleProcess,
			Command: []string{"octopus"},
			Ports: []recipes.Port{
				{Name: "http", Container: recipes.OctopusGatewayPort, Protocol: "http", Routable: true},
				{Name: "admin", Container: recipes.OctopusAdminPort, Protocol: "http", Control: true},
			},
			Readiness: &recipes.HTTPProbe{Port: "http", Path: "/readyz"},
		}},
	}
}

// farpKinds holds the Octopus stand-in and the sample kind.
func farpKinds(t *testing.T) *recipes.Registry {
	t.Helper()
	r := recipestest.Registry(t)
	if err := r.Register(octopusStub()); err != nil {
		t.Fatalf("register octopus stand-in: %v", err)
	}
	return r
}

// farpGraph has an HTTP service with a port, and a worker with none.
func farpGraph() *graph.Graph {
	g := infrastructureGraph()
	g.Services = append(g.Services, graph.Service{
		ID: "svc:go:cmd/jobs", Name: "jobs", Kind: graph.Worker, Deployable: true,
		Ecosystem: discovery.Go, Path: "cmd/jobs", Entrypoint: "cmd/jobs", Image: "Dockerfile", Context: ".",
	})
	return g
}

func farpPlan(t *testing.T, doc string) *Result {
	t.Helper()
	return Compute(Input{
		Environment: "production", Graph: farpGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
		Connections: composedConnections(t), Kinds: farpKinds(t),
	})
}

func problemFor(r *Result, code string) *Finding {
	for i := range r.Problems {
		if r.Problems[i].Code == code {
			return &r.Problems[i]
		}
	}
	return nil
}

func octopus(name, target string) string {
	return fmt.Sprintf("  %s: {kind: octopus, provider: server, target: %s}\n", name, target)
}

func billing(extra string) string {
	return fmt.Sprintf("  billing: {source: cmd/api, provider: server, target: %s, public: false%s}\n", targetA, extra)
}

func TestFarpMetadataKeys(t *testing.T) {
	full := manifest.Farp{
		OpenAPI: "/openapi.json", AsyncAPI: "/asyncapi.json", GraphQL: "/graphql", GRPCReflection: true,
		Health: "/health", Metrics: "/metrics", Capabilities: []string{"rest", "websocket"},
	}
	want := map[string]string{
		"farp.openapi.path": "/openapi.json", "farp.asyncapi.path": "/asyncapi.json", "farp.graphql.path": "/graphql",
		"farp.grpc.reflection": "true", "farp.health.path": "/health", "farp.metrics.path": "/metrics",
		"farp.capabilities": "rest,websocket", "farp.port": "8080",
	}
	got := FarpMetadata(full, 8080)
	if !maps.Equal(got, want) {
		t.Fatalf("metadata: %v, want %v", got, want)
	}
	if got := FarpMetadata(manifest.Farp{}, 9000); !maps.Equal(got, map[string]string{"farp.port": "9000"}) {
		t.Fatalf("empty fields made keys: %v", got)
	}
}

func TestFarpPicksTheOnlyOctopus(t *testing.T) {
	r := farpPlan(t, "version: 1\nresources:\n"+octopus("edge", targetA)+"services:\n"+billing(", port: 8080, farp: {}"))
	if !r.Executable {
		t.Fatalf("plan blocked: %+v", r.Problems)
	}
	got := findAction(r, "service:billing").Service.Farp
	want := &FarpConfig{Gateway: "edge", Port: 8080, Metadata: map[string]string{"farp.port": "8080"}}
	if got == nil || got.Gateway != want.Gateway || got.Port != want.Port || !maps.Equal(got.Metadata, want.Metadata) {
		t.Fatalf("farp: %+v, want %+v", got, want)
	}

	// An explicit gateway and port win over the only Octopus and the service's port.
	r = farpPlan(t, "version: 1\nresources:\n"+octopus("edge", targetA)+octopus("inner", targetA)+"services:\n"+
		billing(", port: 8080, farp: {gateway: inner, port: 9000, openapi: /spec, capabilities: [rest]}"))
	if !r.Executable {
		t.Fatalf("plan blocked: %+v", r.Problems)
	}
	got = findAction(r, "service:billing").Service.Farp
	if got == nil || got.Gateway != "inner" || got.Port != 9000 || got.Metadata["farp.openapi.path"] != "/spec" || got.Metadata["farp.port"] != "9000" || got.Metadata["farp.capabilities"] != "rest" {
		t.Fatalf("explicit farp: %+v", got)
	}
}

func TestFarpNeedsOneGateway(t *testing.T) {
	r := farpPlan(t, "version: 1\nservices:\n"+billing(", farp: {}"))
	if p := problemFor(r, "farp-gateway-missing"); p == nil || p.Message != "billing opts in to FARP, but this manifest has no Octopus resource. Add one, or drop the farp block." || p.Name != "billing" || p.Path != "services.billing.farp" {
		t.Fatalf("missing: %+v", r.Problems)
	}
	if r.Executable {
		t.Fatal("a plan with no gateway to feed is executable")
	}

	r = farpPlan(t, "version: 1\nresources:\n"+octopus("edge", targetA)+octopus("inner", targetA)+"services:\n"+billing(", farp: {}"))
	if p := problemFor(r, "farp-gateway-ambiguous"); p == nil || p.Message != "billing opts in to FARP and the manifest has two Octopus resources (edge, inner). Set farp.gateway to pick one." || p.Path != "services.billing.farp.gateway" {
		t.Fatalf("ambiguous: %+v", r.Problems)
	}

	r = farpPlan(t, "version: 1\nresources:\n"+octopus("edge", targetA)+"  db: {kind: postgres, provider: server, target: "+targetA+"}\nservices:\n"+billing(", farp: {gateway: db}"))
	if p := problemFor(r, "farp-gateway-unknown"); p == nil || p.Message != "farp.gateway names db, which isn't an Octopus resource." || p.Path != "services.billing.farp.gateway" {
		t.Fatalf("unknown: %+v", r.Problems)
	}
	r = farpPlan(t, "version: 1\nresources:\n"+octopus("edge", targetA)+"services:\n"+billing(", farp: {gateway: nowhere}"))
	if problemFor(r, "farp-gateway-unknown") == nil {
		t.Fatalf("a gateway that isn't a resource was accepted: %+v", r.Problems)
	}
}

func TestFarpNeedsAPortAndTheSameTarget(t *testing.T) {
	jobs := fmt.Sprintf("  jobs: {source: cmd/jobs, provider: server, target: %s, farp: {}}\n", targetA)
	r := farpPlan(t, "version: 1\nresources:\n"+octopus("edge", targetA)+"services:\n"+jobs)
	if p := problemFor(r, "farp-port-missing"); p == nil || p.Name != "jobs" || p.Path != "services.jobs.farp.port" {
		t.Fatalf("port: %+v", r.Problems)
	}
	// farp.port stands in for a service port.
	jobs = fmt.Sprintf("  jobs: {source: cmd/jobs, provider: server, target: %s, farp: {port: 7000}}\n", targetA)
	if r = farpPlan(t, "version: 1\nresources:\n"+octopus("edge", targetA)+"services:\n"+jobs); !r.Executable {
		t.Fatalf("farp.port refused: %+v", r.Problems)
	}

	r = farpPlan(t, "version: 1\nresources:\n"+octopus("edge", targetB)+"services:\n"+billing(", farp: {}"))
	if p := problemFor(r, "farp-target-mismatch"); p == nil || p.Message != "billing and edge run on different servers or clusters. FARP needs them on the same one." || p.Path != "services.billing.farp" {
		t.Fatalf("target: %+v", r.Problems)
	}
	if a, g := findAction(r, "service:billing"), findAction(r, "resource:edge--gateway"); a == nil || g == nil || a.ConnectionID == g.ConnectionID {
		t.Fatalf("the actions share a connection: %+v, %+v", a, g)
	}
}

func TestFarpRefusesReservedNames(t *testing.T) {
	doc := fmt.Sprintf("version: 1\nresources:\n%sservices:\n  lp-billing: {source: cmd/api, provider: server, target: %s, public: false, farp: {}}\n", octopus("edge", targetA), targetA)
	r := farpPlan(t, doc)
	if p := problemFor(r, "farp-name-reserved"); p == nil || p.Message != "Services that opt in to FARP can't have names starting with lp-, which Loomplane's Octopus upstreams use." || p.Name != "lp-billing" {
		t.Fatalf("reserved: %+v", r.Problems)
	}
	// A reserved name that isn't opted in is the user's business.
	doc = fmt.Sprintf("version: 1\nresources:\n%sservices:\n  lp-billing: {source: cmd/api, provider: server, target: %s, public: false}\n", octopus("edge", targetA), targetA)
	if r = farpPlan(t, doc); problemFor(r, "farp-name-reserved") != nil {
		t.Fatalf("a service without a farp block was refused: %+v", r.Problems)
	}
}

func TestFarpOptOutLeavesConfigClean(t *testing.T) {
	for name, extra := range map[string]string{
		"enabled false": ", farp: {enabled: false}",
		"no block":      "",
		"disabled here": ", farp: {}, environments: {production: {farp: {enabled: false}}}",
	} {
		// There is no Octopus in the manifest: an opted-out service doesn't need one.
		r := farpPlan(t, "version: 1\nservices:\n"+billing(extra))
		if !r.Executable {
			t.Fatalf("%s: plan blocked: %+v", name, r.Problems)
		}
		svc := findAction(r, "service:billing").Service
		if svc.Farp != nil {
			t.Fatalf("%s: farp: %+v", name, svc.Farp)
		}
		data, err := json.Marshal(svc)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "farp") {
			t.Fatalf("%s: config JSON mentions farp: %s", name, data)
		}
	}
}

func TestFarpChangesAreAnUpdate(t *testing.T) {
	doc := "version: 1\nresources:\n" + octopus("edge", targetA) + "services:\n" + billing(", farp: {}")
	first := farpPlan(t, doc)
	if !first.Executable {
		t.Fatalf("plan blocked: %+v", first.Problems)
	}
	raw, err := json.Marshal(findAction(first, "service:billing").Service)
	if err != nil {
		t.Fatal(err)
	}
	bound := &Binding{ID: "binding", ExternalID: "billing", Subject: ServiceSubject, Name: "billing", Provider: "server", ConnectionID: targetA, Config: raw}
	bindings := append(boundFrom(t, first), bound)
	plan := func(doc string) *Result {
		return Compute(Input{
			Environment: "production", Graph: farpGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
			Connections: composedConnections(t), Kinds: farpKinds(t), Bindings: bindings,
		})
	}
	if a := findAction(plan(doc), "service:billing"); a.Op != Keep {
		t.Fatalf("unchanged opt-in: %s %v", a.Op, a.Changes)
	}
	changed := "version: 1\nresources:\n" + octopus("edge", targetA) + "services:\n" + billing(", farp: {openapi: /spec}")
	if a := findAction(plan(changed), "service:billing"); a.Op != Update {
		t.Fatalf("changed farp block: %s", a.Op)
	}
}
