package deployments

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/pkg/provider"
)

type memStore struct {
	mu sync.Mutex
	d  map[string]*Deployment
}

func (m *memStore) CreateDeployment(_ context.Context, d *Deployment) (*Deployment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.d {
		if x.PlanID == d.PlanID {
			c := *x
			return &c, nil
		}
	}
	c := *d
	c.Steps = slices.Clone(d.Steps)
	c.Sources = slices.Clone(d.Sources)
	m.d[d.ID] = &c
	out := c
	return &out, nil
}

func (m *memStore) GetDeployment(_ context.Context, _, id string) (*Deployment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.d[id]
	if !ok {
		return nil, ErrNotFound
	}
	c := *d
	c.Steps = slices.Clone(d.Steps)
	c.Sources = slices.Clone(d.Sources)
	return &c, nil
}

func (m *memStore) ForPlan(_ context.Context, _, planID string) (*Deployment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.d {
		if d.PlanID == planID {
			c := *d
			return &c, nil
		}
	}
	return nil, ErrNotFound
}

func (m *memStore) ListDeployments(context.Context, string, string, int) ([]*Deployment, error) {
	return nil, nil
}

func (m *memStore) SetSources(_ context.Context, _, id string, sources []SourceArtifact) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.d[id].Sources = slices.Clone(sources)
	return nil
}

func (m *memStore) UpdateStep(_ context.Context, _, id string, s Step) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.d[id].Steps {
		if m.d[id].Steps[i].ActionID == s.ActionID {
			m.d[id].Steps[i] = s
		}
	}
	return nil
}

func (m *memStore) Finish(_ context.Context, _, id, status, reason string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.d[id].Status == StatusRunning {
		m.d[id].Status, m.d[id].Error, m.d[id].FinishedAt = status, reason, &at
	}
	return nil
}

type fakePlans struct {
	mu       sync.Mutex
	plan     *planning.Plan
	bindings map[string]*planning.Binding
	deployed []planning.DeployedSource
}

func (f *fakePlans) RecordDeployed(_ context.Context, _ string, d *planning.DeployedSource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deployed = append(f.deployed, *d)
	return nil
}

func (f *fakePlans) GetPlan(context.Context, string, string) (*planning.Plan, error) {
	return f.plan, nil
}

func (f *fakePlans) ListBindings(context.Context, string, string) ([]*planning.Binding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*planning.Binding, 0, len(f.bindings))
	for _, b := range f.bindings {
		c := *b
		out = append(out, &c)
	}
	return out, nil
}

func (f *fakePlans) PutBinding(_ context.Context, b *planning.Binding) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bindings[string(b.Subject)+":"+b.Name] = b
	return nil
}

func (f *fakePlans) DeleteBinding(_ context.Context, _, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, b := range f.bindings {
		if b.ID == id {
			delete(f.bindings, k)
		}
	}
	return nil
}

type fakeCredentials struct{}

func (fakeCredentials) Credential(_ context.Context, _, id string) (*connections.Connection, []byte, error) {
	return &connections.Connection{ID: id, Status: connections.StatusReady}, []byte("secret-" + id), nil
}

type fakeVariables map[string]map[string]string

func (f fakeVariables) Values(_ context.Context, _, _, _, service string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range f[service] {
		out[k] = v
	}
	return out, nil
}

// fakeSources stores a tarball whose content is the repository and commit
// it came from, so a test can tell which one a job opened.
type fakeSources struct {
	mu     sync.Mutex
	stored []string
	repos  []string
	files  map[string]string
}

func (f *fakeSources) Store(_ context.Context, _, repository, sha, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stored = append(f.stored, key)
	f.repos = append(f.repos, repository)
	if f.files == nil {
		f.files = map[string]string{}
	}
	f.files[key] = repository + "@" + sha
	return nil
}

