package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

var admin = security.Principal{OrgID: "org", UserID: "admin"}

// fakeRegistrar quotes one price and counts every registration call.
type fakeRegistrar struct {
	provider.DomainRegistrar
	extensions         []string
	price              provider.DomainPrice
	noPrice            bool
	reason             string
	state              string
	expires            time.Time
	errorCode, message string
	failAfterPost      bool
	postErr            error
	noContext          bool
	getErr             error
	gets               int
	posts              int
	reconcileOnlyCalls int
	specs              []provider.DomainRegistrationSpec
	autoRenew          []bool
	mode               string
}

func (r *fakeRegistrar) RegistrarMode() string { return r.mode }

func (r *fakeRegistrar) RegistrationExtensions(context.Context, provider.InfrastructureCredential) ([]string, error) {
	return r.extensions, nil
}

func (r *fakeRegistrar) SearchDomains(_ context.Context, _ provider.InfrastructureCredential, q string, _ int) ([]provider.DomainOffer, error) {
	return r.offers([]string{q + ".dev"}), nil
}

func (r *fakeRegistrar) CheckDomains(_ context.Context, c provider.InfrastructureCredential, names []string) ([]provider.DomainOffer, error) {
	if c.AccountID != "account" || c.Token != "token" {
		return nil, provider.ErrInfrastructureIdentity
	}
	return r.offers(names), nil
}

func (r *fakeRegistrar) offers(names []string) []provider.DomainOffer {
	out := make([]provider.DomainOffer, 0, len(names))
	for _, name := range names {
		offer := provider.DomainOffer{Name: name, Registrable: r.reason == "", Reason: r.reason}
		if !r.noPrice {
			price := r.price
			offer.Price = &price
		}
		out = append(out, offer)
	}
	return out
}

func (r *fakeRegistrar) RegisterDomain(_ context.Context, _ provider.InfrastructureCredential, spec provider.DomainRegistrationSpec) (provider.DomainRegistrationResult, error) {
	if spec.ReconcileOnly {
		r.reconcileOnlyCalls++
		if r.failAfterPost {
			return provider.DomainRegistrationResult{}, fmt.Errorf("%w: status read failed", provider.ErrInfrastructureAmbiguous)
		}
		return r.result(spec.Name, true), nil
	}
	r.posts++
	r.specs = append(r.specs, spec)
	switch {
	case r.failAfterPost:
		return provider.DomainRegistrationResult{}, fmt.Errorf("%w: response lost", provider.ErrInfrastructureAmbiguous)
	case r.postErr != nil:
		return provider.DomainRegistrationResult{}, r.postErr
	}
	return r.result(spec.Name, false), nil
}

func (r *fakeRegistrar) result(name string, reconciled bool) provider.DomainRegistrationResult {
	state := r.state
	if state == "" {
		state = provider.RegistrationSucceeded
	}
	res := provider.DomainRegistrationResult{Name: name, State: state, Completed: state != provider.RegistrationInProgress, ErrorCode: r.errorCode, ErrorMessage: r.message, Reconciled: reconciled}
	if state == provider.RegistrationSucceeded && !r.noContext {
		res.ExpiresAt = r.expires
		res.AutoRenew = len(r.specs) > 0 && r.specs[len(r.specs)-1].AutoRenew
		res.PrivacyMode = "redaction"
	}
	return res
}

func (r *fakeRegistrar) GetRegisteredDomain(_ context.Context, _ provider.InfrastructureCredential, name string) (provider.RegisteredDomain, error) {
	r.gets++
	if r.getErr != nil {
		return provider.RegisteredDomain{}, r.getErr
	}
	autoRenew := len(r.specs) > 0 && r.specs[len(r.specs)-1].AutoRenew
	if len(r.autoRenew) > 0 {
		autoRenew = r.autoRenew[len(r.autoRenew)-1]
	}
	return provider.RegisteredDomain{Name: name, Status: "active", ExpiresAt: r.expires, AutoRenew: autoRenew}, nil
}

func (r *fakeRegistrar) SetAutoRenew(_ context.Context, _ provider.InfrastructureCredential, name string, enabled bool) (provider.RegisteredDomain, error) {
	r.autoRenew = append(r.autoRenew, enabled)
	return provider.RegisteredDomain{Name: name, AutoRenew: enabled, ExpiresAt: r.expires}, nil
}

// registrationDNS has no zone until CreateZone is called.
type registrationDNS struct {
	provider.DNSAdapter
	zone    *provider.DNSZone
	creates int
}

func (d *registrationDNS) GetZone(context.Context, provider.InfrastructureCredential, string) (provider.DNSZone, error) {
	if d.zone == nil {
		return provider.DNSZone{}, provider.ErrInfrastructureNotFound
	}
	return *d.zone, nil
}

func (d *registrationDNS) CreateZone(_ context.Context, _ provider.InfrastructureCredential, spec provider.DNSZoneSpec) (provider.DNSZone, error) {
	d.creates++
	d.zone = &provider.DNSZone{ID: "cf_zone", Name: spec.Name, AccountID: "account"}
	return *d.zone, nil
}

// registrationCredentials stores the registrar mode the last connection
// check recorded.
type registrationCredentials struct{ mode *string }

func (c registrationCredentials) Credential(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
	if org != "org" || id != "cf" {
		return nil, nil, connections.ErrNotFound
	}
	return &connections.Connection{ID: id, OrgID: org, Cloud: "cloudflare", Generation: 1, Details: map[string]string{"account_id": "account", "registrar_mode": *c.mode}}, []byte("token"), nil
}

type memorySecrets map[string][]byte

func (m memorySecrets) Put(_ context.Context, org, id string, value []byte) error {
	m[org+"/"+id] = append([]byte{}, value...)
	return nil
}
func (m memorySecrets) Reveal(_ context.Context, org, id string) ([]byte, error) {
	if v, ok := m[org+"/"+id]; ok {
		return v, nil
	}
	return nil, connections.ErrNotFound
}
func (m memorySecrets) Delete(_ context.Context, org, id string) error {
	delete(m, org+"/"+id)
	return nil
}

// memoryRegistrations keeps the JSON bodies the Postgres store would write.
type memoryRegistrations struct {
	bodies map[string][]byte
	order  []string
}

