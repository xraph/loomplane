package planning

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

func cloudTargetFixture() (*cloudprofiles.Assignment, []ConnectionOption) {
	return &cloudprofiles.Assignment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", CloudID: "cloud", CloudRevision: 3, Revision: 4, Targets: []cloudprofiles.Target{
			{ID: "target-a", OrgID: "org", ProjectID: "project", CloudID: "cloud", Name: "frontend", Provider: "vercel", ConnectionID: "a", Revision: 5},
			{ID: "target-b", OrgID: "org", ProjectID: "project", CloudID: "cloud", Name: "preview", Provider: "vercel", ConnectionID: "b", Revision: 6},
		}}, []ConnectionOption{
			{ID: "a", Cloud: "vercel", Ready: true, Details: map[string]string{"team_id": "team-a"}},
			{ID: "b", Cloud: "vercel", Ready: true, Details: map[string]string{"team_id": "team-b"}},
		}
}

func cloudTargetGraph() *graph.Graph {
	g := infrastructureGraph()
	g.Services[0].Kind = graph.WebApp
	return g
}

func TestCloudTargetsTwoManagedAccountsAndMetadataAdoption(t *testing.T) {
	assigned, conns := cloudTargetFixture()
	original := parse(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: frontend}\n  other: {source: cmd/api, cloud_target: preview}\n")
	before, _ := json.Marshal(original)
	resolved, placements, err := ResolveCloudTargets(original, "production", assigned, conns)
	if err != nil {
		t.Fatal(err)
	}
	input := Input{Environment: "production", Graph: cloudTargetGraph(), Manifest: resolved, Providers: providers.Registry(), Connections: conns, CloudPlacements: placements}
	r := Compute(input)
	if !r.Executable {
		t.Fatalf("named accounts blocked: %+v", r.Problems)
	}
	for name, want := range map[string]string{"web": "a", "other": "b"} {
		a := action(t, r, "service:"+name)
		if a.ConnectionID != want || a.Service.CloudPlacement == nil || a.Service.CloudPlacement.CloudRevision != 3 || a.Service.CloudPlacement.AccountIdentity == "" {
			t.Fatalf("not pinned: %+v", a)
		}
		cfg := *a.Service
		cfg.CloudPlacement = nil
		raw, _ := json.Marshal(cfg)
		input.Bindings = append(input.Bindings, &Binding{ID: name, Subject: ServiceSubject, Name: name, Provider: a.Provider, Region: a.Region, ConnectionID: want, Config: raw})
	}
	adopted := Compute(input)
	for _, a := range adopted.Actions {
		if a.Op != Keep {
			t.Fatalf("adoption redeployed: %+v", a)
		}
	}
	after, _ := json.Marshal(original)
	if string(before) != string(after) {
		t.Fatal("resolution mutated stored manifest")
	}
	pinned := action(t, adopted, "service:web")
	digest, _ := Digest(pinned)
	pinned.Service.CloudPlacement.TargetRevision++
	changed, _ := Digest(pinned)
	if changed == digest {
		t.Fatal("review digest omitted target revision")
	}
	for _, b := range input.Bindings {
		if b.Name == "other" {
			b.ConnectionID = "old-account"
		}
	}
	if a := action(t, Compute(input), "service:other"); a.Op != Replace {
		t.Fatalf("account move was not replacement: %+v", a)
	}
}

func TestCloudTargetsResolutionFailuresAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, doc, message string
		mutate             func(*cloudprofiles.Assignment, []ConnectionOption) *cloudprofiles.Assignment
	}{
		{name: "missing assignment", message: "assigned", mutate: func(_ *cloudprofiles.Assignment, _ []ConnectionOption) *cloudprofiles.Assignment { return nil }},
		{name: "missing target", doc: "cloud_target: missing", message: "missing"},
		{name: "wrong scope", message: "scope", mutate: func(a *cloudprofiles.Assignment, _ []ConnectionOption) *cloudprofiles.Assignment {
			a.Targets[0].OrgID = "other"
			return a
		}},
		{name: "unready", message: "ready", mutate: func(a *cloudprofiles.Assignment, c []ConnectionOption) *cloudprofiles.Assignment {
			c[0].Ready = false
			return a
		}},
		{name: "unverified", message: "verif", mutate: func(a *cloudprofiles.Assignment, c []ConnectionOption) *cloudprofiles.Assignment {
			c[0].Details = nil
			return a
		}},
		{name: "conflict", doc: "cloud_target: frontend, provider: vercel", message: "provider"},
		{name: "direct target conflict", doc: "cloud_target: frontend, target: conn_01m4vewksnea2bwdxwdy5aekhn", message: "target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, c := cloudTargetFixture()
			if tc.mutate != nil {
				a = tc.mutate(a, c)
			}
			doc := tc.doc
			if doc == "" {
				doc = "cloud_target: frontend"
			}
			_, _, err := ResolveCloudTargets(parse(t, "version: 1\nservices:\n  web: {source: cmd/api, "+doc+"}\n"), "production", a, c)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.message) {
				t.Fatalf("expected %s, got %v", tc.message, err)
			}
		})
	}
	a, c := cloudTargetFixture()
	m := parse(t, "version: 1\ndefaults: {provider: vercel}\nservices:\n  web: {source: cmd/api, cloud_target: frontend}\n")
	if _, _, err := ResolveCloudTargets(m, "production", a, c); err == nil {
		t.Fatal("inherited provider conflict accepted")
	}
	m = parse(t, "version: 1\nservices:\n  web:\n    source: cmd/api\n    cloud_target: frontend\n    health_path: /ready\n    environments:\n      staging: {cloud_target: preview, scale: {min: 1, max: 1}}\n")
	resolved, p, err := ResolveCloudTargets(m, "staging", a, c)
	if err != nil || p["web"].ConnectionID != "b" || resolved.Services["web"].HealthPath != "/ready" || resolved.Services["web"].Scale == nil {
		t.Fatalf("supported overrides lost: %+v %+v %v", resolved, p, err)
	}
	legacy := parse(t, "version: 1\nservices:\n  api: {source: cmd/api, provider: server, target: conn_01m4vewksnea2bwdxwdy5aekhn}\n")
	got, p, err := ResolveCloudTargets(legacy, "production", nil, nil)
	if err != nil || len(p) != 0 || got.Services["api"].Target != legacy.Services["api"].Target {
		t.Fatalf("legacy changed: %+v %v", got, err)
	}
}

func TestCloudTargetsResolvedCopyDoesNotShareSettings(t *testing.T) {
	a, c := cloudTargetFixture()
	m := parse(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: frontend, scale: {min: 1, max: 2}, build: {context: app}}\n")
	got, _, err := ResolveCloudTargets(m, "production", a, c)
	if err != nil {
		t.Fatal(err)
	}
	got.Services["web"].Scale.Max = 9
	got.Services["web"].Build.Context = "changed"
	if m.Services["web"].Scale.Max != 2 || m.Services["web"].Build.Context != "app" {
		t.Fatal("resolved manifest shares mutable settings with stored manifest")
	}
}

