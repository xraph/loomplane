package planning

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/pkg/provider"
)

func gpuConnections(t *testing.T, inv provider.GPUInventory, architecture string) []ConnectionOption {
	t.Helper()
	cs := infrastructureConnections(t)
	for i := range cs {
		cs[i].Details["architecture"] = architecture
		maps.Copy(cs[i].Details, inv.Details())
	}
	return cs
}

var twoL4 = provider.GPUInventory{Status: "ready", Devices: []provider.GPUDevice{{Index: 0, UUID: "GPU-a", Kind: "nvidia-l4", MemoryMiB: 23034}, {Index: 1, UUID: "GPU-b", Kind: "nvidia-l4", MemoryMiB: 23034}}}

// gpuBinding is a deployed component on connection that holds req's GPUs.
func gpuBinding(t *testing.T, name, connection string, req provider.GPURequest) *Binding {
	t.Helper()
	cfg := ResourceConfig{Provider: "server", Target: connection, Kind: "gpusample", Version: "1", Component: &ComponentConfig{Kind: "gpusample", Resource: strings.Split(name, "--")[0], GPU: &provider.GPUPlacement{GPURequest: req}}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &Binding{ID: "binding-" + name, Subject: ResourceSubject, Name: name, Provider: "server", ConnectionID: connection, ExternalID: "instance-" + name, Engine: provider.ResourceEngine, Config: raw}
}

func gpuDoc(gpu string) string {
	return "version: 1\nresources:\n  llm: {kind: gpusample, provider: server, target: " + targetA + gpu + "}\n"
}

func TestPlanResolvesGPUPlacement(t *testing.T) {
	r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, gpuDoc(", gpu: {kind: nvidia-l4, count: 2}")), Providers: providers.Registry(),
		Connections: gpuConnections(t, twoL4, "amd64"), Kinds: recipestest.Registry(t)})
	if !r.Executable {
		t.Fatalf("blocked: %+v", r.Problems)
	}
	if got := findAction(r, "resource:llm--server").Resource.Component.GPU; got == nil || got.Kind != "nvidia-l4" || got.Count != 2 {
		t.Fatalf("placement: %+v", got)
	}
	if got := findAction(r, "resource:llm--sidecar").Resource.Component.GPU; got != nil {
		t.Fatalf("the CPU sidecar got a GPU: %+v", got)
	}
	for _, tc := range []struct {
		name, gpu, architecture, code string
		inv                           provider.GPUInventory
	}{
		{"no choice", "", "amd64", "catalog-gpu-required", twoL4},
		{"too small", ", gpu: {kind: nvidia-t4, count: 1}", "amd64", "catalog-gpu-memory", twoL4},
		{"absent kind", ", gpu: {kind: nvidia-a100-80gb, count: 1}", "amd64", "gpu-kind-unavailable", twoL4},
		{"too many", ", gpu: {kind: nvidia-l4, count: 3}", "amd64", "gpu-count-unavailable", twoL4},
		{"arm64", ", gpu: {kind: nvidia-l4, count: 1}", "arm64", "gpu-architecture", twoL4},
		{"toolkit missing", ", gpu: {kind: nvidia-l4, count: 1}", "amd64", "gpu-unavailable", provider.GPUInventory{Status: "toolkit_missing", Message: "Install the NVIDIA Container Toolkit"}},
		{"unlisted cluster", ", gpu: {kind: nvidia-l4, count: 1}", "amd64", "gpu-unavailable", provider.GPUInventory{Status: "unlisted", Message: "Loomplane can't list this cluster's nodes"}},
		{"amd", ", gpu: {kind: amd-mi300x, count: 1}", "amd64", "gpu-vendor-unsupported", twoL4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, gpuDoc(tc.gpu)), Providers: providers.Registry(),
				Connections: gpuConnections(t, tc.inv, tc.architecture), Kinds: recipestest.Registry(t)})
			if r.Executable || !hasCode(r.Problems, tc.code) {
				t.Fatalf("want %s: %+v", tc.code, r.Problems)
			}
		})
	}
	t.Run("never checked", func(t *testing.T) {
		r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, gpuDoc(", gpu: {kind: nvidia-l4, count: 1}")), Providers: providers.Registry(),
			Connections: composedConnectionsOn(t, "amd64"), Kinds: recipestest.Registry(t)})
		if r.Executable || !hasCode(r.Problems, "gpu-unavailable") {
			t.Fatalf("a server with no GPU inventory took a GPU workload: %+v", r.Problems)
		}
	})
	t.Run("arm64 names the architecture", func(t *testing.T) {
		r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, gpuDoc(", gpu: {kind: nvidia-l4, count: 1}")), Providers: providers.Registry(),
			Connections: gpuConnections(t, twoL4, "arm64"), Kinds: recipestest.Registry(t)})
		if hasCode(r.Problems, "catalog-unavailable") || !slices.ContainsFunc(r.Problems, func(f Finding) bool {
			return f.Code == "gpu-architecture" && strings.Contains(f.Message, "needs an amd64 target")
		}) {
			t.Fatalf("arm64 wasn't reported as the architecture: %+v", r.Problems)
		}
	})
	t.Run("memory message", func(t *testing.T) {
		r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, gpuDoc(", gpu: {kind: nvidia-t4, count: 1}")), Providers: providers.Registry(),
			Connections: gpuConnections(t, twoL4, "amd64"), Kinds: recipestest.Registry(t)})
		i := slices.IndexFunc(r.Problems, func(f Finding) bool { return f.Code == "catalog-gpu-memory" })
		if i < 0 || r.Problems[i].Message != "NVIDIA T4 has 16 GiB per GPU; GPU sample needs at least 20." {
			t.Fatalf("memory problem: %+v", r.Problems)
		}
	})
}