func (s *memoryRegistrations) CreateRegistration(_ context.Context, reg *Registration) error {
	raw, err := encodeRegistration(reg)
	if err != nil {
		return err
	}
	s.bodies[reg.ID] = raw
	s.order = append(s.order, reg.ID)
	return nil
}
func (s *memoryRegistrations) Registration(_ context.Context, org, id string) (*Registration, error) {
	raw, ok := s.bodies[id]
	if !ok {
		return nil, ErrNotFound
	}
	reg, err := decodeRegistration(raw)
	if err != nil || reg.OrgID != org {
		return nil, ErrNotFound
	}
	return reg, nil
}
func (s *memoryRegistrations) Registrations(ctx context.Context, org string) ([]*Registration, error) {
	out := []*Registration{}
	for _, id := range s.order {
		if reg, err := s.Registration(ctx, org, id); err == nil {
			out = append(out, reg)
		}
	}
	return out, nil
}
func (s *memoryRegistrations) SaveRegistration(_ context.Context, reg *Registration) error {
	current, err := decodeRegistration(s.bodies[reg.ID])
	if err != nil || current.Revision != reg.Revision {
		return ErrConflict
	}
	reg.Revision++
	raw, err := encodeRegistration(reg)
	if err != nil {
		reg.Revision--
		return err
	}
	s.bodies[reg.ID] = raw
	return nil
}
func (s *memoryRegistrations) PendingRegistrations(context.Context) ([]*Registration, error) {
	return s.withStatus(RegistrationPreview, RegistrationApproved, RegistrationRegistering, RegistrationZonePending)
}
func (s *memoryRegistrations) SucceededRegistrations(context.Context) ([]*Registration, error) {
	return s.withStatus(RegistrationSucceededStatus)
}
func (s *memoryRegistrations) UnnotifiedRegistrations(_ context.Context, since time.Time) ([]*Registration, error) {
	regs, err := s.withStatus(notifiedStatuses...)
	out := []*Registration{}
	for _, reg := range regs {
		if reg.ResultNotifiedFor != reg.Status && !reg.UpdatedAt.Before(since) {
			out = append(out, reg)
		}
	}
	return out, err
}
func (s *memoryRegistrations) withStatus(statuses ...string) ([]*Registration, error) {
	out := []*Registration{}
	for _, id := range s.order {
		reg, err := decodeRegistration(s.bodies[id])
		if err != nil {
			return nil, err
		}
		if slices.Contains(statuses, reg.Status) {
			out = append(out, reg)
		}
	}
	return out, nil
}

type registrationLocks struct{ Store }

func (registrationLocks) WithOperationLock(_ context.Context, _ string, run func() error) error {
	return run()
}

type registrationStarts struct {
	Workflow
	started []string
}

func (w *registrationStarts) StartRegistration(_ context.Context, org, id string, attempt int) error {
	w.started = append(w.started, fmt.Sprintf("%s/%s/%d", org, id, attempt))
	return nil
}

type registrationEnv struct {
	svc       *Service
	registrar *fakeRegistrar
	dns       *registrationDNS
	store     *memoryRegistrations
	domains   *overviewStore
	secrets   memorySecrets
	workflow  *registrationStarts
	now       time.Time
	// storedMode is the connection's registrar_mode detail.
	storedMode string
}

// sandbox points the registrar and the stored connection detail at the
// registrar sandbox.
func (env *registrationEnv) sandbox() {
	env.registrar.mode, env.storedMode = "sandbox", "sandbox"
}

func newRegistrationEnv(t *testing.T) *registrationEnv {
	t.Helper()
	env := &registrationEnv{
		registrar:  &fakeRegistrar{extensions: []string{"co.uk", "dev", "uk"}, price: provider.DomainPrice{Currency: "USD", RegistrationCost: "10.11", RenewalCost: "10.11"}, expires: time.Date(2027, 11, 5, 0, 0, 0, 0, time.UTC), mode: "production"},
		dns:        &registrationDNS{},
		store:      &memoryRegistrations{bodies: map[string][]byte{}},
		domains:    &overviewStore{projects: map[string]map[string]string{"org": {"shop": "Shop", "docs": "Docs"}}},
		secrets:    memorySecrets{},
		workflow:   &registrationStarts{},
		now:        time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
		storedMode: "production",
	}
	adapters := NewAdapters()
	if err := adapters.RegisterDNS("cloudflare", env.dns); err != nil {
		t.Fatal(err)
	}
	if err := adapters.RegisterRegistrar("cloudflare", env.registrar); err != nil {
		t.Fatal(err)
	}
	env.svc = NewService(Deps{Store: registrationLocks{}, Domains: env.domains, Registrations: env.store, Connections: registrationCredentials{mode: &env.storedMode}, Secrets: env.secrets, Adapters: adapters, Workflow: env.workflow,
		Authz: overviewAuth{
			"admin": {{security.OrganizationCloudManage, "org"}},
			"dev":   {{security.InfrastructureRead, "shop"}, {security.InfrastructureModify, "shop"}},
		}})
	env.svc.now = func() time.Time { return env.now }
	return env
}

func registrationFixture(t *testing.T) (*Service, *fakeRegistrar) {
	env := newRegistrationEnv(t)
	return env.svc, env.registrar
}

func approvedRegistration(t *testing.T, svc *Service) *Registration {
	t.Helper()
	op, err := svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.dev", ProjectIDs: []string{"shop"}})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := svc.ApproveRegistration(t.Context(), admin, op.ID, RegistrationApproval{Review: op.Review, Currency: op.Quote.Currency, RegistrationCost: op.Quote.RegistrationCost})
	if err != nil {
		t.Fatal(err)
	}
	return approved
}

func TestRegistrationRefusesChangedPrice(t *testing.T) {
	svc, reg := registrationFixture(t) // fixtureRegistrar quotes USD 10.11
	op, _ := svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.dev", ProjectIDs: []string{"shop"}})
	if _, err := svc.ApproveRegistration(t.Context(), admin, op.ID, RegistrationApproval{Review: op.Review, Currency: "USD", RegistrationCost: "10.11"}); err != nil {
		t.Fatal(err)
	}
	reg.price = provider.DomainPrice{Currency: "USD", RegistrationCost: "12.00"}
	_ = svc.ReconcileRegistration(t.Context(), "org", op.ID, nil)
	got, _ := svc.GetRegistration(t.Context(), admin, op.ID)
	if got.Status != RegistrationPriceChanged || reg.posts != 0 {
		t.Fatalf("%s posts=%d", got.Status, reg.posts)
	}
	// Nothing reconciles a price_changed registration into a purchase later.
	reg.price = provider.DomainPrice{Currency: "USD", RegistrationCost: "10.11"}
	_ = svc.ReconcileRegistration(t.Context(), "org", op.ID, nil)
	if reg.posts != 0 || !strings.Contains(got.Message, "USD 12.00") {
		t.Fatalf("posts=%d message=%q", reg.posts, got.Message)
	}
}

func TestRegistrationLostResponseNeverPostsTwice(t *testing.T) {
	svc, reg := registrationFixture(t)
	reg.failAfterPost = true // returns ErrInfrastructureAmbiguous after recording the POST
	op := approvedRegistration(t, svc)
	_ = svc.ReconcileRegistration(t.Context(), "org", op.ID, nil)
	reg.failAfterPost = false
	_ = svc.ReconcileRegistration(t.Context(), "org", op.ID, nil)
	if reg.posts != 1 || reg.reconcileOnlyCalls != 1 {
		t.Fatalf("posts=%d reconcile=%d", reg.posts, reg.reconcileOnlyCalls)
	}
}

