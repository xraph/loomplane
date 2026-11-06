package deployments

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/render"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

// fakeRenderAPI is a stateful Render API that records what it was asked.
type fakeRenderAPI struct {
	mu        sync.Mutex
	calls     []string
	projects  map[string]string            // name -> id
	envs      map[string]*renderTestEnv    // id -> environment
	resources map[string]map[string]string // id -> {"name", "environmentId", "kind"}
	posts     map[string]map[string]any    // "POST path" -> last body
	moved     [][]string                   // resource ids per move into an environment
	// projectStatus, envStatus and envListStatus, when set, are how Render
	// answers a project POST, an environment POST and an environment GET.
	projectStatus, envStatus, envListStatus int
	// envDeleteFailures is how many environment DELETEs fail with a 502
	// before one succeeds.
	envDeleteFailures int
	created           int
}

type renderTestEnv struct{ id, name, project string }

func newFakeRenderAPI() *fakeRenderAPI {
	return &fakeRenderAPI{
		projects: map[string]string{}, envs: map[string]*renderTestEnv{},
		resources: map[string]map[string]string{}, posts: map[string]map[string]any{},
	}
}

func (f *fakeRenderAPI) nextID(prefix string) string {
	f.created++
	return fmt.Sprintf("%s_%d", prefix, f.created)
}

