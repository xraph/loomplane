package agentsettings_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/agentsettings/agentsettingstest"
	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

const (
	org         = "o"
	secretValue = "sk-ant-abcdwxyz"
)

var (
	admin     = security.Principal{OrgID: org, UserID: security.RoleAdmin}
	developer = security.Principal{OrgID: org, UserID: security.RoleDeveloper}
	viewer    = security.Principal{OrgID: org, UserID: security.RoleViewer}
	outsider  = security.Principal{OrgID: org, UserID: "not-a-member"}
)

// roleAuthz allows what the principal's role allows in security.Catalog.
// The principal's UserID names its role.
type roleAuthz struct{}

func (roleAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, _ string) error {
	for _, role := range security.Catalog() {
		if role.Slug == p.UserID && slices.Contains(role.Permissions, perm) {
			return nil
		}
	}
	return fmt.Errorf("%w (%s)", security.ErrForbidden, perm.Name())
}

type memAudit struct {
	mu     sync.Mutex
	events []projects.AuditEvent
}

func (a *memAudit) Record(_ context.Context, e projects.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func (a *memAudit) take() []projects.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.events
	a.events = nil
	return out
}

type memProjects struct{}

func (memProjects) GetProject(_ context.Context, orgID, id string) (*projects.Project, error) {
	if orgID == org && id == "p1" {
		return &projects.Project{ID: id, OrgID: orgID}, nil
	}
	return nil, projects.ErrNotFound
}

// fakeGateway is nexus as far as the agent's settings use it.
type fakeGateway struct {
	mu      sync.Mutex
	calls   []string
	tenants map[string]string
	spend   map[string]string
	priced  map[string]bool // "provider/model"
	err     error
}

func newGateway() *fakeGateway {
	return &fakeGateway{tenants: map[string]string{}, spend: map[string]string{}, priced: map[string]bool{
		"anthropic/claude-sonnet-5-5": true, "anthropic/claude-haiku-5-5": true, "openai/gpt-x": true,
	}}
}

func (g *fakeGateway) log(format string, args ...any) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, fmt.Sprintf(format, args...))
	return g.err
}

func (g *fakeGateway) take() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.calls
	g.calls = nil
	return out
}

func (g *fakeGateway) EnsureTenant(_ context.Context, orgID, budgetUSD string) (string, error) {
	if err := g.log("ensure %s %s", orgID, budgetUSD); err != nil {
		return "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if id, ok := g.tenants[orgID]; ok {
		return id, nil
	}
	id := "tenant_" + orgID
	g.tenants[orgID] = id
	return id, nil
}

func (g *fakeGateway) SetBudget(_ context.Context, tenantID, budgetUSD string) error {
	return g.log("budget %s %s", tenantID, budgetUSD)
}

func (g *fakeGateway) SetActive(_ context.Context, tenantID string, active bool) error {
	return g.log("active %s %t", tenantID, active)
}

func (g *fakeGateway) MonthlySpend(_ context.Context, tenantID string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s, ok := g.spend[tenantID]; ok {
		return s, nil
	}
	return "0", nil
}

func (g *fakeGateway) Priced(_ context.Context, provider, model string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.priced[provider+"/"+model]
}

type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) add(s string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
	return nil
}

func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.calls
	r.calls = nil
	return out
}

// fakeSeeder records seeds. during, when set, runs once inside the next
// seed, for a write that lands while a turn-on is under way.
type fakeSeeder struct {
	recorder
	during func()
}

func (s *fakeSeeder) Seed(_ context.Context, orgID string) error {
	if f := s.during; f != nil {
		s.during = nil
		f()
	}
	return s.add("seed " + orgID)
}

// fakeRoles records role changes. assignErr, when set, fails assigning.
type fakeRoles struct {
	recorder
	assignErr error
}

func (r *fakeRoles) AssignAgent(_ context.Context, orgID string) error {
	_ = r.add("assign " + orgID)
	return r.assignErr
}
func (r *fakeRoles) RevokeAgent(_ context.Context, orgID string) error {
	return r.add("revoke " + orgID)
}

type fakeCanceller struct{ recorder }

func (c *fakeCanceller) CancelAll(_ context.Context, orgID string) error {
	return c.add("cancel " + orgID)
}

// harness is the agent's settings over the real workspace AI settings,
// both in memory.
type harness struct {
	store  *agentsettingstest.Store
	keys   *agentsettingstest.Keys
	ai     *aisettings.Service
	svc    *agentsettings.Service
	gw     *fakeGateway
	seed   *fakeSeeder
	roles  *fakeRoles
	cancel *fakeCanceller
	audit  *memAudit
	deps   agentsettings.Deps
	// hosts is what the fake resolver answers, by host name.
	hosts map[string][]netip.Addr
}

