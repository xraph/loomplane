package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/catalogstate"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/discovery"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// fakeDeployed is what deployments left behind, set by each test. A
// resource it knows nothing about is absent.
type fakeDeployed struct {
	mu       sync.Mutex
	states   map[string]catalogstate.State // by environment/resource
	bindings map[string][]*planning.Binding
	urls     map[string]string
	keys     map[string]string
}

func factKey(env, resource string) string { return env + "/" + resource }

func (f *fakeDeployed) State(_ context.Context, orgID, _, environmentID, resource string) (catalogstate.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if orgID != "org" {
		return catalogstate.State{Value: catalogstate.Absent}, nil
	}
	if st, ok := f.states[factKey(environmentID, resource)]; ok {
		return st, nil
	}
	return catalogstate.State{Value: catalogstate.Absent}, nil
}

func (f *fakeDeployed) Bindings(_ context.Context, orgID, environmentID string) ([]*planning.Binding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if orgID != "org" {
		return nil, nil
	}
	return f.bindings[environmentID], nil
}

func (f *fakeDeployed) URL(_ context.Context, _, environmentID, resource string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.urls[factKey(environmentID, resource)], nil
}

func (f *fakeDeployed) APIKey(_ context.Context, _, _, environmentID, resource string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key, ok := f.keys[factKey(environmentID, resource)]
	if !ok {
		return "", errors.New("no key")
	}
	return key, nil
}

// ConnectionNames names the two boxes.
func (f *fakeDeployed) ConnectionNames(_ context.Context, orgID string) (map[string]string, error) {
	if orgID != "org" {
		return map[string]string{}, nil
	}
	return map[string]string{gpuConn: "gpu-box", cpuConn: "cpu-box"}, nil
}

// running says resource runs ready in env on gpu-box, bound, behind the
// guard at url with key, its engine deployed with settings.
func (f *fakeDeployed) running(env, resource, kind string, settings map[string]string, gpu int) {
	f.runningOn(gpuConn, env, resource, kind, settings, gpu)
}

// runningOn is running, on connection.
func (f *fakeDeployed) runningOn(connection, env, resource, kind string, settings map[string]string, gpu int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.states == nil {
		f.states, f.bindings, f.urls, f.keys = map[string]catalogstate.State{}, map[string][]*planning.Binding{}, map[string]string{}, map[string]string{}
	}
	k := factKey(env, resource)
	f.states[k] = catalogstate.State{Value: catalogstate.Ready, DeploymentID: "dep_1", ConnectionID: connection, Bound: true}
	f.urls[k] = "http://" + resource + "--guard:8080"
	f.keys[k] = "lpm_" + resource + "_key"
	composed, _ := json.Marshal(planning.ResourceConfig{Kind: kind, Composition: &planning.CompositionConfig{Kind: kind}})
	component := planning.ResourceConfig{Kind: kind, Component: &planning.ComponentConfig{Kind: kind, Resource: resource, Settings: settings}}
	if gpu > 0 {
		component.Component.GPU = &provider.GPUPlacement{GPURequest: provider.GPURequest{Kind: "nvidia-l40s", Count: gpu}}
	}
	engine, _ := json.Marshal(component)
	f.bindings[env] = append(f.bindings[env],
		&planning.Binding{Subject: planning.ResourceSubject, Name: resource, ConnectionID: connection, Config: composed},
		&planning.Binding{Subject: planning.ResourceSubject, Name: planning.ComponentActionName(resource, recipes.ModelEngine), ConnectionID: connection, Config: engine},
	)
}

// bindService says a service runs in env on connection.
func (f *fakeDeployed) bindService(env, service, connection string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bindings == nil {
		f.bindings = map[string][]*planning.Binding{}
	}
	f.bindings[env] = append(f.bindings[env], &planning.Binding{Subject: planning.ServiceSubject, Name: service, ConnectionID: connection})
}

func (f *fakeDeployed) set(env, resource string, st catalogstate.State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.states == nil {
		f.states = map[string]catalogstate.State{}
	}
	f.states[factKey(env, resource)] = st
}

// countGateways counts what it's told.
type countGateways struct {
	mu    sync.Mutex
	calls []string
}

func (g *countGateways) ModelsChanged(_ context.Context, orgID, projectID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, orgID+"/"+projectID)
}

func (g *countGateways) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

// The Llama 8B fixture: deployed on gpu-box's L40S in staging.
const llamaResource = "llama-3-1-8b-instruct"

// boxes are the workspace's two servers, by name.
func boxes() []*connections.Connection {
	return []*connections.Connection{
		{ID: gpuConn, Name: "gpu-box", Cloud: connections.Server, Status: connections.StatusReady},
		{ID: cpuConn, Name: "cpu-box", Cloud: connections.Server, Status: connections.StatusReady},
	}
}

// deployLlama deploys Llama 8B onto gpu-box in env and says it's running.
func deployLlama(t *testing.T, d *deploySetup, env string) *Deployment {
	t.Helper()
	d.grantLlama()
	res, err := d.svc.Deploy(context.Background(), member, projectID, env, DeployInput{EntryID: llama8BID, Target: onGPU("nvidia-l40s")})
	if err != nil {
		t.Fatal(err)
	}
	r := current(t, d).Resources[llamaResource]
	d.facts.running(env, llamaResource, r.Kind, r.Settings, 1)
	return res.Deployment
}

func TestStatusCombinesFactsAndCost(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()}, boxes()...)
	row := deployLlama(t, d, stagingID)
	// It ran 2.04 hours this month, at $1.57 an hour.
	*d.now = time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	if err := d.rows.Accrue(ctx, row, "2026-11", "3.2000", *d.now); err != nil {
		t.Fatal(err)
	}

	st, err := d.svc.Status(ctx, member, projectID, stagingID, llamaResource)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateReady || st.Deployment == nil || st.Deployment.ID != row.ID || st.Hint != "" {
		t.Errorf("state %q hint %q deployment %+v", st.State, st.Hint, st.Deployment)
	}
	if st.Entry.ID != llama8BID || st.Kind != recipes.ModelVLLMKind || st.EngineVersion != engines.Current()[registry.EngineVLLM] {
		t.Errorf("entry %q kind %q engine %q", st.Entry.ID, st.Kind, st.EngineVersion)
	}
	if st.GPU == nil || *st.GPU != (provider.GPURequest{Kind: "nvidia-l40s", Count: 1}) || st.Verification.GPUKind != "nvidia-l40s" || !st.Verification.Fits() {
		t.Errorf("gpu %+v verification %+v", st.GPU, st.Verification)
	}
	if st.ConnectionID != gpuConn || st.ConnectionName != "gpu-box" || st.MaxModelLen != 32768 || st.InternalURL != "http://"+llamaResource+"--guard:8080" {
		t.Errorf("connection %q %q, context %d, url %q", st.ConnectionID, st.ConnectionName, st.MaxModelLen, st.InternalURL)
	}
	if len(st.Environments) != 1 || st.Environments[0].Mode != ReachHome || len(st.Linked) != 0 || !st.CanEdit || st.UpdateAvailable {
		t.Errorf("environments %+v linked %+v can edit %v update %v", st.Environments, st.Linked, st.CanEdit, st.UpdateAvailable)
	}
	want := Cost{HourlyUSD: "1.5700", Known: true, Source: CostSourceServer, Share: "1 of 1 GPUs", MonthlyUSD: "1146.10"}
	if st.Cost != want {
		t.Errorf("cost %+v", st.Cost)
	}
	// 3.20 so far, and 16 days (384 hours) left at 1.57: 602.88 more.
	if st.MonthToDateUSD != "3.2000" || st.ProjectedUSD != "606.08" {
		t.Errorf("month to date %q projected %q", st.MonthToDateUSD, st.ProjectedUSD)
	}
	// The key never shows.
	if raw, _ := json.Marshal(st); strings.Contains(string(raw), "lpm_") {
		t.Errorf("the status holds the key: %s", raw)
	}

	// Last month's cost isn't this month's.
	*d.now = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	if st, err = d.svc.Status(ctx, member, projectID, stagingID, llamaResource); err != nil || st.MonthToDateUSD != "0.0000" {
		t.Errorf("a new month: %q, %v", st.MonthToDateUSD, err)
	}
	// A viewer reads it and can't change it; nobody without ai:read does.
	viewing := newDeploy(t, deployRegistry(t, llamaRev, ""), append(slices.Clone(viewer), security.ProjectRead), []gpus.GPUTarget{l40sServer()})
	if _, err = viewing.svc.Status(ctx, member, projectID, stagingID, llamaResource); !errors.Is(err, ErrNotFound) {
		t.Errorf("a model that isn't there: %v", err)
	}
	none := newDeploy(t, deployRegistry(t, llamaRev, ""), perms{}, nil)
	if _, err = none.svc.Status(ctx, member, projectID, stagingID, llamaResource); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("without ai:read: %v", err)
	}
	list, err := d.svc.List(ctx, member)
	if err != nil || len(list) != 1 || list[0].Deployment.Resource != llamaResource {
		t.Errorf("list %+v, %v", list, err)
	}
}

