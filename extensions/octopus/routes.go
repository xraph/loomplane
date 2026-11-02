package octopus

import (
	"context"
	"errors"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/octopusloom"
	"github.com/xraph/loomplane/internal/security"
)

// service is what the routes need from *octopusloom.Service.
type service interface {
	Board(ctx context.Context, p security.Principal, ref octopusloom.GatewayRef) (*octopusloom.Board, error)
	Status(ctx context.Context, p security.Principal, ref octopusloom.GatewayRef) (*octopusloom.OctopusStatus, error)
	Discovery(ctx context.Context, p security.Principal, ref octopusloom.GatewayRef) (*octopusloom.DiscoverySummary, error)
	CreateRoute(ctx context.Context, p security.Principal, ref octopusloom.GatewayRef, in octopusloom.RouteInput) (*octopusloom.RouteResult, error)
	UpdateRoute(ctx context.Context, p security.Principal, ref octopusloom.GatewayRef, id string, in octopusloom.RouteInput) (*octopusloom.RouteResult, error)
	RemoveRoute(ctx context.Context, p security.Principal, ref octopusloom.GatewayRef, id string, revision int64) (*octopusloom.SyncResult, error)
	SetFarpHostnames(ctx context.Context, p security.Principal, ref octopusloom.GatewayRef, hostnames []string, revision int64) (*octopusloom.OctopusStatus, error)
	Sync(ctx context.Context, p security.Principal, ref octopusloom.GatewayRef) (*octopusloom.SyncResult, error)
}

var _ service = (*octopusloom.Service)(nil)

// OctopusRouteInput is the body of octopus.createRoute and
// octopus.updateRoute. It has a name of its own so that its schema doesn't
// clash with the Domains routes' RouteInput.
type OctopusRouteInput octopusloom.RouteInput //nolint:revive // The schema is named after the type; RouteInput is taken.

// RouteRevisionInput is the body of octopus.removeRoute: the revision of
// the route read.
type RouteRevisionInput struct {
	Revision int64 `json:"revision"`
}

// FarpHostnamesInput is the body of octopus.setFarpHostnames. No hostnames
// is any host.
type FarpHostnamesInput struct {
	Hostnames []string `json:"hostnames"`
	Revision  int64    `json:"revision"`
}

