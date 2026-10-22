package cloudinfra

import (
	"context"
	"errors"
	"fmt"
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

// registrationRenotifyWindow bounds how far back recovery looks for a
// result notice that failed to queue.
const registrationRenotifyWindow = 7 * 24 * time.Hour

// notifiedStatuses are the final statuses a person hears about.
var notifiedStatuses = []string{RegistrationSucceededStatus, RegistrationFailedStatus, RegistrationActionRequiredStatus, RegistrationBlockedStatus, RegistrationPriceChanged, RegistrationUnconfirmed}

// notifyResult queues the notice that a registration ended, once per final
// status, and records that it did. Until it does, ResultNotifiedFor trails
// the status and RecoverRegistrations tries again.
func (s *Service) notifyResult(ctx context.Context, st RegistrationStore, reg *Registration) error {
	if s.Notices == nil || !slices.Contains(notifiedStatuses, reg.Status) || reg.ResultNotifiedFor == reg.Status {
		return nil
	}
	message := reg.Message
	if reg.Status == RegistrationActionRequiredStatus || reg.Status == RegistrationBlockedStatus {
		message = registrationDashboard(reg.AccountID)
	}
	notice := notifications.RegistrationResultNotice{OrgID: reg.OrgID, RegistrationID: reg.ID, Domain: reg.Name, Status: reg.Status, Message: message}
	if err := s.Notices.EnqueueRegistrationResult(ctx, notice); err != nil {
		return fmt.Errorf("queue the %s notice for %s: %w", reg.Status, reg.Name, err)
	}
	reg.ResultNotifiedFor = reg.Status
	// Losing this save only means recovery queues the same notice again,
	// which its outbox id turns into a no-op.
	if err := s.saveRegistration(ctx, st, reg); err != nil && !errors.Is(err, ErrConflict) {
		return err
	}
	return nil
}

// renotifyResults queues the result notices that failed to queue when their
// registration ended.
func (s *Service) renotifyResults(ctx context.Context, st RegistrationStore) error {
	if s.Notices == nil {
		return nil
	}
	regs, err := st.UnnotifiedRegistrations(ctx, s.now().Add(-registrationRenotifyWindow))
	if err != nil {
		return err
	}
	var errs []error
	for _, reg := range regs {
		if notifyErr := s.notifyResult(ctx, st, reg); notifyErr != nil {
			errs = append(errs, notifyErr)
		}
	}
	return errors.Join(errs...)
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
	// A date already passed would read "renews on" a day in the past.
	if s.Notices != nil && reg.AutoRenew && !reg.DomainExpiresAt.IsZero() && !now.Before(reg.DomainExpiresAt.Add(-RenewalNoticeLead)) && now.Before(reg.DomainExpiresAt) && !reg.NoticeSentFor.Equal(reg.DomainExpiresAt) {
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
