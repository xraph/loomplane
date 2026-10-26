package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/cloudrun"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

const (
	runRegion      = "europe-west1"
	runOldImage    = "europe-west1-docker.pkg.dev/shop/loomplane/api:old"
	runNewImage    = "europe-west1-docker.pkg.dev/shop/loomplane/api:new"
	runBaseline    = "svc-00001-aaa"
	runProject     = "shop"
	runDeployment  = "dep_01jxcanary0000000000000000"
	runConnection  = "conn_g"
	runAllowedPerm = "run.revisions.get"
	// runSecret is where the job's TOKEN is kept, as secretName names it.
	runSecret = "lp-api-production-3vqjhp41-TOKEN"
)

func TestCanaryRevisionName(t *testing.T) {
	const service = "loomplane-abc"
	name := canaryRevision(service, "dep_01jxa2b3c4d5e6f7g8h9j0k1m2", 2)
	if name != strings.ToLower(name) || !strings.HasPrefix(name, service+"-") || len(name) > 63 {
		t.Fatalf("name %q", name)
	}
	if again := canaryRevision(service, "dep_01jxa2b3c4d5e6f7g8h9j0k1m2", 2); again != name {
		t.Fatalf("not stable: %q, %q", name, again)
	}
	if other := canaryRevision(service, "dep_01jxa2b3c4d5e6f7g8h9j0k1m2", 3); other == name {
		t.Fatalf("a retry reuses %q", name)
	}
	if other := canaryRevision(service, "dep_01jxzzzzzzzzzzzzzzzzzzzzzz", 2); other == name {
		t.Fatalf("another deployment reuses %q", name)
	}
	// Cloud Run service names go up to 49 characters, and a name that has
	// to dodge an earlier one gets a number on the end.
	long := strings.Repeat("s", 49)
	if n := numberedRevision(canaryRevision(long, "dep_x", 12), 99); len(n) > 63 || !strings.HasPrefix(n, long+"-") {
		t.Fatalf("long name %q (%d)", n, len(n))
	}
}

func TestBaselineIsTheRevisionWithMostTraffic(t *testing.T) {
	split := &gcp.RunService{TrafficStatuses: []gcp.RunTraffic{
		{Type: gcp.TrafficRevision, Revision: "a", Percent: 70}, {Type: gcp.TrafficRevision, Revision: "b", Percent: 30},
	}}
	if got := baselineRevision(split); got != "a" {
		t.Fatalf("split: %q", got)
	}
	whole := &gcp.RunService{TrafficStatuses: []gcp.RunTraffic{{Type: gcp.TrafficRevision, Revision: "a", Percent: 100}}}
	if got := baselineRevision(whole); got != "a" {
		t.Fatalf("whole: %q", got)
	}
	latest := &gcp.RunService{
		LatestReadyRev:  "projects/shop/locations/europe-west1/services/svc/revisions/svc-00003-xyz",
		TrafficStatuses: []gcp.RunTraffic{{Type: gcp.TrafficLatest, Percent: 100}},
	}
	if got := baselineRevision(latest); got != "svc-00003-xyz" {
		t.Fatalf("latest only: %q", got)
	}
	if got := baselineRevision(&gcp.RunService{LatestReadyRev: "svc-00004-q"}); got != "svc-00004-q" {
		t.Fatalf("no statuses: %q", got)
	}
}

// --- A fake Google Cloud: Cloud Run, Secret Manager and IAM ---

type fakeRunRevision struct {
	template gcp.RunTemplate
	ready    bool
	deleting bool
}

// fakeGoogleRun is one Cloud Run service as Google keeps it: an immutable
// revision for each template it's been given, the latest of them, and
// how its traffic is split. A template that changes makes a revision, and
// a revision that takes traffic, or that the template makes, can't be
// deleted. A PATCH without traffic keeps the split as it was: Cloud Run
// documents it as all to the latest, but nothing here relies on that.
type fakeGoogleRun struct {
	t  *testing.T
	mu sync.Mutex

	service       string
	template      gcp.RunTemplate
	traffic       []gcp.RunTraffic
	revisions     map[string]*fakeRunRevision
	latestCreated string
	latestReady   string
	generated     int

	// failImage never becomes ready.
	failImage string
	// fail makes the next n calls of a kind answer 503: "PATCH service",
	// "GET service", "GET revision", "DELETE revision". failAt makes the
	// call of a kind that's the nth since the test began answer 503.
	fail   map[string]int
	failAt map[string]int
	counts map[string]int
	// granted are the permissions testIamPermissions grants.
	granted []string

	versions  map[string]int
	refs      []string // every secret reference a template carried
	invokers  [][]string
	asked     [][]string
	calls     []string
	newRevs   []string
	patchedAt []int // len(calls) at each service PATCH
}

func newFakeGoogleRun(t *testing.T, service string, baseline gcp.RunService) *fakeGoogleRun {
	f := &fakeGoogleRun{
		t: t, service: service, revisions: map[string]*fakeRunRevision{}, fail: map[string]int{}, failAt: map[string]int{}, counts: map[string]int{},
		granted: slices.Clone(gcp.CanaryPermissions), versions: map[string]int{},
	}
	f.template = baseline.Template
	f.traffic = cloudrun.Latest()
	f.revisions[runBaseline] = &fakeRunRevision{template: baseline.Template, ready: true}
	f.latestCreated, f.latestReady = runBaseline, runBaseline
	return f
}

func (f *fakeGoogleRun) servicePath() string {
	return "/v2/projects/" + runProject + "/locations/" + runRegion + "/services/" + f.service
}

