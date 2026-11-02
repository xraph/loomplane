package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/pkg/provider"

	"github.com/xraph/loomplane/pkg/manifest"
)

func locationFixture() LocationPlacementInput {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	cheap, expensive := int64(10), int64(20)
	return LocationPlacementInput{Now: now, OrgID: "org", ProjectID: "project", EnvironmentID: "env", GrantRevision: 2,
		Constraints: manifest.PlacementConstraints{},
		Candidates: []PlacementCandidate{
			{TargetID: "a", LocationID: "la", OrgID: "org", ProjectID: "project", EnvironmentID: "env", TargetRevision: 1, LocationRevision: 1, GrantRevision: 2, Granted: true, Runtime: "server", RuntimeQualified: true, Architecture: "amd64", ReadyCapacity: true, CheckedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute), PriceCheckedAt: now.Add(-time.Minute), PriceExpiresAt: now.Add(time.Minute), IncrementalMicroUSD: &cheap},
			{TargetID: "b", LocationID: "lb", OrgID: "org", ProjectID: "project", EnvironmentID: "env", TargetRevision: 1, LocationRevision: 1, GrantRevision: 2, Granted: true, Runtime: "server", RuntimeQualified: true, Architecture: "arm64", ReadyCapacity: true, CheckedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute), PriceCheckedAt: now.Add(-time.Minute), PriceExpiresAt: now.Add(time.Minute), IncrementalMicroUSD: &expensive},
		}}
}

func TestLocationPlacementHardConstraintsFirst(t *testing.T) {
	in := locationFixture()
	in.Constraints.Architecture = "arm64"
	d, err := EvaluateLocations(in)
	if err != nil || d.TargetID != "b" {
		t.Fatalf("cheaper incompatible candidate chosen: %+v %v", d, err)
	}
}

