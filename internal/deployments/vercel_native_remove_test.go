package deployments

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// removeStore adds record deletion to the in-memory store.
type removeStore struct{ *roleStore }

func (s *removeStore) DeleteProviderEnvironment(_ context.Context, pe *ProviderEnvironment) error {
	delete(s.rows, peKey(pe))
	return nil
}

// removeAPI answers the Vercel routes Remove, Observe and FrontendAbsent use.
type removeAPI struct {
	mu          sync.Mutex
	calls       []string
	listings    [][]string // deployment ids per list call; the last repeats
	listed      int
	// states are deployments' readyState by id; READY when missing.
	states      map[string]string
	noProject   bool
	noCustomEnv bool
}

func (a *removeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	missing := func() {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":{"code":"not_found","message":"missing"}}`)
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v7/deployments":
		ids := []string{}
		if len(a.listings) > 0 {
			ids = a.listings[min(a.listed, len(a.listings)-1)]
		}
		a.listed++
		var items []string
		for _, id := range ids {
			state := "READY"
			if s, ok := a.states[id]; ok {
				state = s
			}
			items = append(items, fmt.Sprintf(`{"uid":%q,"readyState":%q,"customEnvironment":{"id":"env_9"}}`, id, state))
		}
		_, _ = fmt.Fprintf(w, `{"deployments":[%s]}`, strings.Join(items, ","))
	case r.Method == http.MethodGet && r.URL.Path == "/v9/projects/prj_1":
		if a.noProject {
			missing()
			return
		}
		_, _ = fmt.Fprint(w, `{"id":"prj_1","name":"web"}`)
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/custom-environments/"):
		if a.noCustomEnv {
			missing()
			return
		}
		_, _ = fmt.Fprint(w, `{"id":"env_9","slug":"staging"}`)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/env"):
		_, _ = fmt.Fprint(w, `{"envs":[{"id":"var_staging","customEnvironmentIds":["env_9"]},{"id":"var_prod","target":["production"]}]}`)
	default:
		_, _ = fmt.Fprint(w, `{}`)
	}
}

func (a *removeAPI) called(call string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.calls {
		if c == call {
			return true
		}
	}
	return false
}