// Ruling M25: a CPU model's cost is its server's, and a GPU model whose
// price or GPU count isn't known is unknown. Neither is ever "0.00" that
// looks free, and neither has a projection.
func TestStatusNeverShowsAnUnknownPriceAsFree(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()}, append(boxes(), cpuCluster())...)
	if _, err := d.svc.Deploy(ctx, member, projectID, stagingID, DeployInput{EntryID: bgeSmall, Target: TargetChoice{Source: TargetConnection, ConnectionID: kubeConn}}); err != nil {
		t.Fatal(err)
	}
	st, err := d.svc.Status(ctx, member, projectID, stagingID, DefaultResource(mustEntry(t, bgeSmall)))
	if err != nil {
		t.Fatal(err)
	}
	if st.Cost.Source != CostSourceCPU || st.ProjectedUSD != "" || st.ConnectionName != "kube" {
		t.Errorf("CPU model: cost %+v projected %q on %q", st.Cost, st.ProjectedUSD, st.ConnectionName)
	}

	deployLlama(t, d, stagingID)
	// gpu-box's price is gone from the GPU targets.
	d.targets.targets = nil
	if st, err = d.svc.Status(ctx, member, projectID, stagingID, llamaResource); err != nil {
		t.Fatal(err)
	}
	if st.Cost.Source != CostSourceUnknown || st.Cost.Known || st.Cost.HourlyUSD != "" || st.ProjectedUSD != "" {
		t.Errorf("unknown price: %+v, projected %q", st.Cost, st.ProjectedUSD)
	}
	// What the owner says it costs wins.
	row, _ := d.rows.Get(ctx, "org", projectID, stagingID, llamaResource)
	override := "2.5"
	if st, err = d.svc.Update(ctx, member, projectID, stagingID, llamaResource, UpdateInput{HourlyUSDOverride: &override, Revision: row.Revision}); err != nil {
		t.Fatal(err)
	}
	if st.Cost.Source != CostSourceOverride || st.Cost.HourlyUSD != "2.5000" || st.ProjectedUSD == "" {
		t.Errorf("override: %+v, projected %q", st.Cost, st.ProjectedUSD)
	}
}

func mustEntry(t *testing.T, id string) registry.Entry {
	t.Helper()
	e, ok := registry.Default().Entry(id)
	if !ok {
		t.Fatalf("no %s", id)
	}
	return e
}

func TestStatusExplainsAFailedStart(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()}, boxes()...)
	deployLlama(t, d, stagingID)
	failed := func(msg string, bound bool) *Status {
		t.Helper()
		d.facts.set(stagingID, llamaResource, catalogstate.State{Value: catalogstate.Failed, Message: msg, ConnectionID: gpuConn, Bound: bound})
		st, err := d.svc.Status(ctx, member, projectID, stagingID, llamaResource)
		if err != nil {
			t.Fatal(err)
		}
		if st.State != StateFailed || st.Message != msg {
			t.Errorf("state %q message %q", st.State, st.Message)
		}
		return st
	}
	memory := "The model didn't fit in GPU memory at 32768 tokens. Try a shorter context or a smaller quantization."
	if st := failed("torch.OutOfMemoryError: CUDA out of memory. Tried to allocate 2.00 GiB", false); st.Hint != memory {
		t.Errorf("first start: %q", st.Hint)
	}
	// A later deployment failed while the one before still runs.
	if st := failed("CUDA error: out of memory", true); st.Hint != memory+" "+hintStillServes {
		t.Errorf("update: %q", st.Hint)
	}
	license := "Your Hugging Face account hasn't accepted the license for " + llama8BID + ". Accept it at https://huggingface.co/" + llama8BID + ", then try again."
	if st := failed("401 Client Error: Unauthorized for url: https://huggingface.co/"+llama8BID+"/resolve/main/config.json", false); st.Hint != license {
		t.Errorf("license: %q", st.Hint)
	}
	if st := failed("exec format error", true); st.Hint != hintStillServes {
		t.Errorf("other failure: %q", st.Hint)
	}
}

// Rulings M21 and M23: a model deployed before a registry re-pin has an
// update, with the move to the pin, not an error.
func TestStatusOffersTheMoveToANewPin(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()}, boxes()...)
	deployLlama(t, d, stagingID)
	old := strings.Repeat("a", 40)
	if _, _, err := d.svc.d.Manifests.Edit(ctx, member, projectID, manifests.EditInput{UpdateResource: &manifests.ResourceUpdateInput{
		Resource: llamaResource, Settings: map[string]string{"revision": old},
	}}); err != nil {
		t.Fatal(err)
	}
	st, err := d.svc.Status(ctx, member, projectID, stagingID, llamaResource)
	if err != nil {
		t.Fatal(err)
	}
	if !st.UpdateAvailable || st.Pin == nil || st.Pin.Revision != llamaRev || !strings.Contains(st.Pin.Message, "Move it to the new pin") || st.State != StateReady {
		t.Errorf("update %v pin %+v state %q", st.UpdateAvailable, st.Pin, st.State)
	}
}

// saveServices adds services to the current manifest, each on a target.
func saveServices(t *testing.T, d *deploySetup, services map[string]manifest.Service) {
	t.Helper()
	ctx := context.Background()
	rev, err := d.revisions.CurrentRevision(ctx, "org", projectID)
	if err != nil {
		t.Fatal(err)
	}
	m := *rev.Manifest
	m.Services = services
	if _, _, err = manifests.NewService(d.revisions, memProjects{}, aiAdmin, nil).Save(ctx, member, projectID, manifests.SaveInput{Manifest: &m}); err != nil {
		t.Fatal(err)
	}
}

// linkedService is a service on a server, deployed there as the
// facts say, needing what it needs.
func linkedService(source string, needs ...string) manifest.Service {
	return manifest.Service{Source: source, Settings: manifest.Settings{Provider: "server"}, Needs: needs}
}

func TestStatusListsLinkedServices(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, []gpus.GPUTarget{l40sServer()}, boxes()...)
	deployLlama(t, d, productID)
	// worker needs the model and is off in production, so it never runs
	// there and isn't listed (final review M3).
	worker := linkedService("go:cmd/worker", llamaResource)
	no := false
	worker.Environments = map[string]manifest.Settings{"production": {Enabled: &no}}
	saveServices(t, d, map[string]manifest.Service{
		"checkout": linkedService("go:cmd/checkout", llamaResource),
		"api":      linkedService("go:cmd/api", llamaResource),
		"web":      linkedService("go:cmd/web"),
		"worker":   worker,
	})
	d.facts.bindService(productID, "checkout", gpuConn)
	d.facts.bindService(productID, "api", cpuConn)
	st, err := d.svc.Status(ctx, member, projectID, productID, llamaResource)
	if err != nil {
		t.Fatal(err)
	}
	want := []LinkedService{
		{Environment: "production", Service: "api", Message: "api runs on cpu-box, and " + llamaResource + " runs on gpu-box. Run them on the same server or cluster."},
		{Environment: "production", Service: "checkout", Reachable: true},
	}
	if len(st.Linked) != len(want) || st.Linked[0] != want[0] || st.Linked[1] != want[1] {
		t.Errorf("linked %+v", st.Linked)
	}
	// Its JSON is snake_case, as the API's is.
	raw, _ := json.Marshal(st.Linked[0])
	if string(raw) != `{"environment":"production","service":"api","reachable":false,"message":"`+want[0].Message+`"}` {
		t.Errorf("JSON %s", raw)
	}
}

func TestUpdateWritesTheBudgetAndOverrideOnly(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()}, boxes()...)
	row := deployLlama(t, d, stagingID)
	plans, revisions := len(d.plans.asked), d.revisions.count()
	budget, override := "300", "2.10"
	st, err := d.svc.Update(ctx, member, projectID, stagingID, llamaResource, UpdateInput{MonthlyBudgetUSD: &budget, HourlyUSDOverride: &override, Revision: row.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if st.Deployment.MonthlyBudgetUSD != "300" || st.Deployment.HourlyUSDOverride != "2.10" || st.Cost.HourlyUSD != "2.1000" || st.Cost.Source != CostSourceOverride {
		t.Errorf("updated %+v cost %+v", st.Deployment, st.Cost)
	}
	if len(d.plans.asked) != plans || d.revisions.count() != revisions || d.gateways.count() != 0 {
		t.Error("an update planned or edited something")
	}
	// The revision read is the one changed.
	if _, err = d.svc.Update(ctx, member, projectID, stagingID, llamaResource, UpdateInput{MonthlyBudgetUSD: &budget, Revision: row.Revision}); !errors.Is(err, ErrStale) {
		t.Errorf("stale: %v", err)
	}
	for _, bad := range []string{"0", "abc", "100001"} {
		if _, err = d.svc.Update(ctx, member, projectID, stagingID, llamaResource, UpdateInput{MonthlyBudgetUSD: &bad, Revision: st.Deployment.Revision}); !errors.Is(err, ErrInvalid) {
			t.Errorf("budget %q: %v", bad, err)
		}
	}
	cleared := ""
	if st, err = d.svc.Update(ctx, member, projectID, stagingID, llamaResource, UpdateInput{MonthlyBudgetUSD: &cleared, HourlyUSDOverride: &cleared, Revision: st.Deployment.Revision}); err != nil ||
		st.Deployment.MonthlyBudgetUSD != "" || st.Deployment.HourlyUSDOverride != "" || st.Cost.Source != CostSourceServer {
		t.Errorf("cleared: %+v, %v", st, err)
	}
	// Production's models need ai:manage.
	if _, err = d.svc.Update(ctx, member, projectID, productID, llamaResource, UpdateInput{MonthlyBudgetUSD: &budget}); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("developer in production: %v", err)
	}
}

