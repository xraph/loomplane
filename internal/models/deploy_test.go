package models

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"go.jetify.com/typeid/v2"
	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/hfhub"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// Entries of the deploy fixture. Llama 3.1 8B's bf16 weights are the
// memory math's worked example, 16 GiB.
const (
	llama8BID  = "meta-llama/Llama-3.1-8B-Instruct"
	llama70BID = "meta-llama/Llama-3.1-70B-Instruct"
	llamaRev   = "5206a32e0bd3067aef1ce90f5528ade7d866253f"
	awqRepo    = "hugging-quants/Meta-Llama-3.1-70B-Instruct-AWQ-INT4"
	awqRev     = "2123003760781134cfc31124aa6560a45b491fdf"
)

func llamaArch() registry.Architecture {
	return registry.Architecture{Name: "LlamaForCausalLM", Layers: 32, AttentionHeads: 32, KVHeads: 8, HeadDim: 128}
}

// deployEntries are gated Llama 3.1 8B and 70B, made for these tests,
// with their min memory filled in as the registry checks it.
func deployEntries(revision string) []registry.Entry {
	e8 := registry.Entry{
		ID: llama8BID, Title: "Llama 3.1 8B Instruct", Publisher: "Meta", Summary: "Meta's 8B chat model.",
		Revision: revision, License: registry.License{ID: "llama3.1", Commercial: true}, Gated: true,
		Tasks: []registry.Task{registry.TaskChat}, Parameters: 8_030_261_248, ContextLength: 131072, Architecture: llamaArch(),
		Quantizations: []registry.Quantization{
			{ID: "bf16", Engine: registry.EngineVLLM, WeightBytes: 16 << 30},
			{ID: "fp8", Engine: registry.EngineVLLM, WeightBytes: 8 << 30},
		},
	}
	arch70 := registry.Architecture{Name: "LlamaForCausalLM", Layers: 80, AttentionHeads: 64, KVHeads: 8, HeadDim: 128}
	e70 := registry.Entry{
		ID: llama70BID, Title: "Llama 3.1 70B Instruct", Publisher: "Meta", Summary: "Meta's 70B chat model.",
		Revision: revision, License: registry.License{ID: "llama3.1", Commercial: true}, Gated: true,
		Tasks: []registry.Task{registry.TaskChat}, Parameters: 70_553_706_496, ContextLength: 131072, Architecture: arch70,
		Quantizations: []registry.Quantization{
			{ID: "bf16", Engine: registry.EngineVLLM, WeightBytes: 141_107_497_606},
			{ID: "fp8", Engine: registry.EngineVLLM, WeightBytes: 72_691_908_608}, // 67.7 GiB
			{ID: "awq-int4", Engine: registry.EngineVLLM, Repo: awqRepo, Revision: awqRev, WeightBytes: 39_770_000_000},
		},
	}
	out := []registry.Entry{e8, e70}
	for i, e := range out {
		for j, q := range e.Quantizations {
			q.MinGPUMemoryGiB = registry.MinGPUMemoryGiB(e, q)
			out[i].Quantizations[j] = q
		}
	}
	return out
}

// deployRegistry is the deploy fixture pinned to revision, with Qwen 7B
// and bge-small from the real registry, and the qualification results
// results (YAML list items, "" for none).
func deployRegistry(t *testing.T, revision, results string) *registry.Registry {
	t.Helper()
	fsys := fstest.MapFS{}
	put := func(family string, entries []registry.Entry) {
		raw, err := yaml.Marshal(registry.File{Family: family, Entries: entries})
		if err != nil {
			t.Fatal(err)
		}
		fsys[family+".yaml"] = &fstest.MapFile{Data: raw}
	}
	put("llama", deployEntries(revision))
	for _, id := range []string{qwen7B, bgeSmall} {
		e, ok := registry.Default().Entry(id)
		if !ok {
			t.Fatalf("the registry has no %s", id)
		}
		put(e.Family, []registry.Entry{e})
	}
	if results != "" {
		fsys["qualification.yaml"] = &fstest.MapFile{Data: []byte("results:\n" + results)}
	}
	r, err := registry.Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Permissions of the deploy tests' members.
var (
	deployer = perms{security.AIRead, security.AIEdit, security.InfrastructureRead, security.CloudAccountRead,
		security.ProjectRead, security.ProjectUpdate, security.DeploymentCreate}
	aiAdmin = append(slices.Clone(deployer), security.AIManage)
)

const (
	projectID  = "prj_shop"
	stagingID  = "env_staging"
	productID  = "env_production"
	gpuConn    = "conn_gpu"
	cpuConn    = "conn_cpu"
	kubeConn   = "conn_kube"
	hfTokenVal = "hf_deployTESTtokenVALUE0001"
)

// memProjects is one project, shop, with staging and production.
type memProjects struct{}

func (memProjects) GetProject(_ context.Context, orgID, id string) (*projects.Project, error) {
	if orgID != "org" || id != projectID {
		return nil, projects.ErrNotFound
	}
	return &projects.Project{ID: projectID, OrgID: orgID, Name: "Shop", Slug: "shop"}, nil
}

func (memProjects) environments() []*projects.Environment {
	return []*projects.Environment{
		{ID: stagingID, OrgID: "org", ProjectID: projectID, Name: "staging", Kind: projects.KindStaging},
		{ID: productID, OrgID: "org", ProjectID: projectID, Name: "production", Kind: projects.KindProduction},
	}
}

func (m memProjects) GetEnvironment(_ context.Context, orgID, project, id string) (*projects.Environment, error) {
	for _, e := range m.environments() {
		if orgID == "org" && project == projectID && e.ID == id {
			return e, nil
		}
	}
	return nil, projects.ErrEnvironmentNotFound
}

func (m memProjects) ListEnvironments(_ context.Context, orgID, project string) ([]*projects.Environment, error) {
	if orgID != "org" || project != projectID {
		return nil, projects.ErrNotFound
	}
	return m.environments(), nil
}

// memRevisions is a manifests.Store in memory.
type memRevisions struct {
	mu   sync.Mutex
	revs []*manifests.Revision
}

func (m *memRevisions) CreateRevision(_ context.Context, r *manifests.Revision) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.Revision = len(m.revs) + 1
	m.revs = append(m.revs, r)
	return nil
}