func (a *removeAPI) count(prefix string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, c := range a.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

type removeHarness struct {
	api   *removeAPI
	store *removeStore
	exec  *Vercel
	job   *Job
	b     *planning.Binding
}

func newRemoveHarness(t *testing.T, ref string, otherRows ...*ProviderEnvironment) *removeHarness {
	t.Helper()
	h := &removeHarness{api: &removeAPI{}}
	server := httptest.NewServer(h.api)
	t.Cleanup(server.Close)
	h.store = &removeStore{&roleStore{fakePEStore: newFakePEStore()}}
	for _, pe := range append(otherRows, &ProviderEnvironment{
		OrgID: "org", EnvironmentID: "env_staging", Provider: "vercel", ConnectionID: "conn", Scope: "web",
		Mode: ModeNative, ProjectRef: "prj_1", EnvironmentRef: "env_9",
	}) {
		h.store.rows[peKey(pe)] = pe
	}
	h.exec = &Vercel{Client: vercel.New(server.URL), Environments: &NativeEnvironments{Store: h.store}}
	h.job = &Job{
		Deployment: &Deployment{OrgID: "org", ProjectID: "proj", EnvironmentID: "env_staging"},
		Action:     planning.Action{Name: "web"},
		Connection: &connections.Connection{ID: "conn"},
		Credential: []byte("token"),
	}
	h.b = &planning.Binding{OrgID: "org", EnvironmentID: "env_staging", Name: "web", Provider: "vercel", ConnectionID: "conn", ExternalID: ref}
	return h
}

func productionRow() *ProviderEnvironment {
	return &ProviderEnvironment{OrgID: "org", EnvironmentID: "env_prod", Provider: "vercel", ConnectionID: "conn", Scope: "web", Mode: ModeNative, ProjectRef: "prj_1", EnvironmentRef: "production"}
}

func TestVercelNativeRemoveKeepsSharedProject(t *testing.T) {
	h := newRemoveHarness(t, "prj_1#env_9", productionRow())
	h.api.listings = [][]string{{"dpl_a", "dpl_b"}, {}}
	if err := h.exec.Remove(t.Context(), h.job, h.b); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, want := range []string{"DELETE /v13/deployments/dpl_a", "DELETE /v13/deployments/dpl_b", "DELETE /v9/projects/prj_1/custom-environments/env_9"} {
		if !h.api.called(want) {
			t.Errorf("missing %s: %v", want, h.api.calls)
		}
	}
	if h.api.called("DELETE /v9/projects/prj_1") {
		t.Fatal("deleted a project another environment uses")
	}
	if !h.api.called("DELETE /v9/projects/prj_1/env/var_staging") {
		t.Errorf("staging's variable wasn't deleted: %v", h.api.calls)
	}
	if h.api.called("DELETE /v9/projects/prj_1/env/var_prod") {
		t.Error("deleted production's variable")
	}
	if len(h.store.rows) != 1 || h.store.rows[peKey(productionRow())] == nil {
		t.Fatalf("rows = %v, want only production's", h.store.rows)
	}
}

func TestVercelNativeRemoveLastDeletesProject(t *testing.T) {
	h := newRemoveHarness(t, "prj_1#env_9")
	if err := h.exec.Remove(t.Context(), h.job, h.b); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !h.api.called("DELETE /v9/projects/prj_1") {
		t.Fatalf("project not deleted: %v", h.api.calls)
	}
	if len(h.store.rows) != 0 {
		t.Fatalf("rows = %v, want none", h.store.rows)
	}
}

func TestVercelNativeRemoveWithoutEnvironmentsKeepsProject(t *testing.T) {
	h := newRemoveHarness(t, "prj_1#env_9")
	h.exec.Environments = nil
	if err := h.exec.Remove(t.Context(), h.job, h.b); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if h.api.called("DELETE /v9/projects/prj_1") {
		t.Fatal("deleted a project that may be shared")
	}
}

func TestVercelNativeRemoveFailsWhileDeploymentsRemain(t *testing.T) {
	h := newRemoveHarness(t, "prj_1#env_9")
	h.api.listings = [][]string{{"dpl_a"}}
	if err := h.exec.Remove(t.Context(), h.job, h.b); err == nil {
		t.Fatal("Remove reported success with deployments left")
	}
	if h.api.called("DELETE /v9/projects/prj_1") || len(h.store.rows) != 1 {
		t.Fatal("removed the project or record despite remaining deployments")
	}
	if got := h.api.count("DELETE /v13/deployments/"); got != removePasses {
		t.Fatalf("delete passes = %d, want %d", got, removePasses)
	}
}

func TestVercelNativeRemoveBuiltinSkipsCustomEnvironment(t *testing.T) {
	h := newRemoveHarness(t, "prj_1#production", productionRow())
	h.store.rows[peKey(productionRow())] = productionRow()
	h.b.EnvironmentID, h.job.Deployment.EnvironmentID = "env_prod", "env_prod"
	if err := h.exec.Remove(t.Context(), h.job, h.b); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if h.api.count("DELETE /v9/projects/prj_1/custom-environments") != 0 {
		t.Fatal("deleted a custom environment for a built-in target")
	}
}

func TestVercelNativeObserveReadsTarget(t *testing.T) {
	h := newRemoveHarness(t, "prj_1#env_9")
	h.api.listings = [][]string{{"dpl_new", "dpl_old"}}
	obs, err := h.exec.Observe(t.Context(), h.job.Connection, h.job.Credential, h.b)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.Exists || obs.Revision != "dpl_new" {
		t.Fatalf("observation = %+v, want dpl_new", obs)
	}
}

func TestVercelNativeObserveMissing(t *testing.T) {
	for name, set := range map[string]func(*removeAPI){
		"project":            func(a *removeAPI) { a.noProject = true },
		"custom environment": func(a *removeAPI) { a.noCustomEnv = true },
	} {
		t.Run(name, func(t *testing.T) {
			h := newRemoveHarness(t, "prj_1#env_9")
			set(h.api)
			obs, err := h.exec.Observe(t.Context(), h.job.Connection, h.job.Credential, h.b)
			if err != nil || obs.Exists {
				t.Fatalf("observation = %+v, %v, want empty", obs, err)
			}
		})
	}
}

func TestFrontendAbsentNative(t *testing.T) {
	tests := []struct {
		name string
		ref  string
		set  func(*removeAPI)
		want bool
	}{
		{"project gone", "prj_1#env_9", func(a *removeAPI) { a.noProject = true }, true},
		{"custom environment gone", "prj_1#env_9", func(a *removeAPI) { a.noCustomEnv = true }, true},
		{"custom environment present", "prj_1#env_9", func(a *removeAPI) {}, false},
		{"production without deployments", "prj_1#production", func(a *removeAPI) {}, true},
		{"production with a deployment", "prj_1#production", func(a *removeAPI) { a.listings = [][]string{{"dpl_1"}} }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newRemoveHarness(t, tt.ref)
			tt.set(h.api)
			got, err := h.exec.FrontendAbsent(t.Context(), h.job, h.b)
			if err != nil || got != tt.want {
				t.Fatalf("FrontendAbsent = %v, %v, want %v", got, err, tt.want)
			}
		})
	}
}

// TestVercelNativeRemoveKeepsProjectSharedThroughAnotherConnection:
// production deployed web through a second connection to the same Vercel
// team, so the project is the same one, and removing staging leaves it.
func TestVercelNativeRemoveKeepsProjectSharedThroughAnotherConnection(t *testing.T) {
	prod := productionRow()
	prod.ConnectionID = "conn_b"
	h := newRemoveHarness(t, "prj_1#env_9", prod)
	if err := h.exec.Remove(t.Context(), h.job, h.b); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if h.api.called("DELETE /v9/projects/prj_1") {
		t.Fatal("deleted a project production uses through another connection")
	}
	if len(h.store.rows) != 1 || h.store.rows[peKey(prod)] == nil {
		t.Fatalf("rows = %v, want only production's", h.store.rows)
	}
}

