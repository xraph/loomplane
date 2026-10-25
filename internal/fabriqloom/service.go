package fabriqloom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// keysReportUseVersion is Plan 2a's migration that adds
// fabriq_api_key.last_used_at. Without it, Fabriq lists no key's last use,
// used or not.
const keysReportUseVersion = "202610210035"

// Errors the panel's service returns.
var (
	// ErrInvalid is a request Loomplane refuses. Its message, which says
	// why, is the whole of err.Error().
	ErrInvalid = errors.New("invalid Fabriq request")
	// ErrNotFound is a composed resource that isn't a Fabriq.
	ErrNotFound = errors.New("no such Fabriq")
	// ErrUnavailable is a Fabriq whose console can't be reached, or whose
	// admin API failed. Errors that are it are *UnavailableError.
	ErrUnavailable = errors.New("Fabriq isn't reachable right now") //nolint:staticcheck // ST1005: a sentence people read, and Fabriq is a name
)

// refusal is an error whose message is all there is to say, and which is
// one of the sentinels above.
type refusal struct {
	msg  string
	kind error
}

func (r *refusal) Error() string        { return r.msg }
func (r *refusal) Is(target error) bool { return target == r.kind }

func invalid(format string, args ...any) error {
	return &refusal{msg: fmt.Sprintf(format, args...), kind: ErrInvalid}
}

// UnavailableError is a Fabriq that can't be reached right now. Reason
// says why, and never carries a key, a secret or a DSN.
type UnavailableError struct{ Reason string }

func (e *UnavailableError) Error() string        { return ErrUnavailable.Error() + ": " + e.Reason }
func (e *UnavailableError) Is(target error) bool { return target == ErrUnavailable }

// secretShapes are what a reason must never show: a Fabriq key, and a
// DSN with whatever credential it carries.
var secretShapes = regexp.MustCompile(`fq_[0-9A-Za-z_]+|[a-z][a-z0-9+.-]*://[^\s"']*@[^\s"']*`)

func scrub(text string) string { return secretShapes.ReplaceAllString(text, "[redacted]") }

// unavailable turns a channel's or the admin API's failure into an
// *UnavailableError. A refused permission stays what it is.
func unavailable(err error) error {
	if err == nil || errors.Is(err, ErrUnavailable) || security.StatusFor(err) != http.StatusInternalServerError {
		return err
	}
	return &UnavailableError{Reason: scrub(err.Error())}
}

// Catalog is how the panel's service reaches a hosted Fabriq.
// *deployments.CatalogAccess is the real one: Components and Channel check
// the principal's permission on the project first.
type Catalog interface {
	Components(ctx context.Context, p security.Principal, need security.Permission, ref deployments.ResourceRef) ([]*planning.Binding, error)
	Channel(ctx context.Context, p security.Principal, need security.Permission, ref deployments.ComponentRef) (*deployments.ComponentChannel, error)
	RunJob(ctx context.Context, orgID string, ref deployments.ResourceRef, component, name string) (owned.JobResult, error)
}

var _ Catalog = (*deployments.CatalogAccess)(nil)

// Manifests reads a project's current manifest without checking a role:
// the service has checked one already. *manifests.Service is the real one.
type Manifests interface {
	Latest(ctx context.Context, orgID, projectID string) (*manifests.Revision, error)
}

// Service is what the dashboard's Fabriq panel asks: the stack's status,
// its tenants, each consuming service's key, and projection rebuilds.
type Service struct {
	Access  Catalog
	Keys    KeyStore
	Secrets deployments.ResourceSecrets
	// RebuildStore keeps the rebuilds the panel requests.
	RebuildStore RebuildStore
	Manifests    Manifests
	// Enqueue hands a queued rebuild to the worker that runs RunRebuild.
	Enqueue func(ctx context.Context, orgID, id string) error
	// Warn logs what failed without failing anything. It may be nil.
	Warn func(msg string, err error)
	// Now is the clock. Nil is time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) warn(msg string, err error) {
	if s.Warn != nil {
		s.Warn(msg, err)
	}
}

// hooks are the fabriq kind's hooks over the service's stores, for the
// key changes the panel and releases share.
func (s *Service) hooks() *Hooks {
	return NewHooks(HookDeps{Keys: s.Keys, Secrets: s.Secrets, Warn: s.Warn, Now: s.Now})
}