func (m *memRevisions) CurrentRevision(_ context.Context, _, _ string) (*manifests.Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.revs) == 0 {
		return nil, manifests.ErrNotFound
	}
	return m.revs[len(m.revs)-1], nil
}

func (m *memRevisions) GetRevision(context.Context, string, string) (*manifests.Revision, error) {
	return nil, manifests.ErrNotFound
}

func (m *memRevisions) ListRevisions(context.Context, string, string, int) ([]*manifests.Revision, error) {
	return nil, nil
}

func (m *memRevisions) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.revs)
}

// fakePlans records each plan asked for.
type fakePlans struct {
	mu    sync.Mutex
	asked []planning.CreatePlanInput
	err   error
}

func (f *fakePlans) Create(_ context.Context, _ security.Principal, _ string, in planning.CreatePlanInput) (*planning.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.asked = append(f.asked, in)
	return &planning.Plan{ID: fmt.Sprintf("plan_%d", len(f.asked)), Environment: in.Environment}, nil
}

// memDeployments is a Store in memory. With now set, it stamps rows as
// Postgres does.
type memDeployments struct {
	mu   sync.Mutex
	rows map[string]*Deployment
	now  func() time.Time
}

func deploymentKey(org, project, env, resource string) string {
	return org + "/" + project + "/" + env + "/" + resource
}

func (m *memDeployments) Create(_ context.Context, d *Deployment) error {
	if err := d.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[string]*Deployment{}
	}
	k := deploymentKey(d.OrgID, d.ProjectID, d.EnvironmentID, d.Resource)
	if _, ok := m.rows[k]; ok {
		return ErrConflict
	}
	if d.ID == "" {
		id, err := typeid.Generate("aimodel")
		if err != nil {
			return err
		}
		d.ID = id.String()
	}
	d.Revision = 1
	if m.now != nil {
		d.CreatedAt, d.UpdatedAt = m.now(), m.now()
	}
	row := *d
	m.rows[k] = &row
	return nil
}

func (m *memDeployments) Get(_ context.Context, org, project, env, resource string) (*Deployment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.rows[deploymentKey(org, project, env, resource)]
	if !ok {
		return nil, ErrNotFound
	}
	row := *d
	return &row, nil
}

func (m *memDeployments) list(keep func(*Deployment) bool) []*Deployment {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Deployment
	for _, k := range slices.Sorted(maps.Keys(m.rows)) {
		if d := m.rows[k]; keep(d) {
			row := *d
			out = append(out, &row)
		}
	}
	return out
}

func (m *memDeployments) List(_ context.Context, org string) ([]*Deployment, error) {
	return m.list(func(d *Deployment) bool { return d.OrgID == org }), nil
}

func (m *memDeployments) ListProject(_ context.Context, org, project string) ([]*Deployment, error) {
	return m.list(func(d *Deployment) bool { return d.OrgID == org && d.ProjectID == project }), nil
}

func (m *memDeployments) Update(_ context.Context, d *Deployment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := deploymentKey(d.OrgID, d.ProjectID, d.EnvironmentID, d.Resource)
	old, ok := m.rows[k]
	if !ok {
		return ErrNotFound
	}
	if old.Revision != d.Revision {
		return ErrStale
	}
	d.Revision++
	if m.now != nil {
		d.UpdatedAt = m.now()
	}
	row := *d
	m.rows[k] = &row
	return nil
}

