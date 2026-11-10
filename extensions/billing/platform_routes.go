//go:build cloud

package billing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/billing"
	"github.com/xraph/loomplane/internal/platformadmin"
	"github.com/xraph/loomplane/internal/security"
)

// platformBase is where the platform owner's billing admin lives.
const platformBase = "/api/v1/platform/billing"

// platformService is what the platform owner's billing routes call.
// *billing.Service is one; the tests use a fake.
type platformService interface {
	Overview(ctx context.Context) (billing.Overview, error)
	Rates(ctx context.Context) (map[string]int64, error)
	SetRate(ctx context.Context, feature string, microUSD int64, by string) (previous int64, err error)
	Workspaces(ctx context.Context) ([]billing.WorkspaceBilling, error)
	PlatformInvoices(ctx context.Context, orgID string) ([]billing.PlatformInvoice, error)
	GrantCredit(ctx context.Context, orgID string, cents int64, note, by string) error
	Refund(ctx context.Context, invoiceID string, cents *int64, refundKey, by string) (billing.RefundResult, error)
	SettleRefund(ctx context.Context, invoiceID, refundKey string, made bool, stripeRefundID, by string) error
}

var _ platformService = (*billing.Service)(nil)

// auditor records each change the platform owner makes and how it went.
type auditor interface {
	Record(ctx context.Context, action, userID, resource, resourceID, outcome string, meta map[string]string)
}

const (
	outcomeSuccess  = "success"
	outcomeFailure  = "failure"
	outcomePending  = "pending"   // recorded, Stripe's answer unknown
	outcomeNoChange = "no_change" // a replay: nothing was sent
)

// chronicleAuditor records the platform owner's billing changes in
// Chronicle, in the staff stream like platform Cloud's: the actor, what
// changed and how it went, never a key.
type chronicleAuditor struct {
	chronicle *chronicle.Chronicle
	appID     string
	log       forge.Logger
}

func (a chronicleAuditor) Record(ctx context.Context, action, userID, resource, resourceID, outcome string, meta map[string]string) {
	b := a.chronicle.Warning(ctx, action, resource, resourceID).
		Category("billing").AppID(a.appID).TenantID(platformadmin.AuditTenant).UserID(userID).Outcome(outcome)
	for k, v := range meta {
		b = b.Meta(k, v)
	}
	if err := b.Record(); err != nil {
		a.log.Error("record audit event", forge.F("action", action), forge.F("error", err.Error()))
	}
}

type platformRoutes struct {
	svc     platformService
	isOwner identity.PlatformOwner
	audit   auditor
}