// Status is what the panel shows of a hosted Fabriq's stack. When its
// console can't be reached, Reachable is false, Reason says why, and the
// counts are zero.
type Status struct {
	Kind      string `json:"kind"`
	Version   string `json:"version"`
	Reachable bool   `json:"reachable"`
	Reason    string `json:"reason,omitempty"`
	// EntityTypes counts the first tenant's entity types: 0 with no
	// tenants.
	EntityTypes int `json:"entity_types"`
	// Migrations counts the migrations applied in Fabriq's own group, when
	// MigrationsKnown: a server that doesn't report them leaves it false.
	Migrations      int  `json:"migrations"`
	MigrationsKnown bool `json:"migrations_known"`
	// KeysReportUse says Fabriq records each key's last use, so a key it
	// lists without one has never been used.
	KeysReportUse bool `json:"keys_report_use"`
}

// ConsumerKey is the key one consuming service holds, as the panel lists
// it. State is "active", "rotating" (a rotated key waits for the service's
// next release), "revoked" or "unreleased" (the service needs Fabriq and
// hasn't been released with a key yet).
type ConsumerKey struct {
	Service string `json:"service" forge:"id"`
	Tenant  string `json:"tenant"`
	// Prefix is Fabriq's prefix of the key, never the key.
	Prefix     string     `json:"prefix,omitempty"`
	State      string     `json:"state"`
	CreatedAt  *time.Time `json:"created_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	// LastUsedKnown is Status.KeysReportUse: without it, a key with no
	// LastUsedAt may have been used.
	LastUsedKnown bool `json:"last_used_known"`
}

// The kinds the panel serves.
var fabriqKinds = []string{"fabriq", "fabriq-library"}

// composition checks p's permission and that ref is a hosted Fabriq in its
// project and environment, and returns its composition and its bindings:
// the resource's own first, then its components'.
func (s *Service) composition(ctx context.Context, p security.Principal, need security.Permission, ref deployments.ResourceRef) (*planning.CompositionConfig, []*planning.Binding, error) {
	parts, err := s.Access.Components(ctx, p, need, ref)
	if err != nil {
		return nil, nil, err
	}
	if len(parts) == 0 {
		return nil, nil, &refusal{msg: fmt.Sprintf("%s isn't a Fabriq in this environment", ref.Resource), kind: ErrNotFound}
	}
	var cfg planning.ResourceConfig
	if json.Unmarshal(parts[0].Config, &cfg) != nil || cfg.Composition == nil || !slices.Contains(fabriqKinds, cfg.Composition.Kind) {
		return nil, nil, &refusal{msg: fmt.Sprintf("%s isn't a Fabriq in this environment", ref.Resource), kind: ErrNotFound}
	}
	return cfg.Composition, parts, nil
}

// hosted rejects tenant and key operations for a library after checking scope.
func (s *Service) hosted(ctx context.Context, p security.Principal, need security.Permission, ref deployments.ResourceRef) error {
	comp, _, err := s.composition(ctx, p, need, ref)
	if err != nil {
		return err
	}
	if comp.Kind == "fabriq-library" {
		return invalid("fabriq-library has no tenants or keys")
	}
	return nil
}

// console opens the admin API through the console, as p. Close it with
// the function returned.
func (s *Service) console(ctx context.Context, p security.Principal, need security.Permission, ref deployments.ResourceRef) (*AdminClient, func(), error) {
	ch, err := s.Access.Channel(ctx, p, need, deployments.ComponentRef{ResourceRef: ref, Component: consoleComponent, Port: consolePort})
	if err != nil {
		return nil, nil, unavailable(err)
	}
	return &AdminClient{Channel: ch}, func() { _ = ch.Close() }, nil //nolint:errcheck // nothing to do about it
}

// Status reports a hosted Fabriq's stack. It answers when Fabriq doesn't,
// so the panel can show the stack from its bindings and say why.
func (s *Service) Status(ctx context.Context, p security.Principal, ref deployments.ResourceRef) (*Status, error) {
	comp, _, err := s.composition(ctx, p, security.DeploymentRead, ref)
	if err != nil {
		return nil, err
	}
	st := &Status{Kind: comp.Kind, Version: comp.Version}
	if comp.Kind == "fabriq-library" {
		err = s.readWorkerStatus(ctx, p, ref, st)
	} else {
		err = s.readStatus(ctx, p, ref, st)
	}
	if err != nil {
		var down *UnavailableError
		if !errors.As(err, &down) {
			return nil, err
		}
		*st = Status{Kind: comp.Kind, Version: comp.Version, Reason: down.Reason}
	}
	return st, nil
}

func (s *Service) readWorkerStatus(ctx context.Context, p security.Principal, ref deployments.ResourceRef, st *Status) error {
	ch, err := s.Access.Channel(ctx, p, security.DeploymentRead, deployments.ComponentRef{ResourceRef: ref, Component: "worker", Port: "api"})
	if err != nil {
		return unavailable(err)
	}
	defer func() { _ = ch.Close() }() //nolint:errcheck // the completed read has no remaining work to retry
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ch.BaseURL.String()+"/_/readyz", http.NoBody)
	if err != nil {
		return unavailable(err)
	}
	res, err := ch.Client.Do(req)
	if err != nil {
		return unavailable(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return unavailable(fmt.Errorf("fabriq worker readiness returned HTTP %d", res.StatusCode))
	}
	st.Reachable = true
	return nil
}

func (s *Service) readStatus(ctx context.Context, p security.Principal, ref deployments.ResourceRef, st *Status) error {
	admin, done, err := s.console(ctx, p, security.DeploymentRead, ref)
	if err != nil {
		return err
	}
	defer done()
	tenant, versions, known, err := appliedMigrations(ctx, admin)
	if err != nil {
		return err
	}
	st.Reachable = true
	st.Migrations, st.MigrationsKnown = len(versions), known
	st.KeysReportUse = slices.Contains(versions, keysReportUseVersion)
	if tenant == "" {
		return nil
	}
	n, err := admin.EntityTypeCount(ctx, tenant)
	switch {
	case errors.Is(err, ErrNotReported):
	case err != nil:
		return unavailable(err)
	default:
		st.EntityTypes = n
	}
	return nil
}

// appliedMigrations is the first tenant, or "" with none, and the versions
// applied in Fabriq's own migration group, asked with that tenant.
func appliedMigrations(ctx context.Context, admin *AdminClient) (tenant string, versions []string, known bool, err error) {
	tenants, err := admin.Tenants(ctx)
	if err != nil {
		return "", nil, false, unavailable(err)
	}
	if len(tenants) > 0 {
		tenant = tenants[0].ID
	}
	versions, known, err = admin.AppliedMigrations(ctx, tenant)
	if err != nil {
		return "", nil, false, unavailable(err)
	}
	return tenant, versions, known, nil
}

// Tenants lists Fabriq's tenants, by id.
func (s *Service) Tenants(ctx context.Context, p security.Principal, ref deployments.ResourceRef) ([]Tenant, error) {
	if err := s.hosted(ctx, p, security.DeploymentRead, ref); err != nil {
		return nil, err
	}
	admin, done, err := s.console(ctx, p, security.DeploymentRead, ref)
	if err != nil {
		return nil, err
	}
	defer done()
	tenants, err := admin.Tenants(ctx)
	if err != nil {
		return nil, unavailable(err)
	}
	return tenants, nil
}

// CreateTenant creates tenant id, or finds it: created says which. The
// panel's tenant ids are stricter than Fabriq's (ValidTenant).
func (s *Service) CreateTenant(ctx context.Context, p security.Principal, ref deployments.ResourceRef, id string) (Tenant, bool, error) {
	if err := s.hosted(ctx, p, security.DeploymentCreate, ref); err != nil {
		return Tenant{}, false, err
	}
	if !ValidTenant(id) {
		return Tenant{}, false, invalid("Use lowercase letters, digits and dashes.")
	}
	admin, done, err := s.console(ctx, p, security.DeploymentCreate, ref)
	if err != nil {
		return Tenant{}, false, err
	}
	defer done()
	tenant, created, err := admin.EnsureTenant(ctx, id)
	if err != nil {
		return Tenant{}, false, unavailable(err)
	}
	return tenant, created, nil
}

// Consumers lists one key per service that needs the resource in the
// project's current manifest or has a key record, by service.
func (s *Service) Consumers(ctx context.Context, p security.Principal, ref deployments.ResourceRef) ([]ConsumerKey, error) {
	if err := s.hosted(ctx, p, security.DeploymentRead, ref); err != nil {
		return nil, err
	}
	admin, done, err := s.console(ctx, p, security.DeploymentRead, ref)
	if err != nil {
		return nil, err
	}
	defer done()
	_, versions, _, err := appliedMigrations(ctx, admin)
	if err != nil {
		return nil, err
	}
	listed, err := admin.Keys(ctx)
	if err != nil {
		return nil, unavailable(err)
	}
	records, err := s.Keys.ForResource(ctx, p.OrgID, ref.EnvironmentID, ref.Resource)
	if err != nil {
		return nil, err
	}
	needs, err := s.needs(ctx, p.OrgID, ref)
	if err != nil {
		return nil, err
	}
	lastUsed := map[string]*time.Time{}
	for _, k := range listed {
		lastUsed[k.ID] = k.LastUsedAt
	}
	reportsUse := slices.Contains(versions, keysReportUseVersion)
	byService := map[string][]*KeyRecord{}
	for _, r := range records {
		byService[r.Service] = append(byService[r.Service], r)
	}
	services := make([]string, 0, len(needs)+len(byService))
	for service := range needs {
		services = append(services, service)
	}
	for service := range byService {
		if _, ok := needs[service]; !ok {
			services = append(services, service)
		}
	}
	slices.Sort(services)
	out := make([]ConsumerKey, 0, len(services))
	for _, service := range services {
		k := consumerKey(service, needs[service], byService[service], lastUsed)
		k.LastUsedKnown = reportsUse
		out = append(out, k)
	}
	return out, nil
}

// needs are the services that need ref in the project's current manifest,
// with the default tenant each passes it.
func (s *Service) needs(ctx context.Context, orgID string, ref deployments.ResourceRef) (map[string]string, error) {
	out := map[string]string{}
	if s.Manifests == nil {
		return out, nil
	}
	rev, err := s.Manifests.Latest(ctx, orgID, ref.ProjectID)
	if errors.Is(err, manifests.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if rev == nil || rev.Manifest == nil {
		return out, nil
	}
	for name, svc := range rev.Manifest.Services {
		if slices.Contains(svc.Needs, ref.Resource) {
			out[name] = svc.NeedOptions[ref.Resource]["tenant"]
		}
	}
	return out, nil
}

// consumerKey is the key a service holds, from its records, newest first:
// the newest one that isn't revoked, else the newest revoked one. Without
// either it is unreleased, with the manifest's default tenant. A rotating
// service's last use is its active key's: the rotated one waits for a
// release.
func consumerKey(service, tenant string, records []*KeyRecord, lastUsed map[string]*time.Time) ConsumerKey {
	k := ConsumerKey{Service: service, Tenant: tenant, State: "unreleased"}
	var live []*KeyRecord
	for _, r := range records {
		if r.State != KeyRevoked {
			live = append(live, r)
		}
	}
	var chosen *KeyRecord
	switch {
	case len(live) > 0:
		chosen, k.State = live[0], "active"
		if inState(live, KeyPending) != nil {
			k.State = "rotating"
		}
		for _, r := range live {
			if at := lastUsed[r.ID]; at != nil && (k.LastUsedAt == nil || at.After(*k.LastUsedAt)) {
				k.LastUsedAt = at
			}
		}
	case len(records) > 0:
		chosen, k.State = records[0], "revoked"
		k.RevokedAt, k.LastUsedAt = chosen.RevokedAt, lastUsed[chosen.ID]
	default:
		return k
	}
	created := chosen.CreatedAt
	k.Tenant, k.Prefix, k.CreatedAt = chosen.Tenant, chosen.Prefix, &created
	return k
}

// Rotate issues service a new key now, with its active key's tenant, and
// keeps it pending: the service's next release promotes it (BindConsumer)
// and then retires the old one. While a rotated key is pending, Rotate
// returns it again.
func (s *Service) Rotate(ctx context.Context, p security.Principal, ref deployments.ResourceRef, service string) (*ConsumerKey, error) {
	if err := s.hosted(ctx, p, security.SecretManage, ref); err != nil {
		return nil, err
	}
	unlock, err := s.Keys.LockService(ctx, p.OrgID, ref.EnvironmentID, ref.Resource, service)
	if err != nil {
		return nil, err
	}
	defer unlock()
	records, err := s.Keys.ForService(ctx, p.OrgID, ref.EnvironmentID, ref.Resource, service)
	if err != nil {
		return nil, err
	}
	if inState(records, KeyPending) != nil {
		return s.keyOf(records, service), nil
	}
	active := inState(records, KeyActive)
	if active == nil {
		return nil, invalid("%s has no key to rotate yet. Its next release issues one.", service)
	}
	admin, done, err := s.console(ctx, p, security.SecretManage, ref)
	if err != nil {
		return nil, err
	}
	defer done()
	consumer := deployments.Consumer{OrgID: p.OrgID, ProjectID: ref.ProjectID, EnvironmentID: ref.EnvironmentID, Resource: ref.Resource, Service: service}
	if err = s.hooks().revokeOrphans(ctx, admin, consumer); err != nil {
		return nil, unavailable(err)
	}
	issued, err := admin.IssueKey(ctx, KeyLabel(service))
	if err != nil {
		return nil, unavailable(err)
	}
	h := s.hooks()
	if err = s.Secrets.Put(ctx, VaultKey(p.OrgID, ref.EnvironmentID, ref.Resource, issued.ID), []byte(issued.Key)); err != nil {
		h.discard(ctx, admin, consumer, issued.ID)
		return nil, fmt.Errorf("keep %s's rotated Fabriq key: %w", service, err)
	}
	now := s.now().UTC()
	err = s.Keys.Insert(ctx, &KeyRecord{ID: issued.ID, OrgID: p.OrgID, ProjectID: ref.ProjectID, EnvironmentID: ref.EnvironmentID, Resource: ref.Resource,
		Service: service, Tenant: active.Tenant, Prefix: issued.Prefix, State: KeyPending, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		h.discard(ctx, admin, consumer, issued.ID)
		if !errors.Is(err, ErrConflict) {
			return nil, fmt.Errorf("record %s's rotated Fabriq key: %w", service, err)
		}
		// Another rotation recorded its key first: that one stands.
	}
	if records, err = s.Keys.ForService(ctx, p.OrgID, ref.EnvironmentID, ref.Resource, service); err != nil {
		return nil, err
	}
	return s.keyOf(records, service), nil
}

// Revoke revokes service's keys in Fabriq now, every one Fabriq still
// accepts (active, pending and retiring), records them revoked and forgets
// them. The service's need still stands, so its next release gets a new
// key.
func (s *Service) Revoke(ctx context.Context, p security.Principal, ref deployments.ResourceRef, service string) (*ConsumerKey, error) {
	if err := s.hosted(ctx, p, security.SecretManage, ref); err != nil {
		return nil, err
	}
	unlock, err := s.Keys.LockService(ctx, p.OrgID, ref.EnvironmentID, ref.Resource, service)
	if err != nil {
		return nil, err
	}
	defer unlock()
	records, err := s.Keys.ForService(ctx, p.OrgID, ref.EnvironmentID, ref.Resource, service)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, invalid("%s has no Fabriq key to revoke.", service)
	}
	var live []*KeyRecord
	for _, r := range records {
		if r.State != KeyRevoked {
			live = append(live, r)
		}
	}
	{
		admin, done, cerr := s.console(ctx, p, security.SecretManage, ref)
		if cerr != nil {
			return nil, cerr
		}
		defer done()
		h := s.hooks()
		consumer := deployments.Consumer{OrgID: p.OrgID, ProjectID: ref.ProjectID, EnvironmentID: ref.EnvironmentID, Resource: ref.Resource, Service: service}
		if err = h.revokeOrphans(ctx, admin, consumer); err != nil {
			return nil, unavailable(err)
		}
		var errs []error
		for _, r := range live {
			// A key Fabriq still accepts is never recorded revoked.
			if rerr := admin.RevokeKey(ctx, r.ID); rerr != nil {
				errs = append(errs, unavailable(rerr))
				continue
			}
			errs = append(errs, h.forget(ctx, p.OrgID, ref.EnvironmentID, ref.Resource, r))
		}
		if err = errors.Join(errs...); err != nil {
			var down *UnavailableError
			if errors.As(err, &down) {
				return nil, down
			}
			return nil, err
		}
		if records, err = s.Keys.ForService(ctx, p.OrgID, ref.EnvironmentID, ref.Resource, service); err != nil {
			return nil, err
		}
	}
	return s.keyOf(records, service), nil
}

// keyOf is service's ConsumerKey from its records, without Fabriq's word
// on last use: the panel lists keys again after a change.
func (s *Service) keyOf(records []*KeyRecord, service string) *ConsumerKey {
	k := consumerKey(service, "", records, nil)
	return &k
}