func (m *memDeployments) Delete(_ context.Context, org, project, env, resource string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := deploymentKey(org, project, env, resource)
	if _, ok := m.rows[k]; !ok {
		return ErrNotFound
	}
	delete(m.rows, k)
	return nil
}

func (m *memDeployments) Accrue(context.Context, *Deployment, string, string, time.Time) error {
	return errors.New("not in these tests")
}

// fakeGuard is the workspace's token lock: it counts its holders and
// passes the token's revision.
type fakeGuard struct {
	tokens *fakeTokens
	held   int
}

func (g *fakeGuard) WithHuggingFaceToken(ctx context.Context, orgID string, fn func(context.Context, int64) error) error {
	g.held++
	revision, err := g.tokens.HuggingFaceTokenRevision(ctx, orgID)
	if err != nil {
		return err
	}
	return fn(ctx, revision)
}

// memAudit keeps the events recorded.
type memAudit struct {
	mu     sync.Mutex
	events []projects.AuditEvent
}

func (a *memAudit) Record(_ context.Context, e projects.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

type deploySetup struct {
	*browseSetup
	revisions *memRevisions
	plans     *fakePlans
	rows      *memDeployments
	guard     *fakeGuard
	audit     *memAudit
	facts     *fakeDeployed
	gateways  *countGateways
}

// l40sServer is gpu-box, a server with one free NVIDIA L40S at $1.57 an hour.
func l40sServer() gpus.GPUTarget {
	return gpus.GPUTarget{
		Source: gpus.SourceConnection, ConnectionID: gpuConn, Name: "gpu-box", Cloud: "server",
		Accelerator: provider.Accelerator{Kind: "nvidia-l40s", Vendor: provider.VendorNVIDIA, Count: 1, MemoryGiB: 48},
		Free:        1, Fits: true, HourlyUSD: 1.57, HourlyPriceKnown: true,
	}
}

func l4Box() gpus.GPUTarget {
	t := l4Server(true)
	t.ConnectionID = gpuConn
	return t
}

func cpuCluster() *connections.Connection {
	return &connections.Connection{ID: kubeConn, Name: "kube", Cloud: connections.Kubernetes, Status: connections.StatusReady}
}

// startYAML is the manifest the project starts with: one service.
const startYAML = "version: 1\nservices:\n  api:\n    source: go:cmd/api\n    provider: cloud-run\n"

// newDeploy is a Service over r that can deploy, as authz, with the GPU
// targets and connections given and the manifest startYAML.
func newDeploy(t *testing.T, r *registry.Registry, authz Authorizer, targets []gpus.GPUTarget, conns ...*connections.Connection) *deploySetup {
	t.Helper()
	b := newBrowse(t, r, authz, targets, conns...)
	d := &deploySetup{browseSetup: b, revisions: &memRevisions{}, plans: &fakePlans{}, rows: &memDeployments{}, audit: &memAudit{},
		facts: &fakeDeployed{}, gateways: &countGateways{}}
	d.guard = &fakeGuard{tokens: b.tokens}
	mf := manifests.NewService(d.revisions, memProjects{}, authz, nil)
	start := manifests.NewService(d.revisions, memProjects{}, aiAdmin, nil)
	if _, _, err := start.Save(context.Background(), member, projectID, manifests.SaveInput{YAML: startYAML}); err != nil {
		t.Fatal(err)
	}
	d.svc = NewService(Deps{
		Registry: r, Hub: hfhub.New(b.hub.srv.URL, b.hub.srv.Client()), Settings: b.tokens, Custom: b.custom,
		GPUs: b.targets, Connections: fakeConnections{authz: authz, list: conns}, Authz: authz,
		Now:   func() time.Time { return *b.now },
		Store: d.rows, Manifests: mf, Plans: d.plans, Projects: memProjects{}, TokenGuard: d.guard, Audit: d.audit,
		Deployed: d.facts, Gateways: d.gateways,
	})
	return d
}

// grantLlama puts a workspace token whose account has accepted the Llama
// license, and the Llama repos at llamaRev on the fake Hub.
func (d *deploySetup) grantLlama() {
	for _, id := range []string{llama8BID, llama70BID} {
		d.hub.add(&hubRepo{id: id, sha: llamaRev, gated: true, config: "{}"})
	}
	d.hub.add(&hubRepo{id: awqRepo, sha: awqRev, gated: true, config: "{}"})
	d.tokens.token, d.tokens.revision = hfTokenVal, 1
	d.hub.grant(hfTokenVal)
}

func onGPU(kind string) TargetChoice {
	return TargetChoice{Source: TargetConnection, ConnectionID: gpuConn, GPU: &provider.GPURequest{Kind: kind}}
}

func codes(pv *DeployPreview) []string {
	out := make([]string, 0, len(pv.Problems))
	for _, p := range pv.Problems {
		out = append(out, p.Code)
	}
	return out
}

func current(t *testing.T, d *deploySetup) *manifest.Manifest {
	t.Helper()
	rev, err := d.revisions.CurrentRevision(context.Background(), "org", projectID)
	if err != nil {
		t.Fatal(err)
	}
	return rev.Manifest
}

func TestPreviewPicksTheBestQuantizationThatFits(t *testing.T) {
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()})
	d.grantLlama()
	pv, err := d.svc.Preview(context.Background(), member, projectID, stagingID, DeployInput{EntryID: llama70BID, Target: onGPU("nvidia-l40s")})
	if err != nil {
		t.Fatal(err)
	}
	// bf16 needs four, fp8's 67.7 GiB is more than one L40S, AWQ fits.
	if len(pv.Problems) != 0 || pv.Quantization != "awq-int4" || pv.Kind != recipes.ModelVLLMKind || pv.Version != "0.31" {
		t.Fatalf("problems %v quantization %q kind %q version %q", pv.Problems, pv.Quantization, pv.Kind, pv.Version)
	}
	if pv.GPU == nil || *pv.GPU != (provider.GPURequest{Kind: "nvidia-l40s", Count: 1}) || !pv.Fit.Fits() {
		t.Errorf("gpu %+v fit %+v", pv.GPU, pv.Fit)
	}
	if pv.Target.Provider != "server" || pv.TargetName != "gpu-box" || pv.Resource != "llama-3-1-70b-instruct" {
		t.Errorf("provider %q name %q resource %q", pv.Target.Provider, pv.TargetName, pv.Resource)
	}
	if !pv.Gated || !pv.TokenReady || !pv.CanDeploy {
		t.Errorf("gated %v token ready %v can deploy %v", pv.Gated, pv.TokenReady, pv.CanDeploy)
	}
	// The whole L40S, at its server's price.
	if pv.Cost.Source != CostSourceServer || pv.Cost.HourlyUSD != "1.5700" || pv.Cost.Share != "1 of 1 GPUs" {
		t.Errorf("cost %+v", pv.Cost)
	}
	// The license check asked about the weights' own repo.
	if !slices.Contains(d.hub.requests(), "HEAD /"+awqRepo+"/resolve/"+awqRev+"/config.json token "+hfTokenVal) {
		t.Errorf("requests %v", d.hub.requests())
	}
	// Preview writes nothing.
	if d.revisions.count() != 1 || len(d.rows.rows) != 0 || len(d.plans.asked) != 0 {
		t.Error("Preview wrote something")
	}
	// Asking for a quantization that doesn't fit says why.
	pv, err = d.svc.Preview(context.Background(), member, projectID, stagingID, DeployInput{EntryID: llama70BID, Quantization: "fp8", Target: onGPU("nvidia-l40s")})
	if err != nil || !slices.Equal(codes(pv), []string{ProblemNoFit}) || !strings.Contains(pv.Problems[0].Message, "NVIDIA L40S") || pv.CanDeploy {
		t.Errorf("fp8: %+v, %v", pv.Problems, err)
	}
}

