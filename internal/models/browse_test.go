package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/hfhub"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// Entries of the browsing fixture, taken from the real registry.
const (
	qwen7B    = "Qwen/Qwen2.5-7B-Instruct"
	llama8B   = "deepseek-ai/DeepSeek-R1-Distill-Llama-8B"
	llama70B  = "deepseek-ai/DeepSeek-R1-Distill-Llama-70B"
	ministral = "mistralai/Ministral-8B-Instruct-2410"
	bgeSmall  = "BAAI/bge-small-en-v1.5"
)

// browseRegistry is a registry of five real entries, with the
// qualification results results (YAML list items, "" for none).
func browseRegistry(t *testing.T, results string) *registry.Registry {
	t.Helper()
	families := map[string][]registry.Entry{}
	for _, id := range []string{qwen7B, llama8B, llama70B, ministral, bgeSmall} {
		e, ok := registry.Default().Entry(id)
		if !ok {
			t.Fatalf("the registry has no %s", id)
		}
		families[e.Family] = append(families[e.Family], e)
	}
	fsys := fstest.MapFS{}
	for family, entries := range families {
		raw, err := yaml.Marshal(registry.File{Family: family, Entries: entries})
		if err != nil {
			t.Fatal(err)
		}
		fsys[family+".yaml"] = &fstest.MapFile{Data: raw}
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

// passedOn is a passed qualification result for id's quantization q on
// one GPU of kind, with the engine version Loomplane ships.
func passedOn(t *testing.T, id, q, kind, version string) string {
	t.Helper()
	e, _ := registry.Default().Entry(id)
	quant, _ := e.Quantization(q)
	_, revision := quant.WeightsRepo(e)
	return fmt.Sprintf(`  - entry: %s
    quantization: %s
    revision: %s
    gpu_kind: %s
    gpus: 1
    max_model_len: 32768
    engine: %s
    engine_version: %s
    outcome: passed
    detail: first token 90 ms
    ran_at: 2026-11-03T06:12:00Z
    run: https://github.com/xraph/loomplane/actions/runs/7
`, id, q, revision, kind, quant.Engine, version)
}

// perms is an Authorizer that grants exactly its permissions.
type perms []security.Permission

func (a perms) Require(_ context.Context, _ security.Principal, perm security.Permission, _ string) error {
	if slices.Contains(a, perm) {
		return nil
	}
	return fmt.Errorf("%w (%s)", security.ErrForbidden, perm.Name())
}

var (
	developer = perms{security.AIRead, security.AIEdit, security.InfrastructureRead, security.CloudAccountRead}
	viewer    = perms{security.AIRead, security.InfrastructureRead, security.CloudAccountRead}
	// aiOnly can use the AI section but not read infrastructure or cloud
	// accounts.
	aiOnly = perms{security.AIRead, security.AIEdit}
)

var member = security.Principal{UserID: "usr_1", OrgID: "org"}

// fakeTargets is the workspace's GPU targets. It counts its calls.
type fakeTargets struct {
	mu      sync.Mutex
	targets []gpus.GPUTarget
	err     error
	calls   int
}

func (f *fakeTargets) Targets(_ context.Context, _ security.Principal, q gpus.GPUTargetQuery) (*gpus.GPUTargetList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if q.Count != 1 {
		return nil, fmt.Errorf("asked for %d GPUs", q.Count)
	}
	if f.err != nil {
		return nil, f.err
	}
	return &gpus.GPUTargetList{Targets: f.targets}, nil
}

// fakeConnections is the workspace's connections, behind
// cloud_account:read as the real service keeps them.
type fakeConnections struct {
	authz Authorizer
	list  []*connections.Connection
}

func (f fakeConnections) List(ctx context.Context, p security.Principal) ([]*connections.Connection, error) {
	if err := f.authz.Require(ctx, p, security.CloudAccountRead, ""); err != nil {
		return nil, err
	}
	return f.list, nil
}

func l4Server(free bool) gpus.GPUTarget {
	return gpus.GPUTarget{
		Source: gpus.SourceConnection, ConnectionID: "conn_gpu", Name: "gpu-box", Cloud: "server",
		Accelerator: provider.Accelerator{Kind: "nvidia-l4", Vendor: provider.VendorNVIDIA, Count: 1, MemoryGiB: 24},
		Free:        1, Fits: free,
	}
}

func cpuServer() *connections.Connection {
	return &connections.Connection{ID: "conn_cpu", Name: "cpu-box", Cloud: connections.Server, Status: connections.StatusReady}
}

// memCustom is a CustomStore in memory, keyed by org and ID.
type memCustom struct {
	mu      sync.Mutex
	entries map[customKey]registry.Entry
}

func (m *memCustom) PutCustom(_ context.Context, orgID string, e registry.Entry) error {
	if err := checkCustom(e); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = map[customKey]registry.Entry{}
	}
	if _, ok := m.entries[customKey{orgID, e.ID}]; !ok {
		m.entries[customKey{orgID, e.ID}] = e
	}
	return nil
}

func (m *memCustom) Custom(_ context.Context, orgID, id string) (registry.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[customKey{orgID, id}]; ok {
		return e, nil
	}
	return registry.Entry{}, fmt.Errorf("%w: %s", ErrEntryNotFound, id)
}

func (m *memCustom) ListCustom(_ context.Context, orgID string) ([]registry.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []registry.Entry
	for k, e := range m.entries {
		if k.org == orgID {
			out = append(out, e)
		}
	}
	return out, nil
}

type browseSetup struct {
	svc     *Service
	targets *fakeTargets
	hub     *fakeHub
	tokens  *fakeTokens
	custom  *memCustom
	now     *time.Time
}

// newBrowse is a Service over r with authz, the targets and connections
// given, a fake Hub and no workspace token.
func newBrowse(t *testing.T, r *registry.Registry, authz Authorizer, targets []gpus.GPUTarget, conns ...*connections.Connection) *browseSetup {
	t.Helper()
	hub := newFakeHub(t)
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	b := &browseSetup{targets: &fakeTargets{targets: targets}, hub: hub, tokens: &fakeTokens{}, custom: &memCustom{}, now: &now}
	b.svc = NewService(Deps{
		Registry: r, Hub: hfhub.New(hub.srv.URL, hub.srv.Client()), Settings: b.tokens, Custom: b.custom,
		GPUs: b.targets, Connections: fakeConnections{authz: authz, list: conns}, Authz: authz,
		Now: func() time.Time { return *b.now },
	})
	return b
}

func ids(entries []EntrySummary) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	slices.Sort(out)
	return out
}