func (f *fakeGoogleRun) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, _ := io.ReadAll(r.Body)
	path := r.URL.Path
	f.calls = append(f.calls, r.Method+" "+path)
	answer := func(v any) {
		out, err := json.Marshal(v)
		if err != nil {
			f.t.Error(err)
		}
		w.Write(out)
	}
	refuse := func(status int, message string) {
		w.WriteHeader(status)
		answer(map[string]any{"error": map[string]any{"code": status, "message": message}})
	}
	kind := ""
	switch {
	case path == f.servicePath():
		kind = r.Method + " service"
	case strings.HasPrefix(path, f.servicePath()+"/revisions/"):
		kind = r.Method + " revision"
	}
	if kind != "" {
		f.counts[kind]++
		if f.fail[kind] > 0 || f.failAt[kind] == f.counts[kind] {
			if f.fail[kind] > 0 {
				f.fail[kind]--
			}
			refuse(http.StatusServiceUnavailable, "the fake failed "+kind)
			return
		}
	}
	switch {
	case path == "/token":
		answer(map[string]any{"access_token": "token", "token_type": "Bearer", "expires_in": 3600})
	case path == "/v3/projects/"+runProject && r.Method == http.MethodGet:
		answer(map[string]string{"name": "projects/123"})
	case path == "/v3/projects/"+runProject+":testIamPermissions":
		var in struct {
			Permissions []string `json:"permissions"`
		}
		json.Unmarshal(data, &in)
		f.asked = append(f.asked, in.Permissions)
		var held []string
		for _, p := range in.Permissions {
			if slices.Contains(f.granted, p) {
				held = append(held, p)
			}
		}
		answer(map[string]any{"permissions": held})
	case strings.HasPrefix(path, "/v1/projects/"+runProject+"/secrets"):
		f.secret(w, r, path, answer)
	case path == f.servicePath()+":setIamPolicy":
		var in struct {
			Policy struct {
				Bindings []struct {
					Members []string `json:"members"`
				} `json:"bindings"`
			} `json:"policy"`
		}
		json.Unmarshal(data, &in)
		f.invokers = append(f.invokers, in.Policy.Bindings[0].Members)
		answer(map[string]any{})
	case kind == "GET service":
		answer(f.view())
	case kind == "PATCH service":
		f.patchedAt = append(f.patchedAt, len(f.calls))
		if err := f.patch(data); err != nil {
			refuse(err.status, err.message)
			return
		}
		op := map[string]any{"name": "projects/" + runProject + "/locations/" + runRegion + "/operations/op", "done": true}
		if rev := f.revisions[f.latestCreated]; rev != nil && !rev.ready {
			op["error"] = map[string]string{"message": "revision " + f.latestCreated + " isn't ready: the container failed to start"}
		}
		answer(op)
	case kind == "GET revision":
		name := strings.TrimPrefix(path, f.servicePath()+"/revisions/")
		rev := f.revisions[name]
		if rev == nil {
			refuse(http.StatusNotFound, "revision "+name+" not found")
			return
		}
		state := "CONDITION_FAILED"
		if rev.ready {
			state = "CONDITION_SUCCEEDED"
		}
		out := map[string]any{
			"name":       f.servicePath()[1:] + "/revisions/" + name,
			"conditions": []map[string]string{{"type": "Ready", "state": state}},
			"containers": rev.template.Containers, "volumes": rev.template.Volumes,
			"scaling": rev.template.Scaling, "vpcAccess": rev.template.VPCAccess,
			"serviceAccount": "123-compute@developer.gserviceaccount.com",
		}
		if rev.deleting {
			out["deleteTime"] = "2026-10-26T10:00:00Z"
		}
		answer(out)
	case kind == "DELETE revision":
		name := strings.TrimPrefix(path, f.servicePath()+"/revisions/")
		switch {
		case f.revisions[name] == nil:
			refuse(http.StatusNotFound, "revision "+name+" not found")
			return
		case f.share(name) > 0:
			refuse(http.StatusBadRequest, "revision "+name+" takes traffic")
			return
		case f.latestCreated == name:
			refuse(http.StatusBadRequest, "revision "+name+" is the service's latest")
			return
		}
		delete(f.revisions, name)
		answer(map[string]any{"name": "op", "done": true})
	default:
		f.t.Errorf("unexpected %s %s", r.Method, path)
		refuse(http.StatusNotImplemented, "unexpected")
	}
}

func (f *fakeGoogleRun) secret(w http.ResponseWriter, r *http.Request, path string, answer func(any)) {
	rest := strings.TrimPrefix(path, "/v1/projects/"+runProject+"/secrets")
	switch {
	case rest == "" && r.Method == http.MethodPost:
		answer(map[string]any{})
	case strings.HasSuffix(rest, ":addVersion"):
		secret := strings.TrimSuffix(strings.TrimPrefix(rest, "/"), ":addVersion")
		f.versions[secret]++
		answer(map[string]string{"name": "projects/123/secrets/" + secret + "/versions/" + strconv.Itoa(f.versions[secret])})
	case strings.HasSuffix(rest, ":setIamPolicy"):
		answer(map[string]any{})
	default:
		f.t.Errorf("unexpected secret call %s %s", r.Method, path)
		w.WriteHeader(http.StatusNotImplemented)
	}
}

type fakeRefusal struct {
	status  int
	message string
}

// patch replaces the service as Cloud Run does.
func (f *fakeGoogleRun) patch(data []byte) *fakeRefusal {
	var in gcp.RunService
	if err := json.Unmarshal(data, &in); err != nil {
		return &fakeRefusal{http.StatusBadRequest, err.Error()}
	}
	for _, c := range in.Template.Containers {
		for _, e := range c.Env {
			if e.ValueSource != nil {
				f.refs = append(f.refs, e.ValueSource.SecretKeyRef.Secret+":"+e.ValueSource.SecretKeyRef.Version)
			}
		}
	}
	changed := !reflect.DeepEqual(templateJSON(in.Template), templateJSON(f.template))
	created := ""
	if changed {
		created = in.Template.Revision
		if created == "" {
			f.generated++
			created = fmt.Sprintf("%s-%05d-gen", "svc", f.generated+1)
		} else if f.revisions[created] != nil {
			return &fakeRefusal{http.StatusConflict, "revision " + created + " already exists"}
		}
	}
	traffic := in.Traffic
	if len(traffic) == 0 {
		traffic = f.traffic
	}
	for _, t := range traffic {
		if t.Type != gcp.TrafficRevision {
			continue
		}
		rev := f.revisions[t.Revision]
		if t.Percent > 0 && (rev == nil || !rev.ready || rev.deleting) {
			return &fakeRefusal{http.StatusBadRequest, "revision " + t.Revision + " can't take traffic"}
		}
	}
	if changed {
		ready := f.failImage == "" || len(in.Template.Containers) == 0 || in.Template.Containers[0].Image != f.failImage
		f.revisions[created] = &fakeRunRevision{template: in.Template, ready: ready}
		f.latestCreated = created
		f.newRevs = append(f.newRevs, created)
		if ready {
			f.latestReady = created
		}
	}
	f.template, f.traffic = in.Template, traffic
	return nil
}