func TestCloudTargetsRejectMissingRevisions(t *testing.T) {
	a, c := cloudTargetFixture()
	a.Revision = 0
	_, _, err := ResolveCloudTargets(parse(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: frontend}\n"), "production", a, c)
	if err == nil {
		t.Fatal("unversioned assignment produced an unexecutable pin")
	}
}

func TestCloudTargetsManagedPrivateDependenciesStayInAccount(t *testing.T) {
	a, c := cloudTargetFixture()
	for _, cloud := range []string{"gcp", "aws"} {
		t.Run(cloud, func(t *testing.T) {
			providerName := "cloud-run"
			resourceProvider := "cloud-sql"
			kind := "postgres"
			if cloud == "aws" {
				providerName = "ecs-fargate"
				resourceProvider = "rds"
			}
			for i := range a.Targets {
				a.Targets[i].Provider = providerName
				c[i].Cloud = cloud
				c[i].Account = c[i].ID
				c[i].Details = map[string]string{"project_id": c[i].ID}
				if cloud == "aws" {
					c[i].Details["region"] = "us-east-1"
				}
			}
			doc := "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: frontend, needs: [private]}\n  private: {source: cmd/api, cloud_target: preview, public: false}\n"
			m, pins, err := ResolveCloudTargets(parse(t, doc), "production", a, c)
			if err != nil {
				t.Fatal(err)
			}
			r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: m, Providers: providers.Registry(), Connections: c, CloudPlacements: pins})
			if r.Executable {
				t.Fatal("private dependency crossed account boundary")
			}
			doc = "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: frontend, needs: [db]}\nresources:\n  db: {kind: " + kind + ", provider: " + resourceProvider + "}\n"
			m, pins, err = ResolveCloudTargets(parse(t, doc), "production", a, c)
			if err != nil {
				t.Fatal(err)
			}
			r = Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: m, Providers: providers.Registry(), Connections: c, CloudPlacements: pins, Chosen: map[string]string{cloud: "b"}})
			found := false
			for _, problem := range r.Problems {
				if problem.Code == "resource-across-accounts" {
					found = true
				}
			}
			if !found {
				t.Fatalf("resource did not report cross-account placement: %+v", r.Problems)
			}
		})
	}
}

// gpuPoolService asks for one L4 on the pool target edge-host.
const gpuPoolService = "version: 1\nservices:\n  api: {source: cmd/api, public: false, cloud_target: edge-host, size: {cpu: 4, memory: 16384, gpu: {kind: nvidia-l4, count: 1}}}\n"

// gpuEchoStore places every request on one host with the GPUs it asks for.
type gpuEchoStore struct {
	catalogStore
	resolver *stickyResolver
}

func (s gpuEchoStore) PreviewPlacement(_ context.Context, in cloudinfra.PlacementRequest) (*cloudinfra.Placement, error) {
	s.resolver.requests = append(s.resolver.requests, in)
	return &cloudinfra.Placement{PoolID: in.PoolID, ServerID: "host", ConnectionID: targetA, CPUMillis: in.CPUMillis, MemoryBytes: in.MemoryBytes, GPUKind: in.GPUKind, GPUs: in.GPUs}, nil
}