func TestPreviewNeverPicksAFailedConfiguration(t *testing.T) {
	failed := fmt.Sprintf(`  - entry: %s
    quantization: bf16
    revision: %s
    gpu_kind: nvidia-l40s
    gpus: 1
    max_model_len: 32768
    engine: vllm
    engine_version: %s
    outcome: failed
    detail: CUDA error out of memory while loading
    ran_at: 2026-11-03T06:12:00Z
    run: https://github.com/xraph/loomplane/actions/runs/7
`, llama8BID, llamaRev, engines.Current()[registry.EngineVLLM])
	d := newDeploy(t, deployRegistry(t, llamaRev, failed), deployer, []gpus.GPUTarget{l40sServer()})
	d.grantLlama()
	pv, err := d.svc.Preview(context.Background(), member, projectID, stagingID, DeployInput{EntryID: llama8BID, Target: onGPU("nvidia-l40s")})
	if err != nil || len(pv.Problems) != 0 || pv.Quantization != "fp8" {
		t.Fatalf("quantization %q problems %v, %v", pv.Quantization, pv.Problems, err)
	}
	pv, err = d.svc.Preview(context.Background(), member, projectID, stagingID, DeployInput{EntryID: llama8BID, Quantization: "bf16", Target: onGPU("nvidia-l40s")})
	if err != nil || !slices.Equal(codes(pv), []string{ProblemNoFit}) || pv.Problems[0].Message != "CUDA error out of memory while loading" {
		t.Errorf("bf16 asked for: %+v, %v", pv.Problems, err)
	}
}