func templateJSON(t gcp.RunTemplate) string {
	data, _ := json.Marshal(t)
	return string(data)
}

// statuses is how the traffic resolves now.
func (f *fakeGoogleRun) statuses() []gcp.RunTraffic {
	out := make([]gcp.RunTraffic, 0, len(f.traffic))
	for _, t := range f.traffic {
		if t.Type == gcp.TrafficLatest {
			t.Revision = f.latestReady
		}
		out = append(out, t)
	}
	return out
}

// share is a revision's percent of the traffic now.
func (f *fakeGoogleRun) share(name string) int {
	n := 0
	for _, t := range f.statuses() {
		if t.Revision == name {
			n += t.Percent
		}
	}
	return n
}

func (f *fakeGoogleRun) view() map[string]any {
	statuses := f.statuses()
	for i := range statuses {
		if f.traffic[i].Type == gcp.TrafficLatest {
			statuses[i].Revision = ""
		}
	}
	full := f.servicePath()[1:] + "/revisions/"
	return map[string]any{
		"name": f.servicePath()[1:], "etag": `"` + strconv.Itoa(len(f.calls)) + `"`, "labels": map[string]string{"managed-by": "loomplane"},
		"ingress": "INGRESS_TRAFFIC_ALL", "template": f.template, "traffic": f.traffic, "trafficStatuses": statuses,
		"latestReadyRevision": full + f.latestReady, "latestCreatedRevision": full + f.latestCreated,
		"uri": "https://" + f.service + ".run.app",
	}
}

// split is how traffic resolves, by revision, without the zeros.
func (f *fakeGoogleRun) split() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for _, t := range f.statuses() {
		if t.Percent > 0 {
			out[t.Revision] += t.Percent
		}
	}
	return out
}

func (f *fakeGoogleRun) current() (gcp.RunTemplate, []gcp.RunTraffic, map[string]*fakeRunRevision) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.template, slices.Clone(f.traffic), maps.Clone(f.revisions)
}

// failNext fails the next call of a kind.
func (f *fakeGoogleRun) failNext(kind string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[kind] = 1
}

// failAfter lets n more calls of a kind through, then fails one.
func (f *fakeGoogleRun) failAfter(kind string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAt[kind] = f.counts[kind] + n + 1
}

// runCanaryEnv is a Cloud Run executor against a fake Google, with a
// bound instance that runs the baseline.
type runCanaryEnv struct {
	g    *fakeGoogleRun
	exec *CloudRun
	inst *instance.Instance
	ctrl *recordingInstances
	job  *Job
}

// recordingInstances is Ctrl Plane's instances, recording updates.
type recordingInstances struct {
	*fakeInstances
	updates   []instance.UpdateRequest
	updateErr error
}

func (r *recordingInstances) Update(_ context.Context, iid id.ID, req instance.UpdateRequest) (*instance.Instance, error) {
	if r.updateErr != nil {
		err := r.updateErr
		r.updateErr = nil
		return nil, err
	}
	r.updates = append(r.updates, req)
	inst := r.byID[iid]
	inst.Services, inst.Labels = req.Services, req.Labels
	return inst, nil
}

func newRunCanaryEnv(t *testing.T) *runCanaryEnv {
	t.Helper()
	inst := ctrlInstance("api-production-x1", cloudrun.Key(runConnection, runRegion))
	inst.TenantID = "org_1"
	inst.Services = []cpprovider.ServiceSpec{{Name: mainService, Image: runOldImage, Env: map[string]string{"MODE": "slow"}, Ports: []cpprovider.PortSpec{{Container: 8080}}}}
	inst.Labels = map[string]string{cloudrun.LabelPublic: "true", cloudrun.LabelSecret + "TOKEN": runSecret + ":1"}
	inst.Endpoints = []cpprovider.Endpoint{{URL: "https://api.run.app"}}
	baseline, err := cloudrun.Service(inst.Services[0], inst.Labels)
	if err != nil {
		t.Fatal(err)
	}
	g := newFakeGoogleRun(t, cloudrun.ServiceName(inst.ID), baseline)
	g.versions[runSecret] = 1
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	ctrl := &recordingInstances{fakeInstances: &fakeInstances{byID: map[id.ID]*instance.Instance{inst.ID: inst}}}
	exec := &CloudRun{Google: gcp.New(srv.URL), Ctrl: CtrlPlane{Providers: cpprovider.NewRegistry(), Instances: ctrl}}
	job := &Job{
		Action: planning.Action{
			ID: "service:api", Name: "api", Region: runRegion, Provider: "cloud-run",
			Service: &planning.ServiceConfig{Port: 8080, Public: true},
		},
		Deployment: &Deployment{ID: runDeployment, OrgID: "org_1", ProjectID: "proj_01h2xcejqtf2nbrexx3vqjhp41", Environment: "production"},
		Connection: &connections.Connection{ID: runConnection, Name: "Prod"},
		Credential: serviceAccountKey(t, srv.URL+"/token"),
		Binding:    &planning.Binding{ExternalID: inst.ID.String(), Region: runRegion},
		Env:        map[string]string{"MODE": "fast", "TOKEN": "s3cret"},
		Secrets:    map[string]bool{"TOKEN": true},
		// The image is built already, as an earlier attempt checkpointed it.
		Resume:     []byte(`{"image":"` + runNewImage + `"}`),
		Checkpoint: func([]byte) error { return nil },
		Progress:   func(string, string) {},
	}
	return &runCanaryEnv{g: g, exec: exec, inst: inst, ctrl: ctrl, job: job}
}

func (e *runCanaryEnv) start(t *testing.T) CanaryTarget {
	t.Helper()
	target, err := e.exec.ApplyCandidate(t.Context(), e.job)
	if err != nil {
		t.Fatal(err)
	}
	return *target
}

