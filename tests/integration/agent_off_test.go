//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	cortexagent "github.com/xraph/cortex/agent"
	cortexengine "github.com/xraph/cortex/engine"
	cortexid "github.com/xraph/cortex/id"
	"github.com/xraph/forge"
	"github.com/xraph/nexus"
	"github.com/xraph/nexus/usage"
	shieldengine "github.com/xraph/shield/engine"
	"github.com/xraph/shield/scan"

	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/security"
)

// With AI off there's no agent: no route, no library in the container,
// nothing in the published spec, and the dashboard is told AI is off.
func TestAgentOff_noRoutesNoLibraries(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Dashboard.Dir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	for _, path := range []string{"/api/v1/agent/status", "/api/v1/ai/settings/agent"} {
		c.must(http.StatusNotFound, http.MethodGet, path, nil, nil)
	}
	c.must(http.StatusNotFound, http.MethodPut, "/api/v1/projects/nope/autonomy", map[string]bool{"autonomous_promotion": true}, nil)

	var spec struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	c.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &spec)
	for path := range spec.Paths {
		if strings.HasPrefix(path, "/api/v1/agent") || strings.HasSuffix(path, "/autonomy") {
			t.Errorf("AI off, and the spec has %s", path)
		}
	}
	container := r.app.(forge.App).Container()
	if forge.HasType[*nexus.Gateway](container) || forge.HasType[*cortexengine.Engine](container) || forge.HasType[*shieldengine.Engine](container) {
		t.Error("AI off, and the container has an agent library")
	}
	if forge.HasType[*agentscope.Binder](container) {
		t.Error("AI off, and the container has the agent's binder")
	}
	var pub struct {
		Features struct {
			AI bool `json:"ai"`
		} `json:"features"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/public-config", nil, &pub)
	if pub.Features.AI {
		t.Error("AI off, and public-config says it's on")
	}
}

// With AI off the conversation and task routes aren't mounted, and
// neither spec has a path or channel under /api/v1/agent.
func TestAgentOff_noChatRoutes(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Dashboard.Dir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	c.must(http.StatusNotFound, http.MethodGet, "/api/v1/agent/conversations", nil, nil)
	c.must(http.StatusNotFound, http.MethodPost, "/api/v1/agent/conversations", map[string]string{"message": "hi"}, nil)
	c.must(http.StatusNotFound, http.MethodGet, "/api/v1/agent/tasks/x/stream", nil, nil)
	c.must(http.StatusNotFound, http.MethodGet, "/api/v1/agent/tasks/x", nil, nil)

	var spec struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	c.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &spec)
	for path := range spec.Paths {
		if strings.HasPrefix(path, "/api/v1/agent") {
			t.Errorf("AI off, and the OpenAPI spec has %s", path)
		}
	}
	var async struct {
		Channels map[string]struct {
			Address string `json:"address"`
		} `json:"channels"`
	}
	c.must(http.StatusOK, http.MethodGet, "/asyncapi.json", nil, &async)
	if len(async.Channels) == 0 {
		t.Fatal("the AsyncAPI spec has no channels at all")
	}
	for name, ch := range async.Channels {
		if strings.HasPrefix(ch.Address, "/api/v1/agent") {
			t.Errorf("AI off, and the AsyncAPI spec has %s (%s)", ch.Address, name)
		}
	}
}

// agentOrg is a fresh workspace on a server with AI on: its owner, its
// ID, and the app.
type agentOrg struct {
	r     *running
	cfg   config.Config
	owner *client
	orgID string
}

// startAgentOrg starts the app with ai and makes Ada the owner of a new
// workspace.
func startAgentOrg(t *testing.T, ai config.AIConfig) *agentOrg {
	t.Helper()
	cfg := config.Defaults()
	cfg.AI = ai
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Dashboard.Dir = t.TempDir()
	r := start(t, cfg)
	a := &agentOrg{r: r, cfg: cfg, owner: &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}}
	account(t, a.owner, "ada@example.com")
	var org struct {
		ID string `json:"id"`
	}
	a.owner.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Engines", "slug": "engines"}, &org)
	a.owner.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	a.orgID = org.ID
	return a
}

// agentView is the part of the agent's settings the tests read.
type agentView struct {
	Enabled       bool   `json:"enabled"`
	ProviderKey   string `json:"provider_key"`
	Model         string `json:"model"`
	NexusTenantID string `json:"nexus_tenant_id"`
	Revision      int64  `json:"revision"`
	Available     bool   `json:"available"`
	Budget        struct {
		LimitUSD string `json:"limit_usd"`
	} `json:"budget"`
}

func (a *agentOrg) agent() agentView {
	var v agentView
	a.owner.must(http.StatusOK, http.MethodGet, "/api/v1/ai/settings/agent", nil, &v)
	return v
}

// budget gives the workspace 100 USD a month and the agent agentUSD of it.
func (a *agentOrg) budget(agentUSD string) {
	var ws struct {
		Revision int64 `json:"revision"`
	}
	a.owner.must(http.StatusOK, http.MethodGet, "/api/v1/ai/settings", nil, &ws)
	a.owner.must(http.StatusOK, http.MethodPut, "/api/v1/ai/settings/budget", map[string]any{"monthly_budget_usd": "100", "revision": ws.Revision}, nil)
	a.owner.must(http.StatusOK, http.MethodPut, "/api/v1/ai/settings/agent/budget", map[string]any{"monthly_budget_usd": agentUSD, "revision": a.agent().Revision}, nil)
}

// enable turns the agent on with the change given, at the current
// revision.
func (a *agentOrg) enable(change map[string]any) agentView {
	body := map[string]any{"enabled": true, "revision": a.agent().Revision}
	for k, v := range change {
		body[k] = v
	}
	var v agentView
	a.owner.must(http.StatusOK, http.MethodPatch, "/api/v1/ai/settings/agent", body, &v)
	return v
}

func (a *agentOrg) container() forge.Container { return a.r.app.(forge.App).Container() }

func inject[T any](t *testing.T, c forge.Container) T {
	t.Helper()
	v, err := forge.InjectType[T](c)
	if err != nil {
		t.Fatalf("resolve %T: %v", v, err)
	}
	return v
}

// run runs one turn of a test agent through cortex at the workspace's
// scope, bound the way the agent's services bind every run.
func (a *agentOrg) run(t *testing.T, name, model string) {
	t.Helper()
	c := a.container()
	eng := inject[*cortexengine.Engine](t, c)
	binder := inject[*agentscope.Binder](t, c)
	scope := agentscope.Scope{OrgID: a.orgID}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ctx, err := binder.Bind(ctx, security.AgentPrincipal(a.orgID, nil), scope)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err = eng.CreateAgent(ctx, &cortexagent.Config{
		ID: cortexid.NewAgentID(), Name: name, Scope: scope.Cortex(), Model: model, Enabled: true, MaxSteps: 4,
		SystemPrompt: "You are Loomplane's test agent.", Guardrails: agentsafety.Guardrails(),
	}); err != nil {
		t.Fatalf("create the agent: %v", err)
	}
	r, err := eng.RunAgent(ctx, name, "How are my deployments?", nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r.Error != "" {
		t.Fatalf("the run failed: %s", r.Error)
	}
}

// usageFor is what nexus recorded for tenant.
func usageFor(t *testing.T, gw *nexus.Gateway, tenant string) []*usage.Record {
	t.Helper()
	ctx := context.Background()
	if err := gw.FlushUsage(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := gw.Usage().Query(ctx, &usage.QueryOptions{TenantID: tenant, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

// With AI on, the owner turns the agent on from the settings, once nexus
// is bound: the gateway answers, the tenant is made. Members see the
// status; only an admin changes the settings. The status and a project's
// autonomy answer while the agent is still off.
func TestAgentOn_settingsRoundTrip(t *testing.T) {
	fake := agenttest.Anthropic(t)
	a := startAgentOrg(t, fake.AIConfig())
	defer a.r.stop()

	v := a.agent()
	if v.Enabled || v.ProviderKey != "" || !v.Available {
		t.Fatalf("a new workspace: %+v", v)
	}
	viewer := joinAs(t, a, "grace@example.com", "viewer")
	developer := joinAs(t, a, "dev@example.com", "developer")

	// Off: the status answers, and an admin sets a project's autonomy.
	var st agentsettings.Status
	viewer.must(http.StatusOK, http.MethodGet, "/api/v1/agent/status", nil, &st)
	if !st.Server || st.Enabled {
		t.Fatalf("status while off: %+v", st)
	}
	var proj struct {
		ID string `json:"id"`
	}
	a.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono"}, &proj)
	var after struct {
		AutonomousPromotion   bool  `json:"autonomous_promotion"`
		MinStagingSoakSeconds int64 `json:"min_staging_soak_seconds"`
	}
	a.owner.must(http.StatusOK, http.MethodPut, "/api/v1/projects/"+proj.ID+"/autonomy", map[string]any{"autonomous_promotion": true, "min_staging_soak_seconds": 1800}, &after)
	if !after.AutonomousPromotion || after.MinStagingSoakSeconds != 1800 {
		t.Fatalf("autonomy %+v", after)
	}
	developer.must(http.StatusForbidden, http.MethodPut, "/api/v1/projects/"+proj.ID+"/autonomy", map[string]any{"autonomous_promotion": false}, nil)

	// On: no more "this server can't run the agent yet".
	a.budget("10")
	on := a.enable(nil)
	if !on.Enabled || on.NexusTenantID == "" || on.Budget.LimitUSD != "10.00" {
		t.Fatalf("on: %+v", on)
	}
	gw := inject[*nexus.Gateway](t, a.container())
	tn, err := gw.Tenants().Get(context.Background(), on.NexusTenantID)
	if err != nil || tn.Metadata["org_id"] != a.orgID || tn.Quota.MonthlyBudgetUSD.String() != "10" {
		t.Fatalf("tenant %+v %v", tn, err)
	}
	developer.must(http.StatusForbidden, http.MethodPatch, "/api/v1/ai/settings/agent", map[string]any{"enabled": false, "revision": on.Revision}, nil)
	viewer.must(http.StatusOK, http.MethodGet, "/api/v1/agent/status", nil, &st)
	if !st.Server || !st.Enabled {
		t.Fatalf("status while on: %+v", st)
	}
	if n := len(fake.Requests()); n != 0 {
		t.Fatalf("turning the agent on called the model %d times", n)
	}

	// The committed spec, which the dashboard's client is generated from,
	// carries the agent's routes and every schema they reach as served.
	var raw json.RawMessage
	a.owner.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &raw)
	committedRaw, err := os.ReadFile("../../apps/dashboard/specs/loomplane.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	served, committed := openAPIDoc(t, raw), openAPIDoc(t, committedRaw)
	todo := map[string]bool{}
	for _, path := range []string{"/api/v1/agent/status", "/api/v1/projects/{id}/autonomy", "/api/v1/ai/settings/agent/budget"} {
		item, ok := served.Paths[path]
		if !ok {
			t.Errorf("the app doesn't serve %s", path)
			continue
		}
		if !jsonEqual(item, committed.Paths[path]) {
			t.Errorf("the committed spec's %s differs from the served one", path)
		}
		schemaRefs(item, todo)
	}
	seen := map[string]bool{}
	for len(todo) > 0 {
		for name := range todo {
			delete(todo, name)
			if seen[name] {
				continue
			}
			seen[name] = true
			if !jsonEqual(served.Components.Schemas[name], committed.Components.Schemas[name]) {
				t.Errorf("the committed spec's schema %s differs from the served one", name)
			}
			schemaRefs(served.Components.Schemas[name], todo)
		}
	}
}

// joinAs invites email to the workspace with role and signs them in.
func joinAs(t *testing.T, a *agentOrg, email, role string) *client {
	t.Helper()
	var created struct {
		AcceptPath string `json:"accept_path"`
	}
	a.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations", map[string]string{"email": email, "role": role}, &created)
	_, token, _ := strings.Cut(created.AcceptPath, "token=")
	c := &client{t: t, base: a.r.srv.URL, key: a.cfg.Identity.PublishableKey}
	account(t, c, email)
	if code := c.do(http.MethodPost, "/authsome/v1/orgs/invitations/accept", map[string]string{"token": token}, nil); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("accept: %d", code)
	}
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": a.orgID}, nil)
	return c
}

// openAICompatible is an OpenAI-compatible server that answers every chat
// request with one line, plain or streamed, and keeps the key each came
// with.
type openAICompatible struct {
	srv *httptest.Server

	mu   sync.Mutex
	keys []string
}

func newOpenAICompatible(t *testing.T) *openAICompatible {
	t.Helper()
	o := &openAICompatible{}
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.Unmarshal(raw, &req)
		o.mu.Lock()
		o.keys = append(o.keys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		o.mu.Unlock()
		usage := map[string]int{"prompt_tokens": 40, "completion_tokens": 8, "total_tokens": 48}
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "chatcmpl-1", "object": "chat.completion", "created": time.Now().Unix(), "model": req.Model,
				"choices": []any{map[string]any{"index": 0, "finish_reason": "stop",
					"message": map[string]any{"role": "assistant", "content": "All your deployments are healthy."}}},
				"usage": usage,
			})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []map[string]any{
			{"id": "chatcmpl-1", "object": "chat.completion.chunk", "model": req.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "All your deployments are healthy."}}}},
			{"id": "chatcmpl-1", "object": "chat.completion.chunk", "model": req.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}},
			{"id": "chatcmpl-1", "object": "chat.completion.chunk", "model": req.Model, "choices": []any{}, "usage": usage},
		} {
			b, _ := json.Marshal(chunk)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(o.srv.Close)
	return o
}

func (o *openAICompatible) seen() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.keys...)
}

// A workspace on its own OpenAI-compatible key runs there, with its key,
// while the server stays on Anthropic: the run goes to the workspace's
// server, the Anthropic fake gets nothing, and nexus bills the workspace's
// tenant on the openai-compatible provider.
func TestOpenAICompatibleWorkspaceRoutesToItsProvider(t *testing.T) {
	fake := agenttest.Anthropic(t)
	compat := newOpenAICompatible(t)
	ai := fake.AIConfig()
	ai.Prices = []config.AIPrice{{Provider: config.AIProviderOpenAICompatible, Model: "llama-test", InputPerMillion: "0.10", OutputPerMillion: "0.20"}}
	a := startAgentOrg(t, ai)
	defer a.r.stop()

	a.owner.must(http.StatusOK, http.MethodPut, "/api/v1/ai/settings/provider-keys/local", map[string]any{
		"type":   "opencompat",
		"fields": map[string]string{"base_url": compat.srv.URL + "/v1", "api_key": "ws-key-0001"},
		"models": []map[string]string{{"id": "llama-test", "task": "chat", "input_per_million": "0.10", "output_per_million": "0.20"}},
	}, nil)
	a.budget("10")
	on := a.enable(map[string]any{"provider_key": "local", "model": "llama-test"})
	if !on.Enabled || on.ProviderKey != "local" {
		t.Fatalf("on: %+v", on)
	}

	a.run(t, "it-compat", "llama-test")

	if keys := compat.seen(); len(keys) == 0 || keys[0] != "ws-key-0001" {
		t.Fatalf("the OpenAI-compatible server saw keys %v", keys)
	}
	if n := len(fake.Requests()); n != 0 {
		t.Fatalf("the Anthropic fake got %d requests", n)
	}
	records := usageFor(t, inject[*nexus.Gateway](t, a.container()), on.NexusTenantID)
	if len(records) == 0 {
		t.Fatal("nexus recorded no usage for the workspace's tenant")
	}
	for _, rec := range records {
		if rec.Provider != config.AIProviderOpenAICompatible {
			t.Errorf("usage on provider %q", rec.Provider)
		}
	}
}

// A run goes through Loomplane's adapters, not cortex's defaults: the
// model call reaches the server's Anthropic (so nexus served it, not
// cortex's mock), nexus bills the workspace's tenant, and shield scans
// under the workspace and agentscope.App.
func TestCortexUsesLoomplaneAdapters(t *testing.T) {
	fake := agenttest.Anthropic(t, agenttest.Turn{Text: "All your deployments are healthy."})
	a := startAgentOrg(t, fake.AIConfig())
	defer a.r.stop()
	a.budget("10")
	on := a.enable(nil)

	a.run(t, "it-anthropic", config.DefaultAIModel)

	reqs := fake.Requests()
	if len(reqs) == 0 || reqs[0].Model != config.DefaultAIModel || !strings.Contains(reqs[0].System, "Loomplane's test agent") {
		t.Fatalf("the Anthropic fake saw %+v", reqs)
	}
	records := usageFor(t, inject[*nexus.Gateway](t, a.container()), on.NexusTenantID)
	if len(records) == 0 || records[0].Provider != config.AIProviderAnthropic {
		t.Fatalf("usage %+v", records)
	}
	shield := inject[*shieldengine.Engine](t, a.container())
	scans, err := shield.Store().ListScans(context.Background(), &scan.ListFilter{TenantID: a.orgID, AppID: agentscope.App, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(scans) == 0 {
		t.Fatal("shield made no scan under the workspace and the loomplane app")
	}
	for _, s := range scans {
		if s.TenantID != a.orgID || s.AppID != agentscope.App {
			t.Errorf("a scan under %s/%s", s.TenantID, s.AppID)
		}
	}
}

// With AI off nothing watches a workspace: no report route, no tick in
// Dispatch, and a failed production deployment leaves no watcher row.
func TestAgentOff_noWatcher(t *testing.T) {
	google := newFakeGoogle(t)
	cloud := newFakeCloud(google.srv.URL)
	cloud.failBuilds = true
	google.deploy = cloud
	const vercelToken = "vcl_0123456789abcdef"
	vc := newFakeVercelDeploys(t, vercelToken)
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.GoogleEndpoint = google.srv.URL
	cfg.Clouds.VercelEndpoint = vc.srv.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	firstAccount(t, c, "ada@example.com", "Correct-horse-battery-9")
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	for _, path := range []string{"/api/v1/agent/reports", "/api/v1/agent/reports/arep_1", "/api/v1/agent/watcher"} {
		c.must(http.StatusNotFound, http.MethodGet, path, nil, nil)
	}
	c.must(http.StatusNotFound, http.MethodPost, "/api/v1/agent/reports/arep_1/close", map[string]string{"reason": "x"}, nil)

	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	analyze(t, cfg, c, proj.ID)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "gcp", "name": "Prod", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, nil)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "vercel", "name": "Web", "credential": vercelToken}, nil)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": webAndAPIManifest}, nil)
	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if d := approveAndWait(t, c, plan); d.Status != "failed" {
		t.Fatalf("the deployment %s, want failed", d.Status)
	}

	if n := dbValue[int64](t, cfg.Database.URL, `SELECT count(*) FROM dispatch_cron_entries WHERE name = 'loomplane.agent.watch'`); n != 0 {
		t.Errorf("AI off, and Dispatch has %d watcher ticks scheduled", n)
	}
	if n := dbValue[int64](t, cfg.Database.URL, `SELECT count(*) FROM loomplane_agent_watchers`); n != 0 {
		t.Errorf("AI off, and %d workspaces have a watcher row", n)
	}
	if n := dbValue[int64](t, cfg.Database.URL, `SELECT count(*) FROM loomplane_agent_reports`); n != 0 {
		t.Errorf("AI off, and %d reports were opened", n)
	}
}

// With AI on the report routes answer once the workspace turns the agent
// on, and the tick is scheduled. The fixture has no GitHub App, so the
// watcher says issues aren't configured.
func TestAgentOn_reportRoutes(t *testing.T) {
	fake := agenttest.Anthropic(t)
	a := startAgentOrg(t, fake.AIConfig())
	defer a.r.stop()

	// Off in the workspace: the binder answers before any report is read.
	a.owner.must(http.StatusConflict, http.MethodGet, "/api/v1/agent/reports", nil, nil)

	a.budget("10")
	a.enable(nil)
	var list struct {
		Reports []json.RawMessage `json:"reports"`
	}
	a.owner.must(http.StatusOK, http.MethodGet, "/api/v1/agent/reports", nil, &list)
	if list.Reports == nil || len(list.Reports) != 0 {
		t.Fatalf("reports %+v", list)
	}
	a.owner.must(http.StatusNotFound, http.MethodGet, "/api/v1/agent/reports/arep_nope", nil, nil)
	a.owner.must(http.StatusUnprocessableEntity, http.MethodGet, "/api/v1/agent/reports?status=bogus", nil, nil)

	var st struct {
		Paused bool `json:"paused"`
		Issues *struct {
			Configured    bool              `json:"configured"`
			Installations []json.RawMessage `json:"installations"`
		} `json:"issues"`
	}
	a.owner.must(http.StatusOK, http.MethodGet, "/api/v1/agent/watcher", nil, &st)
	if st.Paused || st.Issues == nil || st.Issues.Configured || st.Issues.Installations == nil {
		t.Fatalf("watcher %+v", st)
	}
	if n := dbValue[int64](t, a.cfg.Database.URL, `SELECT count(*) FROM dispatch_cron_entries WHERE name = 'loomplane.agent.watch' AND queue = 'default'`); n != 1 {
		t.Errorf("AI on, and Dispatch has %d watcher ticks on the default queue", n)
	}
	if n := len(fake.Requests()); n != 0 {
		t.Fatalf("reading reports called the model %d times", n)
	}
}
