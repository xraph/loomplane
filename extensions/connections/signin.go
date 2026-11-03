package connections

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/connections"
)

// SignInAppList is the body of GET /api/v1/cloud-sign-in/apps: each cloud
// that can be signed in to, where its app comes from, and the callback URL
// to register with an app. Keyless says whether clouds can connect
// keylessly, through Loomplane's own issuer.
type SignInAppList struct {
	Apps        []connections.SignInApp `json:"apps"`
	CallbackURL string                  `json:"callback_url"`
	Keyless     KeylessState            `json:"keyless"`
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
	if _, err := principal(c); err != nil {
		return err
	}
	apps, err := e.svc.SignInApps(c.Request().Context())
	if err != nil {
		return fail(err)
	}
	enabled, reason := e.keylessState()
	return c.JSON(http.StatusOK, SignInAppList{Apps: apps, CallbackURL: e.svc.CallbackURL,
		Keyless: KeylessState{Enabled: enabled, Issuer: e.issuer.URL(), Reason: reason}})
}

// setSignInApp and deleteSignInApp are closed until Task 4's platform-admin
// routes replace them: nothing here may change the platform's apps yet.
func (e *Extension) setSignInApp(forge.Context) error {
	return forge.NewHTTPError(http.StatusNotFound, "not found")
}

func (e *Extension) deleteSignInApp(forge.Context) error {
	return forge.NewHTTPError(http.StatusNotFound, "not found")
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
