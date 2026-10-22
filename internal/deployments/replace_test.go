package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/cloudrun"
	"github.com/xraph/loomplane/internal/clouds/ecs"
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
	// warn is a warning each Remove gives its step.
	warn error
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
	if p.warn != nil {
		job.Warn(p.warn)
	}
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
	// deleteErr is what Delete answers with, when set.
	deleteErr error
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
	if f.deleteErr != nil {
		return f.deleteErr
	}
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
	old := ctrlInstance(base, cloudrun.Key("conn_g", "us-central1"))
	instances := &fakeInstances{byID: map[id.ID]*instance.Instance{old.ID: old}}
	ctrl := CtrlPlane{Instances: instances}
	job.Replaces = &planning.Binding{ExternalID: old.ID.String()}

	inst, name, err := currentInstance(t.Context(), ctrl, job, cloudrun.Key("conn_g", "europe-west1"))
	if err != nil || inst != nil || name != base+"-2" {
		t.Fatalf("got %v %q %v, want a new instance named %s-2", inst, name, err, base)
	}
	if len(instances.deleted) > 0 {
		t.Fatal("the instance it replaces was deleted before the new one started")
	}

	// A retry finds the one the first attempt created.
	created := ctrlInstance(base+"-2", cloudrun.Key("conn_g", "europe-west1"))
	instances.byID[created.ID] = created
	if inst, _, err = currentInstance(t.Context(), ctrl, job, cloudrun.Key("conn_g", "europe-west1")); err != nil || inst != created {
		t.Fatalf("retry found %v %v", inst, err)
	}

	// Moving back, the one it replaces has the second name, so the new
	// one takes the first.
	job.Replaces = &planning.Binding{ExternalID: created.ID.String()}
	delete(instances.byID, old.ID)
	if _, name, _ = currentInstance(t.Context(), ctrl, job, cloudrun.Key("conn_g", "us-central1")); name != instanceName(job) {
		t.Fatalf("moving back named it %q", name)
	}
}

// Cloud Run and ECS share currentInstance, so neither deletes an instance
// before the one that takes its place has started. A bound one under
// another provider key, as a binding from before bindings recorded their
// connection can have, isn't moved here either: that would change its
// address under whatever the plan keeps. currentInstance creates nothing
// and says which connection and region its key names.
func TestCurrentInstance_aBoundOneOnAnotherConnectionIsLeftForThePlan(t *testing.T) {
	job := &Job{
		Deployment: &Deployment{ProjectID: "proj_01h2xcejqtf2nbrexx3vqjhp41", Environment: "production"},
		Action:     planning.Action{Name: "api"}, Progress: func(string, string) {},
	}
	for _, c := range []struct{ old, now, region string }{
		{cloudrun.Key("conn_old", "us-central1"), cloudrun.Key("conn_new", "us-central1"), "us-central1"},
		{ecs.Key("conn_old", "us-east-1"), ecs.Key("conn_new", "us-east-1"), "us-east-1"},
	} {
		other := ctrlInstance(instanceSlug(job), c.old)
		instances := &fakeInstances{byID: map[id.ID]*instance.Instance{other.ID: other}}
		ctrl := CtrlPlane{Instances: instances}
		bound := &planning.Binding{ID: "bind_api", Provider: "cloud-run", ExternalID: other.ID.String()}
		job.Binding, job.Replaces = bound, nil

		inst, _, err := currentInstance(t.Context(), ctrl, job, c.now)
		var elsewhere *ElsewhereError
		if inst != nil || !errors.As(err, &elsewhere) || elsewhere.ConnectionID != "conn_old" || elsewhere.Region != c.region ||
			!strings.Contains(err.Error(), "plan again") {
			t.Fatalf("%s: got %v, %v", c.old, inst, err)
		}
		if len(instances.deleted) > 0 || job.Replaces != nil || job.Binding != bound {
			t.Fatalf("%s: deleted %v, binding %+v, replacing %+v", c.old, instances.deleted, job.Binding, job.Replaces)
		}
	}
}