func sorted(s ...string) []string {
	slices.Sort(s)
	return s
}

func TestRegistryFilters(t *testing.T) {
	ctx := context.Background()
	b := newBrowse(t, browseRegistry(t, ""), developer, nil)
	list := func(q RegistryQuery) *RegistryList {
		t.Helper()
		got, err := b.svc.Registry(ctx, member, q)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	all := list(RegistryQuery{})
	if got := ids(all.Entries); !slices.Equal(got, sorted(qwen7B, llama8B, llama70B, ministral, bgeSmall)) {
		t.Errorf("everything: %v", got)
	}
	if all.Counts["chat"] != 2 || all.Counts["reasoning"] != 2 || all.Counts["embedding"] != 1 || all.FitsNotice != "" {
		t.Errorf("counts %v notice %q", all.Counts, all.FitsNotice)
	}
	for _, e := range all.Entries {
		if e.FitsHere != nil {
			t.Errorf("%s: FitsHere without fits asked for", e.ID)
		}
	}

	if got := ids(list(RegistryQuery{Task: "embedding"}).Entries); !slices.Equal(got, []string{bgeSmall}) {
		t.Errorf("task embedding: %v", got)
	}
	// The counts ignore the task filter, so every chip keeps its number.
	if got := list(RegistryQuery{Task: "embedding"}).Counts; got["chat"] != 2 {
		t.Errorf("counts with a task: %v", got)
	}
	if got := ids(list(RegistryQuery{Size: SizeSmall}).Entries); !slices.Equal(got, sorted(qwen7B, llama8B, ministral)) {
		t.Errorf("size small: %v", got)
	}
	if got := ids(list(RegistryQuery{Size: SizeLarge}).Entries); !slices.Equal(got, []string{llama70B}) {
		t.Errorf("size large: %v", got)
	}
	if got := ids(list(RegistryQuery{Size: SizeTiny}).Entries); !slices.Equal(got, []string{bgeSmall}) {
		t.Errorf("size tiny: %v", got)
	}
	if got := ids(list(RegistryQuery{Commercial: true}).Entries); slices.Contains(got, ministral) || len(got) != 4 {
		t.Errorf("commercial: %v", got)
	}
	if got := ids(list(RegistryQuery{License: "apache-2.0"}).Entries); !slices.Equal(got, []string{qwen7B}) {
		t.Errorf("license apache-2.0: %v", got)
	}
	// "llama" is in both distills' titles and IDs, whatever the case.
	if got := ids(list(RegistryQuery{Q: "LLAMA"}).Entries); !slices.Equal(got, sorted(llama8B, llama70B)) {
		t.Errorf("q llama: %v", got)
	}
	if got := ids(list(RegistryQuery{Q: "Distill Llama 8B"}).Entries); !slices.Equal(got, []string{llama8B}) {
		t.Errorf("q by title: %v", got)
	}
	if got := ids(list(RegistryQuery{Q: "deepseek-ai/DeepSeek-R1-Distill-Llama-70B"}).Entries); !slices.Equal(got, []string{llama70B}) {
		t.Errorf("q by ID: %v", got)
	}
	if got := list(RegistryQuery{Q: "llama", Size: SizeSmall, Task: "reasoning"}); !slices.Equal(ids(got.Entries), []string{llama8B}) {
		t.Errorf("q, size and task: %v", ids(got.Entries))
	}

	for _, bad := range []RegistryQuery{{Task: "image"}, {Size: "enormous"}} {
		if _, err := b.svc.Registry(ctx, member, bad); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if _, err := b.svc.Registry(ctx, member, RegistryQuery{}); err != nil {
		t.Fatal(err)
	}
	if _, err := newBrowse(t, browseRegistry(t, ""), perms{}, nil).svc.Registry(ctx, member, RegistryQuery{}); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("without ai:read: %v", err)
	}
}

func TestRegistryNeverCallsAnEstimateTested(t *testing.T) {
	ctx := context.Background()
	summary := func(r *registry.Registry, id string) EntrySummary {
		t.Helper()
		got, err := newBrowse(t, r, developer, nil).svc.Registry(ctx, member, RegistryQuery{Q: id})
		if err != nil || len(got.Entries) != 1 {
			t.Fatalf("%s: %v, %v", id, got, err)
		}
		return got.Entries[0]
	}

	estimated := summary(browseRegistry(t, ""), qwen7B)
	if estimated.Verification != registry.StatusEstimated || len(estimated.TestedOn) != 0 {
		t.Errorf("memory math only: %s on %v", estimated.Verification, estimated.TestedOn)
	}
	// A pass on an engine version Loomplane no longer ships proves nothing.
	old := summary(browseRegistry(t, passedOn(t, qwen7B, "bf16", "nvidia-l40s", "0.30.0")), qwen7B)
	if old.Verification != registry.StatusEstimated || len(old.TestedOn) != 0 {
		t.Errorf("passed on an older engine: %s on %v", old.Verification, old.TestedOn)
	}
	tested := summary(browseRegistry(t, passedOn(t, qwen7B, "bf16", "nvidia-l40s", engines.Current()[registry.EngineVLLM])), qwen7B)
	if tested.Verification != registry.StatusTested || !slices.Equal(tested.TestedOn, []string{"NVIDIA L40S"}) {
		t.Errorf("passed on the current engine: %s on %v", tested.Verification, tested.TestedOn)
	}
	// The other entries stay estimated.
	if other := summary(browseRegistry(t, passedOn(t, qwen7B, "bf16", "nvidia-l40s", engines.Current()[registry.EngineVLLM])), llama70B); other.Verification != registry.StatusEstimated {
		t.Errorf("another entry: %s", other.Verification)
	}
}

// fitsHere reads each fixture entry's FitsHere.
func fitsHere(t *testing.T, b *browseSetup) map[string]*bool {
	t.Helper()
	out := map[string]*bool{}
	for _, id := range []string{qwen7B, llama8B, llama70B, ministral, bgeSmall} {
		d, err := b.svc.Entry(context.Background(), member, id)
		if err != nil {
			t.Fatal(err)
		}
		out[id] = d.FitsHere
	}
	return out
}

func is(b *bool, want bool) bool { return b != nil && *b == want }

func TestFitsHere(t *testing.T) {
	r := browseRegistry(t, "")

	// One L4 server and no CPU target: an 8B model in bf16 fits one L4, a
	// 70B one doesn't, and a CPU-only model has nowhere to run.
	b := newBrowse(t, r, developer, []gpus.GPUTarget{l4Server(true)})
	got := fitsHere(t, b)
	if !is(got[llama8B], true) || !is(got[llama70B], false) || !is(got[bgeSmall], false) {
		t.Errorf("one L4: 8B %v, 70B %v, bge %v", got[llama8B], got[llama70B], got[bgeSmall])
	}
	if b.targets.calls != 5 {
		t.Errorf("Targets called %d times for five entries", b.targets.calls)
	}
	// A server whose GPU is taken can't take the model now.
	if got := fitsHere(t, newBrowse(t, r, developer, []gpus.GPUTarget{l4Server(false)})); !is(got[llama8B], false) {
		t.Errorf("busy L4: %v", got[llama8B])
	}
	// A GPU server size Loomplane can create counts like one the workspace has.
	size := gpus.GPUTarget{Source: gpus.SourceServerSize, ConnectionID: "conn_do", Name: "DigitalOcean", SizeID: "gpu-h100x8",
		Accelerator: provider.Accelerator{Kind: "nvidia-h100-80gb", Vendor: provider.VendorNVIDIA, Count: 8, MemoryGiB: 80}, Fits: true}
	if got := fitsHere(t, newBrowse(t, r, developer, []gpus.GPUTarget{size})); !is(got[llama70B], true) {
		t.Errorf("an 8 x H100 size: 70B %v", got[llama70B])
	}

	// A CPU-only model with a ready CPU server fits; one not ready doesn't count.
	b = newBrowse(t, r, developer, nil, cpuServer())
	if got := fitsHere(t, b); !is(got[bgeSmall], true) || !is(got[llama70B], false) {
		t.Errorf("CPU server: bge %v, 70B %v", got[bgeSmall], got[llama70B])
	}
	down := cpuServer()
	down.Status = "failed"
	if got := fitsHere(t, newBrowse(t, r, developer, nil, down)); !is(got[bgeSmall], false) {
		t.Errorf("CPU server not ready: bge %v", got[bgeSmall])
	}

	// Asking for fits filters by them, with one read of the targets.
	b = newBrowse(t, r, developer, []gpus.GPUTarget{l4Server(true)})
	list, err := b.svc.Registry(context.Background(), member, RegistryQuery{Fits: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(list.Entries); !slices.Equal(got, sorted(qwen7B, llama8B, ministral)) {
		t.Errorf("fits filter: %v", got)
	}
	for _, e := range list.Entries {
		if !is(e.FitsHere, true) {
			t.Errorf("%s listed with FitsHere %v", e.ID, e.FitsHere)
		}
	}
	if b.targets.calls != 1 {
		t.Errorf("Targets called %d times for one list", b.targets.calls)
	}
}

func TestFitsHereSurvivesATargetError(t *testing.T) {
	b := newBrowse(t, browseRegistry(t, ""), developer, nil)
	b.targets.err = errors.New("the database is down")
	list, err := b.svc.Registry(context.Background(), member, RegistryQuery{Fits: true})
	if err != nil {
		t.Fatalf("a target error failed browsing: %v", err)
	}
	if list.FitsNotice != "Couldn't check your GPUs." || len(list.Entries) != 5 {
		t.Errorf("notice %q, %d entries", list.FitsNotice, len(list.Entries))
	}
	for _, e := range list.Entries {
		if e.FitsHere != nil {
			t.Errorf("%s: FitsHere %v after a target error", e.ID, *e.FitsHere)
		}
	}
	d, err := b.svc.Entry(context.Background(), member, qwen7B)
	if err != nil {
		t.Fatal(err)
	}
	if d.FitsHere != nil || d.FitsNotice != FitsUnknown {
		t.Errorf("entry: fits %v notice %q", d.FitsHere, d.FitsNotice)
	}
}

// Targets needs infrastructure:read and cloud_account:read. A member
// without them still browses, and is told the page couldn't check.
func TestFitsHereWithoutInfrastructureRead(t *testing.T) {
	ctx := context.Background()
	conns := fakeConnections{authz: aiOnly, list: []*connections.Connection{cpuServer()}}
	targets := gpus.NewService(gpus.Deps{Connections: conns, Bindings: noBindings{}, Authz: aiOnly})
	svc := NewService(Deps{Registry: browseRegistry(t, ""), GPUs: targets, Connections: conns, Authz: aiOnly})

	list, err := svc.Registry(ctx, member, RegistryQuery{Fits: true})
	if err != nil {
		t.Fatal(err)
	}
	if list.FitsNotice != FitsUnknown || len(list.Entries) != 5 {
		t.Errorf("notice %q, %d entries", list.FitsNotice, len(list.Entries))
	}
	for _, e := range list.Entries {
		if e.FitsHere != nil {
			t.Errorf("%s: FitsHere %v", e.ID, *e.FitsHere)
		}
	}
	d, err := svc.Entry(ctx, member, bgeSmall)
	if err != nil || d.FitsHere != nil || d.FitsNotice != FitsUnknown {
		t.Errorf("entry: fits %v notice %q, %v", d.FitsHere, d.FitsNotice, err)
	}

	// With both permissions, the same workspace gets an answer.
	both := perms{security.AIRead, security.InfrastructureRead, security.CloudAccountRead}
	conns.authz = both
	svc = NewService(Deps{Registry: browseRegistry(t, ""), GPUs: gpus.NewService(gpus.Deps{Connections: conns, Bindings: noBindings{}, Authz: both}),
		Connections: conns, Authz: both})
	d, err = svc.Entry(ctx, member, bgeSmall)
	if err != nil || !is(d.FitsHere, true) || d.FitsNotice != "" {
		t.Errorf("with infrastructure:read: fits %v notice %q, %v", d.FitsHere, d.FitsNotice, err)
	}
}

type noBindings struct{}

func (noBindings) GPUBindings(context.Context, string) (map[string]map[string]int, error) {
	return map[string]map[string]int{}, nil
}

func TestEntryDetailFitsPerKind(t *testing.T) {
	b := newBrowse(t, browseRegistry(t, ""), developer, nil)
	kinds := nvidiaKinds()
	if len(kinds) < 10 {
		t.Fatalf("only %d NVIDIA kinds", len(kinds))
	}
	d, err := b.svc.Entry(context.Background(), member, llama8B)
	if err != nil {
		t.Fatal(err)
	}
	if d.Revision == "" || d.HuggingFaceURL != "https://huggingface.co/"+llama8B || d.Verification != registry.StatusEstimated {
		t.Errorf("revision %q url %q verification %s", d.Revision, d.HuggingFaceURL, d.Verification)
	}
	if d.MinGPUMemoryGiB != 21 || !d.CPU {
		t.Errorf("min GPU memory %d, cpu %v", d.MinGPUMemoryGiB, d.CPU)
	}
	for _, q := range d.Quantizations {
		var got []string
		for _, f := range q.Fits {
			got = append(got, f.GPUKind)
		}
		var want []string
		for _, k := range kinds {
			want = append(want, k.ID)
		}
		if q.Engine == registry.EngineLlamaCPP {
			want = append(want, registry.CPUKind)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s fits on %v, want %v", q.ID, got, want)
		}
		for _, f := range q.Fits {
			if f.Status == registry.StatusTested || f.Status == registry.StatusUntested {
				t.Errorf("%s on %s: %s without a run", q.ID, f.GPUKind, f.Status)
			}
		}
	}
	// The smallest count that fits: one L4, two T4s.
	bf16 := d.Quantizations[0]
	on := func(kind string) registry.Fit {
		return bf16.Fits[slices.IndexFunc(bf16.Fits, func(f registry.Fit) bool { return f.GPUKind == kind })]
	}
	if l4, t4 := on("nvidia-l4"), on("nvidia-t4"); !l4.Fits() || l4.Need.GPUs != 1 || !t4.Fits() || t4.Need.GPUs != 2 {
		t.Errorf("8B bf16: L4 %+v, T4 %+v", l4, t4)
	}

	// TEI runs on CPU only.
	d, err = b.svc.Entry(context.Background(), member, bgeSmall)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Quantizations) != 1 || len(d.Quantizations[0].Fits) != 1 || d.Quantizations[0].Fits[0].GPUKind != registry.CPUKind ||
		d.MinGPUMemoryGiB != 0 || !d.CPU {
		t.Errorf("bge: %+v, min GPU %d", d.Quantizations, d.MinGPUMemoryGiB)
	}
	if _, err := b.svc.Entry(context.Background(), member, "acme/nothing"); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("unknown entry: %v", err)
	}
}

func TestCardStripsFrontMatter(t *testing.T) {
	ctx := context.Background()
	b := newBrowse(t, browseRegistry(t, ""), developer, nil)
	e, _ := registry.Default().Entry(qwen7B)
	b.hub.add(&hubRepo{id: qwen7B, sha: e.Revision, readme: "---\nlicense: apache-2.0\ntags:\n- chat\n---\n\n# Qwen2.5-7B-Instruct\n\nHello.\n"})

	c, err := b.svc.Card(ctx, member, qwen7B)
	if err != nil {
		t.Fatal(err)
	}
	if c.Markdown != "# Qwen2.5-7B-Instruct\n\nHello.\n" || c.Note != "" {
		t.Errorf("markdown %q note %q", c.Markdown, c.Note)
	}
	if c.SourceURL != "https://huggingface.co/"+qwen7B+"/blob/"+e.Revision+"/README.md" {
		t.Errorf("source %q", c.SourceURL)
	}
	if got := b.hub.requests(); len(got) != 1 || got[0] != "GET /"+qwen7B+"/resolve/"+e.Revision+"/README.md anonymous" {
		t.Errorf("requests %v", got)
	}
	// A day's cache: the second read asks nothing.
	if _, err = b.svc.Card(ctx, member, qwen7B); err != nil || len(b.hub.requests()) != 1 {
		t.Errorf("cached card: %d requests, %v", len(b.hub.requests()), err)
	}
	*b.now = b.now.Add(25 * time.Hour)
	if _, err = b.svc.Card(ctx, member, qwen7B); err != nil || len(b.hub.requests()) != 2 {
		t.Errorf("a day later: %d requests, %v", len(b.hub.requests()), err)
	}

	// Too long to show, and none at all.
	e8, _ := registry.Default().Entry(llama8B)
	b.hub.add(&hubRepo{id: llama8B, sha: e8.Revision, readme: strings.Repeat("x", maxCardBytes+1)})
	if c, err = b.svc.Card(ctx, member, llama8B); err != nil || c.Markdown != "" || !strings.Contains(c.Note, "Read it on Hugging Face") {
		t.Errorf("long card: %+v, %v", c, err)
	}
	e70, _ := registry.Default().Entry(llama70B)
	b.hub.add(&hubRepo{id: llama70B, sha: e70.Revision})
	if c, err = b.svc.Card(ctx, member, llama70B); err != nil || c.Markdown != "" || c.Note != llama70B+" has no model card." {
		t.Errorf("no card: %+v, %v", c, err)
	}

	for in, want := range map[string]string{
		"# Title\n":                                   "# Title\n",
		"---\nno closing line\n# Title\n":             "---\nno closing line\n# Title\n",
		"\ufeff---\r\nlicense: mit\r\n---\r\n# T\r\n": "# T\r\n",
		"--- \nx: 1\n---\n# T":                        "# T",
	} {
		if got := stripFrontMatter(in); got != want {
			t.Errorf("stripFrontMatter(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCPUCacheProblem(t *testing.T) {
	small := registry.Quantization{ID: "gguf-q4_k_m", Engine: registry.EngineLlamaCPP, File: "m-Q4_K_M.gguf", WeightBytes: 5 << 30}
	if got := CPUCacheProblem(small); got != "" {
		t.Errorf("5 GiB: %q", got)
	}
	big := small
	big.WeightBytes = 17 << 30
	if got := CPUCacheProblem(big); got != "m-Q4_K_M.gguf needs a 22528 MB weight cache, and llama.cpp on CPU gets 20480 MB on Kubernetes. Run it on a GPU instead." {
		t.Errorf("17 GiB: %q", got)
	}
	if got := CPUCacheProblem(registry.Quantization{ID: "native", Engine: registry.EngineTEI, WeightBytes: 30 << 30}); got != "" {
		t.Errorf("TEI: %q", got)
	}
}

// fakeHub answers like huggingface.co: model metadata, and files at a
// commit, with gated repos' files only for a token granted access.
type fakeHub struct {
	srv *httptest.Server

	mu      sync.Mutex
	repos   map[string]*hubRepo
	granted map[string]bool // tokens with access to every gated repo
	log     []string
}

type hubRepo struct {
	id, sha, pipeline string
	tags              []string
	files             map[string]int64
	gated, private    bool
	config, readme    string
}

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	h := &fakeHub{repos: map[string]*hubRepo{}, granted: map[string]bool{}}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHub) add(r *hubRepo) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.repos[r.id] = r
}

func (h *fakeHub) grant(token string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.granted[token] = true
}

// requests lists what was asked, as "METHOD path anonymous" or
// "METHOD path token <token>".
func (h *fakeHub) requests() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.log)
}