func (f *fakeSources) Open(_ context.Context, key string) (io.ReadCloser, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	content, ok := f.files[key]
	if !ok {
		return nil, 0, errors.New("no artifact at " + key)
	}
	return io.NopCloser(strings.NewReader(content)), int64(len(content)), nil
}

// recorder is an executor that answers at url and remembers each job, and
// what each service's source held.
type recorder struct {
	mu   sync.Mutex
	url  string
	fail map[string]error
	jobs []*Job
	read map[string]string
}

func (r *recorder) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	r.mu.Lock()
	r.jobs = append(r.jobs, job)
	err := r.fail[job.Action.Name]
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if job.Action.Service != nil {
		src, _, err := job.Source(ctx)
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(src)
		_ = src.Close()
		if err != nil {
			return nil, err
		}
		r.mu.Lock()
		if r.read == nil {
			r.read = map[string]string{}
		}
		r.read[job.Action.Name] = string(content)
		r.mu.Unlock()
	}
	if err := job.Checkpoint([]byte("op-" + job.Action.Name)); err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: "ext-" + job.Action.Name, URL: r.url + "/" + job.Action.Name, Image: "img:" + job.Action.Name}, nil
}

func (r *recorder) Remove(context.Context, *Job, *planning.Binding) error { return nil }

