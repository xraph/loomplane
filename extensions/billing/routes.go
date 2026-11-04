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
	"strconv"
	"strings"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/billing"
	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/security"
)

// base is where every workspace billing route lives.
const base = "/api/v1/billing"

// limitTimeout is how long a spending limit change waits, behind an
// evaluation of the same workspace that holds its lock, before it gives up.
const limitTimeout = 10 * time.Second

// billingService is what the routes call. *billing.Service is one; the
// tests use a fake, so the routes test without Postgres or Stripe.
type billingService interface {
	Account(ctx context.Context, orgID string) (billing.Account, error)
	Summary(ctx context.Context, orgID string) (billing.Summary, error)
	SetupSession(ctx context.Context, orgID, userID string) (string, error)
	SetSpendingLimit(ctx context.Context, orgID string, cents *int64) (billing.Account, error)
	Invoices(ctx context.Context, orgID string) ([]billing.InvoiceSummary, error)
	InvoiceLink(ctx context.Context, orgID, invoiceID string) (string, error)
	HandleStripeWebhook(ctx context.Context, payload []byte, signature string) error
}

var _ billingService = (*billing.Service)(nil)

// authorizer checks a workspace member's permission.
type authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

type routes struct {
	svc   billingService
	authz authorizer
}

// mountRoutes serves the workspace billing routes behind the guard and
// Stripe's webhook outside it. None of them is in the OpenAPI spec: the
// dashboard's cloud client is written by hand.
func mountRoutes(router forge.Router, guard identity.Guard, svc billingService, authz authorizer) error {
	r := &routes{svc: svc, authz: authz}
	g := router.Group(base, forge.WithGroupMiddleware(guard...), forge.WithGroupSchemaExclude())
	for _, rt := range []struct {
		method, path, name string
		h                  forge.Handler
	}{
		{http.MethodGet, "/status", "billing.status", r.status},
		{http.MethodGet, "/account", "billing.account", r.account},
		{http.MethodPost, "/setup-session", "billing.setupSession", r.setupSession},
		{http.MethodPut, "/spending-limit", "billing.spendingLimit", r.setSpendingLimit},
		{http.MethodPut, "/ai", "billing.ai", r.setAI},
		{http.MethodGet, "/invoices", "billing.invoices", r.invoices},
		{http.MethodGet, "/invoices/:id/link", "billing.invoiceLink", r.invoiceLink},
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
	return mountWebhook(router, svc)
}

// What a refused caller reads.
const (
	msgOwnerSession = "Billing changes need the owner signed in."
	msgFailed       = "Billing couldn't do that right now. Try again in a minute."
	msgBusy         = "Billing is busy right now. Try again in a minute."
)

// allow returns the principal when it holds perm in its own workspace.
// The workspace is always the principal's: never one from the query or
// the body.
func (r *routes) allow(c forge.Context, perm security.Permission) (security.Principal, error) {
	ctx := c.Request().Context()
	p, ok := security.PrincipalFrom(ctx)
	if !ok || p.OrgID == "" {
		return security.Principal{}, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	if err := r.authz.Require(ctx, p, perm, ""); err != nil {
		status := security.StatusFor(err)
		if status == http.StatusInternalServerError {
			slog.ErrorContext(ctx, "billing: checking a permission failed", "org", p.OrgID, "error", err)
			return security.Principal{}, forge.NewHTTPError(status, msgFailed)
		}
		return security.Principal{}, forge.NewHTTPError(status, security.ErrForbidden.Error())
	}
	return p, nil
}

// allowOwner is allow for a billing change: the workspace owner, signed in
// themselves. An API key, the AI agent and Loomplane's automation never
// pass, whoever made them.
func (r *routes) allowOwner(c forge.Context) (security.Principal, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok || p.OrgID == "" {
		return security.Principal{}, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	if p.KeyID != "" || p.Kind != "" || p.ReadOnly || p.OnBehalfOf != nil {
		return security.Principal{}, forge.NewHTTPError(http.StatusForbidden, msgOwnerSession)
	}
	return r.allow(c, security.OrganizationAdminister)
}

// fail maps a billing error to its response. Only a gate refusal's own
// sentence reaches the caller; billing's, Stripe's and the database's
// errors are logged and never sent.
func fail(ctx context.Context, org string, err error) error {
	var blocked *metering.Blocked
	switch {
	case errors.As(err, &blocked):
		return forge.NewHTTPError(http.StatusPaymentRequired, blocked.Reason)
	case errors.Is(err, metering.ErrBlocked):
		return forge.NewHTTPError(http.StatusPaymentRequired, "Billing doesn't allow this right now. See Settings > Billing.")
	case errors.Is(err, billing.ErrNoSuchMember):
		return forge.NewHTTPError(http.StatusNotFound, "This workspace or your account can't be found. Reload and try again.")
	case errors.Is(err, billing.ErrInvoiceNotFound):
		return forge.NewHTTPError(http.StatusNotFound, "There's no such invoice in this workspace.")
	case errors.Is(err, context.DeadlineExceeded):
		slog.WarnContext(ctx, "billing: a request ran out of time", "org", org, "error", err)
		return forge.NewHTTPError(http.StatusServiceUnavailable, msgBusy)
	}
	slog.ErrorContext(ctx, "billing: a request failed", "org", org, "error", err)
	return forge.NewHTTPError(http.StatusInternalServerError, msgFailed)
}

type statusView struct {
	Status billing.Status `json:"status"`
}

// status is the one route any member reads, so the card gate shows.
func (r *routes) status(c forge.Context) error {
	p, err := r.allow(c, security.MemberRead)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	a, err := r.svc.Account(ctx, p.OrgID)
	if err != nil {
		return fail(ctx, p.OrgID, err)
	}
	return c.JSON(http.StatusOK, statusView{Status: a.Status})
}

type periodView struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type usageView struct {
	Feature string `json:"feature"`
	Cents   int64  `json:"cents"`
}

type paymentMethodView struct {
	Brand    string `json:"brand"`
	Last4    string `json:"last4"`
	ExpMonth int    `json:"exp_month"`
	ExpYear  int    `json:"exp_year"`
	Default  bool   `json:"default"`
}

type accountView struct {
	Status              billing.Status      `json:"status"`
	AIEnabled           bool                `json:"ai_enabled"`
	AIEndsAt            *time.Time          `json:"ai_ends_at"`
	SpendingLimitUSD    *string             `json:"spending_limit_usd"`
	Period              periodView          `json:"period"`
	Usage               []usageView         `json:"usage"`
	CreditUsedCents     int64               `json:"credit_used_cents"`
	OverageCents        int64               `json:"overage_cents"`
	ProjectedTotalCents int64               `json:"projected_total_cents"`
	PaymentMethods      []paymentMethodView `json:"payment_methods"`
}

func (r *routes) account(c forge.Context) error {
	p, err := r.allow(c, security.MemberManage)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	sum, err := r.svc.Summary(ctx, p.OrgID)
	if err != nil {
		return fail(ctx, p.OrgID, err)
	}
	v := accountView{
		Status: sum.Account.Status, AIEnabled: sum.Account.AIEnabled, AIEndsAt: sum.Account.AIEndsAt,
		SpendingLimitUSD: dollars(sum.Account.SpendingLimitCents),
		Period:           periodView{Start: sum.PeriodStart, End: sum.PeriodEnd},
		Usage:            make([]usageView, 0, len(sum.Usage)),
		CreditUsedCents:  sum.CreditUsedCents, OverageCents: sum.OverageCents, ProjectedTotalCents: sum.ProjectedTotalCents,
		PaymentMethods: make([]paymentMethodView, 0, len(sum.PaymentMethods)),
	}
	for _, u := range sum.Usage {
		v.Usage = append(v.Usage, usageView{Feature: u.Feature, Cents: u.Cents})
	}
	for _, pm := range sum.PaymentMethods {
		v.PaymentMethods = append(v.PaymentMethods, paymentMethodView{
			Brand: pm.Brand, Last4: pm.Last4, ExpMonth: pm.ExpiryMonth, ExpYear: pm.ExpiryYear, Default: pm.IsDefault,
		})
	}
	return c.JSON(http.StatusOK, v)
}

type urlView struct {
	URL string `json:"url"`
}

func (r *routes) setupSession(c forge.Context) error {
	p, err := r.allowOwner(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	url, err := r.svc.SetupSession(ctx, p.OrgID, p.UserID)
	if err != nil {
		return fail(ctx, p.OrgID, err)
	}
	return c.JSON(http.StatusOK, urlView{URL: url})
}

// limitPattern is a dollar amount with at most two decimals and no sign.
var limitPattern = regexp.MustCompile(`^(0|[1-9]\d{0,6})(\.\d{1,2})?$`)

// maxLimitCents is the highest limit an owner can set: $1,000,000.00,
// as billing.SetSpendingLimit allows.
const maxLimitCents = 100_000_000

const msgLimit = `Send limit_usd as a dollar amount from "0.00" to "1000000.00" with at most two decimals, or null for no limit.`

type limitBody struct {
	LimitUSD json.RawMessage `json:"limit_usd"`
}

type limitView struct {
	Status           billing.Status `json:"status,omitempty"` // empty while the limit waits for its evaluation
	SpendingLimitUSD *string        `json:"spending_limit_usd"`
}

func (r *routes) setSpendingLimit(c forge.Context) error {
	p, err := r.allowOwner(c)
	if err != nil {
		return err
	}
	var body limitBody
	if err = json.NewDecoder(io.LimitReader(c.Request().Body, 4<<10)).Decode(&body); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `The body must be JSON: {"limit_usd": "25.00"}`)
	}
	cents, ok := parseLimit(body.LimitUSD)
	if !ok {
		return forge.NewHTTPError(http.StatusUnprocessableEntity, msgLimit)
	}
	// A request waiting behind a long evaluation gives up cleanly.
	ctx, cancel := context.WithTimeout(c.Request().Context(), limitTimeout)
	defer cancel()
	a, err := r.svc.SetSpendingLimit(ctx, p.OrgID, cents)
	if errors.Is(err, billing.ErrLimitNotChecked) {
		slog.WarnContext(ctx, "billing: a spending limit is saved and waits for the next evaluation", "org", p.OrgID, "error", err)
		return c.JSON(http.StatusAccepted, limitView{SpendingLimitUSD: dollars(cents)})
	}
	if err != nil {
		return fail(ctx, p.OrgID, err)
	}
	return c.JSON(http.StatusOK, limitView{Status: a.Status, SpendingLimitUSD: dollars(a.SpendingLimitCents)})
}

// parseLimit reads limit_usd: JSON null is no limit; anything else is a
// string of dollars, at most two decimals, from 0 to 1,000,000.00. A
// missing field or a JSON number isn't a limit.
func parseLimit(raw json.RawMessage) (*int64, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	if string(raw) == "null" {
		return nil, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || !limitPattern.MatchString(s) {
		return nil, false
	}
	whole, frac, _ := strings.Cut(s, ".")
	frac += strings.Repeat("0", 2-len(frac))
	d, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return nil, false
	}
	c, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return nil, false
	}
	cents := d*100 + c
	if cents > maxLimitCents {
		return nil, false
	}
	return &cents, true
}

// dollars writes cents the way limit_usd is sent: 2500 is "25.00".
func dollars(cents *int64) *string {
	if cents == nil {
		return nil
	}
	s := strconv.FormatInt(*cents/100, 10) + "." + leftPad(strconv.FormatInt(*cents%100, 10))
	return &s
}

func leftPad(s string) string {
	if len(s) < 2 {
		return "0" + s
	}
	return s
}

// setAI turns the AI add-on on or off. Task 9 fills it in.
func (r *routes) setAI(c forge.Context) error {
	if _, err := r.allowOwner(c); err != nil {
		return err
	}
	return forge.NewHTTPError(http.StatusNotImplemented, "The AI add-on can't be changed yet.")
}

type invoiceView struct {
	ID          string    `json:"id"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	TotalCents  int64     `json:"total_cents"`
	Status      string    `json:"status"`
	Plan        string    `json:"plan"`
}

func (r *routes) invoices(c forge.Context) error {
	p, err := r.allow(c, security.MemberManage)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	invs, err := r.svc.Invoices(ctx, p.OrgID)
	if err != nil {
		return fail(ctx, p.OrgID, err)
	}
	out := make([]invoiceView, 0, len(invs))
	for _, inv := range invs {
		out = append(out, invoiceView{ID: inv.ID, PeriodStart: inv.PeriodStart, PeriodEnd: inv.PeriodEnd,
			TotalCents: inv.TotalCents, Status: inv.Status, Plan: inv.Plan})
	}
	return c.JSON(http.StatusOK, out)
}

func (r *routes) invoiceLink(c forge.Context) error {
	p, err := r.allow(c, security.MemberManage)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	url, err := r.svc.InvoiceLink(ctx, p.OrgID, c.Param("id"))
	if err != nil {
		return fail(ctx, p.OrgID, err)
	}
	return c.JSON(http.StatusOK, urlView{URL: url})
}
