//go:build cloud

package billing

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/ledger/id"
	"github.com/xraph/ledger/invoice"
	"github.com/xraph/ledger/types"
)

type refundSpy struct {
	Payments
	mu      sync.Mutex
	calls   int
	amounts []int64
	keys    []string
	fail    error
}

func (r *refundSpy) RefundInvoice(_ context.Context, _ string, amount *types.Money, key string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.fail != nil {
		return false, r.fail
	}
	var cents int64
	if amount != nil {
		cents = amount.Amount
	}
	r.amounts = append(r.amounts, cents)
	r.keys = append(r.keys, key)
	return true, nil
}

// paidInvoiceWorld is an October invoice of $5 for org_1, synced to Stripe
// and marked paid.
func paidInvoiceWorld(t *testing.T) (*Service, *pgdriver.PgDB, string) {
	t.Helper()
	clock := at("2026-10-05T00:00:00Z")
	s, pg, _ := billingWorld(t, &clock)
	ctx := t.Context()
	if err := s.MarkCardReady(ctx, "org_1"); err != nil {
		t.Fatal(err)
	}
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	invs := invoicesFor(t, s, "org_1")
	if len(invs) != 1 || invs[0].Total != types.USD(500) {
		t.Fatalf("invoices %+v", invs)
	}
	invID := invs[0].ID.String()
	if err := s.invoicePaid(ctx, invID, "in_"+invID, clock); err != nil {
		t.Fatal(err)
	}
	return s, pg, invID
}

func mustInvoiceID(t *testing.T, s string) id.InvoiceID {
	t.Helper()
	iid, err := id.ParseInvoiceID(s)
	if err != nil {
		t.Fatal(err)
	}
	return iid
}

func TestRefund_RecordedOncePerKey(t *testing.T) {
	s, _, invID := paidInvoiceWorld(t) // an October invoice for org_1, synced and marked paid
	spy := &refundSpy{}
	s.payments = spy
	for range 2 {
		full, err := s.Refund(t.Context(), invID, nil, "rk_1", "rex")
		if err != nil || !full {
			t.Fatalf("%v %v", full, err)
		}
	}
	if spy.calls != 1 {
		t.Fatalf("Stripe refunded %d times for one key", spy.calls)
	}
	if spy.amounts[0] != 500 || spy.keys[0] != "rk_1" {
		t.Fatalf("sent %v with keys %v, want the rest ($5) under rk_1", spy.amounts, spy.keys)
	}
	inv, _ := s.l.Store().GetInvoice(t.Context(), mustInvoiceID(t, invID))
	if inv.Status != invoice.StatusPaid {
		t.Fatalf("a refunded invoice became %s; it must stay paid", inv.Status)
	}
}

func TestRefund_RefusesAnUnpaidInvoice(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	clock = at("2026-11-01T01:15:00Z")
	if err := s.CloseMonth(ctx); err != nil {
		t.Fatal(err)
	}
	invID := invoicesFor(t, s, "org_1")[0].ID.String() // sent to Stripe, not paid
	spy := &refundSpy{}
	s.payments = spy
	if _, err := s.Refund(ctx, invID, nil, "rk_1", "rex"); !errors.Is(err, ErrNotRefundable) {
		t.Fatalf("unpaid: %v", err)
	}
	if _, err := s.Refund(ctx, "inv_nope", nil, "rk_2", "rex"); !errors.Is(err, ErrInvoiceNotFound) {
		t.Fatalf("unknown invoice: %v", err)
	}
	if spy.calls != 0 {
		t.Fatalf("Stripe was called %d times", spy.calls)
	}
}

// A free invoice is paid but was never sent to Stripe: nothing to refund.
func TestRefund_RefusesAFreeInvoice(t *testing.T) {
	clock := at("2026-10-05T00:00:00Z")
	s, _, _ := billingWorld(t, &clock)
	ctx := t.Context()
	_ = s.MarkCardReady(ctx, "org_1")
	if err := s.GrantCredit(ctx, "org_1", 500, "", "rex"); err != nil {
		t.Fatal(err)
	}
	clock = at("2026-11-01T01:15:00Z")
	_ = s.CloseMonth(ctx)
	inv := invoicesFor(t, s, "org_1")[0]
	if inv.Status != invoice.StatusPaid || !inv.Total.IsZero() {
		t.Fatalf("invoice %s %v", inv.Status, inv.Total)
	}
	spy := &refundSpy{}
	s.payments = spy
	if _, err := s.Refund(ctx, inv.ID.String(), nil, "rk_1", "rex"); !errors.Is(err, ErrNotRefundable) || spy.calls != 0 {
		t.Fatalf("free invoice: %v, %d calls", err, spy.calls)
	}
}