// Something that only holds the name, such as an instance a failed first
// attempt left under an earlier connection, or one a person removed from
// the cloud but not from Ctrl Plane, doesn't stop a deployment: the new
// one takes the next free name.
func TestCurrentInstance_oneThatOnlyHoldsTheNameIsLeftAlone(t *testing.T) {
	job := &Job{
		Deployment: &Deployment{ProjectID: "proj_01h2xcejqtf2nbrexx3vqjhp41", Environment: "production"},
		Action:     planning.Action{Name: "api"}, Progress: func(string, string) {},
	}
	key := cloudrun.Key("conn_new", "us-central1")
	first := ctrlInstance(instanceSlug(job), cloudrun.Key("conn_old", "us-central1"))
	second := ctrlInstance(instanceSlug(job)+"-2", cloudrun.Key("conn_gone", "us-central1"))
	instances := &fakeInstances{byID: map[id.ID]*instance.Instance{first.ID: first, second.ID: second}}
	ctrl := CtrlPlane{Instances: instances}

	inst, name, err := currentInstance(t.Context(), ctrl, job, key)
	if err != nil || inst != nil || name != instanceName(job)+"-3" {
		t.Fatalf("got %v %q %v, want a new instance named %s-3", inst, name, err, instanceName(job))
	}
	if len(instances.deleted) > 0 || job.Replaces != nil {
		t.Fatalf("deleted %v, replacing %+v", instances.deleted, job.Replaces)
	}
	// A retry finds the one the first attempt created under this key.
	created := ctrlInstance(instanceSlug(job)+"-3", key)
	instances.byID[created.ID] = created
	if inst, _, err = currentInstance(t.Context(), ctrl, job, key); err != nil || inst != created {
		t.Fatalf("retry found %v %v", inst, err)
	}
}

func TestKeyPlace(t *testing.T) {
	if conn, region := keyPlace(cloudrun.Key("conn_typeid_01h", "europe-west1")); conn != "conn_typeid_01h" || region != "europe-west1" {
		t.Fatalf("cloud run: %s %s", conn, region)
	}
	if conn, region := keyPlace(ecs.Key("conn_a", "eu-west-1")); conn != "conn_a" || region != "eu-west-1" {
		t.Fatalf("ecs: %s %s", conn, region)
	}
	if conn, region := keyPlace("something-else"); conn != "" || region != "" {
		t.Fatalf("unknown key: %s %s", conn, region)
	}
}

// An ECS service a move created as name-2, before target groups were
// named after the instance, runs behind the target group named after the
// first name. ECS can't change a running service's target group, so an
// update keeps the one it has.
func TestTargetGroup_anInstanceKeepsTheOneItHas(t *testing.T) {
	inst := ctrlInstance("api-production-jhp41-2", ecs.Key("conn_a", "us-east-1"))
	inst.Name = "api-production-jhp41-2"
	inst.Labels = map[string]string{ecs.LabelTargetGroup: "arn:tg/" + shortName("api-production-jhp41"), ecs.LabelListener: "arn:listener/1"}
	if arn, group := targetGroup(inst, inst.Name); arn != "arn:tg/"+shortName("api-production-jhp41") || group != "" {
		t.Fatalf("an existing instance got %q %q", arn, group)
	}
	// A new one, or one from before it was exposed, gets one named after
	// its own slug.
	if arn, group := targetGroup(nil, "api-production-jhp41-2"); arn != "" || group != shortName("api-production-jhp41-2") {
		t.Fatalf("a new instance got %q %q", arn, group)
	}
	if arn, group := targetGroup(ctrlInstance("api-production-jhp41", "k"), "Api-Production-jhp41"); arn != "" || group != shortName("api-production-jhp41") {
		t.Fatalf("an instance without one got %q %q", arn, group)
	}
}