// mountPlatformRoutes serves the platform owner's billing admin. Every
// route checks, on the server, that the caller is the platform owner
// signed in themselves; anyone else, a platform admin included, gets the
// 404 an unknown route would. None of them is in the OpenAPI spec.
func mountPlatformRoutes(router forge.Router, guard identity.Guard, isOwner identity.PlatformOwner, svc platformService, audit auditor) error {
	r := &platformRoutes{svc: svc, isOwner: isOwner, audit: audit}
	g := router.Group(platformBase, forge.WithGroupMiddleware(r.ownerOnly(guard)...), forge.WithGroupSchemaExclude())
	for _, rt := range []struct {
		method, path, name string
		h                  forge.Handler
	}{
		{http.MethodGet, "/access", "platformBilling.access", r.access},
		{http.MethodGet, "/overview", "platformBilling.overview", r.overview},
		{http.MethodGet, "/rates", "platformBilling.rates", r.rates},
		{http.MethodPut, "/rates/:feature", "platformBilling.setRate", r.setRate},
		{http.MethodGet, "/workspaces", "platformBilling.workspaces", r.workspaces},
		{http.MethodGet, "/workspaces/:org/invoices", "platformBilling.invoices", r.invoices},
		{http.MethodPost, "/workspaces/:org/credit", "platformBilling.credit", r.credit},
		{http.MethodPost, "/invoices/:id/refund", "platformBilling.refund", r.refund},
		{http.MethodPost, "/invoices/:id/refunds/:key/settle", "platformBilling.settleRefund", r.settleRefund},
	} {
		var err error
		switch rt.method {
		case http.MethodGet:
			err = g.GET(rt.path, rt.h, forge.WithName(rt.name))
		case http.MethodPost:
			err = g.POST(rt.path, rt.h, forge.WithName(rt.name))
		case http.MethodPut:
			err = g.PUT(rt.path, rt.h, forge.WithName(rt.name))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ownerKey carries the platform owner the gate found to the handlers.
type ownerKey struct{}

func ownerID(c forge.Context) string {
	id, ok := c.Request().Context().Value(ownerKey{}).(string)
	if !ok {
		return ""
	}
	return id
}

var errHidden = forge.NewHTTPError(http.StatusNotFound, "not found")

// ownerOnly is the routes' middleware: the guard, then the gate that lets
// only the platform owner through. Anyone else gets the 404 an unknown
// route would, an anonymous caller included, so the routes' existence
// isn't given away. The owner comes from the session alone: an API key,
// the AI agent or anything acting for the owner never passes, nor does a
// platform admin who isn't the owner.
func (r *platformRoutes) ownerOnly(guard identity.Guard) []forge.Middleware {
	hide := func(next forge.Handler) forge.Handler {
		return func(c forge.Context) error {
			err := next(c)
			var status interface{ StatusCode() int }
			if errors.As(err, &status) && (status.StatusCode() == http.StatusUnauthorized || status.StatusCode() == http.StatusForbidden) {
				return errHidden
			}
			return err
		}
	}
	gate := func(next forge.Handler) forge.Handler {
		return func(c forge.Context) error {
			ctx := c.Request().Context()
			p, ok := security.PrincipalFrom(ctx)
			if !ok || p.KeyID != "" || p.Kind != "" || p.ReadOnly || p.OnBehalfOf != nil {
				return errHidden
			}
			userID, ok := r.isOwner(ctx)
			if !ok || userID == "" {
				return errHidden
			}
			c.WithContext(context.WithValue(ctx, ownerKey{}, userID))
			return next(c)
		}
	}
	return slices.Concat([]forge.Middleware{hide}, guard, []forge.Middleware{gate})
}

// What the platform owner reads when a change is refused.
const (
	msgNoFeature     = "There's no such feature to price."
	msgNoCreditable  = "This workspace has no running subscription to credit: it has no card on file, or its subscription ended."
	msgNotRefundable = "Only a paid invoice that went to Stripe can be refunded, and only what's left of it."
	msgRefundTooBig  = "That's more than is left to refund on this invoice."
	msgKeyReused     = "That refund key was used for a different refund. Reload the page and try again."
	msgNoInvoice     = "There's no such invoice."
	msgUnconfirmed   = "Stripe didn't confirm this refund. Retry this same refund from the invoice: it goes to Stripe under the same key, so it can't be paid twice."
	msgStale         = "This refund was recorded more than 23 hours ago and Stripe never confirmed it. Check the invoice in Stripe, then settle it as made or dropped."
	msgNoRefund      = "There's no such refund on this invoice."
	msgSettled       = "Stripe already confirmed this refund, so there's nothing to settle."
)

// platformFail maps a billing error to its response. Billing's, Stripe's
// and the database's own errors are logged and never sent.
func platformFail(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, billing.ErrUnknownFeature):
		return forge.NewHTTPError(http.StatusNotFound, msgNoFeature)
	case errors.Is(err, billing.ErrNeedsPayment):
		return forge.NewHTTPError(http.StatusConflict, msgNoCreditable)
	case errors.Is(err, billing.ErrNotRefundable):
		return forge.NewHTTPError(http.StatusConflict, msgNotRefundable)
	case errors.Is(err, billing.ErrRefundTooLarge):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, msgRefundTooBig)
	case errors.Is(err, billing.ErrRefundKeyReused):
		return forge.NewHTTPError(http.StatusConflict, msgKeyReused)
	case errors.Is(err, billing.ErrInvoiceNotFound):
		return forge.NewHTTPError(http.StatusNotFound, msgNoInvoice)
	case errors.Is(err, billing.ErrRefundUnconfirmed):
		slog.ErrorContext(ctx, "platform billing: a refund waits for Stripe's answer", "error", err)
		return forge.NewHTTPError(http.StatusServiceUnavailable, msgUnconfirmed)
	case errors.Is(err, billing.ErrRefundNotFound):
		return forge.NewHTTPError(http.StatusNotFound, msgNoRefund)
	case errors.Is(err, billing.ErrRefundSettled):
		return forge.NewHTTPError(http.StatusConflict, msgSettled)
	case errors.Is(err, billing.ErrRefundStale):
		slog.ErrorContext(ctx, "platform billing: an unconfirmed refund needs checking in Stripe", "error", err)
		return forge.NewHTTPError(http.StatusConflict, msgStale)
	case errors.Is(err, context.DeadlineExceeded):
		slog.WarnContext(ctx, "platform billing: a request ran out of time", "error", err)
		return forge.NewHTTPError(http.StatusServiceUnavailable, msgBusy)
	}
	slog.ErrorContext(ctx, "platform billing: a request failed", "error", err)
	return forge.NewHTTPError(http.StatusInternalServerError, msgFailed)
}

