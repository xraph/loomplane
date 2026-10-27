package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/provider"
)

type memStore struct {
	mu             sync.Mutex
	d              map[string]*Deployment
	capacityEvents []CapacityTransition
	capacityAcks   map[string]bool
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

func (m *memStore) ListDeployments(context.Context, string, string, page.Cursor, int) ([]*Deployment, error) {
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
	if s.Status == StepSkipped && (m.d[id].Status == StatusPaused || m.d[id].Status == StatusCancelled) {
		return nil
	}
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

func (m *memStore) Reopen(_ context.Context, _, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.d[id]
	if d.Status != StatusFailed {
		return false, nil
	}
	d.Status, d.Error, d.FinishedAt = StatusRunning, "", nil
	for i := range d.Steps {
		if d.Steps[i].Status == StepFailed || d.Steps[i].Status == StepSkipped {
			d.Steps[i].Status = StepPending
		}
	}
	d.Retries++
	return true, nil
}

type fakePlans struct {
	mu       sync.Mutex
	plan     *planning.Plan
	bindings map[string]*planning.Binding
	deployed []planning.DeployedSource
	// releases are the RecordRelease writes, and single counts the
	// RecordDeployed ones.
	releases []planning.DeployedRelease
	single   int
}

func (f *fakePlans) RecordDeployed(_ context.Context, _ string, d *planning.DeployedSource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.single++
	f.deployed = append(f.deployed, *d)
	return nil
}

func (f *fakePlans) RecordRelease(_ context.Context, _ string, r *planning.DeployedRelease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases = append(f.releases, *r)
	for _, d := range r.Sources {
		f.deployed = append(f.deployed, *d)
	}
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

func (f *fakePlans) SetReplaced(_ context.Context, _, id string, replaced []planning.Binding) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, b := range f.bindings {
		if b.ID == id {
			c := *b
			c.Replaced = slices.Clone(replaced)
			f.bindings[k] = &c
		}
	}
	return nil
}

type fakeCredentials struct{}

func (fakeCredentials) Credential(_ context.Context, _, id string) (*connections.Connection, []byte, error) {
	return &connections.Connection{ID: id, Status: connections.StatusReady}, []byte("secret-" + id), nil
}

type fakeVariables map[string]map[string]string

func (f fakeVariables) Values(_ context.Context, _, _, _, service string) (values map[string]string, secret map[string]bool, err error) {
	values, secret = map[string]string{}, map[string]bool{}
	for k, v := range f[service] {
		values[k] = v
		if strings.HasPrefix(k, "SECRET_") {
			secret[k] = true
		}
	}
	return values, secret, nil
}

// fakeSources stores a tarball whose content is the repository and commit
// it came from, so a test can tell which one a job opened.
type fakeSources struct {
	mu     sync.Mutex
	stored []string
	repos  []string
	files  map[string]string
	// fail makes storing these repositories fail.
	fail map[string]bool
}

func (f *fakeSources) Store(_ context.Context, _, repository, sha, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail[repository] {
		return errors.New("github is down")
	}
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
	mu       sync.Mutex
	url      string
	fail     map[string]error
	jobs     []*Job
	read     map[string]string
	progress func(*Job)
}

func (r *recorder) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	r.mu.Lock()
	r.jobs = append(r.jobs, job)
	err := r.fail[job.Action.Name]
	r.mu.Unlock()
	if r.progress != nil {
		r.progress(job)
	}
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

func (r *recorder) Observe(context.Context, *connections.Connection, []byte, *planning.Binding) (*Observation, error) {
	return &Observation{Exists: true}, nil
}

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

// runAll runs a deployment the way the workflow does.
func runAll(t *testing.T, r *Runner, plan *planning.Plan) (string, error) {
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

	id, err := runAll(t, r, webAndAPI())
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
	if _, err := runAll(t, r, webAndAPI()); err != nil {
		t.Fatal(err)
	}
	if got := exec.jobs[1].Env["API_URL"]; got != "https://api.example.com" {
		t.Fatalf("API_URL %q", got)
	}
}

func TestRunner_aFailureStopsAndSkipsTheRest(t *testing.T) {
	exec := &recorder{url: answering(t, http.StatusOK), fail: map[string]error{"api": errors.New("cloud build failed: step 0 exited 1")}}
	r, store, plans := newRunner(t, webAndAPI(), exec, fakeVariables{})
	id, err := runAll(t, r, webAndAPI())
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
	id, err := runAll(t, r, webAndAPI())
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

func TestRunner_activeRetryClearsThePreviousFinishTime(t *testing.T) {
	exec := &recorder{url: answering(t, http.StatusOK), fail: map[string]error{"api": errors.New("first attempt failed")}}
	r, store, _ := newRunner(t, webAndAPI(), exec, fakeVariables{})
	id, _, err := r.Start(t.Context(), "org_1", "plan_1")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.FetchSource(t.Context(), "org_1", id); err != nil {
		t.Fatal(err)
	}
	if err = r.Run(t.Context(), "org_1", id, "service:api", nil, func([]byte) error { return nil }); err == nil {
		t.Fatal("first attempt succeeded")
	}
	exec.fail = nil
	exec.progress = func(job *Job) {
		job.Progress(StepDeploying, "Retrying")
		d, readErr := store.GetDeployment(t.Context(), "org_1", id)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, step := range d.Steps {
			if step.ActionID == "service:api" && (step.StartedAt == nil || step.FinishedAt != nil) {
				t.Fatalf("active retry timestamps: %+v", step)
			}
		}
	}
	if err = r.Run(t.Context(), "org_1", id, "service:api", nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
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
	if _, err := runAll(t, r, threeRepos()); err != nil {
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
	id, err := runAll(t, r, plan)
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

func TestRunner_recordsEveryMovedSourceInOneWrite(t *testing.T) {
	ctx := t.Context()
	at := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	plan := threeRepos()
	plan.ManifestID, plan.ManifestRevision = "man_5", 5
	r, _, plans := newRunner(t, plan, &recorder{}, fakeVariables{})
	r.now = func() time.Time { return at }
	id, _, err := r.Start(ctx, "org_1", "plan_1")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Finish(ctx, "org_1", id, ""); err != nil {
		t.Fatal(err)
	}
	if plans.single != 0 || len(plans.releases) != 1 {
		t.Fatalf("RecordDeployed %d times, RecordRelease %d times", plans.single, len(plans.releases))
	}
	got := plans.releases[0]
	if got.EnvironmentID != "env_1" || got.PlanID != "plan_1" || got.ManifestID != "man_5" || got.ManifestRevision != 5 || !got.DeployedAt.Equal(at) {
		t.Fatalf("release %+v", got)
	}
	moved := make([]string, 0, len(got.Sources))
	for _, d := range got.Sources {
		moved = append(moved, d.SourceID+"@"+d.CommitSHA)
	}
	if !slices.Equal(moved, []string{"src_web@" + shaWeb, "src_api@" + shaAPI}) {
		t.Fatalf("moved %v", moved)
	}

	// A catalog plan moves no repository and isn't a release of the
	// environment's manifest, so it records nothing.
	catalog := webAndAPI()
	catalog.Catalog, catalog.Sources = true, nil
	r, _, plans = newRunner(t, catalog, &recorder{}, fakeVariables{})
	id, _, err = r.Start(ctx, "org_1", "plan_1")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Finish(ctx, "org_1", id, ""); err != nil {
		t.Fatal(err)
	}
	if len(plans.releases) != 0 || plans.single != 0 {
		t.Fatalf("a catalog plan recorded %+v", plans.releases)
	}
}

func TestRunner_aRetryStoresOnlyTheRepositoriesAFailureMissed(t *testing.T) {
	ctx := t.Context()
	exec := &recorder{url: answering(t, http.StatusOK)}
	r, store, plans := newRunner(t, threeRepos(), exec, fakeVariables{})
	sources := r.sources.(*fakeSources)
	sources.fail = map[string]bool{"acme/web": true}

	id, steps, err := r.Start(ctx, "org_1", "plan_1")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.FetchSource(ctx, "org_1", id); err == nil {
		t.Fatal("stored web while GitHub was down")
	}
	if err = r.Finish(ctx, "org_1", id, "The source couldn't be stored"); err != nil {
		t.Fatal(err)
	}
	if ok, rerr := store.Reopen(ctx, "org_1", id); !ok || rerr != nil {
		t.Fatalf("reopen: %v, %v", ok, rerr)
	}

	// The retry stores web, and api only the once.
	sources.fail = nil
	if err = r.FetchSource(ctx, "org_1", id); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sources.repos, []string{"acme/api", "acme/web"}) {
		t.Fatalf("stored from %q", sources.repos)
	}
	for _, group := range steps {
		for _, action := range group {
			if err = r.Run(ctx, "org_1", id, action, nil, func([]byte) error { return nil }); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = r.Finish(ctx, "org_1", id, ""); err != nil {
		t.Fatal(err)
	}
	if len(plans.deployed) != 2 {
		t.Fatalf("recorded %+v", plans.deployed)
	}
	// Each service is built, named and bound at its own repository's commit.
	for _, j := range exec.jobs {
		want := shaWeb
		if j.Action.Name == "api" {
			want = shaAPI
		}
		if j.CommitSHA != want || j.SourceID != want {
			t.Errorf("%s built at %q, named %q, want %s", j.Action.Name, j.CommitSHA, j.SourceID, want)
		}
	}
	for _, b := range plans.bindings {
		if want := map[string]string{"api": shaAPI, "web": shaWeb}[b.Name]; b.CommitSHA != want {
			t.Errorf("%s bound at %q, want %s", b.Name, b.CommitSHA, want)
		}
	}
}

func TestCommitFor_aServiceThatNamesNoRepository(t *testing.T) {
	svc := planning.Action{Service: &planning.ServiceConfig{}}
	one := &Deployment{CommitSHA: shaAPI, Sources: []SourceArtifact{{CommitSHA: shaAPI}}}
	several := &Deployment{
		CommitSHA: "api@aaaaaaa web@bbbbbbb",
		Sources:   []SourceArtifact{{Alias: "api", CommitSHA: shaAPI}, {Alias: "web", CommitSHA: shaWeb}},
	}
	older := &Deployment{CommitSHA: shaAPI}
	for name, tt := range map[string]struct {
		d    *Deployment
		want string
	}{
		"one repository":                   {one, shaAPI},
		"several repositories":             {several, ""},
		"a deployment from before sources": {older, shaAPI},
	} {
		if got := commitFor(tt.d, svc); got != tt.want {
			t.Errorf("%s: %q, want %q", name, got, tt.want)
		}
	}
}

func TestImageTag_refusesWhatIsntACommit(t *testing.T) {
	job := &Job{Action: planning.Action{Name: "api"}, SourceID: shaAPI}
	if tag, err := imageTag(job); err != nil || tag != shaAPI[:12] {
		t.Fatalf("tag %q, %v", tag, err)
	}
	for _, id := range []string{"", "abc1234"} {
		job.SourceID = id
		if _, err := imageTag(job); err == nil || err.Error() != "api has no commit to build from" {
			t.Errorf("SourceID %q: %v", id, err)
		}
	}
}

// startMove starts a deployment of plan and stores its source, so a test
// can run its one action itself.
func startMove(t *testing.T, r *Runner, plan *planning.Plan) string {
	t.Helper()
	id, _, err := r.Start(t.Context(), plan.OrgID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.FetchSource(t.Context(), plan.OrgID, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRunner_aReplaceStartsTheNewOneThenRemovesTheOldOne(t *testing.T) {
	log := &moveLog{}
	oldExec := &placed{provider: "cloud-run", log: log}
	newExec := &placed{provider: "vercel", log: log, url: answering(t, http.StatusOK)}
	plan := apiMoves()
	was := onCloudRun()
	was.Config = []byte(`{"provider":"cloud-run","service":"svc:go:services/api"}`)
	r, _, plans, _ := moveRunner(t, plan, was, map[string]Executor{"cloud-run": oldExec, "vercel": newExec})
	// The new one is bound before the old one goes, so nothing is left
	// untracked if removing it fails.
	var boundAtRemove *planning.Binding
	oldExec.onRemove = func() { boundAtRemove = plans.bindings["service:api"] }

	if _, err := runAll(t, r, plan); err != nil {
		t.Fatal(err)
	}
	if got, want := log.list(), []string{"apply vercel", "remove cloud-run ext-old"}; !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if boundAtRemove == nil || boundAtRemove.Provider != "vercel" || boundAtRemove.ExternalID != "ext-vercel" {
		t.Fatalf("bound while removing the old one: %+v", boundAtRemove)
	}
	// The old one is removed as it was: its provider, region, external
	// ID and config.
	rm := oldExec.removes[0]
	if b := rm.Binding; b.Provider != "cloud-run" || b.Region != "us-central1" || b.ExternalID != "ext-old" ||
		string(b.Config) != string(was.Config) {
		t.Fatalf("removed %+v", b)
	}
}

func TestRunner_aRetryAfterTheOldOneWouldntGoOnlyRemovesIt(t *testing.T) {
	log := &moveLog{}
	oldExec := &placed{provider: "cloud-run", log: log, removeErr: []error{errors.New("google is down")}}
	newExec := &placed{provider: "vercel", log: log, url: answering(t, http.StatusOK)}
	plan := apiMoves()
	r, store, plans, _ := moveRunner(t, plan, onCloudRun(), map[string]Executor{"cloud-run": oldExec, "vercel": newExec})
	ctx := t.Context()
	id := startMove(t, r, plan)

	var saved []byte
	heartbeat := func(b []byte) error { saved = b; return nil }
	err := r.Run(ctx, "org_1", id, "service:api", nil, heartbeat)
	if err == nil || !strings.Contains(err.Error(), "google is down") {
		t.Fatalf("first attempt: %v", err)
	}
	if b := plans.bindings["service:api"]; b.Provider != "vercel" || b.ExternalID != "ext-vercel" {
		t.Fatalf("binding after the failure %+v", b)
	}
	if ferr := r.Finish(ctx, "org_1", id, err.Error()); ferr != nil {
		t.Fatal(ferr)
	}
	if ok, rerr := store.Reopen(ctx, "org_1", id); !ok || rerr != nil {
		t.Fatalf("reopen: %v %v", ok, rerr)
	}
	// A retry from a new workflow has no heartbeat to go on; what the
	// step recorded is enough.
	if err := r.Run(ctx, "org_1", id, "service:api", nil, heartbeat); err != nil {
		t.Fatal(err)
	}
	want := []string{"apply vercel", "remove cloud-run ext-old", "remove cloud-run ext-old"}
	if got := log.list(); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	d, _ := store.GetDeployment(ctx, "org_1", id)
	if s := d.Step("service:api"); s.Status != StepSucceeded || s.Replaced != nil || s.Message != "Answering at "+newExec.url {
		t.Fatalf("step %+v", s)
	}

	// An activity retry hands Remove the checkpoint it saved.
	log2 := &moveLog{}
	oldExec2 := &placed{provider: "cloud-run", log: log2, removeErr: []error{errors.New("google is down")}}
	newExec2 := &placed{provider: "vercel", log: log2, url: newExec.url}
	r2, _, _, _ := moveRunner(t, plan, onCloudRun(), map[string]Executor{"cloud-run": oldExec2, "vercel": newExec2})
	id2 := startMove(t, r2, plan)
	if err := r2.Run(ctx, "org_1", id2, "service:api", nil, heartbeat); err == nil {
		t.Fatal("the first attempt succeeded")
	}
	if err := r2.Run(ctx, "org_1", id2, "service:api", saved, heartbeat); err != nil {
		t.Fatal(err)
	}
	if got := log2.list(); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if got := string(oldExec2.removes[1].Resume); got != "rm-cloud-run" {
		t.Fatalf("remove resumed from %q", got)
	}
}

func TestRunner_aReplacedOneWithNoExecutorFailsAndKeepsTrackOfIt(t *testing.T) {
	log := &moveLog{}
	newExec := &placed{provider: "vercel", log: log, url: answering(t, http.StatusOK)}
	plan := apiMoves()
	r, store, plans, _ := moveRunner(t, plan, onCloudRun(), map[string]Executor{"vercel": newExec})
	ctx := t.Context()
	id := startMove(t, r, plan)
	heartbeat := func([]byte) error { return nil }

	err := r.Run(ctx, "org_1", id, "service:api", nil, heartbeat)
	if !errors.Is(err, ErrNoExecutor) {
		t.Fatalf("err %v, want ErrNoExecutor", err)
	}
	for _, s := range []string{"api now runs on vercel", "the old one on cloud-run in us-central1 (ext-old) can't be removed", "keeps track of it"} {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("%q doesn't say %q", err, s)
		}
	}
	b := plans.bindings["service:api"]
	if b.Provider != "vercel" || b.ExternalID != "ext-vercel" || len(b.Replaced) != 1 || b.Replaced[0].ExternalID != "ext-old" {
		t.Fatalf("binding %+v", b)
	}
	d, _ := store.GetDeployment(ctx, "org_1", id)
	if s := d.Step("service:api"); s.Status != StepFailed || s.Message != err.Error() {
		t.Fatalf("step %+v", s)
	}

	// Nothing has removed it, so a retry fails the same way, without
	// starting the new one again.
	retry := func() error {
		t.Helper()
		if ferr := r.Finish(ctx, "org_1", id, "failed"); ferr != nil {
			t.Fatal(ferr)
		}
		if ok, rerr := store.Reopen(ctx, "org_1", id); !ok || rerr != nil {
			t.Fatalf("reopen: %v %v", ok, rerr)
		}
		return r.Run(ctx, "org_1", id, "service:api", nil, heartbeat)
	}
	if err := retry(); !errors.Is(err, ErrNoExecutor) {
		t.Fatalf("retry: %v", err)
	}
	// Once the executor is back, a retry removes it.
	oldExec := &placed{provider: "cloud-run", log: log}
	r.executors["cloud-run"] = oldExec
	if err := retry(); err != nil {
		t.Fatal(err)
	}
	if got, want := log.list(), []string{"apply vercel", "remove cloud-run ext-old"}; !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if b := plans.bindings["service:api"]; len(b.Replaced) != 0 {
		t.Fatalf("still replaced %+v", b.Replaced)
	}
}

func TestRunner_aServiceOnAnotherConnectionStartsAgainBeforeTheOldOneGoes(t *testing.T) {
	log := &moveLog{}
	exec := &placed{provider: "cloud-run", log: log, url: answering(t, http.StatusOK)}
	plan := apiMoves()
	plan.Actions[0].Op, plan.Actions[0].Provider, plan.Actions[0].Region = planning.Replace, "cloud-run", "us-central1"
	plan.Actions[0].Service.Provider = "cloud-run"
	was := onCloudRun()
	was.ConnectionID = "conn_old"
	r, _, plans, _ := moveRunner(t, plan, was, map[string]Executor{"cloud-run": exec})

	if _, err := runAll(t, r, plan); err != nil {
		t.Fatal(err)
	}
	if got, want := log.list(), []string{"apply cloud-run", "remove cloud-run ext-old"}; !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if apply := exec.applies[0]; apply.Binding != nil || apply.Replaces == nil || apply.Connection.ID != "conn_g" {
		t.Fatalf("applied through %s with binding %+v, replacing %+v", apply.Connection.ID, apply.Binding, apply.Replaces)
	}
	if rm := exec.removes[0]; rm.Connection.ID != "conn_old" {
		t.Fatalf("removed through %s", rm.Connection.ID)
	}
	// Nothing can reach it again, so nothing keeps waiting for it.
	if b := plans.bindings["service:api"]; b.ConnectionID != "conn_g" || b.ExternalID != "ext-cloud-run" || len(b.Replaced) != 0 {
		t.Fatalf("binding %+v", b)
	}
}

func TestRunner_aServiceOnAnotherConnectionThatsTheSameOneRemovesNothing(t *testing.T) {
	log := &moveLog{}
	// Both connections reach the same Vercel team, so the project is the
	// one already bound.
	exec := &placed{provider: "vercel", log: log, url: answering(t, http.StatusOK), externalID: "prj_1"}
	plan := apiMoves()
	plan.Actions[0].Op = planning.Replace
	was := &planning.Binding{
		ID: "bind_api", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ServiceSubject,
		Name: "api", Provider: "vercel", ExternalID: "prj_1", ConnectionID: "conn_old",
	}
	r, store, plans, _ := moveRunner(t, plan, was, map[string]Executor{"vercel": exec})
	r.credentials = bindingIdentityCredentials{sameAccount: true}

	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := log.list(); !slices.Equal(got, []string{"apply vercel"}) {
		t.Fatalf("events %v", got)
	}
	d, _ := store.GetDeployment(t.Context(), "org_1", id)
	if d.Status != StatusSucceeded {
		t.Fatalf("deployment %s: %s", d.Status, d.Error)
	}
	if b := plans.bindings["service:api"]; b.ConnectionID != "conn_v" || b.ExternalID != "prj_1" {
		t.Fatalf("binding %+v", b)
	}
}

// goneCredentials are fakeCredentials, except for connections that were
// deleted.
type goneCredentials struct {
	fakeCredentials
	gone map[string]bool
}

func (g goneCredentials) Credential(ctx context.Context, orgID, id string) (*connections.Connection, []byte, error) {
	if g.gone[id] {
		return nil, nil, connections.ErrNotFound
	}
	return g.fakeCredentials.Credential(ctx, orgID, id)
}

func TestRunner_aReplacedOneWhoseConnectionIsGoneFailsAndKeepsTheNewOne(t *testing.T) {
	log := &moveLog{}
	exec := &placed{provider: "cloud-run", log: log, url: answering(t, http.StatusOK)}
	plan := apiMoves()
	plan.Actions[0].Op, plan.Actions[0].Provider, plan.Actions[0].Region = planning.Replace, "cloud-run", "us-central1"
	plan.Actions[0].Service.Provider = "cloud-run"
	was := onCloudRun()
	was.ConnectionID = "conn_old"
	r, store, plans, _ := moveRunner(t, plan, was, map[string]Executor{"cloud-run": exec})
	r.credentials = goneCredentials{gone: map[string]bool{"conn_old": true}}

	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := store.GetDeployment(t.Context(), "org_1", id)
	if !strings.Contains(d.Error, "the connection that deployed the old api has been removed") ||
		!strings.Contains(d.Error, "If ext-old on cloud-run in us-central1 still runs, delete it yourself") {
		t.Fatalf("deployment %s: %s", d.Status, d.Error)
	}
	if got := log.list(); !slices.Equal(got, []string{"apply cloud-run"}) {
		t.Fatalf("events %v", got)
	}
	// Nothing can reach it again, so nothing keeps waiting for it.
	if b := plans.bindings["service:api"]; b.ConnectionID != "conn_g" || b.ExternalID != "ext-cloud-run" || len(b.Replaced) != 0 {
		t.Fatalf("binding %+v", b)
	}
}

func TestRunner_aLaterDeploymentRemovesWhatAMoveLeft(t *testing.T) {
	log := &moveLog{}
	oldExec := &placed{provider: "cloud-run", log: log, removeErr: []error{errors.New("google is down")}}
	newExec := &placed{provider: "vercel", log: log, url: answering(t, http.StatusOK)}
	plan := apiMoves()
	r, store, plans, _ := moveRunner(t, plan, onCloudRun(), map[string]Executor{"cloud-run": oldExec, "vercel": newExec})

	first, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := store.GetDeployment(t.Context(), "org_1", first); d.Status != StatusFailed {
		t.Fatalf("first deployment %s", d.Status)
	}
	// The environment records what's left, not just the failed step.
	b := plans.bindings["service:api"]
	if b.ExternalID != "ext-vercel" || len(b.Replaced) != 1 || b.Replaced[0].ExternalID != "ext-old" ||
		b.Replaced[0].ConnectionID != "conn_g" || b.Replaced[0].Region != "us-central1" {
		t.Fatalf("binding %+v", b)
	}

	// Someone plans again instead of retrying. That deployment removes it.
	next := apiMoves()
	next.ID, next.Actions[0].Op = "plan_2", planning.Update
	plans.plan = next
	second, err := runAll(t, r, next)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := store.GetDeployment(t.Context(), "org_1", second); d.Status != StatusSucceeded {
		t.Fatalf("second deployment %s: %s", d.Status, d.Error)
	}
	want := []string{"apply vercel", "remove cloud-run ext-old", "apply vercel", "remove cloud-run ext-old"}
	if got := log.list(); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if rm := oldExec.removes[1]; rm.Connection.ID != "conn_g" || rm.Action.Region != "us-central1" {
		t.Fatalf("removed through %s in %s", rm.Connection.ID, rm.Action.Region)
	}
	if b := plans.bindings["service:api"]; len(b.Replaced) != 0 {
		t.Fatalf("still replaced %+v", b.Replaced)
	}
}

func TestRunner_aRetryNeverRemovesWhatsBound(t *testing.T) {
	log := &moveLog{}
	exec := &placed{provider: "vercel", log: log, url: answering(t, http.StatusOK), externalID: "prj_1"}
	plan := apiMoves()
	plan.Actions[0].Op = planning.Update
	// A failed step recorded that it replaced prj_1 through an older
	// connection, and later deployments moved api back onto it.
	bound := &planning.Binding{
		ID: "bind_api", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ServiceSubject,
		Name: "api", Provider: "vercel", ExternalID: "prj_1", ConnectionID: "conn_v",
		Replaced: []planning.Binding{
			{Subject: planning.ServiceSubject, Name: "api", Provider: "vercel", ExternalID: "prj_1", ConnectionID: "conn_old"},
			{Subject: planning.ServiceSubject, Name: "api", Provider: "vercel", ExternalID: "prj_0", ConnectionID: "conn_old"},
		},
	}
	r, store, plans, _ := moveRunner(t, plan, bound, map[string]Executor{"vercel": exec})
	r.credentials = bindingIdentityCredentials{sameAccount: true}
	ctx := t.Context()
	id := startMove(t, r, plan)
	d, _ := store.GetDeployment(ctx, "org_1", id)
	step := d.Step("service:api")
	step.Status, step.Replaced = StepFailed, &planning.Binding{Provider: "vercel", ExternalID: "prj_1", ConnectionID: "conn_old"}
	if err := store.UpdateStep(ctx, "org_1", id, *step); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(ctx, "org_1", id, "service:api", nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got, want := log.list(), []string{"apply vercel", "remove vercel prj_0"}; !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if b := plans.bindings["service:api"]; b.ExternalID != "prj_1" || len(b.Replaced) != 0 {
		t.Fatalf("binding %+v", b)
	}
}

func TestRunner_deletingAnActionRemovesWhatAMoveLeftFirst(t *testing.T) {
	log := &moveLog{}
	oldExec := &placed{provider: "cloud-run", log: log}
	exec := &placed{provider: "vercel", log: log}
	plan := apiMoves()
	plan.Actions[0].Op, plan.Actions[0].Service = planning.Delete, nil
	bound := &planning.Binding{
		ID: "bind_api", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ServiceSubject,
		Name: "api", Provider: "vercel", ExternalID: "prj_1", ConnectionID: "conn_v",
		Replaced: []planning.Binding{*onCloudRun()},
	}
	r, _, plans, _ := moveRunner(t, plan, bound, map[string]Executor{"cloud-run": oldExec, "vercel": exec})

	if _, err := runAll(t, r, plan); err != nil {
		t.Fatal(err)
	}
	if got, want := log.list(), []string{"remove cloud-run ext-old", "remove vercel prj_1"}; !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if _, ok := plans.bindings["service:api"]; ok {
		t.Fatal("the binding is still there")
	}
}

// A binding from before bindings recorded their connection, whose
// instance another connection deployed, isn't moved by the deployment:
// the plan kept what uses it. The step fails, and the binding gets the
// connection, so the next plan replaces it properly.
func TestRunner_aServiceAnotherConnectionDeployedIsLeftForTheNextPlan(t *testing.T) {
	log := &moveLog{}
	exec := &placed{provider: "cloud-run", log: log, url: answering(t, http.StatusOK),
		applyErr: &ElsewhereError{Name: "api", Instance: "ext-old", ConnectionID: "conn_old", Region: "us-central1", Key: "cloud-run.conn_old.us-central1"}}
	plan := apiMoves()
	plan.Actions[0].Op, plan.Actions[0].Provider, plan.Actions[0].Region = planning.Update, "cloud-run", "us-central1"
	plan.Actions[0].Service.Provider = "cloud-run"
	was := onCloudRun()
	was.ConnectionID = ""
	r, store, plans, _ := moveRunner(t, plan, was, map[string]Executor{"cloud-run": exec})

	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := store.GetDeployment(t.Context(), "org_1", id)
	if d.Status != StatusFailed || !strings.Contains(d.Error, "has recorded that on its binding: plan again") {
		t.Fatalf("deployment %s: %s", d.Status, d.Error)
	}
	b := plans.bindings["service:api"]
	if b.ConnectionID != "conn_old" || b.ExternalID != "ext-old" || b.Region != "us-central1" || len(b.Replaced) != 0 {
		t.Fatalf("binding %+v", b)
	}
	if got := log.list(); !slices.Equal(got, []string{"apply cloud-run"}) {
		t.Fatalf("events %v", got)
	}
}

// What an earlier move left is the plan's to warn about. Failing to
// remove it on a later deployment doesn't fail that deployment, so a
// permanent failure, such as a revoked permission, can't block every
// deployment of the action from then on.
func TestRunner_aLaterDeploymentThatCantRemoveWhatAMoveLeftWarns(t *testing.T) {
	log := &moveLog{}
	oldExec := &placed{provider: "cloud-run", log: log, removeErr: []error{errors.New("permission denied"), errors.New("permission denied")}}
	newExec := &placed{provider: "vercel", log: log, url: answering(t, http.StatusOK)}
	plan := apiMoves()
	plan.Actions[0].Op = planning.Update
	bound := &planning.Binding{
		ID: "bind_api", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ServiceSubject,
		Name: "api", Provider: "vercel", ExternalID: "ext-vercel", ConnectionID: "conn_v",
		Replaced: []planning.Binding{*onCloudRun()},
	}
	r, store, plans, _ := moveRunner(t, plan, bound, map[string]Executor{"cloud-run": oldExec, "vercel": newExec})

	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := store.GetDeployment(t.Context(), "org_1", id)
	s := d.Step("service:api")
	if d.Status != StatusSucceeded || s.Status != StepSucceeded || len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "permission denied") {
		t.Fatalf("deployment %s: %s, step %+v", d.Status, d.Error, s)
	}
	if b := plans.bindings["service:api"]; len(b.Replaced) != 1 || b.Replaced[0].ExternalID != "ext-old" {
		t.Fatalf("binding %+v", b)
	}

	// Deleting the action warns too, and says the old one is yours now:
	// the binding that tracked it goes.
	del := apiMoves()
	del.ID, del.Actions[0].Op, del.Actions[0].Service = "plan_2", planning.Delete, nil
	plans.plan = del
	second, err := runAll(t, r, del)
	if err != nil {
		t.Fatal(err)
	}
	d, _ = store.GetDeployment(t.Context(), "org_1", second)
	s = d.Step("service:api")
	want := "remove the old api on cloud-run in us-central1: permission denied. Deleting api stops Loomplane tracking the old one, so if ext-old on cloud-run in us-central1 still runs, delete it yourself"
	if d.Status != StatusSucceeded || len(s.Warnings) != 1 || s.Warnings[0] != want {
		t.Fatalf("deleting: %s: %s, warnings %q", d.Status, d.Error, s.Warnings)
	}
	if _, ok := plans.bindings["service:api"]; ok {
		t.Fatal("the binding is still there")
	}
	if got := log.list(); got[len(got)-1] != "remove vercel ext-vercel" {
		t.Fatalf("events %v", got)
	}
}

// A delete goes through the connection that deployed what it deletes,
// not the one the plan deploys with now, and needs none from the plan.
func TestRunner_aDeleteGoesThroughTheBindingsConnection(t *testing.T) {
	log := &moveLog{}
	exec := &placed{provider: "cloud-run", log: log}
	plan := apiMoves()
	plan.Actions[0].Op, plan.Actions[0].Provider, plan.Actions[0].Region, plan.Actions[0].Service = planning.Delete, "cloud-run", "us-central1", nil
	plan.Connections = map[string]string{"vercel": "conn_v"}
	was := onCloudRun()
	was.ConnectionID = "conn_old"
	r, store, plans, _ := moveRunner(t, plan, was, map[string]Executor{"cloud-run": exec})

	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := store.GetDeployment(t.Context(), "org_1", id); d.Status != StatusSucceeded {
		t.Fatalf("deployment %s: %s", d.Status, d.Error)
	}
	if rm := exec.removes[0]; rm.Connection.ID != "conn_old" || string(rm.Credential) != "secret-conn_old" {
		t.Fatalf("removed with %s %q", rm.Connection.ID, rm.Credential)
	}
	if _, ok := plans.bindings["service:api"]; ok {
		t.Fatal("the binding is still there")
	}
}

// A binding from before bindings recorded their connection is deleted
// through the plan's, and fails, saying why, when the plan names none.
func TestRunner_aLegacyDeleteGoesThroughThePlansConnection(t *testing.T) {
	log := &moveLog{}
	exec := &placed{provider: "cloud-run", log: log}
	plan := apiMoves()
	plan.Actions[0].Op, plan.Actions[0].Provider, plan.Actions[0].Region, plan.Actions[0].Service = planning.Delete, "cloud-run", "us-central1", nil
	was := onCloudRun()
	was.ConnectionID = ""
	r, store, plans, _ := moveRunner(t, plan, was, map[string]Executor{"cloud-run": exec})

	if _, err := runAll(t, r, plan); err != nil {
		t.Fatal(err)
	}
	if rm := exec.removes[0]; rm.Connection.ID != "conn_g" {
		t.Fatalf("removed with %s", rm.Connection.ID)
	}

	again := apiMoves()
	again.ID, again.Actions = "plan_2", plan.Actions
	again.Connections = map[string]string{"vercel": "conn_v"}
	plans.plan = again
	plans.bindings["service:api"] = &planning.Binding{
		ID: "bind_api", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ServiceSubject,
		Name: "api", Provider: "cloud-run", Region: "us-central1", ExternalID: "ext-old",
	}
	id, err := runAll(t, r, again)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := store.GetDeployment(t.Context(), "org_1", id)
	if d.Status != StatusFailed || !strings.Contains(d.Error, "the plan names no gcp connection to delete it through") {
		t.Fatalf("deployment %s: %s", d.Status, d.Error)
	}
	if _, ok := plans.bindings["service:api"]; !ok {
		t.Fatal("the binding went with nothing removed")
	}
}

// A delete whose connection has been removed can't reach what it deletes,
// and failing would fail every deployment from then on, so it stops
// tracking it, forgets what's kept for it, and says it's yours.
func TestRunner_aDeleteWhoseConnectionIsGoneStopsTrackingIt(t *testing.T) {
	log := &moveLog{}
	exec := &placed{provider: "cloud-sql", log: log}
	plan, was := dbMoves()
	plan.Actions[0].Op, plan.Actions[0].Provider, plan.Actions[0].Region, plan.Actions[0].Resource = planning.Delete, was.Provider, was.Region, nil
	was.ConnectionID = "conn_old"
	r, store, plans, secrets := moveRunner(t, plan, was, map[string]Executor{was.Provider: exec})
	r.credentials = goneCredentials{gone: map[string]bool{"conn_old": true}}
	key := resourceKey("org_1", "env_1", "db", ResourceURL)
	secrets.m[key] = []byte("postgres://old")

	// No executor runs, so nothing checkpoints, which runAll insists on.
	ctx := t.Context()
	id, _, err := r.Start(ctx, plan.OrgID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.FetchSource(ctx, plan.OrgID, id); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, plan.OrgID, id, "resource:db", nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(ctx, plan.OrgID, id, ""); err != nil {
		t.Fatal(err)
	}
	d, _ := store.GetDeployment(ctx, "org_1", id)
	s := d.Step("resource:db")
	if d.Status != StatusSucceeded || len(s.Warnings) != 1 ||
		!strings.Contains(s.Warnings[0], "the connection that deployed db has been removed") ||
		!strings.Contains(s.Warnings[0], "delete it yourself") {
		t.Fatalf("deployment %s: %s, warnings %q", d.Status, d.Error, s.Warnings)
	}
	if got := log.list(); len(got) != 0 {
		t.Fatalf("events %v", got)
	}
	if _, ok := plans.bindings["resource:db"]; ok {
		t.Fatal("the binding is still there")
	}
	if _, ok := secrets.m[key]; ok {
		t.Fatal("what was kept for db is still there")
	}
}

// Deleting a database forgets every credential kept for it, the
// administrator's password of one Loomplane runs itself included.
func TestRunner_deletingADatabaseForgetsEveryCredential(t *testing.T) {
	log := &moveLog{}
	exec := &placed{provider: "cloud-sql", log: log}
	plan, was := dbMoves()
	plan.Actions[0].Op, plan.Actions[0].Provider, plan.Actions[0].Region, plan.Actions[0].Resource = planning.Delete, was.Provider, was.Region, nil
	r, store, _, secrets := moveRunner(t, plan, was, map[string]Executor{was.Provider: exec})
	for _, field := range resourceCredentialFields {
		secrets.m[resourceKey("org_1", "env_1", "db", field)] = []byte("secret")
	}

	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := store.GetDeployment(t.Context(), "org_1", id); d.Status != StatusSucceeded {
		t.Fatalf("deployment %s: %s", d.Status, d.Error)
	}
	for _, field := range resourceCredentialFields {
		if _, ok := secrets.m[resourceKey("org_1", "env_1", "db", field)]; ok {
			t.Errorf("db's %s is still kept", field)
		}
	}
}

// projectSQL provisions databases the way Cloud SQL does: in the project
// the job's connection reaches, under cloudSQLInstance's name, creating
// one when that project has none by that name.
type projectSQL struct {
	created []string
	have    map[string]bool
}

func (p *projectSQL) Apply(_ context.Context, job *Job) (*Outcome, error) {
	if err := job.Checkpoint([]byte("op")); err != nil {
		return nil, err
	}
	project, region := job.Connection.ID, job.Action.Resource.Region
	ext := project + ":" + region + ":" + cloudSQLInstance(job)
	if !p.have[ext] {
		p.have[ext] = true
		p.created = append(p.created, ext)
	}
	return &Outcome{ExternalID: ext}, nil
}

func (p *projectSQL) Remove(context.Context, *Job, *planning.Binding) error { return nil }

func (p *projectSQL) Observe(context.Context, *connections.Connection, []byte, *planning.Binding) (*Observation, error) {
	return &Observation{Exists: true}, nil
}

func TestRunner_aResourceOnAnotherConnectionIsntProvisionedAgain(t *testing.T) {
	sql := &projectSQL{have: map[string]bool{"conn_old:us-central1:lp-db": true}}
	plan, was := dbMoves()
	plan.Actions[0].Op, plan.Actions[0].Region, plan.Actions[0].Resource.Region = planning.Update, "us-central1", "us-central1"
	was.ConnectionID, was.ExternalID = "conn_old", "conn_old:us-central1:lp-db"
	r, store, plans, _ := moveRunner(t, plan, was, map[string]Executor{"cloud-sql": sql})

	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	// Applied through the plan's connection, the binding would have
	// started an empty lp-db in that project.
	if len(sql.created) > 0 {
		t.Fatalf("provisioned %v", sql.created)
	}
	d, _ := store.GetDeployment(t.Context(), "org_1", id)
	if d.Status != StatusFailed || !strings.Contains(d.Error, "doesn't move data between connections") {
		t.Fatalf("deployment %s: %s", d.Status, d.Error)
	}
	if b := plans.bindings["resource:db"]; b.ConnectionID != "conn_old" || b.ExternalID != "conn_old:us-central1:lp-db" {
		t.Fatalf("binding %+v", b)
	}
}

// A resource deleted outside Loomplane has no data to leave behind, so
// one another connection provisioned is created again through this one,
// as the planner plans it.
func TestRunner_aMissingResourceOnAnotherConnectionIsCreatedAgain(t *testing.T) {
	sql := &projectSQL{have: map[string]bool{}}
	plan, was := dbMoves()
	plan.Actions[0].Op, plan.Actions[0].Region, plan.Actions[0].Resource.Region = planning.Create, "us-central1", "us-central1"
	was.ConnectionID, was.ExternalID, was.Drift = "conn_old", "conn_old:us-central1:lp-db", planning.DriftMissing
	r, store, plans, _ := moveRunner(t, plan, was, map[string]Executor{"cloud-sql": sql})

	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := store.GetDeployment(t.Context(), "org_1", id); d.Status != StatusSucceeded {
		t.Fatalf("deployment %s: %s", d.Status, d.Error)
	}
	if b := plans.bindings["resource:db"]; b.ConnectionID != "conn_g" || b.ExternalID != "conn_g:us-central1:lp-db" {
		t.Fatalf("binding %+v", b)
	}
}

// A retry runs the plan it was made from. After an executor recorded the
// connection that deployed what's bound, that plan still updates it, and
// moving it to this connection would give it an address nothing the plan
// keeps would get. So the retry deploys and removes nothing, and says to
// plan again.
func TestRunner_aRetryAfterTheConnectionIsRecordedMovesNothing(t *testing.T) {
	log := &moveLog{}
	exec := &placed{provider: "cloud-run", log: log, url: answering(t, http.StatusOK), externalID: "ext-new",
		applyErr: &ElsewhereError{Name: "api", Instance: "ext-old", ConnectionID: "conn_old", Region: "us-central1", Key: "cloud-run.conn_old.us-central1", Recorded: true}}
	plan := apiMoves()
	plan.Actions[0].Op, plan.Actions[0].Provider, plan.Actions[0].Region = planning.Update, "cloud-run", "us-central1"
	plan.Actions[0].Service.Provider = "cloud-run"
	was := onCloudRun()
	was.ConnectionID = ""
	r, store, plans, _ := moveRunner(t, plan, was, map[string]Executor{"cloud-run": exec})
	ctx := t.Context()
	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if b := plans.bindings["service:api"]; b.ConnectionID != "conn_old" {
		t.Fatalf("binding %+v", b)
	}

	exec.applyErr = nil
	if ferr := r.Finish(ctx, "org_1", id, "failed"); ferr != nil {
		t.Fatal(ferr)
	}
	if ok, rerr := store.Reopen(ctx, "org_1", id); !ok || rerr != nil {
		t.Fatalf("reopen: %v %v", ok, rerr)
	}
	err = r.Run(ctx, "org_1", id, "service:api", nil, func([]byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "this plan doesn't replace it") || !strings.Contains(err.Error(), "Plan again") {
		t.Fatalf("retry: %v", err)
	}
	if got := log.list(); !slices.Equal(got, []string{"apply cloud-run"}) {
		t.Fatalf("events %v", got)
	}
	if b := plans.bindings["service:api"]; b.ExternalID != "ext-old" || b.ConnectionID != "conn_old" || len(b.Replaced) != 0 {
		t.Fatalf("binding %+v", b)
	}
}

// A Create over a binding on another connection is planned: what the
// binding named was deleted outside Loomplane, and the planner deploys
// again what uses it. So the runner creates it through this connection
// and clears out the old one, which is already gone.
func TestRunner_aServiceDeletedOnAnotherConnectionIsCreatedAgain(t *testing.T) {
	log := &moveLog{}
	exec := &placed{provider: "cloud-run", log: log, url: answering(t, http.StatusOK), externalID: "ext-new"}
	plan := apiMoves()
	plan.Actions[0].Op, plan.Actions[0].Provider, plan.Actions[0].Region = planning.Create, "cloud-run", "us-central1"
	plan.Actions[0].Service.Provider = "cloud-run"
	was := onCloudRun()
	was.ConnectionID, was.Drift = "conn_old", planning.DriftMissing
	r, store, plans, _ := moveRunner(t, plan, was, map[string]Executor{"cloud-run": exec})
	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := store.GetDeployment(t.Context(), "org_1", id); d.Status != StatusSucceeded {
		t.Fatalf("deployment %s: %s", d.Status, d.Error)
	}
	if b := plans.bindings["service:api"]; b.ExternalID != "ext-new" || b.ConnectionID == "conn_old" {
		t.Fatalf("binding %+v", b)
	}
	if got := log.list(); !slices.Equal(got, []string{"apply cloud-run", "remove cloud-run ext-old"}) {
		t.Fatalf("events %v", got)
	}

	// The same Create over a binding that isn't missing wasn't planned as
	// a move, so the runner refuses it.
	log2 := &moveLog{}
	exec2 := &placed{provider: "cloud-run", log: log2, url: answering(t, http.StatusOK), externalID: "ext-new"}
	live := onCloudRun()
	live.ConnectionID = "conn_old"
	r2, _, _, _ := moveRunner(t, plan, live, map[string]Executor{"cloud-run": exec2})
	if _, err := runAll(t, r2, plan); err != nil {
		t.Fatal(err)
	}
	if got := log2.list(); len(got) != 0 {
		t.Fatalf("events %v", got)
	}
}

// binderHooks is a kind's hooks that issue a value per consuming service.
type binderHooks struct {
	NoHooks
	mu         sync.Mutex
	bound      []Consumer
	released   []Consumer
	releaseErr error
}

func (h *binderHooks) BindConsumer(_ context.Context, c Consumer) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.bound = append(h.bound, c)
	return "key-for-" + c.Service, nil
}

func (h *binderHooks) ReleaseConsumer(_ context.Context, c Consumer) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.released = append(h.released, c)
	return h.releaseErr
}

// consumerLink is the variable a service gets from the per-consumer
// output "key" of the composed resource app.
var consumerLink = planning.EnvLink{Key: "SAMPLE_KEY", From: "app", Subject: planning.ResourceSubject, Field: "consumer:key"}

// consumerApp is the composed resource app, of a test kind with a
// per-consumer output.
func consumerApp(t *testing.T) *planning.Binding {
	t.Helper()
	raw, err := json.Marshal(planning.ResourceConfig{Provider: "server", Kind: "sample", Composition: &planning.CompositionConfig{
		Kind: "sample", Outputs: []recipes.Output{{Name: "url", EnvKey: "SAMPLE_URL"}, {Name: "key", EnvKey: "SAMPLE_KEY", PerConsumer: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &planning.Binding{ID: "bind_app", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ResourceSubject,
		Name: "app", Provider: "server", ExternalID: "catalog/app", Config: raw}
}

// consumerService is a private service on Cloud Run with env.
func consumerService(op planning.Op, name string, env ...planning.EnvLink) planning.Action {
	a := planning.Action{ID: "service:" + name, Op: op, Subject: planning.ServiceSubject, Name: name, Provider: "cloud-run", Step: 1}
	if op != planning.Delete {
		a.Service = &planning.ServiceConfig{Provider: "cloud-run", Service: "svc:go:services/" + name, Env: env}
	}
	return a
}

// consumerRunner is a runner for plan, with the composed resource app and
// any other bindings bound, and hooks serving the kind sample.
func consumerRunner(t *testing.T, plan *planning.Plan, hooks CatalogHooks, others ...*planning.Binding) (*Runner, *memStore, *fakePlans, *recorder) {
	t.Helper()
	exec := &recorder{url: answering(t, http.StatusOK)}
	r, store, plans, _ := moveRunner(t, plan, consumerApp(t), map[string]Executor{"cloud-run": exec})
	for _, b := range others {
		plans.bindings[string(b.Subject)+":"+b.Name] = b
	}
	r.catalogHooks = map[string]CatalogHooks{"sample": hooks}
	return r, store, plans, exec
}

// boundService is a service already deployed with env.
func boundService(t *testing.T, name string, env ...planning.EnvLink) *planning.Binding {
	t.Helper()
	raw, err := json.Marshal(planning.ServiceConfig{Provider: "cloud-run", Service: "svc:go:services/" + name, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	return &planning.Binding{ID: "bind_" + name, OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ServiceSubject,
		Name: name, Provider: "cloud-run", ExternalID: "ext-" + name, ConnectionID: "conn_g", Config: raw}
}

func consumerPlan(actions ...planning.Action) *planning.Plan {
	plan := webAndAPI()
	plan.Actions = actions
	return plan
}

func TestPerConsumerOutputIsIssuedPerService(t *testing.T) {
	plan := consumerPlan(consumerService(planning.Create, "web", consumerLink), consumerService(planning.Create, "jobs", consumerLink))
	plan.Actions[1].Step = 2
	hooks := &binderHooks{}
	r, store, _, exec := consumerRunner(t, plan, hooks)

	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := store.GetDeployment(t.Context(), "org_1", id); d.Status != StatusSucceeded {
		t.Fatalf("deployment %s: %s", d.Status, d.Error)
	}
	if len(hooks.bound) != 2 {
		t.Fatalf("bound %d consumers: %+v", len(hooks.bound), hooks.bound)
	}
	for i, service := range []string{"web", "jobs"} {
		c := hooks.bound[i]
		channel, address := c.Channel, c.Address
		c.Channel, c.Address = nil, nil
		want := Consumer{OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Environment: "production", Resource: "app", Kind: "sample", Output: "key", Service: service}
		if !reflect.DeepEqual(c, want) || channel == nil || address == nil {
			t.Fatalf("consumer %d is %+v, channel %v; want %+v with a channel", i, c, channel != nil, want)
		}
		job := exec.jobs[i]
		if job.Action.Name != service || job.Env["SAMPLE_KEY"] != "key-for-"+service || !job.Secrets["SAMPLE_KEY"] {
			t.Fatalf("%s got %q, secret %v", service, job.Env["SAMPLE_KEY"], job.Secrets["SAMPLE_KEY"])
		}
	}
}

func TestPerConsumerOutputWithoutBinderFails(t *testing.T) {
	plan := consumerPlan(consumerService(planning.Create, "web", consumerLink))
	r, store, plans, exec := consumerRunner(t, plan, NoHooks{})

	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := store.GetDeployment(t.Context(), "org_1", id)
	if d.Status != StatusFailed || !strings.Contains(d.Error, "sample has no consumer binder for output key") {
		t.Fatalf("deployment %s: %q", d.Status, d.Error)
	}
	if len(exec.jobs) != 0 || plans.bindings["service:web"] != nil {
		t.Fatalf("web was deployed anyway: %d jobs", len(exec.jobs))
	}
}

// runStep runs one action of plan the way the workflow does, without the
// checkpoint an executor that deploys nothing never saves.
func runStep(t *testing.T, r *Runner, plan *planning.Plan, store *memStore, action string) *Step {
	t.Helper()
	ctx := t.Context()
	id := startMove(t, r, plan)
	if err := r.Run(ctx, plan.OrgID, id, action, nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(ctx, plan.OrgID, id, ""); err != nil {
		t.Fatal(err)
	}
	d, _ := store.GetDeployment(ctx, plan.OrgID, id)
	return d.Step(action)
}

func TestRemovingConsumerReleasesItsValue(t *testing.T) {
	plan := consumerPlan(consumerService(planning.Delete, "web"))
	hooks := &binderHooks{}
	r, store, plans, _ := consumerRunner(t, plan, hooks, boundService(t, "web", consumerLink), boundService(t, "jobs", consumerLink))

	step := runStep(t, r, plan, store, "service:web")
	if len(hooks.released) != 1 || hooks.released[0].Service != "web" || hooks.released[0].Resource != "app" || hooks.released[0].Output != "key" || hooks.released[0].Kind != "sample" {
		t.Fatalf("released %+v", hooks.released)
	}
	if len(step.Warnings) != 0 || len(hooks.bound) != 0 {
		t.Fatalf("warnings %q, bound %+v", step.Warnings, hooks.bound)
	}
	if plans.bindings["service:web"] != nil || plans.bindings["service:jobs"] == nil {
		t.Fatalf("bindings %v", plans.bindings)
	}
}

func TestDroppingTheNeedReleasesItsValue(t *testing.T) {
	plan := consumerPlan(consumerService(planning.Update, "web"), consumerService(planning.Update, "jobs", consumerLink))
	plan.Actions[1].Step = 2
	hooks := &binderHooks{}
	r, store, plans, _ := consumerRunner(t, plan, hooks, boundService(t, "web", consumerLink), boundService(t, "jobs", consumerLink))

	runStep(t, r, plan, store, "service:web")
	if len(hooks.released) != 1 || hooks.released[0].Service != "web" {
		t.Fatalf("released %+v", hooks.released)
	}
	if plans.bindings["service:web"] == nil {
		t.Fatal("web is gone")
	}
	// A service that still needs it keeps its value.
	hooks.released = nil
	runStep(t, r, plan, store, "service:jobs")
	if len(hooks.released) != 0 || len(hooks.bound) != 1 || hooks.bound[0].Service != "jobs" {
		t.Fatalf("released %+v, bound %+v", hooks.released, hooks.bound)
	}
}

func TestFailingToReleaseAConsumerWarns(t *testing.T) {
	plan := consumerPlan(consumerService(planning.Delete, "web"))
	hooks := &binderHooks{releaseErr: errors.New("fabriq is down")}
	r, store, plans, _ := consumerRunner(t, plan, hooks, boundService(t, "web", consumerLink))

	step := runStep(t, r, plan, store, "service:web")
	if len(step.Warnings) != 1 || !strings.Contains(step.Warnings[0], "web") || !strings.Contains(step.Warnings[0], "fabriq is down") {
		t.Fatalf("warnings %q", step.Warnings)
	}
	if plans.bindings["service:web"] != nil {
		t.Fatal("the binding is still there")
	}
}

// releaseLog is a release observer that records each release, with how
// long it had, and fails with err.
type releaseLog struct {
	mu       sync.Mutex
	releases []Release
	budgets  []time.Duration
	err      error
}

func (l *releaseLog) Released(ctx context.Context, r Release) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releases = append(l.releases, r)
	if deadline, ok := ctx.Deadline(); ok {
		l.budgets = append(l.budgets, time.Until(deadline))
	}
	return l.err
}

func (l *releaseLog) all() []Release {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.releases)
}

// observerFunc is a ReleaseObserver from a function.
type observerFunc func(ctx context.Context, r Release) error

func (f observerFunc) Released(ctx context.Context, r Release) error { return f(ctx, r) }

// cacheAction is the resource cache on Cloud Run, as a plan has it.
func cacheAction(op planning.Op) planning.Action {
	a := planning.Action{ID: "resource:cache", Op: op, Subject: planning.ResourceSubject, Name: "cache", Provider: "cloud-run", Step: 1}
	if op != planning.Delete {
		a.Resource = &planning.ResourceConfig{Provider: "cloud-run", Kind: "redis"}
	}
	return a
}

func TestReleaseObserverNeverFailsTheDeployment(t *testing.T) {
	plan := consumerPlan(cacheAction(planning.Create), consumerService(planning.Create, "web"))
	plan.Actions[1].Step = 2
	failing := &releaseLog{err: errors.New("the catalog is down")}
	fine := &releaseLog{}
	r, store, plans, _ := consumerRunner(t, plan, NoHooks{})
	// What an observer does with its binding changes nothing else.
	scribbler := observerFunc(func(_ context.Context, rel Release) error {
		for i := range rel.Binding.Config {
			rel.Binding.Config[i] = 'x'
		}
		rel.Binding.Name = "scribbled"
		return nil
	})
	r.observers = []ReleaseObserver{scribbler, failing, fine}

	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := store.GetDeployment(t.Context(), "org_1", id)
	if d.Status != StatusSucceeded {
		t.Fatalf("deployment %s: %s", d.Status, d.Error)
	}
	web := d.Step("service:web")
	if web.Status != StepSucceeded || !slices.Equal(web.Warnings, []string{"web: the catalog is down"}) {
		t.Fatalf("web step %s, warnings %q", web.Status, web.Warnings)
	}
	if b := plans.bindings["service:web"]; b.Name != "web" || !json.Valid(b.Config) {
		t.Fatalf("an observer changed the bound web: %s %q", b.Name, b.Config)
	}
	if cache := d.Step("resource:cache"); len(cache.Warnings) != 0 {
		t.Fatalf("cache warnings %q", cache.Warnings)
	}
	for _, observer := range []*releaseLog{failing, fine} {
		releases := observer.all()
		if len(releases) != 1 {
			t.Fatalf("released %d times: %+v", len(releases), releases)
		}
		got := releases[0]
		if got.OrgID != "org_1" || got.ProjectID != "proj_1" || got.EnvironmentID != "env_1" || got.Removed || got.Binding == nil || got.Binding.Name != "web" || got.Binding.Subject != planning.ServiceSubject || got.Binding.ExternalID != "ext-web" {
			t.Fatalf("release %+v", got)
		}
		if got.Binding.ID != plans.bindings["service:web"].ID {
			t.Fatalf("released binding %s, bound %s", got.Binding.ID, plans.bindings["service:web"].ID)
		}
		if len(observer.budgets) != 1 || observer.budgets[0] > 10*time.Second || observer.budgets[0] < 9*time.Second {
			t.Fatalf("observer had %v", observer.budgets)
		}
	}
}

func TestRemovedServiceNotifiesObservers(t *testing.T) {
	plan := consumerPlan(consumerService(planning.Delete, "web"), cacheAction(planning.Delete))
	observer := &releaseLog{}
	cache := &planning.Binding{ID: "bind_cache", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ResourceSubject, Name: "cache", Provider: "cloud-run", ExternalID: "ext-cache", ConnectionID: "conn_g", Config: json.RawMessage(`{}`)}
	r, store, plans, _ := consumerRunner(t, plan, NoHooks{}, boundService(t, "web"), cache)
	r.observers = []ReleaseObserver{observerFunc(func(ctx context.Context, rel Release) error {
		// The binding is gone by the time observers hear of it.
		if b := plans.bindings["service:web"]; b != nil {
			t.Error("observers ran before the binding was deleted")
		}
		return observer.Released(ctx, rel)
	})}

	step := runStep(t, r, plan, store, "service:web")
	releases := observer.all()
	if len(releases) != 1 || !releases[0].Removed || releases[0].Binding == nil || releases[0].Binding.ID != "bind_web" || releases[0].OrgID != "org_1" || releases[0].EnvironmentID != "env_1" {
		t.Fatalf("releases %+v", releases)
	}
	if len(step.Warnings) != 0 {
		t.Fatalf("warnings %q", step.Warnings)
	}
	runStep(t, r, plan, store, "resource:cache")
	if plans.bindings["resource:cache"] != nil || len(observer.all()) != 1 {
		t.Fatalf("removing a resource: %d releases", len(observer.all()))
	}
}

func TestConsumerCarriesOptionsAndAddress(t *testing.T) {
	link := consumerLink
	link.Options = map[string]string{"tenant": "acme"}
	plan := consumerPlan(consumerService(planning.Create, "web", link))
	hooks := &binderHooks{}
	r, store, _, _ := consumerRunner(t, plan, hooks)
	if err := r.resources.Put(t.Context(), ResourceKey("org_1", "env_1", "app--server", "address"), []byte(`{"api":"http://lp-inst-server:8080","admin":"http://lp-inst-server:9090"}`)); err != nil {
		t.Fatal(err)
	}
	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := store.GetDeployment(t.Context(), "org_1", id); d.Status != StatusSucceeded {
		t.Fatalf("deployment %s: %s", d.Status, d.Error)
	}
	if len(hooks.bound) != 1 || !maps.Equal(hooks.bound[0].Options, map[string]string{"tenant": "acme"}) {
		t.Fatalf("bound %+v", hooks.bound)
	}
	c := hooks.bound[0]
	// The consumer gets its own copy of the options.
	c.Options["tenant"] = "changed"
	if plan.Actions[0].Service.Env[0].Options["tenant"] != "acme" {
		t.Fatal("a binder can change the plan's options")
	}
	address, err := c.Address("server", "api")
	if err != nil || address.String() != "http://lp-inst-server:8080" {
		t.Fatalf("address %v: %v", address, err)
	}
	if _, err = c.Address("server", "metrics"); err == nil {
		t.Fatal("an undeclared port has an address")
	}
	if _, err = c.Address("worker", "api"); err == nil {
		t.Fatal("a component that hasn't started has an address")
	}
}

func TestReleaseCopiesTheBinding(t *testing.T) {
	at := time.Now()
	b := &planning.Binding{Name: "web", Config: json.RawMessage(`{"a":1}`), ObservedAt: &at,
		Replaced: []planning.Binding{{Name: "web", ExternalID: "old", Config: json.RawMessage(`{"b":2}`)}}}
	c := cloneBinding(b)
	c.Config[2] = 'z'
	c.Replaced[0].Config[2] = 'z'
	c.Replaced[0].ExternalID = "changed"
	*c.ObservedAt = at.Add(time.Hour)
	if string(b.Config) != `{"a":1}` || string(b.Replaced[0].Config) != `{"b":2}` || b.Replaced[0].ExternalID != "old" || !b.ObservedAt.Equal(at) {
		t.Fatalf("the copy shares with the binding: %+v", b)
	}
}

// revisioned is an executor whose outcome names a digest reference and a
// provider revision, as DigitalOcean's deployment ID is.
type revisioned struct{ *recorder }

func (r revisioned) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	out, err := r.recorder.Apply(ctx, job)
	if out != nil {
		out.Image, out.Revision = "i@sha256:"+strings.Repeat("0", 64), "dep_do_9"
		job.Warn(errors.New(job.Action.Name + " warned while it deployed"))
	}
	return out, err
}

// The step keeps what the executor deployed, and what it warned about.
func TestRunner_recordsTheRevisionOnTheStep(t *testing.T) {
	exec := revisioned{&recorder{url: answering(t, http.StatusOK)}}
	r, store, _ := newRunner(t, webAndAPI(), exec, nil)
	id, err := runAll(t, r, webAndAPI())
	if err != nil {
		t.Fatal(err)
	}
	d, _ := store.GetDeployment(t.Context(), "org_1", id)
	api := d.Step("service:api")
	if api == nil || api.Image != "i@sha256:"+strings.Repeat("0", 64) || api.Revision != "dep_do_9" ||
		!slices.Contains(api.Warnings, "api warned while it deployed") {
		t.Fatalf("api step %+v", api)
	}
}