func serverConfig() config.AIConfig {
	return config.AIConfig{
		Enabled: true, Provider: config.AIProviderAnthropic, APIKey: "sk-server-key-1234", Model: config.DefaultAIModel,
	}
}

func newHarness(t *testing.T, change ...func(*agentsettings.Deps)) *harness {
	t.Helper()
	h := &harness{
		store: agentsettingstest.NewStore(), keys: agentsettingstest.NewKeys(), gw: newGateway(),
		seed: &fakeSeeder{}, roles: &fakeRoles{}, cancel: &fakeCanceller{}, audit: &memAudit{},
		hosts: map[string][]netip.Addr{
			"llm.example.com":      {netip.MustParseAddr("93.184.215.14")},
			"api.anthropic.com":    {netip.MustParseAddr("160.79.104.10")},
			"sneaky.example.com":   {netip.MustParseAddr("93.184.215.15"), netip.MustParseAddr("10.1.2.3")},
			"mgmt.example.com":     {netip.MustParseAddr("203.0.113.9")},
			"localhost":            {netip.MustParseAddr("127.0.0.1")},
			"llm.corp.example.com": {netip.MustParseAddr("192.168.1.20")},
		},
	}
	h.ai = aisettings.NewService(aisettings.Deps{
		Store: h.store, Keys: h.keys, Authz: roleAuthz{}, Audit: h.audit, Projects: memProjects{},
	})
	d := agentsettings.Deps{
		Store: h.store, AI: h.ai, Gateway: h.gw, Seeder: h.seed, Roles: h.roles, Authz: roleAuthz{}, Audit: h.audit,
		Config: serverConfig(), Environment: config.EnvProduction,
		Denied: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")},
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			if addrs, ok := h.hosts[host]; ok {
				return addrs, nil
			}
			return nil, errors.New("no such host")
		},
		Now: func() time.Time { return time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) },
	}
	for _, c := range change {
		c(&d)
	}
	h.deps = d
	h.svc = agentsettings.NewService(d)
	h.svc.BindCanceller(h.cancel)
	return h
}

// restart builds the service again over the same stores, with change
// made to what it's built with.
func (h *harness) restart(change func(*agentsettings.Deps)) {
	change(&h.deps)
	h.svc = agentsettings.NewService(h.deps)
	h.svc.BindCanceller(h.cancel)
}

func ptr[T any](v T) *T { return &v }

// rev is the workspace settings' revision now.
func (h *harness) rev(t *testing.T) int64 {
	t.Helper()
	st, err := h.store.Get(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	return st.Revision
}

// budget gives the workspace a budget of 100 USD and the agent agent of
// it.
func (h *harness) budget(t *testing.T, agent string) {
	t.Helper()
	workspace := "100"
	if _, err := h.ai.Update(context.Background(), admin, aisettings.UpdateInput{MonthlyBudgetUSD: &workspace, Revision: h.rev(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SetBudget(context.Background(), admin, agent); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) update(t *testing.T, in agentsettings.AgentUpdateInput) (*agentsettings.AgentView, error) {
	t.Helper()
	in.Revision = h.rev(t)
	return h.svc.Update(context.Background(), admin, in)
}

func (h *harness) mustUpdate(t *testing.T, in agentsettings.AgentUpdateInput) *agentsettings.AgentView {
	t.Helper()
	v, err := h.update(t, in)
	if err != nil {
		t.Fatalf("update %+v: %v", in, err)
	}
	return v
}

// putKey adds a provider key through the workspace AI settings, which
// take any type.
func (h *harness) putKey(t *testing.T, name string, in aisettings.ProviderKeyInput) {
	t.Helper()
	if _, err := h.ai.PutProviderKey(context.Background(), admin, name, in); err != nil {
		t.Fatalf("put %s: %v", name, err)
	}
}

func anthropicKey() aisettings.ProviderKeyInput {
	return aisettings.ProviderKeyInput{Type: "anthropic", Fields: map[string]string{"api_key": secretValue}}
}

func openCompatKey(baseURL string) aisettings.ProviderKeyInput {
	return aisettings.ProviderKeyInput{
		Type:   "opencompat",
		Fields: map[string]string{"base_url": baseURL, "api_key": "oc-secret-9876"},
		Models: &[]aisettings.OpenCompatModel{{ID: "llama", Task: "chat", InputPerMillion: "0.10", OutputPerMillion: "0.20"}},
	}
}

func wantInvalid(t *testing.T, err error, mention string) {
	t.Helper()
	if !errors.Is(err, agentsettings.ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid mentioning %q", err, mention)
	}
	if !strings.Contains(err.Error(), mention) {
		t.Fatalf("%q doesn't mention %q", err.Error(), mention)
	}
}
