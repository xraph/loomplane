package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/models"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// fakeModels records what each route asks of the models service and
// answers err when it's set.
type fakeModels struct {
	mu     sync.Mutex
	calls  []string
	err    error
	query  models.RegistryQuery
	ids    []string
	inputs []models.DeployInput
}

func (f *fakeModels) record(op string, p security.Principal, extra ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.TrimSpace(fmt.Sprintf("%s %s %s %s", op, p.OrgID, p.UserID, fmt.Sprint(extra...))))
	return f.err
}

func (f *fakeModels) Registry(_ context.Context, p security.Principal, q models.RegistryQuery) (*models.RegistryList, error) {
	f.query = q
	if err := f.record("registry", p); err != nil {
		return nil, err
	}
	return &models.RegistryList{Entries: []models.EntrySummary{}, Counts: map[string]int{}}, nil
}

func (f *fakeModels) Entry(_ context.Context, p security.Principal, id string) (*models.EntryDetail, error) {
	f.ids = append(f.ids, id)
	if err := f.record("entry", p, id); err != nil {
		return nil, err
	}
	return &models.EntryDetail{EntrySummary: models.EntrySummary{ID: id}}, nil
}

func (f *fakeModels) Card(_ context.Context, p security.Principal, id string) (*models.Card, error) {
	f.ids = append(f.ids, id)
	if err := f.record("card", p, id); err != nil {
		return nil, err
	}
	return &models.Card{Markdown: "# Card"}, nil
}

func (f *fakeModels) Inspect(_ context.Context, p security.Principal, repo, revision string) (*models.EntryDetail, error) {
	if err := f.record("inspect", p, repo, "@", revision); err != nil {
		return nil, err
	}
	return &models.EntryDetail{EntrySummary: models.EntrySummary{ID: "hf:" + repo}}, nil
}

func (f *fakeModels) List(_ context.Context, p security.Principal) ([]*models.Status, error) {
	if err := f.record("list", p); err != nil {
		return nil, err
	}
	return nil, nil
}

func (f *fakeModels) Preview(_ context.Context, p security.Principal, projectID, environmentID string, in models.DeployInput) (*models.DeployPreview, error) {
	f.inputs = append(f.inputs, in)
	if err := f.record("preview", p, projectID, "/", environmentID, " ", in.EntryID); err != nil {
		return nil, err
	}
	return &models.DeployPreview{Resource: "llm"}, nil
}

func (f *fakeModels) Deploy(_ context.Context, p security.Principal, projectID, environmentID string, in models.DeployInput) (*models.DeployResult, error) {
	f.inputs = append(f.inputs, in)
	if err := f.record("deploy", p, projectID, "/", environmentID, " ", in.EntryID); err != nil {
		return nil, err
	}
	return &models.DeployResult{PlanID: "plan_1"}, nil
}

func (f *fakeModels) Status(_ context.Context, p security.Principal, projectID, environmentID, resource string) (*models.Status, error) {
	if err := f.record("status", p, projectID, "/", environmentID, "/", resource); err != nil {
		return nil, err
	}
	return &models.Status{State: "ready"}, nil
}

func (f *fakeModels) Update(_ context.Context, p security.Principal, projectID, environmentID, resource string, in models.UpdateInput) (*models.Status, error) {
	budget := ""
	if in.MonthlyBudgetUSD != nil {
		budget = *in.MonthlyBudgetUSD
	}
	if err := f.record("update", p, projectID, "/", environmentID, "/", resource, " ", budget, " ", in.Revision); err != nil {
		return nil, err
	}
	return &models.Status{State: "ready"}, nil
}

func (f *fakeModels) MovePreview(_ context.Context, p security.Principal, projectID, environmentID, resource string, in models.MoveInput) (*models.DeployPreview, error) {
	if err := f.record("movePreview", p, projectID, "/", environmentID, "/", resource, " ", in.Target.ConnectionID, " ", in.ReviewServer); err != nil {
		return nil, err
	}
	return &models.DeployPreview{Resource: resource}, nil
}

func (f *fakeModels) Move(_ context.Context, p security.Principal, projectID, environmentID, resource string, in models.MoveInput) (*models.DeployResult, error) {
	if err := f.record("move", p, projectID, "/", environmentID, "/", resource, " ", in.Target.ConnectionID); err != nil {
		return nil, err
	}
	return &models.DeployResult{PlanID: "plan_2"}, nil
}

