package dbgateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/dbgateway/plane"
	"github.com/xraph/loomplane/internal/security"
)

const (
	// maxBody caps what a gateway may send. Reports are the largest, and a
	// route's stats are a few dozen bytes.
	maxBody = 1 << 20
	// pollWait is how long a snapshot poll waits for something new, and
	// pollEvery how often it looks.
	pollWait  = 25 * time.Second
	pollEvery = time.Second
)

// gateways is what the routes need from *dbgateway.Service.
type gateways interface {
	Configured() bool
	VerifyRequest(r *http.Request, body []byte, now time.Time) (*dbgateway.Gateway, error)
	Snapshot(ctx context.Context, gw *dbgateway.Gateway, after uint64) (*plane.SignedSnapshot, error)
	Report(ctx context.Context, gw *dbgateway.Gateway, r plane.Report) (plane.ReportResponse, error)
	Hold(ctx context.Context, gw *dbgateway.Gateway, l plane.Label) error
}

var _ gateways = (*dbgateway.Service)(nil)

// handlers serve the endpoints deployed gateways call. wait and every time
// the snapshot long-poll; now is the clock requests are checked against;
// warn logs a failure without the request's contents.
type handlers struct {
	svc         gateways
	wait, every time.Duration
	now         func() time.Time
	warn        func(msg string, err error)
}

// mount registers the gateway endpoints. They're outside the guard: a
// gateway has no session, and its request signature is what's checked.
func mount(r forge.Router, h *handlers) error {
	if err := r.GET("/api/v1/db-gateways/:id/snapshot", h.snapshot,
		forge.WithName("dbgateway.snapshot"), forge.WithSchemaExclude()); err != nil {
		return err
	}
	if err := r.POST("/api/v1/db-gateways/:id/report", h.report,
		forge.WithName("dbgateway.report"), forge.WithSchemaExclude()); err != nil {
		return err
	}
	return r.POST("/api/v1/db-gateways/:id/hold", h.hold,
		forge.WithName("dbgateway.hold"), forge.WithSchemaExclude())
}

// management is what the project routes need from *dbgateway.Service.
type management interface {
	ProjectGateways(ctx context.Context, p security.Principal, project string) ([]dbgateway.GatewayStatus, error)
	SetRouteState(ctx context.Context, p security.Principal, project, gatewayID, routeID, state string) (*dbgateway.RouteSummary, error)
}

var _ management = (*dbgateway.Service)(nil)

// GatewayList is a project's database gateways.
type GatewayList struct {
	Gateways []dbgateway.GatewayStatus `json:"gateways"`
}

// ForgeEnvelope tells the generated client the list wraps Gateways.
func (GatewayList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "gateways"}
}

// mountManagement registers the project routes a dashboard uses: listing
// the project's gateways, and pausing and resuming a route. They're behind
// the guard, and the service checks the caller's role.
func mountManagement(r forge.Router, guard identity.Guard, m management) error {
	group := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Database gateways"))
	if err := group.GET("/projects/:id/db-gateways", func(c forge.Context) error {
		p, err := principal(c)
		if err != nil {
			return err
		}
		list, err := m.ProjectGateways(c.Request().Context(), p, c.Param("id"))
		if err != nil {
			return fail(err)
		}
		return c.JSON(http.StatusOK, GatewayList{Gateways: list})
	}, forge.WithName("dbGateways.list"),
		forge.WithResponseSchema(http.StatusOK, "The project's database gateways, their instances and route metrics", GatewayList{})); err != nil {
		return err
	}
	if err := group.POST("/projects/:id/db-gateways/:gatewayId/routes/:routeId/pause", setState(m, "paused"),
		forge.WithName("dbGateways.pauseRoute"),
		forge.WithResponseSchema(http.StatusOK, "The paused route", dbgateway.RouteSummary{})); err != nil {
		return err
	}
	return group.POST("/projects/:id/db-gateways/:gatewayId/routes/:routeId/resume", setState(m, "active"),
		forge.WithName("dbGateways.resumeRoute"),
		forge.WithResponseSchema(http.StatusOK, "The resumed route", dbgateway.RouteSummary{}))
}

func setState(m management, state string) func(forge.Context) error {
	return func(c forge.Context) error {
		p, err := principal(c)
		if err != nil {
			return err
		}
		route, err := m.SetRouteState(c.Request().Context(), p, c.Param("id"), c.Param("gatewayId"), c.Param("routeId"), state)
		if err != nil {
			return fail(err)
		}
		return c.JSON(http.StatusOK, route)
	}
}

func principal(c forge.Context) (security.Principal, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return p, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	return p, nil
}

// fail maps a management error to its HTTP response.
func fail(err error) error {
	switch {
	case errors.Is(err, dbgateway.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, dbgateway.ErrInvalid):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, err.Error())
	}
	return err
}

type errorBody struct {
	Error string `json:"error"`
}

func reply(c forge.Context, status int, msg string) error {
	return c.JSON(status, errorBody{Error: msg})
}

