package environments

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/security"
)

// ResolveQuery is the query of GET /api/v1/projects/:id/variables/resolved.
type ResolveQuery struct {
	// Environment is the environment's name.
	Environment string `query:"environment"`
	// Service is a service ID from the project's latest analysis. Without
	// it, the project as a whole: no service variables.
	Service string `query:"service,omitempty"`
}

// VariableList is the body of the variable lists.
type VariableList struct {
	Variables []environments.Variable `json:"variables"`
}

// ForgeEnvelope tells the generated client the list wraps Variables.
func (VariableList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "variables"}
}

func (e *Extension) routes(r forge.Router, guard identity.Guard) error {
	g := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Variables"))
	type route struct {
		method, path string
		handler      forge.Handler
		opts         []forge.RouteOption
	}
	routes := []route{
		{http.MethodGet, "/variables", e.listOrganization, []forge.RouteOption{
			forge.WithName("variables.list"),
			forge.WithSummary("The organization-wide variables every project inherits"),
			forge.WithResponseSchema(http.StatusOK, "Variables", VariableList{}),
		}},
		{http.MethodPost, "/variables", e.createOrganization, []forge.RouteOption{
			forge.WithName("variables.create"),
			forge.WithSummary("Set an organization-wide variable, for every environment or for one by name"),
			forge.WithRequestSchema(environments.CreateVariableInput{}),
			forge.WithResponseSchema(http.StatusCreated, "The variable", environments.Variable{}),
		}},
		{http.MethodGet, "/projects/:id/variables", e.listProject, []forge.RouteOption{
			forge.WithName("projectVariables.list"),
			forge.WithSummary("A project's own variables, project-wide and per service"),
			forge.WithResponseSchema(http.StatusOK, "Variables", VariableList{}),
		}},
		{http.MethodPost, "/projects/:id/variables", e.createProject, []forge.RouteOption{
			forge.WithName("projectVariables.create"),
			forge.WithSummary("Set a variable for a project, or for one of its services"),
			forge.WithRequestSchema(environments.CreateVariableInput{}),
			forge.WithResponseSchema(http.StatusCreated, "The variable", environments.Variable{}),
		}},
		{http.MethodGet, "/projects/:id/variables/resolved", e.resolve, []forge.RouteOption{
			forge.WithName("variables.resolve"),
			forge.WithSummary("What a project, or one of its services, gets in an environment, and which layer each value comes from"),
			forge.WithQuerySchema(ResolveQuery{}),
			forge.WithResponseSchema(http.StatusOK, "The resolution", environments.Resolution{}),
		}},
		{http.MethodPatch, "/variables/:id", e.update, []forge.RouteOption{
			forge.WithName("variables.update"),
			forge.WithSummary("Change a variable's value, or whether it's secret"),
			forge.WithRequestSchema(environments.UpdateVariableInput{}),
			forge.WithResponseSchema(http.StatusOK, "The variable", environments.Variable{}),
		}},
		{http.MethodDelete, "/variables/:id", e.delete, []forge.RouteOption{
			forge.WithName("variables.delete"),
			forge.WithSummary("Delete a variable, and its secret"),
			forge.WithNoContentResponse(),
			forge.WithEntity(forge.EntityDef{Type: "Variable", IDField: "id"}),
		}},
	}
	for _, rt := range routes {
		var err error
		switch rt.method {
		case http.MethodGet:
			err = g.GET(rt.path, rt.handler, rt.opts...)
		case http.MethodPost:
			err = g.POST(rt.path, rt.handler, rt.opts...)
		case http.MethodPatch:
			err = g.PATCH(rt.path, rt.handler, rt.opts...)
		case http.MethodDelete:
			err = g.DELETE(rt.path, rt.handler, rt.opts...)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func principal(c forge.Context) (security.Principal, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return p, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	return p, nil
}

func list(vars []*environments.Variable) VariableList {
	out := VariableList{Variables: make([]environments.Variable, 0, len(vars))}
	for _, v := range vars {
		out.Variables = append(out.Variables, *v)
	}
	return out
}

func (e *Extension) listOrganization(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	vars, err := e.svc.ListOrganization(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, list(vars))
}

func (e *Extension) createOrganization(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in environments.CreateVariableInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be a JSON variable")
	}
	v, err := e.svc.CreateOrganization(c.Request().Context(), p, in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, v)
}

func (e *Extension) listProject(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	vars, err := e.svc.ListProject(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, list(vars))
}

func (e *Extension) createProject(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in environments.CreateVariableInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be a JSON variable")
	}
	v, err := e.svc.CreateProject(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, v)
}

func (e *Extension) resolve(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	q := c.Request().URL.Query()
	if q.Get("environment") == "" {
		return forge.NewHTTPError(http.StatusBadRequest, "say which environment: ?environment=production")
	}
	res, err := e.svc.Resolve(c.Request().Context(), p, c.Param("id"), q.Get("environment"), q.Get("service"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, res)
}

func (e *Extension) update(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in environments.UpdateVariableInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON: {\"value\": \"...\"}")
	}
	v, err := e.svc.Update(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, v)
}

func (e *Extension) delete(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err := e.svc.Delete(c.Request().Context(), p, c.Param("id")); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}
