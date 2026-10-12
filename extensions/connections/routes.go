package connections

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

// ConnectionList is the body of GET /api/v1/connections.
type ConnectionList struct {
	Connections []connections.Connection `json:"connections"`
}

// ForgeEnvelope tells the generated client the list wraps Connections.
func (ConnectionList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "connections"}
}

func (e *Extension) routes(r forge.Router, guard identity.Guard) error {
	g := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Cloud connections"))
	if err := g.GET("/connections", e.list,
		forge.WithName("connections.list"),
		forge.WithSummary("The organization's cloud connections, never their credentials"),
		forge.WithResponseSchema(http.StatusOK, "Connections", ConnectionList{}),
	); err != nil {
		return err
	}
	if err := g.POST("/connections", e.create,
		forge.WithName("connections.create"),
		forge.WithSummary("Connect a Google Cloud project or a Vercel account, checking the credential with the cloud first"),
		forge.WithRequestSchema(connections.CreateInput{}),
		forge.WithResponseSchema(http.StatusCreated, "The connection", connections.Connection{}),
	); err != nil {
		return err
	}
	if err := g.GET("/connections/:id", e.get,
		forge.WithName("connections.get"),
		forge.WithSummary("One cloud connection"),
		forge.WithResponseSchema(http.StatusOK, "The connection", connections.Connection{}),
	); err != nil {
		return err
	}
	if err := g.POST("/connections/:id/check", e.recheck,
		forge.WithName("connections.check"),
		forge.WithSummary("Ask the cloud again what the connection's credential may do"),
		forge.WithResponseSchema(http.StatusOK, "The connection", connections.Connection{}),
	); err != nil {
		return err
	}
	if err := g.PUT("/connections/:id/credential", e.replace,
		forge.WithName("connections.replace"),
		forge.WithSummary("Replace the connection's credential, such as with a rotated key"),
		forge.WithRequestSchema(connections.ReplaceInput{}),
		forge.WithResponseSchema(http.StatusOK, "The connection", connections.Connection{}),
	); err != nil {
		return err
	}
	return g.DELETE("/connections/:id", e.delete,
		forge.WithName("connections.delete"),
		forge.WithSummary("Remove a cloud connection and its credential"),
		forge.WithNoContentResponse(),
		forge.WithEntity(forge.EntityDef{Type: "Connection", IDField: "id"}),
	)
}

func principal(c forge.Context) (security.Principal, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return p, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	return p, nil
}

func (e *Extension) list(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	list, err := e.svc.List(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	out := ConnectionList{Connections: make([]connections.Connection, 0, len(list))}
	for _, conn := range list {
		out.Connections = append(out.Connections, *conn)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) create(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in connections.CreateInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"cloud": "gcp", "name": "...", "credential": "..."}`)
	}
	conn, err := e.svc.Create(c.Request().Context(), p, in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, conn)
}

func (e *Extension) get(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	conn, err := e.svc.Get(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, conn)
}

func (e *Extension) recheck(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	conn, err := e.svc.Recheck(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, conn)
}

func (e *Extension) replace(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in connections.ReplaceInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"credential": "..."}`)
	}
	conn, err := e.svc.Replace(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, conn)
}

func (e *Extension) delete(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err = e.svc.Delete(c.Request().Context(), p, c.Param("id")); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}