// assertBaselineAlone checks the baseline takes everything, the template
// makes the baseline's configuration again, with its own secret version,
// and the candidate is gone.
func (e *runCanaryEnv) assertBaselineAlone(t *testing.T, candidate string) {
	t.Helper()
	if split := e.g.split(); !maps.Equal(split, map[string]int{runBaseline: 100}) {
		t.Fatalf("traffic %v", split)
	}
	tmpl, _, revisions := e.g.current()
	if tmpl.Revision != "" || tmpl.Containers[0].Image != runOldImage || tokenVersion(tmpl) != "1" || plainEnv(tmpl)["MODE"] != "slow" {
		t.Fatalf("the template isn't the baseline's: %+v", tmpl)
	}
	if candidate != "" && revisions[candidate] != nil {
		t.Fatalf("candidate %s is still there", candidate)
	}
	if revisions[runBaseline] == nil {
		t.Fatal("the baseline revision is gone")
	}
}

// tokenVersion is the Secret Manager version a template reads TOKEN at.
func tokenVersion(t gcp.RunTemplate) string {
	for _, e := range t.Containers[0].Env {
		if e.Name == "TOKEN" && e.ValueSource != nil {
			return e.ValueSource.SecretKeyRef.Version
		}
	}
	return ""
}

func plainEnv(t gcp.RunTemplate) map[string]string {
	out := map[string]string{}
	for _, e := range t.Containers[0].Env {
		if e.ValueSource == nil {
			out[e.Name] = e.Value
		}
	}
	return out
}

func TestCloudRunCanaryStartsTheCandidateDark(t *testing.T) {
	e := newRunCanaryEnv(t)
	target := e.start(t)
	want := canaryRevision(e.g.service, runDeployment, 0)
	if target.Baseline != runBaseline || target.Candidate != want || target.Image != runNewImage {
		t.Fatalf("target %+v", target)
	}
	if split := e.g.split(); !maps.Equal(split, map[string]int{runBaseline: 100}) {
		t.Fatalf("traffic %v", split)
	}
	tmpl, _, revisions := e.g.current()
	cand := revisions[want]
	if cand == nil || !cand.ready || tmpl.Revision != want || cand.template.Containers[0].Image != runNewImage {
		t.Fatalf("candidate %+v, template %+v", cand, tmpl)
	}
	// The candidate reads a version of its own; the baseline still reads
	// the one it started with.
	if tokenVersion(cand.template) != "2" || tokenVersion(revisions[runBaseline].template) != "1" {
		t.Fatalf("candidate reads %q, baseline %q", tokenVersion(cand.template), tokenVersion(revisions[runBaseline].template))
	}
	if len(e.ctrl.updates) != 0 {
		t.Fatalf("Ctrl Plane was told %+v before the candidate was promoted", e.ctrl.updates)
	}
	if target.Refs[runRefRegion] != runRegion || target.Refs[runRefService] != e.g.service || target.Refs[cloudrun.LabelSecret+"TOKEN"] != runSecret+":2" {
		t.Fatalf("refs %v", target.Refs)
	}
}

// Secret Manager's "latest" would hand a baseline that starts a new
// instance whatever the candidate stored. Every reference is pinned.
func TestCloudRunCanarySecretsNeverReachTheBaseline(t *testing.T) {
	e := newRunCanaryEnv(t)
	target := e.start(t)
	if err := e.exec.SetWeights(t.Context(), e.job, target, 25); err != nil {
		t.Fatal(err)
	}
	if err := e.exec.Revert(t.Context(), e.job, target); err != nil {
		t.Fatal(err)
	}
	e.assertBaselineAlone(t, target.Candidate)
	for _, ref := range e.g.refs {
		if _, version, _ := strings.Cut(ref, ":"); version == "latest" || version == "" {
			t.Fatalf("a template read %q", ref)
		}
	}
	_, _, revisions := e.g.current()
	if tokenVersion(revisions[runBaseline].template) != "1" {
		t.Fatal("the baseline revision reads another version")
	}
}

func TestCloudRunCanaryApplyCandidateIsSafeToRepeat(t *testing.T) {
	e := newRunCanaryEnv(t)
	first := e.start(t)
	second := e.start(t)
	// Each start stores its secrets again, so the second is another
	// revision, and the first goes.
	if second.Candidate == first.Candidate || !strings.HasPrefix(second.Candidate, first.Candidate+"-") {
		t.Fatalf("first %s, second %s", first.Candidate, second.Candidate)
	}
	_, _, revisions := e.g.current()
	if revisions[first.Candidate] != nil || revisions[second.Candidate] == nil || len(revisions) != 2 {
		t.Fatalf("revisions %v", slices.Sorted(maps.Keys(revisions)))
	}
	if split := e.g.split(); !maps.Equal(split, map[string]int{runBaseline: 100}) {
		t.Fatalf("traffic %v", split)
	}
}

// An attempt that stopped once its candidate was up, without returning,
// leaves it named in the template. The next attempt replaces it.
func TestCloudRunCanaryStartReplacesWhatAnEarlierAttemptLeft(t *testing.T) {
	e := newRunCanaryEnv(t)
	first := e.start(t)
	// The earlier attempt's candidate got some traffic before the
	// deployment failed and its revert didn't finish; the retry ramps from
	// the baseline the canary recorded.
	if err := e.exec.SetWeights(t.Context(), e.job, first, 50); err != nil {
		t.Fatal(err)
	}
	e.job.Deployment.Retries = 1
	e.job.Deployment.Canary = &Canary{Services: []CanaryService{{ActionID: e.job.Action.ID, Baseline: runBaseline}}}
	second := e.start(t)
	if second.Baseline != runBaseline || second.Candidate != canaryRevision(e.g.service, runDeployment, 1) {
		t.Fatalf("second %+v", second)
	}
	_, _, revisions := e.g.current()
	if revisions[first.Candidate] != nil {
		t.Fatalf("the earlier candidate %s is still there", first.Candidate)
	}
	if split := e.g.split(); !maps.Equal(split, map[string]int{runBaseline: 100}) {
		t.Fatalf("traffic %v", split)
	}
}