// Something already gone isn't an error, so a removal Ctrl Plane no longer
// knows finishes.
func TestECSRemove_anInstanceThatsGoneIsRemoved(t *testing.T) {
	instances := &fakeInstances{byID: map[id.ID]*instance.Instance{}, deleteErr: ctrlplane.ErrNotFound}
	e := &ECS{Ctrl: CtrlPlane{Providers: cpprovider.NewRegistry(), Instances: instances}}
	job := &Job{
		Deployment: &Deployment{OrgID: "org_1", ProjectID: "proj_1", Environment: "production"},
		Connection: &connections.Connection{ID: "conn_a", Details: map[string]string{"region": "us-east-1"}},
	}
	if err := e.Remove(t.Context(), job, &planning.Binding{ExternalID: id.New(id.PrefixInstance).String()}); err != nil {
		t.Fatal(err)
	}
	instances.deleteErr = errors.New("aws is down")
	if err := e.Remove(t.Context(), job, &planning.Binding{ExternalID: id.New(id.PrefixInstance).String()}); err == nil {
		t.Fatal("another error was swallowed")
	}
}

func TestLocate(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	b := &planning.Binding{Provider: "cloud-run", Region: "us-central1", ExternalID: iid.String()}
	if got, want := locate(&CloudRun{}, b), "the Cloud Run service "+cloudrun.ServiceName(iid)+" in us-central1"; got != want {
		t.Fatalf("cloud run: %q, want %q", got, want)
	}
	b.Provider, b.Region = "ecs-fargate", "us-east-1"
	if got := locate(&ECS{}, b); !strings.HasPrefix(got, "the ECS service "+ecs.ServiceName(iid)+" in cluster ") {
		t.Fatalf("ecs: %q", got)
	}
	v := &planning.Binding{Provider: "vercel", ExternalID: "prj_1"}
	if got := locate(&placed{}, v); got != "prj_1 on vercel" {
		t.Fatalf("anything else: %q", got)
	}
}

func TestGCSBucket(t *testing.T) {
	job := &Job{
		Deployment: &Deployment{ProjectID: "proj_01h2xcejqtf2nbrexx3vqjhp41", Environment: "production"},
		Action:     planning.Action{Name: "files", Resource: &planning.ResourceConfig{Kind: "object-storage", Region: "us-central1"}},
	}
	here := gcsBucket("acme", job)
	if !strings.HasPrefix(here, "lp-files-us-central1-") || len(here) != len("lp-files-us-central1-")+10 {
		t.Fatalf("a new bucket is %q", here)
	}
	// A move needs a name the bucket it replaces doesn't hold.
	job.Action.Resource.Region = "europe-west1"
	there := gcsBucket("acme", job)
	if there == here || !strings.HasPrefix(there, "lp-files-europe-west1-") {
		t.Fatalf("moved to europe-west1, the bucket is %q", there)
	}
	if strings.TrimPrefix(there, "lp-files-europe-west1-") != strings.TrimPrefix(here, "lp-files-us-central1-") {
		t.Fatalf("the region changed the hash: %q, %q", here, there)
	}
	if other := gcsBucket("other", job); other == there {
		t.Fatalf("two Google projects share %q", other)
	}

	// A long name is cut so the whole fits, region and hash included.
	job.Action.Name = strings.Repeat("a", 63)
	job.Action.Resource.Region = "northamerica-northeast1"
	if long := gcsBucket("acme", job); len(long) != 63 || !strings.Contains(long, "-northamerica-northeast1-") {
		t.Fatalf("a long name is %q (%d)", long, len(long))
	}

	// One already bound keeps its name, from before names had a region.
	job.Binding = &planning.Binding{ExternalID: "lp-files-0123456789"}
	if got := gcsBucket("acme", job); got != "lp-files-0123456789" {
		t.Fatalf("a bound bucket is %q", got)
	}
}

