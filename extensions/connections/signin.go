package connections

import (
	"errors"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/connections"
)

// SignInAppList is the body of GET /api/v1/cloud-sign-in/apps: each cloud
// that can be signed in to, where its app comes from, and the callback URL
// to register with an app.
type SignInAppList struct {
	Apps        []connections.SignInApp `json:"apps"`
	CallbackURL string                  `json:"callback_url"`
}

func (e *Extension) signInRoutes(r forge.Router, guard identity.Guard) error {
	apps := r.Group("/api/v1/cloud-sign-in", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Cloud sign-in"))
	if err := apps.GET("/apps", e.signInApps,
		forge.WithName("cloudSignInApps.list"),
		forge.WithSummary("The clouds you can sign in to, where each one's app comes from, and the callback URL to register"),
		forge.WithResponseSchema(http.StatusOK, "Sign-in apps", SignInAppList{}),
	); err != nil {
		return err
	}
	if err := apps.PUT("/apps/:cloud", e.setSignInApp,
		forge.WithName("cloudSignInApps.set"),
		forge.WithSummary("Set the organization's own sign-in app for a cloud. The secret is never returned"),
		forge.WithRequestSchema(connections.AppInput{}),
		forge.WithResponseSchema(http.StatusOK, "The sign-in app", connections.SignInApp{}),
	); err != nil {
		return err
	}
	if err := apps.DELETE("/apps/:cloud", e.deleteSignInApp,
		forge.WithName("cloudSignInApps.delete"),
		forge.WithSummary("Remove the organization's own sign-in app for a cloud"),
		forge.WithNoContentResponse(),
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
	p, err := principal(c)
	if err != nil {
		return err
	}
	apps, err := e.svc.SignInApps(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, SignInAppList{Apps: apps, CallbackURL: e.svc.CallbackURL})
}

func (e *Extension) setSignInApp(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in connections.AppInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"client_id": "...", "client_secret": "..."}`)
	}
	app, err := e.svc.SetSignInApp(c.Request().Context(), p, c.Param("cloud"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, app)
}

func (e *Extension) deleteSignInApp(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	err = e.svc.DeleteSignInApp(c.Request().Context(), p, c.Param("cloud"))
	if errors.Is(err, connections.ErrNotFound) {
		return forge.NewHTTPError(http.StatusNotFound, "this organization has no sign-in app of its own for that cloud")
	}
	if err != nil {
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
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"cloud": "digitalocean", "name": "..."}`)
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