// composedConnectionsOn are the server targets checked as architecture,
// with no GPU inventory.
func composedConnectionsOn(t *testing.T, architecture string) []ConnectionOption {
	t.Helper()
	cs := infrastructureConnections(t)
	for i := range cs {
		cs[i].Details["architecture"] = architecture
	}
	return cs
}

func TestPlanUsesEnvironmentGPU(t *testing.T) {
	doc := "version: 1\nresources:\n  llm:\n    kind: gpusample\n    provider: server\n    target: " + targetA + "\n    gpu: {kind: nvidia-l4, count: 2}\n    environments:\n      production: {gpu: {kind: nvidia-l4, count: 1}}\n"
	r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
		Connections: gpuConnections(t, twoL4, "amd64"), Kinds: recipestest.Registry(t)})
	if !r.Executable {
		t.Fatalf("blocked: %+v", r.Problems)
	}
	if got := findAction(r, "resource:llm--server").Resource.Component.GPU; got == nil || got.Count != 1 {
		t.Fatalf("environment gpu ignored: %+v", got)
	}
}

func TestPlanCountsBoundGPUsOnServer(t *testing.T) {
	held := gpuBinding(t, "other--server", targetA, provider.GPURequest{Kind: "nvidia-l4", Count: 1})
	r := Compute(Input{Environment: "production", Graph: infrastructureGraph(),
		Manifest:  parse(t, "version: 1\nresources:\n  llm: {kind: gpusample, provider: server, target: "+targetA+", gpu: {kind: nvidia-l4, count: 2}}\n"),
		Providers: providers.Registry(), Connections: gpuConnections(t, twoL4, "amd64"), Kinds: recipestest.Registry(t), Bindings: []*Binding{held}})
	if r.Executable || !hasCode(r.Problems, "gpu-count-unavailable") {
		t.Fatalf("held GPU ignored: %+v", r.Problems)
	}
	i := slices.IndexFunc(r.Problems, func(f Finding) bool { return f.Code == "gpu-count-unavailable" })
	if want := targetA + " has 2 NVIDIA L4 GPUs and 1 are already given to other--server."; r.Problems[i].Message != want {
		t.Fatalf("message %q, want %q", r.Problems[i].Message, want)
	}

	// A held GPU on the other server doesn't count here.
	elsewhere := gpuBinding(t, "other--server", targetB, provider.GPURequest{Kind: "nvidia-l4", Count: 2})
	r = Compute(Input{Environment: "production", Graph: infrastructureGraph(),
		Manifest:  parse(t, "version: 1\nresources:\n  llm: {kind: gpusample, provider: server, target: "+targetA+", gpu: {kind: nvidia-l4, count: 2}}\n"),
		Providers: providers.Registry(), Connections: gpuConnections(t, twoL4, "amd64"), Kinds: recipestest.Registry(t), Bindings: []*Binding{elsewhere}})
	if !r.Executable {
		t.Fatalf("a GPU held on another server blocked this one: %+v", r.Problems)
	}

	// Two workloads in one plan can't both have the same GPU.
	two := "version: 1\nresources:\n  llm: {kind: gpusample, provider: server, target: " + targetA + ", gpu: {kind: nvidia-l4, count: 1}}\n  second: {kind: gpusample, provider: server, target: " + targetA + ", gpu: {kind: nvidia-l4, count: 2}}\n"
	r = Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, two), Providers: providers.Registry(),
		Connections: gpuConnections(t, twoL4, "amd64"), Kinds: recipestest.Registry(t)})
	if r.Executable || !hasCode(r.Problems, "gpu-count-unavailable") {
		t.Fatalf("two workloads shared a GPU: %+v", r.Problems)
	}

	// The workload's own binding is what it updates, not a holder.
	first := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, gpuDoc(", gpu: {kind: nvidia-l4, count: 2}")), Providers: providers.Registry(),
		Connections: gpuConnections(t, twoL4, "amd64"), Kinds: recipestest.Registry(t)})
	r = Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, gpuDoc(", gpu: {kind: nvidia-l4, count: 2}")), Providers: providers.Registry(),
		Connections: gpuConnections(t, twoL4, "amd64"), Kinds: recipestest.Registry(t), Bindings: boundFrom(t, first)})
	if !r.Executable {
		t.Fatalf("a workload counted its own GPUs: %+v", r.Problems)
	}
}

