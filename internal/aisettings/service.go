package aisettings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// Errors the Service returns, besides the Store's. Test with errors.Is.
var (
	ErrInvalid = errors.New("invalid workspace AI setting")
	ErrInUse   = errors.New("the provider key is in use")
)

// Embedding dimensions a provider model may have.
const (
	minEmbeddingDimensions = 64
	maxEmbeddingDimensions = 4096
)

// minPrimaryLength is the shortest required primary secret accepted, in
// characters. Its last four become the hint, so a shorter one would show
// most of itself.
const minPrimaryLength = 8

// keyHint returns the last four characters of a primary secret, and whether
// the secret is long enough to show them. A shorter one, which only an
// optional secret can be (vLLM's "EMPTY"), gets no hint at all.
func keyHint(secret string) (hint string, long bool) {
	r := []rune(secret)
	if len(r) < minPrimaryLength {
		return "", false
	}
	return string(r[len(r)-4:]), true
}

var (
	namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	// usdPattern fits NUMERIC(12, 2): up to ten whole digits and two places.
	usdPattern = regexp.MustCompile(`^(0|[1-9]\d{0,9})(\.\d{1,2})?$`)
	// pricePattern is a price per million tokens, which can be fractions of a cent.
	pricePattern = regexp.MustCompile(`^(0|[1-9]\d{0,11})(\.\d{1,6})?$`)
)

// Authorizer decides whether a principal may act. *security.Authorizer is
// the real one.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// Auditor records changes. projects.Auditor, as used across Loomplane.
type Auditor = projects.Auditor

// Projects finds a workspace's projects, for the projects a key is granted
// to. *projects.PgStore is the real one.
type Projects interface {
	GetProject(ctx context.Context, orgID, id string) (*projects.Project, error)
}

// ChangeNotifier is told when what a workspace's gateways run with changes.
// aigateway's is the real one. projects nil means every project.
type ChangeNotifier interface {
	ProviderKeysChanged(ctx context.Context, orgID string, projects []string)
	EmbeddingsChanged(ctx context.Context, orgID string)
}

// View is a workspace's AI settings as the dashboard shows them: no secret
// field of any provider key, ever.
type View struct {
	Settings
	ProviderKeys   []*ProviderKey `json:"provider_keys"`
	Allocations    []*Allocation  `json:"allocations"`
	AllocatedUSD   string         `json:"allocated_usd"`
	UnallocatedUSD string         `json:"unallocated_usd"`
	CanManage      bool           `json:"can_manage"`
}

// UpdateInput changes a workspace's settings. A nil field keeps its value.
type UpdateInput struct {
	MonthlyBudgetUSD *string     `json:"monthly_budget_usd,omitempty"`
	Embeddings       *Embeddings `json:"embeddings,omitempty"`
	Revision         int64       `json:"revision"`
}

// ProviderKeyInput creates or changes a provider key.
type ProviderKeyInput struct {
	Type     string             `json:"type"`               // required on create; can't change later
	Fields   SecretFields       `json:"fields,omitempty"`   // every field, secret ones included; a secret omitted keeps its value, "" is refused
	Projects *[]string          `json:"projects,omitempty"` // nil keeps; empty slice: every project
	Models   *[]OpenCompatModel `json:"models,omitempty"`
}

// GatewayKey is a provider key resolved for one project's gateway: values
// included. Never serialized.
type GatewayKey struct {
	Name, Type string
	Fields     SecretFields `json:"-"` // secret and non-secret together, as nexus config keys
	Models     []OpenCompatModel
	Revision   int64
}

// SecretFields are provider key fields with secret values among them. They
// print and log as field names only: fmt's verbs and slog never see a value.
type SecretFields map[string]string

const redacted = "[redacted]"

func (f SecretFields) names() []string { return slices.Sorted(maps.Keys(f)) }

// String implements fmt.Stringer.
func (f SecretFields) String() string { return fmt.Sprintf("%v (values redacted)", f.names()) }

// GoString implements fmt.GoStringer.
func (f SecretFields) GoString() string {
	parts := make([]string, 0, len(f))
	for _, n := range f.names() {
		parts = append(parts, strconv.Quote(n)+": "+strconv.Quote(redacted))
	}
	return "aisettings.SecretFields{" + strings.Join(parts, ", ") + "}"
}

