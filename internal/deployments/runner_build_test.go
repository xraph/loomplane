package deployments

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/registry"
	"github.com/xraph/loomplane/internal/connections"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/planning"
)

// fakeRunners is a runner pool of one runner: a real Firecracker host
// handler over a driver that runs nothing, served as a runner serves it,
// with no TLS and the pool as its only peer.
type fakeRunners struct {
	nodeID string
	addr   string
	driver *platformDriver
	mu     sync.Mutex
	builds int
}

func newFakeRunners(t *testing.T, org, pool string) *fakeRunners {
	t.Helper()
	store, err := fc.OpenStore(t.TempDir() + "/state")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	r := &fakeRunners{nodeID: "node_r1", driver: &platformDriver{hostID: "node_r1"}}
	host := &fc.Host{Store: store, Driver: r.driver, Limits: fc.Limits{VMs: 2, VCPUs: 8, MemoryMiB: 16384, DiskMiB: 100000}, Key: []byte(strings.Repeat("k", 32))}
	server := httptest.NewServer(fc.Handler{Host: host, Node: &fc.Peer{Org: org, Connection: pool}, BuildsOnly: true})
	t.Cleanup(server.Close)
	r.addr = strings.TrimPrefix(server.URL, "http://")
	return r
}

func (r *fakeRunners) BuildRunners(context.Context, string, string) ([]string, error) {
	return []string{r.nodeID}, nil
}

func (r *fakeRunners) RunnerClient(_ context.Context, _, nodeID string) (*fc.Client, error) {
	return fc.NewStreamClient(nodeID, func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", r.addr)
	})
}

func (r *fakeRunners) PoolDetails(context.Context, string, string) (map[string]string, error) {
	return nil, errors.New("no platform pools here")
}
func (r *fakeRunners) MeterBuild(context.Context, string, string, string, time.Time, time.Time) error {
	return nil
}
func (r *fakeRunners) Wait(context.Context, string, string, string) (int, error) { return 0, nil }
func (r *fakeRunners) Unwait(context.Context, string) error                      { return nil }
func (r *fakeRunners) BuildStarted(context.Context, string, string) error        { return nil }
func (r *fakeRunners) BuildEnded(context.Context, string, string) error          { return nil }

func (r *fakeRunners) RecordBuild(context.Context, string, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builds++
	return nil
}

// poolDetails are what a pool's connection carries once a runner with the
// fake driver's toolchain checked in.
func poolDetails(identity string) map[string]string {
	return map[string]string{"pool_id": "rpool_1", "pool_scope": "organization", "pool_revision": "1", "host_id": "rpool_1",
		"isolation": "firecracker", "architecture": "amd64", "runtime_identity": identity, "build_isolation": "ready"}
}