func (f *fakeModels) MoveToPin(_ context.Context, p security.Principal, projectID, environmentID, resource string) (*models.DeployResult, error) {
	if err := f.record("moveToPin", p, projectID, "/", environmentID, "/", resource); err != nil {
		return nil, err
	}
	return &models.DeployResult{PlanID: "plan_4"}, nil
}

func (f *fakeModels) Retry(_ context.Context, p security.Principal, projectID, environmentID, resource string) (*models.Status, error) {
	if err := f.record("retry", p, projectID, "/", environmentID, "/", resource); err != nil {
		return nil, err
	}
	return &models.Status{State: "starting"}, nil
}

func (f *fakeModels) CancelWaiting(_ context.Context, p security.Principal, projectID, environmentID, resource string) (*models.CancelResult, error) {
	if err := f.record("cancel", p, projectID, "/", environmentID, "/", resource); err != nil {
		return nil, err
	}
	return &models.CancelResult{Message: "gpu-box was already approved"}, nil
}

func (f *fakeModels) Remove(_ context.Context, p security.Principal, projectID, environmentID, resource string) (*models.DeployResult, error) {
	if err := f.record("remove", p, projectID, "/", environmentID, "/", resource); err != nil {
		return nil, err
	}
	return &models.DeployResult{PlanID: "plan_3", Message: "Its weights stay on gpu-box."}, nil
}

// fakeSettings records the settings routes' calls.
type fakeSettings struct {
	calls []string
	err   error
}

func (f *fakeSettings) Get(_ context.Context, p security.Principal) (*aisettings.View, error) {
	f.calls = append(f.calls, "get "+p.OrgID+" "+p.UserID)
	if f.err != nil {
		return nil, f.err
	}
	return &aisettings.View{CanManage: true}, nil
}

func (f *fakeSettings) PutHuggingFaceToken(_ context.Context, p security.Principal, _ string) (*aisettings.HuggingFaceToken, error) {
	f.calls = append(f.calls, "putToken "+p.OrgID+" "+p.UserID)
	if f.err != nil {
		return nil, f.err
	}
	return &aisettings.HuggingFaceToken{Hint: "hf_…abcd"}, nil
}

func (f *fakeSettings) DeleteHuggingFaceToken(_ context.Context, p security.Principal) error {
	f.calls = append(f.calls, "deleteToken "+p.OrgID+" "+p.UserID)
	return f.err
}

const envBase = "/api/v1/projects/proj_1/environments/env_1/ai/models"

type call struct{ method, path, body string }

var calls = []call{
	{http.MethodGet, "/api/v1/ai/models/registry", ""},
	{http.MethodGet, "/api/v1/ai/models/registry/entry?id=Qwen%2FQwen2.5-7B-Instruct", ""},
	{http.MethodGet, "/api/v1/ai/models/registry/card?id=Qwen%2FQwen2.5-7B-Instruct", ""},
	{http.MethodPost, "/api/v1/ai/models/inspect", `{"repo":"a/b","revision":"main"}`},
	{http.MethodGet, "/api/v1/ai/models/deployments", ""},
	{http.MethodPost, envBase + "/preview", `{"entry_id":"Qwen/Qwen2.5-7B-Instruct","target":{"source":"connection","connection_id":"conn_1"}}`},
	{http.MethodPost, envBase, `{"entry_id":"Qwen/Qwen2.5-7B-Instruct","target":{"source":"connection","connection_id":"conn_1"}}`},
	{http.MethodGet, envBase + "/llm", ""},
	{http.MethodPatch, envBase + "/llm", `{"monthly_budget_usd":"250.00","revision":3}`},
	{http.MethodPost, envBase + "/llm/move-preview", `{"target":{"source":"connection","connection_id":"conn_2"},"review_server":true}`},
	{http.MethodPost, envBase + "/llm/move", `{"target":{"source":"connection","connection_id":"conn_2"}}`},
	{http.MethodPost, envBase + "/llm/move-to-pin", ""},
	{http.MethodPost, envBase + "/llm/retry", ""},
	{http.MethodDelete, envBase + "/llm/waiting", ""},
	{http.MethodDelete, envBase + "/llm", ""},
	{http.MethodGet, "/api/v1/ai/settings", ""},
	{http.MethodPut, "/api/v1/ai/settings/huggingface-token", `{"token":"hf_abcdefghabcd"}`},
	{http.MethodDelete, "/api/v1/ai/settings/huggingface-token", ""},
}

