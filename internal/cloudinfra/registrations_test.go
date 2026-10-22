package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
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
	refuseAfterPost    bool
	posts              int
	reconcileOnlyCalls int
	specs              []provider.DomainRegistrationSpec
	autoRenew          []bool
}

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
	case r.refuseAfterPost:
		return provider.DomainRegistrationResult{}, provider.ErrInfrastructureScope
	}
	return r.result(spec.Name, false), nil
}

func (r *fakeRegistrar) result(name string, reconciled bool) provider.DomainRegistrationResult {
	state := r.state
	if state == "" {
		state = provider.RegistrationSucceeded
	}
	res := provider.DomainRegistrationResult{Name: name, State: state, Completed: state != provider.RegistrationInProgress, ErrorCode: r.errorCode, ErrorMessage: r.message, Reconciled: reconciled}
	if state == provider.RegistrationSucceeded {
		res.ExpiresAt = r.expires
		res.AutoRenew = len(r.specs) > 0 && r.specs[len(r.specs)-1].AutoRenew
		res.PrivacyMode = "redaction"
	}
	return res
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

type registrationCredentials struct{}

func (registrationCredentials) Credential(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
	if org != "org" || id != "cf" {
		return nil, nil, connections.ErrNotFound
	}
	return &connections.Connection{ID: id, OrgID: org, Cloud: "cloudflare", Generation: 1, Details: map[string]string{"account_id": "account", "registrar_mode": "sandbox"}}, []byte("token"), nil
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
	out := []*Registration{}
	for _, id := range s.order {
		reg, err := decodeRegistration(s.bodies[id])
		if err != nil {
			return nil, err
		}
		switch reg.Status {
		case RegistrationPreview, RegistrationApproved, RegistrationRegistering, RegistrationZonePending:
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
}

func newRegistrationEnv(t *testing.T) *registrationEnv {
	t.Helper()
	env := &registrationEnv{
		registrar: &fakeRegistrar{extensions: []string{"co.uk", "dev", "uk"}, price: provider.DomainPrice{Currency: "USD", RegistrationCost: "10.11", RenewalCost: "10.11"}, expires: time.Date(2027, 11, 5, 0, 0, 0, 0, time.UTC)},
		dns:       &registrationDNS{},
		store:     &memoryRegistrations{bodies: map[string][]byte{}},
		domains:   &overviewStore{projects: map[string]map[string]string{"org": {"shop": "Shop", "docs": "Docs"}}},
		secrets:   memorySecrets{},
		workflow:  &registrationStarts{},
		now:       time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
	}
	adapters := NewAdapters()
	if err := adapters.RegisterDNS("cloudflare", env.dns); err != nil {
		t.Fatal(err)
	}
	if err := adapters.RegisterRegistrar("cloudflare", env.registrar); err != nil {
		t.Fatal(err)
	}
	env.svc = NewService(Deps{Store: registrationLocks{}, Domains: env.domains, Registrations: env.store, Connections: registrationCredentials{}, Secrets: env.secrets, Adapters: adapters, Workflow: env.workflow,
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

func TestRegistrationNeverPostsAgainAfterAnyPostError(t *testing.T) {
	env := newRegistrationEnv(t)
	env.registrar.refuseAfterPost = true // a plain refusal, not marked ambiguous
	op := approvedRegistration(t, env.svc)
	if err := env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil); !errors.Is(err, ErrPending) {
		t.Fatal("first attempt", err)
	}
	got, _ := env.svc.GetRegistration(t.Context(), admin, op.ID)
	if got.Status != RegistrationRegistering || !got.Attempted {
		t.Fatalf("after a refused POST: %+v", got)
	}
	env.registrar.refuseAfterPost = false
	_ = env.svc.ReconcileRegistration(t.Context(), "org", op.ID, nil)
	if env.registrar.posts != 1 || env.registrar.reconcileOnlyCalls != 1 {
		t.Fatalf("posts=%d reconcile=%d", env.registrar.posts, env.registrar.reconcileOnlyCalls)
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

func TestApprovalNeedsExactQuoteAndOpenWindow(t *testing.T) {
	env := newRegistrationEnv(t)
	op, err := env.svc.PreviewRegistration(t.Context(), admin, RegistrationInput{ConnectionID: "cf", Name: "acme.dev"})
	if err != nil {
		t.Fatal(err)
	}
	if op.Review == "" || op.Quote.RegistrationCost != "10.11" || !op.ReviewExpiresAt.Equal(env.now.Add(15*time.Minute)) || !op.Sandbox {
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

func TestSetRegistrationAutoRenewChecksRevision(t *testing.T) {
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