func TestCloudRunCanaryUsesTheRecordedBaseline(t *testing.T) {
	e := newRunCanaryEnv(t)
	first := e.start(t)
	if err := e.exec.SetWeights(t.Context(), e.job, first, 70); err != nil {
		t.Fatal(err)
	}
	// The earlier candidate has most of the traffic now, but it isn't the
	// baseline.
	e.job.Deployment.Retries = 1
	e.job.Deployment.Canary = &Canary{Services: []CanaryService{{ActionID: e.job.Action.ID, Baseline: runBaseline}}}
	second := e.start(t)
	if second.Baseline != runBaseline {
		t.Fatalf("baseline %s", second.Baseline)
	}
	// Without a record, a split can't say which is the baseline.
	e.job.Deployment.Canary = nil
	svc := &gcp.RunService{TrafficStatuses: []gcp.RunTraffic{{Type: gcp.TrafficRevision, Revision: first.Candidate, Percent: 70}, {Type: gcp.TrafficRevision, Revision: runBaseline, Percent: 30}}}
	if got, err := canaryBaseline(e.job, e.g.service, svc); err == nil {
		t.Fatalf("unrecorded: %s", got)
	}
	// Nor can a recorded baseline that takes no traffic any more.
	e.job.Deployment.Canary = &Canary{Services: []CanaryService{{ActionID: e.job.Action.ID, Baseline: "svc-gone"}}}
	if got, err := canaryBaseline(e.job, e.g.service, svc); err == nil {
		t.Fatalf("a recorded baseline without traffic: %s", got)
	}
}

func TestCloudRunCanaryAFailedStartCleansUp(t *testing.T) {
	for name, setup := range map[string]func(*runCanaryEnv){
		"the candidate doesn't start": func(e *runCanaryEnv) { e.g.failImage = runNewImage },
		// The first read finds the name free, the second checks the
		// candidate started.
		"its readiness can't be read": func(e *runCanaryEnv) { e.g.failAt["GET revision"] = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			e := newRunCanaryEnv(t)
			setup(e)
			if _, err := e.exec.ApplyCandidate(t.Context(), e.job); err == nil {
				t.Fatal("the start didn't fail")
			}
			e.assertBaselineAlone(t, canaryRevision(e.g.service, runDeployment, 0))
		})
	}
}

func TestCloudRunCanaryAFailedCleanupSaysWhatMayBeLeft(t *testing.T) {
	e := newRunCanaryEnv(t)
	e.g.failImage = runNewImage
	e.g.fail["DELETE revision"] = 100
	_, err := e.exec.ApplyCandidate(t.Context(), e.job)
	candidate := canaryRevision(e.g.service, runDeployment, 0)
	for _, want := range []string{"isn't ready", "Cleaning up failed too", candidate, e.g.service, "may be left", "the fake failed DELETE revision"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("error %v doesn't say %q", err, want)
		}
	}
	// What it could do, it did: the baseline has everything back.
	if split := e.g.split(); !maps.Equal(split, map[string]int{runBaseline: 100}) {
		t.Fatalf("traffic %v", split)
	}
}

func TestCloudRunCanaryWeighs(t *testing.T) {
	e := newRunCanaryEnv(t)
	target := e.start(t)
	for _, tc := range []struct {
		percent int
		want    map[string]int
	}{
		{25, map[string]int{runBaseline: 75, target.Candidate: 25}},
		{25, map[string]int{runBaseline: 75, target.Candidate: 25}},
		{100, map[string]int{target.Candidate: 100}},
		{0, map[string]int{runBaseline: 100}},
		{0, map[string]int{runBaseline: 100}},
	} {
		if err := e.exec.SetWeights(t.Context(), e.job, target, tc.percent); err != nil {
			t.Fatalf("%d%%: %v", tc.percent, err)
		}
		if split := e.g.split(); !maps.Equal(split, tc.want) {
			t.Fatalf("%d%%: traffic %v", tc.percent, split)
		}
	}
	// Moving traffic makes no revision.
	tmpl, _, revisions := e.g.current()
	if tmpl.Revision != target.Candidate || len(revisions) != 2 {
		t.Fatalf("template %s, revisions %v", tmpl.Revision, slices.Sorted(maps.Keys(revisions)))
	}
	if err := e.exec.SetWeights(t.Context(), e.job, target, 101); err == nil {
		t.Fatal("101% was given")
	}
}

