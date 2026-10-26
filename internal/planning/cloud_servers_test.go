package planning

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

// edgeHostTarget is a direct target on the targetA server connection.
func edgeHostTarget() cloudprofiles.Target {
	return cloudprofiles.Target{ID: "t1", OrgID: "org", CloudID: "cloud_1", Name: "edge-host", Provider: "server", ConnectionID: targetA, Runtime: "server", Revision: 5}
}

func edgeAssignment(targets ...cloudprofiles.Target) *cloudprofiles.Assignment {
	return &cloudprofiles.Assignment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", CloudID: "cloud_1", CloudRevision: 2, Revision: 3, GrantRevision: 4, Targets: targets}
}

// hostedConnections are the composed fixtures with targetA the runtime
// connection of the given Cloud servers.
func hostedConnections(t *testing.T, servers ...CloudServer) []ConnectionOption {
	t.Helper()
	options := composedConnections(t)
	options[0].Details["docker_id"] = "daemon-a"
	options[0].Servers = servers
	return options
}

var composedIDs = []string{"resource:edge", "resource:edge--migrate", "resource:edge--api", "resource:edge--worker"}

const directService = "version: 1\nservices:\n  api: {source: cmd/api, public: false, cloud_target: edge-host}\n"

func bindingConfig(t *testing.T, a *Action) []byte {
	t.Helper()
	var v any = a.Resource
	if a.Service != nil {
		v = a.Service
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// servingBindings are the bindings of a composed and a service plan.
type servingBindings struct{ composed, service []*Binding }

// directPlans plans the composed edge resource and a service on edge-host
// through options, against what's deployed.
func directPlans(t *testing.T, options []ConnectionOption, bound ...servingBindings) (composed, service *Result) {
	t.Helper()
	var was servingBindings
	if len(bound) > 0 {
		was = bound[0]
	}
	a := edgeAssignment(edgeHostTarget())
	m := parse(t, edgeApp)
	composed = Compute(Input{
		Environment: "production", Graph: infrastructureGraph(), Manifest: m, Providers: providers.Registry(),
		Connections: options, Kinds: recipestest.Registry(t), Bindings: was.composed,
		CloudPlacements: composedPlacements(m, "production", []string{"edge"}, a, options),
	})
	resolved, pins, err := ResolveCloudTargets(parse(t, directService), "production", a, options)
	if err != nil {
		t.Fatal(err)
	}
	service = Compute(Input{
		Environment: "production", Graph: infrastructureGraph(), Manifest: resolved, Providers: infrastructureProviders(t),
		Connections: options, CloudPlacements: pins, Bindings: was.service,
	})
	return composed, service
}

// A composed resource and a service on a direct target whose connection
// is one Cloud server's runtime connection record that server, so a
// domain route can find it. The dependency the composition provisions
// is a database and still carries no placement.
func TestDirectTargetRecordsItsCloudServer(t *testing.T) {
	composed, service := directPlans(t, hostedConnections(t, CloudServer{ID: "server_1", CloudID: "cloud_1"}))
	if !composed.Executable || !service.Executable {
		t.Fatalf("plans blocked: %+v %+v", composed.Problems, service.Problems)
	}
	for _, id := range composedIDs {
		a := findAction(composed, id)
		if a == nil || a.Resource == nil || a.Resource.CloudPlacement == nil || a.Resource.CloudPlacement.ServerID != "server_1" {
			t.Fatalf("%s placement: %+v", id, a)
		}
		if data := bindingConfig(t, a); !bytes.Contains(data, []byte(`"server_id":"server_1"`)) {
			t.Fatalf("%s binding config: %s", id, data)
		}
	}
	if a := findAction(composed, "resource:edge--postgres"); a == nil || bytes.Contains(bindingConfig(t, a), []byte("cloud_placement")) {
		t.Fatalf("provisioned database placed: %+v", a)
	}
	a := findAction(service, "service:api")
	if a == nil || a.Service.CloudPlacement == nil || a.Service.CloudPlacement.ServerID != "server_1" || a.Service.CloudPlacement.PoolID != "" {
		t.Fatalf("service placement: %+v", a)
	}
	if data := bindingConfig(t, a); !bytes.Contains(data, []byte(`"server_id":"server_1"`)) {
		t.Fatalf("service binding config: %s", data)
	}
	for _, r := range []*Result{composed, service} {
		if hasCode(r.Warnings, "cloud-server-ambiguous") {
			t.Fatalf("one server warned: %+v", r.Warnings)
		}
	}
}

// A direct connection that is no Cloud server's runtime connection keeps
// its placement without a server and isn't worth a finding. Neither is a
// server in another Cloud. A connection that runs more than one server is
// ambiguous: nothing is picked, and the plan names the target.
func TestDirectTargetWithoutOneCloudServerRecordsNone(t *testing.T) {
	for name, servers := range map[string][]CloudServer{
		"no server":            nil,
		"another Cloud's":      {{ID: "server_9", CloudID: "cloud_9"}},
		"more than one server": {{ID: "server_1", CloudID: "cloud_1"}, {ID: "server_2", CloudID: "cloud_1"}},
	} {
		t.Run(name, func(t *testing.T) {
			composed, service := directPlans(t, hostedConnections(t, servers...))
			if !composed.Executable || !service.Executable {
				t.Fatalf("plans blocked: %+v %+v", composed.Problems, service.Problems)
			}
			for _, id := range append(composedIDs, "service:api") {
				r := composed
				if id == "service:api" {
					r = service
				}
				a := findAction(r, id)
				data := bindingConfig(t, a)
				if !bytes.Contains(data, []byte(`"cloud_placement":{`)) || bytes.Contains(data, []byte("server_id")) {
					t.Fatalf("%s binding config: %s", id, data)
				}
			}
			ambiguous := len(servers) > 1
			for subject, r := range map[Subject]*Result{ResourceSubject: composed, ServiceSubject: service} {
				w := findCode(r.Warnings, "cloud-server-ambiguous")
				if !ambiguous {
					if w != nil {
						t.Fatalf("%s warned: %+v", subject, w)
					}
					continue
				}
				want := map[Subject]Finding{
					ResourceSubject: {Code: "cloud-server-ambiguous", Subject: ResourceSubject, Name: "edge", Path: "resources.edge.target", Message: "edge's target edge-host is the runtime connection of more than one Cloud server, so Loomplane can't tell which one it runs on, and it can't take domain routes. Give each server its own connection."},
					ServiceSubject:  {Code: "cloud-server-ambiguous", Subject: ServiceSubject, Name: "api", Path: "services.api", Message: "api's target edge-host is the runtime connection of more than one Cloud server, so Loomplane can't tell which one it runs on, and it can't take domain routes. Give each server its own connection."},
				}[subject]
				if w == nil || w.Code != want.Code || w.Subject != want.Subject || w.Name != want.Name || w.Path != want.Path || w.Message != want.Message {
					t.Fatalf("%s ambiguity finding: %+v", subject, r.Warnings)
				}
			}
		})
	}
}

// A pool target keeps the server its capacity resolution chose, whatever
// servers its connection runs.
func TestPoolTargetKeepsItsResolvedServer(t *testing.T) {
	pooled := edgeHostTarget()
	pooled.PoolID = "pool"
	options := hostedConnections(t, CloudServer{ID: "server_1", CloudID: "cloud_1"})
	pool := &cloudinfra.Placement{PoolID: "pool", PoolRevision: 3, ServerID: "host", ServerRevision: 2, ConnectionID: targetA, ConnectionGeneration: 1, CPUMillis: 1000, MemoryBytes: 512 << 20}
	_, pins, err := ResolveCloudTargets(parse(t, directService), "production", edgeAssignment(pooled), options, map[string]*cloudinfra.Placement{"api": pool})
	if err != nil {
		t.Fatal(err)
	}
	if p := pins["api"]; p.PoolID != "pool" || p.ServerID != "host" || p.ServerRevision != 2 {
		t.Fatalf("pool placement: %+v", p)
	}
	if placed := composedPlacements(parse(t, edgeApp), "production", []string{"edge"}, edgeAssignment(pooled), options); len(placed) != 0 {
		t.Fatalf("composed resource placed on a pool: %+v", placed)
	}
}

// A service or composition deployed before its placement recorded the
// server adopts it as metadata: nothing is replaced, updated or
// restarted, and once recorded it's a plain Keep.
func TestDeployedDirectTargetAdoptsItsCloudServer(t *testing.T) {
	composed, service := directPlans(t, hostedConnections(t))
	before := servingBindings{boundAll(t, composed), boundAll(t, service)}
	for _, b := range append(before.composed, before.service...) {
		if bytes.Contains(b.Config, []byte("server_id")) {
			t.Fatalf("%s deployed with a server: %s", b.Name, b.Config)
		}
	}
	options := hostedConnections(t, CloudServer{ID: "server_1", CloudID: "cloud_1"})
	composed, service = directPlans(t, options, before)
	if !composed.Executable || !service.Executable {
		t.Fatalf("plans blocked: %+v %+v", composed.Problems, service.Problems)
	}
	adopting := map[string]*Result{"service:api": service}
	for _, id := range composedIDs {
		adopting[id] = composed
	}
	for id, r := range adopting {
		a := findAction(r, id)
		if a == nil || a.Op != Keep || len(a.Changes) != 0 || !a.AdoptCloudPlacement || a.CloudAdoption == nil || a.CloudAdoption.BindingID != "binding-"+a.Name {
			t.Fatalf("%s doesn't adopt its server as metadata: %+v", id, a)
		}
		if data := bindingConfig(t, a); !bytes.Contains(data, []byte(`"server_id":"server_1"`)) {
			t.Fatalf("%s adopts %s", id, data)
		}
	}
	if a := findAction(composed, "resource:edge--postgres"); a == nil || a.Op != Keep || a.AdoptCloudPlacement || a.CloudAdoption != nil {
		t.Fatalf("provisioned database adopts a placement: %+v", a)
	}
	for _, r := range []*Result{composed, service} {
		for _, a := range r.Actions {
			if a.Op != Keep {
				t.Fatalf("%s redeploys for a server: %+v", a.ID, a)
			}
		}
	}

	composed, service = directPlans(t, options, servingBindings{boundAll(t, composed), boundAll(t, service)})
	for _, r := range []*Result{composed, service} {
		for _, a := range r.Actions {
			if a.Op != Keep || a.AdoptCloudPlacement || a.CloudAdoption != nil {
				t.Fatalf("%s isn't a plain Keep: %+v", a.ID, a)
			}
		}
	}
}

// A service that leaves a direct server for a pool doesn't ask the pool
// to keep it on that server, and isn't a pool relocation: the server it
// left belongs to no pool.
func TestLeavingADirectServerIsntAPoolMove(t *testing.T) {
	old := ServiceConfig{CloudPlacement: &CloudPlacement{CloudID: "cloud_1", TargetID: "t1", ConnectionID: targetA, ServerID: "server_1"}}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	bindings := []*Binding{{ID: "binding", Subject: ServiceSubject, Name: "api", Provider: "server", ConnectionID: targetA, Config: raw}}
	if poolRelocation(bindings, map[string]CloudPlacement{"api": {PoolID: "pool", ServerID: "host"}}) {
		t.Fatal("leaving a direct server relocated within a pool")
	}
	pooled := edgeHostTarget()
	pooled.PoolID = "pool"
	resolver := &stickyResolver{}
	s := NewService(Deps{Store: stickyStore{resolver: resolver}})
	plan := &Plan{ID: "plan", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"}
	if _, err = s.resolvePools(t.Context(), plan, parse(t, directService), edgeAssignment(pooled), bindings, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(resolver.requests) != 1 || resolver.requests[0].StickyServerID != "" {
		t.Fatalf("pool requests: %+v", resolver.requests)
	}
}

type stickyResolver struct{ requests []cloudinfra.PlacementRequest }

type stickyStore struct {
	catalogStore
	resolver *stickyResolver
}

func (s stickyStore) PreviewPlacement(_ context.Context, in cloudinfra.PlacementRequest) (*cloudinfra.Placement, error) {
	s.resolver.requests = append(s.resolver.requests, in)
	return &cloudinfra.Placement{PoolID: in.PoolID, ServerID: "host", ConnectionID: targetA}, nil
}

// hostedStore reads which Cloud servers each connection runs.
type hostedStore struct {
	catalogStore
	hosts map[string]ConnectionHost
}

func (s hostedStore) ConnectionHosts(context.Context, string) (map[string]ConnectionHost, error) {
	return s.hosts, nil
}

// Planning reads the servers behind each connection from the store and
// records the one a composed resource's direct target runs on.
func TestCreatePlanRecordsTheCloudServerOfADirectTarget(t *testing.T) {
	resolver := &targetResolver{assignment: edgeAssignment(edgeHostTarget())}
	service := NewService(Deps{
		Store:     hostedStore{hosts: map[string]ConnectionHost{targetA: {Servers: []CloudServer{{ID: "server_1", CloudID: "cloud_1"}}}}},
		Manifests: catalogManifest{revision: &manifests.Revision{Manifest: parse(t, edgeApp)}}, Providers: providers.Registry(),
		Connections: edgeClouds{}, Kinds: recipestest.Registry(t), CloudProfiles: resolver,
	})
	result, _, err := service.compute(t.Context(), &Plan{Catalog: true, OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"})
	if err != nil || !result.Executable {
		t.Fatalf("plan: %+v, %v", result, err)
	}
	for _, id := range composedIDs {
		if a := findAction(result, id); a == nil || !bytes.Contains(bindingConfig(t, a), []byte(`"server_id":"server_1"`)) {
			t.Fatalf("%s placement: %+v", id, a)
		}
	}
}

// A routed release onto a pool keeps the candidate on its old server only
// when that server was a pool placement's. A direct server's ID is no pool
// member to stick to.
func TestRoutedReleaseOntoAPoolSticksOnlyToAPoolServer(t *testing.T) {
	for name, tc := range map[string]struct {
		old    CloudPlacement
		sticky string
	}{
		"from a direct server": {old: CloudPlacement{CloudID: "cloud_1", TargetID: "t1", ConnectionID: targetA, ServerID: "server_1"}},
		"within the pool":      {old: CloudPlacement{CloudID: "cloud_1", TargetID: "t1", ConnectionID: targetA, PoolID: "pool", ServerID: "host-old"}, sticky: "host-old"},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(ServiceConfig{CloudPlacement: &tc.old})
			if err != nil {
				t.Fatal(err)
			}
			bindings := []*Binding{{ID: "binding", Subject: ServiceSubject, Name: "api", Provider: "server", ConnectionID: targetA, Config: raw}}
			pin := &CloudPlacement{CloudID: "cloud_1", TargetID: "t1", TargetRevision: 5, ConnectionID: targetA, PoolID: "pool", ServerID: "host", CPUMillis: 1000, MemoryBytes: 512 << 20}
			result := &Result{Actions: []Action{{ID: "service:api", Subject: ServiceSubject, Name: "api", Op: Replace, Provider: "server", Service: &ServiceConfig{CloudPlacement: pin}, Routes: []cloudinfra.Route{{ID: "route"}}}}}
			resolver := &stickyResolver{}
			s := NewService(Deps{Store: stickyStore{resolver: resolver}})
			plan := &Plan{ID: "plan", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"}
			if err = s.reserveRoutedPlacements(t.Context(), plan, result, bindings); err != nil {
				t.Fatal(err)
			}
			if len(resolver.requests) != 1 || resolver.requests[0].StickyServerID != tc.sticky {
				t.Fatalf("routed overlap requests: %+v", resolver.requests)
			}
		})
	}
}

// A composed resource on a direct target of the Cloud that can't be placed
// is unverified, which its warning says, unlike one on no target of it.
func TestUnverifiedPlacementsNameResourcesOnTheCloud(t *testing.T) {
	m := parse(t, edgeApp)
	unverifiedAccount := composedConnections(t) // no docker_id: no account identity
	unreviewed := edgeHostTarget()
	unreviewed.Revision = 0
	elsewhere := edgeHostTarget()
	elsewhere.ConnectionID = targetB
	pooled := edgeHostTarget()
	pooled.PoolID = "pool"
	unreviewedCloud := edgeAssignment(edgeHostTarget())
	unreviewedCloud.CloudRevision = 0
	for name, tt := range map[string]struct {
		assignment *cloudprofiles.Assignment
		options    []ConnectionOption
		want       bool
	}{
		"placed":              {edgeAssignment(edgeHostTarget()), hostedConnections(t), false},
		"unverified account":  {edgeAssignment(edgeHostTarget()), unverifiedAccount, true},
		"unreviewed target":   {edgeAssignment(unreviewed), hostedConnections(t), true},
		"unreviewed Cloud":    {unreviewedCloud, hostedConnections(t), true},
		"another target only": {edgeAssignment(elsewhere), hostedConnections(t), false},
		"a pool target":       {edgeAssignment(pooled), hostedConnections(t), false},
		"no assignment":       {nil, hostedConnections(t), false},
	} {
		if got := unverifiedPlacements(m, "production", []string{"edge"}, tt.assignment, tt.options)["edge"]; got != tt.want {
			t.Errorf("%s: unverified %v, want %v", name, got, tt.want)
		}
	}
}

// A routed component can't be released with its routes yet: a change to
// it is refused with what to do, and an unchanged one plans as before.
func TestChangingARoutedComponentIsRefused(t *testing.T) {
	options := hostedConnections(t, CloudServer{ID: "server_1", CloudID: "cloud_1"})
	first, _ := directPlans(t, options)
	bound := boundAll(t, first)
	routes := []cloudinfra.Route{{ID: "route_1", Status: "ready", Input: cloudinfra.RouteInput{Service: "edge--api", Hostname: "api.example.com", Port: 8080}}}
	plan := func(bindings []*Binding) *Result {
		a := edgeAssignment(edgeHostTarget())
		m := parse(t, edgeApp)
		return Compute(Input{
			Environment: "production", Graph: infrastructureGraph(), Manifest: m, Providers: providers.Registry(),
			Connections: options, Kinds: recipestest.Registry(t), Bindings: bindings, Routes: routes,
			CloudPlacements: composedPlacements(m, "production", []string{"edge"}, a, options),
		})
	}
	if r := plan(bound); hasCode(r.Problems, "route-release") || findAction(r, "resource:edge--api").Op != Keep {
		t.Fatalf("an unchanged routed component: %+v %+v", findAction(r, "resource:edge--api"), r.Problems)
	}
	// An older image is bound, so the plan changes the api.
	for _, b := range bound {
		if b.Name == "edge--api" {
			b.Config = bytes.Replace(b.Config, []byte(`"image":"registry.example/sample-api@sha256:`), []byte(`"image":"registry.example/sample-api-old@sha256:`), 1)
		}
	}
	r := plan(bound)
	if api := findAction(r, "resource:edge--api"); api == nil || api.Op == Keep {
		t.Fatalf("the api didn't change: %+v", api)
	}
	f := findCode(r.Problems, "route-release")
	if f == nil || r.Executable || f.Subject != ResourceSubject || f.Name != "edge--api" ||
		f.Message != "edge--api has domain routes. Remove them before changing it: routed component releases aren't supported yet." {
		t.Fatalf("problems %+v", r.Problems)
	}
	if n := len(slices.DeleteFunc(slices.Clone(r.Problems), func(f Finding) bool { return f.Code != "route-release" })); n != 1 {
		t.Fatalf("%d route-release problems", n)
	}
}