// outcome is how an audited change went.
func outcome(err error) string {
	if err != nil {
		return outcomeFailure
	}
	return outcomeSuccess
}

// access answers 204 to the platform owner, and the gate 404 to everyone
// else: the dashboard asks it to know whether to show the billing admin.
func (r *platformRoutes) access(c forge.Context) error {
	return c.NoContent(http.StatusNoContent)
}

type planView struct {
	Slug        string `json:"slug"`
	BaseCents   int64  `json:"base_cents"`
	CreditCents int64  `json:"credit_cents"`
}

type overviewView struct {
	StripeMode billing.StripeMode `json:"stripe_mode"`
	Plans      []planView         `json:"plans"`
}

func (r *platformRoutes) overview(c forge.Context) error {
	ctx := c.Request().Context()
	o, err := r.svc.Overview(ctx)
	if err != nil {
		return platformFail(ctx, err)
	}
	v := overviewView{StripeMode: o.StripeMode, Plans: make([]planView, 0, len(o.Plans))}
	for _, p := range o.Plans {
		v.Plans = append(v.Plans, planView{Slug: p.Slug, BaseCents: p.BaseCents, CreditCents: p.CreditCents})
	}
	return c.JSON(http.StatusOK, v)
}

type rateView struct {
	Feature    string `json:"feature"`
	Unit       string `json:"unit"`
	USDPerUnit string `json:"usd_per_unit"`
}

// rateOf is a feature's rate as the routes send it.
func rateOf(feature string, microUSD int64) rateView {
	return rateView{Feature: feature, Unit: billing.Unit(feature), USDPerUnit: microDollars(microUSD)}
}

func (r *platformRoutes) rates(c forge.Context) error {
	ctx := c.Request().Context()
	rates, err := r.svc.Rates(ctx)
	if err != nil {
		return platformFail(ctx, err)
	}
	out := make([]rateView, 0, len(billing.Features))
	for _, f := range billing.Features {
		out = append(out, rateOf(f, rates[f]))
	}
	return c.JSON(http.StatusOK, out)
}

// ratePattern is US dollars with at most six decimals (a micro-USD) and no
// sign, up to 9999.999999; maxRate narrows it to 1000.
var ratePattern = regexp.MustCompile(`^(0|[1-9]\d{0,3})(\.\d{1,6})?$`)

const msgRate = `Send usd_per_unit as US dollars from "0" to "1000" with at most six decimals, like "0.012345".`

type rateBody struct {
	USDPerUnit json.RawMessage `json:"usd_per_unit"`
}