func TestRegistrationPostRefusalEndsFailedAndAmbiguityReconciles(t *testing.T) {
	refused := errors.Join(&provider.InfrastructureScopeError{Provider: "cloudflare", Permission: "Registrar:Edit"}, &provider.InfrastructureAPIError{Provider: "cloudflare", Status: 402, Codes: []int{1234}})
	for _, tc := range []struct {
		name    string
		err     error
		message string
	}{
		{"declined", refused, "Cloudflare refused to register acme.dev (HTTP 402, Cloudflare code 1234). Nothing was bought."},
		{"not sent", provider.ErrInfrastructureInvalid, "Cloudflare refused to register acme.dev. Nothing was bought."},
	} {
		env := newRegistrationEnv(t)
		env.registrar.postErr = tc.err
		op := approvedRegistration(t, env.svc)
		if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err != nil {
			t.Fatal(tc.name, err)
		}
		got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID)
		if got.Status != RegistrationFailedStatus || !got.Attempted || !got.FirstUnconfirmedAt.IsZero() || got.Message != tc.message {
			t.Fatalf("%s: %+v", tc.name, got)
		}
		env.registrar.postErr = nil
		_ = env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil)
		if env.registrar.posts != 1 || env.registrar.reconcileOnlyCalls != 0 {
			t.Fatalf("%s: posts=%d reconcile=%d", tc.name, env.registrar.posts, env.registrar.reconcileOnlyCalls)
		}
	}

	// An uncertain answer keeps waiting and is only read back.
	env := newRegistrationEnv(t)
	env.registrar.postErr = errors.Join(provider.ErrInfrastructureAmbiguous, &provider.InfrastructureAPIError{Provider: "cloudflare", Status: 409})
	op := approvedRegistration(t, env.svc)
	if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); !errors.Is(err, ErrPending) {
		t.Fatal("ambiguous", err)
	}
	if got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID); got.Status != RegistrationRegistering || !got.Attempted || got.FirstUnconfirmedAt.IsZero() {
		t.Fatalf("ambiguous: %+v", got)
	}
	env.registrar.postErr = nil
	_ = env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil)
	if env.registrar.posts != 1 || env.registrar.reconcileOnlyCalls != 1 {
		t.Fatalf("posts=%d reconcile=%d", env.registrar.posts, env.registrar.reconcileOnlyCalls)
	}
}

func TestRegistrationReadsTheDomainWhenTheAnswerHasNoExpiry(t *testing.T) {
	env := newRegistrationEnv(t)
	env.registrar.noContext, env.registrar.getErr = true, errors.New("read failed")
	op := approvedRegistration(t, env.svc)
	if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err == nil {
		t.Fatal("stored a succeeded registration without its expiry", err)
	}
	if got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID); got.Status != RegistrationRegistering || !got.DomainExpiresAt.IsZero() {
		t.Fatalf("after a failed read: %+v", got)
	}
	env.registrar.getErr = nil
	if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID)
	if got.Status != RegistrationZonePending || !got.DomainExpiresAt.Equal(env.registrar.expires) || !got.AutoRenew || env.registrar.posts != 1 || len(env.registrar.autoRenew) != 0 {
		t.Fatalf("%+v posts=%d", got, env.registrar.posts)
	}
}

// flakyDomains fails project lookups until err is cleared.
type flakyDomains struct {
	*overviewStore
	err error
}

func (d *flakyDomains) DomainProject(ctx context.Context, org, project string) error {
	if d.err != nil {
		return d.err
	}
	return d.overviewStore.DomainProject(ctx, org, project)
}

func TestRegistrationKeepsGrantsThroughATransientProjectError(t *testing.T) {
	env := newRegistrationEnv(t)
	env.dns.zone = &provider.DNSZone{ID: "cf_zone", Name: "acme.dev", AccountID: "account", Verified: true}
	op, err := env.svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.dev", ProjectIDs: []string{"shop", "docs"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = env.svc.ApproveRegistration(t.Context(), admin, op.ID, RegistrationApproval{Review: op.Review, Currency: "USD", RegistrationCost: "10.11"}); err != nil {
		t.Fatal(err)
	}
	flaky := &flakyDomains{overviewStore: env.domains, err: errors.New("connection reset")}
	env.svc.Domains = flaky
	if err = env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err == nil {
		t.Fatal("a transient error was taken as a deleted project")
	}
	if got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID); got.Status != RegistrationZonePending || len(env.domains.zones) != 0 {
		t.Fatalf("after a transient error: %+v", got)
	}
	// Docs was deleted meanwhile; only it loses the grant.
	flaky.err = nil
	delete(env.domains.projects["org"], "docs")
	if err = env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	if len(env.domains.zones) != 1 || !slices.Equal(env.domains.zones[0].ProjectIDs, []string{"shop"}) {
		t.Fatalf("zones: %+v", env.domains.zones)
	}
}

func TestRegistrationUnconfirmedAfterAnHour(t *testing.T) {
	env := newRegistrationEnv(t)
	env.registrar.failAfterPost = true
	op := approvedRegistration(t, env.svc)
	for _, wait := range []time.Duration{0, 30 * time.Minute, 30 * time.Minute} {
		env.now = env.now.Add(wait)
		if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); !errors.Is(err, ErrPending) {
			t.Fatal(wait, err)
		}
		if got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID); got.Status != RegistrationRegistering {
			t.Fatalf("unconfirmed too soon: %+v", got)
		}
	}
	env.now = env.now.Add(time.Second)
	if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID)
	want := "Cloudflare has no record of this registration yet. Check the registrations page in your Cloudflare dashboard before trying again."
	if got.Status != RegistrationUnconfirmed || got.Message != want || env.registrar.posts != 1 || env.registrar.reconcileOnlyCalls != 3 {
		t.Fatalf("%+v posts=%d reconcile=%d", got, env.registrar.posts, env.registrar.reconcileOnlyCalls)
	}
}

func TestPreviewSandboxFollowsTheLiveRegistrar(t *testing.T) {
	env := newRegistrationEnv(t)
	env.sandbox()
	op, err := env.svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.dev"})
	if err != nil || !op.Sandbox {
		t.Fatal("sandbox preview", op, err)
	}

	// The connection was checked in the sandbox, but Loomplane now runs
	// against the production registrar.
	env = newRegistrationEnv(t)
	env.storedMode = "sandbox"
	if _, err = env.svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.dev"}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "Check the Cloudflare connection again: its registrar mode changed.") {
		t.Fatal("stale sandbox detail", err)
	}
	if len(env.store.bodies) != 0 {
		t.Fatal("a refused preview was stored")
	}

	// An adapter that can't say counts as production.
	if registrarSandbox(struct{ provider.DomainRegistrar }{env.registrar}) {
		t.Fatal("an adapter without a mode counted as the sandbox")
	}
}

