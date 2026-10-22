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

	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/dbgateway/plane"
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
	Snapshot(ctx context.Context, gw *dbgateway.Gateway, after uint64) (*plane.Snapshot, error)
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