func (f *fakeRenderAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	reply := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(status int, msg string) { reply(status, map[string]string{"message": msg}) }
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if r.Method == http.MethodPost {
		f.posts["POST "+r.URL.Path] = body
	}
	q := r.URL.Query()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/projects":
		out := []any{}
		if id, ok := f.projects[q.Get("name")]; ok {
			out = append(out, map[string]any{"project": map[string]string{"id": id, "name": q.Get("name")}})
		}
		reply(http.StatusOK, out)
	case r.Method == http.MethodPost && r.URL.Path == "/projects":
		if f.projectStatus != 0 {
			fail(f.projectStatus, "projects need a paid workspace")
			return
		}
		name, _ := body["name"].(string)
		id := f.nextID("prj")
		f.projects[name] = id
		for _, e := range body["environments"].([]any) {
			envName := e.(map[string]any)["name"].(string)
			eid := f.nextID("evm")
			f.envs[eid] = &renderTestEnv{id: eid, name: envName, project: id}
		}
		reply(http.StatusCreated, map[string]string{"id": id, "name": name})
	case r.Method == http.MethodGet && r.URL.Path == "/environments":
		if f.envListStatus != 0 {
			fail(f.envListStatus, "forbidden")
			return
		}
		out := []any{}
		for _, e := range f.envs {
			if e.project == q.Get("projectId") && e.name == q.Get("name") {
				out = append(out, map[string]any{"environment": map[string]string{"id": e.id, "name": e.name, "projectId": e.project}})
			}
		}
		reply(http.StatusOK, out)
	case r.Method == http.MethodPost && r.URL.Path == "/environments":
		if f.envStatus != 0 {
			fail(f.envStatus, "environments need a paid workspace")
			return
		}
		eid := f.nextID("evm")
		e := &renderTestEnv{id: eid, name: body["name"].(string), project: body["projectId"].(string)}
		f.envs[eid] = e
		reply(http.StatusCreated, map[string]string{"id": e.id, "name": e.name, "projectId": e.project})
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "environments" && parts[2] == "resources":
		var ids []string
		for _, id := range body["resourceIds"].([]any) {
			ids = append(ids, id.(string))
			if res, ok := f.resources[id.(string)]; ok {
				res["environmentId"] = parts[1]
			}
		}
		f.moved = append(f.moved, ids)
		reply(http.StatusOK, map[string]any{})
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "environments":
		e, ok := f.envs[parts[1]]
		if !ok {
			fail(http.StatusNotFound, "not found")
			return
		}
		services, databases := []string{}, []string{}
		for id, res := range f.resources {
			if res["environmentId"] != e.id {
				continue
			}
			if res["kind"] == "service" {
				services = append(services, id)
			} else {
				databases = append(databases, id)
			}
		}
		reply(http.StatusOK, map[string]any{"id": e.id, "serviceIds": services, "databasesIds": databases})
	case r.Method == http.MethodDelete && len(parts) == 2 && parts[0] == "environments":
		if f.envDeleteFailures > 0 {
			f.envDeleteFailures--
			fail(http.StatusBadGateway, "bad gateway")
			return
		}
		delete(f.envs, parts[1])
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && len(parts) == 2 && parts[0] == "projects":
		for name, id := range f.projects {
			if id == parts[1] {
				delete(f.projects, name)
			}
		}
		for id, e := range f.envs {
			if e.project == parts[1] {
				delete(f.envs, id)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && (r.URL.Path == "/services" || r.URL.Path == "/postgres"):
		kind := strings.TrimPrefix(r.URL.Path, "/")
		out := []any{}
		for id, res := range f.resources {
			if res["name"] == q.Get("name") {
				wrap := "service"
				if kind == "postgres" {
					wrap = "postgres"
				}
				out = append(out, map[string]any{wrap: f.resource(id)})
			}
		}
		reply(http.StatusOK, out)
	case r.Method == http.MethodPost && r.URL.Path == "/services":
		id := f.nextID("srv")
		env, _ := body["environmentId"].(string)
		f.resources[id] = map[string]string{"name": body["name"].(string), "environmentId": env, "kind": "service"}
		reply(http.StatusCreated, map[string]any{"service": f.resource(id)})
	case r.Method == http.MethodPost && r.URL.Path == "/postgres":
		id := f.nextID("dpg")
		env, _ := body["environmentId"].(string)
		f.resources[id] = map[string]string{"name": body["name"].(string), "environmentId": env, "kind": "postgres"}
		reply(http.StatusCreated, f.resource(id))
	case len(parts) == 3 && parts[2] == "connection-info":
		reply(http.StatusOK, map[string]string{"internalConnectionString": "postgres://u:p@" + parts[1] + "/db"})
	case len(parts) == 3 && parts[2] == "deploys" && r.Method == http.MethodPost:
		reply(http.StatusCreated, map[string]string{"id": "dep_1"})
	case len(parts) == 4 && parts[2] == "deploys" && r.Method == http.MethodGet:
		reply(http.StatusOK, map[string]string{"id": "dep_1", "status": "live"})
	case len(parts) == 2 && (parts[0] == "services" || parts[0] == "postgres") && r.Method == http.MethodGet:
		if _, ok := f.resources[parts[1]]; !ok {
			fail(http.StatusNotFound, "not found")
			return
		}
		reply(http.StatusOK, f.resource(parts[1]))
	case len(parts) == 2 && (parts[0] == "services" || parts[0] == "postgres") && r.Method == http.MethodPatch:
		reply(http.StatusOK, f.resource(parts[1]))
	case len(parts) == 2 && (parts[0] == "services" || parts[0] == "postgres") && r.Method == http.MethodDelete:
		delete(f.resources, parts[1])
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 3 && parts[2] == "env-vars":
		reply(http.StatusOK, []any{})
	default:
		fail(http.StatusNotFound, "no route "+r.Method+" "+r.URL.Path)
	}
}

func (f *fakeRenderAPI) resource(id string) map[string]any {
	res := f.resources[id]
	return map[string]any{"id": id, "name": res["name"], "environmentId": res["environmentId"], "status": "available"}
}

func (f *fakeRenderAPI) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

func (f *fakeRenderAPI) indexOf(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.calls {
		if c == call {
			return i
		}
	}
	return -1
}

func (f *fakeRenderAPI) body(call string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts[call]
}

type renderHarness struct {
	api    *fakeRenderAPI
	store  *removeStore
	notify *countingNotifier
	logs   []string
	job    *Job
	exec   *Render
}

// newRenderHarness builds a deploy of service web in environment staging.
// native false means no Environments.
func newRenderHarness(t *testing.T, native bool) *renderHarness {
	t.Helper()
	h := &renderHarness{api: newFakeRenderAPI(), notify: &countingNotifier{}}
	server := httptest.NewServer(h.api)
	t.Cleanup(server.Close)
	h.job = &Job{
		Deployment: &Deployment{OrgID: "org", ProjectID: "proj_abc12345", EnvironmentID: "env_staging", Environment: "staging",
			Sources: []SourceArtifact{{Repository: "acme/web"}}},
		CommitSHA:  "fedcba9876543210fedcba9876543210fedcba98",
		Plan:       &planning.Plan{ID: "plan"},
		Action:     planning.Action{Name: "web", Service: &planning.ServiceConfig{Source: ".", Ecosystem: "node", Workload: provider.WebApp, Execution: provider.Container, Public: true}},
		Connection: &connections.Connection{ID: "conn", Details: map[string]string{"owner_id": "tea-1"}},
		Credential: []byte("key"),
		Progress:   func(_, m string) { h.logs = append(h.logs, m) },
		Checkpoint: func([]byte) error { return nil },
		Keep:       func(context.Context, string, []byte) error { return nil },
		Forget:     func(context.Context) error { return nil },
	}
	h.exec = &Render{Client: render.New(server.URL)}
	if native {
		h.store = &removeStore{roleStore: &roleStore{fakePEStore: newFakePEStore(), kind: "staging", name: "staging", first: true}}
		h.exec.Environments = &NativeEnvironments{Store: h.store, Notify: h.notify}
	}
	return h
}

func (h *renderHarness) datastore() {
	h.job.Action = planning.Action{Name: "db", Subject: planning.ResourceSubject, Resource: &planning.ResourceConfig{Kind: "postgres", Version: "16"}}
}

func (h *renderHarness) row() *ProviderEnvironment {
	return h.store.rows["org|env_staging|render|conn|"]
}

func (h *renderHarness) apply(t *testing.T) *Outcome {
	t.Helper()
	out, err := h.exec.Apply(t.Context(), h.job)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return out
}

func TestRenderProjectName(t *testing.T) {
	h := newRenderHarness(t, false)
	if got, want := RenderProjectName(h.job), "loomplane-"+idSuffix("proj_abc12345"); got != want {
		t.Fatalf("RenderProjectName = %q, want %q", got, want)
	}
}

func TestRenderNativeServiceJoinsEnvironment(t *testing.T) {
	h := newRenderHarness(t, true)
	out := h.apply(t)

	project := h.api.body("POST /projects")
	if project["name"] != RenderProjectName(h.job) {
		t.Fatalf("project POST = %v, want name %s", project, RenderProjectName(h.job))
	}
	envs, _ := project["environments"].([]any)
	if len(envs) != 1 || envs[0].(map[string]any)["name"] != "staging" {
		t.Fatalf("project POST environments = %v, want staging", project["environments"])
	}
	svc := h.api.body("POST /services")
	if svc["environmentId"] != "evm_2" {
		t.Fatalf("service POST environmentId = %v, want evm_2", svc["environmentId"])
	}
	if out.ExternalID != "srv_3" {
		t.Fatalf("ExternalID = %q, want the service id", out.ExternalID)
	}
	row := h.row()
	if row == nil || row.Mode != ModeNative || row.ProjectRef != "prj_1" || row.EnvironmentRef != "evm_2" {
		t.Fatalf("row = %+v, want native prj_1/evm_2", row)
	}
	if h.notify.calls != 0 {
		t.Fatalf("notify calls = %d, want 0", h.notify.calls)
	}
}

func TestRenderNativeCreatesMissingEnvironment(t *testing.T) {
	h := newRenderHarness(t, true)
	h.api.projects[RenderProjectName(h.job)] = "prj_9"
	h.apply(t)
	if h.api.called("POST /projects") {
		t.Fatal("an existing project was made again")
	}
	env := h.api.body("POST /environments")
	if env["name"] != "staging" || env["projectId"] != "prj_9" {
		t.Fatalf("environment POST = %v, want staging in prj_9", env)
	}
	if row := h.row(); row == nil || row.Mode != ModeNative || row.ProjectRef != "prj_9" || row.EnvironmentRef != "evm_1" {
		t.Fatalf("row = %+v, want native prj_9/evm_1", row)
	}
}

func TestRenderNativeDatastoreJoinsEnvironment(t *testing.T) {
	h := newRenderHarness(t, true)
	h.datastore()
	out := h.apply(t)

	if h.api.body("POST /projects")["name"] != RenderProjectName(h.job) {
		t.Fatal("no project was made")
	}
	if got := h.api.body("POST /postgres")["environmentId"]; got != "evm_2" {
		t.Fatalf("postgres POST environmentId = %v, want evm_2", got)
	}
	if out.ExternalID != "postgres/dpg_3" {
		t.Fatalf("ExternalID = %q, want postgres/dpg_3", out.ExternalID)
	}
	if row := h.row(); row == nil || row.Mode != ModeNative || row.EnvironmentRef != "evm_2" {
		t.Fatalf("row = %+v, want native evm_2", row)
	}
}

func TestRenderNativeMovesExistingDatastore(t *testing.T) {
	h := newRenderHarness(t, true)
	h.datastore()
	h.api.resources["dpg_old"] = map[string]string{"name": RenderName(h.job), "kind": "postgres"}
	out := h.apply(t)
	if h.api.called("POST /postgres") {
		t.Fatal("an existing datastore was made again")
	}
	if len(h.api.moved) != 1 || h.api.moved[0][0] != "dpg_old" {
		t.Fatalf("moved = %v, want dpg_old moved", h.api.moved)
	}
	if out.ExternalID != "postgres/dpg_old" {
		t.Fatalf("ExternalID = %q", out.ExternalID)
	}
}

func TestRenderNativeClaimsProjectBeforeEnvironment(t *testing.T) {
	h := newRenderHarness(t, true)
	h.api.projects[RenderProjectName(h.job)] = "prj_9"
	h.api.envStatus = http.StatusInternalServerError
	if _, err := h.exec.Apply(t.Context(), h.job); err == nil {
		t.Fatal("a transient environment failure deployed")
	}
	row := h.row()
	if row == nil || row.Mode != ModeNative || row.ProjectRef != "prj_9" || row.EnvironmentRef != "" {
		t.Fatalf("row = %+v, want a native claim on prj_9", row)
	}
}

func TestRenderFallsBackWhenProjectRefused(t *testing.T) {
	h := newRenderHarness(t, true)
	h.api.projectStatus = http.StatusPaymentRequired
	h.apply(t)

	svc := h.api.body("POST /services")
	if _, ok := svc["environmentId"]; ok {
		t.Fatalf("service POST = %v, want no environmentId", svc)
	}
	row := h.row()
	if row == nil || row.Mode != ModeFallback || row.Permanent || row.Reason == "" {
		t.Fatalf("row = %+v, want a retryable fallback", row)
	}
	if h.notify.calls != 1 {
		t.Fatalf("notify calls = %d, want 1", h.notify.calls)
	}
	if !strings.Contains(strings.Join(h.logs, "\n"), "separate project") {
		t.Fatalf("logs = %v, want the fallback line", h.logs)
	}
}

func TestRenderFallbackRemovesWhatItMade(t *testing.T) {
	h := newRenderHarness(t, true)
	h.api.envListStatus = http.StatusForbidden
	h.apply(t)
	if !h.api.called("DELETE /projects/prj_1") {
		t.Fatal("the project this job made was left behind")
	}
	if row := h.row(); row == nil || row.Mode != ModeFallback {
		t.Fatalf("row = %+v, want fallback", row)
	}
	if svc := h.api.body("POST /services"); svc["environmentId"] != nil {
		t.Fatalf("service POST = %v, want no environmentId", svc)
	}
}

func TestRenderFallbackKeepsProjectAnotherEnvironmentUses(t *testing.T) {
	h := newRenderHarness(t, true)
	h.api.envListStatus = http.StatusForbidden
	_ = h.store.SaveProviderEnvironment(t.Context(), &ProviderEnvironment{OrgID: "org", EnvironmentID: "env_prod", Provider: "render", ConnectionID: "conn", Mode: ModeNative, ProjectRef: "prj_1"})
	h.apply(t)
	if h.api.called("DELETE /projects/prj_1") {
		t.Fatal("a project another environment uses was deleted")
	}
	if row := h.row(); row == nil || row.Mode != ModeFallback {
		t.Fatalf("row = %+v, want fallback", row)
	}
}

func TestRenderFallbackKeepsWhatItDidNotMake(t *testing.T) {
	h := newRenderHarness(t, true)
	h.api.projects[RenderProjectName(h.job)] = "prj_9"
	h.api.envStatus = http.StatusPaymentRequired
	h.apply(t)
	if h.api.called("DELETE /projects/prj_9") {
		t.Fatal("a project this job didn't make was deleted")
	}
	if row := h.row(); row == nil || row.Mode != ModeFallback {
		t.Fatalf("row = %+v, want fallback", row)
	}
}

func TestRenderStickyFallback(t *testing.T) {
	h := newRenderHarness(t, true)
	_ = h.store.SaveProviderEnvironment(t.Context(), &ProviderEnvironment{OrgID: "org", EnvironmentID: "env_staging", Provider: "render", ConnectionID: "conn", Mode: ModeFallback, Reason: "plan"})
	h.apply(t)
	if h.api.called("GET /projects") {
		t.Fatal("a fallen-back environment asked for a project")
	}
	if svc := h.api.body("POST /services"); svc["environmentId"] != nil {
		t.Fatalf("service POST = %v, want no environmentId", svc)
	}
}

func TestRenderTransientErrorDoesNotFallBack(t *testing.T) {
	h := newRenderHarness(t, true)
	h.api.projectStatus = http.StatusBadGateway
	if _, err := h.exec.Apply(t.Context(), h.job); err == nil {
		t.Fatal("a transient project failure deployed")
	}
	if row := h.row(); row != nil && row.Mode == ModeFallback {
		t.Fatalf("row = %+v, want no fallback", row)
	}
	if h.api.called("POST /services") {
		t.Fatal("the service was made after a transient failure")
	}
}

func TestRenderNilEnvironmentsIsLegacy(t *testing.T) {
	h := newRenderHarness(t, false)
	h.apply(t)
	if h.api.called("GET /projects") || h.api.called("GET /environments") {
		t.Fatal("legacy Render asked for projects or environments")
	}
	if svc := h.api.body("POST /services"); svc["environmentId"] != nil {
		t.Fatalf("service POST = %v, want no environmentId", svc)
	}
}

func TestRenderRefusalAfterDeployDoesNotFallBack(t *testing.T) {
	h := newRenderHarness(t, true)
	h.api.projectStatus = http.StatusPaymentRequired
	h.job.Resume = []byte(`{"service_id":"srv_1","deploy_id":"dep_1"}`)
	if _, err := h.exec.Apply(t.Context(), h.job); err == nil {
		t.Fatal("a refusal after the deploy started fell back")
	}
	if row := h.row(); row != nil && row.Mode == ModeFallback {
		t.Fatalf("row = %+v, want no fallback", row)
	}
}

// nativeRenderBinding deploys web natively and returns its binding.
func nativeRenderBinding(t *testing.T, h *renderHarness, external string) *planning.Binding {
	t.Helper()
	return &planning.Binding{OrgID: "org", EnvironmentID: "env_staging", ConnectionID: "conn", Name: h.job.Action.Name, Provider: "render", ExternalID: external}
}

func TestRenderRemoveDeletesEmptyEnvironment(t *testing.T) {
	h := newRenderHarness(t, true)
	h.api.envs["evm_1"] = &renderTestEnv{id: "evm_1", name: "staging", project: "prj_1"}
	h.api.resources["srv_1"] = map[string]string{"name": "web", "environmentId": "evm_1", "kind": "service"}
	_ = h.store.SaveProviderEnvironment(t.Context(), &ProviderEnvironment{OrgID: "org", EnvironmentID: "env_staging", Provider: "render", ConnectionID: "conn", Mode: ModeNative, ProjectRef: "prj_1", EnvironmentRef: "evm_1"})
	_ = h.store.SaveProviderEnvironment(t.Context(), &ProviderEnvironment{OrgID: "org", EnvironmentID: "env_prod", Provider: "render", ConnectionID: "conn", Mode: ModeNative, ProjectRef: "prj_1", EnvironmentRef: "evm_2"})

	if err := h.exec.Remove(t.Context(), h.job, nativeRenderBinding(t, h, "srv_1")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if h.api.indexOf("DELETE /services/srv_1") < 0 || h.api.indexOf("DELETE /environments/evm_1") < h.api.indexOf("DELETE /services/srv_1") {
		t.Fatal("the empty environment wasn't deleted after the service")
	}
	if h.api.called("DELETE /projects/prj_1") {
		t.Fatal("a project another environment uses was deleted")
	}
	if h.row() != nil {
		t.Fatal("the deleted environment's record was kept")
	}
}

func TestRenderRemoveRetryFinishesCleanup(t *testing.T) {
	h := newRenderHarness(t, true)
	h.api.envDeleteFailures = 1
	h.api.projects["loomplane-x"] = "prj_1"
	h.api.envs["evm_1"] = &renderTestEnv{id: "evm_1", name: "staging", project: "prj_1"}
	h.api.resources["srv_1"] = map[string]string{"name": "web", "environmentId": "evm_1", "kind": "service"}
	_ = h.store.SaveProviderEnvironment(t.Context(), &ProviderEnvironment{OrgID: "org", EnvironmentID: "env_staging", Provider: "render", ConnectionID: "conn", Mode: ModeNative, ProjectRef: "prj_1", EnvironmentRef: "evm_1"})
	b := nativeRenderBinding(t, h, "srv_1")

	if err := h.exec.Remove(t.Context(), h.job, b); err == nil {
		t.Fatal("Remove succeeded although the environment delete failed")
	}
	if _, ok := h.api.resources["srv_1"]; ok {
		t.Fatal("the service wasn't deleted on the first try")
	}
	if h.row() == nil {
		t.Fatal("the record went before the cleanup finished")
	}
	if err := h.exec.Remove(t.Context(), h.job, b); err != nil {
		t.Fatalf("retried Remove: %v", err)
	}
	if _, ok := h.api.envs["evm_1"]; ok {
		t.Fatal("the retry left the empty environment behind")
	}
	if !h.api.called("DELETE /projects/prj_1") {
		t.Fatal("the retry left the unused project behind")
	}
	if h.row() != nil {
		t.Fatal("the retry kept the record")
	}
}

func TestRenderRemoveKeepsEnvironmentWithOtherResources(t *testing.T) {
	h := newRenderHarness(t, true)
	h.api.envs["evm_1"] = &renderTestEnv{id: "evm_1", name: "staging", project: "prj_1"}
	h.api.resources["srv_1"] = map[string]string{"name": "web", "environmentId": "evm_1", "kind": "service"}
	h.api.resources["srv_2"] = map[string]string{"name": "api", "environmentId": "evm_1", "kind": "service"}
	_ = h.store.SaveProviderEnvironment(t.Context(), &ProviderEnvironment{OrgID: "org", EnvironmentID: "env_staging", Provider: "render", ConnectionID: "conn", Mode: ModeNative, ProjectRef: "prj_1", EnvironmentRef: "evm_1"})

	if err := h.exec.Remove(t.Context(), h.job, nativeRenderBinding(t, h, "srv_1")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if h.api.called("DELETE /environments/evm_1") || h.api.called("DELETE /projects/prj_1") {
		t.Fatal("an environment still holding a service was deleted")
	}
	if h.row() == nil {
		t.Fatal("the record of an environment still in use was deleted")
	}
}

func TestRenderRemoveLastDatastoreDeletesProject(t *testing.T) {
	h := newRenderHarness(t, true)
	h.datastore()
	h.api.envs["evm_1"] = &renderTestEnv{id: "evm_1", name: "staging", project: "prj_1"}
	h.api.resources["dpg_1"] = map[string]string{"name": "db", "environmentId": "evm_1", "kind": "postgres"}
	_ = h.store.SaveProviderEnvironment(t.Context(), &ProviderEnvironment{OrgID: "org", EnvironmentID: "env_staging", Provider: "render", ConnectionID: "conn", Mode: ModeNative, ProjectRef: "prj_1", EnvironmentRef: "evm_1"})

	if err := h.exec.Remove(t.Context(), h.job, nativeRenderBinding(t, h, "postgres/dpg_1")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !h.api.called("DELETE /postgres/dpg_1") || !h.api.called("DELETE /environments/evm_1") || !h.api.called("DELETE /projects/prj_1") {
		t.Fatalf("calls = %v, want the datastore, environment and project deleted", h.api.calls)
	}
}

func TestRenderRemoveLeavesEnvironmentLoomplaneDidNotRecord(t *testing.T) {
	h := newRenderHarness(t, true)
	h.api.envs["evm_user"] = &renderTestEnv{id: "evm_user", name: "staging", project: "prj_user"}
	h.api.resources["srv_1"] = map[string]string{"name": "web", "environmentId": "evm_user", "kind": "service"}

	if err := h.exec.Remove(t.Context(), h.job, nativeRenderBinding(t, h, "srv_1")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !h.api.called("DELETE /services/srv_1") {
		t.Fatal("the service wasn't deleted")
	}
	if h.api.called("DELETE /environments/evm_user") || h.api.called("DELETE /projects/prj_user") {
		t.Fatal("an environment Loomplane never recorded was deleted")
	}
}

func TestRenderRemoveWithoutEnvironmentsIsLegacy(t *testing.T) {
	h := newRenderHarness(t, false)
	h.api.envs["evm_1"] = &renderTestEnv{id: "evm_1", name: "staging", project: "prj_1"}
	h.api.resources["srv_1"] = map[string]string{"name": "web", "environmentId": "evm_1", "kind": "service"}
	if err := h.exec.Remove(t.Context(), h.job, nativeRenderBinding(t, h, "srv_1")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if h.api.called("GET /services/srv_1") || h.api.called("DELETE /environments/evm_1") {
		t.Fatal("legacy Remove touched environments")
	}
}

// TestRenderRefusalOnNativeEnvironmentFailsInsteadOfFallingBack: staging's
// resources already sit in its Render environment, so a refusal now fails
// the deploy rather than making web a standalone service and leaving the
// native one running untracked.
func TestRenderRefusalOnNativeEnvironmentFailsInsteadOfFallingBack(t *testing.T) {
	h := newRenderHarness(t, true)
	h.api.projects[RenderProjectName(h.job)] = "prj_1"
	h.api.envs["evm_1"] = &renderTestEnv{id: "evm_1", name: "staging", project: "prj_1"}
	h.api.envListStatus = http.StatusForbidden
	_ = h.store.SaveProviderEnvironment(t.Context(), &ProviderEnvironment{OrgID: "org", EnvironmentID: "env_staging", Provider: "render", ConnectionID: "conn", Mode: ModeNative, ProjectRef: "prj_1", EnvironmentRef: "evm_1"})

	if _, err := h.exec.Apply(t.Context(), h.job); err == nil {
		t.Fatal("a refusal on a native environment fell back")
	}
	if row := h.row(); row == nil || row.Mode != ModeNative || row.EnvironmentRef != "evm_1" {
		t.Fatalf("row = %+v, want staging's native record kept", row)
	}
	if h.api.called("POST /services") || h.api.called("DELETE /projects/prj_1") || h.notify.calls != 0 {
		t.Fatalf("calls = %v, want no standalone service and nothing deleted", h.api.calls)
	}
}
