package aiagent

import (
	"context"
	"errors"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// statusAPI is what the status route asks of the agent's settings.
// *agentsettings.Service is the real one.
type statusAPI interface {
	Status(ctx context.Context, p security.Principal) (*agentsettings.Status, error)
}

// autonomyAPI is what the autonomy route asks of the projects service.
// *projects.Service is the real one.
type autonomyAPI interface {
	SetAutonomy(ctx context.Context, p security.Principal, projectID string, in projects.AutonomyInput) (*projects.Project, error)
}

var (
	_ statusAPI   = (*agentsettings.Service)(nil)
	_ autonomyAPI = (*projects.Service)(nil)
)

// autonomyDescription says what the autonomy switch covers.
const autonomyDescription = "Only a person with deployment:approve on the project can change it; the agent never can. " +
	"It covers promoting a release someone asked for, after its staging soak, and never deleting or replacing " +
	"production data or resources, which always waits for a person."

// mountRoutes serves the agent's status and a project's autonomy under
// /api/v1 behind guard. Neither sits behind agentscope.Binder's
// middleware, which needs the agent on: the status says whether it is,
// and an admin sets a project's autonomy before turning the agent on.
func mountRoutes(r forge.Router, guard identity.Guard, status statusAPI, autonomy autonomyAPI) error {
	h := handlers{status: status, autonomy: autonomy}
	g := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Agent"))
	errs := []error{
		g.GET("/agent/status", h.getStatus,
			forge.WithName("agentStatus.get"),
			forge.WithSummary("Whether the AI agent is on, on this server and in the workspace"),
			forge.WithResponseSchema(http.StatusOK, "The agent's status", agentsettings.Status{}),
		),
		g.PUT("/projects/:id/autonomy", h.setAutonomy,
			forge.WithName("projectAutonomy.update"),
			forge.WithSummary("Let the agent promote a release to production without asking first, after a minimum staging soak"),
			forge.WithDescription(autonomyDescription),
			forge.WithRequestSchema(projects.AutonomyInput{}),
			forge.WithResponseSchema(http.StatusOK, "The project", projects.Project{}),
		),
	}
	return errors.Join(errs...)
}

type handlers struct {
	status   statusAPI
	autonomy autonomyAPI
}

func principal(c forge.Context) (security.Principal, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return p, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	return p, nil
}

func (h handlers) getStatus(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	st, err := h.status.Status(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, st)
}

func (h handlers) setAutonomy(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in projects.AutonomyInput
	if berr := c.BindJSON(&in); berr != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"autonomous_promotion": true, "min_staging_soak_seconds": 1800}`)
	}
	proj, err := h.autonomy.SetAutonomy(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, proj)
}

// fail maps the agent's errors to a status, as planning's routes do:
// not found 404, invalid 422, forbidden 403, the agent off or out of
// budget 409.
func fail(err error) error {
	switch {
	case errors.Is(err, projects.ErrNotFound), errors.Is(err, agentsettings.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, projects.ErrInvalid), errors.Is(err, agentsettings.ErrInvalid):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, agentsettings.ErrDisabled), errors.Is(err, agentsettings.ErrBudgetExhausted):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	}
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, err.Error())
	}
	return err
}