func TestPreviewDefaultsContextToWhatFits(t *testing.T) {
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l4Box()})
	d.grantLlama()
	in := DeployInput{EntryID: llama8BID, Quantization: "bf16", Target: onGPU("nvidia-l4")}
	pv, err := d.svc.Preview(context.Background(), member, projectID, stagingID, in)
	if err != nil || len(pv.Problems) != 0 || pv.MaxModelLen != 22528 {
		t.Fatalf("default context %d, problems %v, %v", pv.MaxModelLen, pv.Problems, err)
	}
	in.MaxModelLen = 65536
	pv, err = d.svc.Preview(context.Background(), member, projectID, stagingID, in)
	if err != nil || !slices.Equal(codes(pv), []string{ProblemContext}) || pv.Problems[0].Message != "At most 22528 tokens fit on NVIDIA L4." {
		t.Errorf("65536 tokens: %+v, %v", pv.Problems, err)
	}
	in.MaxModelLen = 8192
	if pv, err = d.svc.Preview(context.Background(), member, projectID, stagingID, in); err != nil || len(pv.Problems) != 0 || pv.MaxModelLen != 8192 {
		t.Errorf("8192 tokens: %d, %+v, %v", pv.MaxModelLen, pv.Problems, err)
	}
}

// Under M1 a model runs in one environment: the preview lists only that
// one, and only its services link to it.
func TestPinnedModelReachesOnlyItsEnvironment(t *testing.T) {
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()})
	d.grantLlama()
	pv, err := d.svc.Preview(context.Background(), member, projectID, stagingID, DeployInput{EntryID: llama8BID, Target: onGPU("nvidia-l40s")})
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Environments) != 1 {
		t.Fatalf("environments %+v", pv.Environments)
	}
	r := pv.Environments[0]
	if r.EnvironmentID != stagingID || r.Environment != "staging" || r.Mode != ReachHome || !strings.Contains(r.Message, "Services there link to it") {
		t.Errorf("reach %+v", r)
	}
}

func TestDeployGatedNeedsAcceptedLicense(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()})
	in := DeployInput{EntryID: llama8BID, Target: onGPU("nvidia-l40s")}

	// No token.
	_, err := d.svc.Deploy(ctx, member, projectID, stagingID, in)
	var refused *ProblemError
	if !errors.As(err, &refused) || !errors.Is(err, ErrInvalid) || refused.Problem.Code != ProblemGated ||
		err.Error() != "Llama 3.1 8B Instruct is gated on Hugging Face. Add a Hugging Face token that has accepted its license in AI settings." {
		t.Errorf("no token: %v", err)
	}

	// A token whose account hasn't accepted Meta's license.
	d.hub.add(&hubRepo{id: llama8BID, sha: llamaRev, gated: true, config: "{}"})
	d.tokens.token, d.tokens.revision = hfTokenVal, 1
	_, err = d.svc.Deploy(ctx, member, projectID, stagingID, in)
	want := "Your Hugging Face account hasn't accepted the license for " + llama8BID + ". Accept it at https://huggingface.co/" + llama8BID + ", then try again."
	if !errors.As(err, &refused) || err.Error() != want {
		t.Errorf("license not accepted: %v", err)
	}
	if strings.Contains(err.Error(), hfTokenVal) {
		t.Error("the refusal holds the token")
	}
	if d.revisions.count() != 1 || len(d.rows.rows) != 0 || len(d.plans.asked) != 0 {
		t.Error("a refused deploy wrote something")
	}

	// The token is deleted between the preview and the row: the row is
	// written under the token lock, which sees it gone.
	d.hub.grant(hfTokenVal)
	d.guard.tokens = &fakeTokens{}
	_, err = d.svc.Deploy(ctx, member, projectID, stagingID, in)
	if !errors.As(err, &refused) || refused.Problem.Code != ProblemGated || len(d.rows.rows) != 0 || d.revisions.count() != 1 {
		t.Errorf("token gone under the lock: %v", err)
	}
}