func available(t *testing.T) *provider.Registry {
	t.Helper()
	catalog := providers.Catalog()
	list := make([]provider.Provider, 0, len(catalog))
	for _, p := range catalog {
		if p.Name == "cloud-run" || p.Name == "vercel" {
			p.Available, p.Note = true, ""
		}
		list = append(list, p)
	}
	reg, err := provider.NewRegistry(list...)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// webAndAPI is an approved plan: api on Cloud Run first, then web on
// Vercel, which needs api.
func webAndAPI() *planning.Plan {
	return &planning.Plan{
		ID: "plan_1", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Environment: "production",
		Status: planning.StatusApproved, Executable: true,
		Sources: []planning.PlanSource{{SourceID: "src_1", Alias: "mono", Repository: "acme/mono", AnalysisID: "anl_1",
			CommitSHA: strings.Repeat("a", 40), InScope: true}},
		Connections: map[string]string{"gcp": "conn_g", "vercel": "conn_v"},
		Actions: []planning.Action{
			{ID: "service:api", Op: planning.Create, Subject: planning.ServiceSubject, Name: "api", Provider: "cloud-run", Step: 1,
				Service: &planning.ServiceConfig{Provider: "cloud-run", Service: "svc:go:services/api", Public: true}},
			{ID: "service:web", Op: planning.Create, Subject: planning.ServiceSubject, Name: "web", Provider: "vercel", Step: 2,
				Service: &planning.ServiceConfig{Provider: "vercel", Service: "svc:node:apps/web", Public: true,
					Env: []planning.EnvLink{{Key: "API_URL", From: "api", Subject: planning.ServiceSubject}}}},
		},
	}
}

func newRunner(t *testing.T, plan *planning.Plan, exec Executor, vars fakeVariables) (*Runner, *memStore, *fakePlans) {
	t.Helper()
	store := &memStore{d: map[string]*Deployment{}}
	plans := &fakePlans{plan: plan, bindings: map[string]*planning.Binding{}}
	r := NewRunner(RunnerDeps{
		Store: store, Plans: plans, Credentials: fakeCredentials{}, Variables: vars,
		Sources: &fakeSources{}, Providers: available(t),
		Executors: map[string]Executor{"cloud-run": exec, "vercel": exec},
	})
	r.verifyWait = time.Second
	return r, store, plans
}

func answering(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	t.Cleanup(srv.Close)
	return srv.URL
}

// deploy runs a deployment the way the workflow does.
func deploy(t *testing.T, r *Runner, plan *planning.Plan) (string, error) {
	t.Helper()
	ctx := t.Context()
	id, steps, err := r.Start(ctx, plan.OrgID, plan.ID)
	if err != nil || id == "" {
		return id, err
	}
	if err := r.FetchSource(ctx, plan.OrgID, id); err != nil {
		return id, err
	}
	reason := ""
	for _, group := range steps {
		for _, action := range group {
			var saved []byte
			if err := r.Run(ctx, plan.OrgID, id, action, nil, func(b []byte) error { saved = b; return nil }); err != nil {
				reason = err.Error()
				break
			}
			if len(saved) == 0 {
				t.Fatalf("%s never checkpointed", action)
			}
		}
		if reason != "" {
			break
		}
	}
	return id, r.Finish(ctx, plan.OrgID, id, reason)
}

func TestRunner_deploysInOrderAndLinksServices(t *testing.T) {
	exec := &recorder{url: answering(t, http.StatusOK)}
	vars := fakeVariables{"svc:node:apps/web": {"NEXT_PUBLIC_NAME": "Acme"}}
	r, store, plans := newRunner(t, webAndAPI(), exec, vars)

	id, err := deploy(t, r, webAndAPI())
	if err != nil {
		t.Fatal(err)
	}
	d, _ := store.GetDeployment(t.Context(), "org_1", id)
	if d.Status != StatusSucceeded || len(d.Sources) != 1 || d.Sources[0].Key != SourceKey("org_1", "proj_1", "mono", strings.Repeat("a", 40)) ||
		d.CommitSHA != strings.Repeat("a", 40) {
		t.Fatalf("deployment %+v", d)
	}
	if len(exec.jobs) != 2 || exec.jobs[0].Action.Name != "api" || exec.jobs[1].Action.Name != "web" {
		t.Fatalf("ran %d jobs", len(exec.jobs))
	}
	web := exec.jobs[1]
	if web.Env["API_URL"] != exec.url+"/api" || web.Env["NEXT_PUBLIC_NAME"] != "Acme" {
		t.Fatalf("web env %v", web.Env)
	}
	if string(web.Credential) != "secret-conn_v" || string(exec.jobs[0].Credential) != "secret-conn_g" {
		t.Fatal("jobs got the wrong connection's credential")
	}
	for _, s := range d.Steps {
		if s.Status != StepSucceeded || s.URL == "" || s.FinishedAt == nil {
			t.Fatalf("step %+v", s)
		}
	}
	if b := plans.bindings["service:api"]; b == nil || b.ExternalID != "ext-api" || b.URL != exec.url+"/api" {
		t.Fatalf("api binding %+v", b)
	}

	// Starting again finds the same deployment: a plan deploys once.
	again, _, err := r.Start(t.Context(), "org_1", "plan_1")
	if err != nil || again != id {
		t.Fatalf("second start %q %v", again, err)
	}
}

func TestRunner_aVariableWinsOverALink(t *testing.T) {
	exec := &recorder{url: answering(t, http.StatusOK)}
	vars := fakeVariables{"svc:node:apps/web": {"API_URL": "https://api.example.com"}}
	r, _, _ := newRunner(t, webAndAPI(), exec, vars)
	if _, err := deploy(t, r, webAndAPI()); err != nil {
		t.Fatal(err)
	}
	if got := exec.jobs[1].Env["API_URL"]; got != "https://api.example.com" {
		t.Fatalf("API_URL %q", got)
	}
}

func TestRunner_aFailureStopsAndSkipsTheRest(t *testing.T) {
	exec := &recorder{url: answering(t, http.StatusOK), fail: map[string]error{"api": errors.New("cloud build failed: step 0 exited 1")}}
	r, store, plans := newRunner(t, webAndAPI(), exec, fakeVariables{})
	id, err := deploy(t, r, webAndAPI())
	if err != nil {
		t.Fatal(err)
	}
	d, _ := store.GetDeployment(t.Context(), "org_1", id)
	if d.Status != StatusFailed || !strings.Contains(d.Error, "cloud build failed") {
		t.Fatalf("deployment %+v", d)
	}
	if d.Steps[0].Status != StepFailed || d.Steps[1].Status != StepSkipped {
		t.Fatalf("steps %+v", d.Steps)
	}
	if len(plans.bindings) != 0 {
		t.Fatalf("a failed step was bound: %+v", plans.bindings)
	}
}

func TestRunner_aServiceThatDoesntAnswerFails(t *testing.T) {
	exec := &recorder{url: answering(t, http.StatusBadGateway)}
	r, store, _ := newRunner(t, webAndAPI(), exec, fakeVariables{})
	id, err := deploy(t, r, webAndAPI())
	if err != nil {
		t.Fatal(err)
	}
	d, _ := store.GetDeployment(t.Context(), "org_1", id)
	if d.Status != StatusFailed || !strings.Contains(d.Steps[0].Message, "didn't answer properly") {
		t.Fatalf("deployment %+v", d)
	}
}

func TestRunner_aPlanForReviewDeploysNothing(t *testing.T) {
	plan := webAndAPI()
	plan.Executable = false
	r, store, _ := newRunner(t, plan, &recorder{}, fakeVariables{})
	id, steps, err := r.Start(t.Context(), "org_1", "plan_1")
	if err != nil || id != "" || steps != nil || len(store.d) != 0 {
		t.Fatalf("started %q %v %v", id, steps, err)
	}
}

func TestRunner_retryResumesFromTheLastCheckpoint(t *testing.T) {
	exec := &recorder{url: answering(t, http.StatusOK)}
	r, _, _ := newRunner(t, webAndAPI(), exec, fakeVariables{})
	id, _, err := r.Start(t.Context(), "org_1", "plan_1")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.FetchSource(t.Context(), "org_1", id); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(t.Context(), "org_1", id, "service:api", []byte("op-earlier"), func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if string(exec.jobs[0].Resume) != "op-earlier" {
		t.Fatalf("resume %q", exec.jobs[0].Resume)
	}
	// A step that already succeeded isn't run again.
	if err := r.Run(t.Context(), "org_1", id, "service:api", nil, func([]byte) error { return nil }); err != nil || len(exec.jobs) != 1 {
		t.Fatalf("ran again: %d jobs, %v", len(exec.jobs), err)
	}
}

var (
	shaAPI = strings.Repeat("a", 40)
	shaKit = strings.Repeat("c", 40)
	shaWeb = strings.Repeat("b", 40)
)

// threeRepos is webAndAPI built from three repositories: it moves web and
// api, each from its own, and holds kit.
func threeRepos() *planning.Plan {
	plan := webAndAPI()
	plan.Sources = []planning.PlanSource{
		{SourceID: "src_web", Alias: "web", Repository: "acme/web", AnalysisID: "anl_web", CommitSHA: shaWeb, InScope: true},
		{SourceID: "src_kit", Alias: "kit", Repository: "acme/kit", AnalysisID: "anl_kit", CommitSHA: shaKit},
		{SourceID: "src_api", Alias: "api", Repository: "acme/api", AnalysisID: "anl_api", CommitSHA: shaAPI, InScope: true},
	}
	plan.Actions[0].Service.Repo, plan.Actions[0].Service.CommitSHA = "api", shaAPI
	plan.Actions[0].Service.Service = "svc:api:go:services/api"
	plan.Actions[1].Service.Repo, plan.Actions[1].Service.CommitSHA = "web", shaWeb
	plan.Actions[1].Service.Service = "svc:web:node:apps/web"
	return plan
}

func TestRunner_fetchesEachRepositoryItMoves(t *testing.T) {
	r, store, _ := newRunner(t, threeRepos(), &recorder{}, fakeVariables{})
	sources := r.sources.(*fakeSources)
	ctx := t.Context()

	id, _, err := r.Start(ctx, "org_1", "plan_1")
	if err != nil {
		t.Fatal(err)
	}
	d, _ := store.GetDeployment(ctx, "org_1", id)
	if d.CommitSHA != "api@aaaaaaa web@bbbbbbb" {
		t.Fatalf("commit label %q", d.CommitSHA)
	}
	if len(d.Sources) != 2 || d.Sources[0].Alias != "api" || d.Sources[1].Alias != "web" || d.Sources[0].Key != "" {
		t.Fatalf("sources at start %+v", d.Sources)
	}

	if err := r.FetchSource(ctx, "org_1", id); err != nil {
		t.Fatal(err)
	}
	apiKey, webKey := SourceKey("org_1", "proj_1", "api", shaAPI), SourceKey("org_1", "proj_1", "web", shaWeb)
	if !slices.Equal(sources.stored, []string{apiKey, webKey}) || !slices.Equal(sources.repos, []string{"acme/api", "acme/web"}) {
		t.Fatalf("stored %q from %q", sources.stored, sources.repos)
	}
	d, _ = store.GetDeployment(ctx, "org_1", id)
	want := []SourceArtifact{
		{Alias: "api", Repository: "acme/api", CommitSHA: shaAPI, Key: apiKey},
		{Alias: "web", Repository: "acme/web", CommitSHA: shaWeb, Key: webKey},
	}
	if !slices.Equal(d.Sources, want) {
		t.Fatalf("sources %+v, want %+v", d.Sources, want)
	}

	// A retried activity stores nothing more.
	if err := r.FetchSource(ctx, "org_1", id); err != nil {
		t.Fatal(err)
	}
	if len(sources.stored) != 2 {
		t.Fatalf("stored again: %q", sources.stored)
	}
}

func TestRunner_buildsEachServiceFromItsRepository(t *testing.T) {
	exec := &recorder{url: answering(t, http.StatusOK)}
	r, _, _ := newRunner(t, threeRepos(), exec, fakeVariables{})
	if _, err := deploy(t, r, threeRepos()); err != nil {
		t.Fatal(err)
	}
	if exec.read["api"] != "acme/api@"+shaAPI || exec.read["web"] != "acme/web@"+shaWeb {
		t.Fatalf("read %q", exec.read)
	}

	// web built from kit, which the plan holds, has nothing to build from.
	plan := threeRepos()
	plan.Actions[1].Service.Repo = "kit"
	exec = &recorder{url: answering(t, http.StatusOK)}
	r, store, _ := newRunner(t, plan, exec, fakeVariables{})
	id, err := deploy(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := store.GetDeployment(t.Context(), "org_1", id)
	web := d.Step("service:web")
	if d.Status != StatusFailed || web.Status != StepFailed || web.Message != "the deployment has no source stored for kit" {
		t.Fatalf("deployment %+v, web %+v", d, web)
	}
}

func TestRunner_recordsWhatsDeployedOnlyOnSuccess(t *testing.T) {
	ctx := t.Context()
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	r, _, plans := newRunner(t, threeRepos(), &recorder{}, fakeVariables{})
	r.now = func() time.Time { return at }
	id, _, err := r.Start(ctx, "org_1", "plan_1")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Finish(ctx, "org_1", id, ""); err != nil {
		t.Fatal(err)
	}
	want := []planning.DeployedSource{
		{EnvironmentID: "env_1", SourceID: "src_web", AnalysisID: "anl_web", CommitSHA: shaWeb, PlanID: "plan_1", DeployedAt: at},
		{EnvironmentID: "env_1", SourceID: "src_api", AnalysisID: "anl_api", CommitSHA: shaAPI, PlanID: "plan_1", DeployedAt: at},
	}
	if !slices.Equal(plans.deployed, want) {
		t.Fatalf("recorded %+v, want %+v", plans.deployed, want)
	}

	r, _, plans = newRunner(t, threeRepos(), &recorder{}, fakeVariables{})
	id, _, err = r.Start(ctx, "org_1", "plan_1")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Finish(ctx, "org_1", id, "cloud build failed"); err != nil {
		t.Fatal(err)
	}
	if len(plans.deployed) != 0 {
		t.Fatalf("a failed deployment recorded %+v", plans.deployed)
	}
}
