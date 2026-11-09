package agentwatch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	watch "github.com/xraph/loomplane/internal/agentwatch"
	"github.com/xraph/loomplane/internal/security"
)

// reportsAPI is what the report routes ask of the agent's reports.
// *agentreports.Service is the real one, and checks every permission.
type reportsAPI interface {
	List(ctx context.Context, p security.Principal, f agentreports.ListFilter) (*agentreports.ReportList, error)
	Get(ctx context.Context, p security.Principal, id string) (*agentreports.Report, error)
	Close(ctx context.Context, p security.Principal, id string, in agentreports.CloseInput) (*agentreports.Report, error)
}

// watcherAPI is what the watcher route asks of the watcher.
// *watch.Watcher is the real one.
type watcherAPI interface {
	Status(ctx context.Context, p security.Principal) (*watch.Status, error)
}

var (
	_ reportsAPI = (*agentreports.Service)(nil)
	_ watcherAPI = (*watch.Watcher)(nil)
)

// ReportQuery narrows the reports listed.
type ReportQuery struct {
	// Status is open (the default), resolved, closed or all.
	Status    string `json:"status,omitempty" query:"status,omitempty"`
	ProjectID string `json:"project_id,omitempty" query:"project_id,omitempty"`
	// Limit is 1 to 100, 50 when left out.
	Limit int `json:"limit,omitempty" query:"limit,omitempty"`
}

// mountRoutes serves the agent's reports and its watcher under /api/v1,
// behind guard and the scope binder, so they exist only while the agent
// is on in the caller's workspace.
func mountRoutes(r forge.Router, guard identity.Guard, binder *agentscope.Binder, reports reportsAPI, watcher watcherAPI) error {
	if reports == nil || watcher == nil || binder == nil {
		return errors.New("the report routes need the reports, the watcher and the scope binder")
	}
	h := handlers{reports: reports, watcher: watcher}
	mw := append(append([]forge.Middleware{}, guard...), binder.Middleware())
	g := r.Group("/api/v1", forge.WithGroupMiddleware(mw...), forge.WithGroupTags("Agent"))
	errs := []error{
		g.GET("/agent/reports", h.list,
			forge.WithName("agentReports.list"),
			forge.WithSummary("What the agent saw go wrong in your workspace"),
			forge.WithDescription("Only the reports of projects whose deployments you may read."),
			forge.WithQuerySchema(ReportQuery{}),
			forge.WithResponseSchema(http.StatusOK, "The reports, newest first", agentreports.ReportList{}),
		),
		g.GET("/agent/reports/:id", h.get,
			forge.WithName("agentReports.get"),
			forge.WithSummary("One report the agent opened"),
			forge.WithResponseSchema(http.StatusOK, "The report", agentreports.Report{}),
		),
		g.POST("/agent/reports/:id/close", h.close,
			forge.WithName("agentReports.close"),
			forge.WithSummary("Close a report"),
			forge.WithDescription("Needs deployment:create on the report's project. If the report has a GitHub issue, Loomplane comments there and closes it."),
			forge.WithRequestSchema(agentreports.CloseInput{}),
			forge.WithResponseSchema(http.StatusOK, "The closed report", agentreports.Report{}),
		),
		g.GET("/agent/watcher", h.status,
			forge.WithName("agentWatcher.get"),
			forge.WithSummary("Whether the agent is watching your workspace, and where its reports go on GitHub"),
			forge.WithResponseSchema(http.StatusOK, "The watcher", watch.Status{}),
		),
	}
	return errors.Join(errs...)
}

type handlers struct {
	reports reportsAPI
	watcher watcherAPI
}

func principal(c forge.Context) (security.Principal, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return p, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	return p, nil
}

func (h handlers) list(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	f := agentreports.ListFilter{Status: c.Query("status"), ProjectID: c.Query("project_id")}
	if raw := c.Query("limit"); raw != "" {
		if f.Limit, err = strconv.Atoi(raw); err != nil {
			return forge.NewHTTPError(http.StatusUnprocessableEntity, "limit is a number from 1 to 100")
		}
	}
	list, err := h.reports.List(c.Request().Context(), p, f)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, list)
}

func (h handlers) get(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	r, err := h.reports.Get(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, r)
}

func (h handlers) close(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	// The body is optional: an empty one, however it's framed, closes
	// without a reason.
	var in agentreports.CloseInput
	if body := c.Request().Body; body != nil {
		dec := json.NewDecoder(body)
		derr := dec.Decode(&in)
		if derr == nil && dec.More() {
			derr = errors.New("data after the JSON object")
		}
		if derr != nil && !errors.Is(derr, io.EOF) {
			return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"reason": "why it's closed"}`)
		}
	}
	r, err := h.reports.Close(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, r)
}

func (h handlers) status(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	st, err := h.watcher.Status(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, st)
}

// fail maps the reports' errors to a status: not found 404, invalid 422,
// a report already closed 409, then the agent's and the permission errors.
func fail(err error) error {
	switch {
	case errors.Is(err, agentreports.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, agentreports.ErrNotFound.Error())
	case errors.Is(err, agentreports.ErrInvalid):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, agentreports.ErrNotOpen),
		errors.Is(err, agentsettings.ErrDisabled), errors.Is(err, agentsettings.ErrBudgetExhausted):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	}
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, err.Error())
	}
	return err
}