// LogValue implements slog.LogValuer.
func (f SecretFields) LogValue() slog.Value {
	attrs := make([]slog.Attr, 0, len(f))
	for _, n := range f.names() {
		attrs = append(attrs, slog.String(n, redacted))
	}
	return slog.GroupValue(attrs...)
}

// String implements fmt.Stringer, without a single value.
func (k GatewayKey) String() string {
	return fmt.Sprintf("{Name:%s Type:%s Fields:%s Models:%d Revision:%d}", k.Name, k.Type, k.Fields, len(k.Models), k.Revision)
}

// GoString implements fmt.GoStringer, without a single value.
func (k GatewayKey) GoString() string {
	return fmt.Sprintf("aisettings.GatewayKey{Name:%q, Type:%q, Fields:%#v, Models:%d, Revision:%d}",
		k.Name, k.Type, k.Fields, len(k.Models), k.Revision)
}

// LogValue implements slog.LogValuer, without a single value.
func (k GatewayKey) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("name", k.Name), slog.String("type", k.Type),
		slog.Any("fields", k.Fields.names()), slog.Int64("revision", k.Revision),
	)
}

func (in ProviderKeyInput) projectNames() string {
	if in.Projects == nil {
		return "unchanged"
	}
	return fmt.Sprint(*in.Projects)
}

// String implements fmt.Stringer, without a single value.
func (in ProviderKeyInput) String() string {
	return fmt.Sprintf("{Type:%s Fields:%s Projects:%s}", in.Type, in.Fields, in.projectNames())
}

// GoString implements fmt.GoStringer, without a single value.
func (in ProviderKeyInput) GoString() string {
	return fmt.Sprintf("aisettings.ProviderKeyInput{Type:%q, Fields:%#v, Projects:%s}", in.Type, in.Fields, in.projectNames())
}

// LogValue implements slog.LogValuer, without a single value.
func (in ProviderKeyInput) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("type", in.Type), slog.Any("fields", in.Fields.names()), slog.String("projects", in.projectNames()),
	)
}

// Deps are what a Service needs. Notify may be nil, and bound later with
// SetNotifier.
type Deps struct {
	Store    Store
	Keys     Keys
	Authz    Authorizer // *security.Authorizer
	Audit    Auditor    // the projects.Auditor already used across Loomplane
	Projects Projects   // *projects.PgStore
	Notify   ChangeNotifier
	Now      func() time.Time // nil: time.Now
}

// Service is the workspace AI settings API. Every method that takes a
// principal checks it first; the ones that don't are for other services.
type Service struct {
	store    Store
	keys     Keys
	authz    Authorizer
	audit    Auditor
	projects Projects
	now      func() time.Time

	mu     sync.RWMutex
	notify ChangeNotifier
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		store: d.Store, keys: d.Keys, authz: d.Authz, audit: d.Audit, projects: d.Projects,
		notify: d.Notify, now: now,
	}
}

// SetNotifier binds the notifier, once aigateway is built. nil unbinds it.
func (s *Service) SetNotifier(n ChangeNotifier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notify = n
}

