package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/catalogstate"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
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
	const connection = gpuConn
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
	row.Month, row.MonthCostUSD = "2026-11", "3.2000"
	if err := d.rows.Update(ctx, row); err != nil {
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
	saveServices(t, d, map[string]manifest.Service{
		"checkout": linkedService("go:cmd/checkout", llamaResource),
		"api":      linkedService("go:cmd/api", llamaResource),
		"web":      linkedService("go:cmd/web"),
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