// authenticate reads the body, at most 1 MiB, and returns the gateway that
// signed the request. When it returns a nil gateway the response is written.
func (h *handlers) authenticate(c forge.Context) (*dbgateway.Gateway, []byte, error) {
	if !h.svc.Configured() {
		return nil, nil, reply(c, http.StatusServiceUnavailable, dbgateway.ErrNotConfigured.Error())
	}
	req := c.Request()
	body, err := io.ReadAll(http.MaxBytesReader(c.Response(), req.Body, maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, nil, reply(c, http.StatusRequestEntityTooLarge, "the request body is over 1 MiB")
		}
		return nil, nil, reply(c, http.StatusBadRequest, "the request body couldn't be read")
	}
	gw, err := h.svc.VerifyRequest(req, body, h.now())
	switch {
	case errors.Is(err, dbgateway.ErrUnauthorized):
		return nil, nil, reply(c, http.StatusUnauthorized, dbgateway.ErrUnauthorized.Error())
	case err != nil:
		h.warn("verify a database gateway request", err)
		return nil, nil, reply(c, http.StatusInternalServerError, "the request couldn't be checked")
	case gw.ID != c.Param("id"):
		return nil, nil, reply(c, http.StatusUnauthorized, dbgateway.ErrUnauthorized.Error())
	}
	return gw, body, nil
}

// snapshot answers GET /api/v1/db-gateways/:id/snapshot?after=N with the
// gateway's signed snapshot once its version passes N, looking again every
// second for up to 25 seconds, and with 204 when nothing new came.
func (h *handlers) snapshot(c forge.Context) error {
	gw, _, err := h.authenticate(c)
	if gw == nil {
		return err
	}
	var after uint64
	if raw := c.Request().URL.Query().Get("after"); raw != "" {
		if after, err = strconv.ParseUint(raw, 10, 64); err != nil {
			return reply(c, http.StatusBadRequest, "after must be a snapshot version")
		}
	}
	ctx := c.Request().Context()
	deadline := time.Now().Add(h.wait)
	for {
		s, err := h.svc.Snapshot(ctx, gw, after)
		if err != nil {
			if ctx.Err() != nil {
				return nil // the gateway hung up
			}
			if errors.Is(err, dbgateway.ErrInvalid) {
				return reply(c, http.StatusBadRequest, "after must be a snapshot version")
			}
			h.warn("compile a database gateway snapshot", err)
			return reply(c, http.StatusInternalServerError, "the snapshot couldn't be compiled")
		}
		if s != nil {
			raw, err := json.Marshal(s)
			if err != nil {
				h.warn("encode a database gateway snapshot", err)
				return reply(c, http.StatusInternalServerError, "the snapshot couldn't be encoded")
			}
			c.Response().Header().Set("Content-Type", "application/json")
			return c.Bytes(http.StatusOK, raw)
		}
		left := time.Until(deadline)
		if left <= 0 {
			return c.NoContent(http.StatusNoContent)
		}
		t := time.NewTimer(min(h.every, left))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}

// report answers POST /api/v1/db-gateways/:id/report with how many of the
// gateway's instances are live.
func (h *handlers) report(c forge.Context) error {
	gw, body, err := h.authenticate(c)
	if gw == nil {
		return err
	}
	var r plane.Report
	if err = json.Unmarshal(body, &r); err != nil {
		return reply(c, http.StatusBadRequest, "the body isn't a gateway report")
	}
	resp, err := h.svc.Report(c.Request().Context(), gw, r)
	switch {
	case errors.Is(err, dbgateway.ErrInvalid):
		return reply(c, http.StatusBadRequest, "the report is incomplete")
	case err != nil:
		h.warn("store a database gateway report", err)
		return reply(c, http.StatusInternalServerError, "the report couldn't be stored")
	}
	return c.JSON(http.StatusOK, resp)
}

// hold answers POST /api/v1/db-gateways/:id/hold. Phase 1 has no hold
// handler, so a well-formed hold gets 404.
func (h *handlers) hold(c forge.Context) error {
	gw, body, err := h.authenticate(c)
	if gw == nil {
		return err
	}
	var in struct {
		Label string `json:"label"`
	}
	if err = json.Unmarshal(body, &in); err != nil {
		return reply(c, http.StatusBadRequest, "the body isn't a hold request")
	}
	label, err := plane.ParseLabel(in.Label)
	if err != nil {
		return reply(c, http.StatusBadRequest, "the label isn't a route label")
	}
	err = h.svc.Hold(c.Request().Context(), gw, label)
	switch {
	case errors.Is(err, dbgateway.ErrNoHoldHandler):
		return reply(c, http.StatusNotFound, dbgateway.ErrNoHoldHandler.Error())
	case err != nil:
		h.warn("hold a database gateway route", err)
		return reply(c, http.StatusInternalServerError, "the hold couldn't be placed")
	}
	return c.NoContent(http.StatusAccepted)
}
