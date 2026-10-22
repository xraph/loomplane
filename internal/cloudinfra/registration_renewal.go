package cloudinfra

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/pkg/provider"
)

// RenewalNoticeLead is how long before a domain renews its notice goes out.
const RenewalNoticeLead = 30 * 24 * time.Hour

// registrationRefreshEvery bounds how often the sweep reads a registered
// domain back from the registrar.
const registrationRefreshEvery = 24 * time.Hour

// RenewalNotifier queues domain notices. Each notice has a fixed outbox id,
// so enqueueing one twice, from a retry or a second API replica, adds
// nothing.
type RenewalNotifier interface {
	EnqueueRenewalNotice(context.Context, notifications.RenewalNotice) error
	EnqueueRegistrationResult(context.Context, notifications.RegistrationResultNotice) error
}

// notifiedStatuses are the final statuses a person hears about.
var notifiedStatuses = []string{RegistrationSucceededStatus, RegistrationFailedStatus, RegistrationActionRequiredStatus, RegistrationBlockedStatus, RegistrationPriceChanged, RegistrationUnconfirmed}

// notifyResult tells the organization a registration ended. The status is
// already saved, so a failure here is recorded rather than returned.
func (s *Service) notifyResult(ctx context.Context, reg *Registration) {
	if s.Notices == nil || !slices.Contains(notifiedStatuses, reg.Status) {
		return
	}
	message := reg.Message
	if reg.Status == RegistrationActionRequiredStatus || reg.Status == RegistrationBlockedStatus {
		message = registrationDashboard(reg.AccountID)
	}
	notice := notifications.RegistrationResultNotice{OrgID: reg.OrgID, RegistrationID: reg.ID, Domain: reg.Name, Status: reg.Status, Message: message}
	if err := s.Notices.EnqueueRegistrationResult(ctx, notice); err != nil {
		s.registrationAudit(ctx, reg.Principal, "domain.registration.notice_pending", reg)
	}
}

// SweepRegistrations keeps succeeded registrations current with the
// registrar and queues each renewal notice once. Every API replica runs it:
// the outbox id makes a second enqueue a no-op, and the revision check makes
// the replica that saves second stand down.
func (s *Service) SweepRegistrations(ctx context.Context) error {
	st, err := s.registrationStore()
	if err != nil {
		return err
	}
	regs, err := st.SucceededRegistrations(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, reg := range regs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if sweepErr := s.sweepRegistration(ctx, st, reg); sweepErr != nil {
			errs = append(errs, sweepErr)
		}
	}
	return errors.Join(errs...)
}

// sweepRegistration refreshes one registration at most daily and queues its
// renewal notice when it falls due. A failed refresh keeps the stored
// expiry and auto-renew, and still lets a due notice go out.
func (s *Service) sweepRegistration(ctx context.Context, st RegistrationStore, reg *Registration) error {
	now := s.now().UTC()
	changed := false
	var errs []error
	if now.Sub(reg.RefreshedAt) >= registrationRefreshEvery {
		if err := s.refreshRegistration(ctx, reg); err != nil {
			errs = append(errs, err)
		} else {
			reg.RefreshedAt, changed = now, true
		}
	}
	if s.Notices != nil && reg.AutoRenew && !reg.DomainExpiresAt.IsZero() && !now.Before(reg.DomainExpiresAt.Add(-RenewalNoticeLead)) && !reg.NoticeSentFor.Equal(reg.DomainExpiresAt) {
		notice := notifications.RenewalNotice{OrgID: reg.OrgID, RegistrationID: reg.ID, Domain: reg.Name, RenewsOn: reg.DomainExpiresAt, Currency: reg.Quote.Currency, RenewalCost: reg.Quote.RenewalCost}
		if err := s.Notices.EnqueueRenewalNotice(ctx, notice); err != nil {
			errs = append(errs, err)
		} else {
			reg.NoticeSentFor, changed = reg.DomainExpiresAt, true
		}
	}
	if changed {
		// Losing this save to another replica is fine: its notice is queued.
		if err := s.saveRegistration(ctx, st, reg); err != nil && !errors.Is(err, ErrConflict) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// refreshRegistration reads the domain's expiry and auto-renew from the
// registrar, and its renewal price when the registrar quotes one in the
// registration's currency.
func (s *Service) refreshRegistration(ctx context.Context, reg *Registration) error {
	adapter, cred, err := s.registrationCredential(ctx, reg)
	if err != nil {
		return err
	}
	domain, err := adapter.GetRegisteredDomain(ctx, cred, reg.Name)
	if err != nil {
		return err
	}
	if domain.Name != reg.Name {
		return provider.ErrInfrastructureIdentity
	}
	if !domain.ExpiresAt.IsZero() {
		reg.DomainExpiresAt = domain.ExpiresAt.UTC()
	}
	reg.AutoRenew = domain.AutoRenew
	// The price is a courtesy: without it the notice says so.
	if offers, checkErr := adapter.CheckDomains(ctx, cred, []string{reg.Name}); checkErr == nil {
		if offer, offerErr := checkedOffer(offers, reg.Name); offerErr == nil && offer.Price != nil && offer.Price.RenewalCost != "" && offer.Price.Currency == reg.Quote.Currency {
			reg.Quote.RenewalCost = offer.Price.RenewalCost
		}
	}
	return nil
}
