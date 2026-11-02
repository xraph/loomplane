package cloudinfra

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// Registration statuses. Preview, approved, registering and zone_pending are
// in flight; every other status is final.
const (
	RegistrationPreview              = "preview"
	RegistrationExpired              = "expired"
	RegistrationApproved             = "approved"
	RegistrationRegistering          = "registering"
	RegistrationZonePending          = "zone_pending"
	RegistrationSucceededStatus      = "succeeded"
	RegistrationPriceChanged         = "price_changed"
	RegistrationFailedStatus         = "failed"
	RegistrationActionRequiredStatus = "action_required"
	RegistrationBlockedStatus        = "blocked"
	RegistrationUnconfirmed          = "unconfirmed"
)

const (
	registrationReviewWindow = 15 * time.Minute
	// registrationUnconfirmedAfter is how long a registration may stay
	// unreadable after its POST before Loomplane stops waiting.
	registrationUnconfirmedAfter = time.Hour
	registrationSearchLimit      = 20
	registrationMaxCheck         = 20
	registrationMaxMessage       = 300
	unconfirmedMessage           = "Cloudflare has no record of this registration yet. Check the registrations page in your Cloudflare dashboard before trying again."
)

// Registration is one domain bought through the organization's own
// registrar account.
type Registration struct {
	ID                 string               `json:"id" forge:"id"`
	OrgID              string               `json:"org_id"`
	ConnectionID       string               `json:"connection_id"`
	Provider           string               `json:"provider"`
	AccountID          string               `json:"account_id"`
	Name               string               `json:"name"`
	AutoRenew          bool                 `json:"auto_renew"`
	PrivacyMode        string               `json:"privacy_mode"`
	CustomRegistrant   bool                 `json:"custom_registrant"`
	ProjectIDs         []string             `json:"project_ids"`
	Quote              provider.DomainPrice `json:"quote"`
	Status             string               `json:"status"`
	Message            string               `json:"message"`
	Review             string               `json:"review"`
	ReviewExpiresAt    time.Time            `json:"review_expires_at"`
	Attempt            int                  `json:"attempt"`
	Attempted          bool                 `json:"attempted"`
	FirstUnconfirmedAt time.Time            `json:"first_unconfirmed_at"`
	ZoneID             string               `json:"zone_id"`
	DomainExpiresAt    time.Time            `json:"domain_expires_at"`
	RefreshedAt        time.Time            `json:"refreshed_at"`
	NoticeSentFor      time.Time            `json:"notice_sent_for"`
	ResultNotifiedFor  string               `json:"result_notified_for"`
	Sandbox            bool                 `json:"sandbox"`
	Revision           int64                `json:"revision"`
	CreatedAt          time.Time            `json:"created_at"`
	UpdatedAt          time.Time            `json:"updated_at"`
	Principal          security.Principal   `json:"-"`
}

// RegistrationInput asks for a price to approve. A nil AutoRenew means on,
// an empty PrivacyMode means "redaction". A nil Registrant uses the
// account's default contact.
type RegistrationInput struct {
	ConnectionID string                      `json:"connection_id"`
	Name         string                      `json:"name"`
	AutoRenew    *bool                       `json:"auto_renew,omitempty"`
	PrivacyMode  string                      `json:"privacy_mode,omitempty"`
	Registrant   *provider.RegistrantContact `json:"registrant,omitempty"`
	ProjectIDs   []string                    `json:"project_ids,omitempty"`
}

// RegistrationApproval repeats the reviewed price. Buying needs both.
type RegistrationApproval struct {
	Review           string `json:"review"`
	Currency         string `json:"currency"`
	RegistrationCost string `json:"registration_cost"`
}

type AutoRenewInput struct {
	AutoRenew bool  `json:"auto_renew"`
	Revision  int64 `json:"revision"`
}

type DomainOfferList struct {
	Offers []provider.DomainOffer `json:"offers"`
}

type RegistrationExtensionList struct {
	Extensions []string `json:"extensions"`
}

type RegistrationList struct {
	Registrations []*Registration `json:"registrations"`
}

type DomainCheckInput struct {
	ConnectionID string   `json:"connection_id"`
	Domains      []string `json:"domains"`
}

type DomainSearchQuery struct {
	ConnectionID string `query:"connection_id"`
	Q            string `query:"q"`
}

type ExtensionsQuery struct {
	ConnectionID string `query:"connection_id"`
}

// RegistrationWorkflow is optional on Deps.Workflow, like RouteWorkflow.
type RegistrationWorkflow interface {
	StartRegistration(ctx context.Context, org, id string, attempt int) error
}

