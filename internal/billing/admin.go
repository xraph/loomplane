//go:build cloud

package billing

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xraph/ledger/invoice"
)

// What the platform owner's billing admin reads: the Stripe mode, the
// plans, every workspace's billing and a workspace's invoices with what was
// refunded. Only the platform owner reaches it (extensions/billing).

// StripeMode is which Stripe the secret key opens, read from its prefix.
// The key itself never leaves the server.
type StripeMode string

const (
	StripeTest StripeMode = "test"
	StripeLive StripeMode = "live"
)

// PlanPrice is a plan as the admin shows it, in cents.
type PlanPrice struct {
	Slug        string
	BaseCents   int64
	CreditCents int64
}

// Overview is the Stripe mode and the plans as ledger has them.
type Overview struct {
	StripeMode StripeMode
	Plans      []PlanPrice
}

// Overview reads the plans from ledger and the Stripe mode from the key's
// prefix: a live secret or restricted key is live, anything else test.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	if err := s.ready(); err != nil {
		return Overview{}, err
	}
	out := Overview{StripeMode: stripeMode(s.cfg.StripeSecretKey)}
	for _, slug := range []string{PlanCloud, PlanAI} {
		p, err := s.l.GetPlanBySlug(ctx, slug, s.appID)
		if err != nil {
			return Overview{}, fmt.Errorf("billing: read the %s plan: %w", slug, err)
		}
		pp := PlanPrice{Slug: slug}
		if p.Pricing != nil {
			pp.BaseCents = p.Pricing.BaseAmount.Amount
		}
		for _, c := range p.Credits {
			pp.CreditCents += c.Amount.Amount
		}
		out.Plans = append(out.Plans, pp)
	}
	return out, nil
}

func stripeMode(key string) StripeMode {
	if strings.HasPrefix(key, "sk_live_") || strings.HasPrefix(key, "rk_live_") {
		return StripeLive
	}
	return StripeTest
}

// WorkspaceBilling is one workspace's billing this month.
type WorkspaceBilling struct {
	OrgID string
	Name  string // "" for a workspace deleted since it was billed
	// Status is as Account reports it.
	Status Status
	// PeriodTotalCents is this month's invoices if the month closed now:
	// the plan fees plus the overage. 0 with no card.
	PeriodTotalCents int64
	OverageCents     int64
	AIEnabled        bool
}

