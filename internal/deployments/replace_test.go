package deployments

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// moveLog is what every executor of a test did, in order.
type moveLog struct {
	mu     sync.Mutex
	events []string
}

func (l *moveLog) add(e string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *moveLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

// placed is an executor on one provider. It checkpoints in Apply and in
// Remove, forgets what's kept when it removes something, as a resource's
// executor does, and fails Remove with each of removeErr in turn.
type placed struct {
	provider string
	log      *moveLog
	url      string
	// externalID is what Apply answers with, "ext-<provider>" by default.
	externalID string
	applyErr   error
	removeErr  []error
	// onRemove runs as Remove starts, so a test can see what's bound
	// at that point.
	onRemove func()

	mu      sync.Mutex
	applies []*Job
	removes []*Job
}

func (p *placed) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	p.mu.Lock()
	p.applies = append(p.applies, job)
	p.mu.Unlock()
	p.log.add("apply " + p.provider)
	if p.applyErr != nil {
		return nil, p.applyErr
	}
	if err := job.Checkpoint([]byte("op-" + p.provider)); err != nil {
		return nil, err
	}
	if job.Action.Resource != nil {
		if err := job.Keep(ctx, ResourceURL, []byte("url-"+p.provider)); err != nil {
			return nil, err
		}
	}
	ext := p.externalID
	if ext == "" {
		ext = "ext-" + p.provider
	}
	out := &Outcome{ExternalID: ext}
	if job.Action.Service != nil {
		out.URL = p.url
	}
	return out, nil
}

func (p *placed) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	p.mu.Lock()
	p.removes = append(p.removes, job)
	var err error
	if len(p.removeErr) > 0 {
		err, p.removeErr = p.removeErr[0], p.removeErr[1:]
	}
	p.mu.Unlock()
	if p.onRemove != nil {
		p.onRemove()
	}
	p.log.add("remove " + p.provider + " " + b.ExternalID)
	if cerr := job.Checkpoint([]byte("rm-" + p.provider)); cerr != nil {
		return cerr
	}
	if err != nil {
		return err
	}
	return job.Forget(ctx)
}

func (p *placed) Observe(context.Context, *connections.Connection, []byte, *planning.Binding) (*Observation, error) {
	return &Observation{Exists: true}, nil
}

// memSecrets keeps resources' credentials in memory.
type memSecrets struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memSecrets) Put(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = value
	return nil
}

func (s *memSecrets) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	if !ok {
		return nil, ErrNoCredential
	}
	return v, nil
}

func (s *memSecrets) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}

// apiMoves is an approved plan that moves api from Cloud Run to Vercel.
func apiMoves() *planning.Plan {
	plan := webAndAPI()
	plan.Actions = []planning.Action{
		{ID: "service:api", Op: planning.Replace, Subject: planning.ServiceSubject, Name: "api", Provider: "vercel", Step: 1,
			Service: &planning.ServiceConfig{Provider: "vercel", Service: "svc:go:services/api", Public: true}},
	}
	return plan
}

// onCloudRun is api's binding before it moves.
func onCloudRun() *planning.Binding {
	return &planning.Binding{
		ID: "bind_api", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ServiceSubject,
		Name: "api", Provider: "cloud-run", Region: "us-central1", ExternalID: "ext-old", URL: "https://old.example",
		ConnectionID: "conn_g",
	}
}

func moveRunner(t *testing.T, plan *planning.Plan, b *planning.Binding, execs map[string]Executor) (*Runner, *memStore, *fakePlans, *memSecrets) {
	t.Helper()
	store := &memStore{d: map[string]*Deployment{}}
	plans := &fakePlans{plan: plan, bindings: map[string]*planning.Binding{string(b.Subject) + ":" + b.Name: b}}
	secrets := &memSecrets{m: map[string][]byte{}}
	r := NewRunner(RunnerDeps{
		Store: store, Plans: plans, Credentials: fakeCredentials{}, Variables: fakeVariables{},
		Sources: &fakeSources{}, Providers: available(t), Executors: execs, Resources: secrets,
	})
	r.verifyWait = time.Second
	return r, store, plans, secrets
}

