package fleets

import (
	"context"
	"errors"
	"net/http"

	"github.com/xraph/forge"

	domain "github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type actionService interface {
	RequestAction(context.Context, security.Principal, string, fleet.ActionInput) (*fleet.Operation, error)
}
type activeOperationService interface {
	GetActiveOperation(context.Context, security.Principal, string) (*fleet.Operation, error)
}
type cancellationService interface {
	CancelOperation(context.Context, security.Principal, string, int64) (*fleet.Operation, error)
}

// FleetCancelOperationInput pins the instance revision reviewed by the caller.
type FleetCancelOperationInput struct {
	ExpectedInstanceRevision int64 `json:"expected_instance_revision"`
}

func (e *Extension) actionRoutes(g forge.Router) error {
	if err := g.GET("/instances/:id/active-operation", e.getActiveOperation, forge.WithName("fleetInstances.activeOperation"), forge.WithSummary("Read the instance's unresolved operation"), forge.WithResponseSchema(http.StatusOK, "Unresolved operation", fleet.Operation{}), forge.WithErrorResponses()); err != nil {
		return err
	}
	if err := g.POST("/instances/:id/operations", e.requestAction, forge.WithName("fleetInstances.requestAction"), forge.WithSummary("Submit an instance action for review"), forge.WithDescription("Acceptance records the request. It does not authorize or confirm runtime effects."), forge.WithRequestSchema(fleet.ActionInput{}), forge.WithResponseSchema(http.StatusAccepted, "Accepted action request", fleet.Operation{}), forge.WithErrorResponses(), forge.WithResponseSchema(http.StatusConflict, "Fleet state conflict", fleetRequestError{}), forge.WithResponseSchema(http.StatusUnprocessableEntity, "Action blocked or invalid", fleetRequestError{}), forge.WithResponseSchema(http.StatusTooManyRequests, "Fleet admission limit", fleetRequestError{})); err != nil {
		return err
	}
	return g.POST("/operations/:id/cancel", e.cancelOperation, forge.WithName("fleetOperations.cancel"), forge.WithSummary("Cancel a create request before execution admission"), forge.WithDescription("Only a create request that never held effect authority can be cancelled here. Admitted or ambiguous work requires its reviewed recovery workflow."), forge.WithRequestSchema(FleetCancelOperationInput{}), forge.WithResponseSchema(http.StatusOK, "Cancelled operation", fleet.Operation{}), forge.WithErrorResponses(), forge.WithResponseSchema(http.StatusConflict, "Operation admitted or instance revision changed", fleetRequestError{}), forge.WithResponseSchema(http.StatusUnprocessableEntity, "Invalid cancellation request", fleetRequestError{}))
}

func (e *Extension) actionInstance(c forge.Context) (security.Principal, *fleet.Instance, error) {
	p, f, err := e.scope(c)
	if err != nil {
		return p, nil, err
	}
	instance, err := e.svc.GetFleetInstance(c.Request().Context(), p, f.ID, c.Param("id"))
	if err != nil {
		return p, nil, fail(err)
	}
	if instance.FleetID != f.ID {
		return p, nil, fail(domain.ErrNotFound)
	}
	return p, instance, nil
}
func (e *Extension) requestAction(c forge.Context) error {
	p, instance, err := e.actionInstance(c)
	if err != nil {
		return err
	}
	var input fleet.ActionInput
	if decodeErr := decodeRequest(c, &input); decodeErr != nil {
		return decodeErr
	}
	if input.Validate() != nil {
		return fail(domain.ErrInvalid)
	}
	svc, ok := e.svc.(actionService)
	if !ok {
		return forge.NewHTTPError(http.StatusServiceUnavailable, "Fleet action requests are unavailable")
	}
	op, err := svc.RequestAction(c.Request().Context(), p, instance.ID, input)
	if err != nil {
		if errors.Is(err, domain.ErrApprovalRequired) {
			return forge.NewHTTPError(http.StatusUnprocessableEntity, "This action requires a qualified runtime and reviewed policy before it can proceed")
		}
		return fail(err)
	}
	return c.JSON(http.StatusAccepted, op)
}
func (e *Extension) getActiveOperation(c forge.Context) error {
	p, instance, err := e.actionInstance(c)
	if err != nil {
		return err
	}
	svc, ok := e.svc.(activeOperationService)
	if !ok {
		return forge.NewHTTPError(http.StatusServiceUnavailable, "Fleet operation reads are unavailable")
	}
	op, err := svc.GetActiveOperation(c.Request().Context(), p, instance.ID)
	if err != nil {
		return fail(err)
	}
	if op.InstanceID != instance.ID {
		return fail(domain.ErrNotFound)
	}
	return c.JSON(http.StatusOK, op)
}
func (e *Extension) cancelOperation(c forge.Context) error {
	p, f, err := e.scope(c)
	if err != nil {
		return err
	}
	op, err := e.svc.GetFleetOperation(c.Request().Context(), p, f.ID, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	instance, err := e.svc.GetFleetInstance(c.Request().Context(), p, f.ID, op.InstanceID)
	if err != nil {
		return fail(err)
	}
	if instance.FleetID != f.ID || instance.ID != op.InstanceID {
		return fail(domain.ErrNotFound)
	}
	var input FleetCancelOperationInput
	if decodeErr := decodeRequest(c, &input); decodeErr != nil {
		return decodeErr
	}
	if input.ExpectedInstanceRevision < 1 {
		return fail(domain.ErrInvalid)
	}
	svc, ok := e.svc.(cancellationService)
	if !ok {
		return forge.NewHTTPError(http.StatusServiceUnavailable, "Fleet cancellation is unavailable")
	}
	result, err := svc.CancelOperation(c.Request().Context(), p, op.ID, input.ExpectedInstanceRevision)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, result)
}
