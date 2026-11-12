package fleets

import (
	"net/http"
	"strconv"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	domain "github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type fleetRequestError struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
}

// InstanceQuery filters a bounded page within the selected project.
type InstanceQuery struct {
	After       string `query:"after" json:"after,omitempty" optional:"true"`
	Limit       int    `query:"limit" json:"limit,omitempty" optional:"true"`
	Status      string `query:"status" json:"status,omitempty" optional:"true"`
	ExternalRef string `query:"external_ref" json:"external_ref,omitempty" optional:"true"`
}

func (e *Extension) routes(r forge.Router, guard identity.Guard) error {
	g := r.Group("/api/v1/projects/:project_id/fleet", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Fleets"))
	routes := []struct {
		path, name, summary string
		handler             forge.Handler
		response            any
	}{
		{"", "fleets.get", "Read the project's instance fleet", e.getFleet, fleet.Fleet{}},
		{"/instances", "fleetInstances.list", "List instances in this fleet", e.listInstances, fleet.InstancePage{}},
		{"/instances/:id", "fleetInstances.get", "Read an instance", e.getInstance, fleet.Instance{}},
		{"/operations/:id", "fleetOperations.get", "Read an instance operation", e.getOperation, fleet.Operation{}},
		{"/releases/:id", "fleetReleases.get", "Read an immutable fleet release", e.getRelease, fleet.TemplateRelease{}},
	}
	for _, route := range routes {
		opts := []forge.RouteOption{forge.WithName(route.name), forge.WithSummary(route.summary), forge.WithErrorResponses(), forge.WithResponseSchema(http.StatusUnprocessableEntity, "Invalid fleet request", fleetRequestError{}), forge.WithResponseSchema(http.StatusOK, route.summary, route.response)}
		if route.path == "/instances" {
			opts = append(opts, forge.WithQuerySchema(InstanceQuery{}))
		}
		if err := g.GET(route.path, route.handler, opts...); err != nil {
			return err
		}
	}
	if err := e.monitoringRoutes(g); err != nil {
		return err
	}
	if err := e.actionRoutes(g); err != nil {
		return err
	}
	return g.POST("/instances", e.createInstance, forge.WithName("fleetInstances.create"), forge.WithSummary("Request a new instance"), forge.WithRequestSchema(fleet.CreateInstanceInput{}), forge.WithResponseSchema(http.StatusAccepted, "Accepted instance request", fleet.OperationResult{}), forge.WithErrorResponses(), forge.WithResponseSchema(http.StatusConflict, "Fleet state conflict", fleetRequestError{}), forge.WithResponseSchema(http.StatusUnprocessableEntity, "Invalid fleet request", fleetRequestError{}), forge.WithResponseSchema(http.StatusTooManyRequests, "Fleet admission limit", fleetRequestError{}))
}

func (e *Extension) scope(c forge.Context) (security.Principal, *fleet.Fleet, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return p, nil, forge.NewHTTPError(http.StatusUnauthorized, "Sign in first")
	}
	f, err := e.svc.GetProjectFleet(c.Request().Context(), p, c.Param("project_id"))
	if err != nil {
		return p, nil, fail(err)
	}
	return p, f, nil
}
func (e *Extension) getFleet(c forge.Context) error {
	_, f, err := e.scope(c)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, f)
}
func (e *Extension) getInstance(c forge.Context) error {
	p, f, err := e.scope(c)
	if err != nil {
		return err
	}
	i, err := e.svc.GetFleetInstance(c.Request().Context(), p, f.ID, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	if i.FleetID != f.ID {
		return fail(domain.ErrNotFound)
	}
	return c.JSON(http.StatusOK, i)
}
func (e *Extension) listInstances(c forge.Context) error {
	p, f, err := e.scope(c)
	if err != nil {
		return err
	}
	q := c.Request().URL.Query()
	in := fleet.ListInstancesInput{After: q.Get("after"), Status: q.Get("status"), ExternalRef: q.Get("external_ref")}
	if q.Has("limit") {
		in.Limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil {
			return fail(domain.ErrInvalid)
		}
	}
	result, err := e.svc.ListInstances(c.Request().Context(), p, f.ID, in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, result)
}
func (e *Extension) getOperation(c forge.Context) error {
	p, f, err := e.scope(c)
	if err != nil {
		return err
	}
	op, err := e.svc.GetFleetOperation(c.Request().Context(), p, f.ID, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	i, err := e.svc.GetFleetInstance(c.Request().Context(), p, f.ID, op.InstanceID)
	if err != nil {
		return fail(err)
	}
	if i.FleetID != f.ID {
		return fail(domain.ErrNotFound)
	}
	return c.JSON(http.StatusOK, op)
}
func (e *Extension) getRelease(c forge.Context) error {
	p, f, err := e.scope(c)
	if err != nil {
		return err
	}
	result, err := e.svc.GetRelease(c.Request().Context(), p, f.ID, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, result)
}