func TestRegistrationRefusesAChangedRegistrarMode(t *testing.T) {
	for _, tc := range []struct{ previewed, live string }{{"sandbox", "production"}, {"production", "sandbox"}} {
		env := newRegistrationEnv(t)
		env.registrar.mode, env.storedMode = tc.previewed, tc.previewed
		op := approvedRegistration(t, env.svc)
		env.registrar.mode, env.storedMode = tc.live, tc.live
		if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err != nil {
			t.Fatal(tc.previewed, err)
		}
		got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID)
		if got.Status != RegistrationFailedStatus || got.Attempted || env.registrar.posts != 0 || !strings.Contains(got.Message, "Nothing was bought.") {
			t.Fatalf("%s to %s: %+v posts=%d", tc.previewed, tc.live, got, env.registrar.posts)
		}
	}
}

func TestApprovalNeedsExactQuoteAndOpenWindow(t *testing.T) {
	env := newRegistrationEnv(t)
	op, err := env.svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.dev"})
	if err != nil {
		t.Fatal(err)
	}
	if op.Review == "" || op.Quote.RegistrationCost != "10.11" || !op.ReviewExpiresAt.Equal(env.now.Add(15*time.Minute)) || op.Sandbox {
		t.Fatalf("preview: %+v", op)
	}
	for name, in := range map[string]RegistrationApproval{
		"wrong review":       {Review: "nope", Currency: "USD", RegistrationCost: "10.11"},
		"different cost":     {Review: op.Review, Currency: "USD", RegistrationCost: "10.12"},
		"different currency": {Review: op.Review, Currency: "EUR", RegistrationCost: "10.11"},
		"no cost":            {Review: op.Review, Currency: "USD"},
	} {
		if _, err = env.svc.ApproveRegistration(t.Context(), admin, op.ID, in); !errors.Is(err, ErrConflict) {
			t.Fatal(name, err)
		}
	}
	env.now = env.now.Add(15 * time.Minute)
	if _, err = env.svc.ApproveRegistration(t.Context(), admin, op.ID, RegistrationApproval{Review: op.Review, Currency: "USD", RegistrationCost: "10.11"}); !errors.Is(err, ErrConflict) {
		t.Fatal("expired window", err)
	}
	got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID)
	if got.Status != RegistrationPreview || got.Review != "" || env.registrar.posts != 0 || len(env.workflow.started) != 0 {
		t.Fatalf("%+v posts=%d started=%v", got, env.registrar.posts, env.workflow.started)
	}

	env.now = env.now.Add(-time.Minute)
	approved, err := env.svc.ApproveRegistration(t.Context(), admin, op.ID, RegistrationApproval{Review: op.Review, Currency: "USD", RegistrationCost: "10.11"})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != RegistrationApproved || approved.Attempt != 1 || approved.Review != "" || len(env.workflow.started) != 1 || env.workflow.started[0] != "org/"+op.ID+"/1" {
		t.Fatalf("%+v started=%v", approved, env.workflow.started)
	}
	if env.registrar.posts != 0 {
		t.Fatal("approval posted by itself")
	}
}

func TestPreviewRefusesUnsupportedTLDAndUnregistrable(t *testing.T) {
	env := newRegistrationEnv(t)
	for _, name := range []string{"acme.zz", "api.acme.dev", "*.acme.dev", "dev"} {
		if _, err := env.svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: name}); !errors.Is(err, ErrInvalid) {
			t.Fatal(name, err)
		}
	}
	// The longest listed suffix decides what is being registered.
	if op, err := env.svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "Acme.CO.uk"}); err != nil || op.Name != "acme.co.uk" {
		t.Fatal("co.uk", op, err)
	}
	if _, err := env.svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.dev", PrivacyMode: "public"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("privacy mode", err)
	}
	env.registrar.reason = "domain_unavailable"
	_, err := env.svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.dev"})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "domain_unavailable") {
		t.Fatal("unregistrable", err)
	}
	env.registrar.reason, env.registrar.noPrice = "", true
	if _, err = env.svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.dev"}); !errors.Is(err, ErrConflict) {
		t.Fatal("no price", err)
	}
	if len(env.store.bodies) != 1 {
		t.Fatalf("refused previews were saved: %d", len(env.store.bodies))
	}
}

func TestRegistrationDefaultsAutoRenewOn(t *testing.T) {
	svc, reg := registrationFixture(t)
	op := approvedRegistration(t, svc)
	if !op.AutoRenew || op.PrivacyMode != "redaction" {
		t.Fatalf("defaults: %+v", op)
	}
	_ = svc.ReconcileRegistration(t.Context(), "org", op.ID, nil)
	if len(reg.specs) != 1 || !reg.specs[0].AutoRenew || reg.specs[0].PrivacyMode != "redaction" || reg.specs[0].Name != "acme.dev" || reg.specs[0].OperationID != op.ID {
		t.Fatalf("spec: %+v", reg.specs)
	}
	off := false
	other, err := svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.uk", AutoRenew: &off, PrivacyMode: "off"})
	if err != nil || other.AutoRenew || other.PrivacyMode != "off" {
		t.Fatal(other, err)
	}
}

func TestSucceededRegistrationBecomesManagedZone(t *testing.T) {
	env := newRegistrationEnv(t)
	op := approvedRegistration(t, env.svc)
	if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); !errors.Is(err, ErrPending) {
		t.Fatal("pending zone", err)
	}
	got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID)
	if got.Status != RegistrationZonePending || env.dns.creates != 1 || !got.DomainExpiresAt.Equal(env.registrar.expires) || !got.AutoRenew || len(env.domains.zones) != 0 {
		t.Fatalf("%+v creates=%d zones=%d", got, env.dns.creates, len(env.domains.zones))
	}
	if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); !errors.Is(err, ErrPending) {
		t.Fatal("still pending", err)
	}
	env.dns.zone.Verified = true
	if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = env.svc.GetRegistration(t.Context(), admin, op.ID)
	if got.Status != RegistrationSucceededStatus || env.dns.creates != 1 || env.registrar.posts != 1 || len(env.domains.zones) != 1 {
		t.Fatalf("%+v creates=%d posts=%d", got, env.dns.creates, env.registrar.posts)
	}
	zone := env.domains.zones[0]
	if zone.Mode != "managed" || zone.Name != "acme.dev" || zone.RegistrationID != op.ID || got.ZoneID != zone.ID || len(zone.ProjectIDs) != 1 || zone.ProjectIDs[0] != "shop" || zone.ProviderZone.ID != "cf_zone" || zone.VerifiedAt.IsZero() {
		t.Fatalf("zone: %+v", zone)
	}
	if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err != nil || len(env.domains.zones) != 1 {
		t.Fatal("a finished registration did more work", err)
	}
	overview, err := env.svc.DomainOverview(t.Context(), admin)
	if err != nil || len(overview.Registrations) != 1 || overview.Registrations[0].Review != "" || overview.Zones[0].RegistrationID != op.ID {
		t.Fatalf("overview: %+v %v", overview, err)
	}
}