// setRate sets one feature's rate from now on. Usage already recorded
// keeps the rate it was recorded at.
func (r *platformRoutes) setRate(c forge.Context) error {
	feature := c.Param("feature")
	if !slices.Contains(billing.Features, feature) {
		return forge.NewHTTPError(http.StatusNotFound, msgNoFeature)
	}
	var body rateBody
	if err := json.NewDecoder(io.LimitReader(c.Request().Body, 4<<10)).Decode(&body); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `The body must be JSON: {"usd_per_unit": "0.012345"}`)
	}
	usd, ok := jsonString(body.USDPerUnit)
	micro, valid := parseFixed(usd, ratePattern, 6)
	if !ok || !valid || micro > billing.MaxRateMicroUSD {
		return forge.NewHTTPError(http.StatusUnprocessableEntity, msgRate)
	}
	ctx := c.Request().Context()
	previous, err := r.svc.SetRate(ctx, feature, micro, ownerID(c))
	meta := map[string]string{"feature": feature, "new_micro_usd": strconv.FormatInt(micro, 10)}
	if err == nil {
		meta["old_micro_usd"] = strconv.FormatInt(previous, 10)
	}
	r.audit.Record(ctx, "billing.rate.set", ownerID(c), "billing_rate", feature, outcome(err), meta)
	if err != nil {
		return platformFail(ctx, err)
	}
	return c.JSON(http.StatusOK, rateOf(feature, micro))
}

type workspaceView struct {
	OrgID            string         `json:"org_id"`
	Name             string         `json:"name"`
	Status           billing.Status `json:"status"`
	PeriodTotalCents int64          `json:"period_total_cents"`
	OverageCents     int64          `json:"overage_cents"`
	AIEnabled        bool           `json:"ai_enabled"`
}

func (r *platformRoutes) workspaces(c forge.Context) error {
	ctx := c.Request().Context()
	ws, err := r.svc.Workspaces(ctx)
	if err != nil {
		return platformFail(ctx, err)
	}
	out := make([]workspaceView, 0, len(ws))
	for _, w := range ws {
		out = append(out, workspaceView{OrgID: w.OrgID, Name: w.Name, Status: w.Status,
			PeriodTotalCents: w.PeriodTotalCents, OverageCents: w.OverageCents, AIEnabled: w.AIEnabled})
	}
	return c.JSON(http.StatusOK, out)
}

type platformInvoiceView struct {
	ID              string    `json:"id"`
	PeriodStart     time.Time `json:"period_start"`
	PeriodEnd       time.Time `json:"period_end"`
	TotalCents      int64     `json:"total_cents"`
	Status          string    `json:"status"`
	Plan            string    `json:"plan"`
	RefundedCents   int64     `json:"refunded_cents"`
	RefundableCents int64     `json:"refundable_cents"`
	// PendingRefunds are refunds Stripe never confirmed. The dashboard
	// retries a resendable one under its refund_key; one that isn't is
	// settled in Stripe by hand.
	PendingRefunds []pendingRefundView `json:"pending_refunds"`
}

type pendingRefundView struct {
	RefundKey  string    `json:"refund_key"`
	Cents      int64     `json:"cents"`
	CreatedAt  time.Time `json:"created_at"`
	Resendable bool      `json:"resendable"`
	// NeedsSettlement says it is past Stripe's idempotency window: someone
	// checks Stripe and settles it as made or dropped.
	NeedsSettlement bool `json:"needs_settlement"`
}

