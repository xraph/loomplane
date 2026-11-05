//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/xraph/loomplane/internal/pglock"
	"github.com/xraph/loomplane/internal/platformcloud"
)

// What each month's subscriptions include, in US cents: the base plan's
// usage credit and the AI add-on's.
const (
	baseCreditCents = 500
	aiCreditCents   = 1000
)

// maxSpendingLimitCents is the highest limit an owner can set: $1,000,000.00.
const maxSpendingLimitCents = 100_000_000

// evaluateLock is the advisory lock, per workspace, that one evaluation
// holds from reading the account to the last suspend or restore, so two
// evaluations never decide on the same state or undo each other's call.
const evaluateLock = "loomplane:billing:evaluate:"

// PeriodSpend is a workspace's usage in one month, in US cents.
type PeriodSpend struct {
	BaseUsageCents int64 // every non-AI feature, priced, this month
	AIUsageCents   int64
	OverageCents   int64 // max(0, base-500) + (AI on ? max(0, ai-1000) : ai)
}

// Spend totals the month from Loomplane's own usage rows, pushed or not,
// so the limit sees usage before ledger does.
//
// Each feature is rounded down to the cent on its own, as its push carries
// what is under a cent, so the totals are what the month's invoice bills.
// The AI credit counts when the account's add-on covers the month, which
// is exact for the month in progress; a closed month is on its invoice.
func (s *Service) Spend(ctx context.Context, orgID string, month time.Time) (PeriodSpend, error) {
	a, err := s.Account(ctx, orgID)
	if err != nil {
		return PeriodSpend{}, err
	}
	start := monthStart(month)
	return s.spend(ctx, orgID, start, aiCovers(a, start))
}

// aiCovers says whether the account's AI add-on runs in the month that
// starts at month. One turned off runs until its month ends.
func aiCovers(a Account, month time.Time) bool {
	return a.AISubscription != "" && (a.AIEndsAt == nil || a.AIEndsAt.After(month))
}

func (s *Service) spend(ctx context.Context, orgID string, start time.Time, aiOn bool) (PeriodSpend, error) {
	usage, err := s.featureCents(ctx, orgID, start)
	if err != nil {
		return PeriodSpend{}, err
	}
	return spendOf(usage, aiOn), nil
}

// spendOf totals a month's per-feature usage into PeriodSpend.
func spendOf(usage []FeatureUsage, aiOn bool) PeriodSpend {
	var sp PeriodSpend
	for _, u := range usage {
		if u.Feature == FeatureAIUSD {
			sp.AIUsageCents += u.Cents
		} else {
			sp.BaseUsageCents += u.Cents
		}
	}
	sp.OverageCents = max(0, sp.BaseUsageCents-baseCreditCents)
	if aiOn {
		sp.OverageCents += max(0, sp.AIUsageCents-aiCreditCents)
	} else {
		sp.OverageCents += sp.AIUsageCents
	}
	return sp
}