func (h *fakeHub) serve(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	who := "anonymous"
	if token != "" {
		who = "token " + token
	}
	h.log = append(h.log, r.Method+" "+r.URL.Path+" "+who)

	refuse := func(status int, code string) {
		w.Header().Set("X-Error-Code", code)
		w.WriteHeader(status)
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, "/api/models/"); ok {
		repoID, rev, _ := strings.Cut(rest, "/revision/")
		repo := h.repos[repoID]
		if repo == nil || (repo.private && !h.granted[token]) || (rev != "main" && rev != repo.sha) {
			refuse(http.StatusUnauthorized, "RepoNotFound")
			return
		}
		siblings := []map[string]any{}
		for name, size := range repo.files {
			siblings = append(siblings, map[string]any{"rfilename": name, "size": size})
		}
		var gated any = false
		if repo.gated {
			gated = "manual"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": repo.id, "sha": repo.sha, "pipeline_tag": repo.pipeline, "gated": gated, "tags": repo.tags, "siblings": siblings,
		})
		return
	}
	for id, repo := range h.repos {
		prefix := "/" + id + "/resolve/" + repo.sha + "/"
		file, ok := strings.CutPrefix(r.URL.Path, prefix)
		if !ok {
			continue
		}
		if (repo.gated || repo.private) && !h.granted[token] {
			refuse(http.StatusForbidden, "GatedRepo")
			return
		}
		var body string
		switch file {
		case "config.json":
			body = repo.config
		case "README.md":
			body = repo.readme
		}
		if body == "" {
			refuse(http.StatusNotFound, "EntryNotFound")
			return
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(body))
		}
		return
	}
	refuse(http.StatusUnauthorized, "RepoNotFound")
}