// mount registers the Octopus routes behind the guard. The service checks
// the caller's permission on the project.
func mount(r forge.Router, guard identity.Guard, svc service) error {
	const base = "/projects/:projectId/environments/:environmentId/octopus/:resource"
	g := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Octopus"))
	h := handlers{svc: svc}
	for _, route := range []struct {
		method, path string
		handler      forge.Handler
		opts         []forge.RouteOption
	}{
		{http.MethodGet, base + "/board", h.board, []forge.RouteOption{
			forge.WithName("octopus.board"),
			forge.WithSummary("An Octopus gateway's routes by domain: the ones added here, the ones FARP found and the ones made in the Octopus admin"),
			forge.WithResponseSchema(http.StatusOK, "The board, with live false and a reason when Octopus doesn't answer", octopusloom.Board{}),
		}},
		{http.MethodGet, base + "/status", h.status, []forge.RouteOption{
			forge.WithName("octopus.status"),
			forge.WithSummary("An Octopus gateway's state, its last sync and what attaching a domain to it takes"),
			forge.WithResponseSchema(http.StatusOK, "The gateway's status", octopusloom.OctopusStatus{}),
		}},
		{http.MethodGet, base + "/discovery", h.discovery, []forge.RouteOption{
			forge.WithName("octopus.discovery"),
			forge.WithSummary("The services an Octopus gateway's FARP discovery found or is waiting for"),
			forge.WithResponseSchema(http.StatusOK, "What FARP found", octopusloom.DiscoverySummary{}),
		}},
		{http.MethodPost, base + "/routes", h.createRoute, []forge.RouteOption{
			forge.WithName("octopus.createRoute"),
			forge.WithSummary("Add a route to an Octopus gateway and push it, without a redeploy"),
			forge.WithRequestSchema(OctopusRouteInput{}),
			forge.WithResponseSchema(http.StatusCreated, "The saved route and how pushing it went", octopusloom.RouteResult{}),
		}},
		{http.MethodPost, base + "/routes/:routeId/update", h.updateRoute, []forge.RouteOption{
			forge.WithName("octopus.updateRoute"),
			forge.WithSummary("Change a route at the revision read, and push it"),
			forge.WithRequestSchema(OctopusRouteInput{}),
			forge.WithResponseSchema(http.StatusOK, "The saved route and how pushing it went", octopusloom.RouteResult{}),
		}},
		{http.MethodPost, base + "/routes/:routeId/remove", h.removeRoute, []forge.RouteOption{
			forge.WithName("octopus.removeRoute"),
			forge.WithSummary("Remove a route at the revision read; a FARP route it hid comes back"),
			forge.WithRequestSchema(RouteRevisionInput{}),
			forge.WithResponseSchema(http.StatusOK, "How pushing the change went", octopusloom.SyncResult{}),
		}},
		{http.MethodPost, base + "/farp-hostnames", h.setFarpHostnames, []forge.RouteOption{
			forge.WithName("octopus.setFarpHostnames"),
			forge.WithSummary("Set the attached domains FARP routes answer on, at the gateway's revision; none is any host"),
			forge.WithRequestSchema(FarpHostnamesInput{}),
			forge.WithResponseSchema(http.StatusOK, "The gateway's status", octopusloom.OctopusStatus{}),
		}},
		{http.MethodPost, base + "/sync", h.sync, []forge.RouteOption{
			forge.WithName("octopus.sync"),
			forge.WithSummary("Push an Octopus gateway's routes now, and register its services with FARP again"),
			forge.WithResponseSchema(http.StatusOK, "How the push went", octopusloom.SyncResult{}),
		}},
	} {
		var err error
		if route.method == http.MethodGet {
			err = g.GET(route.path, route.handler, route.opts...)
		} else {
			err = g.POST(route.path, route.handler, route.opts...)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

type handlers struct{ svc service }

// requestOf reads the principal and the gateway from the request.
func requestOf(c forge.Context) (security.Principal, octopusloom.GatewayRef, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return p, octopusloom.GatewayRef{}, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	return p, octopusloom.GatewayRef{
		OrgID: p.OrgID, ProjectID: c.Param("projectId"), EnvironmentID: c.Param("environmentId"), Resource: c.Param("resource"),
	}, nil
}

// fail maps the service's errors to responses. An ErrInvalid or
// ErrConflict says what to change in words for the person, so it is
// answered as it is. Anything else is a 500, whose text the router hides.
func fail(err error) error {
	switch {
	case errors.Is(err, octopusloom.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, octopusloom.ErrConflict), errors.Is(err, octopusloom.ErrStale):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, octopusloom.ErrInvalid):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, err.Error())
	}
	return err
}

func (h handlers) board(c forge.Context) error {
	p, ref, err := requestOf(c)
	if err != nil {
		return err
	}
	b, err := h.svc.Board(c.Request().Context(), p, ref)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, b)
}

func (h handlers) status(c forge.Context) error {
	p, ref, err := requestOf(c)
	if err != nil {
		return err
	}
	st, err := h.svc.Status(c.Request().Context(), p, ref)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, st)
}

func (h handlers) discovery(c forge.Context) error {
	p, ref, err := requestOf(c)
	if err != nil {
		return err
	}
	d, err := h.svc.Discovery(c.Request().Context(), p, ref)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, d)
}

const routeBody = `the body must be JSON: {"hostname": "api.acme.dev", "path": "/auth/*", "service": "auth", "port": 9000}`

func (h handlers) createRoute(c forge.Context) error {
	p, ref, err := requestOf(c)
	if err != nil {
		return err
	}
	var in OctopusRouteInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, routeBody)
	}
	res, err := h.svc.CreateRoute(c.Request().Context(), p, ref, octopusloom.RouteInput(in))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, res)
}

func (h handlers) updateRoute(c forge.Context) error {
	p, ref, err := requestOf(c)
	if err != nil {
		return err
	}
	var in OctopusRouteInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, routeBody)
	}
	res, err := h.svc.UpdateRoute(c.Request().Context(), p, ref, c.Param("routeId"), octopusloom.RouteInput(in))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, res)
}

func (h handlers) removeRoute(c forge.Context) error {
	p, ref, err := requestOf(c)
	if err != nil {
		return err
	}
	var in RouteRevisionInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"revision": 3}`)
	}
	res, err := h.svc.RemoveRoute(c.Request().Context(), p, ref, c.Param("routeId"), in.Revision)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, res)
}

func (h handlers) setFarpHostnames(c forge.Context) error {
	p, ref, err := requestOf(c)
	if err != nil {
		return err
	}
	var in FarpHostnamesInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"hostnames": ["api.acme.dev"], "revision": 4}`)
	}
	st, err := h.svc.SetFarpHostnames(c.Request().Context(), p, ref, in.Hostnames, in.Revision)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, st)
}

func (h handlers) sync(c forge.Context) error {
	p, ref, err := requestOf(c)
	if err != nil {
		return err
	}
	res, err := h.svc.Sync(c.Request().Context(), p, ref)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, res)
}