// featureCents is the month's usage from start, one entry per feature with
// any, each rounded down to the cent on its own, as its push is.
func (s *Service) featureCents(ctx context.Context, orgID string, start time.Time) ([]FeatureUsage, error) {
	rows, err := s.pg.Query(ctx, `SELECT feature, sum(micro_usd)::bigint FROM loomplane_billing_usage
		WHERE org_id = $1 AND occurred_at >= $2 AND occurred_at < $3 GROUP BY feature ORDER BY feature`,
		orgID, start, start.AddDate(0, 1, 0))
	if err != nil {
		return nil, fmt.Errorf("billing: read the usage of %s: %w", orgID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []FeatureUsage
	for rows.Next() {
		var (
			u     FeatureUsage
			micro int64
		)
		if err = rows.Scan(&u.Feature, &micro); err != nil {
			return nil, fmt.Errorf("billing: read the usage of %s: %w", orgID, err)
		}
		u.Cents = micro / microUSDPerCent
		out = append(out, u)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("billing: read the usage of %s: %w", orgID, err)
	}
	return out, nil
}

// limitReached says whether spend has reached the limit. The included
// credit has to be used up first, so a limit of 0 stops work once the
// credit is used, not at $0.
func limitReached(limit *int64, sp PeriodSpend, aiOn bool) bool {
	return limit != nil && sp.OverageCents >= *limit &&
		(sp.OverageCents > 0 || sp.BaseUsageCents >= baseCreditCents || (aiOn && sp.AIUsageCents >= aiCreditCents))
}

// ErrLimitNotChecked is SetSpendingLimit's error when the limit is saved
// and the evaluation after it failed or ran out of time. The next
// evaluation, within 15 minutes, applies it.
var ErrLimitNotChecked = errors.New("billing: the spending limit is saved, but checking it failed")

// SetSpendingLimit stores the limit (nil clears it) and evaluates at once.
//
// The limit is saved even when the evaluation fails; the error says so,
// and the next evaluation applies it. A suspend or restore platform Cloud
// couldn't finish is logged, not returned: the limit and the status are
// right, and the next evaluation finishes the call.
func (s *Service) SetSpendingLimit(ctx context.Context, orgID string, cents *int64) (Account, error) {
	if err := s.ready(); err != nil {
		return Account{}, err
	}
	if orgID == "" {
		return Account{}, errors.New("billing: a spending limit needs a workspace")
	}
	if cents != nil && (*cents < 0 || *cents > maxSpendingLimitCents) {
		return Account{}, fmt.Errorf("billing: a spending limit is between %s and %s", usd(0), usd(maxSpendingLimitCents))
	}
	defer s.forget(orgID)
	if _, err := s.pg.Exec(ctx, `INSERT INTO loomplane_billing_accounts (org_id, spending_limit_cents, updated_at) VALUES ($1, $2, $3)
		ON CONFLICT (org_id) DO UPDATE SET spending_limit_cents = EXCLUDED.spending_limit_cents, updated_at = EXCLUDED.updated_at`,
		orgID, cents, s.now().UTC()); err != nil {
		return Account{}, fmt.Errorf("billing: save the spending limit of %s: %w", orgID, err)
	}
	effects, err := s.evaluate(ctx, orgID)
	if err != nil {
		return Account{}, fmt.Errorf("%w: %s: %w", ErrLimitNotChecked, orgID, err)
	}
	if effects != nil {
		slog.WarnContext(ctx, "billing: platform Cloud didn't finish applying a spending limit; the next evaluation retries",
			"org", orgID, "error", effects)
	}
	return s.Account(ctx, orgID)
}

// Evaluate settles limited and past_due for one workspace as of now:
// stores or clears limited_period, suspends or restores platform Cloud
// work with the matching reason, and sends each notice once.
//
// A read that fails changes nothing: no suspend, no restore. Once the
// state is settled every suspend and restore is made, even if one before
// it failed, and the errors come back together. The calls are safe to
// repeat, so the next evaluation retries whatever didn't finish. A
// workspace with no card yet has nothing to settle.
func (s *Service) Evaluate(ctx context.Context, orgID string) error {
	effects, err := s.evaluate(ctx, orgID)
	return errors.Join(err, effects)
}

// evaluate is Evaluate with the two kinds of failure apart: err is a state
// that couldn't be settled; effects are platform Cloud calls that failed
// after it was.
func (s *Service) evaluate(ctx context.Context, orgID string) (effects, err error) {
	if readyErr := s.ready(); readyErr != nil {
		return nil, readyErr
	}
	if orgID == "" {
		return nil, errors.New("billing: an evaluation needs a workspace")
	}
	err = pglock.Do(ctx, s.pg, evaluateLock+orgID, func(ctx context.Context) error {
		var settleErr error
		effects, settleErr = s.settle(ctx, orgID)
		return settleErr
	})
	return effects, err
}

// settle is one evaluation, under the workspace's evaluation lock.
func (s *Service) settle(ctx context.Context, orgID string) (effects, err error) {
	now := s.now().UTC()
	month := monthStart(now)
	a, err := s.Account(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if !a.CardReady {
		return nil, nil
	}
	aiOn := aiCovers(a, month)
	sp, err := s.spend(ctx, orgID, month, aiOn)
	if err != nil {
		return nil, err
	}
	reached := limitReached(a.SpendingLimitCents, sp, aiOn)
	limitedNow := a.LimitedPeriod != nil && a.LimitedPeriod.Equal(month)

	// The state first, so the gate refuses platform work and platform-key
	// AI before anything is stopped.
	var (
		epoch            int64
		limited, cleared bool
	)
	switch {
	case reached && !limitedNow:
		err = s.pg.QueryRow(ctx, `UPDATE loomplane_billing_accounts SET limited_period = $2, limit_epoch = limit_epoch + 1, updated_at = $3
			WHERE org_id = $1 RETURNING limit_epoch`, orgID, month, now).Scan(&epoch)
		limited = true
	case !reached && a.LimitedPeriod != nil:
		// The limit was raised or cleared, or a new month began.
		err = s.pg.QueryRow(ctx, `UPDATE loomplane_billing_accounts SET limited_period = NULL, updated_at = $2
			WHERE org_id = $1 RETURNING limit_epoch`, orgID, now).Scan(&epoch)
		cleared = true
	}
	if limited || cleared {
		s.forget(orgID)
	}
	if err != nil {
		return nil, fmt.Errorf("billing: settle the spending limit of %s: %w", orgID, err)
	}

	// Then platform Cloud, with one reason each. Reasons stack there, so
	// paying doesn't lift a limit still reached, nor raising the limit a
	// payment still owed. Restoring a reason that isn't there changes
	// nothing, so a restore that failed last time is made again.
	pastDue := a.Status == StatusPastDue
	var errs []error
	if reached {
		errs = append(errs, s.platform(ctx, orgID, platformcloud.ReasonSpendingLimit, true))
	} else {
		restoreErr := s.platform(ctx, orgID, platformcloud.ReasonSpendingLimit, false)
		// No tenant means nothing restarts and platform Cloud says nothing,
		// so billing tells the admins AI is back, unless a payment owed
		// still holds it.
		if cleared && errors.Is(restoreErr, platformcloud.ErrNotEnabled) && !pastDue {
			s.sendRestored(ctx, orgID, epoch)
		}
		errs = append(errs, restoreErr)
	}
	errs = append(errs, s.platform(ctx, orgID, platformcloud.ReasonPastDue, pastDue))

	if limited {
		s.sendLimited(ctx, orgID, month, epoch)
	}
	s.notices(ctx, orgID, month, a.SpendingLimitCents, sp, reached)

	for i, e := range errs {
		if errors.Is(e, platformcloud.ErrNotEnabled) {
			errs[i] = nil // a workspace with no platform tenant has nothing to stop
		}
	}
	return errors.Join(errs...), nil
}

// platform suspends (stop) or restores the workspace's platform Cloud work
// for one reason.
func (s *Service) platform(ctx context.Context, orgID string, reason platformcloud.SuspendReason, stop bool) error {
	if s.suspender == nil {
		return fmt.Errorf("billing: nothing to suspend or restore platform Cloud work with (%s)", reason)
	}
	if stop {
		return s.suspender.Suspend(ctx, orgID, reason)
	}
	return s.suspender.Restore(ctx, orgID, reason)
}

// EvaluateAll runs Evaluate for every account row. Rows with no card are
// left out: Evaluate does nothing for them. One workspace that fails
// doesn't stop the others; the errors come back together.
func (s *Service) EvaluateAll(ctx context.Context) error {
	if err := s.ready(); err != nil {
		return err
	}
	rows, err := s.pg.Query(ctx, `SELECT org_id FROM loomplane_billing_accounts WHERE card_ready ORDER BY org_id`)
	if err != nil {
		return fmt.Errorf("billing: list the accounts to evaluate: %w", err)
	}
	var orgs []string
	for rows.Next() {
		var org string
		if err = rows.Scan(&org); err != nil {
			_ = rows.Close()
			return fmt.Errorf("billing: list the accounts to evaluate: %w", err)
		}
		orgs = append(orgs, org)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return fmt.Errorf("billing: list the accounts to evaluate: %w", err)
	}
	var errs []error
	for _, org := range orgs {
		if ctxErr := ctx.Err(); ctxErr != nil {
			errs = append(errs, ctxErr)
			break
		}
		if evalErr := s.Evaluate(ctx, org); evalErr != nil {
			slog.ErrorContext(ctx, "billing: evaluating a workspace failed", "org", org, "error", evalErr)
			errs = append(errs, fmt.Errorf("billing: evaluate %s: %w", org, evalErr))
		}
	}
	return errors.Join(errs...)
}