func TestPoolRequestCarriesServiceGPUs(t *testing.T) {
	pooled := edgeHostTarget()
	pooled.PoolID = "pool"
	resolver := &stickyResolver{}
	s := NewService(Deps{Store: stickyStore{resolver: resolver}})
	plan := &Plan{ID: "plan", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"}
	if _, err := s.resolvePools(t.Context(), plan, parse(t, gpuPoolService), edgeAssignment(pooled), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(resolver.requests) != 1 {
		t.Fatalf("pool requests: %+v", resolver.requests)
	}
	if r := resolver.requests[0]; r.GPUKind != "nvidia-l4" || r.GPUs != 1 || r.CPUMillis != 4000 || r.MemoryBytes != 16384<<20 {
		t.Fatalf("GPU service request: %+v", r)
	}
	// A CPU service asks a pool for no GPUs.
	resolver.requests = nil
	if _, err := s.resolvePools(t.Context(), plan, parse(t, directService), edgeAssignment(pooled), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if r := resolver.requests[0]; r.GPUKind != "" || r.GPUs != 0 {
		t.Fatalf("CPU service request: %+v", r)
	}
}

func TestServerGPUs(t *testing.T) {
	for _, tc := range []struct {
		size  *manifest.Size
		kind  string
		count int64
	}{
		{nil, "", 0},
		{&manifest.Size{CPU: 1, Memory: 512}, "", 0},
		{&manifest.Size{CPU: 4, Memory: 16384, GPU: &manifest.GPU{Kind: "nvidia-l4", Count: 2}}, "nvidia-l4", 2},
	} {
		if kind, count := ServerGPUs(tc.size); kind != tc.kind || count != tc.count {
			t.Fatalf("ServerGPUs(%+v) = %q %d, want %q %d", tc.size, kind, count, tc.kind, tc.count)
		}
	}
}

// A pool placement's GPUs reach the pin and the service config. The pool's
// reservation holds them, so planning neither fits them against the
// connection's own GPUs nor gives the service a node selector.
func TestPoolPinCarriesServiceGPUs(t *testing.T) {
	assigned := &cloudprofiles.Assignment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", CloudID: "cloud", CloudRevision: 1, Revision: 1, GrantRevision: 1, Targets: []cloudprofiles.Target{{ID: "target", OrgID: "org", CloudID: "cloud", Name: "shared", Provider: "server", Runtime: "server", PoolID: "pool", Revision: 1}}}
	options := []ConnectionOption{{ID: "runtime", Cloud: "server", Ready: true, Pooled: true, Details: map[string]string{"docker_id": "daemon", "architecture": "amd64"}}}
	m := parse(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: shared, size: {cpu: 4, memory: 16384, gpu: {kind: nvidia-l4, count: 1}}}\n")
	pool := &cloudinfra.Placement{PoolID: "pool", PoolRevision: 3, ServerID: "host", ServerRevision: 2, ConnectionID: "runtime", ConnectionGeneration: 1, CPUMillis: 4000, MemoryBytes: 16384 << 20, GPUKind: "nvidia-l4", GPUs: 1}
	resolved, pins, err := ResolveCloudTargets(m, "production", assigned, options, map[string]*cloudinfra.Placement{"web": pool})
	if err != nil {
		t.Fatal(err)
	}
	pin := pins["web"]
	if pin.GPUKind != "nvidia-l4" || pin.GPUs != 1 || pin.PoolPlacement() != *pool {
		t.Fatalf("pool pin: %+v", pin)
	}
	result := Compute(Input{Environment: "production", Graph: cloudTargetGraph(), Manifest: resolved, Providers: providers.Registry(), Connections: options, CloudPlacements: pins})
	for _, p := range result.Problems {
		if strings.HasPrefix(p.Code, "gpu") {
			t.Fatalf("pooled GPU service checked against its connection: %+v", p)
		}
	}
	a := action(t, result, "service:web")
	want := &provider.GPUPlacement{GPURequest: provider.GPURequest{Kind: "nvidia-l4", Count: 1}}
	if !reflect.DeepEqual(a.Service.GPU, want) || a.Service.CloudPlacement.GPUs != 1 || a.Service.CloudPlacement.GPUKind != "nvidia-l4" {
		t.Fatalf("pooled GPU service: %+v %+v", a.Service.GPU, a.Service.CloudPlacement)
	}
	// A CPU pin's stored shape doesn't change.
	raw, err := json.Marshal(CloudPlacement{PoolID: "pool", CPUMillis: 1000})
	if err != nil || strings.Contains(string(raw), "gpu") {
		t.Fatalf("CPU pin JSON: %s %v", raw, err)
	}
}

// pooledGPUBinding is a deployed service that ran on a GPU server with the
// given size, holding a server GPU placement with a node selector that a
// pool placement can't use.
func pooledGPUBinding(t *testing.T, size *manifest.Size) (*Binding, *cloudprofiles.Assignment, []ConnectionOption) {
	t.Helper()
	binding := appliedTestBinding(t, "api", "original")
	var cfg ServiceConfig
	if err := json.Unmarshal(binding.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Provider, cfg.Public, cfg.Execution, cfg.Workload, cfg.Image = "server", false, provider.Container, provider.Worker, "Dockerfile"
	cfg.Target = "old"
	cfg.TargetSettings = map[string]string{"docker_id": "old", "architecture": "amd64"}
	cfg.Size = size
	cfg.GPU = &provider.GPUPlacement{GPURequest: provider.GPURequest{Kind: "nvidia-h100-80gb", Count: 2}, NodeSelector: map[string]string{"gpu": "h100"}}
	cfg.CloudPlacement.Provider = "server"
	cfg.BuildTarget = &BuildTarget{Builder: "old", Registry: "registry"}
	binding.Provider = "server"
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	binding.Config = raw
	assigned := appliedAssignment("")
	assigned.Targets[0].Provider, assigned.Targets[0].Runtime, assigned.Targets[0].PoolID = "server", "server", "pool"
	options := []ConnectionOption{{ID: "old", Cloud: "server", Ready: true, Details: cfg.TargetSettings}, {ID: "new", Cloud: "server", Ready: true, Details: map[string]string{"docker_id": "new", "architecture": "amd64"}}, {ID: "registry", Cloud: "registry", Ready: true, Details: map[string]string{"endpoint": "registry.example", "repository": "app"}}}
	return binding, assigned, options
}

// Moving a deployed service onto a pool replaces the GPU placement it had
// on its server: the pool's reservation holds its GPUs, by its size alone.
func TestCloudSyncOntoAPoolRecomputesGPUs(t *testing.T) {
	gpuSize := &manifest.Size{CPU: 4, Memory: 16384, GPU: &manifest.GPU{Kind: "nvidia-l4", Count: 1}}
	want := &provider.GPUPlacement{GPURequest: provider.GPURequest{Kind: "nvidia-l4", Count: 1}}
	for name, tc := range map[string]struct {
		size *manifest.Size
		want *provider.GPUPlacement
	}{
		"GPU service": {gpuSize, want},
		"CPU service": {&manifest.Size{CPU: 1, Memory: 512}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			binding, assigned, options := pooledGPUBinding(t, tc.size)
			kind, gpus := ServerGPUs(tc.size)
			cpu, mem, err := ServerResources(tc.size)
			if err != nil {
				t.Fatal(err)
			}
			pin := CloudPlacement{CloudID: "cloud", CloudRevision: 1, AssignmentRevision: 1, TargetID: "target", TargetRevision: 2, TargetName: "frontend", Provider: "server", ConnectionID: "new", AccountIdentity: "server:new", PoolID: "pool", ServerID: "host-new", CPUMillis: cpu, MemoryBytes: mem, GPUKind: kind, GPUs: gpus}
			result := appliedCloudPlan([]*Binding{binding}, assigned, "", options, providers.Registry(), map[string]CloudPlacement{"api": pin})
			moved := action(t, result, "service:api")
			if !reflect.DeepEqual(moved.Service.GPU, tc.want) {
				t.Fatalf("pool placement GPU: %+v", moved.Service.GPU)
			}
			if moved.Service.CloudPlacement.GPUs != gpus || moved.Service.CloudPlacement.GPUKind != kind {
				t.Fatalf("pool pin: %+v", moved.Service.CloudPlacement)
			}

			// Waiting for pool capacity does the same.
			intent := &CapacityIntent{Demand: cloudinfra.CapacityDemand{Architecture: "amd64", Resources: cloudinfra.ResourceVector{CPUMillis: cpu, MemoryBytes: mem, GPUKind: kind, GPUs: gpus}}}
			waiting := appliedCloudPlanWithIntents([]*Binding{binding}, assigned, "", options, providers.Registry(), nil, map[string]*CapacityIntent{"api": intent})
			if a := action(t, waiting, "service:api"); a.Service.CapacityIntent == nil || !reflect.DeepEqual(a.Service.GPU, tc.want) {
				t.Fatalf("capacity intent GPU: %+v %+v", a.Service.CapacityIntent, a.Service.GPU)
			}
		})
	}
}

// A cloud sync asks the pool for the GPUs the deployed size holds, and the
// pin it reviews carries them.
func TestCloudSyncPoolRequestCarriesGPUs(t *testing.T) {
	size := &manifest.Size{CPU: 4, Memory: 16384, GPU: &manifest.GPU{Kind: "nvidia-l4", Count: 1}}
	raw, err := json.Marshal(ServiceConfig{Size: size, CloudPlacement: &CloudPlacement{CloudID: "cloud_1", TargetID: "t1", TargetName: "edge-host", ConnectionID: targetA, PoolID: "pool", ServerID: "host"}})
	if err != nil {
		t.Fatal(err)
	}
	bindings := []*Binding{{ID: "binding", Subject: ServiceSubject, Name: "api", Provider: "server", ConnectionID: targetA, Config: raw}}
	pooled := edgeHostTarget()
	pooled.PoolID = "pool"
	resolver := &stickyResolver{}
	s := NewService(Deps{Store: gpuEchoStore{resolver: resolver}})
	plan := &Plan{ID: "plan", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"}
	pins, err := s.syncPoolPlacements(t.Context(), plan, bindings, edgeAssignment(pooled), nil, []ConnectionOption{{ID: targetA, Cloud: "server", Ready: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolver.requests) != 1 || resolver.requests[0].GPUKind != "nvidia-l4" || resolver.requests[0].GPUs != 1 {
		t.Fatalf("sync pool requests: %+v", resolver.requests)
	}
	if pin := pins["api"]; pin.GPUKind != "nvidia-l4" || pin.GPUs != 1 {
		t.Fatalf("sync pool pin: %+v", pin)
	}
}

// A routed release asks for the GPUs its reviewed pin holds.
func TestRoutedReleaseOntoAPoolCarriesGPUs(t *testing.T) {
	pin := &CloudPlacement{CloudID: "cloud_1", TargetID: "t1", TargetRevision: 5, ConnectionID: targetA, PoolID: "pool", ServerID: "host", CPUMillis: 4000, MemoryBytes: 16384 << 20, GPUKind: "nvidia-l4", GPUs: 1}
	result := &Result{Actions: []Action{{ID: "service:api", Subject: ServiceSubject, Name: "api", Op: Replace, Provider: "server", Service: &ServiceConfig{CloudPlacement: pin}, Routes: []cloudinfra.Route{{ID: "route"}}}}}
	resolver := &stickyResolver{}
	s := NewService(Deps{Store: gpuEchoStore{resolver: resolver}})
	plan := &Plan{ID: "plan", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"}
	if err := s.reserveRoutedPlacements(t.Context(), plan, result, nil); err != nil {
		t.Fatal(err)
	}
	if len(resolver.requests) != 1 || resolver.requests[0].GPUKind != "nvidia-l4" || resolver.requests[0].GPUs != 1 {
		t.Fatalf("routed overlap requests: %+v", resolver.requests)
	}
}

// An accepted move re-previews the service on its destination with the
// old size's CPU and memory, as the move was reviewed, but with the GPUs
// the new size asks for: the runtime labels the container with those.
func TestAcceptedPoolMoveReservesTheNewGPURequest(t *testing.T) {
	pooled := edgeHostTarget()
	pooled.PoolID = "pool"
	old := ServiceConfig{Size: &manifest.Size{CPU: 4, Memory: 16384, GPU: &manifest.GPU{Kind: "nvidia-l4", Count: 2}}, CloudPlacement: &CloudPlacement{CloudID: "cloud_1", TargetID: "t1", PoolID: "pool", ServerID: "host-old", GPUKind: "nvidia-l4", GPUs: 2}}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	bindings := []*Binding{{ID: "binding", Subject: ServiceSubject, Name: "api", Provider: "server", ConnectionID: targetA, Config: raw}}
	resolver := &stickyResolver{}
	s := NewService(Deps{Store: stickyStore{resolver: resolver}})
	plan := &Plan{ID: "plan", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"}
	if _, err = s.resolvePools(t.Context(), plan, parse(t, gpuPoolService), edgeAssignment(pooled), bindings, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(resolver.requests) != 2 {
		t.Fatalf("move requests: %+v", resolver.requests)
	}
	moved := resolver.requests[1]
	if moved.DestinationServerID != "host" || moved.StickyServerID != "host-old" || moved.GPUKind != "nvidia-l4" || moved.GPUs != 1 {
		t.Fatalf("move re-preview: %+v", moved)
	}
}

// A pooled service whose reservation holds a different GPU count or kind
// from its runtime request can't be applied.
func TestPoolGPUMismatchIsAProblem(t *testing.T) {
	assigned := &cloudprofiles.Assignment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", CloudID: "cloud", CloudRevision: 1, Revision: 1, GrantRevision: 1, Targets: []cloudprofiles.Target{{ID: "target", OrgID: "org", CloudID: "cloud", Name: "shared", Provider: "server", Runtime: "server", PoolID: "pool", Revision: 1}}}
	options := []ConnectionOption{{ID: "runtime", Cloud: "server", Ready: true, Pooled: true, Details: map[string]string{"docker_id": "daemon", "architecture": "amd64"}}}
	for name, tc := range map[string]struct {
		manifest  string
		kind      string
		gpus      int64
		want      string
		wantFound bool
	}{
		"matching":        {"gpu: {kind: nvidia-l4, count: 1}", "nvidia-l4", 1, "", false},
		"fewer reserved":  {"gpu: {kind: nvidia-l4, count: 2}", "nvidia-l4", 1, "web asks for 2 nvidia-l4 GPUs, but its pool reservation holds 1 nvidia-l4 GPU. Plan again.", true},
		"other kind":      {"gpu: {kind: nvidia-l4, count: 1}", "nvidia-t4", 1, "web asks for 1 nvidia-l4 GPU, but its pool reservation holds 1 nvidia-t4 GPU. Plan again.", true},
		"CPU reservation": {"gpu: {kind: nvidia-l4, count: 1}", "", 0, "web asks for 1 nvidia-l4 GPU, but its pool reservation holds no GPUs. Plan again.", true},
		"CPU service":     {"", "nvidia-l4", 1, "web asks for no GPUs, but its pool reservation holds 1 nvidia-l4 GPU. Plan again.", true},
	} {
		t.Run(name, func(t *testing.T) {
			size := "size: {cpu: 4, memory: 16384}"
			if tc.manifest != "" {
				size = "size: {cpu: 4, memory: 16384, " + tc.manifest + "}"
			}
			m := parse(t, "version: 1\nservices:\n  web: {source: cmd/api, public: false, cloud_target: shared, "+size+"}\n")
			pool := &cloudinfra.Placement{PoolID: "pool", PoolRevision: 3, ServerID: "host", ServerRevision: 2, ConnectionID: "runtime", ConnectionGeneration: 1, CPUMillis: 4000, MemoryBytes: 16384 << 20, GPUKind: tc.kind, GPUs: tc.gpus}
			resolved, pins, err := ResolveCloudTargets(m, "production", assigned, options, map[string]*cloudinfra.Placement{"web": pool})
			if err != nil {
				t.Fatal(err)
			}
			result := Compute(Input{Environment: "production", Graph: cloudTargetGraph(), Manifest: resolved, Providers: providers.Registry(), Connections: options, CloudPlacements: pins})
			i := slices.IndexFunc(result.Problems, func(f Finding) bool { return f.Code == "gpu-pool-mismatch" })
			if (i >= 0) != tc.wantFound || !tc.wantFound && !result.Executable {
				t.Fatalf("problems: %+v", result.Problems)
			}
			if tc.wantFound && (result.Problems[i].Message != tc.want || result.Problems[i].Path != "services.web.size.gpu" || result.Executable) {
				t.Fatalf("mismatch finding: %+v executable=%v", result.Problems[i], result.Executable)
			}
		})
	}
}
