package identity

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/security"
)

// errNotTheOwner is what anyone but the platform owner gets from an
// owner-only route: the 404 an unknown route would give.
var errNotTheOwner = forge.NewHTTPError(http.StatusNotFound, "not found")

// ownerKey carries the platform owner OwnerOnly let through.
type ownerKey struct{}

// OwnerOnly is the middleware of a route that is the platform owner's
// alone: the guard, then a gate that lets only the owner through. Anyone
// else gets the 404 an unknown route would, an anonymous caller included,
// so the route's existence isn't given away. The owner comes from the
// session alone: an API key, the AI agent or anything acting for the
// owner never passes, nor does a platform admin who isn't the owner.
// isOwner is the PlatformOwner identity provides; OwnerID reads who it
// let through.
func OwnerOnly(guard Guard, isOwner PlatformOwner) []forge.Middleware {
	hide := func(next forge.Handler) forge.Handler {
		return func(c forge.Context) error {
			err := next(c)
			var status interface{ StatusCode() int }
			if errors.As(err, &status) && (status.StatusCode() == http.StatusUnauthorized || status.StatusCode() == http.StatusForbidden) {
				return errNotTheOwner
			}
			return err
		}
	}
	gate := func(next forge.Handler) forge.Handler {
		return func(c forge.Context) error {
			ctx := c.Request().Context()
			p, ok := security.PrincipalFrom(ctx)
			if !ok || p.KeyID != "" || p.Kind != "" || p.ReadOnly || p.OnBehalfOf != nil {
				return errNotTheOwner
			}
			userID, ok := isOwner(ctx)
			if !ok || userID == "" {
				return errNotTheOwner
			}
			c.WithContext(context.WithValue(ctx, ownerKey{}, userID))
			return next(c)
		}
	}
	return slices.Concat([]forge.Middleware{hide}, guard, []forge.Middleware{gate})
}

// OwnerID is the platform owner OwnerOnly let through, or "" on a route
// it doesn't gate.
func OwnerID(c forge.Context) string {
	id, ok := c.Request().Context().Value(ownerKey{}).(string)
	if !ok {
		return ""
	}
	return id
}
