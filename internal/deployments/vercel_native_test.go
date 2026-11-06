package deployments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// roleStore is the in-memory record store with an environment's role.
type roleStore struct {
	*fakePEStore
	kind, name string
	first      bool
}

func (s *roleStore) EnvironmentRole(context.Context, string, string, string) (string, string, bool, error) {
	return s.kind, s.name, s.first, nil
}

func (s *roleStore) SharingProject(_ context.Context, org, provider, conn, projectRef string) ([]*ProviderEnvironment, error) {
	var out []*ProviderEnvironment
	for _, pe := range s.rows {
		if pe.OrgID == org && pe.Provider == provider && pe.ConnectionID == conn && pe.ProjectRef == projectRef && pe.Mode == ModeNative {
			out = append(out, pe)
		}
	}
	return out, nil
}

// fakeVercelAPI is a stateful Vercel API that records what it was asked.
type fakeVercelAPI struct {
	mu       sync.Mutex
	projects map[string]string // name -> id
	envs     map[string]string // project id + "/" + slug -> custom environment id
	calls    []string          // "METHOD path", and "checkpoint <json>" markers from the test
	envPosts [][]map[string]any
	deploys  []map[string]any
	// customStatus, when set, is how Vercel answers a custom environment POST.
	customStatus int
	// customVarStatus, when set, is how Vercel answers a variable POST for
	// a custom environment.
	customVarStatus int
	created         int
	// onCustomEnvironment runs when a custom environment is asked for.
	onCustomEnvironment func()
}

func newFakeVercelAPI() *fakeVercelAPI {
	return &fakeVercelAPI{projects: map[string]string{}, envs: map[string]string{}}
}

