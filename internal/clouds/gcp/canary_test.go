package gcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const testServicePath = "/v2/projects/project-test/locations/europe-west1/services/api"

// runCall is one request the fake Cloud Run saw.
type runCall struct {
	Method, Path string
	Body         map[string]json.RawMessage
}

// fakeRun answers Cloud Run's service, revision and operation calls.
// Every mutation returns an operation that's done on its second read.
type fakeRun struct {
	t       *testing.T
	service string // JSON GET returns for the service
	calls   []runCall
	// opReads counts reads of the pending operation.
	opReads int
	// missing are revision paths that answer 404.
	missing map[string]bool
}

func (f *fakeRun) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	call := runCall{Method: r.Method, Path: r.URL.Path}
	if data, _ := io.ReadAll(r.Body); len(data) > 0 {
		if err := json.Unmarshal(data, &call.Body); err != nil {
			f.t.Errorf("%s %s: body isn't JSON: %v", r.Method, r.URL.Path, err)
		}
	}
	f.calls = append(f.calls, call)
	switch {
	case r.URL.Path == "/v2/projects/project-test/locations/europe-west1/operations/op-1":
		f.opReads++
		w.Write([]byte(`{"name":"projects/project-test/locations/europe-west1/operations/op-1","done":true}`))
	case f.missing[r.URL.Path]:
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
	case r.Method == http.MethodGet && r.URL.Path == testServicePath:
		w.Write([]byte(f.service))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, testServicePath+"/revisions/"):
		name := strings.TrimPrefix(r.URL.Path, testServicePath+"/revisions/")
		w.Write([]byte(`{"name":"projects/project-test/locations/europe-west1/services/api/revisions/` + name + `",
			"conditions":[{"type":"Ready","state":"CONDITION_SUCCEEDED"}]}`))
	case r.Method == http.MethodPatch || r.Method == http.MethodDelete:
		w.Write([]byte(`{"name":"projects/project-test/locations/europe-west1/operations/op-1","done":false}`))
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (f *fakeRun) session(t *testing.T) *Session {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return metricsSession(srv)
}

// patches are the bodies of every PATCH of the service.
func (f *fakeRun) patches() []map[string]json.RawMessage {
	var out []map[string]json.RawMessage
	for _, c := range f.calls {
		if c.Method == http.MethodPatch && c.Path == testServicePath {
			out = append(out, c.Body)
		}
	}
	return out
}

func trafficOf(t *testing.T, body map[string]json.RawMessage) []RunTraffic {
	t.Helper()
	var traffic []RunTraffic
	if err := json.Unmarshal(body["traffic"], &traffic); err != nil {
		t.Fatalf("traffic %s: %v", body["traffic"], err)
	}
	return traffic
}

func templateOf(t *testing.T, body map[string]json.RawMessage) map[string]any {
	t.Helper()
	var tmpl map[string]any
	if err := json.Unmarshal(body["template"], &tmpl); err != nil {
		t.Fatalf("template %s: %v", body["template"], err)
	}
	return tmpl
}

const servingService = `{
	"name": "projects/project-test/locations/europe-west1/services/api",
	"etag": "\"abc\"",
	"labels": {"managed-by": "loomplane"},
	"ingress": "INGRESS_TRAFFIC_ALL",
	"template": {
		"revision": "api-c1a2b3c4-0",
		"serviceAccount": "123-compute@developer.gserviceaccount.com",
		"containers": [{"image": "europe-west1-docker.pkg.dev/p/loomplane/api:new",
			"env": [{"name": "MODE", "value": "fast"},
				{"name": "TOKEN", "valueSource": {"secretKeyRef": {"secret": "lp-api-TOKEN", "version": "4"}}}]}]
	},
	"traffic": [{"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION", "revision": "api-00007-abc", "percent": 100}],
	"trafficStatuses": [{"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION", "revision": "api-00007-abc", "percent": 100}],
	"latestReadyRevision": "projects/project-test/locations/europe-west1/services/api/revisions/api-c1a2b3c4-0",
	"latestCreatedRevision": "projects/project-test/locations/europe-west1/services/api/revisions/api-c1a2b3c4-0"
}`

