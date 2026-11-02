package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/agentsettings/agentsettingstest"
	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// keyValue is a provider key's secret, which no answer, error or audit
// entry may carry.
const keyValue = "sk-ant-route-secret-wxyz"

// auditLog keeps what was audited.
type auditLog struct {
	mu     sync.Mutex
	events []projects.AuditEvent
}

func (a *auditLog) Record(_ context.Context, e projects.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func (a *auditLog) take() []projects.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.events
	a.events = nil
	return out
}

// agentGateway is nexus, as far as turning the agent on goes.
type agentGateway struct{}

func (agentGateway) EnsureTenant(_ context.Context, orgID, _ string) (string, error) {
	return "tenant_" + orgID, nil
}
func (agentGateway) SetBudget(context.Context, string, string) error      { return nil }
func (agentGateway) SetActive(context.Context, string, bool) error        { return nil }
func (agentGateway) MonthlySpend(context.Context, string) (string, error) { return "0", nil }
func (agentGateway) Priced(_ context.Context, provider, _ string) bool {
	return provider == "anthropic"
}

type noRoles struct{}

func (noRoles) AssignAgent(context.Context, string) error { return nil }
func (noRoles) RevokeAgent(context.Context, string) error { return nil }

// settingsRig is the real AI settings and agent settings services over
// memory, with permissions from the role catalog, mounted.
type settingsRig struct {
	r     forge.Router
	store *agentsettingstest.Store
	keys  *agentsettingstest.Keys
	agent *agentsettings.Service
	audit *auditLog
}

func newSettingsRig(t *testing.T, guard identity.Guard) *settingsRig {
	t.Helper()
	rig := &settingsRig{store: agentsettingstest.NewStore(), keys: agentsettingstest.NewKeys(), audit: &auditLog{}}
	ai := aisettings.NewService(aisettings.Deps{
		Store: rig.store, Keys: rig.keys, Authz: roleAuthz{}, Audit: rig.audit, Projects: noProjects{},
	})
	rig.agent = agentsettings.NewService(agentsettings.Deps{
		Store: rig.store, AI: ai, Roles: noRoles{}, Authz: roleAuthz{}, Audit: rig.audit,
		Config:      config.AIConfig{Enabled: true, Provider: "anthropic", APIKey: "sk-server-1234", Model: config.DefaultAIModel},
		Environment: config.EnvProduction,
	})
	rig.r = forge.NewRouter()
	if err := mountModels(rig.r, guard, &fakeModels{}, ai); err != nil {
		t.Fatal(err)
	}
	if err := mountSettings(rig.r, guard, ai, rig.agent); err != nil {
		t.Fatal(err)
	}
	return rig
}

// rev is the workspace settings' revision now.
func (rig *settingsRig) rev(t *testing.T) int64 {
	t.Helper()
	st, err := rig.store.Get(context.Background(), "org_1")
	if err != nil {
		t.Fatal(err)
	}
	return st.Revision
}

var settingWrites = []call{
	{http.MethodPut, "/api/v1/ai/settings/budget", `{"monthly_budget_usd":"100","revision":0}`},
	{http.MethodPut, "/api/v1/ai/settings/provider-keys/claude", `{"type":"anthropic","fields":{"api_key":"` + keyValue + `"}}`},
	{http.MethodDelete, "/api/v1/ai/settings/provider-keys/claude", ""},
	{http.MethodPut, "/api/v1/ai/settings/agent/budget", `{"monthly_budget_usd":"25"}`},
	{http.MethodPatch, "/api/v1/ai/settings/agent", `{"model":"claude-sonnet-5-5","revision":0}`},
}

