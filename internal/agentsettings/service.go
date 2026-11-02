package agentsettings

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// Authorizer decides whether a principal may act. *security.Authorizer is
// the real one.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// Gateway is nexus as the agent's settings use it: one tenant per
// workspace, holding the agent's monthly quota and its spend. Budget
// amounts are decimal USD strings ("25.00"); the adapter converts.
type Gateway interface {
	// EnsureTenant creates the workspace's tenant, or finds the one it
	// has, and sets its monthly quota.
	EnsureTenant(ctx context.Context, orgID, budgetUSD string) (tenantID string, err error)
	SetBudget(ctx context.Context, tenantID, budgetUSD string) error
	SetActive(ctx context.Context, tenantID string, active bool) error
	// MonthlySpend is what the tenant spent this month, in USD.
	MonthlySpend(ctx context.Context, tenantID string) (string, error)
	// Priced reports whether nexus prices the model, from its catalog or
	// ai.prices. A provider key's own prices never count.
	Priced(ctx context.Context, provider, model string) bool
}

// Seeder sets up the workspace's guardrail profiles before the agent's
// first run.
type Seeder interface {
	Seed(ctx context.Context, orgID string) error
}

// Roles gives the workspace's agent identity its role, and takes it away.
// *security.Authorizer is the real one.
type Roles interface {
	AssignAgent(ctx context.Context, orgID string) error
	RevokeAgent(ctx context.Context, orgID string) error
}

// Canceller stops the workspace's agent runs when the agent turns off.
type Canceller interface {
	CancelAll(ctx context.Context, orgID string) error
}

// AISettings is what the agent's settings ask of the workspace AI
// settings. *aisettings.Service is the real one.
type AISettings interface {
	KeySource
	PutProviderKey(ctx context.Context, p security.Principal, name string, in aisettings.ProviderKeyInput) (*aisettings.ProviderKey, error)
	DeleteProviderKey(ctx context.Context, p security.Principal, name string) error
	Allocate(ctx context.Context, p security.Principal, need security.Permission, projectID string, a aisettings.Allocation) error
	Allocation(ctx context.Context, orgID, scope string) (*aisettings.Allocation, error)
	ReleaseAllocation(ctx context.Context, orgID, scope string) error
}

var _ AISettings = (*aisettings.Service)(nil)

// Deps are what a Service needs. Gateway and Seeder come from the agent's
// own extension and may be bound later with BindGateway; until then the
// agent can't turn on.
type Deps struct {
	Store   Store
	AI      AISettings
	Gateway Gateway
	Seeder  Seeder
	Roles   Roles
	Authz   Authorizer
	Audit   projects.Auditor
	Config  config.AIConfig
	// Environment is config.Environment: production wants https base URLs,
	// and only development allows private ones.
	Environment string
	// Denied is planning.endpoint_denied_cidrs, refused everywhere.
	Denied []netip.Prefix
	// Lookup resolves a base URL's host. Nil is the system resolver.
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	Now    func() time.Time
}

// Service is the agent's settings API. Every method that takes a
// principal checks it first; the ones that don't are for the agent's own
// services.
type Service struct {
	store       Store
	ai          AISettings
	keys        *Keys
	roles       Roles
	authz       Authorizer
	audit       projects.Auditor
	cfg         config.AIConfig
	environment string
	denied      []netip.Prefix
	lookup      func(ctx context.Context, host string) ([]netip.Addr, error)
	now         func() time.Time

	mu        sync.RWMutex
	gateway   Gateway
	seeder    Seeder
	canceller Canceller
	knowledge KnowledgeStatus
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	lookup, now := d.Lookup, d.Now
	if lookup == nil {
		lookup = systemLookup
	}
	if now == nil {
		now = time.Now
	}
	return &Service{
		store: d.Store, ai: d.AI, keys: NewKeys(d.AI), roles: d.Roles, authz: d.Authz, audit: d.Audit,
		cfg: d.Config, environment: d.Environment, denied: slices.Clone(d.Denied), lookup: lookup, now: now,
		gateway: d.Gateway, seeder: d.Seeder,
		knowledge: KnowledgeStatus{
			Configured: d.Config.Knowledge.Configured(), Reason: "Knowledge isn't set up yet on this server",
		},
	}
}

