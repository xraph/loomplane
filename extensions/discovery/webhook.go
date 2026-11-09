package discovery

import (
	"errors"
	"io"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/repositories"
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

// bitbucketWebhookRoute serves Bitbucket's deliveries, one address per
// hook. It's outside the guard like GitHub's: the hook's own signature is
// what's checked, and a hook only ever starts analyses in its organization.
func (e *Extension) bitbucketWebhookRoute(r forge.Router, hooks *discovery.BitbucketWebhooks) error {
	return r.POST("/api/v1/bitbucket/webhook/:hook_id", func(c forge.Context) error {
		req := c.Request()
		body, err := io.ReadAll(io.LimitReader(req.Body, maxDelivery))
		if err != nil {
			return forge.NewHTTPError(http.StatusBadRequest, "read the delivery")
		}
		started, err := hooks.Deliver(req.Context(), c.Param("hook_id"), req.Header.Get("X-Event-Key"), req.Header.Get("X-Hub-Signature"), body)
		switch {
		case errors.Is(err, repositories.ErrHookNotFound):
			return forge.NewHTTPError(http.StatusNotFound, "there's no such webhook")
		case errors.Is(err, discovery.ErrBadSignature):
			return forge.NewHTTPError(http.StatusUnauthorized, err.Error())
		case errors.Is(err, discovery.ErrInvalid):
			return forge.NewHTTPError(http.StatusBadRequest, err.Error())
		case err != nil && started == 0:
			e.Logger().Warn("handle a Bitbucket webhook", forge.F("error", err.Error()))
			return forge.NewHTTPError(http.StatusInternalServerError, "the delivery couldn't be handled")
		case err != nil:
			e.Logger().Warn("some projects weren't re-analyzed", forge.F("error", err.Error()))
		}
		return c.JSON(http.StatusAccepted, map[string]int{"analyses_started": started})
	}, forge.WithName("bitbucket.webhook"), forge.WithSchemaExclude())
}

// gitlabWebhookRoute serves GitLab's deliveries, one URL per connection. It's
// outside the guard for the same reason GitHub's is: GitLab has no session,
// and the secret of the connection in the URL is what's checked. Nothing a
// delivery carries is echoed back or logged.
func (e *Extension) gitlabWebhookRoute(r forge.Router, hooks *discovery.GitLabWebhooks) error {
	return r.POST("/api/v1/gitlab/webhook/:id", func(c forge.Context) error {
		req := c.Request()
		body, err := io.ReadAll(io.LimitReader(req.Body, maxDelivery))
		if err != nil {
			return forge.NewHTTPError(http.StatusBadRequest, "read the delivery")
		}
		started, err := hooks.Handle(req.Context(), c.Param("id"), req.Header, body)
		switch {
		case errors.Is(err, repositories.ErrGitLabConnectionNotFound):
			return forge.NewHTTPError(http.StatusNotFound, "no webhook is set up for that GitLab connection")
		case errors.Is(err, discovery.ErrBadSignature):
			return forge.NewHTTPError(http.StatusUnauthorized, err.Error())
		case err != nil && started == 0:
			e.Logger().Warn("handle a GitLab webhook", forge.F("error", err.Error()))
			return forge.NewHTTPError(http.StatusInternalServerError, "the delivery couldn't be handled")
		case err != nil:
			e.Logger().Warn("some projects weren't re-analyzed", forge.F("error", err.Error()))
		}
		return c.JSON(http.StatusAccepted, map[string]int{"analyses_started": started})
	}, forge.WithName("gitlab.webhook"), forge.WithSchemaExclude())
}