func mounted(t *testing.T, guard identity.Guard, m modelsAPI, s settingsAPI) forge.Router {
	t.Helper()
	r := forge.NewRouter()
	if err := mountModels(r, guard, m, s); err != nil {
		t.Fatal(err)
	}
	return r
}

// do makes a request as p, or with no principal when p is nil.
func do(t *testing.T, r forge.Router, c call, p *security.Principal) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), c.method, c.path, strings.NewReader(c.body))
	if c.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if p != nil {
		req = req.WithContext(security.WithPrincipal(req.Context(), *p))
	}
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}

var member = &security.Principal{OrgID: "org_1", UserID: "usr_1"}

// The kept routes, once each, behind the identity guard, and none of the
// ones deferred until the gateway. No route serves Endpoints, whose
// answer holds each model's key.
func TestModelRoutesRegistered(t *testing.T) {
	guarded := 0
	guard := identity.Guard{func(forge.Handler) forge.Handler {
		return func(forge.Context) error {
			guarded++
			return forge.NewHTTPError(http.StatusTeapot, "the guard")
		}
	}}
	m, s := &fakeModels{}, &fakeSettings{}
	r := mounted(t, guard, m, s)
	const p = "/api/v1/projects/:projectId/environments/:environmentId/ai/models"
	want := map[string]string{
		"aiModels.registry":                 "GET /api/v1/ai/models/registry",
		"aiModels.entry":                    "GET /api/v1/ai/models/registry/entry",
		"aiModels.card":                     "GET /api/v1/ai/models/registry/card",
		"aiModels.inspect":                  "POST /api/v1/ai/models/inspect",
		"aiModelDeployments.list":           "GET /api/v1/ai/models/deployments",
		"aiModels.preview":                  "POST " + p + "/preview",
		"aiModels.deploy":                   "POST " + p,
		"aiModelDeployments.get":            "GET " + p + "/:resource",
		"aiModelDeployments.update":         "PATCH " + p + "/:resource",
		"aiModelDeployments.movePreview":    "POST " + p + "/:resource/move-preview",
		"aiModelDeployments.move":           "POST " + p + "/:resource/move",
		"aiModelDeployments.moveToPin":      "POST " + p + "/:resource/move-to-pin",
		"aiModelDeployments.retry":          "POST " + p + "/:resource/retry",
		"aiModelDeployments.cancel":         "DELETE " + p + "/:resource/waiting",
		"aiModelDeployments.remove":         "DELETE " + p + "/:resource",
		"aiSettings.get":                    "GET /api/v1/ai/settings",
		"aiSettings.putHuggingFaceToken":    "PUT /api/v1/ai/settings/huggingface-token",
		"aiSettings.deleteHuggingFaceToken": "DELETE /api/v1/ai/settings/huggingface-token",
	}
	got := map[string]string{}
	for _, info := range r.Routes() {
		if _, dup := got[info.Name]; dup {
			t.Errorf("%s registered twice", info.Name)
		}
		got[info.Name] = info.Method + " " + info.Path
		tag := "AI models"
		if strings.HasPrefix(info.Name, "aiSettings.") {
			tag = "AI"
		}
		if !slices.Contains(info.Tags, tag) {
			t.Errorf("%s has tags %v, want %s", info.Name, info.Tags, tag)
		}
		if strings.Contains(strings.ToLower(info.Path), "endpoint") {
			t.Errorf("%s serves %s", info.Name, info.Path)
		}
	}
	for name, route := range want {
		if got[name] != route {
			t.Errorf("%s is %q, want %q", name, got[name], route)
		}
	}
	if len(got) != len(want) {
		t.Errorf("routes %v", got)
	}
	for _, deferred := range []string{"aiModels.use", "aiModels.usable", "aiModelUsage.series", "aiModelDeployments.tryKey", "aiModelDeployments.updatePreview"} {
		if _, ok := got[deferred]; ok {
			t.Errorf("%s is deferred until the gateway, and it's registered", deferred)
		}
	}
	for _, c := range calls {
		if out := do(t, r, c, member); out.Code != http.StatusTeapot {
			t.Errorf("%s %s: %d past the guard", c.method, c.path, out.Code)
		}
	}
	if guarded != len(calls) || len(m.calls) != 0 || len(s.calls) != 0 {
		t.Errorf("guarded %d, calls %v %v", guarded, m.calls, s.calls)
	}
}