func TestSettingsRoutesRegistered(t *testing.T) {
	guarded := 0
	guard := identity.Guard{func(forge.Handler) forge.Handler {
		return func(forge.Context) error {
			guarded++
			return forge.NewHTTPError(http.StatusTeapot, "the guard")
		}
	}}
	rig := newSettingsRig(t, guard)
	want := map[string]string{
		"aiSettings.setBudget":         "PUT /api/v1/ai/settings/budget",
		"aiSettings.putProviderKey":    "PUT /api/v1/ai/settings/provider-keys/:name",
		"aiSettings.deleteProviderKey": "DELETE /api/v1/ai/settings/provider-keys/:name",
		"aiSettings.getAgent":          "GET /api/v1/ai/settings/agent",
		"aiSettings.updateAgent":       "PATCH /api/v1/ai/settings/agent",
		"aiSettings.setAgentBudget":    "PUT /api/v1/ai/settings/agent/budget",
	}
	got := map[string]string{}
	for _, info := range rig.r.Routes() {
		if _, ours := want[info.Name]; !ours {
			continue
		}
		if _, dup := got[info.Name]; dup {
			t.Errorf("%s registered twice", info.Name)
		}
		got[info.Name] = info.Method + " " + info.Path
		if !slices.Contains(info.Tags, "AI") {
			t.Errorf("%s has tags %v", info.Name, info.Tags)
		}
	}
	for name, route := range want {
		if got[name] != route {
			t.Errorf("%s is %q, want %q", name, got[name], route)
		}
	}
	all := append(slices.Clone(settingWrites), call{http.MethodGet, "/api/v1/ai/settings/agent", ""})
	for _, c := range all {
		if out := do(t, rig.r, c, as(security.RoleAdmin)); out.Code != http.StatusTeapot {
			t.Errorf("%s %s: %d past the guard", c.method, c.path, out.Code)
		}
	}
	if guarded != len(all) {
		t.Errorf("guarded %d of %d", guarded, len(all))
	}
}

// Every write needs ai:manage, and a refused one changes and audits
// nothing. None works without a principal.
func TestSettingsWritesNeedAIManage(t *testing.T) {
	rig := newSettingsRig(t, nil)
	for _, c := range append(slices.Clone(settingWrites), call{http.MethodGet, "/api/v1/ai/settings/agent", ""}) {
		for _, role := range []string{security.RoleViewer, security.RoleDeveloper} {
			if out := do(t, rig.r, c, as(role)); out.Code != http.StatusForbidden {
				t.Errorf("%s %s as %s: %d %s", c.method, c.path, role, out.Code, out.Body.String())
			}
		}
		if out := do(t, rig.r, c, nil); out.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no principal: %d", c.method, c.path, out.Code)
		}
	}
	if events := rig.audit.take(); len(events) != 0 {
		t.Errorf("refused writes audited: %+v", events)
	}
	if rig.rev(t) != 0 {
		t.Errorf("refused writes changed the settings")
	}
	keys, _ := rig.store.ProviderKeys(context.Background(), "org_1")
	if len(keys) != 0 {
		t.Errorf("refused writes added keys: %+v", keys)
	}
}

