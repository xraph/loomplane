package aisettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultmem "github.com/xraph/vault/store/memory"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

const secretValue = "sk-ant-abcdwxyz"

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

var (
	admin     = security.Principal{OrgID: "org", UserID: security.RoleAdmin}
	developer = security.Principal{OrgID: "org", UserID: security.RoleDeveloper}
	viewer    = security.Principal{OrgID: "org", UserID: security.RoleViewer}
)

// memStore is a Store in memory with the Postgres store's rules: the
// revision check, SetBudget bumping the revision and refusing a budget
// below the allocations, and SetAllocation refusing to pass the budget.
type memStore struct {
	mu          sync.Mutex
	settings    map[string]Settings
	keys        map[string]map[string]ProviderKey
	allocations map[string]map[string]Allocation
	nextID      int
}

func newMemStore() *memStore {
	return &memStore{
		settings:    map[string]Settings{},
		keys:        map[string]map[string]ProviderKey{},
		allocations: map[string]map[string]Allocation{},
	}
}

func cents(t string) int64 {
	r, ok := new(big.Rat).SetString(t)
	if !ok {
		panic("bad amount " + t)
	}
	r.Mul(r, big.NewRat(100, 1))
	return r.Num().Int64() / r.Denom().Int64()
}

func usd(c int64) string { return fmt.Sprintf("%d.%02d", c/100, c%100) }

func (m *memStore) Settings(_ context.Context, orgID string) (*Settings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.settings[orgID]; ok {
		return &s, nil
	}
	return &Settings{OrgID: orgID, MonthlyBudgetUSD: "0.00", Embeddings: LocalEmbeddings()}, nil
}

func (m *memStore) SaveSettings(_ context.Context, s *Settings, expectRevision int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.settings[s.OrgID]
	if !ok {
		cur = Settings{OrgID: s.OrgID, MonthlyBudgetUSD: "0.00", Embeddings: LocalEmbeddings()}
	}
	if cur.Revision != expectRevision {
		return ErrStale
	}
	cur.Embeddings, cur.UpdatedBy, cur.Revision = s.Embeddings, s.UpdatedBy, cur.Revision+1
	m.settings[s.OrgID] = cur
	*s = cur
	return nil
}

func (m *memStore) ProviderKeys(_ context.Context, orgID string) ([]*ProviderKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*ProviderKey, 0, len(m.keys[orgID]))
	for _, name := range slices.Sorted(maps.Keys(m.keys[orgID])) {
		k := m.keys[orgID][name]
		out = append(out, &k)
	}
	return out, nil
}

func (m *memStore) ProviderKey(_ context.Context, orgID, name string) (*ProviderKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[orgID][name]
	if !ok {
		return nil, ErrNotFound
	}
	return &k, nil
}

func (m *memStore) SaveProviderKey(_ context.Context, k *ProviderKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.keys[k.OrgID] == nil {
		m.keys[k.OrgID] = map[string]ProviderKey{}
	}
	got := *k
	got.Fields = maps.Clone(k.Fields)
	got.Models = slices.Clone(k.Models)
	got.Projects = slices.Clone(k.Projects)
	if got.Projects == nil {
		got.Projects = []string{}
	}
	if old, ok := m.keys[k.OrgID][k.Name]; ok {
		got.ID, got.CreatedBy, got.Revision = old.ID, old.CreatedBy, old.Revision+1
	} else {
		if got.ID == "" {
			m.nextID++
			got.ID = fmt.Sprintf("aikey_%d", m.nextID)
		}
		got.Revision = 1
	}
	m.keys[k.OrgID][k.Name] = got
	*k = got
	return nil
}

func (m *memStore) DeleteProviderKey(_ context.Context, orgID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.keys[orgID][name]; !ok {
		return ErrNotFound
	}
	delete(m.keys[orgID], name)
	return nil
}

func (m *memStore) Allocations(_ context.Context, orgID string) ([]*Allocation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Allocation, 0, len(m.allocations[orgID]))
	for _, scope := range slices.Sorted(maps.Keys(m.allocations[orgID])) {
		a := m.allocations[orgID][scope]
		out = append(out, &a)
	}
	return out, nil
}

func (m *memStore) ensure(orgID string) Settings {
	s, ok := m.settings[orgID]
	if !ok {
		s = Settings{OrgID: orgID, MonthlyBudgetUSD: "0.00", Embeddings: LocalEmbeddings(), Revision: 1}
		m.settings[orgID] = s
	}
	return s
}