func TestModelRoutesCallTheService(t *testing.T) {
	m, s := &fakeModels{}, &fakeSettings{}
	r := mounted(t, nil, m, s)
	wantCodes := map[int]int{6: http.StatusCreated, 17: http.StatusNoContent}
	for i, c := range calls {
		out := do(t, r, c, member)
		want, ok := wantCodes[i]
		if !ok {
			want = http.StatusOK
		}
		if out.Code != want {
			t.Fatalf("%s %s: %d %s", c.method, c.path, out.Code, out.Body.String())
		}
		if want != http.StatusNoContent && !json.Valid(out.Body.Bytes()) {
			t.Fatalf("%s %s answered %q", c.method, c.path, out.Body.String())
		}
	}
	wantCalls := []string{
		"registry org_1 usr_1",
		"entry org_1 usr_1 Qwen/Qwen2.5-7B-Instruct",
		"card org_1 usr_1 Qwen/Qwen2.5-7B-Instruct",
		"inspect org_1 usr_1 a/b@main",
		"list org_1 usr_1",
		"preview org_1 usr_1 proj_1/env_1 Qwen/Qwen2.5-7B-Instruct",
		"deploy org_1 usr_1 proj_1/env_1 Qwen/Qwen2.5-7B-Instruct",
		"status org_1 usr_1 proj_1/env_1/llm",
		"update org_1 usr_1 proj_1/env_1/llm 250.00 3",
		"movePreview org_1 usr_1 proj_1/env_1/llm conn_2 true",
		"move org_1 usr_1 proj_1/env_1/llm conn_2",
		"moveToPin org_1 usr_1 proj_1/env_1/llm",
		"retry org_1 usr_1 proj_1/env_1/llm",
		"cancel org_1 usr_1 proj_1/env_1/llm",
		"remove org_1 usr_1 proj_1/env_1/llm",
	}
	if !slices.Equal(m.calls, wantCalls) {
		t.Errorf("calls\n%s\nwant\n%s", strings.Join(m.calls, "\n"), strings.Join(wantCalls, "\n"))
	}
	if want := []string{"get org_1 usr_1", "putToken org_1 usr_1", "deleteToken org_1 usr_1"}; !slices.Equal(s.calls, want) {
		t.Errorf("settings calls %v", s.calls)
	}
	// An empty list is a list, not null.
	out := do(t, r, calls[4], member)
	if body := strings.TrimSpace(out.Body.String()); body != `{"models":[]}` {
		t.Errorf("list: %s", body)
	}
	// A cancel says what it leaves behind.
	out = do(t, r, calls[13], member)
	if !strings.Contains(out.Body.String(), "gpu-box was already approved") {
		t.Errorf("cancel: %s", out.Body.String())
	}
}

// Every route needs a principal, and the organization the service sees is
// the principal's, whatever the path says.
func TestModelRoutesRefuseWithoutAPrincipal(t *testing.T) {
	m, s := &fakeModels{}, &fakeSettings{}
	r := mounted(t, nil, m, s)
	for _, c := range calls {
		if out := do(t, r, c, nil); out.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no principal: %d", c.method, c.path, out.Code)
		}
	}
	if len(m.calls) != 0 || len(s.calls) != 0 {
		t.Errorf("calls %v %v", m.calls, s.calls)
	}
}

func TestRegistryRouteFilters(t *testing.T) {
	m := &fakeModels{}
	r := mounted(t, nil, m, &fakeSettings{})
	out := do(t, r, call{http.MethodGet, "/api/v1/ai/models/registry?task=embedding&commercial=true&size=small&license=apache-2.0&q=qwen&fits=1&tested=false", ""}, member)
	if out.Code != http.StatusOK {
		t.Fatalf("%d %s", out.Code, out.Body.String())
	}
	want := models.RegistryQuery{Q: "qwen", Task: "embedding", Size: "small", License: "apache-2.0", Commercial: true, Fits: true}
	if m.query != want {
		t.Errorf("query %+v, want %+v", m.query, want)
	}
	m.calls = nil
	out = do(t, r, call{http.MethodGet, "/api/v1/ai/models/registry?commercial=yes", ""}, member)
	if out.Code != http.StatusUnprocessableEntity || !strings.Contains(out.Body.String(), "commercial is true or false") || len(m.calls) != 0 {
		t.Errorf("bad flag: %d %s %v", out.Code, out.Body.String(), m.calls)
	}
}