func (s *Service) notifier() ChangeNotifier {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.notify
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func (s *Service) record(ctx context.Context, p security.Principal, action, projectID string, attrs map[string]any) {
	if s.audit == nil {
		return
	}
	s.audit.Record(ctx, projects.AuditEvent{
		Action: action, Severity: "info", ProjectID: projectID, Principal: p, Attributes: attrs,
	})
}

// Get returns the workspace's settings, provider keys without their secret
// fields, and budget allocations. ai:read.
func (s *Service) Get(ctx context.Context, p security.Principal) (*View, error) {
	if err := s.authz.Require(ctx, p, security.AIRead, ""); err != nil {
		return nil, err
	}
	st, err := s.store.Settings(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	keys, err := s.store.ProviderKeys(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		k.Fields = publicFields(k.Type, k.Fields)
	}
	allocs, err := s.store.Allocations(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	var allocated int64
	for _, a := range allocs {
		c, cerr := toCents(a.MonthlyBudgetUSD)
		if cerr != nil {
			return nil, fmt.Errorf("allocation %q: %w", a.Scope, cerr)
		}
		allocated += c
	}
	budget, err := toCents(st.MonthlyBudgetUSD)
	if err != nil {
		return nil, fmt.Errorf("workspace AI budget: %w", err)
	}
	canManage := true
	if err := s.authz.Require(ctx, p, security.AIManage, ""); err != nil {
		if !errors.Is(err, security.ErrForbidden) {
			return nil, err
		}
		canManage = false
	}
	return &View{
		Settings:       *st,
		ProviderKeys:   keys,
		Allocations:    allocs,
		AllocatedUSD:   fromCents(allocated),
		UnallocatedUSD: fromCents(max(budget-allocated, 0)),
		CanManage:      canManage,
	}, nil
}

// publicFields drops any secret field a stored key's Fields might hold.
// The Service never stores one; this keeps a bad row from leaking one.
func publicFields(typ string, fields map[string]string) map[string]string {
	pt, ok := providerType(typ)
	if !ok || len(fields) == 0 {
		return nil
	}
	out := map[string]string{}
	for n, v := range fields {
		if f, ok := pt.field(n); ok && !f.Secret {
			out[n] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Update changes the workspace budget and embeddings choice. ai:manage.
// in.Revision is the revision the caller read; a newer one is ErrStale.
func (s *Service) Update(ctx context.Context, p security.Principal, in UpdateInput) (*View, error) {
	if err := s.authz.Require(ctx, p, security.AIManage, ""); err != nil {
		return nil, err
	}
	var budget string
	if in.MonthlyBudgetUSD != nil {
		c, err := toCents(*in.MonthlyBudgetUSD)
		if err != nil {
			return nil, invalid("the monthly AI budget must be an amount in USD, like 250 or 99.50")
		}
		budget = fromCents(c)
	}
	var embeddings *Embeddings
	if in.Embeddings != nil {
		e, err := s.checkEmbeddings(ctx, p.OrgID, *in.Embeddings)
		if err != nil {
			return nil, err
		}
		embeddings = &e
	}
	cur, err := s.store.Settings(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	if cur.Revision != in.Revision {
		return nil, ErrStale
	}

	// Two writes, each audited once it commits. SetBudget bumps the
	// revision, so the embeddings write that follows expects the revision
	// the budget write left.
	rev := cur.Revision
	if in.MonthlyBudgetUSD != nil {
		got, err := s.store.SetBudget(ctx, p.OrgID, budget, p.UserID)
		if errors.Is(err, ErrUnderAllocated) {
			return nil, s.namedAllocations(ctx, p.OrgID, err)
		}
		if err != nil {
			return nil, err
		}
		rev = got.Revision
		s.record(ctx, p, "ai.settings.update", "", map[string]any{"fields": []string{"monthly_budget_usd"}})
	}
	if embeddings != nil {
		next := *cur
		next.Embeddings, next.UpdatedBy = *embeddings, p.UserID
		if err := s.store.SaveSettings(ctx, &next, rev); err != nil {
			if in.MonthlyBudgetUSD != nil {
				return nil, fmt.Errorf("the monthly AI budget was saved as %s USD, but the embeddings weren't: %w", budget, err)
			}
			return nil, err
		}
		s.record(ctx, p, "ai.settings.update", "", map[string]any{"fields": []string{"embeddings"}})
		if n := s.notifier(); n != nil {
			n.EmbeddingsChanged(ctx, p.OrgID)
		}
	}
	return s.Get(ctx, p)
}

// namedAllocations adds the labels of the allocations that hold the budget
// up to the store's ErrUnderAllocated.
func (s *Service) namedAllocations(ctx context.Context, orgID string, cause error) error {
	allocs, err := s.store.Allocations(ctx, orgID)
	if err != nil || len(allocs) == 0 {
		return cause
	}
	labels := make([]string, 0, len(allocs))
	for _, a := range allocs {
		labels = append(labels, fmt.Sprintf("%s (%s USD)", a.Label, a.MonthlyBudgetUSD))
	}
	return fmt.Errorf("%w. Lower or release these first: %s", cause, strings.Join(labels, ", "))
}

// checkEmbeddings validates an embeddings choice and fills in what the mode
// decides.
func (s *Service) checkEmbeddings(ctx context.Context, orgID string, e Embeddings) (Embeddings, error) {
	switch e.Mode {
	case EmbeddingsLocal:
		return LocalEmbeddings(), nil
	case EmbeddingsProvider:
		if e.Provider == "" {
			return Embeddings{}, invalid("provider embeddings need a provider key")
		}
		k, err := s.store.ProviderKey(ctx, orgID, e.Provider)
		if errors.Is(err, ErrNotFound) {
			return Embeddings{}, invalid("there's no provider key called %q", e.Provider)
		}
		if err != nil {
			return Embeddings{}, err
		}
		pt, ok := providerType(k.Type)
		if !ok {
			return Embeddings{}, invalid("provider key %q has type %q, which Loomplane no longer offers", k.Name, k.Type)
		}
		if !pt.Embedding {
			return Embeddings{}, invalid("%s has no embeddings API", pt.Title)
		}
		if strings.TrimSpace(e.Model) == "" {
			return Embeddings{}, invalid("provider embeddings need a model")
		}
		if e.Dimensions < minEmbeddingDimensions || e.Dimensions > maxEmbeddingDimensions {
			return Embeddings{}, invalid("embedding dimensions must be between %d and %d", minEmbeddingDimensions, maxEmbeddingDimensions)
		}
		return Embeddings{Mode: EmbeddingsProvider, Provider: k.Name, Model: strings.TrimSpace(e.Model), Dimensions: e.Dimensions}, nil
	case EmbeddingsDeployment:
		return Embeddings{}, invalid("Model deployments arrive with the model registry.")
	default:
		return Embeddings{}, invalid("embeddings mode %q isn't local, provider or deployment", e.Mode)
	}
}

// PutProviderKey creates the provider key called name, or changes it.
// ai:manage. Secret fields go to Vault before the metadata is saved; the
// returned key, like every other answer, holds none of them.
func (s *Service) PutProviderKey(ctx context.Context, p security.Principal, name string, in ProviderKeyInput) (*ProviderKey, error) {
	if err := s.authz.Require(ctx, p, security.AIManage, ""); err != nil {
		return nil, err
	}
	if !namePattern.MatchString(name) {
		return nil, invalid("a provider key's name is up to 63 lowercase letters, digits and dashes, starting with a letter or digit")
	}
	old, err := s.store.ProviderKey(ctx, p.OrgID, name)
	switch {
	case errors.Is(err, ErrNotFound):
		old = nil
	case err != nil:
		return nil, err
	}

	typ := in.Type
	switch {
	case old != nil && typ != "" && typ != old.Type:
		return nil, invalid("provider key %q is a %s key, and its type can't change. Add a new key instead", name, old.Type)
	case old != nil:
		typ = old.Type
	case typ == "":
		return nil, invalid("a new provider key needs a type")
	}
	pt, ok := providerType(typ)
	if !ok {
		return nil, invalid("%q isn't a provider type Loomplane offers", typ)
	}

	fields, secrets, err := splitFields(pt, old, in.Fields)
	if err != nil {
		return nil, err
	}
	for _, f := range pt.Fields {
		if !f.Required {
			continue
		}
		_, given := secrets[f.Name]
		if (f.Secret && !given && old == nil) || (!f.Secret && fields[f.Name] == "") {
			return nil, invalid("%s keys need %s (%s)", pt.Title, f.Title, f.Name)
		}
	}
	hint := ""
	if old != nil {
		hint = old.KeyHint
	}
	if primary := pt.primary(); primary != "" {
		if v, ok := secrets[primary]; ok {
			f, _ := pt.field(primary)
			h, long := keyHint(v)
			if !long && f.Required {
				return nil, invalid("%s is too short to be a real key", primary)
			}
			hint = h
		}
	}

	var models []OpenCompatModel
	if old != nil {
		models = old.Models
	}
	if in.Models != nil {
		if !pt.Dynamic && len(*in.Models) > 0 {
			return nil, invalid("only OpenAI-compatible keys list their models; %s keys get theirs from the provider", pt.Title)
		}
		if pt.Dynamic {
			if merr := checkModels(*in.Models); merr != nil {
				return nil, merr
			}
			models = slices.Clone(*in.Models)
		}
	}
	if pt.Dynamic && len(models) == 0 {
		return nil, invalid("an OpenAI-compatible key needs models: at least one, with its prices")
	}

	reach := []string{}
	if old != nil {
		reach = old.Projects
	}
	if in.Projects != nil {
		if reach, err = s.checkProjects(ctx, p.OrgID, *in.Projects); err != nil {
			return nil, err
		}
	}

	id := ""
	if old != nil {
		id = old.ID
	} else {
		tid, err := typeid.Generate("aikey")
		if err != nil {
			return nil, fmt.Errorf("generate AI provider key id: %w", err)
		}
		id = tid.String()
	}
	if old == nil || len(secrets) > 0 {
		blob := secrets
		if old != nil {
			stored, err := s.keys.Reveal(ctx, p.OrgID, id)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return nil, fmt.Errorf("provider key %q: %w", name, err)
			}
			blob = stored
			if blob == nil {
				blob = map[string]string{}
			}
			maps.Copy(blob, secrets)
		}
		if err := s.keys.Put(ctx, p.OrgID, id, blob); err != nil {
			return nil, fmt.Errorf("provider key %q: %w", name, err)
		}
	}

	if len(fields) == 0 {
		fields = nil
	}
	k := &ProviderKey{
		ID: id, OrgID: p.OrgID, Name: name, Type: typ, Fields: fields, Models: models, KeyHint: hint,
		Projects: reach, CreatedBy: p.UserID, UpdatedBy: p.UserID,
	}
	if old == nil {
		// Insert only: an upsert would let a key created meanwhile under the
		// same name take this one's type and hint while Vault keeps the
		// other's values.
		if err := s.store.CreateProviderKey(ctx, k); err != nil {
			_ = s.keys.Delete(ctx, p.OrgID, id) //nolint:errcheck // best effort: nothing points at it
			if errors.Is(err, ErrStale) {
				return nil, fmt.Errorf("%w: someone else just added a provider key called %q. Reload and edit that one", ErrStale, name)
			}
			return nil, err
		}
	} else if err := s.store.SaveProviderKey(ctx, k); err != nil {
		return nil, err
	}
	k.Fields = publicFields(k.Type, k.Fields)

	if n := s.notifier(); n != nil {
		var before []string
		if old != nil {
			before = old.Projects
		}
		n.ProviderKeysChanged(ctx, p.OrgID, reached(old != nil, before, k.Projects))
	}
	changed := slices.Sorted(maps.Keys(in.Fields))
	if old == nil {
		changed = append(changed, "type", "projects")
	} else if in.Projects != nil {
		changed = append(changed, "projects")
	}
	if in.Models != nil {
		changed = append(changed, "models")
	}
	slices.Sort(changed)
	s.record(ctx, p, "ai.provider_key.put", "", map[string]any{
		"provider_key": name, "provider_type": typ, "fields": slices.Compact(changed),
	})
	return k, nil
}

// splitFields checks a key's input fields against its type, and returns
// the non-secret fields the key will have and the secret fields given.
// Input lists every field: a non-secret field left out is cleared, while a
// secret left out keeps its stored value. Nil input on an existing key
// keeps every field.
func splitFields(pt ProviderType, old *ProviderKey, in map[string]string) (fields, secrets map[string]string, err error) {
	fields, secrets = map[string]string{}, map[string]string{}
	if in == nil && old != nil {
		for n, v := range publicFields(pt.Type, old.Fields) {
			fields[n] = v
		}
		return fields, secrets, nil
	}
	for _, n := range slices.Sorted(maps.Keys(in)) {
		f, ok := pt.field(n)
		if !ok {
			return nil, nil, invalid("%s keys have no %q field", pt.Title, n)
		}
		v := in[n]
		if f.Secret {
			if v == "" {
				return nil, nil, invalid("%s can't be empty. Leave it out to keep the stored one", n)
			}
			secrets[n] = v
			continue
		}
		if v = strings.TrimSpace(v); v != "" {
			fields[n] = v
		}
	}
	if v, ok := fields["base_url"]; ok {
		u, err := url.Parse(v)
		// The URL isn't quoted back: it's the user's, and could hold anything.
		if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, nil, invalid("base_url must be an absolute http or https URL")
		}
		if u.User != nil {
			return nil, nil, invalid("base_url can't carry credentials. Put the key in api_key")
		}
	}
	return fields, secrets, nil
}

// checkModels validates an OpenAI-compatible key's models.
func checkModels(models []OpenCompatModel) error {
	if len(models) == 0 {
		return invalid("an OpenAI-compatible key needs models: at least one, with its prices")
	}
	seen := map[string]bool{}
	for _, m := range models {
		if strings.TrimSpace(m.ID) == "" {
			return invalid("every model needs an id")
		}
		if seen[m.ID] {
			return invalid("model %q is listed twice", m.ID)
		}
		seen[m.ID] = true
		if m.Task != "chat" && m.Task != "embedding" {
			return invalid("model %q: task must be chat or embedding", m.ID)
		}
		if m.ContextWindow < 0 || m.MaxOutput < 0 || m.Dimensions < 0 {
			return invalid("model %q: sizes can't be negative", m.ID)
		}
		if m.Task == "embedding" && (m.Dimensions < minEmbeddingDimensions || m.Dimensions > maxEmbeddingDimensions) {
			return invalid("model %q: embedding dimensions must be between %d and %d", m.ID, minEmbeddingDimensions, maxEmbeddingDimensions)
		}
		prices := []struct {
			name, value string
			required    bool
		}{
			{"input_per_million", m.InputPerMillion, true},
			{"output_per_million", m.OutputPerMillion, true},
			{"embedding_per_million", m.EmbeddingPerMillion, false},
		}
		for _, pr := range prices {
			if pr.value == "" && !pr.required {
				continue
			}
			if !pricePattern.MatchString(pr.value) {
				return invalid("model %q: %s must be a price in USD, like 0.15 (0 when it's free)", m.ID, pr.name)
			}
		}
	}
	return nil
}

// checkProjects returns the projects a key is granted to, sorted and once
// each, after checking each is one of the workspace's.
func (s *Service) checkProjects(ctx context.Context, orgID string, ids []string) ([]string, error) {
	out := slices.Compact(slices.Sorted(slices.Values(ids)))
	for _, id := range out {
		if id == "" {
			return nil, invalid("a project ID can't be empty")
		}
		if s.projects == nil {
			return nil, errors.New("aisettings: no project lookup configured")
		}
		_, err := s.projects.GetProject(ctx, orgID, id)
		if errors.Is(err, projects.ErrNotFound) {
			return nil, invalid("project %q isn't in this workspace", id)
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// reached is the projects a key change reaches, for ChangeNotifier: nil
// (every project) when the key reached or reaches every project.
func reached(existed bool, before, after []string) []string {
	if (existed && len(before) == 0) || len(after) == 0 {
		return nil
	}
	return slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(before), after...))))
}

// DeleteProviderKey deletes a provider key and its values. ai:manage.
// ErrInUse while the workspace's embeddings use it.
func (s *Service) DeleteProviderKey(ctx context.Context, p security.Principal, name string) error {
	if err := s.authz.Require(ctx, p, security.AIManage, ""); err != nil {
		return err
	}
	k, err := s.store.ProviderKey(ctx, p.OrgID, name)
	if err != nil {
		return err
	}
	st, err := s.store.Settings(ctx, p.OrgID)
	if err != nil {
		return err
	}
	if st.Embeddings.Mode == EmbeddingsProvider && st.Embeddings.Provider == name {
		return fmt.Errorf("%w: the workspace embeddings setting uses %q. Switch embeddings to another source first", ErrInUse, name)
	}
	if err := s.store.DeleteProviderKey(ctx, p.OrgID, name); err != nil {
		return err
	}
	if n := s.notifier(); n != nil {
		n.ProviderKeysChanged(ctx, p.OrgID, reached(true, k.Projects, k.Projects))
	}
	s.record(ctx, p, "ai.provider_key.delete", "", map[string]any{"provider_key": name, "provider_type": k.Type})
	if err := s.keys.Delete(ctx, p.OrgID, k.ID); err != nil {
		return fmt.Errorf("provider key %q is deleted, but its values are still in Vault: %w", name, err)
	}
	return nil
}

// Allocate sets one scope's share of the workspace budget, for a gateway in
// projectID. The caller picks need: ai:edit or ai:manage.
func (s *Service) Allocate(ctx context.Context, p security.Principal, need security.Permission, projectID string, a Allocation) error {
	if need != security.AIEdit && need != security.AIManage {
		return fmt.Errorf("aisettings: allocating takes ai:edit or ai:manage, not %s", need.Name())
	}
	if err := s.authz.Require(ctx, p, need, projectID); err != nil {
		return err
	}
	if strings.TrimSpace(a.Scope) == "" {
		return invalid("an allocation needs a scope")
	}
	if strings.TrimSpace(a.Label) == "" {
		return invalid("an allocation needs a label")
	}
	c, err := toCents(a.MonthlyBudgetUSD)
	if err != nil || c <= 0 {
		return invalid("an allocation must be an amount in USD above 0, like 25 or 12.50")
	}
	a.MonthlyBudgetUSD = fromCents(c)
	if a.ProjectID == "" {
		a.ProjectID = projectID
	}
	a.UpdatedAt = s.now().UTC()
	if err := s.store.SetAllocation(ctx, p.OrgID, &a); err != nil {
		return err
	}
	s.record(ctx, p, "ai.budget.allocate", projectID, map[string]any{
		"scope": a.Scope, "fields": []string{"label", "monthly_budget_usd", "scope"},
	})
	return nil
}

// ReleaseAllocation deletes one scope's allocation, when its gateway goes.
// Releasing one that isn't there is not an error.
func (s *Service) ReleaseAllocation(ctx context.Context, orgID, scope string) error {
	return s.store.DeleteAllocation(ctx, orgID, scope)
}

// Allocation returns one scope's allocation, or ErrNotFound.
func (s *Service) Allocation(ctx context.Context, orgID, scope string) (*Allocation, error) {
	allocs, err := s.store.Allocations(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for _, a := range allocs {
		if a.Scope == scope {
			return a, nil
		}
	}
	return nil, ErrNotFound
}

// grantedTo reports whether k reaches projectID.
func grantedTo(k *ProviderKey, projectID string) bool {
	return len(k.Projects) == 0 || slices.Contains(k.Projects, projectID)
}

// GatewayKeys returns the provider keys granted to projectID, by name, with
// their values. For the gateway's config only: never serialize them.
func (s *Service) GatewayKeys(ctx context.Context, orgID, projectID string) ([]GatewayKey, error) {
	keys, err := s.store.ProviderKeys(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := []GatewayKey{}
	for _, k := range keys {
		if !grantedTo(k, projectID) {
			continue
		}
		g, err := s.reveal(ctx, k)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, nil
}

// RevealKey returns one provider key with its values, or ErrNotFound.
func (s *Service) RevealKey(ctx context.Context, orgID, name string) (*GatewayKey, error) {
	k, err := s.store.ProviderKey(ctx, orgID, name)
	if err != nil {
		return nil, err
	}
	return s.reveal(ctx, k)
}

func (s *Service) reveal(ctx context.Context, k *ProviderKey) (*GatewayKey, error) {
	pt, ok := providerType(k.Type)
	if !ok {
		return nil, fmt.Errorf("provider key %q has type %q, which Loomplane no longer offers", k.Name, k.Type)
	}
	secrets, err := s.keys.Reveal(ctx, k.OrgID, k.ID)
	if err != nil {
		return nil, fmt.Errorf("provider key %q: %w", k.Name, err)
	}
	fields := publicFields(k.Type, k.Fields)
	if fields == nil {
		fields = map[string]string{}
	}
	for n, v := range secrets {
		if f, ok := pt.field(n); ok && f.Secret {
			fields[n] = v
		}
	}
	return &GatewayKey{Name: k.Name, Type: k.Type, Fields: fields, Models: slices.Clone(k.Models), Revision: k.Revision}, nil
}

// GatewayKeysDigest is "sha256:<hex>" over the static provider keys granted
// to projectID: their names, types, non-secret fields and revisions, in
// name order. A new secret bumps the revision, so it moves the digest
// without a byte of it going in. OpenAI-compatible keys are pushed live
// through the admin API, so they don't count.
func (s *Service) GatewayKeysDigest(ctx context.Context, orgID, projectID string) (string, error) {
	keys, err := s.store.ProviderKeys(ctx, orgID)
	if err != nil {
		return "", err
	}
	type entry struct {
		Name     string            `json:"name"`
		Type     string            `json:"type"`
		Fields   map[string]string `json:"fields"`
		Revision int64             `json:"revision"`
	}
	entries := []entry{}
	for _, k := range keys {
		if !grantedTo(k, projectID) {
			continue
		}
		if pt, ok := providerType(k.Type); ok && pt.Dynamic {
			continue
		}
		entries = append(entries, entry{Name: k.Name, Type: k.Type, Fields: publicFields(k.Type, k.Fields), Revision: k.Revision})
	}
	slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.Name, b.Name) })
	b, err := json.Marshal(entries) // maps marshal in key order
	if err != nil {
		return "", fmt.Errorf("digest provider keys: %w", err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// EmbeddingsFor returns the workspace's embeddings choice.
func (s *Service) EmbeddingsFor(ctx context.Context, orgID string) (Embeddings, error) {
	st, err := s.store.Settings(ctx, orgID)
	if err != nil {
		return Embeddings{}, err
	}
	return st.Embeddings, nil
}

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
	frac = (frac + "00")[:2]
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, errors.New("not an amount in USD")
	}
	return w*100 + f, nil
}

func fromCents(c int64) string { return fmt.Sprintf("%d.%02d", c/100, c%100) }