func TestMoveReplansOntoTheNewTarget(t *testing.T) {
	ctx := context.Background()
	two := l4Server(true)
	two.ConnectionID, two.Name = "conn_gpu2", "gpu-two"
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer(), two}, boxes()...)
	row := deployLlama(t, d, stagingID)
	before := current(t, d).Resources[llamaResource]

	in := MoveInput{Target: TargetChoice{Source: TargetConnection, ConnectionID: "conn_gpu2", GPU: &provider.GPURequest{Kind: "nvidia-l4"}}}
	pv, err := d.svc.MovePreview(ctx, member, projectID, stagingID, llamaResource, in)
	if err != nil || len(pv.Problems) != 0 || !pv.CanDeploy || pv.Resource != llamaResource || pv.MaxModelLen != 22528 {
		t.Fatalf("preview: %+v, %v", pv, err)
	}
	if d.revisions.count() != 2 {
		t.Fatal("the move's preview edited the manifest")
	}
	res, err := d.svc.Move(ctx, member, projectID, stagingID, llamaResource, in)
	if err != nil {
		t.Fatal(err)
	}
	r := current(t, d).Resources[llamaResource]
	if r.Target != "conn_gpu2" || r.Provider != "server" || r.GPU == nil || *r.GPU != (manifest.GPU{Kind: "nvidia-l4", Count: 1}) || r.Kind != before.Kind {
		t.Errorf("moved resource %+v", r)
	}
	if r.Settings["max_model_len"] != "22528" || r.Settings["served_name"] != before.Settings["served_name"] || r.Settings["cache_mb"] != before.Settings["cache_mb"] || !enabledIn(r, "staging") {
		t.Errorf("settings %v", r.Settings)
	}
	if len(d.plans.asked) != 2 || d.plans.asked[1].Environment != "staging" || res.PlanID != "plan_2" {
		t.Errorf("plans %+v, result %+v", d.plans.asked, res)
	}
	got, _ := d.rows.Get(ctx, "org", projectID, stagingID, llamaResource)
	if got.ID != row.ID || got.ServerName != "gpu-two" || got.PlanID != "plan_2" || got.Quantization != "bf16" {
		t.Errorf("row %+v", got)
	}
	if d.gateways.count() != 1 {
		t.Errorf("gateways told %d times", d.gateways.count())
	}

	// A target it doesn't fit, or a move to production, is refused with
	// nothing written.
	revs := d.revisions.count()
	if _, err = d.svc.Move(ctx, member, projectID, stagingID, llamaResource, MoveInput{Target: TargetChoice{Source: TargetConnection, ConnectionID: "conn_nope", GPU: &provider.GPURequest{Kind: "nvidia-l4"}}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown target: %v", err)
	}
	if _, err = d.svc.Move(ctx, member, projectID, productID, llamaResource, in); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("developer in production: %v", err)
	}
	if d.revisions.count() != revs || len(d.plans.asked) != 2 {
		t.Error("a refused move wrote something")
	}
}

func TestRemovePlansAndSaysTheWeightsStay(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()}, boxes()...)
	deployLlama(t, d, stagingID)
	res, err := d.svc.Remove(ctx, member, projectID, stagingID, llamaResource)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := current(t, d).Resources[llamaResource]; ok {
		t.Error("the manifest still has it")
	}
	if len(d.plans.asked) != 2 || d.plans.asked[1].Environment != "staging" || res.PlanID != "plan_2" {
		t.Errorf("plans %+v", d.plans.asked)
	}
	if _, err = d.rows.Get(ctx, "org", projectID, stagingID, llamaResource); !errors.Is(err, ErrNotFound) {
		t.Errorf("row after remove: %v", err)
	}
	if !strings.Contains(res.Message, "weights stay on gpu-box as retained storage") {
		t.Errorf("message %q", res.Message)
	}
	if d.gateways.count() != 1 {
		t.Errorf("gateways told %d times", d.gateways.count())
	}

	// A plan that fails keeps the row, saying what to do.
	deployLlama(t, d, stagingID)
	d.plans.err = errors.New("planning is down")
	_, err = d.svc.Remove(ctx, member, projectID, stagingID, llamaResource)
	var after *PlanAfterEditError
	if !errors.As(err, &after) || !strings.HasPrefix(err.Error(), "The manifest no longer has "+llamaResource+". Plan staging from the project to remove it.") {
		t.Errorf("plan failure: %v", err)
	}
	if row, gerr := d.rows.Get(ctx, "org", projectID, stagingID, llamaResource); gerr != nil || !strings.Contains(row.Message, "Plan staging") {
		t.Errorf("row after a failed plan: %+v, %v", row, gerr)
	}
}

func TestRemoveRefusesWhileServicesLink(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()}, boxes()...)
	deployLlama(t, d, stagingID)
	saveServices(t, d, map[string]manifest.Service{
		"checkout": linkedService("go:cmd/checkout", llamaResource),
		"api":      linkedService("go:cmd/api", llamaResource),
	})
	revs := d.revisions.count()
	_, err := d.svc.Remove(ctx, member, projectID, stagingID, llamaResource)
	if !errors.Is(err, ErrInUse) || !strings.HasSuffix(err.Error(), "api and checkout in staging link to it. Unlink them first") {
		t.Errorf("remove while linked: %v", err)
	}
	saveServices(t, d, map[string]manifest.Service{"checkout": linkedService("go:cmd/checkout", llamaResource)})
	revs++
	if _, err = d.svc.Remove(ctx, member, projectID, stagingID, llamaResource); !errors.Is(err, ErrInUse) || !strings.HasSuffix(err.Error(), "checkout in staging links to it. Unlink it first") {
		t.Errorf("one service: %v", err)
	}
	if d.revisions.count() != revs || len(d.plans.asked) != 1 || d.gateways.count() != 0 {
		t.Error("a refused remove wrote something")
	}
	if _, err = d.rows.Get(ctx, "org", projectID, stagingID, llamaResource); err != nil {
		t.Errorf("the row went: %v", err)
	}
}

// Move and Remove each tell the gateways once, after their plan; a
// deploy's gateways hear from the guard's Ready hook instead.
func TestChangesTellGateways(t *testing.T) {
	ctx := context.Background()
	two := l40sServer()
	two.ConnectionID, two.Name = "conn_gpu2", "gpu-two"
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer(), two}, boxes()...)
	deployLlama(t, d, stagingID)
	if d.gateways.count() != 0 {
		t.Fatalf("deploy told the gateways %d times", d.gateways.count())
	}
	if _, err := d.svc.Move(ctx, member, projectID, stagingID, llamaResource, MoveInput{Target: TargetChoice{Source: TargetConnection, ConnectionID: "conn_gpu2", GPU: &provider.GPURequest{Kind: "nvidia-l40s"}}}); err != nil {
		t.Fatal(err)
	}
	if d.gateways.count() != 1 {
		t.Errorf("after move: %d", d.gateways.count())
	}
	if _, err := d.svc.Remove(ctx, member, projectID, stagingID, llamaResource); err != nil {
		t.Fatal(err)
	}
	if d.gateways.count() != 2 || d.gateways.calls[0] != "org/"+projectID {
		t.Errorf("after remove: %v", d.gateways.calls)
	}
	// SetGateways binds another, and nil unbinds it.
	other := &countGateways{}
	d.svc.SetGateways(other)
	d.svc.modelsChanged(ctx, "org", projectID)
	d.svc.SetGateways(nil)
	d.svc.modelsChanged(ctx, "org", projectID)
	if other.count() != 1 || d.gateways.count() != 2 {
		t.Errorf("rebinding: %d, %d", other.count(), d.gateways.count())
	}
}

// Retry plans a failed start again, from the manifest as it is, and
// refuses a model that's running.
func TestRetryReplansAFailedStart(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()}, boxes()...)
	deployLlama(t, d, stagingID)
	if _, err := d.svc.Retry(ctx, member, projectID, stagingID, llamaResource); !errors.Is(err, ErrInvalid) {
		t.Errorf("retry a ready model: %v", err)
	}
	d.facts.set(stagingID, llamaResource, catalogstate.State{Value: catalogstate.Failed, Message: "boom", ConnectionID: gpuConn})
	st, err := d.svc.Retry(ctx, member, projectID, stagingID, llamaResource)
	if err != nil || len(d.plans.asked) != 2 || st.Deployment.PlanID != "plan_2" || d.revisions.count() != 2 {
		t.Errorf("retry: %+v, %v, plans %+v", st, err, d.plans.asked)
	}
}

