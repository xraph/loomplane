package discovery

import (
	"io"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/discovery"
)

// maxDelivery is the largest webhook body read. GitHub caps payloads at
// 25 MB; a push that size isn't one worth reading.
const maxDelivery = 5 << 20

// webhookRoute serves GitHub's webhook deliveries. It's outside the guard:
// GitHub has no session, and the delivery's signature is what's checked.
func (e *Extension) webhookRoute(r forge.Router, hooks *discovery.Webhooks) error {
	return r.POST("/api/v1/github/webhook", func(c forge.Context) error {
		if len(hooks.Secret) == 0 {
			return forge.NewHTTPError(http.StatusNotFound, "webhooks are off: set github.webhook_secret")
		}
		req := c.Request()
		body, err := io.ReadAll(io.LimitReader(req.Body, maxDelivery))
		if err != nil {
			return forge.NewHTTPError(http.StatusBadRequest, "read the delivery")
		}
		if verr := hooks.Verify(req.Header.Get("X-Hub-Signature-256"), body); verr != nil {
			return forge.NewHTTPError(http.StatusUnauthorized, verr.Error())
		}
		started, err := hooks.Handle(req.Context(), req.Header.Get("X-GitHub-Event"), body)
		if err != nil && started == 0 {
			e.Logger().Warn("handle a GitHub webhook", forge.F("error", err.Error()))
			return forge.NewHTTPError(http.StatusInternalServerError, "the delivery couldn't be handled")
		}
		if err != nil {
			e.Logger().Warn("some projects weren't re-analyzed", forge.F("error", err.Error()))
		}
		return c.JSON(http.StatusAccepted, map[string]int{"analyses_started": started})
	}, forge.WithName("github.webhook"), forge.WithSchemaExclude())
}
