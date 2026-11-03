package connections

import (
	"context"
	"net/http"
	"slices"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/connections"
)

// SignInAppList is the body of GET /api/v1/platform/cloud-sign-in/apps: each
// cloud that can be signed in to, where its app comes from, how many
// sign-ins work with it, and the callback URL to register with an app.
// Keyless says whether clouds can connect keylessly, through Loomplane's
// own issuer.
type SignInAppList struct {
	Apps        []connections.SignInApp `json:"apps"`
	CallbackURL string                  `json:"callback_url"`
	Keyless     KeylessState            `json:"keyless"`
}

// platformUserKey carries the platform user the gate found to the handlers.
type platformUserKey struct{}

// platformGate lets only a platform user through, and gives everyone else
// the 404 an unknown route would. It runs after the guard, so an API key
// has been authenticated by then and still isn't a platform user.
func (e *Extension) platformGate() forge.Middleware {
	return func(next forge.Handler) forge.Handler {
		return func(c forge.Context) error {
			ctx := c.Request().Context()
			userID, ok := e.platformUser(ctx)
			if !ok {
				return forge.NewHTTPError(http.StatusNotFound, "not found")
			}
			c.WithContext(context.WithValue(ctx, platformUserKey{}, userID))
			return next(c)
		}
	}
}

func platformUserID(c forge.Context) string {
	id, _ := c.Request().Context().Value(platformUserKey{}).(string)
	return id
}

func (e *Extension) signInRoutes(r forge.Router, guard identity.Guard) error {
	apps := r.Group("/api/v1/platform/cloud-sign-in",
		forge.WithGroupMiddleware(slices.Concat(guard, []forge.Middleware{e.platformGate()})...),
		forge.WithGroupTags("Cloud sign-in"))
	if err := apps.GET("/apps", e.signInApps,
		forge.WithName("platformCloudSignInApps.list"),
		forge.WithSummary("The clouds people can sign in to, where each one's app comes from, how many sign-ins use it, and the callback URL to register"),
		forge.WithResponseSchema(http.StatusOK, "Sign-in apps", SignInAppList{}),
		forge.WithResponseSchema(http.StatusNotFound, "Not a platform user", nil),
	); err != nil {
		return err
	}
	if err := apps.PUT("/apps/:cloud", e.setSignInApp,
		forge.WithName("platformCloudSignInApps.set"),
		forge.WithSummary("Set the platform's sign-in app for a cloud. The secret is never returned"),
		forge.WithRequestSchema(connections.AppInput{}),
		forge.WithResponseSchema(http.StatusOK, "The sign-in app", connections.SignInApp{}),
		forge.WithResponseSchema(http.StatusNotFound, "Not a platform user", nil),
		forge.WithResponseSchema(http.StatusConflict, "The cloud's app comes from configuration", nil),
		forge.WithResponseSchema(http.StatusUnprocessableEntity, "The app is missing something", nil),
	); err != nil {
		return err
	}
	if err := apps.DELETE("/apps/:cloud", e.deleteSignInApp,
		forge.WithName("platformCloudSignInApps.delete"),
		forge.WithSummary("Remove the platform's sign-in app for a cloud"),
		forge.WithNoContentResponse(),
		forge.WithResponseSchema(http.StatusNotFound, "Not a platform user, or the cloud has no platform app", nil),
		forge.WithResponseSchema(http.StatusConflict, "The cloud's app comes from configuration", nil),
	); err != nil {
		return err
	}

	g := r.Group("/api/v1/connections/sign-ins", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Cloud sign-in"))
	if err := g.POST("", e.startSignIn,
		forge.WithName("connectionSignIns.start"),
		forge.WithSummary("Start signing in to a cloud, for a new connection or in place of an existing one's credential"),
		forge.WithRequestSchema(connections.SignInInput{}),
		forge.WithResponseSchema(http.StatusOK, "Where to send you to sign in", connections.SignIn{}),
	); err != nil {
		return err
	}
	return g.POST("/complete", e.completeSignIn,
		forge.WithName("connectionSignIns.complete"),
		forge.WithSummary("Finish a sign-in with what the cloud sent back to the callback"),
		forge.WithRequestSchema(connections.CompleteInput{}),
		forge.WithResponseSchema(http.StatusCreated, "The new connection", connections.Connection{}),
		forge.WithResponseSchema(http.StatusOK, "The connection, with its credential replaced", connections.Connection{}),
	)
}

func (e *Extension) signInApps(c forge.Context) error {
	apps, err := e.svc.SignInApps(c.Request().Context())
	if err != nil {
		return fail(err)
	}
	enabled, reason := e.keylessState()
	return c.JSON(http.StatusOK, SignInAppList{Apps: apps, CallbackURL: e.svc.CallbackURL,
		Keyless: KeylessState{Enabled: enabled, Issuer: e.issuer.URL(), Reason: reason}})
}

func (e *Extension) setSignInApp(c forge.Context) error {
	var in connections.AppInput
	if err := c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"client_id": "...", "client_secret": "..."}`)
	}
	app, err := e.svc.SetSignInApp(c.Request().Context(), platformUserID(c), c.Param("cloud"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, app)
}

func (e *Extension) deleteSignInApp(c forge.Context) error {
	if err := e.svc.DeleteSignInApp(c.Request().Context(), platformUserID(c), c.Param("cloud")); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (e *Extension) startSignIn(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in connections.SignInInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"cloud": "gcp", "name": "...", "settings": {"project_id": "..."}}`)
	}
	out, err := e.svc.StartSignIn(c.Request().Context(), p, in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) completeSignIn(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in connections.CompleteInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"state": "...", "code": "..."}`)
	}
	conn, err := e.svc.CompleteSignIn(c.Request().Context(), p, in)
	if err != nil {
		return fail(err)
	}
	// A new connection is created and updated at the same moment. One whose
	// credential was replaced keeps the time it was first created.
	status := http.StatusOK
	if conn.CreatedAt.Equal(conn.UpdatedAt) {
		status = http.StatusCreated
	}
	return c.JSON(status, conn)
}