// BindGateway binds nexus and the guardrail seeder, once the agent's
// extension has built them.
func (s *Service) BindGateway(g Gateway, seeder Seeder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gateway, s.seeder = g, seeder
}

// BindCanceller binds what stops the agent's runs, once it's built.
func (s *Service) BindCanceller(c Canceller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.canceller = c
}

// SetKnowledge records whether this server can give the agent knowledge.
func (s *Service) SetKnowledge(st KnowledgeStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.knowledge = st
}

func (s *Service) bound() (Gateway, Seeder, Canceller, KnowledgeStatus) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gateway, s.seeder, s.canceller, s.knowledge
}

// Credentials is the nexus credential resolver over these settings.
func (s *Service) Credentials() *Credentials { return &Credentials{s: s} }

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func (s *Service) record(ctx context.Context, p security.Principal, action string, attrs map[string]any) {
	if s.audit == nil {
		return
	}
	s.audit.Record(ctx, projects.AuditEvent{Action: action, Severity: "info", Principal: p, Attributes: attrs})
}

// Get returns the agent's settings as the settings page shows them.
// ai:manage.
func (s *Service) Get(ctx context.Context, p security.Principal) (*AgentView, error) {
	if err := s.authz.Require(ctx, p, security.AIManage, ""); err != nil {
		return nil, err
	}
	return s.view(ctx, p.OrgID)
}

func (s *Service) view(ctx context.Context, orgID string) (*AgentView, error) {
	st, err := s.store.Get(ctx, orgID)
	if err != nil {
		return nil, err
	}
	gw, _, _, knowledge := s.bound()
	v := &AgentView{
		Settings: *st,
		Server: ServerDefaults{
			Provider: s.cfg.Provider, Model: s.cfg.Model, BaseURL: displayURL(s.cfg.BaseURL), KeySet: s.cfg.APIKey != "",
		},
		KnowledgeStatus: knowledge,
		Available:       gw != nil,
	}
	if st.ProviderKey != "" {
		k, kerr := s.keys.Info(ctx, orgID, st.ProviderKey)
		switch {
		case kerr == nil:
			v.Key = &KeyView{Name: k.Name, Type: k.Type, Hint: k.Hint}
		case !errors.Is(kerr, ErrNotFound):
			return nil, kerr
		}
	}
	var problems []string
	r, err := s.resolve(ctx, st)
	switch {
	case err == nil:
		v.Effective = EffectiveView{Provider: r.provider, Model: r.model, SummaryModel: r.summary, BaseURL: displayURL(r.baseURL)}
	case errors.Is(err, ErrInvalid):
		problems = append(problems, sentence(err))
	default:
		return nil, err
	}
	b, err := s.Budget(ctx, orgID)
	if err != nil {
		limit, lerr := s.allocation(ctx, orgID)
		if lerr != nil {
			return nil, lerr
		}
		b = BudgetState{LimitUSD: limit}
		problems = append(problems, "Loomplane couldn't read what the agent spent this month.")
	}
	v.Budget = b
	v.Problem = strings.Join(problems, " ")
	return v, nil
}

// displayURL is a URL without anything after its path that could be a
// secret.
func displayURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

// sentence is err's text without ErrInvalid's in front.
func sentence(err error) string {
	msg := err.Error()
	if rest, ok := strings.CutPrefix(msg, ErrInvalid.Error()+": "); ok {
		return rest
	}
	return msg
}

// resolved is what the agent runs with, worked out from its settings.
type resolved struct {
	provider, model, summary, baseURL string
}