func TestEntryIDWithSlash(t *testing.T) {
	m := &fakeModels{}
	r := mounted(t, nil, m, &fakeSettings{})
	for _, path := range []string{
		"/api/v1/ai/models/registry/entry?id=meta-llama%2FLlama-3.1-8B-Instruct",
		"/api/v1/ai/models/registry/card?id=meta-llama%2FLlama-3.1-8B-Instruct",
		"/api/v1/ai/models/registry/entry?id=meta-llama/Llama-3.1-8B-Instruct",
		"/api/v1/ai/models/registry/entry?id=hf%3Ameta-llama%2FLlama-3.1-8B-Instruct%400123456789ab",
	} {
		if out := do(t, r, call{http.MethodGet, path, ""}, member); out.Code != http.StatusOK {
			t.Errorf("%s: %d %s", path, out.Code, out.Body.String())
		}
	}
	want := []string{
		"meta-llama/Llama-3.1-8B-Instruct", "meta-llama/Llama-3.1-8B-Instruct", "meta-llama/Llama-3.1-8B-Instruct",
		"hf:meta-llama/Llama-3.1-8B-Instruct@0123456789ab",
	}
	if !slices.Equal(m.ids, want) {
		t.Errorf("ids %v", m.ids)
	}
	out := do(t, r, call{http.MethodGet, "/api/v1/ai/models/registry/entry", ""}, member)
	if out.Code != http.StatusUnprocessableEntity {
		t.Errorf("no id: %d", out.Code)
	}
}

// Moving is set only by a move. A request that names it is read as if it
// didn't, and the API's DeployInput has every other field of the
// service's.
func TestDeployBodyCannotSetMoving(t *testing.T) {
	m := &fakeModels{}
	r := mounted(t, nil, m, &fakeSettings{})
	body := `{"entry_id":"Qwen/Qwen2.5-7B-Instruct","quantization":"bf16","resource":"llm","max_model_len":8192,` +
		`"monthly_budget_usd":"100.00","review_server":true,"server":{"cloud_id":"c","operation_id":"o","review":"r"},` +
		`"target":{"source":"server_size","connection_id":"conn_1","region":"nyc2","size_id":"gpu-h100x1"},` +
		`"moving":{"kind":"model-vllm","connection_id":"conn_old","phase":"ready","plan_id":"plan_old"}}`
	for _, path := range []string{envBase + "/preview", envBase} {
		if out := do(t, r, call{http.MethodPost, path, body}, member); out.Code >= 300 {
			t.Fatalf("%s: %d %s", path, out.Code, out.Body.String())
		}
	}
	want := models.DeployInput{
		EntryID: "Qwen/Qwen2.5-7B-Instruct", Quantization: "bf16", Resource: "llm", MaxModelLen: 8192, MonthlyBudgetUSD: "100.00",
		ReviewServer: true, Server: &models.ServerApproval{CloudID: "c", OperationID: "o", Review: "r"},
		Target: models.TargetChoice{Source: "server_size", ConnectionID: "conn_1", Region: "nyc2", SizeID: "gpu-h100x1"},
	}
	if len(m.inputs) != 2 {
		t.Fatalf("inputs %+v", m.inputs)
	}
	for _, got := range m.inputs {
		if got.Moving != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("input %+v, want %+v", got, want)
		}
	}
	// The API's DeployInput is the service's without moving, field for
	// field, so a field the service gains can't be dropped silently.
	api, svc := jsonFields(reflect.TypeFor[DeployInput]()), jsonFields(reflect.TypeFor[models.DeployInput]())
	svc = slices.DeleteFunc(svc, func(f string) bool { return strings.HasPrefix(f, "moving:") })
	if !slices.Equal(api, svc) {
		t.Errorf("API DeployInput %v, service %v without moving", api, svc)
	}
}

func jsonFields(t reflect.Type) []string {
	var out []string
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		out = append(out, name+":"+f.Type.String())
	}
	return out
}