func TestSandboxRegistrationNeverCreatesAZone(t *testing.T) {
	env := newRegistrationEnv(t)
	env.sandbox()
	op := approvedRegistration(t, env.svc)
	for range 2 {
		if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID)
	if got.Status != RegistrationZonePending || got.Message != "Sandbox registrations don't create a DNS zone." || env.dns.creates != 0 || env.registrar.posts != 1 || len(env.domains.zones) != 0 {
		t.Fatalf("%+v creates=%d posts=%d", got, env.dns.creates, env.registrar.posts)
	}

	// A zone that already exists in the account is still adopted.
	env.dns.zone = &provider.DNSZone{ID: "cf_zone", Name: "acme.dev", AccountID: "account", Verified: true}
	if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = env.svc.GetRegistration(t.Context(), admin, op.ID)
	if got.Status != RegistrationSucceededStatus || env.dns.creates != 0 || len(env.domains.zones) != 1 {
		t.Fatalf("%+v creates=%d", got, env.dns.creates)
	}
}

func TestRegistrationZoneIsCreatedOnceAcrossALostSave(t *testing.T) {
	env := newRegistrationEnv(t)
	env.dns.zone = &provider.DNSZone{ID: "cf_zone", Name: "acme.dev", AccountID: "account", Verified: true}
	op := approvedRegistration(t, env.svc)
	// The zone row was written, but the registration's own save was lost.
	env.domains.zones = append(env.domains.zones, &Zone{ID: "zone_1", OrgID: "org", Name: "acme.dev", Mode: "managed", RegistrationID: op.ID, Revision: 1})
	if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID)
	if got.Status != RegistrationSucceededStatus || got.ZoneID != "zone_1" || len(env.domains.zones) != 1 || env.dns.creates != 0 {
		t.Fatalf("%+v zones=%d", got, len(env.domains.zones))
	}
}

func TestRegistrationProviderOutcomes(t *testing.T) {
	for _, tc := range []struct {
		state, status, message string
	}{
		{provider.RegistrationFailed, RegistrationFailedStatus, "Cloudflare didn't register acme.dev (registry_refused): the registry refused [redacted]"},
		{provider.RegistrationActionRequired, RegistrationActionRequiredStatus, "https://dash.cloudflare.com/account/domains/registrations"},
		{provider.RegistrationBlocked, RegistrationBlockedStatus, "https://dash.cloudflare.com/account/domains/registrations"},
	} {
		env := newRegistrationEnv(t)
		env.registrar.state, env.registrar.errorCode, env.registrar.message = tc.state, "registry_refused", "the registry refused someone@example.com"
		op := approvedRegistration(t, env.svc)
		if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err != nil {
			t.Fatal(tc.state, err)
		}
		got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID)
		if got.Status != tc.status || !strings.Contains(got.Message, tc.message) || strings.Contains(got.Message, "@") || len(env.domains.zones) != 0 {
			t.Fatalf("%s: %+v", tc.state, got)
		}
	}
	env := newRegistrationEnv(t)
	env.registrar.state = provider.RegistrationInProgress
	op := approvedRegistration(t, env.svc)
	if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); !errors.Is(err, ErrPending) {
		t.Fatal("in progress", err)
	}
	if got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID); got.Status != RegistrationRegistering || !got.FirstUnconfirmedAt.IsZero() || !got.DomainExpiresAt.IsZero() {
		t.Fatalf("in progress: %+v", got)
	}
}

func TestRegistrantContactStaysInVault(t *testing.T) {
	env := newRegistrationEnv(t)
	contact := &provider.RegistrantContact{Email: "owner@acme.dev", Phone: "+1.5555550100", Name: "Ada Owner", Street: "1 Main St", City: "Springfield", PostalCode: "12345", CountryCode: "US"}
	op, err := env.svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.dev", Registrant: contact})
	if err != nil {
		t.Fatal(err)
	}
	if !op.CustomRegistrant || strings.Contains(string(env.store.bodies[op.ID]), "owner@acme.dev") || strings.Contains(string(env.store.bodies[op.ID]), "Main St") {
		t.Fatalf("contact in the stored body: %s", env.store.bodies[op.ID])
	}
	key := "org/domain-registration/" + op.ID + "/registrant"
	if !strings.Contains(string(env.secrets[key]), "owner@acme.dev") {
		t.Fatalf("vault: %v", env.secrets)
	}
	if _, err = env.svc.ApproveRegistration(t.Context(), admin, op.ID, RegistrationApproval{Review: op.Review, Currency: "USD", RegistrationCost: "10.11"}); err != nil {
		t.Fatal(err)
	}
	_ = env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil)
	if len(env.registrar.specs) != 1 || env.registrar.specs[0].Registrant == nil || *env.registrar.specs[0].Registrant != *contact {
		t.Fatal("the registrar didn't get the contact")
	}
	if _, ok := env.secrets[key]; !ok {
		t.Fatal("deleted before the registration ended")
	}
	env.dns.zone.Verified = true
	if err = env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := env.secrets[key]; ok {
		t.Fatal("the contact outlived the registration")
	}
	if _, err = env.svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.uk", Registrant: &provider.RegistrantContact{Email: "owner@acme.dev"}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("incomplete contact accepted", err)
	}

	// A registration that ends without buying anything drops its contact too.
	other, err := env.svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.uk", Registrant: contact})
	if err != nil {
		t.Fatal(err)
	}
	env.now = env.now.Add(time.Hour)
	if err = env.svc.RecoverRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, _ := env.svc.GetRegistration(t.Context(), admin, other.ID)
	if _, ok := env.secrets["org/domain-registration/"+other.ID+"/registrant"]; ok || got.Status != RegistrationExpired {
		t.Fatalf("expired preview: %+v", got)
	}
}

// conflictingFinish loses the first save of a given status to another
// writer.
type conflictingFinish struct {
	*memoryRegistrations
	status string
	lost   bool
}

func (s *conflictingFinish) SaveRegistration(ctx context.Context, reg *Registration) error {
	if !s.lost && reg.Status == s.status {
		s.lost = true
		return ErrConflict
	}
	return s.memoryRegistrations.SaveRegistration(ctx, reg)
}

// failingDeletes refuses to delete secrets until err is cleared.
type failingDeletes struct {
	memorySecrets
	err error
}

func (f *failingDeletes) Delete(ctx context.Context, org, id string) error {
	if f.err != nil {
		return f.err
	}
	return f.memorySecrets.Delete(ctx, org, id)
}