// Ruling M30: a move on the model's own one-GPU server gets its GPU back
// as free, since the runtimes stop the old engine before the new one
// starts. Another model still can't have it.
func TestMoveOnItsOwnServer(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()}, boxes()...)
	deployLlama(t, d, stagingID)
	// gpu-box's one GPU is the model's now.
	held := l40sServer()
	held.Free, held.Message = 0, "gpu-box has 1 NVIDIA L40S GPUs and 1 are given to other services, so 0 are free."
	d.targets.targets = []gpus.GPUTarget{held}

	res, err := d.svc.Move(ctx, member, projectID, stagingID, llamaResource, MoveInput{Target: onGPU("nvidia-l40s"), MaxModelLen: 16384})
	if err != nil {
		t.Fatal(err)
	}
	r := current(t, d).Resources[llamaResource]
	if r.Target != gpuConn || r.Settings["max_model_len"] != "16384" || r.GPU == nil || r.GPU.Count != 1 || res.PlanID == "" || res.Message != "" {
		t.Errorf("moved on its own server: %+v, %+v", r, res)
	}
	pv, err := d.svc.Preview(ctx, member, projectID, stagingID, DeployInput{EntryID: qwen7B, Target: onGPU("nvidia-l40s")})
	if err != nil || len(pv.Problems) != 1 || pv.Problems[0].Code != ProblemTarget || pv.Problems[0].Message != held.Message {
		t.Errorf("another model on the held GPU: %+v, %v", pv.Problems, err)
	}
}

// Ruling M30: an endpoint's key never prints or encodes.
func TestEndpointKeyNeverPrints(t *testing.T) {
	e := Endpoint{Resource: "llm", APIKey: Secret("lpm_supersecret")}
	raw, err := json.Marshal(e)
	if err != nil || strings.Contains(string(raw), "supersecret") || !strings.Contains(string(raw), `"APIKey":"[redacted]"`) {
		t.Errorf("JSON %s, %v", raw, err)
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		if got := fmt.Sprintf(verb, e); strings.Contains(got, "supersecret") {
			t.Errorf("%s: %s", verb, got)
		}
	}
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("endpoint", "key", e.APIKey)
	if strings.Contains(logged.String(), "supersecret") {
		t.Errorf("log %s", logged.String())
	}
	if e.APIKey.Reveal() != "lpm_supersecret" {
		t.Error("Reveal")
	}
}

// Ruling M30: a model added to the manifest by hand, which no deploy
// recorded, is listed too, with its GPU's cost.
func TestListIncludesModelsAddedByHand(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, []gpus.GPUTarget{l40sServer()}, boxes()...)
	deployLlama(t, d, stagingID)
	e := mustEntry(t, qwen7B)
	q, _ := e.Quantization("bf16")
	if _, _, err := d.svc.d.Manifests.Edit(ctx, member, projectID, manifests.EditInput{AddResource: &manifests.NewResource{
		Name: "handmade", Kind: recipes.ModelVLLMKind, Provider: "server", Target: gpuConn, Version: "0.31",
		Settings: engines.Settings(engines.Spec{Entry: e, Quantization: q, ServedName: qwen7B, MaxModelLen: 8192, GPUs: 1}),
		GPU:      &manifest.GPU{Kind: "nvidia-l40s", Count: 1}, EnabledIn: "production",
	}}); err != nil {
		t.Fatal(err)
	}
	list, err := d.svc.List(ctx, member)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %+v, %v", list, err)
	}
	hand := list[1]
	if hand.Deployment != nil || hand.Entry.ID != qwen7B || hand.Kind != recipes.ModelVLLMKind || hand.Environments[0].Environment != "production" {
		t.Errorf("hand-made model %+v", hand)
	}
	if hand.ProjectID != projectID || hand.Resource != "handmade" {
		t.Errorf("hand-made model names project %q, resource %q", hand.ProjectID, hand.Resource)
	}
	if row := list[0]; row.ProjectID != projectID || row.Resource != llamaResource {
		t.Errorf("deployed model names project %q, resource %q", row.ProjectID, row.Resource)
	}
	if hand.Cost.Source != CostSourceServer || hand.Cost.HourlyUSD != "1.5700" || hand.ProjectedUSD == "" {
		t.Errorf("hand-made cost %+v, projected %q", hand.Cost, hand.ProjectedUSD)
	}
	// Someone who can't read the project sees none of it.
	if list, err = newDeploy(t, deployRegistry(t, llamaRev, ""), viewer, nil).svc.List(ctx, member); err != nil || len(list) != 0 {
		t.Errorf("without project:read: %+v, %v", list, err)
	}
}

// Ruling M30: DeployInput.Moving is Move's own. Sent by a client, Preview
// and Deploy ignore it, so it can't skip the name check.
func TestClientCantSetMoving(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()}, boxes()...)
	deployLlama(t, d, stagingID)
	in := DeployInput{EntryID: llama8BID, Target: onGPU("nvidia-l40s"), Resource: llamaResource, Moving: &MovingFrom{Kind: recipes.ModelVLLMKind}}
	pv, err := d.svc.Preview(ctx, member, projectID, stagingID, in)
	if err != nil || len(pv.Problems) == 0 || pv.Problems[0].Code != ProblemExists {
		t.Errorf("preview: %+v, %v", pv.Problems, err)
	}
	revs := d.revisions.count()
	var refused *ProblemError
	if _, err = d.svc.Deploy(ctx, member, projectID, stagingID, in); !errors.As(err, &refused) || refused.Problem.Code != ProblemExists || d.revisions.count() != revs {
		t.Errorf("deploy: %v", err)
	}
}

// Ruling M31: a move onto the model's own server gives back the GPUs its
// deployed engine holds, never what the manifest asks for. One that was
// planned and never deployed holds none, so it can't take a GPU another
// service holds.
func TestMoveCreditsOnlyTheGPUsItsEngineHolds(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()}, boxes()...)
	d.grantLlama()
	if _, err := d.svc.Deploy(ctx, member, projectID, stagingID, DeployInput{EntryID: llama8BID, Target: onGPU("nvidia-l40s")}); err != nil {
		t.Fatal(err)
	}
	held := l40sServer()
	held.Free, held.Message = 0, "gpu-box has 1 NVIDIA L40S GPUs and 1 are given to other services, so 0 are free."
	d.targets.targets = []gpus.GPUTarget{held}
	var refused *ProblemError
	_, err := d.svc.Move(ctx, member, projectID, stagingID, llamaResource, MoveInput{Target: onGPU("nvidia-l40s"), MaxModelLen: 16384})
	if !errors.As(err, &refused) || refused.Problem.Code != ProblemTarget || refused.Problem.Message != held.Message {
		t.Fatalf("move of an undeployed model onto a held GPU: %v", err)
	}
	// Once its engine holds the GPU, it's its own to move onto.
	r := current(t, d).Resources[llamaResource]
	d.facts.running(stagingID, llamaResource, r.Kind, r.Settings, 1)
	if _, err = d.svc.Move(ctx, member, projectID, stagingID, llamaResource, MoveInput{Target: onGPU("nvidia-l40s"), MaxModelLen: 16384}); err != nil {
		t.Errorf("move on its own GPU: %v", err)
	}
}

// Second and third servers with one free L40S each.
const (
	gpuConn3 = "conn_gpu3"
	gpuConn4 = "conn_gpu4"
)

// l40sOn is a server with one free L40S at hourly dollars an hour.
func l40sOn(id, name string, hourly float64) gpus.GPUTarget {
	t := l40sServer()
	t.ConnectionID, t.Name, t.HourlyUSD = id, name, hourly
	return t
}

// copyServers are gpu-box and gpu-box-2 to 4, all with a free L40S at
// $1.57 an hour, and cpu-box.
func copyServers() ([]gpus.GPUTarget, []*connections.Connection) {
	targets := []gpus.GPUTarget{l40sServer(), l40sOn(gpuConn2, "gpu-box-2", 1.57), l40sOn(gpuConn3, "gpu-box-3", 1.57), l40sOn(gpuConn4, "gpu-box-4", 1.57)}
	conns := boxes()
	for _, t := range targets[1:] {
		conns = append(conns, &connections.Connection{ID: t.ConnectionID, Name: t.Name, Cloud: connections.Server, Status: connections.StatusReady})
	}
	return targets, conns
}

// on is an environment block that turns a resource on there.
func on() manifest.ResourceSettings {
	yes := true
	return manifest.ResourceSettings{Enabled: &yes}
}

// llmSettings are Qwen 7B's bf16 settings on one GPU with a context of
// 8192.
func llmSettings(t *testing.T) map[string]string {
	t.Helper()
	e := mustEntry(t, qwen7B)
	q, _ := e.Quantization("bf16")
	return engines.Settings(engines.Spec{Entry: e, Quantization: q, ServedName: qwen7B, MaxModelLen: 8192, GPUs: 1})
}

// putLLM saves llm, Qwen 7B bf16 on vLLM on gpu-box's L40S with a context
// of 8192, with the environment blocks envs.
func putLLM(t *testing.T, d *deploySetup, envs map[string]manifest.ResourceSettings) {
	t.Helper()
	putResource(t, d, "llm", manifest.Resource{
		Kind: recipes.ModelVLLMKind, Provider: "server", Target: gpuConn, Version: "0.31", GPU: &manifest.GPU{Kind: "nvidia-l40s", Count: 1},
		Settings: llmSettings(t), Environments: envs,
	})
}

// putResource saves r as the manifest's resource name.
func putResource(t *testing.T, d *deploySetup, name string, r manifest.Resource) {
	t.Helper()
	m := *current(t, d)
	m.Resources = maps.Clone(m.Resources)
	if m.Resources == nil {
		m.Resources = map[string]manifest.Resource{}
	}
	m.Resources[name] = r
	if _, _, err := manifests.NewService(d.revisions, memProjects{}, aiAdmin, nil).Save(context.Background(), member, projectID, manifests.SaveInput{Manifest: &m}); err != nil {
		t.Fatal(err)
	}
}