func TestCloudRunCanaryGivesNoTrafficToACandidateThatIsntUp(t *testing.T) {
	e := newRunCanaryEnv(t)
	target := e.start(t)
	e.g.mu.Lock()
	e.g.revisions[target.Candidate].deleting = true
	e.g.mu.Unlock()
	if err := e.exec.SetWeights(t.Context(), e.job, target, 25); err == nil || !strings.Contains(err.Error(), "being deleted") {
		t.Fatalf("weighed onto a revision being deleted: %v", err)
	}
	// The revert's 0% still goes through, and so does it once the
	// candidate's gone.
	if err := e.exec.SetWeights(t.Context(), e.job, target, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.exec.Revert(t.Context(), e.job, target); err != nil {
		t.Fatal(err)
	}
	if err := e.exec.SetWeights(t.Context(), e.job, target, 0); err != nil {
		t.Fatalf("0%% once the candidate is gone: %v", err)
	}
	e.assertBaselineAlone(t, target.Candidate)
}

func TestCloudRunCanaryPromote(t *testing.T) {
	e := newRunCanaryEnv(t)
	target := e.start(t)
	if err := e.exec.SetWeights(t.Context(), e.job, target, 50); err != nil {
		t.Fatal(err)
	}
	_, _, before := e.g.current()
	for i := range 2 {
		out, err := e.exec.Promote(t.Context(), e.job, target)
		if err != nil {
			t.Fatalf("promote %d: %v", i+1, err)
		}
		if out.ExternalID != e.inst.ID.String() || out.Image != runNewImage || out.Revision != runNewImage || out.URL != "https://api.run.app" {
			t.Fatalf("outcome %+v", out)
		}
	}
	tmpl, traffic, after := e.g.current()
	if !reflect.DeepEqual(traffic, []gcp.RunTraffic{{Type: gcp.TrafficLatest, Percent: 100}}) {
		t.Fatalf("traffic %+v", traffic)
	}
	if split := e.g.split(); !maps.Equal(split, map[string]int{target.Candidate: 100}) {
		t.Fatalf("latest isn't the candidate: %v", split)
	}
	if tmpl.Revision != target.Candidate || len(after) != len(before) {
		t.Fatalf("promoting made a revision: template %s, revisions %v", tmpl.Revision, slices.Sorted(maps.Keys(after)))
	}
	// Ctrl Plane records the release as the candidate ran it, without
	// deploying it again.
	last := e.ctrl.updates[len(e.ctrl.updates)-1]
	if last.Services[0].Image != runNewImage || last.Services[0].Env["MODE"] != "fast" || last.Labels[cloudrun.LabelSecret+"TOKEN"] != runSecret+":2" {
		t.Fatalf("Ctrl Plane got %+v", last)
	}
	if _, ok := last.Services[0].Env["TOKEN"]; ok {
		t.Fatal("a secret value went to Ctrl Plane")
	}
	if len(e.g.invokers) == 0 || !slices.Equal(e.g.invokers[len(e.g.invokers)-1], []string{gcp.Public}) {
		t.Fatalf("invokers %v", e.g.invokers)
	}
	// What Observe reads now is the release.
	obs, err := e.exec.Observe(t.Context(), nil, e.job.Credential, &planning.Binding{ExternalID: e.inst.ID.String(), Region: runRegion})
	if err != nil || obs.Revision != runNewImage {
		t.Fatalf("observed %+v, %v", obs, err)
	}
}

func TestCloudRunCanaryPromoteResumesAfterAPartialRun(t *testing.T) {
	for name, fail := range map[string]func(*runCanaryEnv){
		"reading the candidate": func(e *runCanaryEnv) { e.g.failNext("GET revision") },
		"moving the traffic":    func(e *runCanaryEnv) { e.g.failNext("PATCH service") },
		"recording the release": func(e *runCanaryEnv) { e.ctrl.updateErr = errors.New("ctrl plane is down") },
	} {
		t.Run(name, func(t *testing.T) {
			e := newRunCanaryEnv(t)
			target := e.start(t)
			if err := e.exec.SetWeights(t.Context(), e.job, target, 50); err != nil {
				t.Fatal(err)
			}
			fail(e)
			if _, err := e.exec.Promote(t.Context(), e.job, target); err == nil {
				t.Fatal("the promote didn't fail")
			}
			if _, err := e.exec.Promote(t.Context(), e.job, target); err != nil {
				t.Fatalf("the retry: %v", err)
			}
			if split := e.g.split(); !maps.Equal(split, map[string]int{target.Candidate: 100}) {
				t.Fatalf("traffic %v", split)
			}
			if len(e.ctrl.updates) != 1 {
				t.Fatalf("Ctrl Plane updates %d", len(e.ctrl.updates))
			}
		})
	}
}

func TestCloudRunCanaryPromoteRefusesACandidateThatIsntUp(t *testing.T) {
	for name, spoil := range map[string]func(f *fakeGoogleRun, candidate string){
		"being deleted": func(f *fakeGoogleRun, candidate string) { f.revisions[candidate].deleting = true },
		"not ready":     func(f *fakeGoogleRun, candidate string) { f.revisions[candidate].ready = false },
		"gone": func(f *fakeGoogleRun, candidate string) {
			delete(f.revisions, candidate)
			f.template, f.latestCreated, f.latestReady = f.revisions[runBaseline].template, runBaseline, runBaseline
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newRunCanaryEnv(t)
			target := e.start(t)
			if err := e.exec.SetWeights(t.Context(), e.job, target, 25); err != nil {
				t.Fatal(err)
			}
			e.g.mu.Lock()
			spoil(e.g, target.Candidate)
			e.g.mu.Unlock()
			_, err := e.exec.Promote(t.Context(), e.job, target)
			if err == nil || !strings.Contains(err.Error(), "nothing was promoted") || !strings.Contains(err.Error(), "The baseline has all of the traffic") {
				t.Fatalf("promoted: %v", err)
			}
			if split := e.g.split(); !maps.Equal(split, map[string]int{runBaseline: 100}) {
				t.Fatalf("traffic %v", split)
			}
			if len(e.ctrl.updates) != 0 {
				t.Fatalf("Ctrl Plane recorded %+v", e.ctrl.updates)
			}
		})
	}
}

// A candidate that's gone while what serves everything runs the release
// already means an earlier promote got that far: it finishes.
func TestCloudRunCanaryPromoteFinishesWhenWhatServesRunsTheRelease(t *testing.T) {
	e := newRunCanaryEnv(t)
	target := e.start(t)
	e.g.mu.Lock()
	rel := e.g.revisions[target.Candidate].template
	rel.Revision = ""
	e.g.revisions["svc-00009-rel"] = &fakeRunRevision{template: rel, ready: true}
	delete(e.g.revisions, target.Candidate)
	e.g.template, e.g.latestCreated, e.g.latestReady = rel, "svc-00009-rel", "svc-00009-rel"
	e.g.traffic = []gcp.RunTraffic{{Type: gcp.TrafficLatest, Percent: 100}}
	e.g.mu.Unlock()
	if _, err := e.exec.Promote(t.Context(), e.job, target); err != nil {
		t.Fatal(err)
	}
	if len(e.ctrl.updates) != 1 || e.ctrl.updates[0].Services[0].Image != runNewImage {
		t.Fatalf("Ctrl Plane %+v", e.ctrl.updates)
	}
	if split := e.g.split(); !maps.Equal(split, map[string]int{"svc-00009-rel": 100}) {
		t.Fatalf("traffic %v", split)
	}
}

func TestCloudRunCanaryPromoteRefusesWhenTheLatestIsntTheCandidate(t *testing.T) {
	e := newRunCanaryEnv(t)
	target := e.start(t)
	e.g.mu.Lock()
	other := e.g.revisions[runBaseline].template
	other.Labels = map[string]string{"someone": "else"}
	e.g.revisions["svc-00010-new"] = &fakeRunRevision{template: other, ready: true}
	e.g.template, e.g.latestCreated, e.g.latestReady = other, "svc-00010-new", "svc-00010-new"
	e.g.mu.Unlock()
	if _, err := e.exec.Promote(t.Context(), e.job, target); err == nil || !strings.Contains(err.Error(), "nothing was promoted") {
		t.Fatalf("promoted: %v", err)
	}
	if split := e.g.split(); !maps.Equal(split, map[string]int{runBaseline: 100}) {
		t.Fatalf("traffic %v", split)
	}
}