// Workspaces is every workspace with its billing this month, by name: each
// of the app's workspaces, billed or not, and each one deleted since it
// was billed. It reads two queries, whatever the number of workspaces, and
// never Stripe.
func (s *Service) Workspaces(ctx context.Context) ([]WorkspaceBilling, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	start := monthStart(now)
	usage, err := s.allFeatureCents(ctx, start)
	if err != nil {
		return nil, err
	}
	rows, err := s.pg.Query(ctx, `SELECT COALESCE(o.id, a.org_id), COALESCE(o.name, ''),
			COALESCE(a.card_ready, false), COALESCE(a.cloud_subscription_id, ''), COALESCE(a.ai_subscription_id, ''),
			a.ai_ends_at, a.limited_period,
			EXISTS (SELECT 1 FROM loomplane_billing_invoices i
				WHERE i.org_id = a.org_id AND i.paid_at IS NULL AND i.payment_failed_at IS NOT NULL
					AND i.payment_failed_at <= $2)
		FROM (SELECT id, name FROM authsome_organizations WHERE app_id = $1) o
		FULL OUTER JOIN loomplane_billing_accounts a ON a.org_id = o.id
		ORDER BY 2, 1`, s.appID, now.Add(-pastDueAfter))
	if err != nil {
		return nil, fmt.Errorf("billing: list the workspaces' billing: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []WorkspaceBilling
	for rows.Next() {
		var (
			a       Account
			name    string
			pastDue bool
		)
		if err = rows.Scan(&a.OrgID, &name, &a.CardReady, &a.CloudSubscription, &a.AISubscription,
			&a.AIEndsAt, &a.LimitedPeriod, &pastDue); err != nil {
			return nil, fmt.Errorf("billing: list the workspaces' billing: %w", err)
		}
		a.derive(pastDue, now)
		aiOn := aiCovers(a, start)
		sp := spendOf(usage[a.OrgID], aiOn)
		w := WorkspaceBilling{OrgID: a.OrgID, Name: name, Status: a.Status, OverageCents: sp.OverageCents, AIEnabled: a.AIEnabled}
		// As Summary has it: with no subscription nothing is due.
		if a.CloudSubscription != "" {
			w.PeriodTotalCents = cloudBaseCents + sp.OverageCents
			if aiOn {
				w.PeriodTotalCents += aiBaseCents
			}
		}
		out = append(out, w)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("billing: list the workspaces' billing: %w", err)
	}
	return out, nil
}

// allFeatureCents is featureCents for every workspace at once: the month's
// usage from start by workspace, each feature rounded down to the cent on
// its own.
func (s *Service) allFeatureCents(ctx context.Context, start time.Time) (map[string][]FeatureUsage, error) {
	rows, err := s.pg.Query(ctx, `SELECT org_id, feature, sum(micro_usd)::bigint FROM loomplane_billing_usage
		WHERE occurred_at >= $1 AND occurred_at < $2 GROUP BY org_id, feature`, start, start.AddDate(0, 1, 0))
	if err != nil {
		return nil, fmt.Errorf("billing: read every workspace's usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]FeatureUsage{}
	for rows.Next() {
		var (
			org   string
			u     FeatureUsage
			micro int64
		)
		if err = rows.Scan(&org, &u.Feature, &micro); err != nil {
			return nil, fmt.Errorf("billing: read every workspace's usage: %w", err)
		}
		u.Cents = micro / microUSDPerCent
		out[org] = append(out[org], u)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("billing: read every workspace's usage: %w", err)
	}
	return out, nil
}

// PlatformInvoice is one of a workspace's invoices as the platform owner
// sees it: what the workspace sees, and what was refunded.
type PlatformInvoice struct {
	InvoiceSummary
	// RefundedCents is every refund Stripe made of it.
	RefundedCents int64
	// RefundableCents is what is left to refund: the total less every
	// refund recorded, Stripe's answer pending or not. 0 for an invoice
	// that isn't paid or was never sent to Stripe.
	RefundableCents int64
	// PendingRefunds are the refunds Stripe never confirmed, oldest first.
	PendingRefunds []PendingRefund
}

// PendingRefund is a refund recorded and never confirmed by Stripe. One
// that is Resendable is sent again by a Refund with its key; one that
// isn't is past Stripe's idempotency window, and someone checks Stripe and
// settles it by hand.
type PendingRefund struct {
	RefundKey  string
	Cents      int64
	CreatedAt  time.Time
	Resendable bool
}

// PlatformInvoices lists a workspace's invoices, newest first, with what
// was refunded of each. A workspace with no invoices, or none at all, is
// an empty list.
func (s *Service) PlatformInvoices(ctx context.Context, orgID string) ([]PlatformInvoice, error) {
	invs, err := s.Invoices(ctx, orgID)
	if err != nil {
		return nil, err
	}
	type refunds struct{ sent, recorded int64 }
	by := map[string]refunds{}
	providers := map[string]bool{}
	rows, err := s.pg.Query(ctx, `SELECT invoice_id, COALESCE(sum(cents) FILTER (WHERE sent_at IS NOT NULL), 0)::bigint,
			COALESCE(sum(cents), 0)::bigint
		FROM loomplane_billing_refunds WHERE org_id = $1 GROUP BY invoice_id`, orgID)
	if err != nil {
		return nil, fmt.Errorf("billing: read the refunds of %s: %w", orgID, err)
	}
	for rows.Next() {
		var (
			invID string
			r     refunds
		)
		if err = rows.Scan(&invID, &r.sent, &r.recorded); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("billing: read the refunds of %s: %w", orgID, err)
		}
		by[invID] = r
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, fmt.Errorf("billing: read the refunds of %s: %w", orgID, err)
	}
	prow, err := s.pg.Query(ctx, `SELECT invoice_id FROM loomplane_billing_invoices WHERE org_id = $1 AND provider_id <> ''`, orgID)
	if err != nil {
		return nil, fmt.Errorf("billing: read which invoices of %s Stripe has: %w", orgID, err)
	}
	for prow.Next() {
		var invID string
		if err = prow.Scan(&invID); err != nil {
			_ = prow.Close()
			return nil, fmt.Errorf("billing: read which invoices of %s Stripe has: %w", orgID, err)
		}
		providers[invID] = true
	}
	err = prow.Err()
	_ = prow.Close()
	if err != nil {
		return nil, fmt.Errorf("billing: read which invoices of %s Stripe has: %w", orgID, err)
	}
	pending, err := s.pendingRefunds(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]PlatformInvoice, 0, len(invs))
	for _, inv := range invs {
		r := by[inv.ID]
		pi := PlatformInvoice{InvoiceSummary: inv, RefundedCents: r.sent, PendingRefunds: pending[inv.ID]}
		if inv.Status == string(invoice.StatusPaid) && providers[inv.ID] {
			pi.RefundableCents = max(0, inv.TotalCents-r.recorded)
		}
		out = append(out, pi)
	}
	return out, nil
}

// pendingRefunds is the workspace's unconfirmed refunds by invoice. The
// Stripe invoice behind each is the one Loomplane recorded, as Refund
// reads it.
func (s *Service) pendingRefunds(ctx context.Context, orgID string) (map[string][]PendingRefund, error) {
	rows, err := s.pg.Query(ctx, `SELECT invoice_id, refund_key, COALESCE(cents, 0), created_at FROM loomplane_billing_refunds
		WHERE org_id = $1 AND sent_at IS NULL ORDER BY created_at, refund_key`, orgID)
	if err != nil {
		return nil, fmt.Errorf("billing: read the unconfirmed refunds of %s: %w", orgID, err)
	}
	defer func() { _ = rows.Close() }()
	now := s.now()
	out := map[string][]PendingRefund{}
	for rows.Next() {
		var (
			invID string
			p     PendingRefund
		)
		if err = rows.Scan(&invID, &p.RefundKey, &p.Cents, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("billing: read the unconfirmed refunds of %s: %w", orgID, err)
		}
		p.CreatedAt = p.CreatedAt.UTC()
		p.Resendable = resendable(p.CreatedAt, now)
		out[invID] = append(out[invID], p)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("billing: read the unconfirmed refunds of %s: %w", orgID, err)
	}
	return out, nil
}