func TestRegistrantOutlivesALostFinalSave(t *testing.T) {
	env := newRegistrationEnv(t)
	contact := &provider.RegistrantContact{Email: "owner@acme.dev", Phone: "+1.5555550100", Name: "Ada Owner", Street: "1 Main St", City: "Springfield", PostalCode: "12345", CountryCode: "US"}
	op, err := env.svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.dev", Registrant: contact})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = env.svc.ApproveRegistration(t.Context(), admin, op.ID, RegistrationApproval{Review: op.Review, Currency: "USD", RegistrationCost: "10.11"}); err != nil {
		t.Fatal(err)
	}
	key := "org/domain-registration/" + op.ID + "/registrant"
	// The name looks taken, but the final save loses to another writer.
	env.svc.Registrations = &conflictingFinish{memoryRegistrations: env.store, status: RegistrationFailedStatus}
	env.registrar.reason = "domain_unavailable"
	if err = env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("lost save", err)
	}
	if _, ok := env.secrets[key]; !ok {
		t.Fatal("the contact went before the final status was saved")
	}
	// The retry finds the name free again and still has the contact to buy with.
	env.registrar.reason = ""
	if err = env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); !errors.Is(err, ErrPending) {
		t.Fatal("retry", err)
	}
	if len(env.registrar.specs) != 1 || env.registrar.specs[0].Registrant == nil || *env.registrar.specs[0].Registrant != *contact {
		t.Fatalf("the retry bought without the contact: %+v", env.registrar.specs)
	}
}

func TestLeftoverRegistrantIsDroppedOnTheNextPass(t *testing.T) {
	env := newRegistrationEnv(t)
	secrets := &failingDeletes{memorySecrets: env.secrets, err: errors.New("vault unavailable")}
	env.svc.Secrets = secrets
	contact := &provider.RegistrantContact{Email: "owner@acme.dev", Phone: "+1.5555550100", Name: "Ada Owner", Street: "1 Main St", City: "Springfield", PostalCode: "12345", CountryCode: "US"}
	op, err := env.svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.dev", Registrant: contact})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = env.svc.ApproveRegistration(t.Context(), admin, op.ID, RegistrationApproval{Review: op.Review, Currency: "USD", RegistrationCost: "10.11"}); err != nil {
		t.Fatal(err)
	}
	env.registrar.reason = "domain_unavailable"
	if err = env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err == nil {
		t.Fatal("a failed delete was dropped")
	}
	key := "org/domain-registration/" + op.ID + "/registrant"
	got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID)
	if _, ok := env.secrets[key]; !ok || got.Status != RegistrationFailedStatus {
		t.Fatalf("after a failed delete: %+v", got)
	}
	secrets.err = nil
	if err = env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := env.secrets[key]; ok || env.registrar.posts != 0 {
		t.Fatalf("leftover contact kept, posts=%d", env.registrar.posts)
	}
	// A contact that's already gone is no error.
	if err = env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverRegistrationsRequeuesWorkInFlight(t *testing.T) {
	env := newRegistrationEnv(t)
	op := approvedRegistration(t, env.svc)
	if err := env.svc.RecoverRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(env.workflow.started) != 2 || env.workflow.started[1] != "org/"+op.ID+"/1" {
		t.Fatalf("started: %v", env.workflow.started)
	}
	if env.registrar.posts != 0 {
		t.Fatal("recovery posted by itself")
	}
}

func TestSetAutoRenewOffCallsProviderAndRecordsIt(t *testing.T) {
	env := newRegistrationEnv(t)
	env.dns.zone = &provider.DNSZone{ID: "cf_zone", Name: "acme.dev", AccountID: "account", Verified: true}
	op := approvedRegistration(t, env.svc)
	if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID)
	if _, err := env.svc.SetRegistrationAutoRenew(t.Context(), admin, op.ID, AutoRenewInput{AutoRenew: false, Revision: got.Revision - 1}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision", err)
	}
	off, err := env.svc.SetRegistrationAutoRenew(t.Context(), admin, op.ID, AutoRenewInput{AutoRenew: false, Revision: got.Revision})
	if err != nil || off.AutoRenew || off.Revision != got.Revision+1 || len(env.registrar.autoRenew) != 1 || env.registrar.autoRenew[0] {
		t.Fatal(off, err, env.registrar.autoRenew)
	}
}

func TestRegistrationRequiresOrganizationCloudManage(t *testing.T) {
	env := newRegistrationEnv(t)
	op := approvedRegistration(t, env.svc)
	dev := security.Principal{OrgID: "org", UserID: "dev"}
	ctx := t.Context()
	calls := map[string]func() error{
		"extensions": func() error { _, err := env.svc.RegistrationExtensions(ctx, dev, "cf"); return err },
		"search":     func() error { _, err := env.svc.SearchDomains(ctx, dev, "cf", "acme"); return err },
		"check": func() error {
			_, err := env.svc.CheckDomains(ctx, dev, DomainCheckInput{ConnectionID: "cf", Domains: []string{"acme.dev"}})
			return err
		},
		"preview": func() error {
			_, err := env.svc.PreviewRegistration(ctx, dev, RegistrationInput{ConnectionID: "cf", Name: "acme.uk"})
			return err
		},
		"approve": func() error {
			_, err := env.svc.ApproveRegistration(ctx, dev, op.ID, RegistrationApproval{Review: "x", Currency: "USD", RegistrationCost: "10.11"})
			return err
		},
		"list": func() error { _, err := env.svc.ListRegistrations(ctx, dev); return err },
		"get":  func() error { _, err := env.svc.GetRegistration(ctx, dev, op.ID); return err },
		"auto-renew": func() error {
			_, err := env.svc.SetRegistrationAutoRenew(ctx, dev, op.ID, AutoRenewInput{Revision: op.Revision})
			return err
		},
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, security.ErrForbidden) {
			t.Fatal(name, err)
		}
	}
	readOnly := admin
	readOnly.ReadOnly = true
	if _, err := env.svc.PreviewRegistration(ctx, readOnly, RegistrationInput{ConnectionID: "cf", Name: "acme.uk"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("read-only key previewed a purchase", err)
	}
	// The admin can use the read endpoints.
	if list, err := env.svc.CheckDomains(ctx, admin, DomainCheckInput{ConnectionID: "cf", Domains: []string{"Acme.dev"}}); err != nil || len(list.Offers) != 1 || list.Offers[0].Name != "acme.dev" {
		t.Fatal(list, err)
	}
	if list, err := env.svc.RegistrationExtensions(ctx, admin, "cf"); err != nil || len(list.Extensions) != 3 {
		t.Fatal(list, err)
	}
	if list, err := env.svc.ListRegistrations(ctx, admin); err != nil || len(list) != 1 || list[0].Review != "" {
		t.Fatal(list, err)
	}
}

func TestRegistrationZoneAddedByHand(t *testing.T) {
	for _, tc := range []struct {
		zone    Zone
		adopted bool
	}{
		{Zone{ID: "zone_1", OrgID: "org", Name: "acme.dev", Mode: "managed", AccountID: "account", Revision: 1}, true},
		{Zone{ID: "zone_1", OrgID: "org", Name: "acme.dev", Mode: "external", Revision: 1}, false},
	} {
		env := newRegistrationEnv(t)
		env.dns.zone = &provider.DNSZone{ID: "cf_zone", Name: "acme.dev", AccountID: "account", Verified: true}
		op := approvedRegistration(t, env.svc)
		zone := tc.zone
		env.domains.zones = append(env.domains.zones, &zone)
		err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil)
		got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID)
		if tc.adopted {
			if err != nil || got.Status != RegistrationSucceededStatus || got.ZoneID != "zone_1" || env.domains.zones[0].RegistrationID != op.ID {
				t.Fatalf("adopt: %v %+v", err, got)
			}
			continue
		}
		if !errors.Is(err, ErrConflict) || got.Status != RegistrationZonePending || env.domains.zones[0].RegistrationID != "" || len(env.domains.zones) != 1 {
			t.Fatalf("external zone: %v %+v", err, got)
		}
	}
}