// llmRow records a row for llm in env, as a deploy there would have.
func llmRow(t *testing.T, d *deploySetup, env string) *Deployment {
	t.Helper()
	row := &Deployment{OrgID: "org", ProjectID: projectID, EnvironmentID: env, Resource: "llm", EntryID: qwen7B, Quantization: "bf16",
		Engine: "vllm", Phase: PhaseActive}
	if err := d.rows.Create(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestStatusReadsTheEnvironmentsCopy(t *testing.T) {
	ctx := context.Background()
	targets, conns := copyServers()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, targets, conns...)
	production := on()
	production.Target, production.Settings = gpuConn2, map[string]string{"max_model_len": "4096"}
	putLLM(t, d, map[string]manifest.ResourceSettings{"staging": on(), "production": production})

	st, err := d.svc.Status(ctx, member, projectID, productID, "llm")
	if err != nil {
		t.Fatal(err)
	}
	if st.ConnectionID != gpuConn2 || st.ConnectionName != "gpu-box-2" || st.MaxModelLen != 4096 || st.Cost.HourlyUSD != "1.5700" {
		t.Errorf("production's copy: %q %q, context %d, cost %+v", st.ConnectionID, st.ConnectionName, st.MaxModelLen, st.Cost)
	}
	want := []Reach{
		{EnvironmentID: productID, Environment: "production", Mode: ReachHome, Message: "Runs in production. Services in production link to this copy."},
		{EnvironmentID: stagingID, Environment: "staging", Mode: ReachHome, Message: "Runs in staging. Services in staging link to this copy."},
	}
	if !slices.Equal(st.Environments, want) {
		t.Errorf("environments %+v", st.Environments)
	}

	st, err = d.svc.Status(ctx, member, projectID, stagingID, "llm")
	if err != nil || st.ConnectionID != gpuConn || st.MaxModelLen != 8192 || len(st.Environments) != 2 || st.Environments[0].Environment != "staging" {
		t.Errorf("staging's copy: %+v, %v", st, err)
	}
}

func TestListShowsEachEnvironmentCopy(t *testing.T) {
	ctx := context.Background()
	targets, conns := copyServers()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, targets, conns...)
	production := on()
	production.Target = gpuConn2
	putLLM(t, d, map[string]manifest.ResourceSettings{"staging": on(), "production": production})
	llmRow(t, d, stagingID)
	llmRow(t, d, productID)

	list, err := d.svc.List(ctx, member)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %+v, %v", list, err)
	}
	got := map[string]string{}
	for _, st := range list {
		got[st.Deployment.EnvironmentID] = st.ConnectionID
	}
	if !maps.Equal(got, map[string]string{stagingID: gpuConn, productID: gpuConn2}) {
		t.Errorf("copies %v", got)
	}
}

// countingStore counts the reads of a project's rows.
type countingStore struct {
	Store
	lists int
}

func (c *countingStore) ListProject(ctx context.Context, orgID, projectID string) ([]*Deployment, error) {
	c.lists++
	return c.Store.ListProject(ctx, orgID, projectID)
}

// countingProjects counts the reads of a project's environments.
type countingProjects struct {
	memProjects
	lists int
}

func (c *countingProjects) ListEnvironments(ctx context.Context, orgID, projectID string) ([]*projects.Environment, error) {
	c.lists++
	return c.memProjects.ListEnvironments(ctx, orgID, projectID)
}

// Final review M8: a list reads the project's rows and environments once,
// however many models it has.
func TestListReadsTheProjectOnce(t *testing.T) {
	ctx := context.Background()
	targets, conns := copyServers()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, targets, conns...)
	putLLM(t, d, map[string]manifest.ResourceSettings{"staging": on(), "production": on()})
	putResource(t, d, "chat", current(t, d).Resources["llm"])
	llmRow(t, d, stagingID)
	store, envs := &countingStore{Store: d.rows}, &countingProjects{}
	d.svc.d.Store, d.svc.d.Projects = store, envs

	list, err := d.svc.List(ctx, member)
	if err != nil || len(list) != 4 {
		t.Fatalf("list %d, %v", len(list), err)
	}
	if store.lists != 1 || envs.lists != 1 {
		t.Errorf("read the rows %d times and the environments %d times", store.lists, envs.lists)
	}
	for _, st := range list {
		if len(st.Environments) != 2 {
			t.Errorf("%s: environments %+v", st.Resource, st.Environments)
		}
	}
}

// Review Focus 3: moving one environment's copy leaves the other's alone.
func TestMoveOneEnvironmentLeavesTheOther(t *testing.T) {
	ctx := context.Background()
	moveTo := func(conn string) MoveInput {
		return MoveInput{Target: TargetChoice{Source: TargetConnection, ConnectionID: conn, GPU: &provider.GPURequest{Kind: "nvidia-l40s"}},
			Quantization: "bf16", MaxModelLen: 8192}
	}
	targets, conns := copyServers()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, targets, conns...)
	production := on()
	production.Target = gpuConn2
	putLLM(t, d, map[string]manifest.ResourceSettings{"staging": on(), "production": production})
	edits := d.recordEdits()

	// Production has a target of its own: its block moves.
	if _, err := d.svc.Move(ctx, member, projectID, productID, "llm", moveTo(gpuConn3)); err != nil {
		t.Fatal(err)
	}
	if len(edits.edits) != 1 || edits.edits[0].UpdateResource == nil {
		t.Fatalf("edits %+v", edits.edits)
	}
	if u := edits.edits[0].UpdateResource; u.Environment != "production" || *u.Target != gpuConn3 || u.Provider != nil || u.GPU != nil || len(u.Settings) != 0 || u.Enabled != nil {
		t.Errorf("production's move %+v", u)
	}
	m := current(t, d)
	llm := m.Resources["llm"]
	if llm.Target != gpuConn || llm.For("staging", m.Defaults).Target != gpuConn || llm.For("production", m.Defaults).Target != gpuConn3 {
		t.Errorf("after production's move %+v", llm)
	}
	if len(d.plans.asked) != 1 || d.plans.asked[0].Environment != "production" {
		t.Errorf("plans %+v", d.plans.asked)
	}

	// No other environment reads the top-level target: staging moves it.
	if _, err := d.svc.Move(ctx, member, projectID, stagingID, "llm", moveTo(gpuConn4)); err != nil {
		t.Fatal(err)
	}
	if u := edits.edits[1].UpdateResource; u.Environment != "" || *u.Target != gpuConn4 {
		t.Errorf("staging's move %+v", u)
	}
	m = current(t, d)
	llm = m.Resources["llm"]
	if llm.Target != gpuConn4 || llm.For("production", m.Defaults).Target != gpuConn3 || !maps.Equal(llm.For("production", m.Defaults).Settings, llmSettings(t)) {
		t.Errorf("after staging's move %+v", llm)
	}
	if len(d.plans.asked) != 2 || d.plans.asked[1].Environment != "staging" {
		t.Errorf("plans %+v", d.plans.asked)
	}

	// Production reading the top-level target keeps it: staging's block
	// moves.
	putLLM(t, d, map[string]manifest.ResourceSettings{"staging": on(), "production": on()})
	if _, err := d.svc.Move(ctx, member, projectID, stagingID, "llm", moveTo(gpuConn4)); err != nil {
		t.Fatal(err)
	}
	if u := edits.edits[2].UpdateResource; u.Environment != "staging" || *u.Target != gpuConn4 {
		t.Errorf("staging's move beside production %+v", u)
	}
	m = current(t, d)
	if llm = m.Resources["llm"]; llm.Target != gpuConn || llm.For("production", m.Defaults).Target != gpuConn || llm.For("staging", m.Defaults).Target != gpuConn4 {
		t.Errorf("after staging's move beside production %+v", llm)
	}
}

