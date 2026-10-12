package identity

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/security"
)

// guard authenticates a request with an API key or, without one, the
// authsome session, and puts the principal on its context. A request
// with a Loomplane key never falls back to a session: a key that doesn't
// check out is a 401, not an anonymous request.
func (e *Extension) guard() forge.Middleware {
	chain := e.middleware()
	return func(next forge.Handler) forge.Handler {
		withSession := next
		for i := len(chain) - 1; i >= 0; i-- {
			withSession = chain[i](withSession)
		}
		return func(c forge.Context) error {
			raw, ok := strings.CutPrefix(c.Request().Header.Get("Authorization"), "Bearer ")
			if !ok || !apikeys.IsKey(raw) {
				return withSession(c)
			}
			ctx := c.Request().Context()
			p, err := e.keys.Authenticate(ctx, strings.TrimSpace(raw))
			e.telemetry.KeyAuthenticated(err == nil)
			if err != nil {
				return forge.NewHTTPError(http.StatusUnauthorized, err.Error())
			}
			c.WithContext(security.WithPrincipal(ctx, p))
			return next(c)
		}
	}
}

// APIKeyList is the keys the caller can see.
type APIKeyList struct {
	Keys []apikeys.APIKey `json:"keys"`
}

func (e *Extension) keyRoutes(r forge.Router) error {
	g := r.Group("/api/v1/api-keys", forge.WithGroupMiddleware(e.guard()), forge.WithGroupTags("API keys"))
	if err := g.GET("", func(c forge.Context) error {
		p, err := principalOf(c)
		if err != nil {
			return err
		}
		keys, err := e.keys.List(c.Request().Context(), p)
		if err != nil {
			return keyError(err)
		}
		return c.JSON(http.StatusOK, APIKeyList{Keys: keys})
	},
		forge.WithName("apiKeys.list"),
		forge.WithSummary("Your API keys in this organization, or everyone's for an owner or admin"),
		forge.WithResponseSchema(http.StatusOK, "The keys", APIKeyList{}),
	); err != nil {
		return err
	}
	if err := g.POST("", func(c forge.Context) error {
		p, err := principalOf(c)
		if err != nil {
			return err
		}
		var in apikeys.NewKey
		if berr := c.BindJSON(&in); berr != nil {
			return forge.NewHTTPError(http.StatusBadRequest, "send name, access (read or write) and optionally expires_in_days")
		}
		out, err := e.keys.Create(c.Request().Context(), p, in)
		if err != nil {
			return keyError(err)
		}
		return c.JSON(http.StatusCreated, out)
	},
		forge.WithName("apiKeys.create"),
		forge.WithSummary("Make an API key that acts as you here. The secret is shown once."),
		forge.WithRequestSchema(apikeys.NewKey{}),
		forge.WithResponseSchema(http.StatusCreated, "The key, with its secret", apikeys.CreatedKey{}),
	); err != nil {
		return err
	}
	return g.DELETE("/:id", func(c forge.Context) error {
		p, err := principalOf(c)
		if err != nil {
			return err
		}
		if err := e.keys.Revoke(c.Request().Context(), p, c.Param("id")); err != nil {
			return keyError(err)
		}
		return c.NoContent(http.StatusNoContent)
	},
		forge.WithName("apiKeys.revoke"),
		forge.WithSummary("Revoke an API key, at once"),
		forge.WithNoContentResponse(),
		forge.WithEntity(forge.EntityDef{Type: "APIKey", IDField: "id"}),
	)
}

func principalOf(c forge.Context) (security.Principal, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return security.Principal{}, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	return p, nil
}

func keyError(err error) error {
	switch {
	case errors.Is(err, apikeys.ErrInvalid):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, apikeys.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, apikeys.ErrNotWithKey):
		return forge.NewHTTPError(http.StatusForbidden, err.Error())
	}
	return err
}

// keyAuditor records key changes in Chronicle, in the organization's
// stream: the key's name, access and hint, never the key.
type keyAuditor struct {
	chronicle *chronicle.Chronicle
	appID     string
	log       forge.Logger
}

func (a keyAuditor) Record(ctx context.Context, action string, p security.Principal, k apikeys.APIKey) {
	b := a.chronicle.Warning(ctx, action, "api_key", k.ID).
		Category("api_keys").AppID(a.appID).TenantID(p.OrgID).UserID(p.UserID).Outcome("success").
		Meta("name", k.Name).Meta("access", string(k.Access)).Meta("hint", k.Hint).Meta("created_by", k.CreatedBy)
	if err := b.Record(); err != nil {
		a.log.Error("record audit event", forge.F("action", action), forge.F("error", err.Error()))
	}
}