// What a refusal answers: the service's sentence and a code, never the
// text of an error Loomplane doesn't know.
func TestModelRoutesAnswerRefusals(t *testing.T) {
	m := &fakeModels{}
	r := mounted(t, nil, m, &fakeSettings{})
	deploy := call{http.MethodPost, envBase, `{"entry_id":"x/y","target":{"source":"connection","connection_id":"conn_1"}}`}
	for _, c := range []struct {
		err  error
		want int
		body string
	}{
		{&models.ProblemError{Problem: models.Problem{Code: models.ProblemNameTaken, Message: "llm is already a model in this environment. Pick another name."}},
			http.StatusUnprocessableEntity, `{"code":"model-name-taken","message":"llm is already a model in this environment. Pick another name."}`},
		{fmt.Errorf("%w (ai:edit)", security.ErrForbidden), http.StatusForbidden, ""},
		{projects.ErrEnvironmentNotFound, http.StatusNotFound, `{"code":"not_found","message":"environment not found"}`},
		{fmt.Errorf("%w: llm changed since you read it. Load it again", models.ErrStale), http.StatusConflict,
			`{"code":"stale","message":"llm changed since you read it. Load it again"}`},
		{errors.New("pq: password authentication failed for user loomplane at 10.1.2.3"), http.StatusInternalServerError, ""},
	} {
		m.err = c.err
		out := do(t, r, deploy, member)
		if out.Code != c.want {
			t.Errorf("%v: %d %s", c.err, out.Code, out.Body.String())
		}
		if c.body != "" && strings.TrimSpace(out.Body.String()) != c.body {
			t.Errorf("%v: %s", c.err, out.Body.String())
		}
		if strings.Contains(out.Body.String(), "10.1.2.3") || strings.Contains(out.Body.String(), "password") {
			t.Errorf("internal text in %s", out.Body.String())
		}
	}
	// A body that isn't JSON is a bad request, and the answer doesn't
	// repeat it.
	m.err, m.calls = nil, nil
	out := do(t, r, call{http.MethodPut, "/api/v1/ai/settings/huggingface-token", `{"token":"hf_secretsecret`}, member)
	if out.Code != http.StatusBadRequest || strings.Contains(out.Body.String(), "hf_secretsecret") {
		t.Errorf("bad body: %d %s", out.Code, out.Body.String())
	}
}

// roleAuthz allows what the principal's role allows in security.Catalog.
// The principal's UserID names its role.
type roleAuthz struct{}

func (roleAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, _ string) error {
	if ok, _ := (roleAuthz{}).Allowed(context.Background(), p, perm); ok {
		return nil
	}
	return fmt.Errorf("%w (%s)", security.ErrForbidden, perm.Name())
}

func (roleAuthz) Allowed(_ context.Context, p security.Principal, perm security.Permission) (bool, error) {
	for _, role := range security.Catalog() {
		if role.Slug == p.UserID && slices.Contains(role.Permissions, perm) {
			return true, nil
		}
	}
	return false, nil
}

func as(role string) *security.Principal {
	return &security.Principal{OrgID: "org_1", UserID: role}
}

// memSettings is the settings store in memory: settings and the token's
// details, which is all the routes reach.
type memSettings struct {
	aisettings.Store
	mu sync.Mutex
	hf *aisettings.HuggingFaceToken
}

func (s *memSettings) Settings(_ context.Context, orgID string) (*aisettings.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := &aisettings.Settings{OrgID: orgID, MonthlyBudgetUSD: "0.00", Embeddings: aisettings.Embeddings{Mode: "local"}}
	if s.hf != nil {
		hf := *s.hf
		st.HuggingFace = &hf
	}
	return st, nil
}

func (*memSettings) ProviderKeys(context.Context, string) ([]*aisettings.ProviderKey, error) {
	return []*aisettings.ProviderKey{}, nil
}

func (*memSettings) Allocations(context.Context, string) ([]*aisettings.Allocation, error) {
	return []*aisettings.Allocation{}, nil
}

func (s *memSettings) SetHuggingFaceToken(_ context.Context, _, hint, account string) (*aisettings.HuggingFaceToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	revision := int64(1)
	if s.hf != nil {
		revision = s.hf.Revision + 1
	}
	s.hf = &aisettings.HuggingFaceToken{Hint: hint, Account: account, SetAt: time.Unix(1_800_000_000, 0).UTC(), Revision: revision}
	hf := *s.hf
	return &hf, nil
}