// A provider key's secret goes in and never comes out: not in an answer,
// an error or the audit, which names the key's ID and hint.
func TestProviderKeySecretIsWriteOnly(t *testing.T) {
	rig := newSettingsRig(t, nil)
	admin := as(security.RoleAdmin)
	put := do(t, rig.r, settingWrites[1], admin)
	if put.Code != http.StatusOK {
		t.Fatalf("put: %d %s", put.Code, put.Body.String())
	}
	var key aisettings.ProviderKey
	if err := json.Unmarshal(put.Body.Bytes(), &key); err != nil || key.KeyHint != "wxyz" || key.Type != "anthropic" || key.ID == "" {
		t.Errorf("put answered %s: %v", put.Body.String(), err)
	}
	get := do(t, rig.r, call{http.MethodGet, "/api/v1/ai/settings", ""}, admin)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"key_hint":"wxyz"`) {
		t.Errorf("get: %d %s", get.Code, get.Body.String())
	}

	// Refusals that held the secret: too short, a broken body, an unknown
	// field, and a type the agent's settings don't write.
	refusals := map[string]call{
		"short":  {http.MethodPut, "/api/v1/ai/settings/provider-keys/other", `{"type":"anthropic","fields":{"api_key":"wxyz"}}`},
		"broken": {http.MethodPut, "/api/v1/ai/settings/provider-keys/other", `{"type":"anthropic","fields":{"api_key":"` + keyValue + `"`},
		"field":  {http.MethodPut, "/api/v1/ai/settings/provider-keys/other", `{"type":"anthropic","fields":{"secret_access_key":"` + keyValue + `"}}`},
		"type":   {http.MethodPut, "/api/v1/ai/settings/provider-keys/other", `{"type":"mistral","fields":{"api_key":"` + keyValue + `"}}`},
	}
	answers := map[string]string{"put": put.Body.String(), "get": get.Body.String()}
	for name, c := range refusals {
		out := do(t, rig.r, c, admin)
		if out.Code < 400 || out.Code >= 500 {
			t.Errorf("%s: %d %s", name, out.Code, out.Body.String())
		}
		answers[name] = out.Body.String() + fmt.Sprint(out.Header())
	}
	if !strings.Contains(answers["type"], "Anthropic, OpenAI or OpenAI-compatible") {
		t.Errorf("type refusal: %s", answers["type"])
	}
	for name, body := range answers {
		if strings.Contains(body, keyValue) || strings.Contains(body, "route-secret") {
			t.Errorf("%s echoes the secret: %s", name, body)
		}
	}

	del := do(t, rig.r, settingWrites[2], admin)
	if del.Code != http.StatusNoContent || del.Body.Len() != 0 {
		t.Errorf("delete: %d %s", del.Code, del.Body.String())
	}
	events := rig.audit.take()
	actions := make([]string, 0, len(events))
	for _, e := range events {
		actions = append(actions, e.Action)
		if strings.Contains(fmt.Sprint(e.Attributes), keyValue) {
			t.Errorf("%s audits the secret: %v", e.Action, e.Attributes)
		}
		if e.Attributes["provider_key_id"] != key.ID || e.Attributes["key_hint"] != "wxyz" {
			t.Errorf("%s attributes %v, want id %s and hint wxyz", e.Action, e.Attributes, key.ID)
		}
	}
	if !slices.Equal(actions, []string{"ai.provider_key.put", "ai.provider_key.delete"}) {
		t.Errorf("audited %v", actions)
	}
}

// The workspace budget is written with the revision the page read.
func TestWorkspaceBudgetRoute(t *testing.T) {
	rig := newSettingsRig(t, nil)
	admin := as(security.RoleAdmin)
	out := do(t, rig.r, settingWrites[0], admin)
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"monthly_budget_usd":"100.00"`) {
		t.Fatalf("budget: %d %s", out.Code, out.Body.String())
	}
	// The same revision again is stale.
	out = do(t, rig.r, call{http.MethodPut, "/api/v1/ai/settings/budget", `{"monthly_budget_usd":"200","revision":0}`}, admin)
	if out.Code != http.StatusConflict || !strings.Contains(out.Body.String(), `"code":"stale"`) {
		t.Errorf("stale: %d %s", out.Code, out.Body.String())
	}
	out = do(t, rig.r, call{http.MethodPut, "/api/v1/ai/settings/budget", `{"monthly_budget_usd":"lots","revision":1}`}, admin)
	if out.Code != http.StatusUnprocessableEntity {
		t.Errorf("not an amount: %d %s", out.Code, out.Body.String())
	}
	events := rig.audit.take()
	if len(events) != 1 || events[0].Action != "ai.settings.update" {
		t.Errorf("audit %+v", events)
	}
}

func TestAgentSettingsRoutes(t *testing.T) {
	rig := newSettingsRig(t, nil)
	admin := as(security.RoleAdmin)
	for _, c := range []call{settingWrites[0], settingWrites[1], settingWrites[3]} {
		if out := do(t, rig.r, c, admin); out.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", c.method, c.path, out.Code, out.Body.String())
		}
	}
	get := do(t, rig.r, call{http.MethodGet, "/api/v1/ai/settings/agent", ""}, admin)
	var v agentsettings.AgentView
	if err := json.Unmarshal(get.Body.Bytes(), &v); err != nil || get.Code != http.StatusOK || v.Budget.LimitUSD != "25.00" || v.Enabled {
		t.Fatalf("get: %d %s", get.Code, get.Body.String())
	}

	// The agent on the workspace key, with no nexus on this server yet.
	patch := func(body string) *httpResponse {
		out := do(t, rig.r, call{http.MethodPatch, "/api/v1/ai/settings/agent", body}, admin)
		return &httpResponse{out.Code, out.Body.String()}
	}
	out := patch(fmt.Sprintf(`{"provider_key":"claude","revision":%d}`, rig.rev(t)))
	if out.code != http.StatusOK || !strings.Contains(out.body, `"provider_key":"claude"`) || !strings.Contains(out.body, `"hint":"wxyz"`) {
		t.Fatalf("choose a key: %d %s", out.code, out.body)
	}
	out = patch(fmt.Sprintf(`{"enabled":true,"revision":%d}`, rig.rev(t)))
	if out.code != http.StatusUnprocessableEntity || !strings.Contains(out.body, "can't run the agent yet") {
		t.Errorf("on without nexus: %d %s", out.code, out.body)
	}
	rig.agent.BindGateway(agentGateway{}, nil)
	out = patch(fmt.Sprintf(`{"enabled":true,"revision":%d}`, rig.rev(t)))
	if out.code != http.StatusOK || !strings.Contains(out.body, `"enabled":true`) || !strings.Contains(out.body, `"nexus_tenant_id":"tenant_org_1"`) {
		t.Errorf("on: %d %s", out.code, out.body)
	}
	out = patch(`{"enabled":false,"revision":1}`)
	if out.code != http.StatusConflict {
		t.Errorf("stale: %d %s", out.code, out.body)
	}
	for name, body := range map[string]string{"put": out.body, "get": get.Body.String()} {
		if strings.Contains(body, keyValue) || strings.Contains(body, "sk-server-1234") {
			t.Errorf("%s carries a key: %s", name, body)
		}
	}

	// The agent's key can't go, and its budget can't go to zero while it's on.
	del := do(t, rig.r, settingWrites[2], admin)
	if del.Code != http.StatusUnprocessableEntity || !strings.Contains(del.Body.String(), `"code":"in_use"`) {
		t.Errorf("delete the agent's key: %d %s", del.Code, del.Body.String())
	}
	zero := do(t, rig.r, call{http.MethodPut, "/api/v1/ai/settings/agent/budget", `{"monthly_budget_usd":"0"}`}, admin)
	if zero.Code != http.StatusUnprocessableEntity || !strings.Contains(zero.Body.String(), "turn the agent off") {
		t.Errorf("zero budget while on: %d %s", zero.Code, zero.Body.String())
	}
	over := do(t, rig.r, call{http.MethodPut, "/api/v1/ai/settings/agent/budget", `{"monthly_budget_usd":"500"}`}, admin)
	if over.Code != http.StatusUnprocessableEntity {
		t.Errorf("over the workspace budget: %d %s", over.Code, over.Body.String())
	}

	events := rig.audit.take()
	actions := make([]string, 0, len(events))
	for _, e := range events {
		actions = append(actions, e.Action)
	}
	want := []string{"ai.settings.update", "ai.provider_key.put", "ai.budget.allocate", "agent.settings.update", "agent.settings.update"}
	if !slices.Equal(actions, want) {
		t.Errorf("audited %v, want %v", actions, want)
	}
}

type httpResponse struct {
	code int
	body string
}
