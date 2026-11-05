//go:build cloud

package billing

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/billing"
)

// webhookPath is where Stripe sends billing's events.
const webhookPath = base + "/webhooks/stripe"

// maxEvent is the largest Stripe event read. Stripe's events are a few
// kilobytes.
const maxEvent = 1 << 20

// mountWebhook serves Stripe's webhook. It's outside the guard: Stripe has
// no session, and the event's signature is what's checked. A 2xx tells
// Stripe the event is applied; anything else and Stripe sends it again.
func mountWebhook(router forge.Router, svc billingService) error {
	return router.POST(webhookPath, func(c forge.Context) error {
		req := c.Request()
		ctx := req.Context()
		payload, err := io.ReadAll(io.LimitReader(req.Body, maxEvent))
		if err != nil {
			return forge.NewHTTPError(http.StatusBadRequest, "The event couldn't be read.")
		}
		err = svc.HandleStripeWebhook(ctx, payload, req.Header.Get("Stripe-Signature"))
		switch {
		case err == nil:
			return c.JSON(http.StatusOK, map[string]bool{"received": true})
		case errors.Is(err, billing.ErrAPIVersion):
			// Every event fails this way until the endpoint's version in
			// Stripe is changed, and Stripe disables an endpoint that keeps
			// failing, so it is an error and names the version to set.
			slog.ErrorContext(ctx, "billing: refused a Stripe event on another API version; set the webhook endpoint in Stripe to the expected version",
				"expected_api_version", billing.StripeAPIVersion, "error", err)
			return forge.NewHTTPError(http.StatusBadRequest,
				"The event's Stripe API version isn't the one Loomplane reads. Set the webhook endpoint to "+billing.StripeAPIVersion+".")
		case errors.Is(err, billing.ErrBadSignature):
			slog.WarnContext(ctx, "billing: refused a Stripe event with a bad signature", "error", err)
			return forge.NewHTTPError(http.StatusBadRequest, "The signature doesn't match.")
		case errors.Is(err, billing.ErrBadEvent):
			slog.WarnContext(ctx, "billing: refused a Stripe event it can't read", "error", err)
			return forge.NewHTTPError(http.StatusBadRequest, "The event can't be read.")
		}
		slog.ErrorContext(ctx, "billing: a Stripe event wasn't applied; Stripe sends it again", "error", err)
		return forge.NewHTTPError(http.StatusInternalServerError, "The event wasn't applied. Send it again.")
	}, forge.WithName("billing.stripeWebhook"), forge.WithSchemaExclude())
}