func TestPlanShowsGPUChange(t *testing.T) {
	first := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, gpuDoc(", gpu: {kind: nvidia-l4, count: 2}")), Providers: providers.Registry(),
		Connections: gpuConnections(t, twoL4, "amd64"), Kinds: recipestest.Registry(t)})
	bound := boundFrom(t, first)
	same := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, gpuDoc(", gpu: {kind: nvidia-l4, count: 2}")), Providers: providers.Registry(),
		Connections: gpuConnections(t, twoL4, "amd64"), Kinds: recipestest.Registry(t), Bindings: bound})
	if a := findAction(same, "resource:llm--server"); a.Op != Keep {
		t.Fatalf("unchanged GPU workload %s: %+v", a.Op, a.Changes)
	}
	r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, gpuDoc(", gpu: {kind: nvidia-l4, count: 1}")), Providers: providers.Registry(),
		Connections: gpuConnections(t, twoL4, "amd64"), Kinds: recipestest.Registry(t), Bindings: bound})
	a := findAction(r, "resource:llm--server")
	if a.Op != Update || len(a.Changes) != 1 || a.Changes[0].Field != "gpu" {
		t.Fatalf("GPU change not shown: %s %+v", a.Op, a.Changes)
	}
}

func TestPlanGPUOnCluster(t *testing.T) {
	cluster := func(t *testing.T, architecture string, storage bool) ConnectionOption {
		t.Helper()
		details := map[string]string{"endpoint": "https://cluster.example", "cluster_ca": "checked-ca", "namespace": "apps", "namespace_uid": "namespace-identity", "architecture": architecture, "service_proxy": "ready"}
		if storage {
			maps.Copy(details, map[string]string{"storage_class": "retained", "storage_class_uid": "class-identity", "storage_provisioner": "fixture.csi", "storage_binding": "WaitForFirstConsumer", "storage_reclaim": "Retain", "persistent_storage": "ready"})
		}
		inv := provider.GPUInventory{Status: "ready", Nodes: []provider.GPUNodeGroup{{
			Kind: "nvidia-l4", MemoryGiB: 24, Nodes: 2, PerNode: 1, Total: 2,
			NodeSelector: map[string]string{"cloud.google.com/gke-accelerator": "nvidia-l4"},
			Tolerations:  []provider.GPUToleration{{Key: "nvidia.com/gpu", Operator: "Exists", Effect: "NoSchedule"}},
		}}}
		maps.Copy(details, inv.Details())
		return infrastructureConnection(t, targetA, "kubernetes", details)
	}
	doc := "version: 1\nresources:\n  llm: {kind: gpusample, provider: kubernetes, target: " + targetA + ", gpu: {kind: nvidia-l4, count: 1}}\n"
	plan := func(c ConnectionOption) *Result {
		return Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
			Connections: []ConnectionOption{c}, Kinds: recipestest.Registry(t)})
	}
	r := plan(cluster(t, "amd64", true))
	if !r.Executable {
		t.Fatalf("blocked: %+v", r.Problems)
	}
	a := findAction(r, "resource:llm--server")
	got := a.Resource.Component.GPU
	if got == nil || got.NodeSelector["cloud.google.com/gke-accelerator"] != "nvidia-l4" || len(got.Tolerations) != 1 {
		t.Fatalf("cluster placement: %+v", got)
	}
	if _, ok := a.Resource.TargetSettings["gpus"]; ok {
		t.Fatalf("cluster GPU facts became reviewed connection settings: %+v", a.Resource.TargetSettings)
	}
	if r := plan(cluster(t, "amd64", false)); r.Executable || !hasCode(r.Problems, "catalog-storage-unavailable") {
		t.Fatalf("a weight cache ran without retained storage: %+v", r.Problems)
	}
	if r := plan(cluster(t, "arm64", true)); r.Executable || !hasCode(r.Problems, "gpu-architecture") || hasCode(r.Problems, "catalog-unavailable") {
		t.Fatalf("arm64 cluster: %+v", r.Problems)
	}
}

