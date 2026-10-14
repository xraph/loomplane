package planning

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/pkg/discovery"
	"github.com/xraph/loomplane/pkg/provider"
)

const targetA = "conn_01m4vewksnea2bwdxwdy5aekhn"
const targetB = "conn_01m4va49zwe9pvk24sm9h8v0ty"
const registryTarget = "conn_01m4vem4sde8ab4yfr1abnj3wf"

func infrastructureGraph() *graph.Graph {
	return &graph.Graph{Services: []graph.Service{{
		ID: "svc:go:cmd/api", Name: "api", Kind: graph.HTTPService, Deployable: true,
		Ecosystem: discovery.Go, Framework: "forge", Path: "cmd/api", Entrypoint: "cmd/api", Ports: []int{8080}, Image: "Dockerfile", Context: ".",
	}}}
}

func infrastructureProviders(t *testing.T) *provider.Registry {
	t.Helper()
	reg, err := provider.NewRegistry(
		provider.Provider{Name: "server", Title: "Server", Cloud: "server", Model: provider.Instance, Available: true, Scaling: true,
			Runs: []provider.Runs{{Workload: provider.HTTPService, Execution: provider.Container, Creates: "Docker container"}}},
		provider.Provider{Name: "kubernetes", Title: "Kubernetes", Cloud: "kubernetes", Model: provider.Instance, Available: true, Scaling: true,
			Runs: []provider.Runs{{Workload: provider.HTTPService, Execution: provider.Container, Creates: "Kubernetes Deployment"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func infrastructureConnection(t *testing.T, id, cloud string, details map[string]string) ConnectionOption {
	t.Helper()
	data, err := json.Marshal(map[string]any{"ID": id, "Name": id, "Cloud": cloud, "Ready": true, "Details": details})
	if err != nil {
		t.Fatal(err)
	}
	var c ConnectionOption
	if err = json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func infrastructureConnections(t *testing.T) []ConnectionOption {
	return []ConnectionOption{
		infrastructureConnection(t, targetA, "server", map[string]string{"host": "one.example.com", "user": "deploy", "docker_socket": "/var/run/docker.sock"}),
		infrastructureConnection(t, targetB, "server", map[string]string{"host": "two.example.com", "user": "deploy", "docker_socket": "/var/run/docker.sock"}),
		infrastructureConnection(t, registryTarget, "registry", map[string]string{"endpoint": "https://registry.example.com", "repository": "acme"}),
	}
}

func TestInfrastructureTargets_pinnedImageAcceptsPullOnlyRegistry(t *testing.T) {
	connections := infrastructureConnections(t)
	connections[2].Ready = false
	connections[2].PullReady = true
	connections[2].Details["pull"] = "ready"
	connections[2].Details["push"] = "unavailable"
	doc := fmt.Sprintf("version: 1\nservices:\n  api: {source: cmd/api, provider: server, target: %s, public: false, build: {registry: %s, image: 'registry.example.com/acme/api@sha256:%s'}}\n", targetA, registryTarget, strings.Repeat("a", 64))
	r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: infrastructureProviders(t), Connections: connections})
	if !r.Executable {
		t.Fatalf("pinned image needs only pull: %+v", r.Problems)
	}
	doc = fmt.Sprintf("version: 1\nservices:\n  api: {source: cmd/api, provider: server, target: %s, public: false, build: {registry: %s}}\n", targetA, registryTarget)
	r = Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: infrastructureProviders(t), Connections: connections})
	if r.Executable {
		t.Fatal("source build accepted a registry without push")
	}
}

func TestInfrastructureTargets_eachServicePinsItsConnection(t *testing.T) {
	doc := fmt.Sprintf(`version: 1
services:
  api: {source: cmd/api, provider: server, target: %s, public: false}
  copy: {source: cmd/api, provider: server, target: %s, public: false}
`, targetA, targetB)
	r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc),
		Providers: infrastructureProviders(t), Connections: infrastructureConnections(t)})
	if !r.Executable || len(r.Problems) != 0 {
		t.Fatalf("independent targets blocked: %+v", r.Problems)
	}
	for _, tc := range []struct{ name, target, host string }{{"api", targetA, "one.example.com"}, {"copy", targetB, "two.example.com"}} {
		a := action(t, r, "service:"+tc.name)
		data, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"connection_id":"`+tc.target+`"`) || !strings.Contains(string(data), `"host":"`+tc.host+`"`) {
			t.Fatalf("target not pinned: %s", data)
		}
		if !strings.Contains(string(data), `"health_path":"/_/health"`) {
			t.Fatalf("Forge probe not planned: %s", data)
		}
	}
}

func TestInfrastructureTargets_preservesAnEnvironmentDefault(t *testing.T) {
	doc := "version: 1\nservices:\n  api: {source: cmd/api, provider: server, public: false}\n"
	r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: infrastructureProviders(t),
		Connections: infrastructureConnections(t), Chosen: map[string]string{"server": targetB}})
	if !r.Executable {
		t.Fatalf("existing environment choice stopped working: %+v", r.Problems)
	}
	data, err := json.Marshal(action(t, r, "service:api"))
	if err != nil || !strings.Contains(string(data), `"connection_id":"`+targetB+`"`) {
		t.Fatalf("default not pinned: %s %v", data, err)
	}
}

func TestInfrastructureTargets_rejectsWrongOrMissingConnections(t *testing.T) {
	for _, tc := range []struct {
		name, provider, settings, code string
		connections                    []ConnectionOption
	}{
		{"missing", "server", "", "target-required", infrastructureConnections(t)},
		{"foreign", "server", "target: " + targetA, "target-gone", nil},
		{"wrong cloud", "server", "target: " + registryTarget, "target-cloud", infrastructureConnections(t)},
		{"not ready", "server", "target: " + targetA, "target-not-ready", []ConnectionOption{{ID: targetA, Cloud: "server"}}},
		{"builder role", "server", "target: " + targetA + "\n    build: {builder: " + registryTarget + "}", "builder-cloud", infrastructureConnections(t)},
		{"cluster registry", "kubernetes", "target: " + targetA + "\n    build: {builder: " + targetB + "}", "registry-required",
			[]ConnectionOption{infrastructureConnection(t, targetA, "kubernetes", map[string]string{"namespace": "apps"}), infrastructureConnection(t, targetB, "server", nil)}},
		{"server replicas", "server", "target: " + targetA + "\n    scale: {min: 2, max: 2}", "server-scale", infrastructureConnections(t)},
		{"cluster ingress", "kubernetes", "target: " + targetA + "\n    public: true\n    build: {image: 'docker.io/acme/api@sha256:" + strings.Repeat("a", 64) + "'}", "ingress-required",
			[]ConnectionOption{infrastructureConnection(t, targetA, "kubernetes", map[string]string{"namespace": "apps"})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := "version: 1\nservices:\n  api:\n    source: cmd/api\n    provider: " + tc.provider + "\n    public: false\n    " + tc.settings + "\n"
			if tc.name == "cluster ingress" {
				doc = strings.Replace(doc, "    public: false\n", "", 1)
			}
			r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: infrastructureProviders(t), Connections: tc.connections})
			if r.Executable || !slices.Contains(findingCodes(r.Problems), tc.code) {
				t.Fatalf("wanted %s: %+v", tc.code, r.Problems)
			}
		})
	}
}

func TestInfrastructureTargets_publicPortsAndPrivateNetworks(t *testing.T) {
	for _, tc := range []struct{ name, secondTarget, settings, code string }{
		{"port collision", targetA, "public: true, endpoint: {host_port: 8081}", "target-port-conflict"},
		{"private across servers", targetB, "public: false, needs: [api]", "private-across-targets"},
		{"same private network", targetA, "public: false, needs: [api]", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := "public: false"
			if tc.code == "target-port-conflict" {
				first = "public: true, endpoint: {host_port: 8081}"
			}
			doc := fmt.Sprintf("version: 1\nservices:\n  api: {source: cmd/api, provider: server, target: %s, %s}\n  copy: {source: cmd/api, provider: server, target: %s, %s}\n", targetA, first, tc.secondTarget, tc.settings)
			r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: infrastructureProviders(t), Connections: infrastructureConnections(t)})
			if tc.code == "" {
				if !r.Executable || len(r.Problems) != 0 {
					t.Fatalf("same network unreachable: %+v", r.Problems)
				}
			} else if r.Executable || !slices.Contains(findingCodes(r.Problems), tc.code) {
				t.Fatalf("wanted %s: %+v", tc.code, r.Problems)
			}
		})
	}
}

func TestInfrastructureTargets_moveReplacesAndHeldSourceStays(t *testing.T) {
	g := federated(t)
	oldDoc := fmt.Sprintf(`version: 1
services:
  api: {repo: mono, source: services/api, provider: server, target: %s, public: false}
  web: {repo: site, source: ., kind: http-service, provider: server, target: %s, public: false, build: {image: 'docker.io/acme/api@sha256:%s'}}
`, targetA, targetA, strings.Repeat("a", 64))
	in := Input{Environment: "production", Graph: g, Manifest: parse(t, oldDoc), Providers: infrastructureProviders(t), Connections: infrastructureConnections(t)}
	old := Compute(in)
	if len(old.Problems) != 0 {
		t.Fatalf("initial placement: %+v", old.Problems)
	}
	for _, a := range old.Actions {
		cfg, err := json.Marshal(a.Service)
		if err != nil {
			t.Fatal(err)
		}
		in.Bindings = append(in.Bindings, &Binding{Subject: ServiceSubject, Name: a.Name, Provider: a.Provider, ConnectionID: targetA, Config: cfg, CommitSHA: a.Service.CommitSHA})
	}
	in.Manifest = parse(t, strings.ReplaceAll(oldDoc, targetA, targetB))
	in.Held = map[string]bool{"site": true}
	r := Compute(in)
	if len(r.Problems) != 0 || action(t, r, "service:api").Op != Replace || action(t, r, "service:web").Op != Keep {
		t.Fatalf("move or held source: %+v %+v", r.Problems, r.Actions)
	}
}

func TestInfrastructureTargets_clusterCapabilitiesAreRequiredOnlyWhenUsed(t *testing.T) {
	for _, tc := range []struct{ name, settings, code string }{
		{"private fixed", "public: false, scale: {min: 1, max: 1}", ""},
		{"denied ingress", "public: true, endpoint: {hostname: api.example.com}", "ingress-unavailable"},
		{"zero elastic replicas", "public: false, scale: {min: 0, max: 2}", "hpa-minimum"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connections := []ConnectionOption{infrastructureConnection(t, targetA, "kubernetes", map[string]string{"namespace": "apps", "hpa": "ready", "ingress": "unavailable", "cluster_ca": "reviewed-ca"})}
			doc := fmt.Sprintf("version: 1\nservices:\n  api: {source: cmd/api, provider: kubernetes, target: %s, %s, build: {image: 'docker.io/acme/api@sha256:%s'}}\n", targetA, tc.settings, strings.Repeat("a", 64))
			r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: infrastructureProviders(t), Connections: connections})
			if tc.code == "" {
				if !r.Executable || action(t, r, "service:api").Service.TargetSettings["cluster_ca"] != "reviewed-ca" {
					t.Fatalf("private placement/trust: %+v", r)
				}
			} else if r.Executable || !slices.Contains(findingCodes(r.Problems), tc.code) {
				t.Fatalf("wanted %s: %+v", tc.code, r.Problems)
			}
		})
	}
}

func TestInfrastructureTargets_aliasesCannotPublishSamePhysicalServerPort(t *testing.T) {
	connections := infrastructureConnections(t)
	for index := range connections {
		if connections[index].Cloud == "server" {
			connections[index].Details["docker_id"] = "same-daemon"
		}
	}
	doc := fmt.Sprintf("version: 1\nservices:\n  api: {source: cmd/api, provider: server, target: %s, public: true, endpoint: {host_port: 8081}}\n  copy: {source: cmd/api, provider: server, target: %s, public: true, endpoint: {host_port: 8081}}\n", targetA, targetB)
	result := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: infrastructureProviders(t), Connections: connections})
	if result.Executable || !slices.Contains(findingCodes(result.Problems), "target-port-conflict") {
		t.Fatalf("alias port conflict missed: %+v", result)
	}
}