func (r *platformRoutes) invoices(c forge.Context) error {
	ctx := c.Request().Context()
	invs, err := r.svc.PlatformInvoices(ctx, c.Param("org"))
	if err != nil {
		return platformFail(ctx, err)
	}
	out := make([]platformInvoiceView, 0, len(invs))
	for _, inv := range invs {
		v := platformInvoiceView{ID: inv.ID, PeriodStart: inv.PeriodStart, PeriodEnd: inv.PeriodEnd,
			TotalCents: inv.TotalCents, Status: inv.Status, Plan: inv.Plan,
			RefundedCents: inv.RefundedCents, RefundableCents: inv.RefundableCents,
			PendingRefunds: make([]pendingRefundView, 0, len(inv.PendingRefunds))}
		for _, p := range inv.PendingRefunds {
			v.PendingRefunds = append(v.PendingRefunds, pendingRefundView{RefundKey: p.RefundKey, Cents: p.Cents,
				CreatedAt: p.CreatedAt, Resendable: p.Resendable, NeedsSettlement: !p.Resendable})
		}
		out = append(out, v)
	}
	return c.JSON(http.StatusOK, out)
}

// usdPattern is US dollars with at most two decimals and no sign, up to
// 999999.99; each route narrows the range.
var usdPattern = regexp.MustCompile(`^(0|[1-9]\d{0,5})(\.\d{1,2})?$`)

//nolint:gosec // G101: what a person reads, not a credential
const msgCredit = `Send usd as US dollars from "0.01" to "1000.00" with at most two decimals, and a note of at most 500 characters.`

type creditBody struct {
	USD  json.RawMessage `json:"usd"`
	Note string          `json:"note"`
}

// credit grants the workspace a one-use credit on its next invoice.
func (r *platformRoutes) credit(c forge.Context) error {
	org := c.Param("org")
	var body creditBody
	if err := json.NewDecoder(io.LimitReader(c.Request().Body, 8<<10)).Decode(&body); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `The body must be JSON: {"usd": "5.00", "note": "why"}`)
	}
	usd, ok := jsonString(body.USD)
	cents, valid := parseFixed(usd, usdPattern, 2)
	note := strings.TrimSpace(body.Note)
	if !ok || !valid || cents <= 0 || cents > billing.MaxCreditCents || utf8.RuneCountInString(note) > billing.MaxCreditNote {
		return forge.NewHTTPError(http.StatusUnprocessableEntity, msgCredit)
	}
	ctx := c.Request().Context()
	err := r.svc.GrantCredit(ctx, org, cents, note, ownerID(c))
	r.audit.Record(ctx, "billing.credit.grant", ownerID(c), "workspace", org, outcome(err), map[string]string{
		"cents": strconv.FormatInt(cents, 10),
		"note":  note,
	})
	if err != nil {
		return platformFail(ctx, err)
	}
	return c.NoContent(http.StatusNoContent)
}

const msgRefund = `Send usd as US dollars with at most two decimals, or null for what is left of the invoice, and refund_key: a new UUID for each refund, the same one when you retry it.`

type refundBody struct {
	USD       json.RawMessage `json:"usd"`
	RefundKey string          `json:"refund_key"`
}

type refundView struct {
	Full bool `json:"full"`
	// Replay says the refund was made by an earlier call with the key.
	Replay bool `json:"replay"`
}

// refund refunds a paid invoice through Stripe, once per refund key. usd
// must be sent: null is what is left of the invoice, so a client that
// leaves it out never refunds everything by mistake.
func (r *platformRoutes) refund(c forge.Context) error {
	invoiceID := c.Param("id")
	var body refundBody
	if err := json.NewDecoder(io.LimitReader(c.Request().Body, 4<<10)).Decode(&body); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `The body must be JSON: {"usd": "3.00", "refund_key": "<uuid>"}`)
	}
	cents, ok := parseRefund(body.USD)
	if !ok || !billing.RefundKeyPattern.MatchString(body.RefundKey) {
		return forge.NewHTTPError(http.StatusUnprocessableEntity, msgRefund)
	}
	ctx := c.Request().Context()
	res, err := r.svc.Refund(ctx, invoiceID, cents, body.RefundKey, ownerID(c))
	amount := "rest"
	if cents != nil {
		amount = strconv.FormatInt(*cents, 10)
	}
	result := outcome(err)
	switch {
	case errors.Is(err, billing.ErrRefundUnconfirmed):
		result = outcomePending
	case err == nil && res.Replay:
		result = outcomeNoChange
	}
	r.audit.Record(ctx, "billing.invoice.refund", ownerID(c), "invoice", invoiceID, result, map[string]string{
		"cents":  amount,
		"full":   strconv.FormatBool(res.Full),
		"replay": strconv.FormatBool(res.Replay),
		"resent": strconv.FormatBool(res.Resent),
	})
	if err != nil {
		return platformFail(ctx, err)
	}
	return c.JSON(http.StatusOK, refundView{Full: res.Full, Replay: res.Replay})
}

