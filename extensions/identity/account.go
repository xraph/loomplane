package identity

import (
	"net/http"

	"github.com/xraph/forge"
)

// TwoStep says whether the signed-in account has two-step verification on.
// authsome serves the rest of two-step (enrol, verify, turn off, recovery
// codes) but has no route that reads this.
type TwoStep struct {
	Enabled bool `json:"enabled"`
}

// accountRoutes serve the signed-in account itself, so they need no
// organization, which the guard would ask for. They take a browser session
// only: neither an API key nor a loomctl token reads them.
func (e *Extension) accountRoutes(r forge.Router) error {
	g := r.Group("/api/v1/account", forge.WithGroupMiddleware(e.sessionMiddleware()...), forge.WithGroupTags("Identity"))
	return g.GET("/two-step", e.getTwoStep,
		forge.WithName("account.twoStep.get"),
		forge.WithSummary("Whether your account asks for a code from an authenticator app at sign-in"),
		forge.WithResponseSchema(http.StatusOK, "Two-step verification", TwoStep{}),
	)
}

func (e *Extension) getTwoStep(c forge.Context) error {
	ctx := c.Request().Context()
	uid, err := e.sessionUser(ctx)
	if err != nil {
		return err
	}
	on, err := e.mfa.Enrolled(ctx, uid)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, TwoStep{Enabled: on})
}
