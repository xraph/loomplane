package planning

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

const runnerPoolConn = "conn_01m4vf1ff2e9pvk24sm9h8vrxx"

// runnerPool is a pool connection whose first runner checked in.
func runnerPool(t *testing.T, mutate func(map[string]string)) ConnectionOption {
	t.Helper()
	d := map[string]string{"pool_id": "rpool_01m4vf1ff2e9pvk24sm9h8vrxx", "pool_scope": "organization", "pool_revision": "2", "host_id": "rpool_01m4vf1ff2e9pvk24sm9h8vrxx",
		"isolation": "firecracker", "architecture": "amd64", "runtime_identity": strings.Repeat("a", 64), "build_isolation": "ready"}
	if mutate != nil {
		mutate(d)
	}
	return infrastructureConnection(t, runnerPoolConn, "runner", d)
}

// planOnRunner plans api on a server target, built by the runner pool.
func planOnRunner(t *testing.T, pool ConnectionOption, serverDetails map[string]string, projectID string) *Result {
	t.Helper()
	doc := fmt.Sprintf("version: 1\nservices:\n  api: {source: cmd/api, provider: server, target: %s, public: false, build: {builder: %s, registry: %s}}\n", targetA, runnerPoolConn, registryTarget)
	conns := []ConnectionOption{infrastructureConnection(t, targetA, "server", serverDetails), pool,
		infrastructureConnection(t, registryTarget, "registry", map[string]string{"endpoint": "https://registry.example.com", "repository": "acme"})}
	return Compute(Input{Environment: "production", ProjectID: projectID, Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: infrastructureProviders(t), Connections: conns})
}

func TestRunnerPoolBuildsAServerService(t *testing.T) {
	r := planOnRunner(t, runnerPool(t, nil), map[string]string{"architecture": "amd64"}, "")
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", r.Problems)
	}
	b := action(t, r, "service:api").Service.BuildTarget
	if b.BuilderCloud != "runner" || b.Builder != runnerPoolConn {
		t.Fatalf("build target %+v", b)
	}
	for _, k := range []string{"pool_id", "pool_scope", "pool_revision", "host_id", "isolation", "architecture", "runtime_identity"} {
		if b.BuilderSettings[k] == "" {
			t.Errorf("the plan doesn't pin %s: %v", k, b.BuilderSettings)
		}
	}
	if _, ok := b.BuilderSettings["build_isolation"]; ok {
		t.Error("whether a runner is online changed the reviewed settings")
	}
}

func TestRunnerPoolFindings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pool    func(map[string]string)
		server  map[string]string
		project string
		want    string
	}{
		{"no runner has checked in", func(d map[string]string) { delete(d, "runtime_identity"); delete(d, "build_isolation") }, map[string]string{"architecture": "amd64"}, "", "build-isolation-required"},
		{"not microVM isolation", func(d map[string]string) { d["isolation"] = "container" }, map[string]string{"architecture": "amd64"}, "", "build-isolation-required"},
		{"another architecture than the server", nil, map[string]string{"architecture": "arm64"}, "", "build-architecture"},
		{"another project's pool", func(d map[string]string) { d["pool_scope"] = "project"; d["project_id"] = "prj_other" }, map[string]string{"architecture": "amd64"}, "prj_mine", "builder-project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := planOnRunner(t, runnerPool(t, tc.pool), tc.server, tc.project)
			if !slices.Contains(codes(r.Problems), tc.want+":api") {
				t.Fatalf("want %s, got %v", tc.want, codes(r.Problems))
			}
		})
	}
	// A project's own pool is fine.
	r := planOnRunner(t, runnerPool(t, func(d map[string]string) { d["pool_scope"] = "project"; d["project_id"] = "prj_mine" }), map[string]string{"architecture": "amd64"}, "prj_mine")
	if slices.Contains(codes(r.Problems), "builder-project:api") {
		t.Fatalf("the project's own pool was refused: %v", codes(r.Problems))
	}
}

func TestRelayPoolIsReviewedPlacement(t *testing.T) {
	for _, cloud := range []string{"server", "kubernetes", "firecracker"} {
		details := map[string]string{"via_pool_id": "rpool_01m4vf1ff2e9pvk24sm9h8vrxx", "host": "10.0.0.5", "endpoint": "https://10.0.0.6", "host_id": "h"}
		if ReviewedConnectionSettings(cloud, details)["via_pool_id"] == "" {
			t.Errorf("%s: moving to another relay pool wouldn't make plans stale", cloud)
		}
	}
}

func TestDefaultRunnerPoolBuildsWhenNoBuilderIsNamed(t *testing.T) {
	doc := fmt.Sprintf("version: 1\nservices:\n  api: {source: cmd/api, provider: server, target: %s, public: false}\n", targetA)
	plan := func(server map[string]string) *Result {
		conns := []ConnectionOption{infrastructureConnection(t, targetA, "server", server), runnerPool(t, nil),
			infrastructureConnection(t, registryTarget, "registry", map[string]string{"endpoint": "https://registry.example.com", "repository": "acme"})}
		return Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: infrastructureProviders(t), Connections: conns,
			BuildDefault: &BuildDefault{Builder: runnerPoolConn, Registry: registryTarget, Source: "the organization's runner pool builds"}})
	}
	r := plan(map[string]string{"architecture": "amd64", "build_workers": "shared"})
	b := action(t, r, "service:api").Service.BuildTarget
	if b.Builder != runnerPoolConn || b.BuilderCloud != "runner" || b.Registry != registryTarget {
		t.Fatalf("build target %+v, problems %v", b, codes(r.Problems))
	}
	if !slices.Contains(codes(r.Warnings), "runner-default:api") {
		t.Fatalf("the plan doesn't say a default pool builds it: %v", codes(r.Warnings))
	}
	// A server its administrator made a dedicated builder keeps building.
	r = plan(map[string]string{"architecture": "amd64", "build_workers": "dedicated"})
	if b = action(t, r, "service:api").Service.BuildTarget; b.Builder != targetA {
		t.Fatalf("a dedicated server lost its own builds: %+v", b)
	}
}