// moveUpdate edits the top level only when no other copy reads what it
// changes there.
func TestMoveUpdateLeavesSharedSettings(t *testing.T) {
	e := mustEntry(t, qwen7B)
	q, _ := e.Quantization("bf16")
	settings := llmSettings(t)
	raw := manifest.Resource{Kind: recipes.ModelVLLMKind, Provider: "server", Target: gpuConn, GPU: &manifest.GPU{Kind: "nvidia-l40s", Count: 1},
		Settings: settings, Environments: map[string]manifest.ResourceSettings{"staging": on(), "production": {Enabled: on().Enabled, Target: gpuConn2}}}
	longer := engines.Settings(engines.Spec{Entry: e, Quantization: q, ServedName: qwen7B, MaxModelLen: 16384, GPUs: 1})
	pv := &DeployPreview{Resource: "llm", Target: TargetChoice{ConnectionID: gpuConn3, Provider: "server"}, GPU: &provider.GPURequest{Kind: "nvidia-l40s", Count: 1}}
	cur := raw.In("staging", manifest.Defaults{})

	// Production reads the top-level context, so staging's longer one goes
	// in its block.
	u := mustUpdate(t)(moveUpdate(raw, cur, "staging", manifest.Defaults{}, pv, maps.Clone(longer)))
	if u.Environment != "staging" || *u.Target != gpuConn3 || !maps.Equal(u.Settings, map[string]string{"max_model_len": "16384"}) {
		t.Errorf("a longer context beside production: %+v", u)
	}
	// A model added by hand with no top-level context: adding one there
	// would reach production's copy too.
	bare := raw
	bare.Settings = maps.Clone(settings)
	delete(bare.Settings, "max_model_len")
	if u = mustUpdate(t)(moveUpdate(bare, cur, "staging", manifest.Defaults{}, pv, maps.Clone(longer))); u.Environment != "staging" || u.Settings["max_model_len"] != "16384" {
		t.Errorf("no top-level context: %+v", u)
	}
	// staging's own block sets the context: moving it changes the block,
	// even where the new value is the top level's.
	own := raw
	own.Environments = maps.Clone(raw.Environments)
	own.Environments["production"] = manifest.ResourceSettings{Enabled: on().Enabled, Target: gpuConn2, Settings: map[string]string{"max_model_len": "8192"}}
	own.Environments["staging"] = manifest.ResourceSettings{Enabled: on().Enabled, Settings: map[string]string{"max_model_len": "4096"}}
	if u = mustUpdate(t)(moveUpdate(own, own.In("staging", manifest.Defaults{}), "staging", manifest.Defaults{}, pv, maps.Clone(settings))); u.Environment != "staging" || u.Settings["max_model_len"] != "8192" {
		t.Errorf("staging's own context: %+v", u)
	}
	// With production's own context, the top level is staging's alone.
	raw.Environments["production"] = manifest.ResourceSettings{Enabled: on().Enabled, Target: gpuConn2, Settings: map[string]string{"max_model_len": "8192"}}
	if u = mustUpdate(t)(moveUpdate(raw, cur, "staging", manifest.Defaults{}, pv, maps.Clone(longer))); u.Environment != "" || u.Settings["max_model_len"] != "16384" {
		t.Errorf("production on its own context: %+v", u)
	}
}

// Review Focus 3 and Decision 4: removing one environment's copy turns it
// off there and leaves the resource, the other copy and its row alone,
// even while a service there links to it.
func TestRemoveOneEnvironmentKeepsTheResource(t *testing.T) {
	ctx := context.Background()
	targets, conns := copyServers()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, targets, conns...)
	production := on()
	production.Target, production.Settings = gpuConn2, map[string]string{"max_model_len": "4096"}
	putLLM(t, d, map[string]manifest.ResourceSettings{"staging": on(), "production": production})
	// worker is off in production, so it loses nothing there.
	worker := linkedService("go:cmd/worker", "llm")
	no := false
	worker.Environments = map[string]manifest.Settings{"production": {Enabled: &no}}
	saveServices(t, d, map[string]manifest.Service{"checkout": linkedService("go:cmd/checkout", "llm"), "worker": worker})
	staged := llmRow(t, d, stagingID)
	llmRow(t, d, productID)
	edits := d.recordEdits()

	res, err := d.svc.Remove(ctx, member, projectID, productID, "llm")
	if err != nil {
		t.Fatal(err)
	}
	if len(edits.edits) != 1 || edits.edits[0].RemoveResource != "" || edits.edits[0].UpdateResource == nil {
		t.Fatalf("edits %+v", edits.edits)
	}
	u := edits.edits[0].UpdateResource
	if u.Resource != "llm" || u.Environment != "production" || u.Enabled == nil || *u.Enabled || u.Target == nil || *u.Target != "" ||
		!maps.Equal(u.Settings, map[string]string{"max_model_len": ""}) {
		t.Errorf("update %+v", u)
	}
	m := current(t, d)
	llm := m.Resources["llm"]
	if !enabledIn(llm, "staging") || enabledIn(llm, "production") || llm.Environments["production"].Target != "" ||
		len(llm.Environments["production"].Settings) != 0 || llm.Target != gpuConn {
		t.Errorf("manifest %+v", llm)
	}
	if len(d.plans.asked) != 1 || d.plans.asked[0].Environment != "production" || res.PlanID == "" {
		t.Errorf("plans %+v", d.plans.asked)
	}
	if _, err = d.rows.Get(ctx, "org", projectID, productID, "llm"); !errors.Is(err, ErrNotFound) {
		t.Errorf("production's row: %v", err)
	}
	if after, gerr := d.rows.Get(ctx, "org", projectID, stagingID, "llm"); gerr != nil || after.Revision != staged.Revision {
		t.Errorf("staging's row: %+v, %v", after, gerr)
	}
	want := "checkout in production gets no MODEL_URL once this plan is approved. The plan warns need-off for it until you deploy llm to production again or unlink it."
	if !strings.Contains(res.Message, "weights stay on gpu-box-2 as retained storage") || !strings.HasSuffix(res.Message, " "+want) ||
		strings.Contains(res.Message, "worker") {
		t.Errorf("message %q", res.Message)
	}

	// Production's plan, from the manifest as the remove left it, warns
	// need-off for checkout. Staging's doesn't.
	needOff := func(env string) bool {
		r := planCopies(t, m, env)
		return slices.ContainsFunc(r.Warnings, func(f planning.Finding) bool { return f.Code == "need-off" && f.Name == "checkout" })
	}
	if !needOff("production") || needOff("staging") {
		t.Errorf("need-off: production %v, staging %v", needOff("production"), needOff("staging"))
	}
}

// planCopies is the planner's result for env from m, with checkout in the
// repository.
func planCopies(t *testing.T, m *manifest.Manifest, env string) *planning.Result {
	t.Helper()
	g := &graph.Graph{Services: []graph.Service{{ID: "svc:go:cmd/checkout", Name: "checkout", Kind: graph.HTTPService, Deployable: true,
		Ecosystem: discovery.Go, Path: "cmd/checkout", Entrypoint: "cmd/checkout", Ports: []int{8080}, Image: "Dockerfile", Context: "."}}}
	return planning.Compute(planning.Input{Environment: env, Graph: g, Manifest: m, Providers: providers.Registry(), Kinds: recipes.Default()})
}

// Review Focus 3: removing the last environment removes the resource, and
// a service that needs it stops that.
func TestRemoveLastEnvironmentRemovesTheResource(t *testing.T) {
	ctx := context.Background()
	targets, conns := copyServers()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, targets, conns...)
	putLLM(t, d, map[string]manifest.ResourceSettings{"staging": on()})
	saveServices(t, d, map[string]manifest.Service{"checkout": linkedService("go:cmd/checkout", "llm")})
	edits := d.recordEdits()
	if _, err := d.svc.Remove(ctx, member, projectID, stagingID, "llm"); !errors.Is(err, ErrInUse) || len(edits.edits) != 0 {
		t.Errorf("linked: %v, edits %+v", err, edits.edits)
	}
	saveServices(t, d, map[string]manifest.Service{})
	if _, err := d.svc.Remove(ctx, member, projectID, stagingID, "llm"); err != nil {
		t.Fatal(err)
	}
	if len(edits.edits) != 1 || edits.edits[0].RemoveResource != "llm" || edits.edits[0].UpdateResource != nil {
		t.Errorf("edits %+v", edits.edits)
	}
	if _, ok := current(t, d).Resources["llm"]; ok {
		t.Error("the manifest still has llm")
	}
}

// joinMeanwhile has production join resource ahead of the first edit
// edits sees, reading the top level: another environment's deploy saving
// between this change's read and its edit.
func joinMeanwhile(t *testing.T, d *deploySetup, resource string) *recordingManifests {
	t.Helper()
	inner := d.svc.d.Manifests
	edits := d.recordEdits()
	edits.before = func(n int) error {
		if n != 1 {
			return nil
		}
		yes := true
		_, _, err := inner.Edit(context.Background(), member, projectID, manifests.EditInput{UpdateResource: &manifests.ResourceUpdateInput{
			Resource: resource, Environment: "production", Enabled: &yes,
		}})
		if err != nil {
			t.Fatal(err)
		}
		return nil
	}
	return edits
}

// Final review I1: staging runs llm alone, and production joins between
// Remove(staging)'s read and its edit. The remove is decided again, so
// it turns staging off and leaves llm and production's copy alone.
func TestRemoveDecidesAgainWhenACopyJoinsMeanwhile(t *testing.T) {
	ctx := context.Background()
	targets, conns := copyServers()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, targets, conns...)
	putLLM(t, d, map[string]manifest.ResourceSettings{"staging": on()})
	llmRow(t, d, stagingID)
	edits := joinMeanwhile(t, d, "llm")

	if _, err := d.svc.Remove(ctx, member, projectID, stagingID, "llm"); err != nil {
		t.Fatal(err)
	}
	if len(edits.edits) != 2 || edits.edits[0].RemoveResource != "llm" || edits.edits[1].UpdateResource == nil {
		t.Fatalf("edits %+v", edits.edits)
	}
	if u := edits.edits[1].UpdateResource; u.Environment != "staging" || u.Enabled == nil || *u.Enabled {
		t.Errorf("the second decision %+v", u)
	}
	m := current(t, d)
	llm, ok := m.Resources["llm"]
	if !ok || !enabledIn(llm, "production") || enabledIn(llm, "staging") || llm.For("production", m.Defaults).Target != gpuConn {
		t.Errorf("manifest %+v", m.Resources)
	}
}