func (s *memSettings) ClearHuggingFaceToken(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hf = nil
	return nil
}

func (*memSettings) LockHuggingFaceToken(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

// memTokens keeps token values in memory.
type memTokens struct{ values map[string]string }

func (m *memTokens) Put(_ context.Context, orgID, token string) error {
	m.values[orgID] = token
	return nil
}

func (m *memTokens) Reveal(_ context.Context, orgID string) (string, error) {
	v, ok := m.values[orgID]
	if !ok {
		return "", aisettings.ErrNotFound
	}
	return v, nil
}

func (m *memTokens) Delete(_ context.Context, orgID string) error {
	delete(m.values, orgID)
	return nil
}

// whoAmI says whose a token is. A token with "refuse" in it fails with an
// error that quotes it, as a careless Hub client might.
type whoAmI struct{}

func (whoAmI) WhoAmI(_ context.Context, token string) (string, error) {
	if strings.Contains(token, "refuse") {
		return "", errors.New("whoami answered 500 for " + token)
	}
	return "rex", nil
}

type noAudit struct{}

func (noAudit) Record(context.Context, projects.AuditEvent) {}

type noProjects struct{}

func (noProjects) GetProject(context.Context, string, string) (*projects.Project, error) {
	return nil, projects.ErrNotFound
}

// realSettings is the AI settings service over memory, with permissions
// from the role catalog.
func realSettings() (*aisettings.Service, *memTokens) {
	tokens := &memTokens{values: map[string]string{}}
	svc := aisettings.NewService(aisettings.Deps{
		Store: &memSettings{}, HuggingFace: tokens, Authz: roleAuthz{}, Audit: noAudit{}, Projects: noProjects{},
	})
	svc.SetTokenChecker(whoAmI{})
	return svc, tokens
}

func TestHuggingFaceTokenNeverEchoed(t *testing.T) {
	settings, tokens := realSettings()
	r := mounted(t, nil, &fakeModels{}, settings)
	const token = "hf_test_value"
	put := do(t, r, call{http.MethodPut, "/api/v1/ai/settings/huggingface-token", `{"token":"` + token + `"}`}, as(security.RoleAdmin))
	if put.Code != http.StatusOK {
		t.Fatalf("put: %d %s", put.Code, put.Body.String())
	}
	if tokens.values["org_1"] != token {
		t.Fatalf("the token wasn't saved: %v", tokens.values)
	}
	get := do(t, r, call{http.MethodGet, "/api/v1/ai/settings", ""}, as(security.RoleAdmin))
	if get.Code != http.StatusOK {
		t.Fatalf("get: %d %s", get.Code, get.Body.String())
	}
	var view struct {
		HuggingFace struct {
			Hint    string `json:"hint"`
			Account string `json:"account"`
		} `json:"huggingface"`
		CanManage bool `json:"can_manage"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &view); err != nil || view.HuggingFace.Hint != "hf_…alue" || view.HuggingFace.Account != "rex" || !view.CanManage {
		t.Errorf("view %s: %v", get.Body.String(), err)
	}
	// A token Hugging Face refused, with an error quoting it.
	const refused = "hf_refuse_this_one"
	bad := do(t, r, call{http.MethodPut, "/api/v1/ai/settings/huggingface-token", `{"token":"` + refused + `"}`}, as(security.RoleAdmin))
	for name, out := range map[string]*httptest.ResponseRecorder{"put": put, "get": get, "refused put": bad} {
		for _, secret := range []string{token, refused, "test_value", "refuse_this"} {
			if strings.Contains(out.Body.String(), secret) || strings.Contains(fmt.Sprint(out.Header()), secret) {
				t.Errorf("%s echoes %q: %s", name, secret, out.Body.String())
			}
		}
	}
	if bad.Code < 400 {
		t.Errorf("refused put: %d", bad.Code)
	}
	del := do(t, r, call{http.MethodDelete, "/api/v1/ai/settings/huggingface-token", ""}, as(security.RoleAdmin))
	if del.Code != http.StatusNoContent || del.Body.Len() != 0 {
		t.Errorf("delete: %d %s", del.Code, del.Body.String())
	}
}

// A viewer reads the AI settings; a developer can't change the token.
func TestViewerReadsDeveloperCannotManage(t *testing.T) {
	settings, tokens := realSettings()
	r := mounted(t, nil, &fakeModels{}, settings)
	out := do(t, r, call{http.MethodGet, "/api/v1/ai/settings", ""}, as(security.RoleViewer))
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"can_manage":false`) {
		t.Errorf("viewer get: %d %s", out.Code, out.Body.String())
	}
	out = do(t, r, call{http.MethodPut, "/api/v1/ai/settings/huggingface-token", `{"token":"hf_abcdefghabcd"}`}, as(security.RoleDeveloper))
	if out.Code != http.StatusForbidden || len(tokens.values) != 0 {
		t.Errorf("developer put: %d %s, tokens %v", out.Code, out.Body.String(), tokens.values)
	}
	out = do(t, r, call{http.MethodDelete, "/api/v1/ai/settings/huggingface-token", ""}, as(security.RoleDeveloper))
	if out.Code != http.StatusForbidden {
		t.Errorf("developer delete: %d %s", out.Code, out.Body.String())
	}
}

// memEnvironments are one project's environments.
type memEnvironments struct{}

func (memEnvironments) GetProject(_ context.Context, orgID, id string) (*projects.Project, error) {
	if orgID != "org_1" || id != "proj_1" {
		return nil, projects.ErrNotFound
	}
	return &projects.Project{ID: id, OrgID: orgID, Name: "shop"}, nil
}

func (memEnvironments) GetEnvironment(_ context.Context, orgID, projectID, id string) (*projects.Environment, error) {
	if orgID != "org_1" || projectID != "proj_1" {
		return nil, projects.ErrEnvironmentNotFound
	}
	switch id {
	case "env_1":
		return &projects.Environment{ID: id, OrgID: orgID, ProjectID: projectID, Name: "staging", Kind: projects.KindStaging}, nil
	case "env_prod":
		return &projects.Environment{ID: id, OrgID: orgID, ProjectID: projectID, Name: "production", Kind: projects.KindProduction}, nil
	}
	return nil, projects.ErrEnvironmentNotFound
}

func (memEnvironments) ListProjects(context.Context, string) ([]*projects.Project, error) {
	return nil, nil
}

func (memEnvironments) ListEnvironments(context.Context, string, string) ([]*projects.Environment, error) {
	return nil, nil
}

// A viewer can't deploy anywhere, a developer can't deploy to production,
// and another organization's environment isn't found. Each is refused
// before anything is read or written.
func TestViewerCannotDeploy(t *testing.T) {
	svc := models.NewService(models.Deps{Authz: roleAuthz{}, Projects: memEnvironments{}})
	settings, _ := realSettings()
	r := mounted(t, nil, svc, settings)
	body := `{"entry_id":"Qwen/Qwen2.5-7B-Instruct","target":{"source":"connection","connection_id":"conn_1"}}`
	for _, c := range []struct {
		p    *security.Principal
		path string
		want int
	}{
		{as(security.RoleViewer), envBase, http.StatusForbidden},
		{as(security.RoleViewer), envBase + "/llm/move", http.StatusForbidden},
		{as(security.RoleViewer), envBase + "/llm/retry", http.StatusForbidden},
		{as(security.RoleViewer), envBase + "/llm/move-to-pin", http.StatusForbidden},
		{as(security.RoleDeveloper), "/api/v1/projects/proj_1/environments/env_prod/ai/models", http.StatusForbidden},
		{&security.Principal{OrgID: "org_2", UserID: security.RoleAdmin}, envBase, http.StatusNotFound},
	} {
		out := do(t, r, call{http.MethodPost, c.path, body}, c.p)
		if out.Code != c.want {
			t.Errorf("%s as %s/%s: %d %s", c.path, c.p.OrgID, c.p.UserID, out.Code, out.Body.String())
		}
	}
	for _, c := range []call{{http.MethodDelete, envBase + "/llm", ""}, {http.MethodDelete, envBase + "/llm/waiting", ""}, {http.MethodPatch, envBase + "/llm", `{"revision":1}`}} {
		if out := do(t, r, c, as(security.RoleViewer)); out.Code != http.StatusForbidden {
			t.Errorf("viewer %s %s: %d %s", c.method, c.path, out.Code, out.Body.String())
		}
	}
}
