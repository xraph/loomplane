//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/xraph/ledger/provider"
	"github.com/xraph/ledger/provider/stripe"
)

// FeatureUsage is one feature's usage this month, in US cents.
type FeatureUsage struct {
	Feature string
	Cents   int64
}

// Summary is what Settings > Billing shows: the account, this month's
// usage with the included credit applied, and the saved cards.
type Summary struct {
	Account     Account
	PeriodStart time.Time
	PeriodEnd   time.Time
	// Usage has every billed feature, in display order, then any other
	// feature with usage.
	Usage []FeatureUsage
	// BaseCreditUsedCents is the plan's included usage spent: up to $5.00
	// of base usage. AICreditUsedCents is the add-on's: up to $10.00 of AI
	// while the add-on covers the month, else 0. Each is shown against its
	// own credit.
	BaseCreditUsedCents int64
	AICreditUsedCents   int64
	OverageCents        int64
	// ProjectedTotalCents is this month's invoice if the month closed now:
	// the plan fees plus the overage.
	ProjectedTotalCents int64
	PaymentMethods      []provider.PaymentMethod
}

// Summary reads the workspace's billing as of now. A workspace with no
// Stripe customer has no cards.
func (s *Service) Summary(ctx context.Context, orgID string) (Summary, error) {
	a, err := s.Account(ctx, orgID)
	if err != nil {
		return Summary{}, err
	}
	start := monthStart(s.now())
	aiOn := aiCovers(a, start)
	usage, err := s.featureCents(ctx, orgID, start)
	if err != nil {
		return Summary{}, err
	}
	sp := spendOf(usage, aiOn)
	sum := Summary{
		Account: a, PeriodStart: start, PeriodEnd: start.AddDate(0, 1, 0),
		Usage:               orderedUsage(usage),
		BaseCreditUsedCents: min(sp.BaseUsageCents, baseCreditCents),
		OverageCents:        sp.OverageCents,
	}
	if aiOn {
		sum.AICreditUsedCents = min(sp.AIUsageCents, aiCreditCents)
	}
	// A workspace with no card has no subscription yet, so nothing is due.
	if a.CloudSubscription != "" {
		sum.ProjectedTotalCents = cloudBaseCents + sp.OverageCents
		if aiOn {
			sum.ProjectedTotalCents += aiBaseCents
		}
	}
	if sum.PaymentMethods, err = s.paymentMethods(ctx, orgID); err != nil {
		return Summary{}, err
	}
	return sum, nil
}

// paymentMethods lists the workspace's saved cards, live from Stripe.
func (s *Service) paymentMethods(ctx context.Context, orgID string) ([]provider.PaymentMethod, error) {
	if s.payments == nil {
		return nil, errors.New("billing: nothing to list cards with")
	}
	pms, err := s.payments.ListPaymentMethods(ctx, orgID)
	if errors.Is(err, stripe.ErrNoCustomer) {
		return []provider.PaymentMethod{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("billing: list the cards of %s: %w", orgID, err)
	}
	if pms == nil {
		pms = []provider.PaymentMethod{}
	}
	return pms, nil
}

// orderedUsage puts every billed feature first, in display order and at 0
// when it has no usage, then any other feature with usage.
func orderedUsage(usage []FeatureUsage) []FeatureUsage {
	by := make(map[string]int64, len(usage))
	for _, u := range usage {
		by[u.Feature] = u.Cents
	}
	out := make([]FeatureUsage, 0, len(Features)+len(usage))
	for _, f := range Features {
		out = append(out, FeatureUsage{Feature: f, Cents: by[f]})
	}
	for _, u := range usage {
		if !slices.Contains(Features, u.Feature) {
			out = append(out, u)
		}
	}
	return out
}