// Final review I1: a top-level move of staging's copy that production
// joins meanwhile goes to staging's block instead, so production's copy
// stays where it is.
func TestMoveDecidesAgainWhenACopyJoinsMeanwhile(t *testing.T) {
	ctx := context.Background()
	targets, conns := copyServers()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, targets, conns...)
	putLLM(t, d, map[string]manifest.ResourceSettings{"staging": on()})
	edits := joinMeanwhile(t, d, "llm")

	_, err := d.svc.Move(ctx, member, projectID, stagingID, "llm", MoveInput{
		Target:       TargetChoice{Source: TargetConnection, ConnectionID: gpuConn4, GPU: &provider.GPURequest{Kind: "nvidia-l40s"}},
		Quantization: "bf16", MaxModelLen: 8192,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(edits.edits) != 2 || edits.edits[0].UpdateResource.Environment != "" || edits.edits[1].UpdateResource.Environment != "staging" {
		t.Fatalf("edits %+v", edits.edits)
	}
	m := current(t, d)
	llm := m.Resources["llm"]
	if llm.Target != gpuConn || llm.For("production", m.Defaults).Target != gpuConn || llm.For("staging", m.Defaults).Target != gpuConn4 {
		t.Errorf("manifest %+v", llm)
	}
}

// A change that races twice changes nothing, and says why.
func TestRemoveRacedTwiceIsAConflict(t *testing.T) {
	ctx := context.Background()
	targets, conns := copyServers()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, targets, conns...)
	putLLM(t, d, map[string]manifest.ResourceSettings{"staging": on()})
	edits := d.recordEdits()
	edits.before = func(int) error { return manifests.ErrConflict }

	_, err := d.svc.Remove(ctx, member, projectID, stagingID, "llm")
	if !errors.Is(err, manifests.ErrConflict) || !strings.Contains(err.Error(), "changed twice while removing llm from staging") || len(edits.edits) != 2 {
		t.Fatalf("err %v, edits %d", err, len(edits.edits))
	}
	if _, ok := current(t, d).Resources["llm"]; !ok || len(d.plans.asked) != 0 {
		t.Errorf("a refused remove changed something: plans %+v", d.plans.asked)
	}
}

// Final review M1: a copy that waits for its GPU server in staging is
// another copy, so removing production's keeps llm for staging to join,
// even while checkout links to it.
func TestRemoveKeepsTheResourceForAWaitingCopy(t *testing.T) {
	ctx := context.Background()
	targets, conns := copyServers()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, targets, conns...)
	putLLM(t, d, map[string]manifest.ResourceSettings{"production": on()})
	saveServices(t, d, map[string]manifest.Service{"checkout": linkedService("go:cmd/checkout", "llm")})
	llmRow(t, d, productID)
	waiting := llmRow(t, d, stagingID)
	waiting.Phase = PhaseWaitingForServer
	if err := d.rows.Update(ctx, waiting); err != nil {
		t.Fatal(err)
	}
	edits := d.recordEdits()

	res, err := d.svc.Remove(ctx, member, projectID, productID, "llm")
	if err != nil {
		t.Fatal(err)
	}
	if len(edits.edits) != 1 || edits.edits[0].UpdateResource == nil || edits.edits[0].UpdateResource.Environment != "production" {
		t.Fatalf("edits %+v", edits.edits)
	}
	if llm, ok := current(t, d).Resources["llm"]; !ok || enabledIn(llm, "production") {
		t.Errorf("manifest %+v", llm)
	}
	if !strings.Contains(res.Message, "checkout in production gets no MODEL_URL") {
		t.Errorf("message %q", res.Message)
	}
}

// Final review M2: a block left by qa, an environment the project no
// longer has, is no other copy. Removing staging's copy is removing the
// last one: refused while checkout links to it, and the resource goes
// once it doesn't. A move of staging's copy edits the top level, which
// only staging reads.
func TestAGoneEnvironmentsBlockIsNoOtherCopy(t *testing.T) {
	ctx := context.Background()
	targets, conns := copyServers()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, targets, conns...)
	putLLM(t, d, map[string]manifest.ResourceSettings{"staging": on(), "qa": on()})
	saveServices(t, d, map[string]manifest.Service{"checkout": linkedService("go:cmd/checkout", "llm")})
	edits := d.recordEdits()

	if _, err := d.svc.Move(ctx, member, projectID, stagingID, "llm", MoveInput{
		Target:       TargetChoice{Source: TargetConnection, ConnectionID: gpuConn4, GPU: &provider.GPURequest{Kind: "nvidia-l40s"}},
		Quantization: "bf16", MaxModelLen: 8192,
	}); err != nil {
		t.Fatal(err)
	}
	if len(edits.edits) != 1 || edits.edits[0].UpdateResource.Environment != "" {
		t.Fatalf("staging's move: %+v", edits.edits)
	}

	if _, err := d.svc.Remove(ctx, member, projectID, stagingID, "llm"); !errors.Is(err, ErrInUse) || len(edits.edits) != 1 {
		t.Fatalf("linked: %v, edits %+v", err, edits.edits)
	}
	saveServices(t, d, map[string]manifest.Service{})
	if _, err := d.svc.Remove(ctx, member, projectID, stagingID, "llm"); err != nil {
		t.Fatal(err)
	}
	if len(edits.edits) != 2 || edits.edits[1].RemoveResource != "llm" {
		t.Errorf("edits %+v", edits.edits)
	}
	if _, ok := current(t, d).Resources["llm"]; ok {
		t.Error("the manifest still has llm, on in qa alone")
	}
}

// waitingLLM records a row for llm in env that waits for its GPU server,
// as an add there would have; moving, when set, makes it a move's.
func waitingLLM(t *testing.T, d *deploySetup, env string, moving *MovingFrom) {
	t.Helper()
	row := llmRow(t, d, env)
	row.Phase, row.Request = PhaseWaitingForServer, &DeployInput{EntryID: qwen7B, Resource: "llm", Moving: moving}
	if err := d.rows.Update(context.Background(), row); err != nil {
		t.Fatal(err)
	}
}

// Ruling E8: production's copy is removed while staging's waits, which
// keeps llm for staging (M1). Cancelling staging's wait leaves llm
// running nowhere, so it goes from the manifest too. While checkout
// needs it, it stays, and the cancel says to unlink checkout first. A
// failed wait, one that needs attention, is cleaned up the same way.
func TestCancelRemovesAModelLeftInNoEnvironment(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		linked bool
		phase  string
	}{{false, PhaseWaitingForServer}, {true, PhaseWaitingForServer}, {false, PhaseNeedsAttention}} {
		linked := c.linked
		targets, conns := copyServers()
		d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, targets, conns...)
		putLLM(t, d, map[string]manifest.ResourceSettings{"production": on()})
		if linked {
			saveServices(t, d, map[string]manifest.Service{"checkout": linkedService("go:cmd/checkout", "llm")})
		}
		llmRow(t, d, productID)
		waitingLLM(t, d, stagingID, nil)
		if _, err := d.svc.Remove(ctx, member, projectID, productID, "llm"); err != nil {
			t.Fatal(err)
		}
		if _, ok := current(t, d).Resources["llm"]; !ok {
			t.Fatal("Remove took llm out while staging waits for it")
		}
		if c.phase == PhaseNeedsAttention {
			row, gerr := d.rows.Get(ctx, "org", projectID, stagingID, "llm")
			if gerr != nil {
				t.Fatal(gerr)
			}
			row.Phase = PhaseNeedsAttention
			if uerr := d.rows.Update(ctx, row); uerr != nil {
				t.Fatal(uerr)
			}
		}

		res, err := d.svc.CancelWaiting(ctx, member, projectID, stagingID, "llm")
		if err != nil {
			t.Fatal(err)
		}
		_, kept := current(t, d).Resources["llm"]
		want := "llm stays in the project's manifest, running in no environment, because checkout needs it. Unlink it first, then remove llm from the manifest."
		switch {
		case !linked && (kept || res.Message != ""):
			t.Errorf("%s, unlinked: kept %v, message %q", c.phase, kept, res.Message)
		case linked && (!kept || res.Message != want):
			t.Errorf("linked: kept %v, message %q", kept, res.Message)
		}
	}
}

// A cancelled move leaves the model where it runs, in the manifest.
func TestCancelledMoveLeavesTheResource(t *testing.T) {
	ctx := context.Background()
	targets, conns := copyServers()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, targets, conns...)
	putLLM(t, d, map[string]manifest.ResourceSettings{"staging": on()})
	waitingLLM(t, d, stagingID, &MovingFrom{Kind: recipes.ModelVLLMKind, ConnectionID: gpuConn, Phase: PhaseActive})
	edits := d.recordEdits()

	res, err := d.svc.CancelWaiting(ctx, member, projectID, stagingID, "llm")
	if err != nil || res.Message != "" {
		t.Fatalf("cancel: %+v, %v", res, err)
	}
	if llm, ok := current(t, d).Resources["llm"]; !ok || !enabledIn(llm, "staging") || len(edits.edits) != 0 {
		t.Errorf("manifest %+v, edits %+v", llm, edits.edits)
	}
	if row, gerr := d.rows.Get(ctx, "org", projectID, stagingID, "llm"); gerr != nil || row.Phase != PhaseActive {
		t.Errorf("row %+v, %v", row, gerr)
	}
}