func TestDeployEditsTheManifestAndPlans(t *testing.T) {
	ctx := context.Background()
	r := deployRegistry(t, llamaRev, "")
	d := newDeploy(t, r, deployer, []gpus.GPUTarget{l40sServer()})
	d.grantLlama()

	got, err := d.svc.Deploy(ctx, member, projectID, stagingID, DeployInput{EntryID: llama8BID, Target: onGPU("nvidia-l40s"), MonthlyBudgetUSD: "250"})
	if err != nil {
		t.Fatal(err)
	}
	// One revision, with the resource on in staging and nothing shared.
	if d.revisions.count() != 2 {
		t.Fatalf("%d revisions", d.revisions.count())
	}
	m := current(t, d)
	res, ok := m.Resources["llama-3-1-8b-instruct"]
	if !ok {
		t.Fatalf("resources %v", slices.Collect(maps.Keys(m.Resources)))
	}
	e, _ := r.Entry(llama8BID)
	q, _ := e.Quantization("bf16")
	want := engines.Settings(engines.Spec{Entry: e, Quantization: q, ServedName: llama8BID, MaxModelLen: 32768, GPUs: 1})
	if res.Kind != recipes.ModelVLLMKind || res.Version != "0.31" || res.Provider != "server" || res.Target != gpuConn ||
		res.GPU == nil || *res.GPU != (manifest.GPU{Kind: "nvidia-l40s", Count: 1}) || !maps.Equal(res.Settings, want) {
		t.Errorf("resource %+v, want settings %v", res, want)
	}
	if !enabledIn(res, "staging") || len(res.Environments) != 1 {
		t.Errorf("environments %+v", res.Environments)
	}
	rev, _ := d.revisions.CurrentRevision(ctx, "org", projectID)
	if strings.Contains(rev.YAML, "share") || !strings.Contains(rev.YAML, "enabled: true") {
		t.Errorf("YAML:\n%s", rev.YAML)
	}
	// One plan, for staging, of the whole project.
	if len(d.plans.asked) != 1 || d.plans.asked[0].Environment != "staging" || d.plans.asked[0].Catalog || len(d.plans.asked[0].Scope) != 0 {
		t.Errorf("plans %+v", d.plans.asked)
	}
	// The row: planned, with the plan, written under the token lock.
	row, err := d.rows.Get(ctx, "org", projectID, stagingID, "llama-3-1-8b-instruct")
	if err != nil {
		t.Fatal(err)
	}
	if row.Phase != PhasePlanned || row.PlanID != "plan_1" || got.PlanID != "plan_1" || row.EntryID != llama8BID ||
		row.Quantization != "bf16" || row.Engine != "vllm" || row.MonthlyBudgetUSD != "250" || row.ServerName != "gpu-box" ||
		row.RequestedBy != member || d.guard.held != 1 {
		t.Errorf("row %+v, lock held %d times", row, d.guard.held)
	}
	if len(d.audit.events) != 1 || d.audit.events[0].Action != "ai.model.deploy" || d.audit.events[0].Attributes["target"] != gpuConn {
		t.Errorf("audit %+v", d.audit.events)
	}
	for _, ev := range d.audit.events {
		if strings.Contains(fmt.Sprint(ev.Attributes), hfTokenVal) {
			t.Error("the audit holds the token")
		}
	}

	// A plan that fails after the edit keeps the row and says what to do.
	d.plans.err = errors.New("planning is down")
	got, err = d.svc.Deploy(ctx, member, projectID, stagingID, DeployInput{EntryID: qwen7B, Target: onGPU("nvidia-l40s")})
	var after *PlanAfterEditError
	if !errors.As(err, &after) || !strings.HasPrefix(err.Error(), "The manifest has qwen2-5-7b-instruct now. Plan staging from the project to deploy it.") {
		t.Fatalf("plan failure: %v", err)
	}
	if row, gerr := d.rows.Get(ctx, "org", projectID, stagingID, "qwen2-5-7b-instruct"); gerr != nil || row.PlanID != "" || !strings.Contains(row.Message, "Plan staging") || got.Deployment == nil {
		t.Errorf("row after a failed plan: %+v, %v", row, gerr)
	}
}

func TestDeployToProductionNeedsAIManage(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()})
	d.grantLlama()
	in := DeployInput{EntryID: llama8BID, Target: onGPU("nvidia-l40s")}
	before := len(d.hub.requests())
	if _, err := d.svc.Deploy(ctx, member, projectID, productID, in); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("developer to production: %v", err)
	}
	if d.revisions.count() != 1 || len(d.rows.rows) != 0 || len(d.hub.requests()) != before {
		t.Error("a forbidden deploy did something")
	}
	// The preview says so too.
	if pv, err := d.svc.Preview(ctx, member, projectID, productID, in); err != nil || len(pv.Problems) != 0 || pv.CanDeploy {
		t.Errorf("preview: can deploy %v, %+v, %v", pv.CanDeploy, pv.Problems, err)
	}

	for env, want := range map[string]security.Permission{productID: security.AIManage, stagingID: security.AIEdit} {
		if got, err := d.svc.WriteNeed(ctx, "org", projectID, env); err != nil || got != want {
			t.Errorf("WriteNeed(%s) = %v, %v", env, got, err)
		}
	}
	if _, err := d.svc.WriteNeed(ctx, "org", projectID, "env_missing"); !errors.Is(err, projects.ErrEnvironmentNotFound) {
		t.Errorf("WriteNeed of a missing environment: %v", err)
	}

	admin := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, []gpus.GPUTarget{l40sServer()})
	admin.grantLlama()
	if _, err := admin.svc.Deploy(ctx, member, projectID, productID, in); err != nil {
		t.Errorf("admin to production: %v", err)
	}
}