// resolve works out the agent's provider, model and base URL. A setting
// that can't run is ErrInvalid, with why.
func (s *Service) resolve(ctx context.Context, st *Settings) (resolved, error) {
	r := resolved{provider: s.cfg.Provider, baseURL: s.cfg.BaseURL, model: st.Model}
	if st.ProviderKey == "" {
		if r.model == "" {
			r.model = s.cfg.Model
		}
	} else {
		k, err := s.keyInfo(ctx, st.OrgID, st.ProviderKey)
		if err != nil {
			return resolved{}, err
		}
		if k.Provider == "" {
			return resolved{}, unusableType(k)
		}
		r.provider, r.baseURL = k.Provider, k.BaseURL
		if r.model == "" {
			switch r.provider {
			case s.cfg.Provider:
				r.model = s.cfg.Model
			case config.AIProviderAnthropic:
				r.model = config.DefaultAIModel
			}
		}
		if r.provider == config.AIProviderOpenAICompatible && r.baseURL == "" {
			return resolved{}, invalid("the OpenAI-compatible key %q has no base_url", k.Name)
		}
	}
	if r.model == "" {
		return resolved{}, invalid("choose a model for the agent: Loomplane has no default model for %s", r.provider)
	}
	r.summary = r.model
	if r.provider == config.AIProviderAnthropic {
		r.summary = config.SummaryAIModel
	}
	return r, nil
}

// keyInfo is the key called name, with a missing one ErrInvalid.
func (s *Service) keyInfo(ctx context.Context, orgID, name string) (*Key, error) {
	k, err := s.keys.Info(ctx, orgID, name)
	if errors.Is(err, ErrNotFound) {
		return nil, invalid("there's no provider key called %q. Choose another key for the agent", name)
	}
	return k, err
}

func unusableType(k *Key) error {
	return invalid("the agent runs on Anthropic, OpenAI or OpenAI-compatible keys, and %q is a %s key", k.Name, k.Type)
}

// checkKey decides whether the agent may use k: a type it runs on, granted
// to every project, and a base URL it may send the key to.
func (s *Service) checkKey(ctx context.Context, k *Key) error {
	if k.Provider == "" {
		return unusableType(k)
	}
	if len(k.Projects) > 0 {
		return invalid("the agent works across the workspace, so its provider key must be granted to every project, and %q is granted to some", k.Name)
	}
	if k.BaseURL != "" {
		return s.checkBaseURL(ctx, k.BaseURL)
	}
	return nil
}

// priced reports whether the server prices model: ai.prices or nexus's
// catalog. A provider key's own prices don't count.
func (s *Service) priced(ctx context.Context, gw Gateway, provider, model string) bool {
	for _, p := range s.cfg.Prices {
		if p.Provider == provider && p.Model == model {
			return true
		}
	}
	return gw != nil && gw.Priced(ctx, provider, model)
}

// maxModelLength bounds a model name.
const maxModelLength = 200