func (f *fakeVercelAPI) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeVercelAPI) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeVercelAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.record(r.Method + " " + r.URL.Path)
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, msg string) {
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"error":{"code":"x","message":%q}}`, msg)
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "v9" && parts[1] == "projects":
		for name, id := range f.projects {
			if parts[2] == name || parts[2] == id {
				_, _ = fmt.Fprintf(w, `{"id":%q,"name":%q}`, id, name)
				return
			}
		}
		fail(http.StatusNotFound, "missing")
	case r.Method == http.MethodPost && r.URL.Path == "/v10/projects":
		var body struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.created++
		id := fmt.Sprintf("prj_%d", f.created)
		f.projects[body.Name] = id
		_, _ = fmt.Fprintf(w, `{"id":%q,"name":%q}`, id, body.Name)
	case r.Method == http.MethodDelete && len(parts) == 3 && parts[0] == "v9" && parts[1] == "projects":
		for name, id := range f.projects {
			if id == parts[2] {
				delete(f.projects, name)
			}
		}
		_, _ = io.WriteString(w, `{}`)
	case len(parts) >= 4 && parts[0] == "v9" && parts[3] == "custom-environments":
		project := parts[2]
		if f.onCustomEnvironment != nil {
			f.onCustomEnvironment()
		}
		if r.Method == http.MethodDelete {
			for key, id := range f.envs {
				if strings.HasPrefix(key, project+"/") && id == parts[4] {
					delete(f.envs, key)
				}
			}
			_, _ = io.WriteString(w, `{}`)
			return
		}
		if r.Method == http.MethodGet {
			if id, ok := f.envs[project+"/"+parts[4]]; ok {
				_, _ = fmt.Fprintf(w, `{"id":%q,"slug":%q}`, id, parts[4])
				return
			}
			fail(http.StatusNotFound, "missing")
			return
		}
		if f.customStatus != 0 {
			fail(f.customStatus, "custom environments need a Pro plan")
			return
		}
		var body struct{ Slug string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.envs[project+"/"+body.Slug] = "env_9"
		_, _ = fmt.Fprintf(w, `{"id":"env_9","slug":%q}`, body.Slug)
	case r.Method == http.MethodPost && len(parts) == 4 && parts[0] == "v10" && parts[3] == "env":
		var items []map[string]any
		_ = json.NewDecoder(r.Body).Decode(&items)
		f.envPosts = append(f.envPosts, items)
		if f.customVarStatus != 0 && len(items) > 0 && items[0]["customEnvironmentIds"] != nil {
			fail(f.customVarStatus, "sensitive variables aren't available for custom environments")
			return
		}
		_, _ = io.WriteString(w, `{}`)
	case r.Method == http.MethodPost && r.URL.Path == "/v13/deployments":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.deploys = append(f.deploys, body)
		_, _ = io.WriteString(w, `{"id":"dpl_1"}`)
	case r.Method == http.MethodGet && r.URL.Path == "/v13/deployments/dpl_1":
		_, _ = io.WriteString(w, `{"id":"dpl_1","readyState":"READY","alias":["web.vercel.app"]}`)
	default:
		_, _ = io.WriteString(w, `{}`)
	}
}

// calledExactly reports whether this exact request was made.
func (f *fakeVercelAPI) calledExactly(call string) bool {
	for _, c := range f.callLog() {
		if c == call {
			return true
		}
	}
	return false
}

func (f *fakeVercelAPI) called(prefix string) bool {
	for _, c := range f.callLog() {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func (f *fakeVercelAPI) askedForCustomEnvironment() bool {
	for _, c := range f.callLog() {
		if strings.Contains(c, "custom-environments") {
			return true
		}
	}
	return false
}

type nativeHarness struct {
	api    *fakeVercelAPI
	store  *roleStore
	notify *countingNotifier
	logs   []string
	job    *Job
	exec   *Vercel
}

// newNativeHarness builds a deploy of service web in an environment of the
// given name and kind. A nil store means no Environments.
func newNativeHarness(t *testing.T, environment, kind string, native bool) *nativeHarness {
	t.Helper()
	h := &nativeHarness{api: newFakeVercelAPI(), notify: &countingNotifier{}}
	server := httptest.NewServer(h.api)
	t.Cleanup(server.Close)
	archive := buildArchive(t, map[string]string{"package.json": "{}"})
	h.job = &Job{
		Deployment: &Deployment{OrgID: "org", ProjectID: "proj_abc12345", EnvironmentID: "env_" + environment, Environment: environment},
		Plan:       &planning.Plan{ID: "plan"},
		Action:     planning.Action{Name: "web", Service: &planning.ServiceConfig{Source: "."}},
		Connection: &connections.Connection{ID: "conn"},
		Credential: []byte("token"),
		Env:        map[string]string{"API_URL": "https://api", "TOKEN": "s3cret"},
		Secrets:    map[string]bool{"TOKEN": true},
		Progress:   func(_, m string) { h.logs = append(h.logs, m) },
		Source: func(context.Context) (io.ReadCloser, int64, error) {
			return io.NopCloser(bytes.NewReader(archive)), int64(len(archive)), nil
		},
		Checkpoint: func(raw []byte) error { h.api.record("checkpoint " + string(raw)); return nil },
	}
	h.exec = &Vercel{Client: vercel.New(server.URL)}
	if native {
		h.store = &roleStore{fakePEStore: newFakePEStore(), kind: kind, name: environment, first: true}
		h.exec.Environments = &NativeEnvironments{Store: h.store, Notify: h.notify}
	}
	return h
}

func (h *nativeHarness) row() *ProviderEnvironment {
	return h.store.rows["org|env_"+h.job.Deployment.Environment+"|vercel|conn|web"]
}

func (h *nativeHarness) apply(t *testing.T) *Outcome {
	t.Helper()
	out, err := h.exec.Apply(t.Context(), h.job)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return out
}

func TestVercelNativeStagingUsesCustomEnvironment(t *testing.T) {
	h := newNativeHarness(t, "staging", "staging", true)
	out := h.apply(t)

	if h.api.projects["web-abc12345"] != "prj_1" || len(h.api.projects) != 1 {
		t.Fatalf("projects = %v, want only web-abc12345", h.api.projects)
	}
	if !h.api.called("POST /v9/projects/prj_1/custom-environments") || h.api.envs["prj_1/staging"] != "env_9" {
		t.Fatalf("custom environment staging not created: %v", h.api.callLog())
	}
	if len(h.api.envPosts) == 0 {
		t.Fatal("no variables were written")
	}
	for _, post := range h.api.envPosts {
		for _, item := range post {
			ids, _ := item["customEnvironmentIds"].([]any)
			if len(ids) != 1 || ids[0] != "env_9" || item["target"] != nil {
				t.Fatalf("variable %v isn't scoped to env_9 alone", item)
			}
			if item["key"] == "TOKEN" && item["type"] != "sensitive" {
				t.Fatalf("secret written as %v", item["type"])
			}
		}
	}
	if len(h.api.deploys) != 1 || h.api.deploys[0]["customEnvironmentSlugOrId"] != "env_9" || h.api.deploys[0]["target"] != nil {
		t.Fatalf("deploy body = %v, want customEnvironmentSlugOrId env_9", h.api.deploys)
	}
	if out.ExternalID != "prj_1#env_9" {
		t.Fatalf("ExternalID = %q, want prj_1#env_9", out.ExternalID)
	}
	row := h.row()
	if row == nil || row.Mode != ModeNative || row.ProjectRef != "prj_1" || row.EnvironmentRef != "env_9" {
		t.Fatalf("row = %+v, want native prj_1/env_9", row)
	}
	// The target is checkpointed before any variable is written.
	var checkpointed bool
	for _, c := range h.api.callLog() {
		if strings.HasPrefix(c, "checkpoint ") && strings.Contains(c, `"target":"env_9"`) {
			checkpointed = true
		}
		if strings.HasPrefix(c, "POST /v10/projects/prj_1/env") {
			if !checkpointed {
				t.Fatal("variables were written before the target was checkpointed")
			}
			break
		}
	}
}

func TestVercelNativeProductionTarget(t *testing.T) {
	h := newNativeHarness(t, "production", "production", true)
	out := h.apply(t)

	if h.api.askedForCustomEnvironment() {
		t.Fatal("production made a custom environment")
	}
	if len(h.api.deploys) != 1 || h.api.deploys[0]["target"] != "production" {
		t.Fatalf("deploy body = %v, want target production", h.api.deploys)
	}
	for _, post := range h.api.envPosts {
		for _, item := range post {
			if targets, _ := item["target"].([]any); len(targets) != 1 || targets[0] != "production" || item["customEnvironmentIds"] != nil {
				t.Fatalf("variable %v isn't scoped to production alone", item)
			}
		}
	}
	if out.ExternalID != "prj_1#production" {
		t.Fatalf("ExternalID = %q, want prj_1#production", out.ExternalID)
	}
	if row := h.row(); row == nil || row.Mode != ModeNative || row.EnvironmentRef != "production" {
		t.Fatalf("row = %+v, want native production", row)
	}
}

func TestVercelNativePreviewTarget(t *testing.T) {
	h := newNativeHarness(t, "preview", "preview", true)
	out := h.apply(t)

	if h.api.askedForCustomEnvironment() {
		t.Fatal("preview made a custom environment")
	}
	if len(h.api.deploys) != 1 || h.api.deploys[0]["target"] != nil || h.api.deploys[0]["customEnvironmentSlugOrId"] != nil {
		t.Fatalf("deploy body = %v, want Vercel's default preview target", h.api.deploys)
	}
	for _, post := range h.api.envPosts {
		for _, item := range post {
			if targets, _ := item["target"].([]any); len(targets) != 1 || targets[0] != "preview" {
				t.Fatalf("variable %v isn't scoped to preview alone", item)
			}
		}
	}
	if out.ExternalID != "prj_1#preview" {
		t.Fatalf("ExternalID = %q, want prj_1#preview", out.ExternalID)
	}
}

func TestVercelFallsBackWhenCustomEnvironmentRefused(t *testing.T) {
	h := newNativeHarness(t, "qa", "staging", true)
	h.api.customStatus = http.StatusPaymentRequired
	out := h.apply(t)

	id := h.api.projects["web-qa-abc12345"]
	if id == "" {
		t.Fatalf("projects = %v, want web-qa-abc12345", h.api.projects)
	}
	if len(h.api.deploys) != 1 || h.api.deploys[0]["target"] != "production" || h.api.deploys[0]["project"] != id {
		t.Fatalf("deploy body = %v, want production of %s", h.api.deploys, id)
	}
	if out.ExternalID != id {
		t.Fatalf("ExternalID = %q, want the separate project %q", out.ExternalID, id)
	}
	row := h.row()
	if row == nil || row.Mode != ModeFallback || row.Permanent || row.Reason == "" {
		t.Fatalf("row = %+v, want a retryable fallback with a reason", row)
	}
	if h.notify.calls != 1 {
		t.Fatalf("notify calls = %d, want 1", h.notify.calls)
	}
	if !strings.Contains(strings.Join(h.logs, "\n"), "deploys to a separate project") {
		t.Fatalf("logs = %v, want the fallback line", h.logs)
	}
}

func TestVercelFallsBackWhenSensitiveVariableRefused(t *testing.T) {
	h := newNativeHarness(t, "staging", "staging", true)
	h.api.customVarStatus = http.StatusForbidden
	out := h.apply(t)

	id := h.api.projects["web-staging-abc12345"]
	if id == "" || out.ExternalID != id {
		t.Fatalf("ExternalID = %q, projects = %v, want the separate project", out.ExternalID, h.api.projects)
	}
	for _, post := range h.api.envPosts {
		for _, item := range post {
			if item["key"] == "TOKEN" && item["type"] != "sensitive" {
				t.Fatalf("secret written as %v", item["type"])
			}
		}
	}
	if row := h.row(); row == nil || row.Mode != ModeFallback {
		t.Fatalf("row = %+v, want a fallback", row)
	}
}

func TestVercelStickyFallback(t *testing.T) {
	h := newNativeHarness(t, "staging", "staging", true)
	_ = h.store.SaveProviderEnvironment(t.Context(), &ProviderEnvironment{OrgID: "org", EnvironmentID: "env_staging", Provider: "vercel", ConnectionID: "conn", Scope: "web", Mode: ModeFallback, Reason: "plan"})
	out := h.apply(t)

	if h.api.askedForCustomEnvironment() {
		t.Fatal("a fallen-back environment asked for a custom environment")
	}
	id := h.api.projects["web-staging-abc12345"]
	if id == "" || len(h.api.projects) != 1 || out.ExternalID != id {
		t.Fatalf("projects = %v, ExternalID = %q, want web-staging-abc12345 only", h.api.projects, out.ExternalID)
	}
	if h.notify.calls != 0 {
		t.Fatalf("notify calls = %d, want 0", h.notify.calls)
	}
}

func TestVercelTransientErrorDoesNotFallBack(t *testing.T) {
	h := newNativeHarness(t, "staging", "staging", true)
	h.api.customStatus = http.StatusServiceUnavailable
	if _, err := h.exec.Apply(t.Context(), h.job); err == nil {
		t.Fatal("a 503 deployed anyway")
	}
	// Only the claim on the shared project is recorded; a transient error
	// never records a fallback.
	if row := h.row(); len(h.store.rows) != 1 || row == nil || row.Mode != ModeNative || row.EnvironmentRef != "" {
		t.Fatalf("rows = %v, want only staging's native claim", h.store.rows)
	}
	if len(h.api.deploys) != 0 {
		t.Fatalf("deploys = %v, want none", h.api.deploys)
	}
}

func TestVercelNativeRetryReusesCustomEnvironment(t *testing.T) {
	h := newNativeHarness(t, "staging", "staging", true)
	h.api.projects["web-abc12345"] = "prj_1"
	h.job.Resume = []byte(`{"project_id":"prj_1","target":"env_9","mode":"native"}`)
	out := h.apply(t)

	if h.api.called("POST /v9/projects/prj_1/custom-environments") {
		t.Fatal("the retry made the custom environment again")
	}
	if len(h.api.deploys) != 1 || h.api.deploys[0]["customEnvironmentSlugOrId"] != "env_9" {
		t.Fatalf("deploy body = %v, want env_9", h.api.deploys)
	}
	if out.ExternalID != "prj_1#env_9" {
		t.Fatalf("ExternalID = %q, want prj_1#env_9", out.ExternalID)
	}
}

func TestVercelNilEnvironmentsIsLegacy(t *testing.T) {
	h := newNativeHarness(t, "staging", "staging", false)
	out := h.apply(t)

	id := h.api.projects["web-staging-abc12345"]
	if id == "" || len(h.api.projects) != 1 || out.ExternalID != id {
		t.Fatalf("projects = %v, ExternalID = %q, want web-staging-abc12345", h.api.projects, out.ExternalID)
	}
	if h.api.askedForCustomEnvironment() {
		t.Fatal("legacy deploy asked for a custom environment")
	}
	if len(h.api.deploys) != 1 || h.api.deploys[0]["target"] != "production" {
		t.Fatalf("deploy body = %v, want target production", h.api.deploys)
	}
	for _, c := range h.api.callLog() {
		if strings.HasPrefix(c, "checkpoint ") && strings.Contains(c, `"mode"`) {
			t.Fatalf("legacy checkpoint carries a mode: %s", c)
		}
	}
}

func TestVercelPendingResourceIsTheEnvironment(t *testing.T) {
	v := &Vercel{}
	for _, c := range []struct {
		resume, want string
		fails        bool
	}{
		{`{"project_id":"prj_old"}`, "prj_old", false},
		{`{"project_id":"prj_old","mode":"fallback"}`, "prj_old", false},
		{`{"project_id":"prj_1","target":"env_9","mode":"native"}`, "prj_1#env_9", false},
		{`{"project_id":"prj_1","mode":"native"}`, "", true},
	} {
		got, err := v.PendingCloudResource(t.Context(), &Job{Resume: []byte(c.resume)})
		if (err != nil) != c.fails || got != c.want {
			t.Errorf("PendingCloudResource(%s) = %q, %v; want %q, fails %v", c.resume, got, err, c.want, c.fails)
		}
	}
}

func TestVercelFallbackCleansUpWhatItCreated(t *testing.T) {
	h := newNativeHarness(t, "staging", "staging", true)
	h.api.customVarStatus = http.StatusPaymentRequired
	out := h.apply(t)

	if !h.api.called("DELETE /v9/projects/prj_1/custom-environments/env_9") || len(h.api.envs) != 0 {
		t.Fatalf("the custom environment this job made is still there: %v", h.api.callLog())
	}
	if !h.api.calledExactly("DELETE /v9/projects/prj_1") || h.api.projects["web-abc12345"] != "" {
		t.Fatalf("the shared project this job made is still there: %v", h.api.callLog())
	}
	id := h.api.projects["web-staging-abc12345"]
	if id == "" || out.ExternalID != id {
		t.Fatalf("ExternalID = %q, projects = %v, want the separate project", out.ExternalID, h.api.projects)
	}
	if row := h.row(); row == nil || row.Mode != ModeFallback {
		t.Fatalf("row = %+v, want a fallback", row)
	}
}

func TestVercelFallbackRetryCleansUpWhatItCreatedEarlier(t *testing.T) {
	h := newNativeHarness(t, "staging", "staging", true)
	h.api.projects["web-abc12345"] = "prj_1"
	h.api.envs["prj_1/staging"] = "env_9"
	h.api.created = 1
	h.api.customVarStatus = http.StatusPaymentRequired
	h.job.Resume = []byte(`{"project_id":"prj_1","target":"env_9","mode":"native","created_project":true,"created_environment":"env_9"}`)
	h.apply(t)

	if !h.api.called("DELETE /v9/projects/prj_1/custom-environments/env_9") || !h.api.calledExactly("DELETE /v9/projects/prj_1") {
		t.Fatalf("the retry left what the first attempt made: %v", h.api.callLog())
	}
}

func TestVercelFallbackKeepsSharedProject(t *testing.T) {
	for _, tc := range []struct {
		name              string
		prepare           func(h *nativeHarness)
		envStatus, varSts int
	}{
		{"another environment uses it", func(h *nativeHarness) {
			// Production's native deploy recorded the project this job
			// goes on to make; it is shared, so it stays.
			_ = h.store.SaveProviderEnvironment(t.Context(), &ProviderEnvironment{OrgID: "org", ProjectID: "proj_abc12345", EnvironmentID: "env_production", Provider: "vercel", ConnectionID: "conn", Scope: "web", Mode: ModeNative, ProjectRef: "prj_1", EnvironmentRef: "production"})
		}, http.StatusPaymentRequired, 0},
		{"the project existed before", func(h *nativeHarness) {
			h.api.projects["web-abc12345"] = "prj_1"
			h.api.created = 1
		}, http.StatusPaymentRequired, 0},
		{"the custom environment existed before", func(h *nativeHarness) {
			h.api.projects["web-abc12345"] = "prj_1"
			h.api.envs["prj_1/staging"] = "env_9"
			h.api.created = 1
		}, 0, http.StatusPaymentRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newNativeHarness(t, "staging", "staging", true)
			tc.prepare(h)
			h.api.customStatus, h.api.customVarStatus = tc.envStatus, tc.varSts
			h.apply(t)

			for _, c := range h.api.callLog() {
				if strings.HasPrefix(c, "DELETE /v9/projects/prj_1") {
					t.Fatalf("deleted what this job didn't make or another environment uses: %s", c)
				}
			}
			if h.api.projects["web-staging-abc12345"] == "" {
				t.Fatalf("projects = %v, want the separate project", h.api.projects)
			}
		})
	}
}

func TestVercelFallbackKeepsProjectClaimedByConcurrentDeploy(t *testing.T) {
	h := newNativeHarness(t, "staging", "staging", true)
	// Production found the project staging made and is deploying into it:
	// its claim has no environment ref yet.
	_ = h.store.SaveProviderEnvironment(t.Context(), &ProviderEnvironment{OrgID: "org", ProjectID: "proj_abc12345", EnvironmentID: "env_production", Provider: "vercel", ConnectionID: "conn", Scope: "web", Mode: ModeNative, ProjectRef: "prj_1"})
	h.api.customStatus = http.StatusPaymentRequired
	h.apply(t)

	if h.api.calledExactly("DELETE /v9/projects/prj_1") || h.api.projects["web-abc12345"] != "prj_1" {
		t.Fatalf("deleted a project production is deploying into: %v", h.api.callLog())
	}
	if row := h.row(); row == nil || row.Mode != ModeFallback || row.ProjectRef != "" {
		t.Fatalf("row = %+v, want staging's claim replaced by a fallback", row)
	}
}

func TestVercelNativeClaimsProjectBeforeWriting(t *testing.T) {
	h := newNativeHarness(t, "staging", "staging", true)
	var claimed *ProviderEnvironment
	h.api.onCustomEnvironment = func() { claimed = h.row() }
	h.apply(t)

	if claimed == nil || claimed.Mode != ModeNative || claimed.ProjectRef != "prj_1" || claimed.EnvironmentRef != "" {
		t.Fatalf("row before the custom environment = %+v, want a native claim on prj_1", claimed)
	}
	if row := h.row(); row.EnvironmentRef != "env_9" {
		t.Fatalf("row after = %+v, want env_9", row)
	}
}

func TestVercelRefusalAfterDeployDoesNotFallBack(t *testing.T) {
	h := newNativeHarness(t, "staging", "staging", true)
	h.api.projects["web-abc12345"] = "prj_1"
	h.api.envs["prj_1/staging"] = "env_9"
	h.api.created = 1
	h.api.customVarStatus = http.StatusPaymentRequired
	h.job.Resume = []byte(`{"project_id":"prj_1","deployment_id":"dpl_1","target":"env_9","mode":"native","created_project":true,"created_environment":"env_9"}`)
	if _, err := h.exec.Apply(t.Context(), h.job); err == nil {
		t.Fatal("a refusal after the deployment started fell back")
	}
	for _, c := range h.api.callLog() {
		if strings.HasPrefix(c, "DELETE ") {
			t.Fatalf("deleted the live deployment's environment or project: %s", c)
		}
	}
	if row := h.row(); row != nil && row.Mode == ModeFallback {
		t.Fatalf("row = %+v, want no fallback", row)
	}
	if h.api.projects["web-staging-abc12345"] != "" {
		t.Fatal("deployed to a separate project")
	}
}

// legacyBinding is web's binding on a Vercel project of its own.
func legacyBinding(drift string) *planning.Binding {
	return &planning.Binding{ID: "bind_web", Subject: planning.ServiceSubject, Name: "web", Provider: "vercel", ExternalID: "prj_old", ConnectionID: "conn", Drift: drift}
}

// TestVercelRefusesNativeOverLegacyBindingWithoutMove: a plan made while
// staging's fallback record still existed updates web on its own project.
// Run after "try again" cleared the record, it would deploy into the shared
// project and leave the old one serving with nothing tracking it.
func TestVercelRefusesNativeOverLegacyBindingWithoutMove(t *testing.T) {
	h := newNativeHarness(t, "staging", "staging", true)
	h.job.Binding = legacyBinding("")
	h.job.Action.Op = planning.Update

	_, err := h.exec.Apply(t.Context(), h.job)
	if err == nil || !strings.Contains(err.Error(), "Plan again") || !strings.Contains(err.Error(), "prj_old") {
		t.Fatalf("Apply: %v, want a refusal that says to plan again", err)
	}
	for _, c := range h.api.callLog() {
		if !strings.HasPrefix(c, "checkpoint ") {
			t.Fatalf("called Vercel: %v", h.api.callLog())
		}
	}
	if len(h.store.rows) != 0 {
		t.Fatalf("rows = %v, want none", h.store.rows)
	}
}

// A legacy binding still goes native when the plan moves it, when it's
// already native, or when what it ran on was deleted outside Loomplane.
func TestVercelGoesNativeOverLegacyBindingWhenAllowed(t *testing.T) {
	for name, set := range map[string]func(*Job){
		"native move":       func(j *Job) { j.Action.NativeMove = true },
		"deleted outside":   func(j *Job) { j.Binding = legacyBinding(planning.DriftMissing); j.Action.Op = planning.Create },
		"already native":    func(j *Job) { j.Binding.ExternalID = "prj_1#env_9" },
		"nothing bound yet": func(j *Job) { j.Binding = nil },
	} {
		t.Run(name, func(t *testing.T) {
			h := newNativeHarness(t, "staging", "staging", true)
			h.job.Binding = legacyBinding("")
			set(h.job)
			if out := h.apply(t); out.ExternalID != "prj_1#env_9" {
				t.Fatalf("ExternalID = %q, want prj_1#env_9", out.ExternalID)
			}
		})
	}
}

// TestVercelKeepsItsTargetWhenAnotherEnvironmentIsDeleted: prod-eu ran in
// its own custom environment while production held the production target.
// Production was deleted, so prod-eu is now the oldest production-kind
// environment, but it keeps deploying where it always did: moving onto the
// production target would hand it production's leftover variables and
// orphan its custom environment.
func TestVercelKeepsItsTargetWhenAnotherEnvironmentIsDeleted(t *testing.T) {
	for name, set := range map[string]func(*nativeHarness){
		"from the record": func(h *nativeHarness) {
			_ = h.store.SaveProviderEnvironment(t.Context(), &ProviderEnvironment{OrgID: "org", ProjectID: "proj_abc12345", EnvironmentID: "env_prod-eu", Provider: "vercel", ConnectionID: "conn", Scope: "web", Mode: ModeNative, ProjectRef: "prj_1", EnvironmentRef: "env_eu"})
		},
		"from the binding": func(h *nativeHarness) {
			h.job.Binding = &planning.Binding{ID: "bind_web", Subject: planning.ServiceSubject, Name: "web", Provider: "vercel", ExternalID: "prj_1#env_eu", ConnectionID: "conn"}
		},
		"from what it replaces": func(h *nativeHarness) {
			h.job.Replaces = &planning.Binding{ID: "bind_web", Subject: planning.ServiceSubject, Name: "web", Provider: "vercel", ExternalID: "prj_1#env_eu", ConnectionID: "conn_old"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newNativeHarness(t, "prod-eu", "production", true)
			h.api.projects["web-abc12345"] = "prj_1"
			h.api.envs["prj_1/prod-eu"] = "env_eu"
			h.api.created = 1
			set(h)
			out := h.apply(t)

			if h.api.called("POST /v9/projects/prj_1/custom-environments") {
				t.Fatal("made a custom environment")
			}
			if len(h.api.deploys) != 1 || h.api.deploys[0]["customEnvironmentSlugOrId"] != "env_eu" || h.api.deploys[0]["target"] != nil {
				t.Fatalf("deploy body = %v, want customEnvironmentSlugOrId env_eu", h.api.deploys)
			}
			for _, post := range h.api.envPosts {
				for _, item := range post {
					ids, _ := item["customEnvironmentIds"].([]any)
					if len(ids) != 1 || ids[0] != "env_eu" || item["target"] != nil {
						t.Fatalf("variable %v isn't scoped to env_eu alone", item)
					}
				}
			}
			if out.ExternalID != "prj_1#env_eu" {
				t.Fatalf("ExternalID = %q, want prj_1#env_eu", out.ExternalID)
			}
			if row := h.row(); row == nil || row.EnvironmentRef != "env_eu" {
				t.Fatalf("row = %+v, want env_eu", row)
			}
		})
	}
}

// TestVercelRefusalOnNativeEnvironmentFailsInsteadOfFallingBack: staging
// already deploys natively, so a refusal now fails the deploy. Falling
// back would rebind web to a separate project and leave its live custom
// environment serving with nothing tracking it.
func TestVercelRefusalOnNativeEnvironmentFailsInsteadOfFallingBack(t *testing.T) {
	for name, set := range map[string]func(*nativeHarness){
		"native binding": func(h *nativeHarness) {
			h.job.Binding = &planning.Binding{ID: "bind_web", Subject: planning.ServiceSubject, Name: "web", Provider: "vercel", ExternalID: "prj_1#env_9", ConnectionID: "conn"}
			h.job.Action.Op = planning.Update
		},
		"native record": func(h *nativeHarness) {
			_ = h.store.SaveProviderEnvironment(t.Context(), &ProviderEnvironment{OrgID: "org", ProjectID: "proj_abc12345", EnvironmentID: "env_staging", Provider: "vercel", ConnectionID: "conn", Scope: "web", Mode: ModeNative, ProjectRef: "prj_1", EnvironmentRef: "env_9"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newNativeHarness(t, "staging", "staging", true)
			h.api.projects["web-abc12345"] = "prj_1"
			h.api.envs["prj_1/staging"] = "env_9"
			h.api.created = 1
			h.api.customVarStatus = http.StatusPaymentRequired
			set(h)

			if _, err := h.exec.Apply(t.Context(), h.job); err == nil {
				t.Fatal("a refusal on a native environment fell back")
			}
			for _, c := range h.api.callLog() {
				if strings.HasPrefix(c, "DELETE ") {
					t.Fatalf("deleted the native environment or project: %s", c)
				}
			}
			if row := h.row(); row != nil && row.Mode == ModeFallback {
				t.Fatalf("row = %+v, want no fallback", row)
			}
			if h.api.projects["web-staging-abc12345"] != "" || h.notify.calls != 0 {
				t.Fatalf("deployed to a separate project: %v", h.api.projects)
			}
		})
	}
}