const msgSettle = `Send outcome "made" with stripe_refund_id (the re_... id from Stripe), or outcome "dropped" when Stripe has no such refund.`

type settleBody struct {
	Outcome        string `json:"outcome"`
	StripeRefundID string `json:"stripe_refund_id"`
}

// settleRefund settles a refund Stripe never confirmed, once the owner
// checked Stripe: made counts it as refunded, dropped deletes it and frees
// its amount.
func (r *platformRoutes) settleRefund(c forge.Context) error {
	invoiceID, key := c.Param("id"), c.Param("key")
	var body settleBody
	if err := json.NewDecoder(io.LimitReader(c.Request().Body, 4<<10)).Decode(&body); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `The body must be JSON: {"outcome": "made", "stripe_refund_id": "re_..."}`)
	}
	var made bool
	switch {
	case body.Outcome == "made" && billing.StripeRefundIDPattern.MatchString(body.StripeRefundID):
		made = true
	case body.Outcome == "dropped" && body.StripeRefundID == "":
	default:
		return forge.NewHTTPError(http.StatusUnprocessableEntity, msgSettle)
	}
	if !billing.RefundKeyPattern.MatchString(key) {
		return forge.NewHTTPError(http.StatusNotFound, msgNoRefund)
	}
	ctx := c.Request().Context()
	err := r.svc.SettleRefund(ctx, invoiceID, key, made, body.StripeRefundID, ownerID(c))
	r.audit.Record(ctx, "billing.refund.settle", ownerID(c), "invoice", invoiceID, outcome(err), map[string]string{
		"refund_key":       key,
		"settled_as":       body.Outcome,
		"stripe_refund_id": body.StripeRefundID,
	})
	if err != nil {
		return platformFail(ctx, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// parseRefund reads a refund's usd: JSON null is the rest of the invoice;
// anything else is a string of dollars, at least a cent, at most two
// decimals. A missing field or a JSON number isn't an amount.
func parseRefund(raw json.RawMessage) (*int64, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	if string(raw) == "null" {
		return nil, true
	}
	s, ok := jsonString(raw)
	if !ok {
		return nil, false
	}
	cents, ok := parseFixed(s, usdPattern, 2)
	if !ok || cents <= 0 {
		return nil, false
	}
	return &cents, true
}

// jsonString reads a JSON string. A missing field, null or any other JSON
// value isn't one.
func jsonString(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// parseFixed reads a decimal string matching pattern, with at most places
// decimals, as a whole number of its smallest unit: "0.012345" with six
// places is 12345.
func parseFixed(s string, pattern *regexp.Regexp, places int) (int64, bool) {
	if !pattern.MatchString(s) {
		return 0, false
	}
	whole, frac, _ := strings.Cut(s, ".")
	if len(frac) > places {
		return 0, false
	}
	frac += strings.Repeat("0", places-len(frac))
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, false
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, false
	}
	scale := int64(1)
	for range places {
		scale *= 10
	}
	return w*scale + f, true
}

// microDollars writes micro-USD as dollars with six decimals: 12345 is
// "0.012345".
func microDollars(micro int64) string {
	frac := strconv.FormatInt(micro%1_000_000, 10)
	return strconv.FormatInt(micro/1_000_000, 10) + "." + strings.Repeat("0", 6-len(frac)) + frac
}
