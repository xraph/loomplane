//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	stripego "github.com/stripe/stripe-go/v87"
	"github.com/xraph/ledger/invoice"
)

// ErrInvoiceNotFound is InvoiceLink's error for an invoice that isn't the
// workspace's, or that Stripe doesn't have yet.
var ErrInvoiceNotFound = errors.New("billing: there's no such invoice in this workspace")

// InvoiceSummary is one invoice as the workspace's invoice list shows it.
type InvoiceSummary struct {
	ID          string
	PeriodStart time.Time
	PeriodEnd   time.Time
	TotalCents  int64
	Status      string
	Plan        string // cloud or ai; "" for an invoice Loomplane didn't record
}

// Invoices lists the workspace's invoices in ledger, newest period first.
// Drafts are left out: an invoice is only one once it is final.
func (s *Service) Invoices(ctx context.Context, orgID string) ([]InvoiceSummary, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if orgID == "" {
		return nil, errors.New("billing: invoices need a workspace")
	}
	invs, err := s.l.Store().ListInvoices(ctx, orgID, s.appID, invoice.ListOpts{})
	if err != nil {
		return nil, fmt.Errorf("billing: list the invoices of %s: %w", orgID, err)
	}
	plans, err := s.invoicePlans(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]InvoiceSummary, 0, len(invs))
	for _, inv := range invs {
		if inv.Status == invoice.StatusDraft {
			continue
		}
		out = append(out, InvoiceSummary{
			ID: inv.ID.String(), PeriodStart: inv.PeriodStart.UTC(), PeriodEnd: inv.PeriodEnd.UTC(),
			TotalCents: inv.Total.Amount, Status: string(inv.Status), Plan: plans[inv.ID.String()],
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].PeriodStart.Equal(out[j].PeriodStart) {
			return out[i].PeriodStart.After(out[j].PeriodStart)
		}
		// In one month the base plan's invoice comes before the add-on's.
		return out[i].Plan == PlanCloud && out[j].Plan != PlanCloud
	})
	return out, nil
}

// invoicePlans maps each invoice Loomplane recorded for the workspace to
// its plan.
func (s *Service) invoicePlans(ctx context.Context, orgID string) (map[string]string, error) {
	rows, err := s.pg.Query(ctx, `SELECT invoice_id, plan FROM loomplane_billing_invoices WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, fmt.Errorf("billing: read the invoice plans of %s: %w", orgID, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var invID, plan string
		if err = rows.Scan(&invID, &plan); err != nil {
			return nil, fmt.Errorf("billing: read the invoice plans of %s: %w", orgID, err)
		}
		out[invID] = plan
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("billing: read the invoice plans of %s: %w", orgID, err)
	}
	return out, nil
}

// InvoiceLink is the URL of Stripe's hosted page for one of the
// workspace's invoices, fetched now with the secret key, which never
// leaves the server. An invoice of another workspace, or one not sent to
// Stripe yet, is ErrInvoiceNotFound.
func (s *Service) InvoiceLink(ctx context.Context, orgID, invoiceID string) (string, error) {
	if err := s.ready(); err != nil {
		return "", err
	}
	if orgID == "" || invoiceID == "" {
		return "", ErrInvoiceNotFound
	}
	var providerID string
	err := s.pg.QueryRow(ctx, `SELECT provider_id FROM loomplane_billing_invoices WHERE invoice_id = $1 AND org_id = $2`,
		invoiceID, orgID).Scan(&providerID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && providerID == "") {
		return "", ErrInvoiceNotFound
	}
	if err != nil {
		return "", fmt.Errorf("billing: read invoice %s: %w", invoiceID, err)
	}
	url, err := s.hostedInvoiceURL(ctx, providerID)
	if err != nil {
		return "", fmt.Errorf("billing: read Stripe's page for invoice %s: %w", invoiceID, err)
	}
	if url == "" {
		// Stripe has no page for a draft, and no longer for a deleted one.
		return "", ErrInvoiceNotFound
	}
	return url, nil
}

// stripeInvoiceURL reads a Stripe invoice's hosted page URL with stripe-go.
func (s *Service) stripeInvoiceURL(ctx context.Context, stripeInvoiceID string) (string, error) {
	if s.cfg.StripeSecretKey == "" {
		return "", errors.New("billing: no Stripe key to read the invoice with")
	}
	inv, err := stripego.NewClient(s.cfg.StripeSecretKey).V1Invoices.Retrieve(ctx, stripeInvoiceID, nil)
	if err != nil {
		return "", err
	}
	return inv.HostedInvoiceURL, nil
}