// Update changes the agent's settings. ai:manage. in.Revision is the
// workspace AI settings' revision the caller read; a newer one is
// ErrStale. Turning the agent on needs its budget, a key and a model the
// server prices; it creates or finds the workspace's nexus tenant, seeds
// its guardrails, gives the agent its role and opens the tenant. Turning
// it off closes the tenant, takes the role away and stops its runs.
func (s *Service) Update(ctx context.Context, p security.Principal, in AgentUpdateInput) (*AgentView, error) {
	if err := s.authz.Require(ctx, p, security.AIManage, ""); err != nil {
		return nil, err
	}
	cur, err := s.store.Get(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	if cur.Revision != in.Revision {
		return nil, ErrStale
	}
	next := *cur
	var changed []string
	if in.Enabled != nil && *in.Enabled != cur.Enabled {
		next.Enabled = *in.Enabled
		changed = append(changed, "enabled")
	}
	if in.ProviderKey != nil {
		if name := strings.TrimSpace(*in.ProviderKey); name != cur.ProviderKey {
			next.ProviderKey = name
			changed = append(changed, "provider_key")
		}
	}
	if in.Model != nil {
		m := strings.TrimSpace(*in.Model)
		if len(m) > maxModelLength || strings.ContainsFunc(m, unicode.IsControl) {
			return nil, invalid("a model name is up to %d characters, with no control characters", maxModelLength)
		}
		if m != cur.Model {
			next.Model = m
			changed = append(changed, "model")
		}
	}
	if in.Knowledge != nil && *in.Knowledge != cur.Knowledge {
		next.Knowledge = *in.Knowledge
		changed = append(changed, "knowledge")
	}
	if len(changed) == 0 {
		return s.view(ctx, p.OrgID)
	}
	slices.Sort(changed)

	key, budget, err := s.validate(ctx, &next, slices.Contains(changed, "provider_key"))
	if err != nil {
		return nil, err
	}
	gw, seeder, canceller, _ := s.bound()
	next.UpdatedBy = p.UserID
	rev := cur.Revision
	turningOn, turningOff := next.Enabled && !cur.Enabled, !next.Enabled && cur.Enabled
	if turningOn {
		if rev, err = s.turnOn(ctx, p, cur, &next, gw, seeder, budget); err != nil {
			return nil, err
		}
	}
	if serr := s.store.Save(ctx, &next, rev); serr != nil {
		if turningOn {
			s.undoOn(ctx, gw, p.OrgID, next.NexusTenantID)
		}
		return nil, serr
	}
	attrs := map[string]any{"fields": changed}
	if slices.Contains(changed, "provider_key") {
		attrs["provider_key"] = next.ProviderKey
		if key != nil {
			attrs["provider_key_id"], attrs["key_hint"] = key.ID, key.Hint
		}
	}
	s.record(ctx, p, "agent.settings.update", attrs)
	if turningOff {
		var errs []error
		if gw != nil && next.NexusTenantID != "" {
			errs = append(errs, gw.SetActive(ctx, next.NexusTenantID, false))
		}
		if s.roles != nil {
			errs = append(errs, s.roles.RevokeAgent(ctx, p.OrgID))
		}
		if canceller != nil {
			errs = append(errs, canceller.CancelAll(ctx, p.OrgID))
		}
		if err = errors.Join(errs...); err != nil {
			return nil, fmt.Errorf("the agent is off, but turning it off didn't finish: %w", err)
		}
	}
	return s.view(ctx, p.OrgID)
}

// validate checks next before anything is written, and returns the key it
// names, when it read one, and the agent's budget when it's on.
func (s *Service) validate(ctx context.Context, next *Settings, keyChanged bool) (*Key, string, error) {
	var key *Key
	if next.ProviderKey != "" && (keyChanged || next.Enabled) {
		k, err := s.keyInfo(ctx, next.OrgID, next.ProviderKey)
		if err != nil {
			return nil, "", err
		}
		if cerr := s.checkKey(ctx, k); cerr != nil {
			return nil, "", cerr
		}
		key = k
	}
	if !next.Enabled {
		return key, "", nil
	}
	gw, _, _, _ := s.bound()
	if gw == nil {
		return nil, "", invalid("this server can't run the agent yet")
	}
	r, err := s.resolve(ctx, next)
	if err != nil {
		return nil, "", err
	}
	if next.ProviderKey == "" && s.cfg.APIKey == "" && r.provider != config.AIProviderOpenAICompatible {
		return nil, "", invalid("the server has no AI key (ai.api_key). Choose a provider key for the agent")
	}
	for _, m := range slices.Compact([]string{r.model, r.summary}) {
		if !s.priced(ctx, gw, r.provider, m) {
			return nil, "", invalid("this server has no price for %s model %q. The agent runs only on models nexus's catalog or ai.prices prices; a provider key's own prices don't count", r.provider, m)
		}
	}
	budget, err := s.allocation(ctx, next.OrgID)
	if err != nil {
		return nil, "", err
	}
	if c, cerr := toCents(budget); cerr != nil || c <= 0 {
		return nil, "", invalid("give the agent a monthly budget first")
	}
	return key, budget, nil
}

// turnOn makes the workspace's tenant, storing its ID at once so a retry
// finds it, then seeds, assigns and opens. It returns the revision the
// final save expects.
func (s *Service) turnOn(ctx context.Context, p security.Principal, cur, next *Settings, gw Gateway, seeder Seeder, budget string) (int64, error) {
	rev := cur.Revision
	if next.NexusTenantID == "" {
		tenant, err := gw.EnsureTenant(ctx, p.OrgID, budget)
		if err != nil {
			return 0, fmt.Errorf("create the agent's nexus tenant: %w", err)
		}
		stage := *cur
		stage.NexusTenantID, stage.UpdatedBy = tenant, p.UserID
		if serr := s.store.Save(ctx, &stage, rev); serr != nil {
			return 0, serr
		}
		rev, next.NexusTenantID = stage.Revision, tenant
	} else if err := gw.SetBudget(ctx, next.NexusTenantID, budget); err != nil {
		return 0, fmt.Errorf("set the agent's nexus budget: %w", err)
	}
	if seeder != nil {
		if err := seeder.Seed(ctx, p.OrgID); err != nil {
			return 0, fmt.Errorf("seed the agent's guardrails: %w", err)
		}
	}
	if s.roles != nil {
		if err := s.roles.AssignAgent(ctx, p.OrgID); err != nil {
			return 0, fmt.Errorf("give the agent its role: %w", err)
		}
	}
	if err := gw.SetActive(ctx, next.NexusTenantID, true); err != nil {
		s.undoOn(ctx, gw, p.OrgID, next.NexusTenantID)
		return 0, fmt.Errorf("open the agent's nexus tenant: %w", err)
	}
	return rev, nil
}

// undoOn closes the tenant and takes the agent's role away again, when
// turning it on didn't finish, so an agent that's off holds neither. It's
// best effort: the next enable or disable sets both again.
func (s *Service) undoOn(ctx context.Context, gw Gateway, orgID, tenantID string) {
	if gw != nil && tenantID != "" {
		_ = gw.SetActive(ctx, tenantID, false) //nolint:errcheck // best effort, see above
	}
	if s.roles != nil {
		_ = s.roles.RevokeAgent(ctx, orgID) //nolint:errcheck // best effort, see above
	}
}

// SetBudget sets the agent's monthly budget: its allocation of the
// workspace budget, which the workspace budget must cover. "0" releases
// it, which only an agent that's off can do. ai:manage.
func (s *Service) SetBudget(ctx context.Context, p security.Principal, amount string) (*AgentView, error) {
	if err := s.authz.Require(ctx, p, security.AIManage, ""); err != nil {
		return nil, err
	}
	c, err := toCents(strings.TrimSpace(amount))
	if err != nil {
		return nil, invalid("the agent's monthly budget must be an amount in USD, like 25 or 12.50")
	}
	st, err := s.store.Get(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	if c == 0 {
		if st.Enabled {
			return nil, invalid("turn the agent off before taking its budget away")
		}
		if rerr := s.ai.ReleaseAllocation(ctx, p.OrgID, AllocationScope); rerr != nil {
			return nil, rerr
		}
		s.record(ctx, p, "ai.budget.release", map[string]any{"scope": AllocationScope})
		return s.view(ctx, p.OrgID)
	}
	budget := fromCents(c)
	err = s.ai.Allocate(ctx, p, security.AIManage, "", aisettings.Allocation{
		Scope: AllocationScope, Label: AllocationLabel, MonthlyBudgetUSD: budget,
	})
	if err != nil {
		return nil, err
	}
	if gw, _, _, _ := s.bound(); gw != nil && st.NexusTenantID != "" {
		if err = gw.SetBudget(ctx, st.NexusTenantID, budget); err != nil {
			return nil, fmt.Errorf("the agent's budget is saved as %s USD, but nexus didn't take it: %w", budget, err)
		}
	}
	return s.view(ctx, p.OrgID)
}

// PutProviderKey creates or changes a workspace provider key of a type the
// agent runs on, through the workspace AI settings. ai:manage. The key the
// agent uses must stay granted to every project and at a base URL the
// agent may call.
func (s *Service) PutProviderKey(ctx context.Context, p security.Principal, name string, in aisettings.ProviderKeyInput) (*aisettings.ProviderKey, error) {
	if err := s.authz.Require(ctx, p, security.AIManage, ""); err != nil {
		return nil, err
	}
	old, err := s.keys.Info(ctx, p.OrgID, name)
	switch {
	case errors.Is(err, ErrNotFound):
		old = nil
	case err != nil:
		return nil, err
	}
	typ := in.Type
	if old != nil {
		typ = old.Type
	}
	if _, ok := NexusProvider(typ); !ok {
		return nil, invalid("these settings add Anthropic, OpenAI or OpenAI-compatible keys, and %q isn't one", typ)
	}
	if old != nil {
		st, err := s.store.Get(ctx, p.OrgID)
		if err != nil {
			return nil, err
		}
		if st.ProviderKey == name {
			if in.Projects != nil && len(*in.Projects) > 0 {
				return nil, invalid("the agent uses %q and works across the workspace, so the key must stay granted to every project", name)
			}
			if u := strings.TrimSpace(in.Fields["base_url"]); u != "" {
				if err := s.checkBaseURL(ctx, u); err != nil {
					return nil, err
				}
			}
		}
	}
	return s.ai.PutProviderKey(ctx, p, name, in)
}

// DeleteProviderKey deletes a workspace provider key of a type the agent
// runs on, unless the agent uses it. ai:manage.
func (s *Service) DeleteProviderKey(ctx context.Context, p security.Principal, name string) error {
	if err := s.authz.Require(ctx, p, security.AIManage, ""); err != nil {
		return err
	}
	k, err := s.keys.Info(ctx, p.OrgID, name)
	if err != nil {
		return err
	}
	if k.Provider == "" {
		return invalid("these settings delete Anthropic, OpenAI or OpenAI-compatible keys, and %q is a %s key", name, k.Type)
	}
	st, err := s.store.Get(ctx, p.OrgID)
	if err != nil {
		return err
	}
	if st.ProviderKey == name {
		return fmt.Errorf("%w: the agent uses %q. Choose another key for the agent first", aisettings.ErrInUse, name)
	}
	return s.ai.DeleteProviderKey(ctx, p, name)
}

// Status is whether the agent is on, for any member. agent:use.
func (s *Service) Status(ctx context.Context, p security.Principal) (*Status, error) {
	if err := s.authz.Require(ctx, p, security.AgentUse, ""); err != nil {
		return nil, err
	}
	st, err := s.store.Get(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	_, _, _, knowledge := s.bound()
	return &Status{Server: s.cfg.Enabled, Enabled: st.Enabled, Knowledge: st.Enabled && st.Knowledge && knowledge.Available}, nil
}

// Effective is what the agent runs with in the workspace, or ErrDisabled
// when it's off.
func (s *Service) Effective(ctx context.Context, orgID string) (*Effective, error) {
	st, err := s.store.Get(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if !st.Enabled {
		return nil, ErrDisabled
	}
	r, err := s.resolve(ctx, st)
	if err != nil {
		return nil, err
	}
	budget, err := s.allocation(ctx, orgID)
	if err != nil {
		return nil, err
	}
	_, _, _, knowledge := s.bound()
	return &Effective{
		OrgID: orgID, Provider: r.provider, ProviderKey: st.ProviderKey, Model: r.model, SummaryModel: r.summary,
		BaseURL: r.baseURL, NexusTenantID: st.NexusTenantID, MonthlyBudgetUSD: budget, Enabled: true,
		Knowledge: st.Knowledge && knowledge.Available,
	}, nil
}

// Enabled reports whether the agent is on in the workspace.
func (s *Service) Enabled(ctx context.Context, orgID string) (bool, error) {
	st, err := s.store.Get(ctx, orgID)
	if err != nil {
		return false, err
	}
	return st.Enabled, nil
}

// NexusTenant is the workspace's nexus tenant, or ErrDisabled while the
// agent is off.
func (s *Service) NexusTenant(ctx context.Context, orgID string) (string, error) {
	st, err := s.store.Get(ctx, orgID)
	if err != nil {
		return "", err
	}
	if !st.Enabled || st.NexusTenantID == "" {
		return "", ErrDisabled
	}
	return st.NexusTenantID, nil
}

// usdPattern fits NUMERIC(12, 2), as aisettings' does.
var usdPattern = regexp.MustCompile(`^(0|[1-9]\d{0,9})(\.\d{1,2})?$`)

// toCents parses a USD amount with up to two places, "0" or more.
func toCents(v string) (int64, error) {
	if !usdPattern.MatchString(v) {
		return 0, errors.New("not an amount in USD")
	}
	whole, frac, _ := strings.Cut(v, ".")
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, errors.New("not an amount in USD")
	}
	f, err := strconv.ParseInt((frac + "00")[:2], 10, 64)
	if err != nil {
		return 0, errors.New("not an amount in USD")
	}
	return w*100 + f, nil
}

func fromCents(c int64) string { return fmt.Sprintf("%d.%02d", c/100, c%100) }