func TestApplyCandidateRevisionPinsTheBaseline(t *testing.T) {
	f := &fakeRun{t: t, service: servingService}
	svc := RunService{
		Labels: map[string]string{"managed-by": "loomplane"}, Ingress: "INGRESS_TRAFFIC_ALL",
		Template: RunTemplate{Revision: "api-c1a2b3c4-0", Containers: []RunContainer{{Image: "europe-west1-docker.pkg.dev/p/loomplane/api:new"}}},
		Traffic:  []RunTraffic{{Type: TrafficRevision, Revision: "api-00007-abc", Percent: 100}},
	}
	if _, err := f.session(t).ApplyCandidateRevision(t.Context(), "europe-west1", "api", svc); err != nil {
		t.Fatal(err)
	}
	patches := f.patches()
	if len(patches) != 1 {
		t.Fatalf("patches: %d (calls %+v)", len(patches), f.calls)
	}
	if got := templateOf(t, patches[0])["revision"]; got != "api-c1a2b3c4-0" {
		t.Fatalf("template.revision %v", got)
	}
	want := []RunTraffic{{Type: TrafficRevision, Revision: "api-00007-abc", Percent: 100}}
	if got := trafficOf(t, patches[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("traffic %+v", got)
	}
	if f.opReads != 1 {
		t.Fatalf("the operation was read %d times, so it wasn't waited for", f.opReads)
	}
	// It checks the candidate revision is ready once the operation is done.
	last := f.calls[len(f.calls)-1]
	if last.Method != http.MethodGet || last.Path != testServicePath+"/revisions/api-c1a2b3c4-0" {
		t.Fatalf("last call %s %s", last.Method, last.Path)
	}
}

func TestApplyCandidateRevisionRefusesTrafficToTheCandidate(t *testing.T) {
	s := (&fakeRun{t: t, service: servingService}).session(t)
	for name, traffic := range map[string][]RunTraffic{
		"latest":    {{Type: TrafficLatest, Percent: 100}},
		"candidate": {{Type: TrafficRevision, Revision: "api-00007-abc", Percent: 90}, {Type: TrafficRevision, Revision: "api-c1a2b3c4-0", Percent: 10}},
		"none":      nil,
	} {
		svc := RunService{Template: RunTemplate{Revision: "api-c1a2b3c4-0"}, Traffic: traffic}
		if _, err := s.ApplyCandidateRevision(t.Context(), "europe-west1", "api", svc); err == nil {
			t.Errorf("%s: a candidate that takes traffic, or pins nothing, was started", name)
		}
	}
	if _, err := s.ApplyCandidateRevision(t.Context(), "europe-west1", "api", RunService{Traffic: []RunTraffic{{Type: TrafficRevision, Revision: "a", Percent: 100}}}); err == nil {
		t.Error("a candidate without a revision name was started")
	}
}

func TestSetTrafficKeepsTheTemplate(t *testing.T) {
	f := &fakeRun{t: t, service: servingService}
	split := []RunTraffic{{Type: TrafficRevision, Revision: "api-00007-abc", Percent: 75}, {Type: TrafficRevision, Revision: "api-c1a2b3c4-0", Percent: 25}}
	if err := f.session(t).SetTraffic(t.Context(), "europe-west1", "api", split); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) < 2 || f.calls[0].Method != http.MethodGet || f.calls[1].Method != http.MethodPatch {
		t.Fatalf("calls %+v", f.calls)
	}
	var read map[string]json.RawMessage
	if err := json.Unmarshal([]byte(servingService), &read); err != nil {
		t.Fatal(err)
	}
	sent := f.patches()[0]
	if !reflect.DeepEqual(templateOf(t, sent), templateOf(t, read)) {
		t.Fatalf("template changed:\n%s\nwas\n%s", sent["template"], read["template"])
	}
	for _, field := range []string{"labels", "ingress", "etag"} {
		var got, was any
		if json.Unmarshal(sent[field], &got) != nil || json.Unmarshal(read[field], &was) != nil || !reflect.DeepEqual(got, was) {
			t.Errorf("%s changed: %s, was %s", field, sent[field], read[field])
		}
	}
	if got := trafficOf(t, sent); !reflect.DeepEqual(got, split) {
		t.Fatalf("traffic %+v", got)
	}
	if f.opReads != 1 {
		t.Fatalf("the operation was read %d times", f.opReads)
	}
}

func TestPromoteSendsEverythingToLatestWithoutANewRevision(t *testing.T) {
	f := &fakeRun{t: t, service: servingService}
	if err := f.session(t).SetTraffic(t.Context(), "europe-west1", "api", []RunTraffic{{Type: TrafficLatest, Percent: 100}}); err != nil {
		t.Fatal(err)
	}
	sent := f.patches()[0]
	if got := trafficOf(t, sent); !reflect.DeepEqual(got, []RunTraffic{{Type: TrafficLatest, Percent: 100}}) {
		t.Fatalf("traffic %+v", got)
	}
	// The template keeps the candidate's name, so Cloud Run makes no new
	// revision: the latest is the candidate.
	if got := templateOf(t, sent)["revision"]; got != "api-c1a2b3c4-0" {
		t.Fatalf("template.revision %v", got)
	}
}

