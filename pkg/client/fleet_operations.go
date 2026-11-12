package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/xraph/loomplane/pkg/fleet"
)

// GetActiveFleetOperation reads the unresolved mutation, including older generations needing recovery.
func (c *Client) GetActiveFleetOperation(ctx context.Context, projectID, instanceID string) (*fleet.Operation, error) {
	var out fleet.Operation
	err := c.do(ctx, http.MethodGet, fleetPath(projectID)+"/instances/"+url.PathEscape(instanceID)+"/active-operation", nil, &out)
	return &out, err
}

// RequestFleetInstanceAction records an intent for review. Acceptance does not confirm runtime effects.
func (c *Client) RequestFleetInstanceAction(ctx context.Context, projectID, instanceID string, in fleet.ActionInput) (*fleet.Operation, error) {
	var out fleet.Operation
	err := c.do(ctx, http.MethodPost, fleetPath(projectID)+"/instances/"+url.PathEscape(instanceID)+"/operations", in, &out)
	return &out, err
}

// CancelFleetOperation requests cancellation only before a create has held effect authority.
func (c *Client) CancelFleetOperation(ctx context.Context, projectID, operationID string, expectedInstanceRevision int64) (*fleet.Operation, error) {
	in := struct {
		ExpectedInstanceRevision int64 `json:"expected_instance_revision"`
	}{ExpectedInstanceRevision: expectedInstanceRevision}
	var out fleet.Operation
	err := c.do(ctx, http.MethodPost, fleetPath(projectID)+"/operations/"+url.PathEscape(operationID)+"/cancel", in, &out)
	return &out, err
}