// outboxNotices records every call, and keeps one row per outbox id the way
// the outbox's primary key does.
type outboxNotices struct {
	sent      []notifications.RenewalNotice
	rows      map[string]notifications.RenewalNotice
	results   []notifications.RegistrationResultNotice
	err       error
	resultErr error
}

func (n *outboxNotices) EnqueueRenewalNotice(_ context.Context, notice notifications.RenewalNotice) error {
	if n.err != nil {
		return n.err
	}
	n.sent = append(n.sent, notice)
	key := "renewal:" + notice.RegistrationID + ":" + notice.RenewsOn.Format("2006-01-02")
	if _, ok := n.rows[key]; !ok {
		n.rows[key] = notice
	}
	return nil
}

func (n *outboxNotices) EnqueueRegistrationResult(_ context.Context, notice notifications.RegistrationResultNotice) error {
	if n.resultErr != nil {
		return n.resultErr
	}
	n.results = append(n.results, notice)
	return nil
}

// newSweepEnv registers acme.dev, expiring on 2026-11-30 with auto-renew on.
func newSweepEnv(t *testing.T, now time.Time) (*registrationEnv, *outboxNotices, *Registration) {
	t.Helper()
	env := newRegistrationEnv(t)
	notices := &outboxNotices{rows: map[string]notifications.RenewalNotice{}}
	env.svc.Notices = notices
	env.now = now
	env.registrar.expires = time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)
	env.dns.zone = &provider.DNSZone{ID: "cf_zone", Name: "acme.dev", AccountID: "account", Verified: true}
	op := approvedRegistration(t, env.svc)
	if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID)
	if got.Status != RegistrationSucceededStatus || !got.AutoRenew {
		t.Fatalf("fixture: %+v", got)
	}
	return env, notices, got
}

func sweepFixture(t *testing.T, now time.Time) (*Service, *outboxNotices) {
	env, notices, _ := newSweepEnv(t, now)
	return env.svc, notices
}

// staleRegistrations hands out what another replica read before this one saved.
type staleRegistrations struct {
	*memoryRegistrations
	stale []*Registration
}

func (s staleRegistrations) SucceededRegistrations(context.Context) ([]*Registration, error) {
	return s.stale, nil
}

func TestRenewalNoticeIsIdempotent(t *testing.T) {
	svc, notices := sweepFixture(t, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)) // registration expires 2026-11-30, auto-renew on
	if err := svc.SweepRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := svc.SweepRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(notices.sent) != 1 || notices.sent[0].RenewsOn.Format("2006-01-02") != "2026-11-30" || notices.sent[0].RenewalCost != "10.11" {
		t.Fatalf("%+v", notices.sent)
	}
	n := notices.sent[0]
	if n.OrgID != "org" || n.Domain != "acme.dev" || n.Currency != "USD" || n.RegistrationID == "" {
		t.Fatalf("notice: %+v", n)
	}

	// Two API replicas read the registration before either saved it. Both
	// enqueue, the outbox keeps one row, and the replica that lost the save
	// race reports nothing.
	env, notices, reg := newSweepEnv(t, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	stale, err := env.store.SucceededRegistrations(t.Context())
	if err != nil || len(stale) != 1 {
		t.Fatal(stale, err)
	}
	other := NewService(env.svc.Deps)
	other.now = env.svc.now
	other.Registrations = staleRegistrations{memoryRegistrations: env.store, stale: stale}
	if err = env.svc.SweepRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = other.SweepRegistrations(t.Context()); err != nil {
		t.Fatal("the replica that lost the race", err)
	}
	if len(notices.sent) != 2 || len(notices.rows) != 1 {
		t.Fatalf("sent=%d rows=%d", len(notices.sent), len(notices.rows))
	}
	got, _ := env.svc.GetRegistration(t.Context(), admin, reg.ID)
	if !got.NoticeSentFor.Equal(env.registrar.expires) {
		t.Fatalf("notice not recorded: %+v", got)
	}
	if err = env.svc.SweepRegistrations(t.Context()); err != nil || len(notices.sent) != 2 {
		t.Fatal("swept again", err, len(notices.sent))
	}
}

func TestNoNoticeOnceTheRenewalDatePassed(t *testing.T) {
	// The registrar still reports 2026-11-30 two days after it.
	env, notices, _ := newSweepEnv(t, time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC))
	if err := env.svc.SweepRegistrations(t.Context()); err != nil || len(notices.sent) != 0 {
		t.Fatal("a notice for a date in the past", err, notices.sent)
	}
	env.now = time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)
	if err := env.svc.SweepRegistrations(t.Context()); err != nil || len(notices.sent) != 0 {
		t.Fatal("a notice on the renewal date itself", err, notices.sent)
	}
}

