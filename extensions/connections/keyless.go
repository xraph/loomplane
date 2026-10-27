package connections

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/connections"
)

// wellKnownCache is how long a cloud may keep discovery and the keys. The
// issuer publishes a new key a day before it signs, so five minutes is
// far inside that.
const wellKnownCache = "public, max-age=300"

// KeylessPrepareInput is the body of POST
// /api/v1/connections/keyless/prepare: the cloud to connect keylessly.
type KeylessPrepareInput struct {
	Cloud string `json:"cloud"`
}

// KeylessReplaceInput is the body of PUT /api/v1/connections/:id/keyless:
// the settings of the cloud's trust. None keeps the connection's own.
type KeylessReplaceInput struct {
	Settings map[string]string `json:"settings"`
}

// KeylessState says whether clouds can connect keylessly: Enabled when a
// cloud could reach the issuer, the issuer's URL, and Reason, why not, or
// a warning when it works only in development.
type KeylessState struct {
	Enabled bool   `json:"enabled"`
	Issuer  string `json:"issuer"`
	Reason  string `json:"reason"`
}

// keylessState is whether the issuer is usable, and why not.
func (e *Extension) keylessState() (enabled bool, reason string) {
	if e.svc.Keyless == nil {
		return false, "Loomplane has no keyless issuer"
	}
	return e.svc.Keyless.Usable()
}

func (e *Extension) keylessRoutes(r forge.Router, guard identity.Guard) error {
	// The clouds fetch discovery and the keys themselves, with no session.
	// They're served at the root, where the issuer's URL puts them, and
	// stay out of the API's contract: the dashboard never calls them.
	if err := r.GET("/.well-known/openid-configuration", e.discovery,
		forge.WithName("keylessIssuer.discovery"), forge.WithSchemaExclude(),
	); err != nil {
		return err
	}
	if err := r.GET("/.well-known/jwks.json", e.jwks,
		forge.WithName("keylessIssuer.jwks"), forge.WithSchemaExclude(),
	); err != nil {
		return err
	}

	g := r.Group("/api/v1/connections", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Keyless"))
	if err := g.POST("/keyless/prepare", e.prepareKeyless,
		forge.WithName("connectionKeyless.prepare"),
		forge.WithSummary("Reserve a new keyless connection's ID for a day, and say how to set up the cloud's trust in it"),
		forge.WithRequestSchema(KeylessPrepareInput{}),
		forge.WithResponseSchema(http.StatusOK, "How to set up the cloud's trust", connections.KeylessSetup{}),
	); err != nil {
		return err
	}
	if err := g.POST("/keyless", e.createKeyless,
		forge.WithName("connectionKeyless.create"),
		forge.WithSummary("Connect a cloud keylessly, with the reserved ID, once the cloud accepts Loomplane's token"),
		forge.WithRequestSchema(connections.KeylessInput{}),
		forge.WithResponseSchema(http.StatusCreated, "The connection", connections.Connection{}),
	); err != nil {
		return err
	}
	if err := g.GET("/:id/keyless", e.keylessSetup,
		forge.WithName("connectionKeyless.setup"),
		forge.WithSummary("How to set up the cloud's trust in an existing connection, to move it to keyless"),
		forge.WithResponseSchema(http.StatusOK, "How to set up the cloud's trust", connections.KeylessSetup{}),
	); err != nil {
		return err
	}
	return g.PUT("/:id/keyless", e.replaceKeyless,
		forge.WithName("connectionKeyless.replace"),
		forge.WithSummary("Move a connection to keyless, or change its keyless settings, if the cloud's trust reaches the same account"),
		forge.WithRequestSchema(KeylessReplaceInput{}),
		forge.WithResponseSchema(http.StatusOK, "The connection", connections.Connection{}),
	)
}

func (e *Extension) discovery(c forge.Context) error {
	c.Response().Header().Set("Cache-Control", wellKnownCache)
	return c.JSON(http.StatusOK, e.issuer.Discovery())
}

func (e *Extension) jwks(c forge.Context) error {
	keys, err := e.issuer.JWKS(c.Request().Context())
	if err != nil {
		e.Logger().Warn("read the keyless issuer's keys", forge.F("error", err.Error()))
		return forge.NewHTTPError(http.StatusServiceUnavailable, "Loomplane's signing keys can't be read right now")
	}
	c.Response().Header().Set("Cache-Control", wellKnownCache)
	return c.JSON(http.StatusOK, keys)
}

func (e *Extension) prepareKeyless(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in KeylessPrepareInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"cloud": "gcp"}`)
	}
	out, err := e.svc.PrepareKeyless(c.Request().Context(), p, in.Cloud)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) createKeyless(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in connections.KeylessInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"connection_id": "...", "name": "...", "settings": {...}}`)
	}
	conn, err := e.svc.CreateKeyless(c.Request().Context(), p, in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, conn)
}

func (e *Extension) keylessSetup(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	out, err := e.svc.KeylessSetupFor(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) replaceKeyless(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in KeylessReplaceInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"settings": {...}}`)
	}
	conn, err := e.svc.ReplaceKeyless(c.Request().Context(), p, c.Param("id"), in.Settings)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, conn)
}