func TestPlanServiceGPU(t *testing.T) {
	image := "build: {registry: " + registryTarget + ", image: 'registry.example.com/acme/api@sha256:" + strings.Repeat("a", 64) + "'}"
	doc := func(prov, gpu string) string {
		target := ""
		if prov == "server" {
			target = ", target: " + targetA
		}
		return fmt.Sprintf("version: 1\nservices:\n  api: {source: cmd/api, provider: %s%s, public: false, size: {cpu: 1, memory: 2048%s}, %s}\n", prov, target, gpu, image)
	}
	r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc("server", ", gpu: {kind: nvidia-l4, count: 1}")), Providers: infrastructureProviders(t),
		Connections: gpuConnections(t, twoL4, "amd64")})
	if !r.Executable {
		t.Fatalf("blocked: %+v", r.Problems)
	}
	if got := findAction(r, "service:api").Service.GPU; got == nil || got.Kind != "nvidia-l4" || got.Count != 1 {
		t.Fatalf("service placement: %+v", got)
	}
	if r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc("server", "")), Providers: infrastructureProviders(t),
		Connections: gpuConnections(t, twoL4, "amd64")}); findAction(r, "service:api").Service.GPU != nil {
		t.Fatal("a service without gpu got one")
	}
	if r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc("server", ", gpu: {kind: nvidia-l4, count: 3}")), Providers: infrastructureProviders(t),
		Connections: gpuConnections(t, twoL4, "amd64")}); r.Executable || !hasCode(r.Problems, "gpu-count-unavailable") {
		t.Fatalf("service asked for more GPUs than the server has: %+v", r.Problems)
	}
	// A GPU component and a service on one server share its two GPUs.
	both := "version: 1\nresources:\n  llm: {kind: gpusample, provider: server, target: " + targetA + ", gpu: {kind: nvidia-l4, count: 2}}\nservices:\n  api: {source: cmd/api, provider: server, target: " + targetA + ", public: false, size: {cpu: 1, memory: 2048, gpu: {kind: nvidia-l4, count: 1}}, " + image + "}\n"
	if r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, both), Providers: providers.Registry(),
		Connections: gpuConnections(t, twoL4, "amd64"), Kinds: recipestest.Registry(t)}); r.Executable || !hasCode(r.Problems, "gpu-count-unavailable") {
		t.Fatalf("a service and a component shared a GPU: %+v", r.Problems)
	}
	if r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc("cloud-run", ", gpu: {kind: nvidia-l4, count: 1}")), Providers: providers.Registry(),
		Connections: gpuConnections(t, twoL4, "amd64")}); r.Executable || !hasCode(r.Problems, "gpu-target-unsupported") {
		t.Fatalf("GPU on Cloud Run: %+v", r.Problems)
	}
}

func TestPlanWarnsUnusedGPU(t *testing.T) {
	for _, doc := range []string{
		"version: 1\nresources:\n  app: {kind: sample, provider: server, target: " + targetA + ", gpu: {kind: nvidia-l4, count: 1}}\n",
		"version: 1\nresources:\n  db: {kind: postgres, provider: server, target: " + targetA + ", gpu: {kind: nvidia-l4, count: 1}}\n",
	} {
		r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
			Connections: gpuConnections(t, twoL4, "arm64"), Kinds: recipestest.Registry(t)})
		if !r.Executable || !hasCode(r.Warnings, "gpu-unused") {
			t.Fatalf("want a gpu-unused warning on an executable plan: problems %+v warnings %+v", r.Problems, r.Warnings)
		}
	}
}