// Ruling M6: a model runs in one environment, so the same entry deployed
// in another one gets its own resource name, and a name the manifest has
// is refused.
func TestDeployRefusesATakenName(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, []gpus.GPUTarget{l40sServer()})
	d.grantLlama()
	in := DeployInput{EntryID: llama8BID, Target: onGPU("nvidia-l40s")}
	if _, err := d.svc.Deploy(ctx, member, projectID, productID, in); err != nil {
		t.Fatal(err)
	}

	// The same entry in staging gets the environment in its name.
	pv, err := d.svc.Preview(ctx, member, projectID, stagingID, in)
	if err != nil || len(pv.Problems) != 0 || pv.Resource != "llama-3-1-8b-instruct-staging" {
		t.Fatalf("second environment: %q %+v, %v", pv.Resource, pv.Problems, err)
	}
	// A name the manifest has, for a resource or a service, is taken.
	for _, name := range []string{"llama-3-1-8b-instruct", "api"} {
		named := in
		named.Resource = name
		_, err = d.svc.Deploy(ctx, member, projectID, stagingID, named)
		var refused *ProblemError
		if !errors.As(err, &refused) || refused.Problem.Code != ProblemNameTaken ||
			err.Error() != name+" is already in this project's manifest. Pick another name." {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Not a resource name at all.
	bad := in
	bad.Resource = "Llama 8B"
	if pv, err = d.svc.Preview(ctx, member, projectID, stagingID, bad); err != nil || !slices.Equal(codes(pv), []string{ProblemName}) {
		t.Errorf("bad name: %+v, %v", pv.Problems, err)
	}
	// The same entry again in production is the model it already has.
	if pv, err = d.svc.Preview(ctx, member, projectID, productID, in); err != nil || !slices.Contains(codes(pv), ProblemExists) {
		t.Errorf("again in production: %+v, %v", pv.Problems, err)
	}
	if d.revisions.count() != 2 {
		t.Errorf("%d revisions after refusals", d.revisions.count())
	}
}

func TestCPUEnginesNeedNoGPU(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, nil, cpuServer())
	in := DeployInput{EntryID: bgeSmall, Target: TargetChoice{Source: TargetConnection, ConnectionID: cpuConn}}
	pv, err := d.svc.Preview(ctx, member, projectID, stagingID, in)
	if err != nil || len(pv.Problems) != 0 {
		t.Fatalf("%+v, %v", pv.Problems, err)
	}
	if pv.Kind != recipes.ModelTEIKind || pv.GPU != nil || pv.TargetName != "cpu-box" || pv.Target.Provider != "server" {
		t.Errorf("kind %q gpu %+v target %q", pv.Kind, pv.GPU, pv.TargetName)
	}
	// Its cost is part of the server's, which the page says in words.
	if pv.Cost.Source != CostSourceCPU || !pv.Cost.Known {
		t.Errorf("cost %+v", pv.Cost)
	}
	if _, err = d.svc.Deploy(ctx, member, projectID, stagingID, in); err != nil {
		t.Fatal(err)
	}
	res := current(t, d).Resources["bge-small-en-v1-5"]
	if res.Kind != recipes.ModelTEIKind || res.GPU != nil || res.Target != cpuConn {
		t.Errorf("resource %+v", res)
	}
	// It's not gated, so the token lock was never taken.
	if d.guard.held != 0 || d.tokens.reads != 0 {
		t.Errorf("lock held %d, token read %d times", d.guard.held, d.tokens.reads)
	}
	// A GPU model with no GPU asked for doesn't fit on CPU.
	pv, err = d.svc.Preview(ctx, member, projectID, stagingID, DeployInput{EntryID: llama70BID, Target: in.Target})
	if err != nil || !slices.Equal(codes(pv), []string{ProblemNoFit}) {
		t.Errorf("70B on CPU: %+v, %v", pv.Problems, err)
	}
}