// Partial refunds add up to the invoice and no further: one above what is
// left is refused before Stripe is asked, and so is any once it is all
// refunded.
func TestRefund_RefusesMoreThanIsLeft(t *testing.T) {
	s, _, invID := paidInvoiceWorld(t)
	spy := &refundSpy{}
	s.payments = spy
	ctx := t.Context()
	three, four, two := int64(300), int64(400), int64(200)
	if _, err := s.Refund(ctx, invID, &three, "rk_1", "rex"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refund(ctx, invID, &four, "rk_2", "rex"); !errors.Is(err, ErrRefundTooLarge) {
		t.Fatalf("$4 of the $2 left: %v", err)
	}
	if _, err := s.Refund(ctx, invID, &two, "rk_3", "rex"); err != nil {
		t.Fatalf("the $2 left: %v", err)
	}
	if _, err := s.Refund(ctx, invID, nil, "rk_4", "rex"); !errors.Is(err, ErrNotRefundable) {
		t.Fatalf("after all of it: %v", err)
	}
	if spy.calls != 2 || spy.amounts[0] != 300 || spy.amounts[1] != 200 {
		t.Fatalf("Stripe got %v", spy.amounts)
	}
	pis, err := s.PlatformInvoices(ctx, "org_1")
	if err != nil || len(pis) != 1 || pis[0].RefundedCents != 500 || pis[0].RefundableCents != 0 {
		t.Fatalf("platform invoices %+v %v", pis, err)
	}
}

// A key used once means that one refund: replayed for another invoice or
// another amount it is refused, and Stripe isn't asked.
func TestRefund_KeyIsOneRefund(t *testing.T) {
	s, _, invID := paidInvoiceWorld(t)
	spy := &refundSpy{}
	s.payments = spy
	ctx := t.Context()
	one, two := int64(100), int64(200)
	if _, err := s.Refund(ctx, invID, &one, "rk_1", "rex"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refund(ctx, invID, &one, "rk_1", "rex"); err != nil {
		t.Fatalf("the same refund again: %v", err)
	}
	if _, err := s.Refund(ctx, invID, &two, "rk_1", "rex"); !errors.Is(err, ErrRefundKeyReused) {
		t.Fatalf("another amount: %v", err)
	}
	if spy.calls != 1 {
		t.Fatalf("Stripe was called %d times", spy.calls)
	}
}

// Concurrent replays of one key refund once.
func TestRefund_ConcurrentReplaysRefundOnce(t *testing.T) {
	s, _, invID := paidInvoiceWorld(t)
	spy := &refundSpy{}
	s.payments = spy
	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.Refund(t.Context(), invID, nil, "rk_1", "rex")
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if spy.calls != 1 {
		t.Fatalf("Stripe was called %d times", spy.calls)
	}
}

// Stripe refusing deletes the record: nothing counts as refunded, and the
// same key works once Stripe does.
func TestRefund_StripeErrorCanBeRetriedWithTheSameKey(t *testing.T) {
	s, pg, invID := paidInvoiceWorld(t)
	spy := &refundSpy{fail: errors.New("stripe: refund invoice: connection reset")}
	s.payments = spy
	ctx := t.Context()
	if _, err := s.Refund(ctx, invID, nil, "rk_1", "rex"); err == nil {
		t.Fatal("a Stripe error was success")
	}
	var n int
	_ = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_billing_refunds`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d refunds recorded after Stripe refused", n)
	}
	spy.fail = nil
	if full, err := s.Refund(ctx, invID, nil, "rk_1", "rex"); err != nil || !full {
		t.Fatalf("retry: %v %v", full, err)
	}
	if spy.calls != 2 || spy.keys[0] != "rk_1" {
		t.Fatalf("calls %d keys %v", spy.calls, spy.keys)
	}
}

// A refund recorded and never answered (the process stopped while Stripe
// was asked) is sent again under the same key when that key comes back,
// not taken as done.
func TestRefund_UnsentIsSentAgain(t *testing.T) {
	s, pg, invID := paidInvoiceWorld(t)
	spy := &refundSpy{}
	s.payments = spy
	ctx := t.Context()
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_refunds (refund_key, invoice_id, org_id, cents, full_refund, refunded_by)
		VALUES ('rk_1', $1, 'org_1', 500, false, 'rex')`, invID); err != nil {
		t.Fatal(err)
	}
	if full, err := s.Refund(ctx, invID, nil, "rk_1", "rex"); err != nil || !full {
		t.Fatalf("%v %v", full, err)
	}
	if spy.calls != 1 || spy.amounts[0] != 500 || spy.keys[0] != "rk_1" {
		t.Fatalf("calls %d %v %v", spy.calls, spy.amounts, spy.keys)
	}
	var sent bool
	_ = pg.QueryRow(ctx, `SELECT sent_at IS NOT NULL AND full_refund FROM loomplane_billing_refunds WHERE refund_key = 'rk_1'`).Scan(&sent)
	if !sent {
		t.Fatal("the refund isn't recorded as made")
	}
}