func TestBucketName(t *testing.T) {
	key := &aws.Key{AccessKeyID: "AKIAEXAMPLE0000", SecretAccessKey: "x"}
	east := aws.New("").Session(key, "us-east-1", "123456789012")
	west := aws.New("").Session(key, "eu-west-1", "123456789012")
	job := &Job{
		Deployment: &Deployment{ProjectID: "proj_01h2xcejqtf2nbrexx3vqjhp41", Environment: "production"},
		Action:     planning.Action{Name: "files", Resource: &planning.ResourceConfig{Kind: "object-storage"}},
	}
	here, there := bucketName(east, job), bucketName(west, job)
	if !strings.HasPrefix(here, "lp-files-us-east-1-") || !strings.HasPrefix(there, "lp-files-eu-west-1-") {
		t.Fatalf("new buckets are %q and %q", here, there)
	}
	// The environment's tasks reach a bucket in either region.
	_, policy := tasksRole(east, job)
	for _, b := range []string{here, there} {
		pattern := "lp-*-" + awsEnvKey(east, job)
		if ok, _ := path.Match(pattern, b); !ok || !strings.Contains(policy, "arn:aws:s3:::"+pattern) {
			t.Fatalf("the tasks role doesn't grant %q: %s", b, policy)
		}
	}

	job.Action.Name = strings.Repeat("a", 63)
	if long := bucketName(west, job); len(long) != 63 || !strings.Contains(long, "-eu-west-1-") {
		t.Fatalf("a long name is %q (%d)", long, len(long))
	}

	// One already bound keeps its name, from before names had a region.
	job.Binding = &planning.Binding{ExternalID: "lp-files-0123456789"}
	if got := bucketName(west, job); got != "lp-files-0123456789" {
		t.Fatalf("a bound bucket is %q", got)
	}
}

// Cloud Run and ECS find out what's bound runs through another connection
// before they build or set anything up, so the step creates nothing. With
// no Google or AWS client, getting any further would panic.
func TestApply_aBoundOneOnAnotherConnectionBuildsNothing(t *testing.T) {
	job := &Job{
		Deployment: &Deployment{OrgID: "org_1", ProjectID: "proj_01h2xcejqtf2nbrexx3vqjhp41", Environment: "production"},
		Action: planning.Action{Name: "api", Provider: "cloud-run", Region: "us-central1",
			Service: &planning.ServiceConfig{Provider: "cloud-run"}},
		Connection: &connections.Connection{ID: "conn_new", Details: map[string]string{"region": "us-east-1"}},
		Progress:   func(string, string) {},
	}
	for _, c := range []struct {
		exec Executor
		key  string
	}{
		{&CloudRun{}, cloudrun.Key("conn_old", "us-central1")},
		{&ECS{}, ecs.Key("conn_old", "us-east-1")},
	} {
		other := ctrlInstance("api-production-jhp41", c.key)
		ctrl := CtrlPlane{Providers: cpprovider.NewRegistry(), Instances: &fakeInstances{byID: map[id.ID]*instance.Instance{other.ID: other}}}
		switch e := c.exec.(type) {
		case *CloudRun:
			e.Ctrl = ctrl
		case *ECS:
			e.Ctrl = ctrl
		}
		job.Binding = &planning.Binding{ExternalID: other.ID.String()}
		_, err := c.exec.Apply(t.Context(), job)
		var elsewhere *ElsewhereError
		if !errors.As(err, &elsewhere) || elsewhere.ConnectionID != "conn_old" {
			t.Fatalf("%s: %v", c.key, err)
		}
	}
}

// What an ElsewhereError says depends on what the runner could record.
func TestElsewhereError_saysWhatWasRecorded(t *testing.T) {
	e := &ElsewhereError{Name: "api", Instance: "inst_1", ConnectionID: "conn_old", Key: "cloud-run.conn_old.us-central1"}
	if msg := e.Error(); !strings.Contains(msg, "couldn't record that") || !strings.Contains(msg, "Retry this step") {
		t.Fatalf("not recorded: %s", msg)
	}
	e.Recorded = true
	if msg := e.Error(); !strings.Contains(msg, "has recorded that on its binding: plan again") {
		t.Fatalf("recorded: %s", msg)
	}
	e.Recorded, e.SameConnection, e.Region = false, true, "us-central1"
	if msg := e.Error(); !strings.Contains(msg, "region us-central1") || strings.Contains(msg, "plan again") {
		t.Fatalf("same connection: %s", msg)
	}
	e.SameConnection = false
	e.ConnectionID = ""
	if msg := e.Error(); !strings.Contains(msg, "Choose the connection that deployed it") || strings.Contains(msg, "recorded") {
		t.Fatalf("unreadable key: %s", msg)
	}
}

