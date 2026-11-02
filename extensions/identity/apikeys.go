package identity

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/nodes"
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

// JoinedNodes lists the nodes that joined with a token an API key made. The
// nodes extension provides it; identity can't import that extension, so it
// asks for this when a revoke says the key was compromised.
type JoinedNodes interface {
	JoinedByKey(ctx context.Context, p security.Principal, keyID string) ([]nodes.Node, error)
}

// CompromisedRevoke is the answer to revoking a key as compromised: the key,
// and the nodes that joined with a token it made, which the operator may now
// want to revoke too.
type CompromisedRevoke struct {
	Revoked     apikeys.APIKey `json:"revoked"`
	JoinedNodes []nodes.Node   `json:"joined_nodes"`
	// JoinedNodesWithheld is true when the list is empty because it could not
	// be read, or the caller may not manage nodes. It does not mean no node joined.
	JoinedNodesWithheld bool `json:"joined_nodes_withheld"`
}

func (e *Extension) keyRoutes(r forge.Router) error { return e.keyRoutesWith(r, e.guard()) }

func (e *Extension) keyRoutesWith(r forge.Router, guard ...forge.Middleware) error {
	g := r.Group("/api/v1/api-keys", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("API keys"))
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
		ctx := c.Request().Context()
		revoked, err := e.keys.RevokeKey(ctx, p, c.Param("id"))
		if err != nil {
			return keyError(err)
		}
		if c.Request().URL.Query().Get("compromised") != "true" {
			return c.NoContent(http.StatusNoContent)
		}
		// Only the key's owner or an admin gets past RevokeKey, so they alone
		// see the node list. The key is revoked by now, so a list that cannot
		// be read is withheld and logged, never a failure of the revoke.
		out := CompromisedRevoke{Revoked: *revoked, JoinedNodes: []nodes.Node{}, JoinedNodesWithheld: true}
		if lister := e.joinedNodes(); lister != nil {
			joined, jerr := lister.JoinedByKey(ctx, p, revoked.ID)
			switch {
			case jerr == nil:
				out.JoinedNodes, out.JoinedNodesWithheld = append(out.JoinedNodes, joined...), false
			case errors.Is(jerr, security.ErrForbidden):
			default:
				e.Logger().Error("list the nodes that joined with a revoked key", forge.F("key", revoked.ID), forge.F("error", jerr.Error()))
			}
		}
		return c.JSON(http.StatusOK, out)
	},
		forge.WithName("apiKeys.revoke"),
		forge.WithSummary("Revoke an API key, at once. With compromised=true, also list the nodes that joined with a token it made."),
		forge.WithQuerySchema(RevokeKeyQuery{}),
		forge.WithNoContentResponse(),
		forge.WithResponseSchema(http.StatusOK, "The key and the nodes that joined with it", CompromisedRevoke{}),
		forge.WithEntity(forge.EntityDef{Type: "APIKey", IDField: "id"}),
	)
}

// RevokeKeyQuery is the query of apiKeys.revoke.
type RevokeKeyQuery struct {
	// Compromised asks for the nodes that joined with a token this key made.
	Compromised bool `query:"compromised,omitempty"`
}

func (e *Extension) joinedNodes() JoinedNodes {
	if e.joined == nil {
		return nil
	}
	return e.joined()
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
	a.RecordWithMeta(ctx, action, p, k, nil)
}

// RecordWithMeta is Record with extra string details, such as who minted a
// node join token and with which key.
func (a keyAuditor) RecordWithMeta(ctx context.Context, action string, p security.Principal, k apikeys.APIKey, meta map[string]string) {
	b := a.chronicle.Warning(ctx, action, "api_key", k.ID).
		Category("api_keys").AppID(a.appID).TenantID(p.OrgID).UserID(p.UserID).Outcome("success").
		Meta("name", k.Name).Meta("access", string(k.Access)).Meta("hint", k.Hint).Meta("created_by", k.CreatedBy)
	for name, v := range meta {
		b = b.Meta(name, v)
	}
	if err := b.Record(); err != nil {
		a.log.Error("record audit event", forge.F("action", action), forge.F("error", err.Error()))
	}
}