func TestRunner_aMoveRemovesTheOldOneOnceTheNewOneIsUp(t *testing.T) {
	log := &moveLog{}
	oldExec := &placed{provider: "cloud-run", log: log}
	newExec := &placed{provider: "vercel", log: log, url: answering(t, http.StatusOK)}
	plan := apiMoves()
	r, _, plans, _ := moveRunner(t, plan, onCloudRun(), map[string]Executor{"cloud-run": oldExec, "vercel": newExec})

	if _, err := runAll(t, r, plan); err != nil {
		t.Fatal(err)
	}
	if got, want := log.list(), []string{"apply vercel", "remove cloud-run ext-old"}; !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	apply := newExec.applies[0]
	if apply.Binding != nil || apply.Replaces == nil || apply.Replaces.ID != "bind_api" {
		t.Fatalf("the new one was given binding %+v, replacing %+v", apply.Binding, apply.Replaces)
	}
	// The old one is removed the way it was deployed: by its own
	// provider's executor, with its own connection and region.
	rm := oldExec.removes[0]
	if rm.Connection.ID != "conn_g" || string(rm.Credential) != "secret-conn_g" ||
		rm.Action.Provider != "cloud-run" || rm.Action.Region != "us-central1" || rm.Binding.ExternalID != "ext-old" {
		t.Fatalf("removed with %s %q as %s in %s", rm.Connection.ID, rm.Credential, rm.Action.Provider, rm.Action.Region)
	}
	b := plans.bindings["service:api"]
	if b.ID != "bind_api" || b.Provider != "vercel" || b.ExternalID != "ext-vercel" || b.ConnectionID != "conn_v" {
		t.Fatalf("binding %+v", b)
	}
}

func TestRunner_aMoveThatDoesntStartKeepsTheOldOne(t *testing.T) {
	log := &moveLog{}
	oldExec := &placed{provider: "cloud-run", log: log}
	newExec := &placed{provider: "vercel", log: log, applyErr: errors.New("vercel is down")}
	plan := apiMoves()
	r, _, plans, _ := moveRunner(t, plan, onCloudRun(), map[string]Executor{"cloud-run": oldExec, "vercel": newExec})

	if _, err := runAll(t, r, plan); err != nil {
		t.Fatal(err)
	}
	if got := log.list(); !slices.Equal(got, []string{"apply vercel"}) {
		t.Fatalf("events %v", got)
	}
	if b := plans.bindings["service:api"]; b.Provider != "cloud-run" || b.ExternalID != "ext-old" {
		t.Fatalf("binding %+v", b)
	}
}

// dbMoves is an approved plan that moves db to another Cloud SQL region.
func dbMoves() (*planning.Plan, *planning.Binding) {
	plan := webAndAPI()
	plan.Actions = []planning.Action{
		{ID: "resource:db", Op: planning.Replace, Subject: planning.ResourceSubject, Name: "db", Provider: "cloud-sql",
			Region: "europe-west1", Step: 1,
			Resource: &planning.ResourceConfig{Kind: "postgres", Version: "16", Provider: "cloud-sql", Region: "europe-west1"}},
	}
	b := &planning.Binding{
		ID: "bind_db", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ResourceSubject,
		Name: "db", Provider: "cloud-sql", Region: "us-central1", ExternalID: "p:us-central1:lp-db", ConnectionID: "conn_g",
	}
	return plan, b
}

