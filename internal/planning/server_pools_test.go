package planning

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestPoolServerResourceDefaultsAndBounds(t *testing.T) {
	cpu, mem, err := ServerResources(nil)
	if err != nil || cpu != 1000 || mem != 512<<20 {
		t.Fatalf("default limits %d %d %v", cpu, mem, err)
	}
	for _, size := range []*manifest.Size{{CPU: 0, Memory: 512}, {CPU: 1, Memory: 0}, {CPU: math.NaN(), Memory: 512}, {CPU: math.Inf(1), Memory: 512}, {CPU: 0.0001, Memory: 512}, {CPU: 0.9999, Memory: 512}} {
		if _, _, err = ServerResources(size); err == nil {
			t.Fatalf("unbounded/invalid size accepted: %+v", size)
		}
	}
}
func TestPoolTargetSelectsRuntimeAndRemainsStable(t *testing.T) {
	assigned := &cloudprofiles.Assignment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", CloudID: "cloud", CloudRevision: 1, Revision: 1, GrantRevision: 1, Targets: []cloudprofiles.Target{{ID: "target", OrgID: "org", CloudID: "cloud", Name: "shared", Provider: "server", Runtime: "server", PoolID: "pool", Revision: 1}}}
	options := []ConnectionOption{{ID: "runtime", Cloud: "server", Ready: true, Pooled: true, Details: map[string]string{"docker_id": "daemon", "architecture": "amd64"}}}
	m := parse(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: shared}\n")
	pool := &cloudinfra.Placement{PoolID: "pool", PoolRevision: 3, ServerID: "host", ServerRevision: 2, ConnectionID: "runtime", ConnectionGeneration: 1, CPUMillis: 1000, MemoryBytes: 512 << 20}
	resolved, pins, err := ResolveCloudTargets(m, "production", assigned, options, map[string]*cloudinfra.Placement{"web": pool})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Services["web"].Target != "runtime" || pins["web"].TargetID != "target" || pins["web"].PoolID != "pool" {
		t.Fatal("pool replaced stable target identity or used compute credentials")
	}
	in := Input{Environment: "production", Graph: cloudTargetGraph(), Manifest: resolved, Providers: providers.Registry(), Connections: options, CloudPlacements: pins}
	first := Compute(in)
	a := action(t, first, "service:web")
	cfg, _ := json.Marshal(a.Service)
	in.Bindings = []*Binding{{ID: "binding", Subject: ServiceSubject, Name: "web", Provider: "server", ConnectionID: "runtime", Config: cfg}}
	second := Compute(in)
	if a = action(t, second, "service:web"); a.Op != Keep || a.AdoptCloudPlacement {
		t.Fatalf("unchanged placement redeployed: %+v", a)
	}
	in.CloudPlacements = nil
	direct := Compute(in)
	found := false
	for _, p := range direct.Problems {
		found = found || p.Code == "pool-target-required"
	}
	if !found {
		t.Fatal("direct pooled host lacked actionable finding")
	}
}
func TestPoolRelocationPreservesAppliedInputsAndPrivateBoundary(t *testing.T) {
	binding := appliedTestBinding(t, "api", "original")
	var cfg ServiceConfig
	if err := json.Unmarshal(binding.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Provider = "server"
	cfg.Public = false
	cfg.Execution = provider.Container
	cfg.Workload = provider.Worker
	cfg.Image = "Dockerfile"
	cfg.Target = "old"
	cfg.TargetSettings = map[string]string{"docker_id": "old", "architecture": "amd64"}
	cfg.Size = &manifest.Size{CPU: 1, Memory: 512}
	cfg.CloudPlacement.Provider = "server"
	cfg.CloudPlacement.PoolID = "pool"
	cfg.CloudPlacement.ServerID = "host-old"
	cfg.BuildTarget = &BuildTarget{Builder: "old", Registry: "registry"}
	binding.Provider = "server"
	binding.Config, _ = json.Marshal(cfg)
	assigned := appliedAssignment("")
	assigned.Targets[0].Provider = "server"
	assigned.Targets[0].Runtime = "server"
	assigned.Targets[0].PoolID = "pool"
	options := []ConnectionOption{{ID: "old", Cloud: "server", Ready: true, Details: cfg.TargetSettings}, {ID: "new", Cloud: "server", Ready: true, Details: map[string]string{"docker_id": "new", "architecture": "amd64"}}, {ID: "registry", Cloud: "registry", Ready: true, Details: map[string]string{"endpoint": "registry.example", "repository": "app"}}}
	pin := *cfg.CloudPlacement
	pin.ConnectionID = "new"
	pin.ServerID = "host-new"
	pin.CPUMillis = 1000
	pin.MemoryBytes = 512 << 20
	route := cloudinfra.Route{ID: "reviewed-route", Status: "ready", Input: cloudinfra.RouteInput{ExternalTTLSeconds: 60, Service: "api"}, Binding: cloudinfra.RouteBinding{BindingID: binding.ID, InstanceID: binding.ExternalID}}
	result := appliedCloudPlan([]*Binding{binding}, assigned, "", options, providers.Registry(), map[string]CloudPlacement{"api": pin}, []cloudinfra.Route{route})
	if !result.Executable {
		t.Fatalf("relocation blocked: %+v", result.Problems)
	}
	moved := action(t, result, "service:api")
	if len(moved.Routes) != 1 || moved.Routes[0].ID != route.ID || moved.Op != Replace || !reflect.DeepEqual(moved.Service.AppliedInputs, cfg.AppliedInputs) || moved.Service.CommitSHA != "original" {
		t.Fatal("relocation replaced serving snapshots")
	}
	withoutRegistry := append([]ConnectionOption{}, options[:2]...)
	blocked := appliedCloudPlan([]*Binding{binding}, assigned, "", withoutRegistry, providers.Registry(), map[string]CloudPlacement{"api": pin})
	if blocked.Executable {
		t.Fatal("remote builder bypassed required registry")
	}
	dependency := appliedTestBinding(t, "dependency", "dep")
	var dep ServiceConfig
	_ = json.Unmarshal(dependency.Config, &dep)
	dep.CloudPlacement = nil
	dep.Provider = "server"
	dep.Target = "old"
	dep.Public = false
	dependency.Provider = "server"
	dependency.Config, _ = json.Marshal(dep)
	cfg.Env = []EnvLink{{Key: "DEP_URL", From: "dependency", Subject: ServiceSubject}}
	binding.Config, _ = json.Marshal(cfg)
	result = appliedCloudPlan([]*Binding{binding, dependency}, assigned, "", options, providers.Registry(), map[string]CloudPlacement{"api": pin})
	if result.Executable {
		t.Fatal("pool implied private connectivity across hosts")
	}
	found := false
	for _, p := range result.Problems {
		found = found || strings.Contains(p.Message, "private dependency")
	}
	if !found {
		t.Fatalf("missing boundary finding: %+v", result.Problems)
	}
}

// A service a promotion deployed with staging's image keeps nothing of
// that image in its binding, but a binding written with one still builds
// its commit again when a pool relocation or Cloud sync move rebuilds its
// action: the image was chosen for the target it ran on.
func TestRelocationAfterAPromotionBuildsAgain(t *testing.T) {
	binding := appliedTestBinding(t, "api", "original")
	var cfg ServiceConfig
	if err := json.Unmarshal(binding.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Provider = "server"
	cfg.Public = false
	cfg.Execution = provider.Container
	cfg.Workload = provider.Worker
	cfg.Image = "Dockerfile"
	cfg.Target = "old"
	cfg.TargetSettings = map[string]string{"docker_id": "old", "architecture": "amd64"}
	cfg.Size = &manifest.Size{CPU: 1, Memory: 512}
	cfg.CloudPlacement.Provider = "server"
	cfg.CloudPlacement.PoolID = "pool"
	cfg.CloudPlacement.ServerID = "host-old"
	cfg.BuildTarget = &BuildTarget{Builder: "old", Registry: "registry"}
	cfg.Artifact = &ReusedArtifact{Reference: "registry.example/app/api@" + artifactDigest, Digest: artifactDigest, CommitSHA: "original", FromEnvironment: "staging"}
	binding.Provider = "server"
	binding.Config, _ = json.Marshal(cfg)
	assigned := appliedAssignment("")
	assigned.Targets[0].Provider = "server"
	assigned.Targets[0].Runtime = "server"
	assigned.Targets[0].PoolID = "pool"
	options := []ConnectionOption{{ID: "old", Cloud: "server", Ready: true, Details: cfg.TargetSettings}, {ID: "new", Cloud: "server", Ready: true, Details: map[string]string{"docker_id": "new", "architecture": "amd64"}}, {ID: "registry", Cloud: "registry", Ready: true, Details: map[string]string{"endpoint": "registry.example", "repository": "app"}}}
	pin := *cfg.CloudPlacement
	pin.ConnectionID = "new"
	pin.ServerID = "host-new"
	pin.CPUMillis = 1000
	pin.MemoryBytes = 512 << 20
	result := appliedCloudPlan([]*Binding{binding}, assigned, "", options, providers.Registry(), map[string]CloudPlacement{"api": pin})
	if !result.Executable {
		t.Fatalf("relocation blocked: %+v", result.Problems)
	}
	moved := action(t, result, "service:api")
	if moved.Op != Replace || moved.Service.CommitSHA != "original" {
		t.Fatalf("relocation %s at %s", moved.Op, moved.Service.CommitSHA)
	}
	if moved.Service.Artifact != nil {
		t.Fatalf("the relocated service deploys %s's image instead of building: %+v", moved.Service.Artifact.FromEnvironment, moved.Service.Artifact)
	}
}

func TestServerPlanPreservesRuntimeLimitsWithoutAutoscaling(t *testing.T) {
	for _, tc := range []struct {
		name, settings      string
		wantCPU, wantMemory int64
		blocked             bool
	}{
		{name: "nondefault", settings: "size: {cpu: 0.2, memory: 128}", wantCPU: 200, wantMemory: 128 << 20},
		{name: "default", wantCPU: 1000, wantMemory: 512 << 20},
		{name: "single replica", settings: "scale: {min: 1, max: 1}", wantCPU: 1000, wantMemory: 512 << 20},
		{name: "replicas", settings: "scale: {min: 2, max: 2}", blocked: true},
		{name: "autoscaling", settings: "scale: {min: 1, max: 2}", blocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := parse(t, "version: 1\nservices:\n  api:\n    source: cmd/api\n    provider: server\n    target: "+targetA+"\n    public: false\n    "+tc.settings+"\n")
			r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: m, Providers: providers.Registry(), Connections: infrastructureConnections(t)})
			if tc.blocked {
				for _, p := range r.Problems {
					if p.Code == "server-scale" && !r.Executable {
						return
					}
				}
				t.Fatalf("unsupported server scale accepted: %+v", r)
			}
			if !r.Executable {
				t.Fatalf("supported server limits blocked: %+v", r.Problems)
			}
			cfg := action(t, r, "service:api").Service
			cpu, memory, err := ServerResources(cfg.Size)
			if err != nil || cpu != tc.wantCPU || memory != tc.wantMemory {
				t.Fatalf("planned runtime limits = %dm/%d bytes, want %dm/%d bytes: %v", cpu, memory, tc.wantCPU, tc.wantMemory, err)
			}
			if !reflect.DeepEqual(cfg.Scale, m.Services["api"].Scale) {
				t.Fatalf("single-replica scale lost: %+v", cfg.Scale)
			}
			for _, warning := range r.Warnings {
				if warning.Code == "scaling-ignored" {
					t.Fatalf("supported runtime limits reported as ignored: %+v", warning)
				}
			}
		})
	}
}

func TestManagedPlanStillIgnoresUncontrolledSizing(t *testing.T) {
	r := compute(t, "version: 1\nservices:\n  web:\n    source: apps/web\n    provider: vercel\n    size: {cpu: 0.2, memory: 128}\n    scale: {min: 1, max: 2}\n", "production")
	cfg := action(t, r, "service:web").Service
	if cfg.Size != nil || cfg.Scale != nil {
		t.Fatalf("managed runtime received ignored limits: %+v", cfg)
	}
	for _, warning := range r.Warnings {
		if warning.Code == "scaling-ignored" && warning.Name == "web" {
			return
		}
	}
	t.Fatal("managed sizing warning missing")
}
