package aiagent

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/agentrequests"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// requestsAPI is what the request routes ask of the agent's requests.
// *agentrequests.Service is the real one.
type requestsAPI interface {
	List(ctx context.Context, p security.Principal, f agentrequests.ListFilter) (*agentrequests.RequestList, error)
	Get(ctx context.Context, p security.Principal, id string) (*agentrequests.Request, error)
	Answer(ctx context.Context, p security.Principal, id string, in agentrequests.AnswerInput) (*agentrequests.Request, error)
	ProvideSecret(ctx context.Context, p security.Principal, id string, in agentrequests.SecretInput) (*agentrequests.Request, error)
	Complete(ctx context.Context, p security.Principal, id string) (*agentrequests.Request, error)
	Decline(ctx context.Context, p security.Principal, id string, in agentrequests.DeclineInput) (*agentrequests.Request, error)
}

var _ requestsAPI = (*agentrequests.Service)(nil)

// RequestQuery narrows the requests listed.
type RequestQuery struct {
	// Status is pending (the default) or all.
	Status    string `json:"status,omitempty" query:"status,omitempty"`
	ProjectID string `json:"project_id,omitempty" query:"project_id,omitempty"`
	// For is "me" for the requests asked of you, routed to a team you're
	// in, or routed to the workspace's admins when you're one.
	For   string `json:"for,omitempty" query:"for,omitempty"`
	Limit int    `json:"limit,omitempty" query:"limit,omitempty"`
}

// mountRequestRoutes serves the agent's requests under /api/v1, behind
// guard and the scope binder, so they exist only while the agent is on in
// the caller's workspace.
//
// No body is logged: Loomplane's routes have no body logging, and Forge's
// logging middleware, which isn't installed, logs only the method, the
// path and the error. The secret route's handler never puts the value in
// an error.
func mountRequestRoutes(r forge.Router, guard identity.Guard, binder *agentscope.Binder, api requestsAPI) error {
	if api == nil || binder == nil {
		return errors.New("the agent's request routes need the requests service and the scope binder")
	}
	h := requestHandlers{api: api}
	mw := append(append([]forge.Middleware{}, guard...), binder.Middleware())
	g := r.Group("/api/v1", forge.WithGroupMiddleware(mw...), forge.WithGroupTags("Agent"))
	one := func(desc string) forge.RouteOption {
		return forge.WithResponseSchema(http.StatusOK, desc, agentrequests.Request{})
	}
	errs := []error{
		g.GET("/agent/requests", h.list,
			forge.WithName("agentRequests.list"),
			forge.WithSummary("What the agent is asking of people in your workspace"),
			forge.WithQuerySchema(RequestQuery{}),
			forge.WithResponseSchema(http.StatusOK, "The requests, newest first", agentrequests.RequestList{}),
		),
		g.GET("/agent/requests/:id", h.get,
			forge.WithName("agentRequests.get"),
			forge.WithSummary("One request the agent made"),
			one("The request"),
		),
		g.POST("/agent/requests/:id/answer", h.answer,
			forge.WithName("agentRequests.answer"),
			forge.WithSummary("Answer the agent's question"),
			forge.WithDescription("The person asked, a member of the team it was sent to, or someone who manages the workspace's AI may answer. The first answer counts."),
			forge.WithRequestSchema(agentrequests.AnswerInput{}),
			one("The answered request"),
		),
		g.POST("/agent/requests/:id/secret", h.provideSecret,
			forge.WithName("agentRequests.provideSecret"),
			forge.WithSummary("Set the secret the agent asked for"),
			forge.WithDescription("Needs variable:manage on the request's project. The value is stored as a secret variable, as you; the agent only hears that it's set."),
			forge.WithRequestSchema(agentrequests.SecretInput{}),
			forge.WithSensitiveFieldCleaning(),
			one("The request"),
		),
		g.POST("/agent/requests/:id/complete", h.complete,
			forge.WithName("agentRequests.complete"),
			forge.WithSummary("Say the GitHub or cloud connection the agent asked for is made"),
			forge.WithDescription("Loomplane checks the connection exists before the agent goes on."),
			one("The request"),
		),
		g.POST("/agent/requests/:id/decline", h.decline,
			forge.WithName("agentRequests.decline"),
			forge.WithSummary("Decline the agent's request"),
			forge.WithDescription("Declining a plan approval rejects the plan."),
			forge.WithRequestSchema(agentrequests.DeclineInput{}),
			one("The declined request"),
		),
	}
	return errors.Join(errs...)
}

type requestHandlers struct{ api requestsAPI }

func (h requestHandlers) list(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	f := agentrequests.ListFilter{Status: c.Query("status"), ProjectID: c.Query("project_id"), For: c.Query("for")}
	if raw := c.Query("limit"); raw != "" {
		if f.Limit, err = strconv.Atoi(raw); err != nil {
			return forge.NewHTTPError(http.StatusUnprocessableEntity, "limit is a number from 1 to 100")
		}
	}
	list, err := h.api.List(c.Request().Context(), p, f)
	if err != nil {
		return requestFail(err)
	}
	return c.JSON(http.StatusOK, list)
}

func (h requestHandlers) get(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	r, err := h.api.Get(c.Request().Context(), p, c.Param("id"))
	return respond(c, r, err)
}

func (h requestHandlers) answer(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in agentrequests.AnswerInput
	if berr := c.BindJSON(&in); berr != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"text": "your answer"}`)
	}
	r, err := h.api.Answer(c.Request().Context(), p, c.Param("id"), in)
	return respond(c, r, err)
}

func (h requestHandlers) provideSecret(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in agentrequests.SecretInput
	if berr := c.BindJSON(&in); berr != nil {
		// The decoder's error could quote the body.
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"value": "the secret"}`)
	}
	r, err := h.api.ProvideSecret(c.Request().Context(), p, c.Param("id"), in)
	in.Value = ""
	return respond(c, r, err)
}

func (h requestHandlers) complete(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	r, err := h.api.Complete(c.Request().Context(), p, c.Param("id"))
	return respond(c, r, err)
}

func (h requestHandlers) decline(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in agentrequests.DeclineInput
	if c.Request().ContentLength != 0 {
		if berr := c.BindJSON(&in); berr != nil {
			return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"reason": "why not"}`)
		}
	}
	r, err := h.api.Decline(c.Request().Context(), p, c.Param("id"), in)
	return respond(c, r, err)
}

func respond(c forge.Context, r *agentrequests.Request, err error) error {
	if err != nil {
		return requestFail(err)
	}
	return c.JSON(http.StatusOK, r)
}

// requestFail maps the requests' errors to a status: not found 404, a
// request already settled or not done yet 409, an invalid answer 422,
// then the agent's and the permission errors.
func requestFail(err error) error {
	switch {
	case errors.Is(err, agentrequests.ErrNotFound), errors.Is(err, planning.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, agentrequests.ErrNotFound.Error())
	case errors.Is(err, agentrequests.ErrNotPending), errors.Is(err, agentrequests.ErrNotDone):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, agentrequests.ErrInvalid), errors.Is(err, planning.ErrInvalid):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}
	return fail(err)
}
