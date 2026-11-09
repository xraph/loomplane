package releaseprep

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releaseprep"
	"github.com/xraph/loomplane/internal/security"
)

// reportsAPI is what the routes ask of release preparation.
// *releaseprep.Service is the real one, and checks every permission on the
// report's project. Lookup, which reads with no principal, is not here:
// no route may reach it.
type reportsAPI interface {
	Prepare(ctx context.Context, p security.Principal, projectID string, in releaseprep.PrepareInput, via string) (*releaseprep.ReleaseReport, error)
	List(ctx context.Context, p security.Principal, projectID string, limit int) ([]*releaseprep.ReleaseReport, error)
	Get(ctx context.Context, p security.Principal, id string) (*releaseprep.ReleaseReport, error)
	Cancel(ctx context.Context, p security.Principal, id string) (*releaseprep.ReleaseReport, error)
}

var _ reportsAPI = (*releaseprep.Service)(nil)

// via is how this route asks for a release. It is the route's own, never
// read from a request: the dashboard and loomctl call the same route, and
// both are a person asking for themselves.
const via = releaseprep.ViaDashboard

// ReleaseReportList is a project's release reports, newest first.
type ReleaseReportList struct {
	Reports []*releaseprep.ReleaseReport `json:"reports"`
}

// ReleaseReportQuery narrows the release reports listed.
type ReleaseReportQuery struct {
	// Limit is 1 to 50, 20 when left out.
	Limit int `json:"limit,omitempty" query:"limit,omitempty"`
}

// mountRoutes serves release reports under /api/v1, behind guard and the
// scope binder, so they exist only while the agent is on in the caller's
// workspace.
func mountRoutes(r forge.Router, guard identity.Guard, binder *agentscope.Binder, api reportsAPI) error {
	if api == nil || binder == nil {
		return errors.New("the release report routes need release preparation and the scope binder")
	}
	h := handlers{api: api}
	mw := append(append([]forge.Middleware{}, guard...), binder.Middleware())
	g := r.Group("/api/v1", forge.WithGroupMiddleware(mw...), forge.WithGroupTags("Agent"))
	one := func(status int, desc string) forge.RouteOption {
		return forge.WithResponseSchema(status, desc, releaseprep.ReleaseReport{})
	}
	errs := []error{
		g.POST("/projects/:id/release-reports", h.prepare,
			forge.WithName("releaseReports.prepare"),
			forge.WithSummary("Ask the agent to prepare a production release"),
			forge.WithDescription("Needs agent:use and deployment:create on the project, as a person. Loomplane plans the promotion from staging, collects the evidence and the agent reviews it. "+
				"Only someone who may approve production gets a release promoted without a second person; anyone else's waits for an approver in Needs approval. "+
				"A plan waiting for a decision on production is a 409 naming it, unless you set replace."),
			forge.WithRequestSchema(releaseprep.PrepareInput{}),
			one(http.StatusAccepted, "The release report, while it's prepared"),
		),
		g.GET("/projects/:id/release-reports", h.list,
			forge.WithName("releaseReports.list"),
			forge.WithSummary("A project's release reports"),
			forge.WithDescription("Needs deployment:read on the project. Newest first."),
			forge.WithQuerySchema(ReleaseReportQuery{}),
			forge.WithResponseSchema(http.StatusOK, "The release reports, newest first", ReleaseReportList{}),
		),
		g.GET("/release-reports/:id", h.get,
			forge.WithName("releaseReports.get"),
			forge.WithSummary("One release report"),
			forge.WithDescription("Needs deployment:read on the report's project."),
			one(http.StatusOK, "The release report"),
		),
		g.POST("/release-reports/:id/cancel", h.cancel,
			forge.WithName("releaseReports.cancel"),
			forge.WithSummary("Stop a release preparation"),
			forge.WithDescription("The person who asked for it, while they may use the agent on the project, or someone who may approve its deployments. "+
				"What already happened stands: a report that has finished is a 409. The plan is left for a person to decide."),
			one(http.StatusOK, "The cancelled release report"),
		),
	}
	return errors.Join(errs...)
}