func TestSetTrafficRefusesWhatIsntWholeTraffic(t *testing.T) {
	f := &fakeRun{t: t, service: servingService}
	s := f.session(t)
	for name, traffic := range map[string][]RunTraffic{
		"empty":     nil,
		"short":     {{Type: TrafficRevision, Revision: "a", Percent: 60}},
		"over":      {{Type: TrafficRevision, Revision: "a", Percent: 60}, {Type: TrafficRevision, Revision: "b", Percent: 60}},
		"unnamed":   {{Type: TrafficRevision, Percent: 100}},
		"twice":     {{Type: TrafficRevision, Revision: "a", Percent: 50}, {Type: TrafficRevision, Revision: "a", Percent: 50}},
		"negative":  {{Type: TrafficRevision, Revision: "a", Percent: 110}, {Type: TrafficRevision, Revision: "b", Percent: -10}},
		"wrongtype": {{Type: "SOMETHING", Revision: "a", Percent: 100}},
	} {
		if err := s.SetTraffic(t.Context(), "europe-west1", "api", traffic); err == nil {
			t.Errorf("%s: traffic %+v was set", name, traffic)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("refused traffic still called Cloud Run: %+v", f.calls)
	}
}

func TestDeleteRevisionIgnoresGone(t *testing.T) {
	f := &fakeRun{t: t, missing: map[string]bool{testServicePath + "/revisions/api-c1a2b3c4-0": true}}
	s := f.session(t)
	if err := s.DeleteRevision(t.Context(), "europe-west1", "api", "api-c1a2b3c4-0"); err != nil {
		t.Fatalf("a revision that's gone: %v", err)
	}
	if err := s.DeleteRevision(t.Context(), "europe-west1", "api", "api-c1a2b3c4-1"); err != nil {
		t.Fatal(err)
	}
	if f.opReads != 1 {
		t.Fatalf("the delete's operation was read %d times", f.opReads)
	}
	methods := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		methods = append(methods, c.Method+" "+c.Path)
	}
	if !slices.Contains(methods, http.MethodDelete+" "+testServicePath+"/revisions/api-c1a2b3c4-1") {
		t.Fatalf("calls %v", methods)
	}
}

func TestGetRevisionReadsWhetherItsUp(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		up   bool
	}{
		"ready":    {`{"conditions":[{"type":"Ready","state":"CONDITION_SUCCEEDED"}]}`, true},
		"failed":   {`{"conditions":[{"type":"Ready","state":"CONDITION_FAILED","message":"crashed"}]}`, false},
		"starting": {`{"reconciling":true,"conditions":[{"type":"Ready","state":"CONDITION_PENDING"}]}`, false},
		"deleting": {`{"deleteTime":"2026-10-26T10:00:00Z","conditions":[{"type":"Ready","state":"CONDITION_SUCCEEDED"}]}`, false},
		"no ready": {`{"conditions":[{"type":"ContainerHealthy","state":"CONDITION_SUCCEEDED"}]}`, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(tc.body)) }))
		rev, err := metricsSession(srv).GetRevision(t.Context(), "europe-west1", "api", "api-c1a2b3c4-0")
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if rev.Up() != tc.up {
			t.Errorf("%s: up %v", name, rev.Up())
		}
	}
	var gone *RunRevision
	if gone.Up() {
		t.Error("a missing revision is up")
	}
}

func TestShortRevisionNames(t *testing.T) {
	svc := RunService{
		LatestReadyRev:   "projects/p/locations/r/services/api/revisions/api-00007-abc",
		LatestCreatedRev: "api-00008-def",
	}
	if svc.LatestReady() != "api-00007-abc" || svc.LatestCreated() != "api-00008-def" {
		t.Fatalf("%q %q", svc.LatestReady(), svc.LatestCreated())
	}
}