func TestCodeBuildBuildsAServerService(t *testing.T) {
	const awsConn = "conn_01m4vf1ff2e9pvk24sm9h8vawz"
	plan := func(regDetails map[string]string) *Result {
		doc := fmt.Sprintf("version: 1\nservices:\n  api: {source: cmd/api, provider: server, target: %s, public: false, build: {builder: %s, registry: %s}}\n", targetA, awsConn, registryTarget)
		conns := []ConnectionOption{infrastructureConnection(t, targetA, "server", map[string]string{"architecture": "amd64"}),
			infrastructureConnection(t, awsConn, "aws", map[string]string{"account_id": "123456789012"}),
			infrastructureConnection(t, registryTarget, "registry", regDetails)}
		return Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: infrastructureProviders(t), Connections: conns})
	}
	r := plan(map[string]string{"endpoint": "https://123456789012.dkr.ecr.us-east-1.amazonaws.com", "repository": "loomplane"})
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	b := action(t, r, "service:api").Service.BuildTarget
	if b.BuilderCloud != "aws" || b.BuilderSettings["account_id"] != "123456789012" {
		t.Fatalf("build target %+v", b)
	}
	r = plan(map[string]string{"endpoint": "https://999999999999.dkr.ecr.us-east-1.amazonaws.com", "repository": "loomplane"})
	if !slices.Contains(codes(r.Problems), "codebuild-registry:api") {
		t.Fatalf("another account's registry: %v", codes(r.Problems))
	}
	r = plan(map[string]string{"endpoint": "https://ghcr.io", "repository": "acme"})
	if !slices.Contains(codes(r.Problems), "codebuild-registry:api") {
		t.Fatalf("a registry CodeBuild doesn't push to: %v", codes(r.Problems))
	}
}

func TestGlobalRunnersReachThePlanWithoutAConnection(t *testing.T) {
	const global = "rpool_01m4vf1ff2e9pvk24sm9h8vgzb"
	pool := runnerPool(t, func(d map[string]string) {
		d["pool_id"], d["host_id"], d["pool_scope"] = global, global, "platform"
		d["platform"], d["smt_active"], d["denied_ports"] = "true", "false", "25,465,587"
		d["per_org_vms"], d["per_org_vcpus"], d["per_org_memory_mib"] = "2", "4", "4096"
	})
	pool.ID = global
	doc := fmt.Sprintf("version: 1\nservices:\n  api: {source: cmd/api, provider: server, target: %s, public: false, build: {registry: %s}}\n", targetA, registryTarget)
	conns := []ConnectionOption{infrastructureConnection(t, targetA, "server", map[string]string{"architecture": "amd64", "build_workers": "shared"}),
		infrastructureConnection(t, registryTarget, "registry", map[string]string{"endpoint": "https://registry.example.com", "repository": "acme"})}
	r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: infrastructureProviders(t), Connections: conns,
		BuildDefault: &BuildDefault{Builder: global, Source: "Loomplane's global runners", Connection: &pool}})
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	b := action(t, r, "service:api").Service.BuildTarget
	if b.Builder != global || b.BuilderCloud != "runner" || b.BuilderSettings["pool_scope"] != "platform" {
		t.Fatalf("build target %+v", b)
	}
}

func TestOfflineRunnerPoolsAreWarnedAbout(t *testing.T) {
	plan := func(runners map[string]RunnerAvailability) []string {
		doc := fmt.Sprintf("version: 1\nservices:\n  api: {source: cmd/api, provider: server, target: %s, public: false, build: {builder: %s, registry: %s}}\n", targetA, runnerPoolConn, registryTarget)
		conns := []ConnectionOption{infrastructureConnection(t, targetA, "server", map[string]string{"architecture": "amd64", "via_pool_id": "rpool_relay"}), runnerPool(t, nil),
			infrastructureConnection(t, registryTarget, "registry", map[string]string{"endpoint": "https://registry.example.com", "repository": "acme"})}
		r := Compute(Input{Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: infrastructureProviders(t), Connections: conns, Runners: runners})
		return codes(r.Warnings)
	}
	w := plan(map[string]RunnerAvailability{runnerPoolConn: {}, "rpool_relay": {}})
	if !slices.Contains(w, "runner-pool-offline:api") || !slices.Contains(w, "relay-pool-offline:api") {
		t.Fatalf("warnings %v", w)
	}
	w = plan(map[string]RunnerAvailability{runnerPoolConn: {Online: 1, Build: 1}, "rpool_relay": {Online: 1}})
	if slices.Contains(w, "runner-pool-offline:api") || slices.Contains(w, "relay-pool-offline:api") {
		t.Fatalf("warnings with runners online %v", w)
	}
	// An on-demand pool starts one, so it isn't warned about.
	if w = plan(map[string]RunnerAvailability{runnerPoolConn: {OnDemand: true}, "rpool_relay": {Online: 1}}); slices.Contains(w, "runner-pool-offline:api") {
		t.Fatalf("an on-demand pool was warned about: %v", w)
	}
	// Without counts there is no warning.
	if w = plan(nil); slices.Contains(w, "runner-pool-offline:api") {
		t.Fatalf("warnings without counts %v", w)
	}
}