func TestRemovalUpdateClearsTheBlock(t *testing.T) {
	block := on()
	block.Target, block.Provider, block.GPU = gpuConn2, "kubernetes", &manifest.GPU{Kind: "nvidia-h100", Count: 1}
	block.Settings = map[string]string{"quantization": "awq-int4", "cache_mb": "20000"}
	raw := manifest.Resource{Environments: map[string]manifest.ResourceSettings{"production": block, "staging": on()}}
	u := removalUpdate(raw, "production")
	if u.Environment != "production" || u.Enabled == nil || *u.Enabled || *u.Target != "" || *u.Provider != "" || !u.ClearGPU ||
		!maps.Equal(u.Settings, map[string]string{"quantization": "", "cache_mb": ""}) {
		t.Errorf("update %+v", u)
	}
	if u = removalUpdate(raw, "staging"); u.ClearGPU || u.Settings != nil {
		t.Errorf("a bare block %+v", u)
	}
}

// onKubernetes says resource's engine in staging runs on the kube
// cluster, with its weights on a Kubernetes volume.
func onKubernetes(d *deploySetup, resource string, settings map[string]string) {
	component := planning.ResourceConfig{Kind: recipes.ModelVLLMKind, Component: &planning.ComponentConfig{Kind: recipes.ModelVLLMKind, Resource: resource, Settings: settings,
		Component: recipes.ResolvedComponent{Component: recipes.Component{Name: recipes.ModelEngine, WeightCache: &recipes.WeightCache{MountPath: "/cache", KubernetesSizeMB: 1024, SizeSetting: "cache_mb"}}}}}
	engine, _ := json.Marshal(component)
	d.facts.mu.Lock()
	defer d.facts.mu.Unlock()
	if d.facts.bindings == nil {
		d.facts.bindings = map[string][]*planning.Binding{}
	}
	d.facts.bindings[stagingID] = append(d.facts.bindings[stagingID], &planning.Binding{Subject: planning.ResourceSubject,
		Name: planning.ComponentActionName(resource, recipes.ModelEngine), Provider: connections.Kubernetes, ConnectionID: kubeConn, Config: engine})
}

// A move on the same cluster that needs a bigger weight cache is refused
// before the manifest changes: the volume can't grow in place. Its
// cache_mb of 1024 leaves it at the recipe's 20480 MB, and bf16 needs
// 25600 MB here.
func TestMoveRefusesToGrowAKubernetesCache(t *testing.T) {
	ctx := context.Background()
	kube := l40sOn(kubeConn, "kube", 1.57)
	kube.Cloud = connections.Kubernetes
	d := newDeploy(t, deployRegistryWith(t, llamaRev, "", bigBF16), aiAdmin, []gpus.GPUTarget{kube}, cpuCluster())
	settings := llmSettings(t)
	settings["cache_mb"] = "1024"
	putResource(t, d, "llm", manifest.Resource{Kind: recipes.ModelVLLMKind, Provider: "kubernetes", Target: kubeConn, Version: "0.31",
		GPU: &manifest.GPU{Kind: "nvidia-l40s", Count: 1}, Settings: settings, Environments: map[string]manifest.ResourceSettings{"staging": on()}})
	onKubernetes(d, "llm", settings)
	revisions := d.revisions.count()
	in := MoveInput{Target: TargetChoice{Source: TargetConnection, ConnectionID: kubeConn, GPU: &provider.GPURequest{Kind: "nvidia-l40s"}}, Quantization: "bf16", MaxModelLen: 16384}
	_, err := d.svc.Move(ctx, member, projectID, stagingID, "llm", in)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "llm keeps its weight cache on a Kubernetes volume, which can't grow in place") {
		t.Errorf("move: %v", err)
	}
	if d.revisions.count() != revisions || len(d.plans.asked) != 0 {
		t.Error("the refused move wrote something")
	}
}

// The move's preview shows the same refusal, so it never offers a move
// Move refuses. With weights that need no more than the recipe's 20480
// MB, a cache_mb of 1024 or none leaves the volume at that size, as the
// planner sizes it, so that move goes ahead.
func TestMovePreviewShowsTheKubernetesCacheRefusal(t *testing.T) {
	ctx := context.Background()
	kube := l40sOn(kubeConn, "kube", 1.57)
	kube.Cloud = connections.Kubernetes
	settings := llmSettings(t)
	settings["cache_mb"] = "1024"
	setup := func(reg *registry.Registry) *deploySetup {
		d := newDeploy(t, reg, aiAdmin, []gpus.GPUTarget{kube}, cpuCluster())
		putResource(t, d, "llm", manifest.Resource{Kind: recipes.ModelVLLMKind, Provider: "kubernetes", Target: kubeConn, Version: "0.31",
			GPU: &manifest.GPU{Kind: "nvidia-l40s", Count: 1}, Settings: settings, Environments: map[string]manifest.ResourceSettings{"staging": on()}})
		onKubernetes(d, "llm", settings)
		return d
	}
	d := setup(deployRegistryWith(t, llamaRev, "", bigBF16))
	in := MoveInput{Target: TargetChoice{Source: TargetConnection, ConnectionID: kubeConn, GPU: &provider.GPURequest{Kind: "nvidia-l40s"}}, Quantization: "bf16", MaxModelLen: 16384}
	pv, err := d.svc.MovePreview(ctx, member, projectID, stagingID, "llm", in)
	want := Problem{Code: ProblemTarget, Message: "llm keeps its weight cache on a Kubernetes volume, which can't grow in place, and bf16 needs a bigger one. Remove llm from staging and deploy it again to move it."}
	if err != nil || pv.CanDeploy || len(pv.Problems) != 1 || pv.Problems[0] != want {
		t.Fatalf("preview: %+v, %v", pv, err)
	}
	_, err = d.svc.Move(ctx, member, projectID, stagingID, "llm", in)
	if !errors.Is(err, ErrInvalid) || err.Error() != want.Message {
		t.Errorf("move: %v", err)
	}

	for _, cache := range []string{"1024", ""} {
		if cache == "" {
			delete(settings, "cache_mb")
		}
		d = setup(deployRegistry(t, llamaRev, ""))
		if pv, err = d.svc.MovePreview(ctx, member, projectID, stagingID, "llm", in); err != nil || !pv.CanDeploy || len(pv.Problems) != 0 {
			t.Fatalf("cache_mb %q: %+v, %v", cache, pv, err)
		}
		if _, err = d.svc.Move(ctx, member, projectID, stagingID, "llm", in); err != nil {
			t.Errorf("move with cache_mb %q: %v", cache, err)
		}
	}
}

// A move of staging's copy to weights with no file, while the top level
// names a file production reads, is refused in the preview and by Move:
// staging's block can't say "no file".
func TestMoveRefusesToLeaveATopLevelFile(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, []gpus.GPUTarget{l40sServer(), l40sOn(gpuConn2, "gpu-box-2", 1.57)})
	settings := llmSettings(t)
	settings["file"] = "old.gguf"
	putLLM(t, d, map[string]manifest.ResourceSettings{"staging": on(), "production": on()})
	r := current(t, d).Resources["llm"]
	r.Settings = settings
	putResource(t, d, "llm", r)
	revisions := d.revisions.count()
	in := MoveInput{Target: TargetChoice{Source: TargetConnection, ConnectionID: gpuConn2, GPU: &provider.GPURequest{Kind: "nvidia-l40s"}}, Quantization: "bf16", MaxModelLen: 8192}
	want := Problem{Code: ProblemQuantization, Message: "bf16 has no file, and staging would still read file old.gguf from the top level of the model's manifest entry, which its other environments run. Deploy it to staging as a separately named model, or remove it from the project's other environments first."}
	pv, err := d.svc.MovePreview(ctx, member, projectID, stagingID, "llm", in)
	if err != nil || pv.CanDeploy || len(pv.Problems) != 1 || pv.Problems[0] != want {
		t.Fatalf("preview: %+v, %v", pv.Problems, err)
	}
	if _, err = d.svc.Move(ctx, member, projectID, stagingID, "llm", in); !errors.Is(err, ErrInvalid) || err.Error() != want.Message {
		t.Errorf("move: %v", err)
	}
	if d.revisions.count() != revisions {
		t.Error("the refused move wrote something")
	}
}

func TestCacheGrows(t *testing.T) {
	for _, c := range []struct {
		settings map[string]string
		need     int64
		want     bool
	}{
		{map[string]string{}, 20480, false},                    // the recipe's 20480 MB
		{map[string]string{}, 30720, true},                     // more than it
		{map[string]string{"cache_mb": "1024"}, 20480, false},  // below the recipe's: the planner's 20480
		{map[string]string{"cache_mb": "1024"}, 30720, true},   // and more than that
		{map[string]string{"cache_mb": "25600"}, 30720, true},  // a bigger one set by hand, still too small
		{map[string]string{"cache_mb": "40960"}, 30720, false}, // big enough
		{map[string]string{"cache_mb": "big"}, 20480, false},   // not a size: the recipe's
	} {
		if got := cacheGrows(recipes.ModelVLLMKind, c.settings, c.need); got != c.want {
			t.Errorf("cacheGrows(%v, %d) = %v", c.settings, c.need, got)
		}
	}
	if got := defaultCacheMB(recipes.ModelVLLMKind); got != engines.MinCacheMB {
		t.Errorf("the vLLM recipe's cache is %d MB", got)
	}
}
