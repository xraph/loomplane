package provider

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// DomainRegistration is the registrar capability. It is declared apart from
// DNS: an account can manage zones without being able to buy domains.
const DomainRegistration InfrastructureCapability = "domain:register"

// Registration states reported by a registrar.
const (
	RegistrationInProgress     = "in_progress"
	RegistrationSucceeded      = "succeeded"
	RegistrationFailed         = "failed"
	RegistrationActionRequired = "action_required"
	RegistrationBlocked        = "blocked"
)

// DomainPrice holds decimal amounts exactly as the provider quotes them.
type DomainPrice struct {
	Currency         string `json:"currency"`
	RegistrationCost string `json:"registration_cost"`
	RenewalCost      string `json:"renewal_cost"`
}

// DomainOffer is one availability and price answer for a name.
type DomainOffer struct {
	Name        string       `json:"name"`
	Registrable bool         `json:"registrable"`
	Reason      string       `json:"reason,omitempty"`
	Tier        string       `json:"tier,omitempty"`
	Price       *DomainPrice `json:"price,omitempty"`
}

// RegistrantContact is the WHOIS contact. It is stored in Vault and never
// formatted into logs or errors.
type RegistrantContact struct {
	Email        string `json:"email"`
	Phone        string `json:"phone"`
	Name         string `json:"name"`
	Organization string `json:"organization"`
	Street       string `json:"street"`
	City         string `json:"city"`
	State        string `json:"state"`
	PostalCode   string `json:"postal_code"`
	CountryCode  string `json:"country_code"`
}

func (RegistrantContact) String() string { return "[registrant contact]" }

// GoString keeps contact details out of diagnostic formatting too.
func (c RegistrantContact) GoString() string { return c.String() }

// DomainRegistrationSpec is one purchase request. ReconcileOnly asks the
// adapter to look up the outcome of an earlier attempt and never to buy.
type DomainRegistrationSpec struct {
	OperationID   string             `json:"operation_id"`
	Name          string             `json:"name"`
	AutoRenew     bool               `json:"auto_renew"`
	PrivacyMode   string             `json:"privacy_mode"`
	Registrant    *RegistrantContact `json:"-"`
	ReconcileOnly bool               `json:"-"`
}

// DomainRegistrationResult is the registrar's view of a registration.
type DomainRegistrationResult struct {
	Name         string    `json:"name"`
	State        string    `json:"state"`
	Completed    bool      `json:"completed"`
	ExpiresAt    time.Time `json:"expires_at"`
	AutoRenew    bool      `json:"auto_renew"`
	PrivacyMode  string    `json:"privacy_mode"`
	ErrorCode    string    `json:"error_code"`
	ErrorMessage string    `json:"error_message"`
	Reconciled   bool      `json:"reconciled"`
}

// RegisteredDomain is a domain the account already holds.
type RegisteredDomain struct {
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	ExpiresAt   time.Time `json:"expires_at"`
	AutoRenew   bool      `json:"auto_renew"`
	PrivacyMode string    `json:"privacy_mode"`
	Locked      bool      `json:"locked"`
}

// DomainRegistrar buys and inspects domains through the user's own account.
type DomainRegistrar interface {
	RegistrationExtensions(context.Context, InfrastructureCredential) ([]string, error)
	SearchDomains(context.Context, InfrastructureCredential, string, int) ([]DomainOffer, error)
	CheckDomains(context.Context, InfrastructureCredential, []string) ([]DomainOffer, error)
	RegisterDomain(context.Context, InfrastructureCredential, DomainRegistrationSpec) (DomainRegistrationResult, error)
	RegistrationStatus(context.Context, InfrastructureCredential, string) (DomainRegistrationResult, error)
	GetRegisteredDomain(context.Context, InfrastructureCredential, string) (RegisteredDomain, error)
	SetAutoRenew(context.Context, InfrastructureCredential, string, bool) (RegisteredDomain, error)
}

// MatchDomainPrice reports drift unless the currency and registration cost are
// equal as decimals. A missing or unparsable cost on either side is drift.
func MatchDomainPrice(approved, current DomainPrice) error {
	if strings.TrimSpace(approved.Currency) != strings.TrimSpace(current.Currency) {
		return fmt.Errorf("%w: domain price currency changed", ErrInfrastructureDrift)
	}
	a, ok := new(big.Rat).SetString(strings.TrimSpace(approved.RegistrationCost))
	if !ok {
		return fmt.Errorf("%w: approved domain price is not a decimal", ErrInfrastructureDrift)
	}
	c, ok := new(big.Rat).SetString(strings.TrimSpace(current.RegistrationCost))
	if !ok {
		return fmt.Errorf("%w: current domain price is not a decimal", ErrInfrastructureDrift)
	}
	if a.Cmp(c) != 0 {
		return fmt.Errorf("%w: domain price changed", ErrInfrastructureDrift)
	}
	return nil
}