func TestCloudRunCanaryRevertIsSafeToRepeat(t *testing.T) {
	e := newRunCanaryEnv(t)
	target := e.start(t)
	if err := e.exec.SetWeights(t.Context(), e.job, target, 50); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err := e.exec.Revert(t.Context(), e.job, target); err != nil {
			t.Fatalf("revert %d: %v", i+1, err)
		}
		e.assertBaselineAlone(t, target.Candidate)
	}
	// The restored template made one revision, once.
	if len(e.g.newRevs) != 2 {
		t.Fatalf("new revisions %v", e.g.newRevs)
	}
	obs, err := e.exec.Observe(t.Context(), nil, e.job.Credential, &planning.Binding{ExternalID: e.inst.ID.String(), Region: runRegion})
	if err != nil || obs.Revision != runOldImage {
		t.Fatalf("observed %+v, %v: drift would see the candidate", obs, err)
	}
	if len(e.ctrl.updates) != 0 {
		t.Fatalf("a revert told Ctrl Plane %+v", e.ctrl.updates)
	}
}

func TestCloudRunCanaryRevertResumesAfterAPartialRun(t *testing.T) {
	for name, fail := range map[string]func(*fakeGoogleRun){
		"moving the traffic":                func(f *fakeGoogleRun) { f.failNext("PATCH service") },
		"reading the service once it moved": func(f *fakeGoogleRun) { f.failAfter("GET service", 1) },
		"restoring the template":            func(f *fakeGoogleRun) { f.failAfter("PATCH service", 1) },
		"deleting the candidate":            func(f *fakeGoogleRun) { f.failNext("DELETE revision") },
	} {
		t.Run(name, func(t *testing.T) {
			e := newRunCanaryEnv(t)
			target := e.start(t)
			if err := e.exec.SetWeights(t.Context(), e.job, target, 50); err != nil {
				t.Fatal(err)
			}
			fail(e.g)
			if err := e.exec.Revert(t.Context(), e.job, target); err == nil {
				t.Fatal("the revert didn't fail")
			}
			// The runner's retry: 0%, then the revert again.
			if err := e.exec.SetWeights(t.Context(), e.job, target, 0); err != nil {
				t.Fatal(err)
			}
			if err := e.exec.Revert(t.Context(), e.job, target); err != nil {
				t.Fatalf("the retry: %v", err)
			}
			e.assertBaselineAlone(t, target.Candidate)
		})
	}
}

func TestCloudRunCanaryNeedsThePermissionsToSplitTraffic(t *testing.T) {
	e := newRunCanaryEnv(t)
	e.g.granted = []string{runAllowedPerm}
	_, err := e.exec.ApplyCandidate(t.Context(), e.job)
	for _, want := range []string{"api can't run a canary", "Prod", "run.revisions.delete", "direct or blue_green"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("error %v doesn't say %q", err, want)
		}
	}
	if len(e.g.asked) != 1 || !slices.Equal(e.g.asked[0], gcp.CanaryPermissions) {
		t.Fatalf("asked %v", e.g.asked)
	}
	for _, c := range e.g.calls {
		if strings.HasPrefix(c, http.MethodPatch) || strings.Contains(c, "/secrets") {
			t.Fatalf("a refused canary called %s", c)
		}
	}
	e.g.granted = slices.Clone(gcp.CanaryPermissions)
	if _, err = e.exec.ApplyCandidate(t.Context(), e.job); err != nil {
		t.Fatalf("allowed: %v", err)
	}
}

func TestCloudRunCanaryNeedsSomethingRunning(t *testing.T) {
	e := newRunCanaryEnv(t)
	e.job.Binding = nil
	if _, err := e.exec.ApplyCandidate(t.Context(), e.job); !errors.Is(err, ErrNoExecutor) {
		t.Fatalf("nothing bound: %v", err)
	}
	if len(e.g.calls) != 0 {
		t.Fatalf("calls %v", e.g.calls)
	}
}

// ordinaryDeploy deploys image the way a direct release does, through the
// Cloud Run provider Ctrl Plane calls.
func (e *runCanaryEnv) ordinaryDeploy(t *testing.T, image string) {
	t.Helper()
	session := func(ctx context.Context) (*gcp.Session, error) {
		key, err := gcp.ParseKey(e.job.Credential)
		if err != nil {
			return nil, err
		}
		return e.exec.Google.Session(ctx, key)
	}
	p := cloudrun.New(runConnection, runRegion, session, e.ctrl)
	if _, err := p.Deploy(t.Context(), cpprovider.DeployRequest{InstanceID: e.inst.ID, Services: []cpprovider.ServiceDeploySpec{{Name: mainService, Image: image}}}); err != nil {
		t.Fatal(err)
	}
}

// Whatever a canary left pinned, the next ordinary deploy gives its new
// revision everything, without relying on Cloud Run's default.
func TestCloudRunCanaryAnOrdinaryDeployAfterwardTakesEverything(t *testing.T) {
	for name, after := range map[string]func(*testing.T, *runCanaryEnv){
		"a revert": func(t *testing.T, e *runCanaryEnv) {
			target := e.start(t)
			if err := e.exec.Revert(t.Context(), e.job, target); err != nil {
				t.Fatal(err)
			}
		},
		"a failed start": func(t *testing.T, e *runCanaryEnv) {
			e.g.failImage = runNewImage
			if _, err := e.exec.ApplyCandidate(t.Context(), e.job); err == nil {
				t.Fatal("the start didn't fail")
			}
			e.g.failImage = ""
		},
		"a refused promote": func(t *testing.T, e *runCanaryEnv) {
			target := e.start(t)
			e.g.mu.Lock()
			e.g.revisions[target.Candidate].ready = false
			e.g.mu.Unlock()
			if _, err := e.exec.Promote(t.Context(), e.job, target); err == nil {
				t.Fatal("the promote didn't fail")
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newRunCanaryEnv(t)
			after(t, e)
			if split := e.g.split(); !maps.Equal(split, map[string]int{runBaseline: 100}) {
				t.Fatalf("before the deploy: %v", split)
			}
			e.ordinaryDeploy(t, "europe-west1-docker.pkg.dev/shop/loomplane/api:next")
			_, traffic, _ := e.g.current()
			if !reflect.DeepEqual(traffic, cloudrun.Latest()) {
				t.Fatalf("traffic %+v", traffic)
			}
			e.g.mu.Lock()
			latest := e.g.latestReady
			e.g.mu.Unlock()
			if split := e.g.split(); latest == runBaseline || !maps.Equal(split, map[string]int{latest: 100}) {
				t.Fatalf("the deploy's revision %s doesn't take everything: %v", latest, split)
			}
		})
	}
}