func (m *memStore) SetAllocation(_ context.Context, orgID string, a *Allocation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.ensure(orgID)
	var others int64
	for scope, o := range m.allocations[orgID] {
		if scope != a.Scope {
			others += cents(o.MonthlyBudgetUSD)
		}
	}
	left := cents(s.MonthlyBudgetUSD) - others
	if cents(a.MonthlyBudgetUSD) > left {
		return fmt.Errorf("%w: %s USD of it is left to allocate", ErrOverBudget, usd(max(left, 0)))
	}
	if m.allocations[orgID] == nil {
		m.allocations[orgID] = map[string]Allocation{}
	}
	got := *a
	got.MonthlyBudgetUSD = usd(cents(a.MonthlyBudgetUSD))
	m.allocations[orgID][a.Scope] = got
	*a = got
	return nil
}

func (m *memStore) DeleteAllocation(_ context.Context, orgID, scope string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.allocations[orgID], scope)
	return nil
}

func (m *memStore) SetBudget(_ context.Context, orgID, budgetUSD, by string) (*Settings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.ensure(orgID)
	var total int64
	for _, a := range m.allocations[orgID] {
		total += cents(a.MonthlyBudgetUSD)
	}
	if total > cents(budgetUSD) {
		return nil, fmt.Errorf("%w: allocations total %s USD", ErrUnderAllocated, usd(total))
	}
	s.MonthlyBudgetUSD, s.UpdatedBy, s.Revision = usd(cents(budgetUSD)), by, s.Revision+1
	m.settings[orgID] = s
	return &s, nil
}

// memKeys is Keys in memory, recording each Put.
type memKeys struct {
	mu     sync.Mutex
	blobs  map[string]map[string]string
	puts   []string
	putErr error
}

func newMemKeys() *memKeys { return &memKeys{blobs: map[string]map[string]string{}} }

func (k *memKeys) Put(_ context.Context, orgID, keyID string, secrets map[string]string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.putErr != nil {
		return k.putErr
	}
	k.puts = append(k.puts, orgID+"/"+keyID)
	k.blobs[orgID+"/"+keyID] = maps.Clone(secrets)
	return nil
}

func (k *memKeys) Reveal(_ context.Context, orgID, keyID string) (map[string]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	b, ok := k.blobs[orgID+"/"+keyID]
	if !ok {
		return nil, ErrNotFound
	}
	return maps.Clone(b), nil
}

func (k *memKeys) Delete(_ context.Context, orgID, keyID string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.blobs, orgID+"/"+keyID)
	return nil
}

func (k *memKeys) putCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.puts)
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

func (a *memAudit) all() []projects.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.events)
}

type notifyCall struct {
	kind     string
	orgID    string
	projects []string
}

type memNotify struct {
	mu    sync.Mutex
	calls []notifyCall
}

func (n *memNotify) ProviderKeysChanged(_ context.Context, orgID string, projects []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, notifyCall{"keys", orgID, projects})
}

func (n *memNotify) EmbeddingsChanged(_ context.Context, orgID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, notifyCall{"embeddings", orgID, nil})
}

func (n *memNotify) take() []notifyCall {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := n.calls
	n.calls = nil
	return out
}

// memProjects knows projects p1, p2 and p3 of org.
type memProjects struct{}

func (memProjects) GetProject(_ context.Context, orgID, id string) (*projects.Project, error) {
	if orgID == "org" && (id == "p1" || id == "p2" || id == "p3") {
		return &projects.Project{ID: id, OrgID: orgID}, nil
	}
	return nil, projects.ErrNotFound
}

type harness struct {
	svc    *Service
	store  *memStore
	keys   *memKeys
	audit  *memAudit
	notify *memNotify
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{store: newMemStore(), keys: newMemKeys(), audit: &memAudit{}, notify: &memNotify{}}
	h.svc = NewService(Deps{
		Store: h.store, Keys: h.keys, Authz: roleAuthz{}, Audit: h.audit, Projects: memProjects{},
		Notify: h.notify, Now: func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) },
	})
	return h
}

func ptr[T any](v T) *T { return &v }

func (h *harness) put(t *testing.T, name string, in ProviderKeyInput) *ProviderKey {
	t.Helper()
	k, err := h.svc.PutProviderKey(context.Background(), admin, name, in)
	if err != nil {
		t.Fatalf("put %s: %v", name, err)
	}
	return k
}

func anthropicKey() ProviderKeyInput {
	return ProviderKeyInput{Type: "anthropic", Fields: map[string]string{"api_key": secretValue}}
}

func openCompat() ProviderKeyInput {
	return ProviderKeyInput{
		Type:   "opencompat",
		Fields: map[string]string{"base_url": "http://10.0.0.5:8000/v1", "api_key": "oc-secret-1234"},
		Models: &[]OpenCompatModel{{
			ID: "llama-3.1-8b", Task: "chat", InputPerMillion: "0", OutputPerMillion: "0.20",
		}},
	}
}

func mustErr(t *testing.T, err, want error, contains string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if contains != "" && !strings.Contains(err.Error(), contains) {
		t.Fatalf("err = %q, want it to name %q", err, contains)
	}
}