type handlers struct{ api reportsAPI }

// person is the caller, who must be a person acting for themselves: the
// agent, the agent acting for someone and Loomplane's automation never
// reach release reports over HTTP.
func person(c forge.Context) (security.Principal, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return p, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	if p.Kind != security.KindUser || p.OnBehalfOf != nil || p.UserID == "" || p.OrgID == "" {
		return p, forge.NewHTTPError(http.StatusForbidden, security.ErrForbidden.Error()+": release reports are for people")
	}
	if _, agent := security.IsAgentSubject(p.UserID); agent {
		return p, forge.NewHTTPError(http.StatusForbidden, security.ErrForbidden.Error()+": release reports are for people")
	}
	if _, system := security.IsSystemSubject(p.UserID); system {
		return p, forge.NewHTTPError(http.StatusForbidden, security.ErrForbidden.Error()+": release reports are for people")
	}
	return p, nil
}

func (h handlers) prepare(c forge.Context) error {
	p, err := person(c)
	if err != nil {
		return err
	}
	var in releaseprep.PrepareInput
	if c.Request().ContentLength != 0 {
		if berr := c.BindJSON(&in); berr != nil {
			return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"from": "staging", "to": "production", "scope": ["api"], "replace": false}`)
		}
	}
	r, err := h.api.Prepare(c.Request().Context(), p, c.Param("id"), in, via)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusAccepted, r)
}

func (h handlers) list(c forge.Context) error {
	p, err := person(c)
	if err != nil {
		return err
	}
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil {
			return forge.NewHTTPError(http.StatusUnprocessableEntity, "limit is a number from 1 to 50")
		}
	}
	reports, err := h.api.List(c.Request().Context(), p, c.Param("id"), limit)
	if err != nil {
		return fail(err)
	}
	if reports == nil {
		reports = []*releaseprep.ReleaseReport{}
	}
	return c.JSON(http.StatusOK, ReleaseReportList{Reports: reports})
}

func (h handlers) get(c forge.Context) error {
	p, err := person(c)
	if err != nil {
		return err
	}
	r, err := h.api.Get(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, r)
}

func (h handlers) cancel(c forge.Context) error {
	p, err := person(c)
	if err != nil {
		return err
	}
	r, err := h.api.Cancel(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, r)
}

// fail maps release preparation's errors to a status. A 409 for an open
// plan, a preparation under way or one already finished carries the
// error's whole message, which names the plan or the report and is
// Loomplane's own words. The agent's settings errors carry only their own
// sentence: a key error may wrap what a store said.
func fail(err error) error {
	switch {
	case errors.Is(err, releaseprep.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, releaseprep.ErrNotFound.Error())
	case errors.Is(err, planning.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, planning.ErrNotFound.Error())
	case errors.Is(err, projects.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, projects.ErrNotFound.Error())
	case errors.Is(err, releaseprep.ErrOpenPlan), errors.Is(err, releaseprep.ErrInProgress), errors.Is(err, releaseprep.ErrNotCancelable):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, agentsettings.ErrDisabled):
		return forge.NewHTTPError(http.StatusConflict, agentsettings.ErrDisabled.Error())
	case errors.Is(err, agentsettings.ErrBudgetExhausted):
		return forge.NewHTTPError(http.StatusConflict, agentsettings.ErrBudgetExhausted.Error())
	case errors.Is(err, agentsettings.ErrKeyUnavailable):
		return forge.NewHTTPError(http.StatusConflict, agentsettings.ErrKeyUnavailable.Error())
	case errors.Is(err, releaseprep.ErrNoStaging), errors.Is(err, releaseprep.ErrNoProduction),
		errors.Is(err, releaseprep.ErrAmbiguous), errors.Is(err, releaseprep.ErrInvalid):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, releaseprep.ErrNotYourTask):
		return forge.NewHTTPError(http.StatusForbidden, releaseprep.ErrNotYourTask.Error())
	}
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, err.Error())
	}
	return err
}
