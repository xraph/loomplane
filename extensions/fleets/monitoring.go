package fleets

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/xraph/forge"

	domain "github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type monitoringService interface {
	Events(context.Context, security.Principal, string, string, int) (*fleet.EventPage, error)
	Metrics(context.Context, security.Principal, string, fleet.MetricWindow) (*fleet.MetricSummary, error)
}

// EventQuery selects a bounded page from one instance's journal.
type EventQuery struct {
	After string `query:"after" json:"after,omitempty" optional:"true"`
	Limit int    `query:"limit" json:"limit,omitempty" optional:"true"`
}

// MetricQuery accepts an RFC3339 interval or defaults to the last 15 server minutes.
type MetricQuery struct {
	Start string `query:"start" json:"start,omitempty" optional:"true"`
	End   string `query:"end" json:"end,omitempty" optional:"true"`
}

func (e *Extension) monitoringRoutes(g forge.Router) error {
	for _, route := range []struct {
		path, name, summary string
		handler             forge.Handler
		query, response     any
	}{
		{"/instances/:id/events", "fleetEvents.list", "Read an instance event journal", e.listEvents, EventQuery{}, fleet.EventPage{}},
		{"/instances/:id/metrics", "fleetMetrics.get", "Read instance observations and coverage", e.getMetrics, MetricQuery{}, fleet.MetricSummary{}},
	} {
		if err := g.GET(route.path, route.handler, forge.WithName(route.name), forge.WithSummary(route.summary), forge.WithQuerySchema(route.query), forge.WithResponseSchema(http.StatusOK, route.summary, route.response), forge.WithErrorResponses(), forge.WithResponseSchema(http.StatusUnprocessableEntity, "Invalid monitoring query", fleetRequestError{}), forge.WithResponseSchema(http.StatusServiceUnavailable, "Monitoring reader unavailable", fleetRequestError{})); err != nil {
			return err
		}
	}
	return nil
}

func (e *Extension) monitoringScope(c forge.Context) (security.Principal, *fleet.Instance, monitoringService, error) {
	p, f, err := e.scope(c)
	if err != nil {
		return p, nil, nil, err
	}
	instance, err := e.svc.GetFleetInstance(c.Request().Context(), p, f.ID, c.Param("id"))
	if err != nil {
		return p, nil, nil, fail(err)
	}
	if instance.FleetID != f.ID {
		return p, nil, nil, fail(domain.ErrNotFound)
	}
	svc, ok := e.svc.(monitoringService)
	if !ok {
		return p, nil, nil, forge.NewHTTPError(http.StatusServiceUnavailable, "Fleet monitoring is unavailable")
	}
	return p, instance, svc, nil
}

func (e *Extension) listEvents(c forge.Context) error {
	p, instance, svc, err := e.monitoringScope(c)
	if err != nil {
		return err
	}
	q := c.Request().URL.Query()
	if len(q["after"]) > 1 || len(q["limit"]) > 1 || len(q.Get("after")) > 160 {
		return fail(domain.ErrInvalid)
	}
	limit := 0
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 0 || limit > 200 {
			return fail(domain.ErrInvalid)
		}
	}
	out, err := svc.Events(c.Request().Context(), p, instance.ID, q.Get("after"), limit)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) getMetrics(c forge.Context) error {
	p, instance, svc, err := e.monitoringScope(c)
	if err != nil {
		return err
	}
	q := c.Request().URL.Query()
	end := time.Now().UTC()
	start := end.Add(-15 * time.Minute)
	if q.Has("start") || q.Has("end") {
		if len(q["start"]) != 1 || len(q["end"]) != 1 {
			return fail(domain.ErrInvalid)
		}
		start, err = time.Parse(time.RFC3339Nano, q.Get("start"))
		if err != nil {
			return fail(domain.ErrInvalid)
		}
		end, err = time.Parse(time.RFC3339Nano, q.Get("end"))
		if err != nil || start.IsZero() || !end.After(start) || end.Sub(start) > 24*time.Hour {
			return fail(domain.ErrInvalid)
		}
	}
	out, err := svc.Metrics(c.Request().Context(), p, instance.ID, fleet.MetricWindow{Start: start, End: end})
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}