// RegistrationStore saves registrations with a revision check.
// PendingRegistrations lists every organization's preview, approved,
// registering and zone_pending rows, SucceededRegistrations its succeeded
// ones. UnnotifiedRegistrations lists rows saved since the given time whose
// final status has no result notice queued yet.
type RegistrationStore interface {
	CreateRegistration(context.Context, *Registration) error
	Registration(ctx context.Context, org, id string) (*Registration, error)
	Registrations(ctx context.Context, org string) ([]*Registration, error)
	SaveRegistration(context.Context, *Registration) error
	PendingRegistrations(context.Context) ([]*Registration, error)
	SucceededRegistrations(context.Context) ([]*Registration, error)
	UnnotifiedRegistrations(ctx context.Context, since time.Time) ([]*Registration, error)
}

func (s *Service) registrationStore() (RegistrationStore, error) {
	if s.Registrations != nil {
		return s.Registrations, nil
	}
	if store, ok := s.Store.(RegistrationStore); ok {
		return store, nil
	}
	return nil, ErrInvalid
}

func registrantKey(id string) string { return "domain-registration/" + id + "/registrant" }

// shown is what callers see: the review token only ever leaves preview.
func shown(reg *Registration) *Registration {
	v := *reg
	v.Review = ""
	return &v
}

func (s *Service) registrar(ctx context.Context, org, connectionID string) (provider.DomainRegistrar, provider.InfrastructureCredential, map[string]string, error) {
	if connectionID == "" {
		return nil, provider.InfrastructureCredential{}, nil, ErrInvalid
	}
	conn, raw, err := s.Connections.Credential(ctx, org, connectionID)
	if err != nil {
		return nil, provider.InfrastructureCredential{}, nil, err
	}
	cred, err := Credential(conn, raw)
	if err != nil {
		return nil, provider.InfrastructureCredential{}, nil, err
	}
	adapter, err := s.Adapters.Registrar(conn.Cloud)
	if err != nil {
		return nil, provider.InfrastructureCredential{}, nil, err
	}
	return adapter, cred, conn.Details, nil
}

// registrarSandbox says whether the adapter calls the registrar sandbox. It
// asks the adapter itself, never a stored detail, and an adapter that can't
// say counts as production.
func registrarSandbox(adapter provider.DomainRegistrar) bool {
	moded, ok := adapter.(interface{ RegistrarMode() string })
	return ok && moded.RegistrarMode() == "sandbox"
}

func registrarMode(sandbox bool) string {
	if sandbox {
		return "sandbox"
	}
	return "production"
}

// RegistrationExtensions lists the TLDs the connection's registrar sells.
func (s *Service) RegistrationExtensions(ctx context.Context, p security.Principal, connectionID string) (*RegistrationExtensionList, error) {
	if err := s.domainScope(ctx, p, "", false); err != nil {
		return nil, err
	}
	adapter, cred, _, err := s.registrar(ctx, p.OrgID, connectionID)
	if err != nil {
		return nil, err
	}
	tlds, err := adapter.RegistrationExtensions(ctx, cred)
	if err != nil {
		return nil, err
	}
	return &RegistrationExtensionList{Extensions: append([]string{}, tlds...)}, nil
}

func (s *Service) SearchDomains(ctx context.Context, p security.Principal, connectionID, q string) (*DomainOfferList, error) {
	if err := s.domainScope(ctx, p, "", false); err != nil {
		return nil, err
	}
	if strings.TrimSpace(q) == "" {
		return nil, ErrInvalid
	}
	adapter, cred, _, err := s.registrar(ctx, p.OrgID, connectionID)
	if err != nil {
		return nil, err
	}
	offers, err := adapter.SearchDomains(ctx, cred, q, registrationSearchLimit)
	if err != nil {
		return nil, err
	}
	return &DomainOfferList{Offers: append([]provider.DomainOffer{}, offers...)}, nil
}

