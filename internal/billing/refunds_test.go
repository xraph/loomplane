//go:build cloud

package billing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	stripego "github.com/stripe/stripe-go/v87"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/ledger/id"
	"github.com/xraph/ledger/invoice"
	"github.com/xraph/ledger/provider/stripe"
	"github.com/xraph/ledger/types"
)

type refundSpy struct {
	Payments
	mu      sync.Mutex
	calls   int
	amounts []int64
	keys    []string
	fail    error
	// during runs inside the call, before it answers, with the call's ctx.
	during func(ctx context.Context)
}

func (r *refundSpy) RefundInvoice(ctx context.Context, _ string, amount *types.Money, key string) (bool, error) {
	if r.during != nil {
		r.during(ctx)
	}
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
		res, err := s.Refund(t.Context(), invID, nil, "rk_1", "rex")
		if err != nil || !res.Full {
			t.Fatalf("%+v %v", res, err)
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

func refundRows(t *testing.T, pg *pgdriver.PgDB) (all, unsent int) {
	t.Helper()
	if err := pg.QueryRow(t.Context(), `SELECT count(*), count(*) FILTER (WHERE sent_at IS NULL) FROM loomplane_billing_refunds`).
		Scan(&all, &unsent); err != nil {
		t.Fatal(err)
	}
	return all, unsent
}

// The errors ledger's Stripe provider can give, as it wraps them: the ones
// that certainly made no refund delete the record, so the same key or a
// new one can refund; the rest keep it unsent and say so, and only the
// same key sends it again.
func TestRefund_DefinitiveFailuresDeleteAmbiguousOnesStay(t *testing.T) {
	const inv = "in_x"
	apiErr := func(status int) error {
		return fmt.Errorf("stripe: refund invoice %s: %w", inv, &stripego.Error{HTTPStatusCode: status, Msg: "refused"})
	}
	for name, c := range map[string]struct {
		err      error
		refused  bool
		wantSent bool
	}{
		"no paid payment":         {stripe.ErrNotPaid, true, false},
		"listing payments failed": {fmt.Errorf("stripe: list payments for invoice %s: %w", inv, errors.New("connection reset")), true, false},
		"two payments":            {fmt.Errorf("stripe: refund invoice %s: it has more than one paid payment", inv), true, false},
		"no charge":               {fmt.Errorf("stripe: refund invoice %s: payment inpay_1 has no charge to refund", inv), true, false},
		"currency":                {fmt.Errorf("stripe: refund invoice %s: amount is in usd, payment is in eur", inv), true, false},
		"400":                     {apiErr(http.StatusBadRequest), true, false},
		"401":                     {apiErr(http.StatusUnauthorized), true, false},
		"402":                     {apiErr(http.StatusPaymentRequired), true, false},
		"403":                     {apiErr(http.StatusForbidden), true, false},
		"404":                     {apiErr(http.StatusNotFound), true, false},
		"409 key in use":          {apiErr(http.StatusConflict), false, false},
		"429":                     {apiErr(http.StatusTooManyRequests), false, false},
		"500":                     {apiErr(http.StatusInternalServerError), false, false},
		"no answer":               {fmt.Errorf("stripe: refund invoice %s: %w", inv, errors.New("read tcp: connection reset by peer")), false, false},
		"timeout":                 {fmt.Errorf("stripe: refund invoice %s: %w", inv, context.DeadlineExceeded), false, false},
		"canceled":                {fmt.Errorf("stripe: refund invoice %s: %w", inv, context.Canceled), false, false},
		"after Create":            {fmt.Errorf("stripe: refund invoice %s: refund re_1 names no charge", inv), false, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := refundRefused(c.err); got != c.refused {
				t.Fatalf("refundRefused = %v, want %v", got, c.refused)
			}
		})
	}

	// Against the database: one definitive and one ambiguous failure.
	s, pg, invID := paidInvoiceWorld(t)
	ctx := t.Context()
	spy := &refundSpy{fail: apiErr(http.StatusBadRequest)}
	s.payments = spy
	if _, err := s.Refund(ctx, invID, nil, "rk_1", "rex"); err == nil || errors.Is(err, ErrRefundUnconfirmed) {
		t.Fatalf("a 400: %v", err)
	}
	if all, _ := refundRows(t, pg); all != 0 {
		t.Fatalf("%d refunds recorded after Stripe refused", all)
	}

	spy.fail = apiErr(http.StatusInternalServerError)
	if _, err := s.Refund(ctx, invID, nil, "rk_2", "rex"); !errors.Is(err, ErrRefundUnconfirmed) {
		t.Fatalf("a 500: %v", err)
	}
	if all, unsent := refundRows(t, pg); all != 1 || unsent != 1 {
		t.Fatalf("after a 500: %d rows, %d unsent; want the refund kept, unsent", all, unsent)
	}
	// It holds its amount: a new key can't refund the same money again.
	spy.fail = nil
	if _, err := s.Refund(ctx, invID, nil, "rk_3", "rex"); !errors.Is(err, ErrNotRefundable) {
		t.Fatalf("a new key while the first is unconfirmed: %v", err)
	}
	pis, _ := s.PlatformInvoices(ctx, "org_1")
	if len(pis) != 1 || len(pis[0].PendingRefunds) != 1 || pis[0].PendingRefunds[0].RefundKey != "rk_2" ||
		!pis[0].PendingRefunds[0].Resendable || pis[0].PendingRefunds[0].Cents != 500 || pis[0].RefundedCents != 0 {
		t.Fatalf("platform invoices %+v", pis)
	}
	// The same key sends it again, under that key, and it is made.
	res, err := s.Refund(ctx, invID, nil, "rk_2", "rex")
	if err != nil || !res.Full || !res.Resent || res.Replay {
		t.Fatalf("retry: %+v %v", res, err)
	}
	if spy.keys[len(spy.keys)-1] != "rk_2" {
		t.Fatalf("sent under %v", spy.keys)
	}
	if _, unsent := refundRows(t, pg); unsent != 0 {
		t.Fatal("the confirmed refund is still unsent")
	}
	if res, err = s.Refund(ctx, invID, nil, "rk_2", "rex"); err != nil || !res.Replay {
		t.Fatalf("a replay: %+v %v", res, err)
	}
}

// An unconfirmed refund is sent again under its key while it is under 23
// hours old; from then on it is refused, and listed as needing a check in
// Stripe.
func TestRefund_UnsentIsSentAgainOnlyInsideTheWindow(t *testing.T) {
	s, pg, invID := paidInvoiceWorld(t)
	spy := &refundSpy{}
	s.payments = spy
	ctx := t.Context()
	recorded := at("2026-11-01T02:00:00Z")
	for _, key := range []string{"rk_young", "rk_old"} {
		if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_refunds (refund_key, invoice_id, org_id, cents, full_refund, refunded_by, created_at)
			VALUES ($1, $2, 'org_1', 100, false, 'rex', $3)`, key, invID, recorded); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = pg.Exec(ctx, `UPDATE loomplane_billing_refunds SET created_at = $1 WHERE refund_key = 'rk_old'`, recorded.Add(-time.Second))
	one := int64(100)

	s.now = func() time.Time { return recorded.Add(23*time.Hour - time.Second) } // rk_young just inside, rk_old exactly 23h
	pis, _ := s.PlatformInvoices(ctx, "org_1")
	resendable := map[string]bool{}
	for _, p := range pis[0].PendingRefunds {
		resendable[p.RefundKey] = p.Resendable
	}
	if !resendable["rk_young"] || resendable["rk_old"] {
		t.Fatalf("resendable %v", resendable)
	}
	if _, err := s.Refund(ctx, invID, &one, "rk_old", "rex"); !errors.Is(err, ErrRefundStale) {
		t.Fatalf("23 hours old: %v", err)
	}
	if res, err := s.Refund(ctx, invID, &one, "rk_young", "rex"); err != nil || !res.Resent {
		t.Fatalf("just under 23 hours: %+v %v", res, err)
	}
	if spy.calls != 1 || spy.keys[0] != "rk_young" {
		t.Fatalf("Stripe got %v", spy.keys)
	}
	if all, unsent := refundRows(t, pg); all != 2 || unsent != 1 {
		t.Fatalf("%d rows, %d unsent: the stale one stays for someone to settle", all, unsent)
	}
}

// A person closing the page while Stripe is asked doesn't cut the call
// off: Stripe gets a context the request's cancel doesn't reach, with a
// deadline, and the refund is recorded as made.
func TestRefund_CanceledRequestDoesntAbortTheSend(t *testing.T) {
	s, pg, invID := paidInvoiceWorld(t)
	ctx, cancel := context.WithCancel(t.Context())
	var sendErr error
	var deadline bool
	spy := &refundSpy{during: func(sendCtx context.Context) {
		cancel()
		_, deadline = sendCtx.Deadline()
		sendErr = sendCtx.Err()
	}}
	s.payments = spy
	res, err := s.Refund(ctx, invID, nil, "rk_1", "rex")
	if sendErr != nil || !deadline {
		t.Fatalf("Stripe's call saw %v, deadline %v", sendErr, deadline)
	}
	if err != nil || !res.Full {
		t.Fatalf("%+v %v", res, err)
	}
	if all, unsent := refundRows(t, pg); all != 1 || unsent != 0 {
		t.Fatalf("%d rows, %d unsent", all, unsent)
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
	if _, err := pg.Exec(ctx, `INSERT INTO loomplane_billing_refunds (refund_key, invoice_id, org_id, cents, full_refund, refunded_by, created_at)
		VALUES ('rk_1', $1, 'org_1', 500, false, 'rex', $2)`, invID, s.now()); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Refund(ctx, invID, nil, "rk_1", "rex"); err != nil || !res.Full || !res.Resent {
		t.Fatalf("%+v %v", res, err)
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