func TestCanaryPermissionsAreAskedApart(t *testing.T) {
	var asked [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Permissions []string `json:"permissions"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		asked = append(asked, in.Permissions)
		w.Write([]byte(`{"permissions":["run.revisions.get"]}`))
	}))
	defer srv.Close()
	missing, err := metricsSession(srv).MissingOf(t.Context(), CanaryPermissions)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(missing, []string{"run.revisions.delete"}) || len(asked) != 1 || !slices.Equal(asked[0], CanaryPermissions) {
		t.Fatalf("missing %v, asked %v", missing, asked)
	}
	for _, p := range CanaryPermissions {
		if slices.Contains(Permissions, p) {
			t.Errorf("%s is required of every connection", p)
		}
	}
}

func TestSetTrafficLooksAtTheSplitWhenTheOperationFails(t *testing.T) {
	for name, tc := range map[string]struct {
		statuses string
		ok       bool
	}{
		"in place": {`[{"type":"TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION","revision":"api-00007-abc","percent":100}]`, true},
		"not":      {`[{"type":"TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION","revision":"api-c1a2b3c4-0","percent":100}]`, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				w.Write([]byte(`{"template":{"revision":"api-c1a2b3c4-0","containers":[{"image":"x"}]},"trafficStatuses":` + tc.statuses + `}`))
			case http.MethodPatch:
				w.Write([]byte(`{"name":"op","done":true,"error":{"message":"revision api-c1a2b3c4-0 isn't ready"}}`))
			}
		}))
		err := metricsSession(srv).SetTraffic(t.Context(), "europe-west1", "api", []RunTraffic{{Type: TrafficRevision, Revision: "api-00007-abc", Percent: 100}})
		srv.Close()
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRevisionRunsComparesTheWholeRelease(t *testing.T) {
	release := func() RunTemplate {
		ref := &RunValueSource{}
		ref.SecretKeyRef.Secret, ref.SecretKeyRef.Version = "lp-api-TOKEN", "2"
		sql := RunVolume{Name: "cloudsql", CloudSQLInstance: &struct {
			Instances []string `json:"instances"`
		}{Instances: []string{"shop:europe-west1:db"}}}
		return RunTemplate{
			Scaling: &RunScaling{Min: 1, Max: 3},
			Containers: []RunContainer{{
				Image: "img:abc", Env: []RunEnv{{Name: "MODE", Value: "fast"}, {Name: "TOKEN", ValueSource: ref}},
				Ports: []RunPort{{ContainerPort: 8080}}, Resources: &RunResources{Limits: map[string]string{"cpu": "1", "memory": "512Mi"}},
				Mounts: []RunVolumeMount{{Name: "cloudsql", MountPath: "/cloudsql"}},
			}},
			Volumes:   []RunVolume{sql},
			VPCAccess: &RunVPCAccess{NetworkInterfaces: []RunNetworkInterface{{Network: "default", Subnetwork: "default"}}, Egress: "PRIVATE_RANGES_ONLY"},
		}
	}
	revisionOf := func(t RunTemplate) *RunRevision {
		// Cloud Run names the secret by its full path and the account it
		// runs as.
		data, _ := json.Marshal(t)
		var copied RunTemplate
		json.Unmarshal(data, &copied)
		copied.Containers[0].Env[1].ValueSource.SecretKeyRef.Secret = "projects/123/secrets/lp-api-TOKEN"
		return &RunRevision{
			ServiceAccount: "123-compute@developer.gserviceaccount.com", Scaling: copied.Scaling,
			Containers: copied.Containers, Volumes: copied.Volumes, VPCAccess: copied.VPCAccess,
		}
	}
	if !revisionOf(release()).Runs(release()) {
		t.Fatal("the same release doesn't run")
	}
	for name, change := range map[string]func(*RunTemplate){
		"secret version": func(t *RunTemplate) { t.Containers[0].Env[1].ValueSource.SecretKeyRef.Version = "1" },
		"plain variable": func(t *RunTemplate) { t.Containers[0].Env[0].Value = "slow" },
		"image":          func(t *RunTemplate) { t.Containers[0].Image = "img:def" },
		"scale":          func(t *RunTemplate) { t.Scaling.Max = 5 },
		"size":           func(t *RunTemplate) { t.Containers[0].Resources.Limits["cpu"] = "2" },
		"port":           func(t *RunTemplate) { t.Containers[0].Ports[0].ContainerPort = 9090 },
		"cloud sql":      func(t *RunTemplate) { t.Volumes[0].CloudSQLInstance.Instances = []string{"shop:europe-west1:other"} },
		"vpc":            func(t *RunTemplate) { t.VPCAccess = nil },
	} {
		running := release()
		change(&running)
		if revisionOf(running).Runs(release()) {
			t.Errorf("%s: a revision that differs runs the release", name)
		}
	}
	other := revisionOf(release())
	other.ServiceAccount = "someone@shop.iam.gserviceaccount.com"
	if other.Runs(release()) {
		t.Error("a revision that runs as another account runs the release")
	}
	var gone *RunRevision
	if gone.Runs(release()) {
		t.Error("a missing revision runs the release")
	}
}