func TestRunner_removingWhatAMoveReplacesKeepsTheNewOnesCredentials(t *testing.T) {
	log := &moveLog{}
	exec := &placed{provider: "cloud-sql", log: log, externalID: "p:europe-west1:lp-db-europe-west1"}
	plan, b := dbMoves()
	r, _, plans, secrets := moveRunner(t, plan, b, map[string]Executor{"cloud-sql": exec})

	if _, err := runAll(t, r, plan); err != nil {
		t.Fatal(err)
	}
	if got, want := log.list(), []string{"apply cloud-sql", "remove cloud-sql p:us-central1:lp-db"}; !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if rm := exec.removes[0]; rm.Action.Region != "us-central1" {
		t.Fatalf("removed in %s", rm.Action.Region)
	}
	url, err := secrets.Get(t.Context(), resourceKey("org_1", "env_1", "db", ResourceURL))
	if err != nil || string(url) != "url-cloud-sql" {
		t.Fatalf("the new database's address: %q %v", url, err)
	}
	if b := plans.bindings["resource:db"]; b.Region != "europe-west1" || b.ExternalID != "p:europe-west1:lp-db-europe-west1" {
		t.Fatalf("binding %+v", b)
	}
}

func TestRunner_aMoveThatKeepsTheOldOneRemovesNothing(t *testing.T) {
	log := &moveLog{}
	// The executor updated the old one in place instead of starting
	// another.
	exec := &placed{provider: "cloud-sql", log: log, externalID: "p:us-central1:lp-db"}
	plan, b := dbMoves()
	r, store, plans, _ := moveRunner(t, plan, b, map[string]Executor{"cloud-sql": exec})

	id, err := runAll(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := log.list(); !slices.Equal(got, []string{"apply cloud-sql"}) {
		t.Fatalf("events %v", got)
	}
	d, _ := store.GetDeployment(t.Context(), "org_1", id)
	if d.Status != StatusFailed || !strings.Contains(d.Error, "can't move db to cloud-sql in europe-west1") {
		t.Fatalf("deployment %s: %s", d.Status, d.Error)
	}
	if got := plans.bindings["resource:db"]; got.Region != "us-central1" {
		t.Fatalf("binding %+v", got)
	}
}

func TestReadReplaceCheckpoint(t *testing.T) {
	rc := readReplaceCheckpoint([]byte("op-1"))
	if string(rc.Apply) != "op-1" || rc.Remove != nil {
		t.Fatalf("an earlier checkpoint read as %+v", rc)
	}
	rc.Remove = []byte("rm-1")
	again := readReplaceCheckpoint(rc.encode())
	if string(again.Apply) != "op-1" || string(again.Remove) != "rm-1" {
		t.Fatalf("read back %+v", again)
	}
	if rc := readReplaceCheckpoint(nil); rc.Apply != nil || rc.Remove != nil {
		t.Fatalf("nothing read as %+v", rc)
	}
}

// fakeInstances holds Ctrl Plane instances by ID.
type fakeInstances struct {
	instance.Service
	byID    map[id.ID]*instance.Instance
	deleted []id.ID
}

func (f *fakeInstances) Get(_ context.Context, iid id.ID) (*instance.Instance, error) {
	if inst, ok := f.byID[iid]; ok {
		return inst, nil
	}
	return nil, ctrlplane.ErrNotFound
}

func (f *fakeInstances) GetBySlug(_ context.Context, slug string) (*instance.Instance, error) {
	for _, inst := range f.byID {
		if inst.Slug == slug {
			return inst, nil
		}
	}
	return nil, ctrlplane.ErrNotFound
}

func (f *fakeInstances) Delete(_ context.Context, iid id.ID) error {
	f.deleted = append(f.deleted, iid)
	delete(f.byID, iid)
	return nil
}

func ctrlInstance(slug, providerName string) *instance.Instance {
	return &instance.Instance{Entity: ctrlplane.Entity{ID: id.New(id.PrefixInstance)}, Slug: slug, ProviderName: providerName}
}

func TestCurrentInstance_aReplacementTakesANameOfItsOwn(t *testing.T) {
	job := &Job{
		Deployment: &Deployment{ProjectID: "proj_01h2xcejqtf2nbrexx3vqjhp41", Environment: "production"},
		Action:     planning.Action{Name: "api"}, Progress: func(string, string) {},
	}
	base := instanceSlug(job)
	old := ctrlInstance(base, "cloudrun.conn_g.us-central1")
	instances := &fakeInstances{byID: map[id.ID]*instance.Instance{old.ID: old}}
	ctrl := CtrlPlane{Instances: instances}
	job.Replaces = &planning.Binding{ExternalID: old.ID.String()}

	inst, name, err := currentInstance(t.Context(), ctrl, job, "cloudrun.conn_g.europe-west1")
	if err != nil || inst != nil || name != base+"-2" {
		t.Fatalf("got %v %q %v, want a new instance named %s-2", inst, name, err, base)
	}
	if len(instances.deleted) > 0 {
		t.Fatal("the instance it replaces was deleted before the new one started")
	}

	// A retry finds the one the first attempt created.
	created := ctrlInstance(base+"-2", "cloudrun.conn_g.europe-west1")
	instances.byID[created.ID] = created
	if inst, _, err = currentInstance(t.Context(), ctrl, job, "cloudrun.conn_g.europe-west1"); err != nil || inst != created {
		t.Fatalf("retry found %v %v", inst, err)
	}

	// Moving back, the one it replaces has the second name, so the new
	// one takes the first.
	job.Replaces = &planning.Binding{ExternalID: created.ID.String()}
	delete(instances.byID, old.ID)
	if _, name, _ = currentInstance(t.Context(), ctrl, job, "cloudrun.conn_g.us-central1"); name != instanceName(job) {
		t.Fatalf("moving back named it %q", name)
	}
}

// Cloud Run and ECS share currentInstance, so neither deletes an instance
// before the one that takes its place has started.
func TestCurrentInstance_neverDeletesOneOnAnotherConnection(t *testing.T) {
	job := &Job{
		Deployment: &Deployment{ProjectID: "proj_01h2xcejqtf2nbrexx3vqjhp41", Environment: "production"},
		Action:     planning.Action{Name: "api"}, Progress: func(string, string) {},
	}
	for _, key := range []string{"cloudrun.conn_old.us-central1", "ecs.conn_old.us-east-1"} {
		other := ctrlInstance(instanceSlug(job), key)
		instances := &fakeInstances{byID: map[id.ID]*instance.Instance{other.ID: other}}
		ctrl := CtrlPlane{Instances: instances}

		// Found by its binding, and found by its name.
		for _, b := range []*planning.Binding{{ExternalID: other.ID.String()}, nil} {
			job.Binding = b
			_, _, err := currentInstance(t.Context(), ctrl, job, strings.Replace(key, "conn_old", "conn_new", 1))
			if err == nil || !strings.Contains(err.Error(), "remove it") {
				t.Fatalf("%s, binding %v: %v", key, b, err)
			}
			if len(instances.deleted) > 0 {
				t.Fatalf("%s: deleted the instance on another connection", key)
			}
		}
	}
}

func TestCloudSQLInstance(t *testing.T) {
	job := &Job{
		Deployment: &Deployment{ProjectID: "proj_01h2xcejqtf2nbrexx3vqjhp41", Environment: "production"},
		Action:     planning.Action{Name: "db", Resource: &planning.ResourceConfig{Region: "europe-west1"}},
	}
	if got, want := cloudSQLInstance(job), SQLInstanceName(job)+"-europe-west1"; got != want {
		t.Fatalf("a new instance is %q, want %q", got, want)
	}
	// One already bound keeps its name, from before names had a region.
	job.Binding = &planning.Binding{ExternalID: "example.com:acme:us-central1:lp-db-production-jhp41"}
	if got := cloudSQLInstance(job); got != "lp-db-production-jhp41" {
		t.Fatalf("a bound instance is %q", got)
	}
	// One bound in the same region keeps its name too.
	job.Binding.Region = "europe-west1"
	if got := cloudSQLInstance(job); got != "lp-db-production-jhp41" {
		t.Fatalf("a bound instance in its region is %q", got)
	}
	// One bound in another region can't be this one, since a region
	// can't change in place, so this one gets a name of its own.
	job.Binding.Region = "us-central1"
	if got, want := cloudSQLInstance(job), SQLInstanceName(job)+"-europe-west1"; got != want {
		t.Fatalf("with a binding in another region, %q, want %q", got, want)
	}
}