// Ruling M20 and the Task 20 note: a pasted model is stored for the
// workspace when it's deployed, and llama.cpp on CPU on Kubernetes is
// refused when its weights outgrow the fixed weight cache there.
func TestDeployStoresACustomModel(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()}, cpuCluster())
	d.hub.add(qwenRepo(t))
	big := ggufRepo(17 << 30)
	d.hub.add(big)
	gguf := ggufRepo(4683074240)
	gguf.id = "acme/Small-GGUF"
	d.hub.add(gguf)

	vllm, err := d.svc.Inspect(ctx, member, qwen7B, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.svc.Deploy(ctx, member, projectID, stagingID, DeployInput{EntryID: vllm.ID, Target: onGPU("nvidia-l40s")}); err != nil {
		t.Fatal(err)
	}
	if stored, cerr := d.custom.Custom(ctx, "org", vllm.ID); cerr != nil || stored.ID != vllm.ID {
		t.Errorf("stored: %v", cerr)
	}
	if res := current(t, d).Resources["qwen2-5-7b-instruct"]; res.Settings["entry"] != vllm.ID || res.Settings["model"] != qwen7B {
		t.Errorf("settings %v", res.Settings)
	}

	cluster := TargetChoice{Source: TargetConnection, ConnectionID: kubeConn}
	bigEntry, err := d.svc.Inspect(ctx, member, big.id, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.svc.Deploy(ctx, member, projectID, stagingID, DeployInput{EntryID: bigEntry.ID, Target: cluster})
	var refused *ProblemError
	want := "Qwen2.5-7B-Instruct-Q4_K_M.gguf needs a 22528 MB weight cache, and llama.cpp on CPU gets 20480 MB on Kubernetes. Run it on a GPU instead."
	if !errors.As(err, &refused) || refused.Problem.Code != ProblemNoFit || err.Error() != want {
		t.Errorf("17 GiB GGUF on a cluster's CPU: %v", err)
	}
	if _, cerr := d.custom.Custom(ctx, "org", bigEntry.ID); !errors.Is(cerr, ErrEntryNotFound) {
		t.Errorf("a refused custom model was stored: %v", cerr)
	}
	small, err := d.svc.Inspect(ctx, member, gguf.id, "")
	if err != nil {
		t.Fatal(err)
	}
	pv, err := d.svc.Preview(ctx, member, projectID, stagingID, DeployInput{EntryID: small.ID, Target: cluster})
	if err != nil || len(pv.Problems) != 0 || pv.Kind != recipes.ModelLlamaCPPKind || pv.Target.Provider != "kubernetes" {
		t.Errorf("4.4 GiB GGUF on a cluster: %q %+v, %v", pv.Kind, pv.Problems, err)
	}
}

func TestPreviewChecksTheBudgetAndTarget(t *testing.T) {
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()})
	in := DeployInput{EntryID: qwen7B, Target: onGPU("nvidia-l40s")}
	for _, budget := range []string{"0", "-5", "100000.01", "12.345", "lots"} {
		in.MonthlyBudgetUSD = budget
		if pv, err := d.svc.Preview(ctx, member, projectID, stagingID, in); err != nil || !slices.Equal(codes(pv), []string{ProblemBudget}) {
			t.Errorf("budget %q: %+v, %v", budget, pv.Problems, err)
		}
	}
	in.MonthlyBudgetUSD = "100000"
	if pv, err := d.svc.Preview(ctx, member, projectID, stagingID, in); err != nil || len(pv.Problems) != 0 {
		t.Errorf("budget 100000: %+v, %v", pv.Problems, err)
	}
	in.MonthlyBudgetUSD = ""
	for name, target := range map[string]TargetChoice{
		"server size":    {Source: TargetServerSize, ConnectionID: "conn_do", SizeID: "gpu-l40sx1-48gb"},
		"no such server": onGPU("nvidia-h100-80gb"),
		"no source":      {ConnectionID: gpuConn},
	} {
		in.Target = target
		if pv, err := d.svc.Preview(ctx, member, projectID, stagingID, in); err != nil || !slices.Equal(codes(pv), []string{ProblemTarget}) {
			t.Errorf("%s: %+v, %v", name, pv.Problems, err)
		}
	}
	// Two GPUs asked for, on a server with one free.
	in.Target = onGPU("nvidia-l40s")
	in.Target.GPU.Count = 2
	if pv, err := d.svc.Preview(ctx, member, projectID, stagingID, in); err != nil || !slices.Equal(codes(pv), []string{ProblemTarget}) ||
		pv.Problems[0].Message != "gpu-box has 1 free NVIDIA L40S GPUs, not 2." {
		t.Errorf("two GPUs: %+v, %v", pv.Problems, err)
	}
	if _, err := d.svc.Preview(ctx, member, projectID, stagingID, DeployInput{EntryID: "acme/none", Target: onGPU("nvidia-l40s")}); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("unknown entry: %v", err)
	}
	if _, err := newDeploy(t, deployRegistry(t, llamaRev, ""), perms{}, nil).svc.Preview(ctx, member, projectID, stagingID, in); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("without ai:read: %v", err)
	}
}

func TestDefaultResourceAndAlias(t *testing.T) {
	for id, want := range map[string][2]string{
		"meta-llama/Llama-3.1-8B-Instruct":             {"llama-3-1-8b-instruct", "llama-3.1-8b-instruct"},
		"hf:acme/7B__Chat@0123456789ab":                {"model-7b-chat", "7b__chat"},
		"acme/" + strings.Repeat("Very-Long-Name-", 5): {"very-long-name-very-long-name-very-long", strings.ToLower(strings.Repeat("Very-Long-Name-", 5))},
	} {
		e := registry.Entry{ID: id}
		if got := DefaultResource(e); got != want[0] || !resourceName.MatchString(got) {
			t.Errorf("DefaultResource(%s) = %q, want %q", id, got, want[0])
		}
		if got := DefaultAlias(e); got != want[1] {
			t.Errorf("DefaultAlias(%s) = %q, want %q", id, got, want[1])
		}
	}
}