func TestNoNoticeWhenAutoRenewOffOrTooEarly(t *testing.T) {
	// 45 days ahead nothing goes out; 25 days ahead it does.
	env, notices, _ := newSweepEnv(t, time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC))
	if err := env.svc.SweepRegistrations(t.Context()); err != nil || len(notices.sent) != 0 {
		t.Fatal("too early", err, notices.sent)
	}
	env.now = time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	if err := env.svc.SweepRegistrations(t.Context()); err != nil || len(notices.sent) != 1 {
		t.Fatal("inside the lead", err, notices.sent)
	}

	// Auto-renew off: no notice.
	env, notices, reg := newSweepEnv(t, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	if _, err := env.svc.SetRegistrationAutoRenew(t.Context(), admin, reg.ID, AutoRenewInput{AutoRenew: false, Revision: reg.Revision}); err != nil {
		t.Fatal(err)
	}
	env.now = env.now.Add(25 * time.Hour)
	if err := env.svc.SweepRegistrations(t.Context()); err != nil || len(notices.sent) != 0 {
		t.Fatal("auto-renew off", err, notices.sent)
	}

	// A renewal moves the expiry a year on, and the next notice carries the
	// price the registrar quotes now.
	env, notices, reg = newSweepEnv(t, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	if err := env.svc.SweepRegistrations(t.Context()); err != nil || len(notices.sent) != 1 {
		t.Fatal(err, notices.sent)
	}
	// The registrar answering without an expiry keeps the stored one.
	env.registrar.expires = time.Time{}
	env.now = env.now.Add(25 * time.Hour)
	if err := env.svc.SweepRegistrations(t.Context()); err != nil || len(notices.sent) != 1 {
		t.Fatal("zero expiry", err, notices.sent)
	}
	if got, _ := env.svc.GetRegistration(t.Context(), admin, reg.ID); !got.DomainExpiresAt.Equal(time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("zero expiry stored: %+v", got)
	}
	env.registrar.expires = time.Date(2027, 11, 30, 0, 0, 0, 0, time.UTC)
	env.registrar.price.RenewalCost = "11.00"
	env.now = time.Date(2027, 11, 5, 0, 0, 0, 0, time.UTC)
	if err := env.svc.SweepRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(notices.sent) != 2 || notices.sent[1].RenewsOn.Format("2006-01-02") != "2027-11-30" || notices.sent[1].RenewalCost != "11.00" {
		t.Fatalf("after renewal: %+v", notices.sent)
	}
}

func TestSweepRefreshesAtMostDaily(t *testing.T) {
	env, notices, _ := newSweepEnv(t, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	for range 3 {
		if err := env.svc.SweepRegistrations(t.Context()); err != nil {
			t.Fatal(err)
		}
		env.now = env.now.Add(time.Hour)
	}
	if env.registrar.gets != 1 {
		t.Fatalf("gets=%d", env.registrar.gets)
	}
	env.now = env.now.Add(24 * time.Hour)
	if err := env.svc.SweepRegistrations(t.Context()); err != nil || env.registrar.gets != 2 {
		t.Fatal(err, env.registrar.gets)
	}
	// A failed enqueue leaves the notice to the next sweep.
	env.now = time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	notices.err = errors.New("outbox unavailable")
	if err := env.svc.SweepRegistrations(t.Context()); err == nil {
		t.Fatal("a failed enqueue was hidden")
	}
	notices.err = nil
	if err := env.svc.SweepRegistrations(t.Context()); err != nil || len(notices.sent) != 1 {
		t.Fatal(err, notices.sent)
	}
}

func TestRegistrationResultNotifiesOnce(t *testing.T) {
	env, notices, reg := newSweepEnv(t, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	if err := env.svc.ReconcileRegistration(t.Context(), "org", reg.ID, nil); err != nil {
		t.Fatal(err)
	}
	if len(notices.results) != 1 || notices.results[0] != (notifications.RegistrationResultNotice{OrgID: "org", RegistrationID: reg.ID, Domain: "acme.dev", Status: RegistrationSucceededStatus}) || reg.ResultNotifiedFor != RegistrationSucceededStatus {
		t.Fatalf("succeeded: %+v %+v", notices.results, reg)
	}
	if err := env.svc.RecoverRegistrations(t.Context()); err != nil || len(notices.results) != 1 {
		t.Fatal("recovery notified a recorded result again", err, notices.results)
	}

	env = newRegistrationEnv(t)
	notices = &outboxNotices{rows: map[string]notifications.RenewalNotice{}}
	env.svc.Notices = notices
	op := approvedRegistration(t, env.svc)
	env.registrar.price = provider.DomainPrice{Currency: "USD", RegistrationCost: "12.00"}
	_ = env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil)
	_ = env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil)
	if len(notices.results) != 1 || notices.results[0].Status != RegistrationPriceChanged || notices.results[0].Domain != "acme.dev" {
		t.Fatalf("price_changed: %+v", notices.results)
	}

	// The dashboard link travels with a registration Cloudflare needs finished.
	env = newRegistrationEnv(t)
	notices = &outboxNotices{rows: map[string]notifications.RenewalNotice{}}
	env.svc.Notices = notices
	env.registrar.state = provider.RegistrationActionRequired
	op = approvedRegistration(t, env.svc)
	_ = env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil)
	if len(notices.results) != 1 || notices.results[0].Status != RegistrationActionRequiredStatus || notices.results[0].Message != "https://dash.cloudflare.com/account/domains/registrations" {
		t.Fatalf("action_required: %+v", notices.results)
	}

	// A preview nobody approved ends quietly.
	if _, err := env.svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.uk"}); err != nil {
		t.Fatal(err)
	}
	env.now = env.now.Add(time.Hour)
	if err := env.svc.RecoverRegistrations(t.Context()); err != nil || len(notices.results) != 1 {
		t.Fatal("expired preview", err, notices.results)
	}
}

// recordingAudit keeps every audit event.
type recordingAudit struct{ events []projects.AuditEvent }

func (a *recordingAudit) Record(_ context.Context, e projects.AuditEvent) {
	a.events = append(a.events, e)
}

func TestRegistrationResultRetriesAfterAFailedEnqueue(t *testing.T) {
	env := newRegistrationEnv(t)
	audit := &recordingAudit{}
	notices := &outboxNotices{rows: map[string]notifications.RenewalNotice{}, resultErr: errors.New("outbox unavailable")}
	env.svc.Notices, env.svc.Audit = notices, audit
	env.dns.zone = &provider.DNSZone{ID: "cf_zone", Name: "acme.dev", AccountID: "account", Verified: true}
	op := approvedRegistration(t, env.svc)
	if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal("a failed notice failed the registration", err)
	}
	got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID)
	if got.Status != RegistrationSucceededStatus || got.ResultNotifiedFor != "" || len(notices.results) != 0 {
		t.Fatalf("after the failed enqueue: %+v", got)
	}
	pending := audit.events[len(audit.events)-1]
	if pending.Action != "domain.registration.notice_pending" || pending.Attributes["error"] != "queue the succeeded notice for acme.dev: outbox unavailable" {
		t.Fatalf("audit: %+v", pending)
	}
	// Recovery keeps failing loudly while the outbox is down.
	if err := env.svc.RecoverRegistrations(t.Context()); err == nil || !strings.Contains(err.Error(), "outbox unavailable") {
		t.Fatal("recovery hid the failure", err)
	}
	notices.resultErr = nil
	env.now = env.now.Add(30 * time.Second)
	if err := env.svc.RecoverRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(notices.results) != 1 || notices.results[0].Status != RegistrationSucceededStatus || notices.results[0].RegistrationID != op.ID {
		t.Fatalf("retried notice: %+v", notices.results)
	}
	got, _ = env.svc.GetRegistration(t.Context(), admin, op.ID)
	if got.ResultNotifiedFor != RegistrationSucceededStatus {
		t.Fatalf("not recorded: %+v", got)
	}
	env.now = env.now.Add(30 * time.Second)
	if err := env.svc.RecoverRegistrations(t.Context()); err != nil || len(notices.results) != 1 {
		t.Fatal("notified again", err, notices.results)
	}

	// A registration that ended more than a week ago is left alone.
	other := newRegistrationEnv(t)
	late := &outboxNotices{rows: map[string]notifications.RenewalNotice{}, resultErr: errors.New("outbox unavailable")}
	other.svc.Notices = late
	other.registrar.state = provider.RegistrationFailed
	failed := approvedRegistration(t, other.svc)
	_ = other.svc.ReconcileRegistration(t.Context(), "org", failed.ID, nil)
	late.resultErr = nil
	other.now = other.now.Add(8 * 24 * time.Hour)
	if err := other.svc.RecoverRegistrations(t.Context()); err != nil || len(late.results) != 0 {
		t.Fatal("an old registration was notified", err, late.results)
	}
}