// runnerJob builds web from source on the pool conn_pool, pushing to a
// registry connection.
func runnerJob(t *testing.T, pool map[string]string) *Job {
	t.Helper()
	reg := newPlatformRegistry(t)
	regDetails := map[string]string{"endpoint": reg.url, "repository": "acme", "push": "ready"}
	regRaw := []byte(`{"username":"builder","password":"registry-secret"}`)
	parsed, err := registry.ParseConfig(regRaw, regDetails)
	if err != nil {
		t.Fatal(err)
	}
	maps := map[string]*connections.Connection{
		"conn_pool": {ID: "conn_pool", OrgID: "org_a", Cloud: connections.Runner, Method: connections.MethodRunner, Name: "builds", Status: connections.StatusReady, Details: pool},
		"conn_reg":  {ID: "conn_reg", OrgID: "org_a", Cloud: connections.Registry, Name: "registry", Status: connections.StatusReady, Details: withPush(parsed.Details)},
	}
	creds := buildCredentialFunc(func(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
		c := maps[id]
		if c == nil || org != "org_a" {
			return nil, nil, connections.ErrNotFound
		}
		if id == "conn_reg" {
			return c, regRaw, nil
		}
		return c, []byte(`{"runner_pool":true}`), nil
	})
	cfg := &planning.ServiceConfig{Provider: "server", Service: "web", Image: "Dockerfile", Context: ".",
		BuildTarget: &planning.BuildTarget{BuilderCloud: connections.Runner, Builder: "conn_pool", Registry: "conn_reg",
			BuilderSettings:  planning.ReviewedBuildSettings(connections.Runner, pool),
			RegistrySettings: planning.ReviewedConnectionSettings(connections.Registry, parsed.Details)}}
	source := sourceTarball(t)
	job := &Job{BuildCredentials: creds, Deployment: &Deployment{ID: "dep_1", OrgID: "org_a", ProjectID: "prj_1", EnvironmentID: "env_1", PlanID: "plan_1"}, Plan: &planning.Plan{ID: "plan_1"},
		Action: planning.Action{ID: "service:web", Name: "web", Provider: "server", Service: cfg}, Progress: func(string, string) {},
		SourceID: strings.Repeat("a", 40), Logs: &buildLogFixture{},
		Source: func(context.Context) (io.ReadCloser, int64, error) {
			return io.NopCloser(bytes.NewReader(source)), int64(len(source)), nil
		}}
	return job
}

// withPush is details with the push access a source build checks for.
func withPush(details map[string]string) map[string]string {
	out := map[string]string{"push": "ready"}
	for k, v := range details {
		out[k] = v
	}
	return out
}