func TestViewerReadsSettingsWithoutSecrets(t *testing.T) {
	h := newHarness(t)
	h.put(t, "claude", anthropicKey())
	v, err := h.svc.Get(context.Background(), viewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.ProviderKeys) != 1 {
		t.Fatalf("keys = %d, want 1", len(v.ProviderKeys))
	}
	k := v.ProviderKeys[0]
	if k.KeyHint != "wxyz" {
		t.Errorf("hint = %q, want wxyz", k.KeyHint)
	}
	if _, ok := k.Fields["api_key"]; ok {
		t.Error("Get returned api_key")
	}
	if v.CanManage {
		t.Error("a viewer can manage")
	}
	if v.MonthlyBudgetUSD != "0.00" || v.AllocatedUSD != "0.00" || v.UnallocatedUSD != "0.00" {
		t.Errorf("money = %q %q %q", v.MonthlyBudgetUSD, v.AllocatedUSD, v.UnallocatedUSD)
	}
	if v.Embeddings != LocalEmbeddings() {
		t.Errorf("embeddings = %+v", v.Embeddings)
	}
	av, err := h.svc.Get(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	if !av.CanManage {
		t.Error("an admin can't manage")
	}
	if _, err := h.svc.Get(context.Background(), security.Principal{OrgID: "org", UserID: "nobody"}); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("stranger Get err = %v", err)
	}
}

func TestDeveloperCannotChangeSettings(t *testing.T) {
	h := newHarness(t)
	h.put(t, "claude", anthropicKey())
	ctx := context.Background()
	if _, err := h.svc.Update(ctx, developer, UpdateInput{MonthlyBudgetUSD: ptr("10")}); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("Update err = %v", err)
	}
	if _, err := h.svc.PutProviderKey(ctx, developer, "other", anthropicKey()); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("PutProviderKey err = %v", err)
	}
	if err := h.svc.DeleteProviderKey(ctx, developer, "claude"); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("DeleteProviderKey err = %v", err)
	}
	if _, err := h.store.ProviderKey(ctx, "org", "claude"); err != nil {
		t.Errorf("the key is gone: %v", err)
	}
}