// CheckDomains asks for live availability and prices. It buys nothing.
func (s *Service) CheckDomains(ctx context.Context, p security.Principal, in DomainCheckInput) (*DomainOfferList, error) {
	if err := s.domainScope(ctx, p, "", false); err != nil {
		return nil, err
	}
	if len(in.Domains) == 0 || len(in.Domains) > registrationMaxCheck {
		return nil, ErrInvalid
	}
	names := make([]string, 0, len(in.Domains))
	for _, domain := range in.Domains {
		name, err := NormalizeHostname(domain)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	adapter, cred, _, err := s.registrar(ctx, p.OrgID, in.ConnectionID)
	if err != nil {
		return nil, err
	}
	offers, err := adapter.CheckDomains(ctx, cred, names)
	if err != nil {
		return nil, err
	}
	return &DomainOfferList{Offers: append([]provider.DomainOffer{}, offers...)}, nil
}

func checkedOffer(offers []provider.DomainOffer, name string) (provider.DomainOffer, error) {
	for _, offer := range offers {
		if offer.Name == name {
			return offer, nil
		}
	}
	return provider.DomainOffer{}, provider.ErrInfrastructureIdentity
}

// registrableName checks name is one label under the longest TLD the
// registrar lists.
func registrableName(name string, tlds []string) error {
	tld := ""
	for _, candidate := range tlds {
		if strings.HasSuffix(name, "."+candidate) && len(candidate) > len(tld) {
			tld = candidate
		}
	}
	if tld == "" || strings.Contains(strings.TrimSuffix(name, "."+tld), ".") {
		return fmt.Errorf("%w: register a name directly under one of the registrar's extensions", ErrInvalid)
	}
	return nil
}

func completeContact(c *provider.RegistrantContact) bool {
	for _, v := range []string{c.Email, c.Phone, c.Name, c.Street, c.City, c.PostalCode, c.CountryCode} {
		if strings.TrimSpace(v) == "" {
			return false
		}
	}
	return true
}

// PreviewRegistration quotes a live price for one name. Nothing is bought
// until the quote is approved and checked again.
func (s *Service) PreviewRegistration(ctx context.Context, p security.Principal, in RegistrationInput) (*Registration, error) {
	if err := s.domainScope(ctx, p, "", true); err != nil {
		return nil, err
	}
	st, err := s.registrationStore()
	if err != nil {
		return nil, err
	}
	name, err := NormalizeHostname(in.Name)
	if err != nil {
		return nil, err
	}
	privacy := in.PrivacyMode
	if privacy == "" {
		privacy = "redaction"
	}
	if (privacy != "redaction" && privacy != "off") || (in.Registrant != nil && !completeContact(in.Registrant)) {
		return nil, ErrInvalid
	}
	domains, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	if checkErr := zoneProjects(ctx, domains, p.OrgID, in.ProjectIDs); checkErr != nil {
		return nil, checkErr
	}
	adapter, cred, details, err := s.registrar(ctx, p.OrgID, in.ConnectionID)
	if err != nil {
		return nil, err
	}
	// The dialog shows the connection's mode until the preview answers, so
	// the two must agree before anyone is told whether they'll be charged.
	sandbox := registrarSandbox(adapter)
	if details["registrar_mode"] != registrarMode(sandbox) {
		return nil, fmt.Errorf("%w: Check the Cloudflare connection again: its registrar mode changed", ErrConflict)
	}
	tlds, err := adapter.RegistrationExtensions(ctx, cred)
	if err != nil {
		return nil, err
	}
	if checkErr := registrableName(name, tlds); checkErr != nil {
		return nil, checkErr
	}
	offers, err := adapter.CheckDomains(ctx, cred, []string{name})
	if err != nil {
		return nil, err
	}
	offer, err := checkedOffer(offers, name)
	if err != nil {
		return nil, err
	}
	if !offer.Registrable {
		return nil, fmt.Errorf("%w: %s can't be registered: %s", ErrConflict, name, offer.Reason)
	}
	if offer.Price == nil {
		return nil, fmt.Errorf("%w: the registrar gave no price for %s, so Loomplane won't buy it", ErrConflict, name)
	}
	id, err := randomID("reg")
	if err != nil {
		return nil, err
	}
	review, err := reviewToken()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	reg := &Registration{ID: id, OrgID: p.OrgID, ConnectionID: in.ConnectionID, Provider: cred.Provider, AccountID: cred.AccountID, Name: name,
		AutoRenew: in.AutoRenew == nil || *in.AutoRenew, PrivacyMode: privacy, CustomRegistrant: in.Registrant != nil, ProjectIDs: append([]string{}, in.ProjectIDs...),
		Quote: *offer.Price, Status: RegistrationPreview, Review: review, ReviewExpiresAt: now.Add(registrationReviewWindow),
		Sandbox: sandbox, Revision: 1, CreatedAt: now, UpdatedAt: now, Principal: p}
	if in.Registrant != nil {
		raw, encodeErr := json.Marshal(in.Registrant)
		if encodeErr != nil {
			return nil, encodeErr
		}
		if putErr := s.Secrets.Put(ctx, p.OrgID, registrantKey(id), raw); putErr != nil {
			return nil, putErr
		}
	}
	if createErr := st.CreateRegistration(ctx, reg); createErr != nil {
		if reg.CustomRegistrant {
			_ = s.Secrets.Delete(ctx, p.OrgID, registrantKey(id)) //nolint:errcheck // the preview failed either way
		}
		return nil, createErr
	}
	s.registrationAudit(ctx, p, "domain.registration.preview", reg)
	return reg, nil
}

// ApproveRegistration accepts the reviewed price and starts the purchase.
// The price is checked again with the registrar before anything is bought.
func (s *Service) ApproveRegistration(ctx context.Context, p security.Principal, id string, in RegistrationApproval) (*Registration, error) {
	if err := s.domainScope(ctx, p, "", true); err != nil {
		return nil, err
	}
	st, err := s.registrationStore()
	if err != nil {
		return nil, err
	}
	reg, err := st.Registration(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if in.Review == "" || subtle.ConstantTimeCompare([]byte(in.Review), []byte(reg.Review)) != 1 {
		return nil, ErrConflict
	}
	if reg.Quote.Currency != in.Currency || reg.Quote.RegistrationCost != in.RegistrationCost {
		return nil, fmt.Errorf("%w: the approved price isn't the quoted one", ErrConflict)
	}
	switch {
	case reg.Status == RegistrationApproved || reg.Status == RegistrationRegistering || reg.Status == RegistrationZonePending || reg.Status == RegistrationSucceededStatus:
		// A repeated approval, after a lost response, starts nothing new.
		return shown(reg), nil
	case reg.Status != RegistrationPreview || !s.now().Before(reg.ReviewExpiresAt):
		return nil, fmt.Errorf("%w: this quote can't be approved any more; preview the registration again", ErrConflict)
	}
	reg.Status, reg.Attempt, reg.Message, reg.Principal = RegistrationApproved, 1, "", p
	if saveErr := s.saveRegistration(ctx, st, reg); saveErr != nil {
		return nil, saveErr
	}
	s.registrationAudit(ctx, p, "domain.registration.approve", reg)
	s.enqueueRegistration(ctx, reg)
	return shown(reg), nil
}

func (s *Service) enqueueRegistration(ctx context.Context, reg *Registration) {
	if workflow, ok := s.Workflow.(RegistrationWorkflow); ok {
		if err := workflow.StartRegistration(ctx, reg.OrgID, reg.ID, reg.Attempt); err != nil {
			s.registrationAudit(ctx, reg.Principal, "domain.registration.enqueue_pending", reg)
		}
	}
}

func (s *Service) ListRegistrations(ctx context.Context, p security.Principal) ([]*Registration, error) {
	if err := s.domainScope(ctx, p, "", false); err != nil {
		return nil, err
	}
	st, err := s.registrationStore()
	if err != nil {
		return nil, err
	}
	regs, err := st.Registrations(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	out := make([]*Registration, 0, len(regs))
	for _, reg := range regs {
		out = append(out, shown(reg))
	}
	return out, nil
}

func (s *Service) GetRegistration(ctx context.Context, p security.Principal, id string) (*Registration, error) {
	if err := s.domainScope(ctx, p, "", false); err != nil {
		return nil, err
	}
	st, err := s.registrationStore()
	if err != nil {
		return nil, err
	}
	reg, err := st.Registration(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	return shown(reg), nil
}

// SetRegistrationAutoRenew turns renewal on or off at the registrar and
// records what the registrar reports.
func (s *Service) SetRegistrationAutoRenew(ctx context.Context, p security.Principal, id string, in AutoRenewInput) (*Registration, error) {
	if err := s.domainScope(ctx, p, "", true); err != nil {
		return nil, err
	}
	st, err := s.registrationStore()
	if err != nil {
		return nil, err
	}
	reg, err := st.Registration(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if reg.Revision != in.Revision || (reg.Status != RegistrationSucceededStatus && reg.Status != RegistrationZonePending) {
		return nil, ErrConflict
	}
	adapter, cred, _, err := s.registrar(ctx, reg.OrgID, reg.ConnectionID)
	if err != nil {
		return nil, err
	}
	if cred.AccountID != reg.AccountID {
		return nil, provider.ErrInfrastructureIdentity
	}
	domain, err := adapter.SetAutoRenew(ctx, cred, reg.Name, in.AutoRenew)
	if err != nil {
		return nil, err
	}
	reg.AutoRenew = domain.AutoRenew
	if !domain.ExpiresAt.IsZero() {
		reg.DomainExpiresAt = domain.ExpiresAt
	}
	reg.RefreshedAt = s.now().UTC()
	if saveErr := s.saveRegistration(ctx, st, reg); saveErr != nil {
		return nil, saveErr
	}
	s.registrationAudit(ctx, p, "domain.registration.auto_renew", reg)
	return shown(reg), nil
}

// RecoverRegistrations restarts registrations in flight, expires previews
// nobody approved in time and queues result notices that failed to queue.
func (s *Service) RecoverRegistrations(ctx context.Context) error {
	st, err := s.registrationStore()
	if err != nil {
		return err
	}
	regs, err := st.PendingRegistrations(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, reg := range regs {
		if reg.Status != RegistrationPreview {
			s.enqueueRegistration(ctx, reg)
			continue
		}
		if s.now().Before(reg.ReviewExpiresAt) {
			continue
		}
		reg.Status, reg.Message = RegistrationExpired, "The quote expired before anyone approved it."
		if finishErr := s.finishRegistration(ctx, st, reg); finishErr != nil && !errors.Is(finishErr, ErrConflict) {
			errs = append(errs, finishErr)
		}
	}
	if notifyErr := s.renotifyResults(ctx, st); notifyErr != nil {
		errs = append(errs, notifyErr)
	}
	return errors.Join(errs...)
}

// ReconcileRegistration is a Dispatch activity. Every transition is saved
// before the next registrar call, and once the purchase may have been sent
// it is only ever read back, never sent again.
func (s *Service) ReconcileRegistration(ctx context.Context, org, id string, heartbeat func([]byte) error) error {
	st, err := s.registrationStore()
	if err != nil {
		return err
	}
	return s.Store.WithOperationLock(ctx, "registration/"+id, func() error {
		reg, err := st.Registration(ctx, org, id)
		if err != nil {
			return err
		}
		if reg.Status != RegistrationApproved && reg.Status != RegistrationRegistering && reg.Status != RegistrationZonePending {
			if reg.Status == RegistrationPreview {
				return nil
			}
			// A final row may still hold a contact its first delete missed.
			return s.dropRegistrant(ctx, reg)
		}
		if heartbeat != nil {
			if checkErr := heartbeat([]byte(`{"stage":"registration"}`)); checkErr != nil {
				return checkErr
			}
		}
		if reg.Status != RegistrationZonePending {
			if err = s.register(ctx, st, reg); err != nil || reg.Status != RegistrationZonePending {
				return err
			}
		}
		return s.handOffZone(ctx, st, reg)
	})
}

// register buys the domain at most once. Before the purchase is sent any
// failure ends the registration with nothing bought, and so does a definite
// refusal of the purchase itself. An uncertain answer is only ever read back
// through ReconcileOnly.
func (s *Service) register(ctx context.Context, st RegistrationStore, reg *Registration) error {
	if !reg.Attempted {
		adapter, cred, spec, stop, err := s.prepareRegistration(ctx, reg)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			reg.Status, reg.Message = RegistrationFailedStatus, registrationFailure(err)
			return s.finishRegistration(ctx, st, reg)
		}
		if stop {
			return s.finishRegistration(ctx, st, reg)
		}
		reg.Attempted, reg.Status, reg.Message = true, RegistrationRegistering, "Cloudflare is registering "+reg.Name+"."
		if saveErr := s.saveRegistration(ctx, st, reg); saveErr != nil {
			return saveErr
		}
		result, err := adapter.RegisterDomain(ctx, cred, spec)
		if err != nil && ctx.Err() == nil && !errors.Is(err, provider.ErrInfrastructureAmbiguous) {
			// Cloudflare refused the purchase, or it was never sent.
			reg.Status, reg.FirstUnconfirmedAt = RegistrationFailedStatus, time.Time{}
			reg.Message = "Cloudflare refused to register " + reg.Name + refusalDetail(err) + ". Nothing was bought."
			return s.finishRegistration(ctx, st, reg)
		}
		return s.recordResult(ctx, st, reg, adapter, cred, result, err)
	}
	adapter, cred, err := s.registrationCredential(ctx, reg)
	if err != nil {
		return s.unconfirmed(ctx, st, reg)
	}
	result, err := adapter.RegisterDomain(ctx, cred, provider.DomainRegistrationSpec{OperationID: reg.ID, Name: reg.Name, AutoRenew: reg.AutoRenew, PrivacyMode: reg.PrivacyMode, ReconcileOnly: true})
	return s.recordResult(ctx, st, reg, adapter, cred, result, err)
}

// refusalDetail names the HTTP status and Cloudflare's error codes, never
// its message.
func refusalDetail(err error) string {
	var api *provider.InfrastructureAPIError
	if !errors.As(err, &api) || api.Status == 0 {
		return ""
	}
	detail := fmt.Sprintf(" (HTTP %d", api.Status)
	if len(api.Codes) > 0 {
		codes := make([]string, 0, len(api.Codes))
		for _, code := range api.Codes {
			codes = append(codes, strconv.Itoa(code))
		}
		label := "code"
		if len(codes) > 1 {
			label = "codes"
		}
		detail += ", Cloudflare " + label + " " + strings.Join(codes, ", ")
	}
	return detail + ")"
}

func (s *Service) registrationCredential(ctx context.Context, reg *Registration) (provider.DomainRegistrar, provider.InfrastructureCredential, error) {
	adapter, cred, _, err := s.registrar(ctx, reg.OrgID, reg.ConnectionID)
	if err == nil && cred.AccountID != reg.AccountID {
		err = provider.ErrInfrastructureIdentity
	}
	return adapter, cred, err
}

// prepareRegistration rechecks the approver and the price, and builds the
// purchase. stop means the registration ended here, with its status set.
func (s *Service) prepareRegistration(ctx context.Context, reg *Registration) (adapter provider.DomainRegistrar, cred provider.InfrastructureCredential, spec provider.DomainRegistrationSpec, stop bool, err error) {
	if err = s.Authz.Require(ctx, reg.Principal, security.OrganizationCloudManage, reg.OrgID); err != nil {
		return
	}
	if adapter, cred, err = s.registrationCredential(ctx, reg); err != nil {
		return
	}
	if live := registrarSandbox(adapter); live != reg.Sandbox {
		reg.Status = RegistrationFailedStatus
		reg.Message = fmt.Sprintf("The registrar mode changed from %s to %s since the preview. Nothing was bought. Check the Cloudflare connection again and preview the registration again.", registrarMode(reg.Sandbox), registrarMode(live))
		return adapter, cred, spec, true, nil
	}
	offers, err := adapter.CheckDomains(ctx, cred, []string{reg.Name})
	if err != nil {
		return
	}
	offer, err := checkedOffer(offers, reg.Name)
	if err != nil {
		return
	}
	if !offer.Registrable {
		reg.Status = RegistrationFailedStatus
		reg.Message = cleanMessage(fmt.Sprintf("%s is no longer available (%s). Nothing was bought.", reg.Name, offer.Reason), nil)
		return adapter, cred, spec, true, nil
	}
	if offer.Price == nil || provider.MatchDomainPrice(reg.Quote, *offer.Price) != nil {
		reg.Status, reg.Message = RegistrationPriceChanged, "The registrar no longer quotes a price for "+reg.Name+". Nothing was bought."
		if offer.Price != nil {
			reg.Message = cleanMessage(fmt.Sprintf("Cloudflare now quotes %s %s for %s. Nothing was bought. Review the new price before buying.", offer.Price.Currency, offer.Price.RegistrationCost, reg.Name), nil)
		}
		return adapter, cred, spec, true, nil
	}
	spec = provider.DomainRegistrationSpec{OperationID: reg.ID, Name: reg.Name, AutoRenew: reg.AutoRenew, PrivacyMode: reg.PrivacyMode}
	if reg.CustomRegistrant {
		if spec.Registrant, err = s.registrant(ctx, reg); err != nil {
			return
		}
	}
	return adapter, cred, spec, false, nil
}

func (s *Service) registrant(ctx context.Context, reg *Registration) (*provider.RegistrantContact, error) {
	raw, err := s.Secrets.Reveal(ctx, reg.OrgID, registrantKey(reg.ID))
	if err != nil {
		return nil, err
	}
	var contact provider.RegistrantContact
	if decodeErr := json.Unmarshal(raw, &contact); decodeErr != nil {
		return nil, decodeErr
	}
	return &contact, nil
}

// recordResult saves what the registrar answered. An error here means the
// purchase may have been sent, so the outcome is unknown.
func (s *Service) recordResult(ctx context.Context, st RegistrationStore, reg *Registration, adapter provider.DomainRegistrar, cred provider.InfrastructureCredential, result provider.DomainRegistrationResult, err error) error {
	if err != nil || result.Name != reg.Name {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return s.unconfirmed(ctx, st, reg)
	}
	dashboard := registrationDashboard(reg.AccountID)
	switch result.State {
	case provider.RegistrationInProgress:
		return s.keepRegistering(ctx, st, reg, "Cloudflare is still registering "+reg.Name+".", time.Time{})
	case provider.RegistrationSucceeded:
		expires, autoRenew := result.ExpiresAt, result.AutoRenew
		if expires.IsZero() {
			// The workflow answer can omit the registration; read the domain.
			domain, readErr := adapter.GetRegisteredDomain(ctx, cred, reg.Name)
			if readErr != nil {
				return readErr
			}
			if domain.Name != reg.Name {
				return provider.ErrInfrastructureIdentity
			}
			expires, autoRenew = domain.ExpiresAt, domain.AutoRenew
		}
		reg.Status, reg.Message = RegistrationZonePending, reg.Name+" is registered. Waiting for its Cloudflare zone to become active."
		reg.DomainExpiresAt, reg.AutoRenew, reg.FirstUnconfirmedAt = expires, autoRenew, time.Time{}
		return s.saveRegistration(ctx, st, reg)
	case provider.RegistrationFailed:
		reg.Status = RegistrationFailedStatus
		reg.Message = fmt.Sprintf("Cloudflare didn't register %s", reg.Name)
		if code := cleanCode(result.ErrorCode); code != "" {
			reg.Message += " (" + code + ")"
		}
		if result.ErrorMessage != "" {
			reg.Message += ": " + result.ErrorMessage
		}
		contact, _ := s.registrant(ctx, reg) //nolint:errcheck // without the contact only the generic patterns are removed
		reg.Message = cleanMessage(reg.Message, contact)
	case provider.RegistrationActionRequired:
		reg.Status, reg.Message = RegistrationActionRequiredStatus, "Cloudflare needs you to finish registering "+reg.Name+" at "+dashboard
	case provider.RegistrationBlocked:
		reg.Status, reg.Message = RegistrationBlockedStatus, "Cloudflare blocked the registration of "+reg.Name+". See "+dashboard
	default:
		return s.unconfirmed(ctx, st, reg)
	}
	reg.FirstUnconfirmedAt = time.Time{}
	return s.finishRegistration(ctx, st, reg)
}

// unconfirmed keeps waiting for the registrar to show the registration, for
// up to an hour from the first answer it couldn't read.
func (s *Service) unconfirmed(ctx context.Context, st RegistrationStore, reg *Registration) error {
	now, first := s.now().UTC(), reg.FirstUnconfirmedAt
	if first.IsZero() {
		first = now
	}
	if now.Sub(first) > registrationUnconfirmedAfter {
		reg.Status, reg.Message = RegistrationUnconfirmed, unconfirmedMessage
		return s.finishRegistration(ctx, st, reg)
	}
	return s.keepRegistering(ctx, st, reg, "Waiting for Cloudflare to confirm the registration of "+reg.Name+".", first)
}

// keepRegistering waits for the registrar, saving only what changed.
func (s *Service) keepRegistering(ctx context.Context, st RegistrationStore, reg *Registration, message string, firstUnconfirmed time.Time) error {
	if reg.Status != RegistrationRegistering || reg.Message != message || !reg.FirstUnconfirmedAt.Equal(firstUnconfirmed) {
		reg.Status, reg.Message, reg.FirstUnconfirmedAt = RegistrationRegistering, message, firstUnconfirmed
		if err := s.saveRegistration(ctx, st, reg); err != nil {
			return err
		}
	}
	return ErrPending
}

// handOffZone adds the bought domain as a managed zone once its Cloudflare
// zone is active.
func (s *Service) handOffZone(ctx context.Context, st RegistrationStore, reg *Registration) error {
	domains, err := s.domainStore()
	if err != nil {
		return err
	}
	zones, err := domains.Zones(ctx, reg.OrgID)
	if err != nil {
		return err
	}
	for _, zone := range zones {
		if zone.Name != reg.Name {
			continue
		}
		switch {
		case zone.RegistrationID == reg.ID:
		case zone.RegistrationID == "" && zone.Mode == "managed" && zone.AccountID == reg.AccountID:
			// Someone added the new zone by hand while it activated.
			zone.RegistrationID = reg.ID
			if saveErr := domains.SaveZone(ctx, zone); saveErr != nil {
				return saveErr
			}
		default:
			if message := reg.Name + " is registered, but it was already added to Loomplane another way. Remove that zone so the registration can add it."; reg.Message != message {
				reg.Message = message
				if saveErr := s.saveRegistration(ctx, st, reg); saveErr != nil {
					return saveErr
				}
			}
			return ErrConflict
		}
		return s.zoneHandedOff(ctx, st, reg, zone)
	}
	dns, cred, err := s.registrationDNS(ctx, reg)
	if err != nil {
		return err
	}
	current, err := dns.GetZone(ctx, cred, reg.Name)
	if errors.Is(err, provider.ErrInfrastructureNotFound) {
		if reg.Sandbox {
			// DNS calls always reach the production account, and a sandbox
			// purchase must leave nothing there.
			return s.sandboxZonePending(ctx, st, reg)
		}
		current, err = dns.CreateZone(ctx, cred, provider.DNSZoneSpec{Name: reg.Name})
	}
	if err != nil {
		return err
	}
	if current.Name != reg.Name || current.AccountID != reg.AccountID {
		return provider.ErrInfrastructureIdentity
	}
	if !current.Verified {
		return ErrPending
	}
	projectIDs := []string{}
	for _, project := range reg.ProjectIDs {
		// A project deleted since the preview loses its grant.
		switch checkErr := domains.DomainProject(ctx, reg.OrgID, project); {
		case checkErr == nil:
			projectIDs = append(projectIDs, project)
		case !errors.Is(checkErr, ErrNotFound):
			return checkErr
		}
	}
	zone, err := s.createManagedZone(ctx, reg.Principal, reg.Name, reg.ConnectionID, projectIDs, true)
	if err != nil {
		return err
	}
	zone.RegistrationID = reg.ID
	if createErr := domains.CreateZone(ctx, zone); createErr != nil {
		return createErr
	}
	s.domainAudit(ctx, reg.Principal, "domain.zone.write", zone.ID, "")
	// A proof that doesn't publish now is retried by the background check.
	if publishErr := s.publishZoneProof(ctx, domains, zone); publishErr != nil {
		s.warn("Publish registered zone proof", publishErr)
	}
	return s.zoneHandedOff(ctx, st, reg, zone)
}

// sandboxZonePending leaves a sandbox registration at zone_pending, as far
// as it can go without a zone someone added to the account themselves.
func (s *Service) sandboxZonePending(ctx context.Context, st RegistrationStore, reg *Registration) error {
	const message = "Sandbox registrations don't create a DNS zone."
	if reg.Message == message {
		return nil
	}
	reg.Message = message
	return s.saveRegistration(ctx, st, reg)
}

func (s *Service) registrationDNS(ctx context.Context, reg *Registration) (provider.DNSAdapter, provider.InfrastructureCredential, error) {
	conn, raw, err := s.Connections.Credential(ctx, reg.OrgID, reg.ConnectionID)
	if err != nil {
		return nil, provider.InfrastructureCredential{}, err
	}
	cred, err := Credential(conn, raw)
	if err != nil {
		return nil, provider.InfrastructureCredential{}, err
	}
	if cred.AccountID != reg.AccountID {
		return nil, provider.InfrastructureCredential{}, provider.ErrInfrastructureIdentity
	}
	dns, err := s.Adapters.DNS(conn.Cloud, provider.DNSZoneDiscovery, provider.DNSZoneCreation)
	return dns, cred, err
}

func (s *Service) zoneHandedOff(ctx context.Context, st RegistrationStore, reg *Registration, zone *Zone) error {
	reg.ZoneID, reg.Status, reg.Message = zone.ID, RegistrationSucceededStatus, ""
	return s.finishRegistration(ctx, st, reg)
}

func registrationDashboard(accountID string) string {
	return "https://dash.cloudflare.com/" + url.PathEscape(accountID) + "/domains/registrations"
}

// finishRegistration saves a final status, then deletes the registrant
// contact. A save lost to another writer keeps the contact for the attempt
// that follows. A delete that fails fails the step, and its retry finds the
// final row and drops the contact then. The outbox keeps one notice per
// status, so a retry can't send it twice.
func (s *Service) finishRegistration(ctx context.Context, st RegistrationStore, reg *Registration) error {
	if err := s.saveRegistration(ctx, st, reg); err != nil {
		return err
	}
	s.registrationAudit(ctx, reg.Principal, "domain.registration."+reg.Status, reg)
	// The status is saved, so a notice that fails to queue is recorded and
	// left to RecoverRegistrations rather than failing the step.
	if err := s.notifyResult(ctx, st, reg); err != nil {
		s.registrationAuditError(ctx, reg.Principal, "domain.registration.notice_pending", reg, err)
	}
	return s.dropRegistrant(ctx, reg)
}

// dropRegistrant deletes a registration's contact from Vault. One that's
// already gone is fine.
func (s *Service) dropRegistrant(ctx context.Context, reg *Registration) error {
	if !reg.CustomRegistrant {
		return nil
	}
	err := s.Secrets.Delete(ctx, reg.OrgID, registrantKey(reg.ID))
	if errors.Is(err, connections.ErrNotFound) {
		return nil
	}
	return err
}

func (s *Service) saveRegistration(ctx context.Context, st RegistrationStore, reg *Registration) error {
	reg.UpdatedAt = s.now().UTC()
	return st.SaveRegistration(ctx, reg)
}

func registrationFailure(err error) string {
	if errors.Is(err, security.ErrForbidden) {
		return "Organization management access was revoked before the domain was bought. Nothing was bought."
	}
	return "Loomplane couldn't check the name and price with the registrar, so nothing was bought. Check the connection and preview the registration again."
}

var (
	codePattern  = regexp.MustCompile(`[^A-Za-z0-9_.-]`)
	emailPattern = regexp.MustCompile(`[^\s@]+@[^\s@]+`)
)

func cleanCode(code string) string {
	code = codePattern.ReplaceAllString(code, "")
	if len(code) > 64 {
		code = code[:64]
	}
	return code
}

// cleanMessage keeps a registrar message safe to store and show: no
// contact details, no control characters, and a bounded length.
func cleanMessage(message string, contact *provider.RegistrantContact) string {
	if contact != nil {
		for _, v := range []string{contact.Email, contact.Phone, contact.Name, contact.Organization, contact.Street, contact.City, contact.PostalCode} {
			if v = strings.TrimSpace(v); len(v) >= 3 {
				message = replaceFold(message, v, "[redacted]")
			}
		}
	}
	message = emailPattern.ReplaceAllString(message, "[redacted]")
	message = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, message)
	if r := []rune(message); len(r) > registrationMaxMessage {
		message = string(r[:registrationMaxMessage])
	}
	return message
}

func replaceFold(s, old, replacement string) string {
	lower, target := strings.ToLower(s), strings.ToLower(old)
	if len(lower) != len(s) {
		return strings.ReplaceAll(s, old, replacement)
	}
	var b strings.Builder
	for {
		i := strings.Index(lower, target)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(replacement)
		s, lower = s[i+len(old):], lower[i+len(old):]
	}
}

func (s *Service) registrationAudit(ctx context.Context, p security.Principal, action string, reg *Registration) {
	s.registrationAuditError(ctx, p, action, reg, nil)
}

// registrationAuditError records err's text with the event. Callers pass
// only errors that carry no secrets, such as outbox failures.
func (s *Service) registrationAuditError(ctx context.Context, p security.Principal, action string, reg *Registration, err error) {
	if s.Audit == nil {
		return
	}
	attributes := map[string]any{"registration_id": reg.ID, "domain": reg.Name, "status": reg.Status}
	severity, outcome := "info", ""
	if err != nil {
		attributes["error"], severity, outcome = err.Error(), "warning", "failure"
	}
	s.Audit.Record(ctx, projects.AuditEvent{Action: action, Severity: severity, Outcome: outcome, Principal: p, Attributes: attributes})
}