// A warning an executor gives while it removes what a delete names lands
// on the step, which still succeeds.
func TestRunner_aRemovalsWarningLandsOnItsStep(t *testing.T) {
	log := &moveLog{}
	exec := &placed{provider: "cloud-run", log: log, warn: errors.New("the admin sessions open on api couldn't be ended")}
	del := apiMoves()
	del.Actions[0].Op, del.Actions[0].Service, del.Actions[0].Provider = planning.Delete, nil, "cloud-run"
	r, store, plans, _ := moveRunner(t, del, onCloudRun(), map[string]Executor{"cloud-run": exec})
	id, err := runAll(t, r, del)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := store.GetDeployment(t.Context(), "org_1", id)
	s := d.Step("service:api")
	if d.Status != StatusSucceeded || !slices.Equal(s.Warnings, []string{"the admin sessions open on api couldn't be ended"}) {
		t.Fatalf("deleting: %s: %s, warnings %q", d.Status, d.Error, s.Warnings)
	}
	if _, ok := plans.bindings["service:api"]; ok {
		t.Fatal("the binding is still there")
	}
}

// Deleting a composed resource whose connection is gone can't reach its
// components, but still ends the admin sessions open on it, and a failure
// to only warns.
func TestRunner_aDeleteWhoseConnectionIsGoneEndsAComposedResourcesSessions(t *testing.T) {
	for _, revokeErr := range []error{nil, errors.New("the session store is down")} {
		var revoked []string
		exec := &OwnedTarget{Cloud: connections.Server, CompositionRemoved: func(_ context.Context, orgID, environmentID, resource string) error {
			revoked = append(revoked, orgID+"/"+environmentID+"/"+resource)
			return revokeErr
		}}
		cfg, err := json.Marshal(planning.ResourceConfig{Provider: connections.Server, Target: "conn_old", Kind: "sample", Version: "1",
			Composition: &planning.CompositionConfig{Kind: "sample", Version: "1", Revision: "1"}})
		if err != nil {
			t.Fatal(err)
		}
		was := &planning.Binding{ID: "bind_app", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Subject: planning.ResourceSubject,
			Name: "app", Provider: connections.Server, ExternalID: "catalog/app", ConnectionID: "conn_old", Config: cfg}
		plan := webAndAPI()
		plan.Actions = []planning.Action{{ID: "resource:app", Op: planning.Delete, Subject: planning.ResourceSubject, Name: "app", Provider: connections.Server, Step: 1}}
		r, store, plans, _ := moveRunner(t, plan, was, map[string]Executor{connections.Server: exec})
		r.credentials = goneCredentials{gone: map[string]bool{"conn_old": true}}

		ctx := t.Context()
		id, _, err := r.Start(ctx, plan.OrgID, plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.FetchSource(ctx, plan.OrgID, id); err != nil {
			t.Fatal(err)
		}
		if err := r.Run(ctx, plan.OrgID, id, "resource:app", nil, func([]byte) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if err := r.Finish(ctx, plan.OrgID, id, ""); err != nil {
			t.Fatal(err)
		}
		d, _ := store.GetDeployment(ctx, "org_1", id)
		s := d.Step("resource:app")
		if !slices.Equal(revoked, []string{"org_1/env_1/app"}) {
			t.Fatalf("revoked %q", revoked)
		}
		warned := slices.ContainsFunc(s.Warnings, func(w string) bool {
			return strings.Contains(w, "the admin sessions open on app couldn't be ended") && strings.Contains(w, "the session store is down")
		})
		if d.Status != StatusSucceeded || warned != (revokeErr != nil) {
			t.Fatalf("deployment %s: %s, warnings %q", d.Status, d.Error, s.Warnings)
		}
		if _, ok := plans.bindings["resource:app"]; ok {
			t.Fatal("the binding is still there")
		}
	}
}
