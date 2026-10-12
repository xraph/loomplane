package projects

import (
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// ProjectList is the body of GET /api/v1/projects.
//
// The lists hold values, not pointers: the generated client types a slice
// of pointers as nullable items, which neither list ever has.
type ProjectList struct {
	Projects []projects.Project `json:"projects"`
}

// EnvironmentList is the body of GET /api/v1/projects/:id/environments.
type EnvironmentList struct {
	Environments []projects.Environment `json:"environments"`
}

// ForgeEnvelope tells the generated client the list wraps Projects.
func (ProjectList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "projects"}
}

// ForgeEnvelope tells the generated client the list wraps Environments.
func (EnvironmentList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "environments"}
}

func (e *Extension) routes(r forge.Router, guard identity.Guard) error {
	g := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Projects"))
	type route struct {
		method, path string
		handler      forge.Handler
		opts         []forge.RouteOption
	}
	routes := []route{
		{http.MethodGet, "/projects", e.list, []forge.RouteOption{
			forge.WithName("projects.list"), forge.WithSummary("The organization's projects, newest first"),
			forge.WithResponseSchema(http.StatusOK, "Projects", ProjectList{}),
		}},
		{http.MethodPost, "/projects", e.create, []forge.RouteOption{
			forge.WithName("projects.create"), forge.WithSummary("Create a project and start initializing it"),
			forge.WithRequestSchema(projects.CreateProjectInput{}),
			forge.WithResponseSchema(http.StatusCreated, "The new project", projects.Project{}),
		}},
		{http.MethodGet, "/projects/:id", e.get, []forge.RouteOption{
			forge.WithName("projects.get"), forge.WithSummary("One project"),
			forge.WithResponseSchema(http.StatusOK, "The project", projects.Project{}),
		}},
		{http.MethodPatch, "/projects/:id", e.update, []forge.RouteOption{
			forge.WithName("projects.update"), forge.WithSummary("Change a project's name, description or repository"),
			forge.WithRequestSchema(projects.UpdateProjectInput{}),
			forge.WithResponseSchema(http.StatusOK, "The project", projects.Project{}),
		}},
		{http.MethodDelete, "/projects/:id", e.remove, []forge.RouteOption{
			forge.WithName("projects.delete"), forge.WithSummary("Delete a project and its environments"),
			forge.WithNoContentResponse(),
			// A 204 carries no entity for the client to infer. The
			// environments go too, but nothing lists them outside the
			// project's own page, and invalidating them made that page,
			// still open, refetch a project that no longer exists (404).
			forge.WithEntity(forge.EntityDef{Type: "Project", IDField: "id"}),
		}},
		{http.MethodGet, "/projects/:id/environments", e.environments, []forge.RouteOption{
			forge.WithName("environments.list"), forge.WithSummary("A project's environments"),
			forge.WithResponseSchema(http.StatusOK, "Environments", EnvironmentList{}),
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
		return security.Principal{}, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
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
	return c.JSON(http.StatusOK, ProjectList{Projects: values(list)})
}

func (e *Extension) create(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in projects.CreateProjectInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be a JSON project")
	}
	proj, err := e.svc.Create(c.Request().Context(), p, in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, proj)
}

func (e *Extension) get(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	proj, err := e.svc.Get(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, proj)
}

func (e *Extension) update(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in projects.UpdateProjectInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be a JSON project update")
	}
	proj, err := e.svc.Update(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, proj)
}

func (e *Extension) remove(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err := e.svc.Delete(c.Request().Context(), p, c.Param("id")); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (e *Extension) environments(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	envs, err := e.svc.Environments(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, EnvironmentList{Environments: values(envs)})
}

// values copies a list of records out of their pointers, never returning nil
// so an empty list encodes as [] rather than null.
func values[T any](in []*T) []T {
	out := make([]T, 0, len(in))
	for _, v := range in {
		if v != nil {
			out = append(out, *v)
		}
	}
	return out
}
