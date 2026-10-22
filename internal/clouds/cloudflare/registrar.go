package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

var _ provider.DomainRegistrar = (*Client)(nil)

const (
	extensionPageSize    = 50
	extensionMaxPages    = 100
	maxCheckNames        = 20
	maxSearchLimit       = 20
	maxSearchQuery       = 200
	maxErrorMessage      = 500
	updatePolls          = 10
	defaultPollInterval  = 2 * time.Second
	registrarReadScope   = "Registrar:Read"
	registrarWriteScope  = "Registrar:Edit"
	registrationEndpoint = "/registrations"
)

var (
	plainDecimal = regexp.MustCompile(`^\d+(\.\d+)?$`)

	// errUnconfirmed stands in for a transport or decoding failure, whose own
	// text could carry parts of what was sent.
	errUnconfirmed = errors.New("cloudflare did not confirm the registration request")
)

// registrarName is a domain name the registrar API can be asked about:
// lowercase ASCII labels and at least one dot.
func registrarName(name string) error {
	if !strings.Contains(name, ".") {
		return provider.ErrInfrastructureInvalid
	}
	return provider.ValidateDNSName(name, false)
}

// text reads a JSON string or number as the text the provider wrote.
type text string

func (t *text) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case string(b) == "null":
		*t = ""
	case len(b) > 0 && b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*t = text(s)
	case len(b) > 0 && (b[0] == '-' || (b[0] >= '0' && b[0] <= '9')):
		*t = text(b)
	default:
		return errors.New("expected a string or a number")
	}
	return nil
}

// decimal is an amount as a plain decimal string, exactly as quoted. Empty
// means the provider gave none.
func (t text) decimal() (string, error) {
	s := strings.TrimSpace(string(t))
	if s != "" && !plainDecimal.MatchString(s) {
		return "", fmt.Errorf("%w: cloudflare quoted an amount that is not a plain decimal", provider.ErrInfrastructureIdentity)
	}
	return s, nil
}

// stamp is an RFC 3339 time. Empty or null is the zero time.
type stamp struct{ time.Time }

func (s *stamp) UnmarshalJSON(b []byte) error {
	var v *string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	if v == nil || *v == "" {
		s.Time = time.Time{}
		return nil
	}
	t, err := time.Parse(time.RFC3339, *v)
	s.Time = t
	return err
}

type pricingBody struct {
	Currency         string `json:"currency"`
	RegistrationCost text   `json:"registration_cost"`
	RenewalCost      text   `json:"renewal_cost"`
}

type offerBody struct {
	Name        string       `json:"name"`
	Registrable bool         `json:"registrable"`
	Reason      string       `json:"reason"`
	Tier        string       `json:"tier"`
	Pricing     *pricingBody `json:"pricing"`
}

// offers converts an answer to search or check. With asked set, every name
// must be one that was asked about, once.
func offers(in []offerBody, asked []string) ([]provider.DomainOffer, error) {
	out := make([]provider.DomainOffer, 0, len(in))
	seen := map[string]bool{}
	for _, o := range in {
		if registrarName(o.Name) != nil || seen[o.Name] || (asked != nil && !slices.Contains(asked, o.Name)) {
			return nil, provider.ErrInfrastructureIdentity
		}
		seen[o.Name] = true
		offer := provider.DomainOffer{Name: o.Name, Registrable: o.Registrable, Reason: o.Reason, Tier: o.Tier}
		if p := o.Pricing; p != nil {
			registration, err := p.RegistrationCost.decimal()
			if err != nil {
				return nil, err
			}
			renewal, err := p.RenewalCost.decimal()
			if err != nil {
				return nil, err
			}
			// A quote with no currency or registration cost is not a price.
			if p.Currency != "" && registration != "" {
				offer.Price = &provider.DomainPrice{Currency: p.Currency, RegistrationCost: registration, RenewalCost: renewal}
			}
		}
		out = append(out, offer)
	}
	return out, nil
}