// The commit, and so the image, can stay the same while secrets, scale or
// size change. A revision with that image doesn't run the release.
func TestCloudRunCanaryPromoteWontFinishOnARevisionThatOnlySharesTheImage(t *testing.T) {
	for name, change := range map[string]func(*gcp.RunTemplate){
		"secret version": func(t *gcp.RunTemplate) {
			for i := range t.Containers[0].Env {
				if vs := t.Containers[0].Env[i].ValueSource; vs != nil {
					vs.SecretKeyRef.Version = "1"
				}
			}
		},
		"scale": func(t *gcp.RunTemplate) { t.Scaling = &gcp.RunScaling{Min: 2, Max: 4} },
		"size": func(t *gcp.RunTemplate) {
			t.Containers[0].Resources = &gcp.RunResources{Limits: map[string]string{"cpu": "2"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newRunCanaryEnv(t)
			target := e.start(t)
			e.g.mu.Lock()
			data, _ := json.Marshal(e.g.revisions[target.Candidate].template)
			var serving gcp.RunTemplate
			json.Unmarshal(data, &serving)
			serving.Revision = ""
			change(&serving)
			e.g.revisions["svc-00009-old"] = &fakeRunRevision{template: serving, ready: true}
			e.g.revisions[target.Candidate].ready = false
			e.g.traffic = []gcp.RunTraffic{{Type: gcp.TrafficRevision, Revision: "svc-00009-old", Percent: 100}}
			e.g.mu.Unlock()
			for i := range 2 {
				if _, err := e.exec.Promote(t.Context(), e.job, target); err == nil || !strings.Contains(err.Error(), "nothing was promoted") {
					t.Fatalf("promote %d: %v", i+1, err)
				}
			}
			if len(e.ctrl.updates) != 0 {
				t.Fatalf("Ctrl Plane recorded a release that never ran: %+v", e.ctrl.updates)
			}
		})
	}
}

// One canary ends revert_failed with its candidate holding most of the
// traffic. A later deployment's canary has no baseline recorded, and the
// revision with most of the traffic is that failed candidate.
func TestCloudRunCanaryRefusesTrafficAnEarlierCanaryLeft(t *testing.T) {
	for name, leave := range map[string]func(*testing.T, *runCanaryEnv, CanaryTarget){
		"split": func(t *testing.T, e *runCanaryEnv, first CanaryTarget) {
			if err := e.exec.SetWeights(t.Context(), e.job, first, 70); err != nil {
				t.Fatal(err)
			}
		},
		"pinned to the candidate": func(t *testing.T, e *runCanaryEnv, first CanaryTarget) {
			if err := e.exec.SetWeights(t.Context(), e.job, first, 100); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newRunCanaryEnv(t)
			first := e.start(t)
			leave(t, e, first)
			before := e.g.split()
			e.job.Deployment = &Deployment{ID: "dep_01jxlater000000000000000000", OrgID: "org_1", ProjectID: e.job.Deployment.ProjectID, Environment: "production"}
			calls := len(e.g.calls)
			_, err := e.exec.ApplyCandidate(t.Context(), e.job)
			for _, want := range []string{"can't start a canary", first.Candidate, fmt.Sprintf("%s %d%%", first.Candidate, before[first.Candidate]), "Settle that canary's traffic first"} {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("error %v doesn't say %q", err, want)
				}
			}
			if split := e.g.split(); !maps.Equal(split, before) {
				t.Fatalf("traffic moved: %v, was %v", split, before)
			}
			for _, c := range e.g.calls[calls:] {
				if !strings.HasPrefix(c, http.MethodGet) && !strings.Contains(c, "testIamPermissions") && c != "POST /token" {
					t.Fatalf("a refused canary called %s", c)
				}
			}
			_, _, revisions := e.g.current()
			if revisions[runBaseline] == nil {
				t.Fatal("the true baseline was deleted")
			}
		})
	}
}

// A clean revert leaves the baseline pinned under its own template, which
// is settled: the next deployment's canary starts from it.
func TestCloudRunCanaryStartsAfterAnotherDeploymentReverted(t *testing.T) {
	e := newRunCanaryEnv(t)
	first := e.start(t)
	if err := e.exec.Revert(t.Context(), e.job, first); err != nil {
		t.Fatal(err)
	}
	e.job.Deployment = &Deployment{ID: "dep_01jxlater000000000000000000", OrgID: "org_1", ProjectID: e.job.Deployment.ProjectID, Environment: "production"}
	if second := e.start(t); second.Baseline != runBaseline {
		t.Fatalf("baseline %s", second.Baseline)
	}
}

// A start that failed and cleaned up recorded no baseline, and the retry
// still starts.
func TestCloudRunCanaryStartsAgainAfterAFailedStart(t *testing.T) {
	e := newRunCanaryEnv(t)
	e.g.failImage = runNewImage
	if _, err := e.exec.ApplyCandidate(t.Context(), e.job); err == nil {
		t.Fatal("the start didn't fail")
	}
	e.g.failImage = ""
	if target := e.start(t); target.Baseline != runBaseline {
		t.Fatalf("baseline %s", target.Baseline)
	}
}

// An earlier candidate that can't be deleted takes no traffic, so the new
// candidate stays up and the leftover is noted.
func TestCloudRunCanaryKeepsTheCandidateWhenAnEarlierOneWontGo(t *testing.T) {
	e := newRunCanaryEnv(t)
	first := e.start(t)
	var notes []string
	e.job.Progress = func(_, m string) { notes = append(notes, m) }
	e.g.failNext("DELETE revision")
	second := e.start(t)
	_, _, revisions := e.g.current()
	if revisions[second.Candidate] == nil || revisions[first.Candidate] == nil {
		t.Fatalf("revisions %v", slices.Sorted(maps.Keys(revisions)))
	}
	if !slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, first.Candidate) && strings.Contains(n, "left") }) {
		t.Fatalf("notes %v", notes)
	}
	if split := e.g.split(); !maps.Equal(split, map[string]int{runBaseline: 100}) {
		t.Fatalf("traffic %v", split)
	}
}