func TestRunnerBuildRunsInARunnersMicroVM(t *testing.T) {
	pool := poolDetails(strings.Repeat("a", 64))
	runners := newFakeRunners(t, "org_a", "conn_pool")
	job := runnerJob(t, pool)
	state := &ImageArtifact{}
	b := &ImageBuilder{FirecrackerAuthority: &countingIssuer{}, Runners: runners}
	ref, err := b.Build(t.Context(), job, state, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(ref, "@sha256:"+strings.Repeat("c", 64)) || !strings.Contains(ref, "/acme/loomplane/") {
		t.Fatalf("reference %q", ref)
	}
	if state.Runner != "node_r1" || state.Builder != "conn_pool" || state.Architecture != "amd64" {
		t.Fatalf("state %+v", state)
	}
	if runners.driver.sources != 1 || runners.driver.started != 1 || runners.builds != 1 {
		t.Fatalf("sources %d, started %d, recorded builds %d", runners.driver.sources, runners.driver.started, runners.builds)
	}
}

func TestRunnerWithAnotherToolchainGetsNoSource(t *testing.T) {
	defer func(wait, poll time.Duration) { runnerWait, runnerPoll = wait, poll }(runnerWait, runnerPoll)
	runnerWait, runnerPoll = 50*time.Millisecond, 10*time.Millisecond
	// The pool pinned another toolchain than the one this runner shows.
	pool := poolDetails(strings.Repeat("b", 64))
	runners := newFakeRunners(t, "org_a", "conn_pool")
	job := runnerJob(t, pool)
	b := &ImageBuilder{FirecrackerAuthority: &countingIssuer{}, Runners: runners}
	_, err := b.Build(t.Context(), job, &ImageArtifact{}, func() error { return nil })
	if err == nil || !strings.Contains(err.Error(), "another toolchain") {
		t.Fatalf("err = %v", err)
	}
	if runners.driver.sources != 0 || runners.driver.started != 0 {
		t.Fatalf("the runner got work: sources %d, started %d", runners.driver.sources, runners.driver.started)
	}
}

func TestRunnerBuildRefusedWithoutMicroVMEvidence(t *testing.T) {
	pool := poolDetails(strings.Repeat("a", 64))
	pool["isolation"] = "container"
	runners := newFakeRunners(t, "org_a", "conn_pool")
	job := runnerJob(t, pool)
	b := &ImageBuilder{FirecrackerAuthority: &countingIssuer{}, Runners: runners}
	if _, err := b.Build(t.Context(), job, &ImageArtifact{}, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "microVM") {
		t.Fatalf("err = %v", err)
	}
	if runners.driver.started != 0 {
		t.Fatal("a pool without microVM evidence started a VM")
	}
}

func TestRunnerBuildsNeedRunnerPools(t *testing.T) {
	job := runnerJob(t, poolDetails(strings.Repeat("a", 64)))
	b := &ImageBuilder{FirecrackerAuthority: &countingIssuer{}}
	if _, err := b.Build(t.Context(), job, &ImageArtifact{}, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "runner builds aren't available") {
		t.Fatalf("err = %v", err)
	}
}

// A runner on another organization's pool refuses the build's scope.
func TestRunnerOfAnotherPoolRefusesTheBuild(t *testing.T) {
	defer func(wait, poll time.Duration) { runnerWait, runnerPoll = wait, poll }(runnerWait, runnerPoll)
	runnerWait, runnerPoll = 50*time.Millisecond, 10*time.Millisecond
	pool := poolDetails(strings.Repeat("a", 64))
	runners := newFakeRunners(t, "org_b", "conn_other")
	job := runnerJob(t, pool)
	b := &ImageBuilder{FirecrackerAuthority: &countingIssuer{}, Runners: runners}
	// That can only happen when the pool's list of runners is wrong, so
	// the build fails loudly instead of waiting.
	if _, err := b.Build(t.Context(), job, &ImageArtifact{}, func() error { return nil }); err == nil {
		t.Fatal("another pool's runner took the build")
	}
	if runners.driver.started != 0 || runners.driver.sources != 0 {
		t.Fatal("another pool's runner started a VM")
	}
}

// globalRunners is fakeRunners as a platform pool of global runners.
type globalRunners struct {
	*fakeRunners
	details map[string]string
	metered []string
}

func (g *globalRunners) PoolDetails(context.Context, string, string) (map[string]string, error) {
	return g.details, nil
}
func (g *globalRunners) MeterBuild(_ context.Context, org, builder, _ string, _, _ time.Time) error {
	g.metered = append(g.metered, org+"/"+builder)
	return nil
}

type recordingGate struct{ calls []bool }

func (g *recordingGate) CanRun(_ context.Context, _ string, _ metering.Kind, platform bool) error {
	g.calls = append(g.calls, platform)
	return nil
}

func TestGlobalRunnerBuildIsPlatformUsage(t *testing.T) {
	defer func(wait, poll time.Duration) { runnerWait, runnerPoll = wait, poll }(runnerWait, runnerPoll)
	runnerWait, runnerPoll = 50*time.Millisecond, 10*time.Millisecond
	const global = "rpool_global"
	details := poolDetails(strings.Repeat("a", 64))
	details["pool_id"], details["host_id"], details["pool_scope"] = global, global, "platform"
	details["platform"], details["smt_active"], details["denied_ports"] = "true", "false", "25,465,587"
	details["per_org_vms"], details["per_org_vcpus"], details["per_org_memory_mib"] = "4", "8", "8192"
	runners := &globalRunners{fakeRunners: newFakeRunners(t, "org_a", global), details: details}
	job := runnerJob(t, details)
	job.Action.Service.BuildTarget.Builder = global
	gate := &recordingGate{}
	b := &ImageBuilder{FirecrackerAuthority: &countingIssuer{}, Runners: runners, Gate: gate}
	_, err := b.Build(t.Context(), job, &ImageArtifact{}, func() error { return nil })
	// The fake host isn't in platform mode, so the live platform check
	// refuses it before any source goes out.
	if err == nil || runners.driver.sources != 0 {
		t.Fatalf("a runner that isn't in platform mode built a global build: %v", err)
	}
	if len(gate.calls) < 2 || !gate.calls[len(gate.calls)-1] {
		t.Fatalf("gate calls %v: a global build is gated as platform usage", gate.calls)
	}
}