func TestLocationPlacementStableOrder(t *testing.T) {
	in := locationFixture()
	a, err := EvaluateLocations(in)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(in.Candidates)
	b, err := EvaluateLocations(in)
	if err != nil {
		t.Fatal(err)
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if string(x) != string(y) {
		t.Fatalf("order changed: %s != %s", x, y)
	}
}

func TestLocationPlacementKeepsExisting(t *testing.T) {
	in := locationFixture()
	in.CurrentTargetID = "b"
	d, err := EvaluateLocations(in)
	if err != nil || d.TargetID != "b" || d.MovementRequired {
		t.Fatalf("existing target displaced: %+v %v", d, err)
	}
}

func TestLocationPlacementUnknownResidency(t *testing.T) {
	in := locationFixture()
	in.Constraints.Residency = []string{"US"}
	d, err := EvaluateLocations(in)
	if err != nil || d.TargetID != "" || !slices.Contains(d.Blockers, LocationResidencyUnverified) {
		t.Fatalf("unknown residency accepted: %+v %v", d, err)
	}
}

func TestLocationPlacementNoReachability(t *testing.T) {
	in := locationFixture()
	in.Constraints.Network = []string{"private-database"}
	d, err := EvaluateLocations(in)
	if err != nil || d.TargetID != "" || !slices.Contains(d.Blockers, LocationNetworkUnverified) {
		t.Fatalf("unknown network accepted: %+v %v", d, err)
	}
}

func TestLocationPlacementGrantChanged(t *testing.T) {
	in := locationFixture()
	in.GrantRevision++
	d, err := EvaluateLocations(in)
	if err != nil || d.TargetID != "" || !slices.Contains(d.Blockers, LocationGrantChanged) {
		t.Fatalf("stale grant accepted: %+v %v", d, err)
	}
}

func TestLocationPlacementExactTarget(t *testing.T) {
	in := locationFixture()
	in.ExactTargetID = "a"
	in.Constraints.Architecture = "arm64"
	d, err := EvaluateLocations(in)
	if err != nil || d.TargetID != "" {
		t.Fatalf("exact target substituted: %+v %v", d, err)
	}
}

func TestLocationPlacementUnknownConstraints(t *testing.T) {
	for _, dimension := range []string{"gpu", "storage", "availability", "stale", "taint", "latency", "purchase"} {
		t.Run(dimension, func(t *testing.T) {
			in := locationFixture()
			in.Candidates = in.Candidates[:1]
			switch dimension {
			case "gpu":
				in.Constraints.GPU = &manifest.GPU{Kind: "nvidia-a100", Count: 1}
			case "storage":
				in.Constraints.Storage = []string{"portable"}
			case "availability":
				in.Constraints.Availability = "independent-zone"
			case "stale":
				in.Candidates[0].ExpiresAt = in.Now
			case "taint":
				in.Candidates[0].UnsupportedTaints = true
			case "latency":
				in.Policy.Ranking = "latency"
			case "purchase":
				in.AutomatedPurchase = true
				in.Candidates[0].IncrementalMicroUSD = nil
			}
			d, err := EvaluateLocations(in)
			if err != nil || d.TargetID != "" || len(d.Blockers) == 0 {
				t.Fatalf("unsupported %s accepted: %+v %v", dimension, d, err)
			}
		})
	}
}

func TestLocationPlacementVerifiedResidency(t *testing.T) {
	in := locationFixture()
	in.Constraints.Residency = []string{"US"}
	in.Candidates[1].Residency = []string{"US"}
	in.Candidates[1].ResidencySource = "operator-attested"
	in.Candidates[1].EvidenceRevision = 1
	d, err := EvaluateLocations(in)
	if err != nil || d.TargetID != "b" {
		t.Fatalf("saved attestation not accepted: %+v %v", d, err)
	}
	in.Candidates[1].ResidencySource = "region-name"
	d, err = EvaluateLocations(in)
	if err != nil || d.TargetID != "" {
		t.Fatalf("untrusted residency accepted: %+v %v", d, err)
	}
}

func TestLocationPlacementPolicyDoesNotMoveExisting(t *testing.T) {
	in := locationFixture()
	in.CurrentTargetID = "b"
	in.Policy.PreferredLocations = []string{"la"}
	in.Policy.Revision = 7
	d, err := EvaluateLocations(in)
	if err != nil || d.TargetID != "b" || d.MovementRequired {
		t.Fatalf("policy moved serving placement: %+v %v", d, err)
	}
	in.Constraints.AllowedLocations = []string{"la"}
	d, err = EvaluateLocations(in)
	if err != nil || d.TargetID != "a" || !d.MovementRequired {
		t.Fatalf("new hard constraint has no reviewed move: %+v %v", d, err)
	}
}

func TestLocationPlacementFreshCostAndLatency(t *testing.T) {
	in := locationFixture()
	in.Candidates[0].PriceExpiresAt = in.Now
	d, err := EvaluateLocations(in)
	if err != nil || d.TargetID != "b" {
		t.Fatalf("stale quote ranked as known: %+v %v", d, err)
	}
	in.Policy.Ranking = "latency"
	slow, fast := int64(100), int64(10)
	in.Candidates[0].LatencyMicros = &slow
	in.Candidates[1].LatencyMicros = &fast
	for i := range in.Candidates {
		in.Candidates[i].LatencyCheckedAt = in.Now
		in.Candidates[i].LatencyExpiresAt = in.Now.Add(time.Minute)
	}
	d, err = EvaluateLocations(in)
	if err != nil || d.TargetID != "b" {
		t.Fatalf("measured latency ignored: %+v %v", d, err)
	}
}

type locationTestStore struct {
	Store
	candidates []PlacementCandidate
	policy     cloudprofiles.LocationPolicy
}

func (s locationTestStore) LocationPolicy(context.Context, string, string) (*cloudprofiles.LocationPolicy, error) {
	return &s.policy, nil
}
func (s locationTestStore) LocationCandidate(_ context.Context, _ *Plan, _ *cloudprofiles.Assignment, target cloudprofiles.Target, _ manifest.Settings, _ cloudinfra.PlacementRequest, _ cloudinfra.ClusterWorkloadVector) (PlacementCandidate, error) {
	for _, c := range s.candidates {
		if c.TargetID == target.ID {
			return c, nil
		}
	}
	return PlacementCandidate{}, fmt.Errorf("missing fixture candidate")
}

func TestLocationPlacementPlannerSelection(t *testing.T) {
	in := locationFixture()
	for i := range in.Candidates {
		in.Candidates[i].Provider = "vercel"
		in.Candidates[i].Runtime = "managed"
	}
	store := locationTestStore{candidates: in.Candidates, policy: cloudprofiles.LocationPolicy{Revision: 3, PreferredLocations: []string{"la"}}}
	s := NewService(Deps{Store: store, Providers: providers.Registry()})
	s.now = func() time.Time { return in.Now }
	plan := &Plan{OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"}
	assignment := &cloudprofiles.Assignment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", CloudID: "cloud", GrantRevision: 2, Targets: []cloudprofiles.Target{{ID: "a", Name: "a", Provider: "vercel", Runtime: "managed", LocationID: "la"}, {ID: "b", Name: "b", Provider: "vercel", Runtime: "managed", LocationID: "lb"}}}
	raw, err := json.Marshal(ServiceConfig{Workload: provider.WebApp, CloudPlacement: &CloudPlacement{TargetID: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	bindings := []*Binding{{Subject: ServiceSubject, Name: "api", Config: raw}}
	set := manifest.Settings{Placement: &manifest.PlacementConstraints{}}
	target, selection, err := s.selectLocation(t.Context(), plan, assignment, set, "api", bindings, nil, nil, "", nil, nil, locationRequirements{Workload: provider.WebApp})
	if err != nil || target.ID != "b" || selection.Review.PolicyRevision != 3 || selection.Decision.MovementRequired {
		t.Fatalf("existing placement moved: %+v %+v %v", target, selection, err)
	}
	set.Placement = &manifest.PlacementConstraints{AllowedLocations: []string{"la"}}
	target, selection, err = s.selectLocation(t.Context(), plan, assignment, set, "api", bindings, nil, nil, "", nil, nil, locationRequirements{Workload: provider.WebApp})
	if err != nil || target.ID != "a" || !selection.Decision.MovementRequired {
		t.Fatalf("reviewed hard constraint move missing: %+v %+v %v", target, selection, err)
	}
	set.CloudTarget = "b"
	if _, _, err = s.selectLocation(t.Context(), plan, assignment, set, "api", bindings, nil, nil, "", nil, nil, locationRequirements{Workload: provider.WebApp}); err == nil {
		t.Fatal("exact target was substituted")
	}
}

func TestLocationPlacementRequiresAssignment(t *testing.T) {
	s := NewService(Deps{Providers: providers.Registry()})
	m := &manifest.Manifest{Services: map[string]manifest.Service{"api": {Settings: manifest.Settings{Provider: "vercel", Placement: &manifest.PlacementConstraints{Residency: []string{"US"}}}}}}
	if _, err := s.resolvePools(t.Context(), &Plan{Environment: "production"}, m, nil, nil, nil, nil); err == nil {
		t.Fatal("constraints bypassed without a Cloud assignment")
	}
}

func TestLocationPlacementAvailabilityNeedsProviderEvidence(t *testing.T) {
	in := locationFixture()
	in.Candidates = in.Candidates[:1]
	in.Constraints.Availability = "independent-zone"
	in.Candidates[0].Availability = "independent-zone"
	d, err := EvaluateLocations(in)
	if err != nil || d.TargetID != "" {
		t.Fatalf("unverified independence accepted: %+v %v", d, err)
	}
	in.Candidates[0].AvailabilityVerified = true
	in.Candidates[0].AvailabilityEvidenceSource = "provider"
	d, err = EvaluateLocations(in)
	if err != nil || d.TargetID != "a" {
		t.Fatalf("provider-qualified independence rejected: %+v %v", d, err)
	}
	in.Candidates[0].UnknownCorrelations = true
	d, err = EvaluateLocations(in)
	if err != nil || d.TargetID != "" {
		t.Fatalf("unknown correlation accepted: %+v %v", d, err)
	}
}

// mixedLocationStore models a heterogeneous pool: an unconstrained or amd64
// second preview chooses the wrong host even after arm64 was evaluated.
type mixedLocationStore struct{ locationTestStore }

func (s mixedLocationStore) PreviewPlacement(_ context.Context, req cloudinfra.PlacementRequest) (*cloudinfra.Placement, error) {
	host := "amd"
	if req.Architecture == "arm64" {
		host = "arm"
	}
	if req.DestinationServerID != "" && req.DestinationServerID != host {
		return nil, fmt.Errorf("destination architecture mismatch")
	}
	return &cloudinfra.Placement{PoolID: req.PoolID, ServerID: host, ConnectionID: host, ConnectionGeneration: 1, CPUMillis: req.CPUMillis, MemoryBytes: req.MemoryBytes}, nil
}

func TestLocationPlacementMaterializationPreservesArchitecture(t *testing.T) {
	in := locationFixture()
	c := in.Candidates[1]
	c.Provider, c.ServerID, c.RuntimeConnectionID, c.ConnectionGeneration = "server", "arm", "arm", 1
	store := mixedLocationStore{locationTestStore{candidates: []PlacementCandidate{c}}}
	s := NewService(Deps{Store: store, Providers: providers.Registry()})
	s.now = func() time.Time { return in.Now }
	m := &manifest.Manifest{Services: map[string]manifest.Service{"api": {Kind: "web-app", Settings: manifest.Settings{Placement: &manifest.PlacementConstraints{Architecture: "arm64"}}}}}
	a := &cloudprofiles.Assignment{CloudID: "cloud", GrantRevision: 2, Targets: []cloudprofiles.Target{{ID: "b", Name: "pool", Provider: "server", PoolID: "mixed"}}}
	raw, err := json.Marshal(ServiceConfig{Workload: provider.WebApp, TargetSettings: map[string]string{"architecture": "amd64"}})
	if err != nil {
		t.Fatal(err)
	}
	selections := map[string]locationSelection{}
	got, err := s.resolvePoolsReviewed(t.Context(), &Plan{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, m, a, []*Binding{{Subject: ServiceSubject, Name: "api", Config: raw}}, nil, nil, selections)
	if err != nil || got["api"].ConnectionID != "arm" || got["api"].ConnectionID != selections["api"].Review.Candidates[0].RuntimeConnectionID {
		t.Fatalf("materialization replaced evaluated arm64 host: %+v %v", got, err)
	}
}

func TestLocationPlacementDesiredWorkloadBeforeRank(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			in := locationFixture()
			in.Candidates[0].Provider, in.Candidates[0].Runtime = "vercel", "managed"
			in.Candidates[1].Provider, in.Candidates[1].Runtime = "render", "managed"
			store := locationTestStore{candidates: in.Candidates, policy: cloudprofiles.LocationPolicy{PreferredProviders: []string{"vercel"}}}
			s := NewService(Deps{Store: store, Providers: providers.Registry()})
			s.now = func() time.Time { return in.Now }
			m := &manifest.Manifest{Services: map[string]manifest.Service{"api": {Source: "cmd/api", Settings: manifest.Settings{Placement: &manifest.PlacementConstraints{}}}}}
			a := &cloudprofiles.Assignment{CloudID: "cloud", GrantRevision: 2, Targets: []cloudprofiles.Target{{ID: "a", Name: "a", Provider: "vercel"}, {ID: "b", Name: "b", Provider: "render"}}}
			var bindings []*Binding
			if existing {
				raw, _ := json.Marshal(ServiceConfig{Workload: provider.WebApp, CloudPlacement: &CloudPlacement{TargetID: "a"}})
				bindings = []*Binding{{Subject: ServiceSubject, Name: "api", Config: raw}}
			}
			selections := map[string]locationSelection{}
			g := cloudTargetGraph()
			g.Services[0].Kind = graph.Worker
			_, err := s.resolvePoolsWithLocationInput(t.Context(), &Plan{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, m, a, bindings, nil, nil, selections, Input{Graph: g})
			if err != nil || selections["api"].Decision.TargetID != "b" {
				t.Fatalf("worker ranked unsupported Vercel: %+v %v", selections, err)
			}
		})
	}
}

func TestLocationPlacementSavedDependencyBeforeRank(t *testing.T) {
	in := locationFixture()
	for i := range in.Candidates {
		in.Candidates[i].Provider = "server"
		in.Candidates[i].RuntimeConnectionID = in.Candidates[i].TargetID
	}
	store := locationTestStore{candidates: in.Candidates}
	s := NewService(Deps{Store: store, Providers: providers.Registry()})
	s.now = func() time.Time { return in.Now }
	m := &manifest.Manifest{Services: map[string]manifest.Service{"api": {Kind: "web-app", Needs: []string{"db"}, Settings: manifest.Settings{Placement: &manifest.PlacementConstraints{}}}}, Resources: map[string]manifest.Resource{"db": {Kind: "postgres", Provider: "server", Target: "b"}}}
	a := &cloudprofiles.Assignment{CloudID: "cloud", GrantRevision: 2, Targets: []cloudprofiles.Target{{ID: "a", Name: "a", Provider: "server", ConnectionID: "a"}, {ID: "b", Name: "b", Provider: "server", ConnectionID: "b"}}}
	raw := json.RawMessage(`{"provider":"server","target":"b","target_settings":{"docker_id":"b"},"catalog":{}}`)
	bindings := []*Binding{{Subject: ResourceSubject, Name: "db", Provider: "server", ConnectionID: "b", Config: raw}}
	options := []ConnectionOption{{ID: "a", Cloud: "server", Ready: true, Details: map[string]string{"docker_id": "a"}}, {ID: "b", Cloud: "server", Ready: true, Details: map[string]string{"docker_id": "b"}}}
	selections := map[string]locationSelection{}
	_, err := s.resolvePoolsReviewed(t.Context(), &Plan{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, m, a, bindings, options, nil, selections)
	if err != nil || selections["api"].Decision.TargetID != "b" {
		t.Fatalf("private dependency did not filter target A: %+v %v", selections, err)
	}
	m.Services["api"].Placement.Network = []string{"same-target"}
	_, err = s.resolvePoolsReviewed(t.Context(), &Plan{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, m, a, bindings, options, nil, selections)
	if err != nil {
		t.Fatalf("first placement had proven same-target dependency: %v", err)
	}
	bindings[0].ProjectID = "foreign"
	if _, err = s.resolvePoolsReviewed(t.Context(), &Plan{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, m, a, bindings, options, nil, selections); err == nil {
		t.Fatal("foreign binding established network reachability")
	}
}

type locationComputeStore struct {
	catalogStore
	mixed        mixedLocationStore
	moveReviewed bool
}

func (s locationComputeStore) LocationPolicy(ctx context.Context, org, cloud string) (*cloudprofiles.LocationPolicy, error) {
	return s.mixed.LocationPolicy(ctx, org, cloud)
}
func (s locationComputeStore) LocationCandidate(ctx context.Context, p *Plan, a *cloudprofiles.Assignment, target cloudprofiles.Target, set manifest.Settings, req cloudinfra.PlacementRequest, c cloudinfra.ClusterWorkloadVector) (PlacementCandidate, error) {
	return s.mixed.LocationCandidate(ctx, p, a, target, set, req, c)
}
func (s locationComputeStore) PreviewPlacement(ctx context.Context, req cloudinfra.PlacementRequest) (*cloudinfra.Placement, error) {
	return s.mixed.PreviewPlacement(ctx, req)
}

func TestLocationPlacementSamePoolMoveKeepsDesiredConfiguration(t *testing.T) {
	in := locationFixture()
	c := in.Candidates[1]
	c.Provider, c.ServerID, c.RuntimeConnectionID, c.ConnectionGeneration = "server", "arm", "arm", 1
	s, plan, resolver := targetPlanningService(t, "version: 1\nservices:\n  web: {source: cmd/api, kind: worker, placement: {architecture: arm64}}\n")
	s.now = func() time.Time { return in.Now }
	resolver.assignment = &cloudprofiles.Assignment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", CloudID: "cloud", CloudRevision: 1, Revision: 1, GrantRevision: 2, Targets: []cloudprofiles.Target{{ID: "b", OrgID: "org", CloudID: "cloud", Name: "mixed", Provider: "server", Runtime: "server", PoolID: "mixed", Revision: 1}}}
	old := ServiceConfig{Provider: "server", Target: "amd", TargetSettings: map[string]string{"docker_id": "amd", "architecture": "amd64"}, Workload: provider.WebApp, Source: "cmd/api", Image: "Dockerfile", CloudPlacement: &CloudPlacement{CloudID: "cloud", TargetID: "b", TargetName: "mixed", PoolID: "mixed", ServerID: "amd", ConnectionID: "amd"}}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	s.store = locationComputeStore{catalogStore: catalogStore{bindings: []*Binding{{ID: "binding", Subject: ServiceSubject, Name: "web", Provider: "server", ConnectionID: "amd", Config: raw}}}, mixed: mixedLocationStore{locationTestStore{candidates: []PlacementCandidate{c}}}}
	s.clouds = targetOptions{connections: []*connections.Connection{{ID: "amd", Cloud: connections.Server, Status: connections.StatusReady, Generation: 1, Details: map[string]string{"docker_id": "amd", "architecture": "amd64"}}, {ID: "arm", Cloud: connections.Server, Status: connections.StatusReady, Generation: 1, Details: map[string]string{"docker_id": "arm", "architecture": "arm64", "build_workers": "dedicated"}}}}
	blocked, _, blockedErr := s.compute(t.Context(), plan)
	if blockedErr != nil || blocked.Executable {
		t.Fatalf("unreviewed move executable: %+v %v", blocked, blockedErr)
	}
	store := s.store.(locationComputeStore)
	store.moveReviewed = true
	s.store = store
	result, _, err := s.compute(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) == 0 {
		t.Fatalf("no reviewed move: %+v", result.Problems)
	}
	a := action(t, result, "service:web")
	if !result.Executable || a.Op != Replace || a.Service.Workload != provider.Worker || a.Service.Placement == nil || a.Service.Placement.Architecture != "arm64" || a.Service.Target != "arm" || !result.LocationPlacements["web"].MovementRequired {
		t.Fatalf("same target move lost desired configuration: %+v %+v problems=%+v", a, result.LocationPlacements, result.Problems)
	}
}

func TestLocationPlacementCurrentPoolLabelDoesNotKeepIneligibleHost(t *testing.T) {
	in := locationFixture()
	for i := range in.Candidates {
		in.Candidates[i].Provider = "server"
		in.Candidates[i].RuntimeConnectionID = "new-" + in.Candidates[i].TargetID
		in.Candidates[i].Architecture = "arm64"
	}
	store := locationTestStore{candidates: in.Candidates}
	s := NewService(Deps{Store: store, Providers: providers.Registry()})
	s.now = func() time.Time { return in.Now }
	a := &cloudprofiles.Assignment{CloudID: "cloud", GrantRevision: 2, Targets: []cloudprofiles.Target{{ID: "a", Name: "a", Provider: "server"}, {ID: "b", Name: "b", Provider: "server", PoolID: "mixed"}}}
	old := ServiceConfig{Target: "old-b", TargetSettings: map[string]string{"architecture": "amd64"}, Workload: provider.Worker, CloudPlacement: &CloudPlacement{TargetID: "b", PoolID: "mixed", ServerID: "old-b"}}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	_, selected, err := s.selectLocation(t.Context(), &Plan{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, a, manifest.Settings{Placement: &manifest.PlacementConstraints{Architecture: "arm64"}}, "api", []*Binding{{Subject: ServiceSubject, Name: "api", Config: raw}}, nil, nil, "", nil, nil, locationRequirements{Workload: provider.Worker})
	if err != nil || selected.Decision.TargetID != "a" || !selected.Decision.MovementRequired {
		t.Fatalf("logical target retained ineligible physical host: %+v %v", selected.Decision, err)
	}
}

func TestLocationPlacementNewPoolDependencyBeforeRank(t *testing.T) {
	for _, scenario := range []string{"public", "private", "applied-private"} {
		t.Run(scenario, func(t *testing.T) {
			public := scenario != "private"
			in := locationFixture()
			for i := range in.Candidates {
				in.Candidates[i].Provider = "server"
				in.Candidates[i].RuntimeConnectionID = in.Candidates[i].TargetID
			}
			in.Candidates[1].RuntimeConnectionID = "amd"
			store := mixedLocationStore{locationTestStore{candidates: in.Candidates}}
			s := NewService(Deps{Store: store, Providers: providers.Registry()})
			s.now = func() time.Time { return in.Now }
			m := &manifest.Manifest{Services: map[string]manifest.Service{
				"dependency": {Kind: "web-app", Settings: manifest.Settings{CloudTarget: "pool", Public: &public}},
				"consumer":   {Kind: "web-app", Needs: []string{"dependency"}, Settings: manifest.Settings{Placement: &manifest.PlacementConstraints{}}},
			}}
			a := &cloudprofiles.Assignment{CloudID: "cloud", GrantRevision: 2, Targets: []cloudprofiles.Target{{ID: "a", Name: "a", Provider: "server", ConnectionID: "a"}, {ID: "b", Name: "pool", Provider: "server", PoolID: "mixed"}}}
			options := []ConnectionOption{{ID: "a", Cloud: "server", Ready: true, Details: map[string]string{"docker_id": "a"}}, {ID: "amd", Cloud: "server", Ready: true, Details: map[string]string{"docker_id": "amd"}}}
			var bindings []*Binding
			if scenario == "applied-private" {
				raw, _ := json.Marshal(ServiceConfig{Provider: "server", Target: "source", Public: false, Workload: provider.WebApp, CloudPlacement: &CloudPlacement{TargetID: "b", PoolID: "mixed", ServerID: "source"}})
				bindings = []*Binding{{Subject: ServiceSubject, Name: "dependency", Config: raw}}
			}
			selections := map[string]locationSelection{}
			_, err := s.resolvePoolsReviewed(t.Context(), &Plan{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, m, a, bindings, options, nil, selections)
			want := "b"
			if public && scenario != "applied-private" {
				want = "a"
			}
			if err != nil || selections["consumer"].Decision.TargetID != want {
				t.Fatalf("new pool dependency public=%v: want=%s selection=%+v err=%v", public, want, selections, err)
			}
		})
	}
}

func TestLocationPlacementMixedMovePreservesAppliedInputs(t *testing.T) {
	in := locationFixture()
	c := in.Candidates[1]
	c.Provider, c.ServerID, c.RuntimeConnectionID, c.ConnectionGeneration = "server", "amd", "amd", 1
	s, plan, resolver := targetPlanningService(t, "version: 1\nservices:\n  web: {source: cmd/api, kind: worker, placement: {}}\n  other: {source: cmd/api, kind: worker, cloud_target: mixed, size: {cpu: 1.5, memory: 768}}\n")
	s.now = func() time.Time { return in.Now }
	resolver.assignment = &cloudprofiles.Assignment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", CloudID: "cloud", CloudRevision: 1, Revision: 1, GrantRevision: 2, Targets: []cloudprofiles.Target{{ID: "b", OrgID: "org", CloudID: "cloud", Name: "mixed", Provider: "server", Runtime: "server", PoolID: "mixed", Revision: 1}}}
	old := ServiceConfig{Provider: "server", Target: "arm", TargetSettings: map[string]string{"docker_id": "arm", "architecture": "amd64"}, Workload: provider.Worker, Repo: "app", CommitSHA: "old", Source: "cmd/api", Image: "Dockerfile", Size: &manifest.Size{CPU: 1, Memory: 512}, CloudPlacement: &CloudPlacement{CloudID: "cloud", TargetID: "b", TargetName: "mixed", PoolID: "mixed", ServerID: "arm", ConnectionID: "arm"}, AppliedInputs: &AppliedInputs{Variables: "vars", ArtifactKey: "artifact", Source: &PlanSource{Alias: "app", CommitSHA: "old"}}}
	raw, _ := json.Marshal(old)
	s.store = locationComputeStore{catalogStore: catalogStore{bindings: []*Binding{{ID: "binding", Subject: ServiceSubject, Name: "other", Provider: "server", ConnectionID: "arm", CommitSHA: "old", Config: raw}}}, mixed: mixedLocationStore{locationTestStore{candidates: []PlacementCandidate{c}}}}
	s.clouds = targetOptions{connections: []*connections.Connection{{ID: "amd", Cloud: connections.Server, Status: connections.StatusReady, Generation: 1, Details: map[string]string{"docker_id": "amd", "architecture": "amd64", "build_workers": "dedicated"}}, {ID: "arm", Cloud: connections.Server, Status: connections.StatusReady, Generation: 1, Details: map[string]string{"docker_id": "arm", "architecture": "amd64", "build_workers": "dedicated"}}}}
	result, _, err := s.compute(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	a := action(t, result, "service:other")
	if !result.Executable || a.Service.Size.CPU != 1 || a.Service.Size.Memory != 512 || a.Service.CommitSHA != "old" || a.Service.CloudPlacement.CPUMillis != 1000 {
		t.Fatalf("mixed move changed applied configuration: %+v problems=%+v", a.Service, result.Problems)
	}
}

func (s locationComputeStore) LocationMoveReviewed(context.Context, *Plan, string, *CloudPlacement, string) (bool, error) {
	return s.moveReviewed, nil
}