// TestVercelNativeRefRejectsEmptyTarget: "prj_1#" names the project but no
// environment of it. Listing its target would list every deployment of the
// project, so Remove, Observe and FrontendAbsent refuse it untouched.
func TestVercelNativeRefRejectsEmptyTarget(t *testing.T) {
	h := newRemoveHarness(t, "prj_1#")
	h.api.listings = [][]string{{"dpl_prod"}}
	if err := h.exec.Remove(t.Context(), h.job, h.b); err == nil {
		t.Error("Remove accepted a native ref with no target")
	}
	if _, err := h.exec.Observe(t.Context(), h.job.Connection, h.job.Credential, h.b); err == nil {
		t.Error("Observe accepted a native ref with no target")
	}
	if _, err := h.exec.FrontendAbsent(t.Context(), h.job, h.b); err == nil {
		t.Error("FrontendAbsent accepted a native ref with no target")
	}
	if len(h.api.calls) != 0 {
		t.Fatalf("called Vercel: %v", h.api.calls)
	}
	if len(h.store.rows) != 1 {
		t.Fatalf("rows = %v, want staging's kept", h.store.rows)
	}
}

// TestVercelNativeObserveReadsNewestReady: the newest deployment of the
// target is still building, and an older one failed, so what serves is
// the newest READY one.
func TestVercelNativeObserveReadsNewestReady(t *testing.T) {
	h := newRemoveHarness(t, "prj_1#env_9")
	h.api.listings = [][]string{{"dpl_building", "dpl_failed", "dpl_live", "dpl_old"}}
	h.api.states = map[string]string{"dpl_building": "BUILDING", "dpl_failed": "ERROR"}
	obs, err := h.exec.Observe(t.Context(), h.job.Connection, h.job.Credential, h.b)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.Exists || obs.Revision != "dpl_live" {
		t.Fatalf("observation = %+v, want dpl_live", obs)
	}
}

// TestVercelLocateNamesTheEnvironment: what a deleted environment leaves
// on a shared project is its environment there, not the project, which
// other environments still deploy to.
func TestVercelLocateNamesTheEnvironment(t *testing.T) {
	v := &Vercel{}
	for ref, want := range map[string]string{
		"prj_1#env_9":      "the custom environment env_9 of the Vercel project prj_1 (other environments share the project, so delete only that environment, not the project)",
		"prj_1#production": "the production environment of the Vercel project prj_1 (other environments share the project, so delete only its production deployments, not the project)",
		"prj_1#preview":    "the preview environment of the Vercel project prj_1 (other environments share the project, so delete only its preview deployments, not the project)",
		"prj_old":          "the Vercel project prj_old",
		"":                 "",
	} {
		b := &planning.Binding{Subject: planning.ServiceSubject, Name: "web", Provider: "vercel", ExternalID: ref}
		if got := v.Locate(b); got != want {
			t.Errorf("Locate(%q) = %q, want %q", ref, got, want)
		}
	}
}

// TestVercelLegacyRemoveForgetsTheFallback: removing web from the project
// of its own it fell back to deletes that project and staging's fallback
// record for web, and nothing else. A native record at the same key, as
// after a move into the shared project, stays.
func TestVercelLegacyRemoveForgetsTheFallback(t *testing.T) {
	fallback := &ProviderEnvironment{OrgID: "org", EnvironmentID: "env_staging", Provider: "vercel", ConnectionID: "conn", Scope: "web", Mode: ModeFallback, Reason: "plan"}
	api := &ProviderEnvironment{OrgID: "org", EnvironmentID: "env_staging", Provider: "vercel", ConnectionID: "conn", Scope: "api", Mode: ModeFallback, Reason: "plan"}

	t.Run("fallback", func(t *testing.T) {
		h := newRemoveHarness(t, "prj_old", api)
		h.store.rows[peKey(fallback)] = fallback
		if err := h.exec.Remove(t.Context(), h.job, h.b); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if !h.api.called("DELETE /v9/projects/prj_old") {
			t.Fatalf("calls = %v, want the separate project deleted", h.api.calls)
		}
		if len(h.store.rows) != 1 || h.store.rows[peKey(api)] == nil {
			t.Fatalf("rows = %v, want only api's", h.store.rows)
		}
	})

	t.Run("native at the same key", func(t *testing.T) {
		h := newRemoveHarness(t, "prj_old")
		if err := h.exec.Remove(t.Context(), h.job, h.b); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if row := h.store.rows["org|env_staging|vercel|conn|web"]; row == nil || row.Mode != ModeNative {
			t.Fatalf("rows = %v, want staging's native record kept", h.store.rows)
		}
	})
}