// RegistrationExtensions lists the TLDs the registrar API can register,
// sorted. The caller shows these and never hardcodes a list.
func (c *Client) RegistrationExtensions(ctx context.Context, credential provider.InfrastructureCredential) ([]string, error) {
	if err := checkCredential(ctx, credential); err != nil {
		return nil, err
	}
	path := c.registrarPath(credential.AccountID) + "/extensions"
	seen := map[string]bool{}
	var tlds []string
	cursor := ""
	for range extensionMaxPages {
		seen[cursor] = true
		q := url.Values{"per_page": {strconv.Itoa(extensionPageSize)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var items []struct {
			Metadata struct {
				TLD string `json:"tld"`
			} `json:"metadata"`
		}
		info, err := c.send(ctx, credential.Token, http.MethodGet, path, q, nil, &items)
		if err != nil {
			return nil, infrastructureError(err, registrarReadScope)
		}
		for _, item := range items {
			tld := strings.TrimPrefix(item.Metadata.TLD, ".")
			if provider.ValidateDNSName(tld, false) != nil {
				return nil, provider.ErrInfrastructureIdentity
			}
			tlds = append(tlds, tld)
		}
		if info.Cursor == "" {
			slices.Sort(tlds)
			return slices.Compact(tlds), nil
		}
		if seen[info.Cursor] {
			return nil, provider.ErrInfrastructurePagination
		}
		cursor = info.Cursor
	}
	return nil, provider.ErrInfrastructurePagination
}

// SearchDomains suggests names for a keyword. Search is cached discovery:
// buying always goes through CheckDomains first.
func (c *Client) SearchDomains(ctx context.Context, credential provider.InfrastructureCredential, query string, limit int) ([]provider.DomainOffer, error) {
	if err := checkCredential(ctx, credential); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" || len(query) > maxSearchQuery {
		return nil, provider.ErrInfrastructureInvalid
	}
	limit = min(max(limit, 1), maxSearchLimit)
	var out struct {
		Domains []offerBody `json:"domains"`
	}
	q := url.Values{"q": {query}, "limit": {strconv.Itoa(limit)}}
	if err := c.json(ctx, credential.Token, http.MethodGet, c.registrarPath(credential.AccountID)+"/domain-search", q, nil, &out); err != nil {
		return nil, infrastructureError(err, registrarReadScope)
	}
	return offers(out.Domains, nil)
}

// CheckDomains asks the registry for the live availability and price of one
// to twenty names. It changes nothing.
func (c *Client) CheckDomains(ctx context.Context, credential provider.InfrastructureCredential, names []string) ([]provider.DomainOffer, error) {
	if err := checkCredential(ctx, credential); err != nil {
		return nil, err
	}
	if len(names) == 0 || len(names) > maxCheckNames {
		return nil, provider.ErrInfrastructureInvalid
	}
	for _, name := range names {
		if err := registrarName(name); err != nil {
			return nil, err
		}
	}
	var out struct {
		Domains []offerBody `json:"domains"`
	}
	in := map[string][]string{"domains": names}
	if err := c.json(ctx, credential.Token, http.MethodPost, c.registrarPath(credential.AccountID)+"/domain-check", nil, in, &out); err != nil {
		return nil, infrastructureError(err, registrarReadScope)
	}
	return offers(out.Domains, names)
}

type registrationBody struct {
	DomainName  string `json:"domain_name"`
	Status      string `json:"status"`
	ExpiresAt   stamp  `json:"expires_at"`
	AutoRenew   bool   `json:"auto_renew"`
	PrivacyMode string `json:"privacy_mode"`
	Locked      bool   `json:"locked"`
}

// workflowBody is a registration or update workflow, as POST
// /registrations, registration-status and update-status answer.
type workflowBody struct {
	DomainName string `json:"domain_name"`
	State      string `json:"state"`
	Completed  bool   `json:"completed"`
	Context    struct {
		Registration *registrationBody `json:"registration"`
	} `json:"context"`
	Error *struct {
		Code    text   `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Links struct {
		Self string `json:"self"`
	} `json:"links"`
}

func knownState(state string) bool {
	switch state {
	case provider.RegistrationInProgress, provider.RegistrationSucceeded, provider.RegistrationFailed, provider.RegistrationActionRequired, provider.RegistrationBlocked:
		return true
	}
	return false
}

// result is the registration workflow for name. An unknown state is
// ErrInfrastructureAmbiguous: the outcome can't be read.
func (w workflowBody) result(name string) (provider.DomainRegistrationResult, error) {
	if w.DomainName != name {
		return provider.DomainRegistrationResult{}, provider.ErrInfrastructureIdentity
	}
	if !knownState(w.State) {
		return provider.DomainRegistrationResult{}, fmt.Errorf("%w: cloudflare reported a registration state this client doesn't know", provider.ErrInfrastructureAmbiguous)
	}
	res := provider.DomainRegistrationResult{Name: name, State: w.State, Completed: w.Completed}
	if r := w.Context.Registration; r != nil {
		res.ExpiresAt, res.AutoRenew, res.PrivacyMode = r.ExpiresAt.Time, r.AutoRenew, r.PrivacyMode
	}
	if e := w.Error; e != nil {
		res.ErrorCode = string(e.Code)
		res.ErrorMessage = e.Message
		if r := []rune(res.ErrorMessage); len(r) > maxErrorMessage {
			res.ErrorMessage = string(r[:maxErrorMessage])
		}
	}
	return res, nil
}

type contactsBody struct {
	Registrant registrantBody `json:"registrant"`
}

type registrantBody struct {
	Email      string `json:"email"`
	Phone      string `json:"phone"`
	PostalInfo struct {
		Name         string `json:"name"`
		Organization string `json:"organization,omitempty"`
		Address      struct {
			Street      string `json:"street"`
			City        string `json:"city"`
			State       string `json:"state,omitempty"`
			PostalCode  string `json:"postal_code"`
			CountryCode string `json:"country_code"`
		} `json:"address"`
	} `json:"postal_info"`
}

func contacts(c *provider.RegistrantContact) *contactsBody {
	if c == nil {
		return nil
	}
	var r registrantBody
	r.Email, r.Phone = c.Email, c.Phone
	r.PostalInfo.Name, r.PostalInfo.Organization = c.Name, c.Organization
	r.PostalInfo.Address.Street, r.PostalInfo.Address.City, r.PostalInfo.Address.State = c.Street, c.City, c.State
	r.PostalInfo.Address.PostalCode, r.PostalInfo.Address.CountryCode = c.PostalCode, c.CountryCode
	return &contactsBody{Registrant: r}
}

type registerBody struct {
	DomainName  string        `json:"domain_name"`
	AutoRenew   bool          `json:"auto_renew"`
	PrivacyMode string        `json:"privacy_mode,omitempty"`
	Contacts    *contactsBody `json:"contacts,omitempty"`
}

// statusPath is where a registration's workflow is read.
func (c *Client) statusPath(accountID, name string) string {
	return c.registrarPath(accountID) + registrationEndpoint + "/" + url.PathEscape(name) + "/registration-status"
}

// ownStatusLink says whether link is exactly the registration status path
// for name, as a path or as this client's own endpoint plus that path.
func (c *Client) ownStatusLink(link, accountID, name string) bool {
	path := c.statusPath(accountID, name)
	return link == path || link == c.endpoint+path
}

// RegisterDomain buys the domain, billed to the credential's account. It
// posts at most once: any failure after the request could have been sent is
// ErrInfrastructureAmbiguous, and the caller then reconciles with
// ReconcileOnly, which never posts.
func (c *Client) RegisterDomain(ctx context.Context, credential provider.InfrastructureCredential, spec provider.DomainRegistrationSpec) (provider.DomainRegistrationResult, error) {
	if err := checkCredential(ctx, credential); err != nil {
		return provider.DomainRegistrationResult{}, err
	}
	if err := registrarName(spec.Name); err != nil {
		return provider.DomainRegistrationResult{}, err
	}
	if spec.ReconcileOnly {
		return c.reconcile(ctx, credential, spec.Name)
	}
	data, err := json.Marshal(registerBody{DomainName: spec.Name, AutoRenew: spec.AutoRenew, PrivacyMode: spec.PrivacyMode, Contacts: contacts(spec.Registrant)})
	if err != nil {
		return provider.DomainRegistrationResult{}, provider.ErrInfrastructureInvalid
	}
	var w workflowBody
	if err = c.do(ctx, credential.Token, http.MethodPost, c.registrarPath(credential.AccountID)+registrationEndpoint, nil, bytes.NewReader(data), &w); err != nil {
		return provider.DomainRegistrationResult{}, registerFailure(err)
	}
	// From here the registration may exist.
	if !c.ownStatusLink(w.Links.Self, credential.AccountID, spec.Name) {
		return provider.DomainRegistrationResult{}, errors.Join(provider.ErrInfrastructureAmbiguous, provider.ErrInfrastructureIdentity)
	}
	res, err := w.result(spec.Name)
	if err != nil {
		return provider.DomainRegistrationResult{}, errors.Join(provider.ErrInfrastructureAmbiguous, err)
	}
	return res, nil
}

// registerFailure classifies a failed POST. A 4xx other than a conflict
// means Cloudflare refused it. A request that was never built wasn't sent.
// Anything else (a transport error, a 5xx, an answer that can't be read, a
// conflict that says the name may already be taken by this attempt) leaves
// the outcome unknown.
func registerFailure(err error) error {
	var api *APIError
	switch {
	case errors.Is(err, errNotSent):
		return provider.ErrInfrastructureInvalid
	case errors.As(err, &api):
		if api.Status >= 400 && api.Status < 500 && api.Status != http.StatusConflict {
			return infrastructureError(err, registrarWriteScope)
		}
		return errors.Join(provider.ErrInfrastructureAmbiguous, infrastructureError(err, registrarWriteScope))
	}
	unknown := errors.Join(provider.ErrInfrastructureAmbiguous, errUnconfirmed)
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, cause) {
			unknown = errors.Join(unknown, cause)
		}
	}
	return unknown
}

// reconcile reads what an earlier attempt left. It can't tell "never sent"
// from "not visible yet", so a missing registration is ambiguous too.
func (c *Client) reconcile(ctx context.Context, credential provider.InfrastructureCredential, name string) (provider.DomainRegistrationResult, error) {
	res, err := c.RegistrationStatus(ctx, credential, name)
	switch {
	case err == nil:
		res.Reconciled = true
		return res, nil
	case errors.Is(err, provider.ErrInfrastructureScope), errors.Is(err, provider.ErrInfrastructureInvalid), errors.Is(err, provider.ErrInfrastructureAmbiguous),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return provider.DomainRegistrationResult{}, err
	}
	return provider.DomainRegistrationResult{}, errors.Join(provider.ErrInfrastructureAmbiguous, err)
}

// RegistrationStatus reads the registration workflow for name.
func (c *Client) RegistrationStatus(ctx context.Context, credential provider.InfrastructureCredential, name string) (provider.DomainRegistrationResult, error) {
	if err := checkCredential(ctx, credential); err != nil {
		return provider.DomainRegistrationResult{}, err
	}
	if err := registrarName(name); err != nil {
		return provider.DomainRegistrationResult{}, err
	}
	var w workflowBody
	if err := c.json(ctx, credential.Token, http.MethodGet, c.statusPath(credential.AccountID, name), nil, nil, &w); err != nil {
		return provider.DomainRegistrationResult{}, infrastructureError(err, registrarReadScope)
	}
	return w.result(name)
}

// GetRegisteredDomain reads a domain the account holds.
func (c *Client) GetRegisteredDomain(ctx context.Context, credential provider.InfrastructureCredential, name string) (provider.RegisteredDomain, error) {
	if err := checkCredential(ctx, credential); err != nil {
		return provider.RegisteredDomain{}, err
	}
	if err := registrarName(name); err != nil {
		return provider.RegisteredDomain{}, err
	}
	var r registrationBody
	if err := c.json(ctx, credential.Token, http.MethodGet, c.registrarPath(credential.AccountID)+registrationEndpoint+"/"+url.PathEscape(name), nil, nil, &r); err != nil {
		return provider.RegisteredDomain{}, infrastructureError(err, registrarReadScope)
	}
	if r.DomainName != name {
		return provider.RegisteredDomain{}, provider.ErrInfrastructureIdentity
	}
	return provider.RegisteredDomain{Name: name, Status: r.Status, ExpiresAt: r.ExpiresAt.Time, AutoRenew: r.AutoRenew, PrivacyMode: r.PrivacyMode, Locked: r.Locked}, nil
}

// SetAutoRenew turns renewal on or off. Cloudflare may take the change as
// a workflow (202), which is polled until it ends. The domain is then read
// back, so the answer is what Cloudflare reports and not what was asked.
func (c *Client) SetAutoRenew(ctx context.Context, credential provider.InfrastructureCredential, name string, enabled bool) (provider.RegisteredDomain, error) {
	if err := checkCredential(ctx, credential); err != nil {
		return provider.RegisteredDomain{}, err
	}
	if err := registrarName(name); err != nil {
		return provider.RegisteredDomain{}, err
	}
	data, err := json.Marshal(map[string]bool{"auto_renew": enabled})
	if err != nil {
		return provider.RegisteredDomain{}, provider.ErrInfrastructureInvalid
	}
	path := c.registrarPath(credential.AccountID) + registrationEndpoint + "/" + url.PathEscape(name)
	info, err := c.send(ctx, credential.Token, http.MethodPatch, path, nil, bytes.NewReader(data), nil)
	if err != nil {
		return provider.RegisteredDomain{}, infrastructureError(err, registrarWriteScope)
	}
	if info.Status == http.StatusAccepted {
		if waitErr := c.awaitUpdate(ctx, credential, name, path+"/update-status"); waitErr != nil {
			return provider.RegisteredDomain{}, waitErr
		}
	}
	domain, err := c.GetRegisteredDomain(ctx, credential, name)
	if err != nil {
		return provider.RegisteredDomain{}, err
	}
	if domain.AutoRenew != enabled {
		return provider.RegisteredDomain{}, fmt.Errorf("%w: cloudflare doesn't report the auto-renew change yet", provider.ErrInfrastructureAmbiguous)
	}
	return domain, nil
}

// awaitUpdate polls an update workflow: updatePolls reads, pollInterval
// apart.
func (c *Client) awaitUpdate(ctx context.Context, credential provider.InfrastructureCredential, name, path string) error {
	interval := c.pollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	for poll := range updatePolls {
		if poll > 0 {
			wait := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				wait.Stop()
				return ctx.Err()
			case <-wait.C:
			}
		}
		var w workflowBody
		if err := c.json(ctx, credential.Token, http.MethodGet, path, nil, nil, &w); err != nil {
			return infrastructureError(err, registrarReadScope)
		}
		if w.DomainName != "" && w.DomainName != name {
			return provider.ErrInfrastructureIdentity
		}
		switch w.State {
		case provider.RegistrationSucceeded:
			return nil
		case provider.RegistrationInProgress:
		case provider.RegistrationFailed, provider.RegistrationBlocked, provider.RegistrationActionRequired:
			return fmt.Errorf("cloudflare: the auto-renew change ended as %s", w.State)
		default:
			return fmt.Errorf("%w: cloudflare reported an update state this client doesn't know", provider.ErrInfrastructureAmbiguous)
		}
	}
	return fmt.Errorf("%w: the auto-renew change is still in progress", provider.ErrInfrastructureAmbiguous)
}