func TestPutProviderKeyStoresSecretsInVault(t *testing.T) {
	h := newHarness(t)
	k := h.put(t, "claude", anthropicKey())
	if h.keys.putCount() != 1 {
		t.Fatalf("Keys.Put calls = %d, want 1", h.keys.putCount())
	}
	blob, err := h.keys.Reveal(context.Background(), "org", k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(blob, map[string]string{"api_key": secretValue}) {
		t.Errorf("vault blob = %v", blob)
	}
	stored, err := h.store.ProviderKey(context.Background(), "org", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if stored.KeyHint != "wxyz" || stored.ID != k.ID {
		t.Errorf("stored = %+v", stored)
	}
	if _, ok := stored.Fields["api_key"]; ok {
		t.Error("the store holds api_key")
	}
	if _, ok := k.Fields["api_key"]; ok {
		t.Error("the put result holds api_key")
	}
	if k.Type != "anthropic" || len(k.Projects) != 0 {
		t.Errorf("put result = %+v", k)
	}
}

func TestProviderKeyValuesNeverReturned(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	k := h.put(t, "claude", anthropicKey())
	v, err := h.svc.Get(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	seen := make([]string, 0, 8)
	for _, x := range []any{v, k} {
		b, merr := json.Marshal(x)
		if merr != nil {
			t.Fatal(merr)
		}
		seen = append(seen, string(b))
	}
	for _, e := range h.audit.all() {
		seen = append(seen, fmt.Sprint(e.Attributes))
	}
	h.keys.putErr = errors.New("vault is sealed")
	_, err = h.svc.PutProviderKey(ctx, admin, "claude", ProviderKeyInput{Fields: map[string]string{"api_key": secretValue}})
	if err == nil {
		t.Fatal("a failing Keys.Put didn't fail the put")
	}
	seen = append(seen, err.Error())
	_, err = h.svc.PutProviderKey(ctx, admin, "other", ProviderKeyInput{Type: "anthropic", Fields: map[string]string{"api_key": secretValue, "nope": secretValue}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown field err = %v", err)
	}
	seen = append(seen, err.Error())
	for _, s := range seen {
		if strings.Contains(s, secretValue) || strings.Contains(s, "sk-ant") {
			t.Errorf("secret leaked: %s", s)
		}
	}
}

func TestOmittedSecretKeepsTheStoredOne(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	k := h.put(t, "claude", anthropicKey())
	got := h.put(t, "claude", ProviderKeyInput{Projects: &[]string{"p1"}})
	if h.keys.putCount() != 1 {
		t.Errorf("Keys.Put calls = %d, want 1", h.keys.putCount())
	}
	if got.KeyHint != "wxyz" || !slices.Equal(got.Projects, []string{"p1"}) || got.ID != k.ID {
		t.Errorf("after projects-only update: %+v", got)
	}
	blob, _ := h.keys.Reveal(ctx, "org", k.ID)
	if blob["api_key"] != secretValue {
		t.Error("the stored secret changed")
	}
	_, err := h.svc.PutProviderKey(ctx, admin, "claude", ProviderKeyInput{Fields: map[string]string{"api_key": ""}})
	mustErr(t, err, ErrInvalid, "api_key")

	// A non-secret field changes without touching the secret.
	got = h.put(t, "claude", ProviderKeyInput{Fields: map[string]string{"base_url": "https://anthropic.example.com"}})
	if h.keys.putCount() != 1 || got.Fields["base_url"] != "https://anthropic.example.com" || got.KeyHint != "wxyz" {
		t.Errorf("after base_url update: puts %d, %+v", h.keys.putCount(), got)
	}
	// A new secret replaces the old one and its hint.
	got = h.put(t, "claude", ProviderKeyInput{Fields: map[string]string{"api_key": "sk-ant-newkey9876"}})
	blob, _ = h.keys.Reveal(ctx, "org", k.ID)
	if blob["api_key"] != "sk-ant-newkey9876" || got.KeyHint != "9876" {
		t.Errorf("after new secret: blob %v hint %q", blob, got.KeyHint)
	}
}

func TestPutProviderKeyValidation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	cases := []struct {
		name string
		key  string
		in   ProviderKeyInput
		want string
	}{
		{"bad name", "Claude!", anthropicKey(), "name"},
		{"no type", "a", ProviderKeyInput{Fields: map[string]string{"api_key": secretValue}}, "type"},
		{"unknown type", "a", ProviderKeyInput{Type: "vertex", Fields: map[string]string{"api_key": secretValue}}, "vertex"},
		{"missing secret", "a", ProviderKeyInput{Type: "anthropic"}, "api_key"},
		{"unknown field", "a", ProviderKeyInput{Type: "anthropic", Fields: map[string]string{"api_key": secretValue, "region": "x"}}, "region"},
		{"relative base_url", "a", ProviderKeyInput{Type: "openai", Fields: map[string]string{"api_key": secretValue, "base_url": "/v1"}}, "base_url"},
		{"ftp base_url", "a", ProviderKeyInput{Type: "openai", Fields: map[string]string{"api_key": secretValue, "base_url": "ftp://x/v1"}}, "base_url"},
		{"short secret", "a", ProviderKeyInput{Type: "anthropic", Fields: map[string]string{"api_key": "abc"}}, "api_key"},
		{"unknown project", "a", ProviderKeyInput{Type: "anthropic", Fields: map[string]string{"api_key": secretValue}, Projects: &[]string{"p9"}}, "p9"},
		{"bedrock without region", "a", ProviderKeyInput{Type: "bedrock", Fields: map[string]string{"access_key_id": "AKIAEXAMPLE1", "secret_access_key": "secretsecret"}}, "region"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := h.svc.PutProviderKey(ctx, admin, c.key, c.in)
			mustErr(t, err, ErrInvalid, c.want)
		})
	}
	if h.keys.putCount() != 0 {
		t.Errorf("a refused put reached Vault %d times", h.keys.putCount())
	}
	// A private address is fine: the gateway dials it, not Loomplane.
	h.put(t, "local", ProviderKeyInput{Type: "openai", Fields: map[string]string{"api_key": secretValue, "base_url": "http://192.168.1.10:8080/v1"}})
	b := h.put(t, "aws", ProviderKeyInput{Type: "bedrock", Fields: map[string]string{
		"region": "us-east-1", "access_key_id": "AKIAEXAMPLE1", "secret_access_key": "secretsecret",
	}})
	if b.KeyHint != "PLE1" || b.Fields["region"] != "us-east-1" || len(b.Fields) != 1 {
		t.Errorf("bedrock key = %+v", b)
	}
}

func TestTypeCannotChange(t *testing.T) {
	h := newHarness(t)
	h.put(t, "claude", anthropicKey())
	_, err := h.svc.PutProviderKey(context.Background(), admin, "claude", ProviderKeyInput{Type: "openai"})
	mustErr(t, err, ErrInvalid, "type")
	h.put(t, "claude", ProviderKeyInput{Type: "anthropic"})
}

func TestOpenCompatNeedsModelsWithPrices(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	noModels := openCompat()
	noModels.Models = nil
	_, err := h.svc.PutProviderKey(ctx, admin, "local", noModels)
	mustErr(t, err, ErrInvalid, "models")

	cheap := openCompat()
	(*cheap.Models)[0].InputPerMillion = "cheap"
	_, err = h.svc.PutProviderKey(ctx, admin, "local", cheap)
	mustErr(t, err, ErrInvalid, "input_per_million")

	withModels := anthropicKey()
	withModels.Models = openCompat().Models
	_, err = h.svc.PutProviderKey(ctx, admin, "claude", withModels)
	mustErr(t, err, ErrInvalid, "models")

	k := h.put(t, "local", openCompat())
	if len(k.Models) != 1 || k.KeyHint != "1234" || k.Fields["base_url"] != "http://10.0.0.5:8000/v1" {
		t.Errorf("opencompat key = %+v", k)
	}
	_, err = h.svc.PutProviderKey(ctx, admin, "local", ProviderKeyInput{Models: &[]OpenCompatModel{}})
	mustErr(t, err, ErrInvalid, "models")
}

func TestKeyChangeNotifiesReachedProjects(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	in := anthropicKey()
	in.Projects = &[]string{"p1"}
	h.put(t, "claude", in)
	if got := h.notify.take(); len(got) != 1 || !slices.Equal(got[0].projects, []string{"p1"}) || got[0].kind != "keys" {
		t.Errorf("create notified %+v", got)
	}
	h.put(t, "claude", ProviderKeyInput{Projects: &[]string{"p2"}})
	if got := h.notify.take(); len(got) != 1 || got[0].orgID != "org" || !slices.Equal(got[0].projects, []string{"p1", "p2"}) {
		t.Errorf("update notified %+v", got)
	}
	h.put(t, "claude", ProviderKeyInput{Projects: &[]string{}})
	if got := h.notify.take(); len(got) != 1 || got[0].projects != nil {
		t.Errorf("widening to every project notified %+v", got)
	}
	// Narrowing from every project reaches every project it used to.
	h.put(t, "claude", ProviderKeyInput{Projects: &[]string{"p3"}})
	if got := h.notify.take(); len(got) != 1 || got[0].projects != nil {
		t.Errorf("narrowing from every project notified %+v", got)
	}
	if _, err := h.svc.Update(ctx, admin, UpdateInput{Embeddings: &Embeddings{Mode: EmbeddingsLocal}}); err != nil {
		t.Fatal(err)
	}
	if got := h.notify.take(); len(got) != 1 || got[0].kind != "embeddings" || got[0].orgID != "org" {
		t.Errorf("embeddings notified %+v", got)
	}
	if err := h.svc.DeleteProviderKey(ctx, admin, "claude"); err != nil {
		t.Fatal(err)
	}
	if got := h.notify.take(); len(got) != 1 || got[0].kind != "keys" || !slices.Equal(got[0].projects, []string{"p3"}) {
		t.Errorf("delete notified %+v", got)
	}
}

func TestUpdateBudgetAndEmbeddingsTogether(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.put(t, "voyage", ProviderKeyInput{Type: "voyageai", Fields: map[string]string{"api_key": "pa-voyage-0001"}})
	v, err := h.svc.Update(ctx, admin, UpdateInput{
		MonthlyBudgetUSD: ptr("250.5"),
		Embeddings:       &Embeddings{Mode: EmbeddingsProvider, Provider: "voyage", Model: "voyage-3", Dimensions: 1024},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.MonthlyBudgetUSD != "250.50" || v.Embeddings.Provider != "voyage" || v.UnallocatedUSD != "250.50" {
		t.Errorf("view = %+v", v)
	}
	_, err = h.svc.Update(ctx, admin, UpdateInput{MonthlyBudgetUSD: ptr("1"), Revision: v.Revision - 1})
	mustErr(t, err, ErrStale, "")
	for _, bad := range []string{"-1", "abc", "1.234", "", "1e3"} {
		_, err = h.svc.Update(ctx, admin, UpdateInput{MonthlyBudgetUSD: ptr(bad), Revision: v.Revision})
		mustErr(t, err, ErrInvalid, "")
	}
	v, err = h.svc.Update(ctx, admin, UpdateInput{MonthlyBudgetUSD: ptr("0"), Revision: v.Revision})
	if err != nil || v.MonthlyBudgetUSD != "0.00" {
		t.Fatalf("budget 0: %v %+v", err, v)
	}
}

func TestBudgetBelowAllocationsIsRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.Update(ctx, admin, UpdateInput{MonthlyBudgetUSD: ptr("100")}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Allocation{
		{Scope: "gateway:env1/ai", Label: "shop / staging", MonthlyBudgetUSD: "40"},
		{Scope: "gateway:env2/ai", Label: "shop / production", MonthlyBudgetUSD: "50"},
	} {
		if err := h.svc.Allocate(ctx, developer, security.AIEdit, "p1", a); err != nil {
			t.Fatal(err)
		}
	}
	v, err := h.svc.Get(ctx, viewer)
	if err != nil {
		t.Fatal(err)
	}
	if v.AllocatedUSD != "90.00" || v.UnallocatedUSD != "10.00" || len(v.Allocations) != 2 {
		t.Errorf("view money = %q %q, %d allocations", v.AllocatedUSD, v.UnallocatedUSD, len(v.Allocations))
	}
	_, err = h.svc.Update(ctx, admin, UpdateInput{MonthlyBudgetUSD: ptr("50"), Revision: v.Revision})
	mustErr(t, err, ErrUnderAllocated, "shop / staging")
	if !strings.Contains(err.Error(), "shop / production") {
		t.Errorf("err %q doesn't name every allocation", err)
	}
	err = h.svc.Allocate(ctx, admin, security.AIManage, "p1", Allocation{Scope: "gateway:env3/ai", Label: "x", MonthlyBudgetUSD: "20"})
	mustErr(t, err, ErrOverBudget, "10.00")
}

func TestAllocateValidates(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.Update(ctx, admin, UpdateInput{MonthlyBudgetUSD: ptr("100")}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Allocation{
		{Label: "x", MonthlyBudgetUSD: "1"},
		{Scope: "s", MonthlyBudgetUSD: "1"},
		{Scope: "s", Label: "x", MonthlyBudgetUSD: "0"},
		{Scope: "s", Label: "x", MonthlyBudgetUSD: "-5"},
		{Scope: "s", Label: "x", MonthlyBudgetUSD: "ten"},
	} {
		mustErr(t, h.svc.Allocate(ctx, admin, security.AIManage, "p1", a), ErrInvalid, "")
	}
	err := h.svc.Allocate(ctx, viewer, security.AIEdit, "p1", Allocation{Scope: "s", Label: "x", MonthlyBudgetUSD: "1"})
	mustErr(t, err, security.ErrForbidden, "")
	err = h.svc.Allocate(ctx, developer, security.AIManage, "p1", Allocation{Scope: "s", Label: "x", MonthlyBudgetUSD: "1"})
	mustErr(t, err, security.ErrForbidden, "")

	if err = h.svc.Allocate(ctx, developer, security.AIEdit, "p1", Allocation{Scope: "s", Label: "x", MonthlyBudgetUSD: "12.5"}); err != nil {
		t.Fatal(err)
	}
	a, err := h.svc.Allocation(ctx, "org", "s")
	if err != nil || a.MonthlyBudgetUSD != "12.50" || a.ProjectID != "p1" {
		t.Fatalf("allocation = %+v, %v", a, err)
	}
	if err = h.svc.ReleaseAllocation(ctx, "org", "s"); err != nil {
		t.Fatal(err)
	}
	_, err = h.svc.Allocation(ctx, "org", "s")
	mustErr(t, err, ErrNotFound, "")
}

func TestEmbeddingsChoice(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.put(t, "claude", anthropicKey())
	h.put(t, "voyage", ProviderKeyInput{Type: "voyageai", Fields: map[string]string{"api_key": "pa-voyage-0001"}})

	v, err := h.svc.Update(ctx, admin, UpdateInput{Embeddings: &Embeddings{Mode: EmbeddingsLocal, Model: "whatever", Dimensions: 7}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Embeddings != LocalEmbeddings() {
		t.Errorf("local = %+v", v.Embeddings)
	}
	if v.Embeddings.Model != "granite-embedding-107m-multilingual" || v.Embeddings.Dimensions != 384 {
		t.Errorf("local = %+v", v.Embeddings)
	}
	v, err = h.svc.Update(ctx, admin, UpdateInput{Revision: v.Revision,
		Embeddings: &Embeddings{Mode: EmbeddingsProvider, Provider: "voyage", Model: "voyage-3", Dimensions: 1024}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := h.svc.EmbeddingsFor(ctx, "org"); got.Provider != "voyage" || got.Model != "voyage-3" || got.Dimensions != 1024 {
		t.Errorf("EmbeddingsFor = %+v", got)
	}
	bad := []struct {
		e    Embeddings
		want string
	}{
		{Embeddings{Mode: EmbeddingsProvider, Provider: "claude", Model: "x", Dimensions: 1024}, "Anthropic has no embeddings API"},
		{Embeddings{Mode: EmbeddingsProvider, Provider: "missing", Model: "x", Dimensions: 1024}, "missing"},
		{Embeddings{Mode: EmbeddingsProvider, Provider: "voyage", Dimensions: 1024}, "model"},
		{Embeddings{Mode: EmbeddingsProvider, Provider: "voyage", Model: "voyage-3", Dimensions: 32}, "64"},
		{Embeddings{Mode: EmbeddingsProvider, Provider: "voyage", Model: "voyage-3", Dimensions: 8192}, "4096"},
		{Embeddings{Mode: EmbeddingsDeployment, Deployment: "d"}, "Model deployments arrive with the model registry."},
		{Embeddings{Mode: "cloud"}, "cloud"},
	}
	for _, b := range bad {
		_, err := h.svc.Update(ctx, admin, UpdateInput{Revision: v.Revision, Embeddings: &b.e})
		mustErr(t, err, ErrInvalid, b.want)
	}
}

func TestDeleteKeyUsedForEmbeddings(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	k := h.put(t, "voyage", ProviderKeyInput{Type: "voyageai", Fields: map[string]string{"api_key": "pa-voyage-0001"}})
	if _, err := h.svc.Update(ctx, admin, UpdateInput{
		Embeddings: &Embeddings{Mode: EmbeddingsProvider, Provider: "voyage", Model: "voyage-3", Dimensions: 1024},
	}); err != nil {
		t.Fatal(err)
	}
	mustErr(t, h.svc.DeleteProviderKey(ctx, admin, "voyage"), ErrInUse, "embeddings")
	if _, err := h.keys.Reveal(ctx, "org", k.ID); err != nil {
		t.Errorf("a refused delete removed the secret: %v", err)
	}
	mustErr(t, h.svc.DeleteProviderKey(ctx, admin, "nope"), ErrNotFound, "")

	v, _ := h.svc.Get(ctx, admin)
	if _, err := h.svc.Update(ctx, admin, UpdateInput{Revision: v.Revision, Embeddings: &Embeddings{Mode: EmbeddingsLocal}}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.DeleteProviderKey(ctx, admin, "voyage"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.keys.Reveal(ctx, "org", k.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the secret outlived its key: %v", err)
	}
}

func TestGatewayKeysOnlyForGrantedProjects(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.put(t, "k1", anthropicKey())
	k2 := ProviderKeyInput{Type: "openai", Fields: map[string]string{"api_key": "sk-openai-5555", "base_url": "https://oai.example.com/v1"}, Projects: &[]string{"p2"}}
	h.put(t, "k2", k2)

	got, err := h.svc.GatewayKeys(ctx, "org", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "k1" || got[0].Type != "anthropic" || got[0].Fields["api_key"] != secretValue || got[0].Revision != 1 {
		t.Errorf("GatewayKeys(p1) = %+v", got)
	}
	got, err = h.svc.GatewayKeys(ctx, "org", "p2")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "k1" || got[1].Name != "k2" {
		t.Fatalf("GatewayKeys(p2) = %+v", got)
	}
	if !maps.Equal(got[1].Fields, map[string]string{"api_key": "sk-openai-5555", "base_url": "https://oai.example.com/v1"}) {
		t.Errorf("k2 fields = %v", got[1].Fields)
	}
	r, err := h.svc.RevealKey(ctx, "org", "k2")
	if err != nil || r.Fields["api_key"] != "sk-openai-5555" {
		t.Errorf("RevealKey = %+v, %v", r, err)
	}
	_, err = h.svc.RevealKey(ctx, "org", "nope")
	mustErr(t, err, ErrNotFound, "")
}

func TestGatewayKeysDigest(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	digest := func(project string) string {
		t.Helper()
		d, err := h.svc.GatewayKeysDigest(ctx, "org", project)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(d, "sha256:") || len(d) != len("sha256:")+64 {
			t.Fatalf("digest %q", d)
		}
		return d
	}
	h.put(t, "k1", anthropicKey())
	h.put(t, "k2", ProviderKeyInput{Type: "openai", Fields: map[string]string{"api_key": "sk-openai-5555"}, Projects: &[]string{"p2"}})
	h.put(t, "local", openCompat())

	d1, d2 := digest("p1"), digest("p1")
	if d1 != d2 {
		t.Fatal("digest isn't stable")
	}
	p2 := digest("p2")
	if p2 == d1 {
		t.Error("p2's digest ignores k2")
	}
	for _, d := range []string{d1, p2} {
		if strings.Contains(d, secretValue) || strings.Contains(d, "sk-openai") {
			t.Error("digest has secret bytes")
		}
	}

	// An opencompat key is pushed live, so it doesn't change the digest.
	h.put(t, "local", ProviderKeyInput{Models: &[]OpenCompatModel{{ID: "m2", Task: "chat", InputPerMillion: "1", OutputPerMillion: "2"}}})
	if digest("p1") != d1 {
		t.Error("an opencompat change moved the digest")
	}
	// k2 reaches only p2.
	h.put(t, "k2", ProviderKeyInput{Fields: map[string]string{"api_key": "sk-openai-6666"}})
	if digest("p1") != d1 {
		t.Error("a p2-only key moved p1's digest")
	}
	if digest("p2") == p2 {
		t.Error("k2's revision didn't move p2's digest")
	}
	// A new secret bumps the revision; a new non-secret field changes it too.
	h.put(t, "k1", ProviderKeyInput{Fields: map[string]string{"base_url": "https://a.example.com"}})
	d3 := digest("p1")
	if d3 == d1 {
		t.Error("a field change didn't move the digest")
	}
}

func TestSettingsChangesAreAudited(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.put(t, "claude", anthropicKey())
	h.put(t, "claude", ProviderKeyInput{Projects: &[]string{"p1"}})
	if _, err := h.svc.Update(ctx, admin, UpdateInput{MonthlyBudgetUSD: ptr("100"), Embeddings: &Embeddings{Mode: EmbeddingsLocal}}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Allocate(ctx, admin, security.AIManage, "p1", Allocation{Scope: "s", Label: "x", MonthlyBudgetUSD: "5"}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.DeleteProviderKey(ctx, admin, "claude"); err != nil {
		t.Fatal(err)
	}
	// Refused writes aren't recorded.
	_, _ = h.svc.PutProviderKey(ctx, developer, "x", anthropicKey())
	_, _ = h.svc.PutProviderKey(ctx, admin, "x", ProviderKeyInput{Type: "nope"})

	events := h.audit.all()
	actions := make([]string, 0, len(events))
	for _, e := range events {
		actions = append(actions, e.Action)
		if e.Principal != admin {
			t.Errorf("%s principal = %+v", e.Action, e.Principal)
		}
	}
	want := []string{"ai.provider_key.put", "ai.provider_key.put", "ai.settings.update", "ai.budget.allocate", "ai.provider_key.delete"}
	if !slices.Equal(actions, want) {
		t.Fatalf("actions = %v, want %v", actions, want)
	}
	fieldsOf := func(e projects.AuditEvent) []string {
		f, _ := e.Attributes["fields"].([]string)
		return f
	}
	checks := [][]string{
		{"api_key", "projects", "type"},
		{"projects"},
		{"embeddings", "monthly_budget_usd"},
		{"label", "monthly_budget_usd", "scope"},
		nil,
	}
	for i, w := range checks {
		got := fieldsOf(events[i])
		sort.Strings(got)
		if w != nil && !slices.Equal(got, w) {
			t.Errorf("%s fields = %v, want %v", events[i].Action, got, w)
		}
	}
	for _, e := range events {
		s := fmt.Sprint(e.Attributes)
		if strings.Contains(s, secretValue) || strings.Contains(s, "100") {
			t.Errorf("%s attributes carry a value: %s", e.Action, s)
		}
	}
}

func TestSetNotifierIsOptional(t *testing.T) {
	h := newHarness(t)
	h.svc.SetNotifier(nil)
	h.put(t, "claude", anthropicKey())
	if got := h.notify.take(); len(got) != 0 {
		t.Errorf("an unset notifier was called: %+v", got)
	}
	n := &memNotify{}
	h.svc.SetNotifier(n)
	h.put(t, "claude", ProviderKeyInput{Projects: &[]string{"p3"}})
	if got := n.take(); len(got) != 1 {
		t.Errorf("the bound notifier got %+v", got)
	}
}

func TestVaultKeysKeepOneBlobPerKey(t *testing.T) {
	ctx := context.Background()
	enc, err := crypto.NewEncryptor([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	svc := secret.NewService(vaultmem.New(), enc, secret.WithAppID(VaultAppID))
	keys := NewVaultKeys(svc)
	if _, err = keys.Reveal(ctx, "org", "aikey_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reveal before Put = %v, want ErrNotFound", err)
	}
	blob := map[string]string{"access_key_id": "AKIAEXAMPLE1", "secret_access_key": "secretsecret"}
	if err = keys.Put(ctx, "org", "aikey_1", blob); err != nil {
		t.Fatal(err)
	}
	got, err := keys.Reveal(ctx, "org", "aikey_1")
	if err != nil || !maps.Equal(got, blob) {
		t.Fatalf("Reveal = %v, %v", got, err)
	}
	// The Vault key is the org's and the key's, under the AI app.
	raw, err := svc.Get(ctx, "org/provider-keys/aikey_1", VaultAppID)
	if err != nil {
		t.Fatalf("vault key: %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(raw.Value, &decoded); err != nil || !maps.Equal(decoded, blob) {
		t.Errorf("vault value = %s", raw.Value)
	}
	if _, err := keys.Reveal(ctx, "other", "aikey_1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another org read the key: %v", err)
	}
	if err := keys.Delete(ctx, "org", "aikey_1"); err != nil {
		t.Fatal(err)
	}
	if err := keys.Delete(ctx, "org", "aikey_1"); err != nil {
		t.Errorf("second Delete = %v", err)
	}
	if _, err := keys.Reveal(ctx, "org", "aikey_1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Reveal after Delete = %v", err)
	}
}
