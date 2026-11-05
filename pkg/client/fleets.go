package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/xraph/loomplane/pkg/fleet"
)

func fleetPath(projectID string) string {
	return "/api/v1/projects/" + url.PathEscape(projectID) + "/fleet"
}

// GetFleet reads the fleet belonging to a project.
func (c *Client) GetFleet(ctx context.Context, projectID string) (*fleet.Fleet, error) {
	var out fleet.Fleet
	err := c.do(ctx, http.MethodGet, fleetPath(projectID), nil, &out)
	return &out, err
}

// GetFleetInstance reads one instance in the project's fleet.
func (c *Client) GetFleetInstance(ctx context.Context, projectID, instanceID string) (*fleet.Instance, error) {
	var out fleet.Instance
	err := c.do(ctx, http.MethodGet, fleetPath(projectID)+"/instances/"+url.PathEscape(instanceID), nil, &out)
	return &out, err
}

// ListFleetInstances returns a bounded page and its continuation cursor.
func (c *Client) ListFleetInstances(ctx context.Context, projectID string, in fleet.ListInstancesInput) (*fleet.InstancePage, error) {
	q := url.Values{}
	if in.After != "" {
		q.Set("after", in.After)
	}
	if in.Limit != 0 {
		q.Set("limit", strconv.Itoa(in.Limit))
	}
	if in.Status != "" {
		q.Set("status", in.Status)
	}
	if in.ExternalRef != "" {
		q.Set("external_ref", in.ExternalRef)
	}
	var out fleet.InstancePage
	err := c.do(ctx, http.MethodGet, fleetPath(projectID)+"/instances?"+q.Encode(), nil, &out)
	return &out, err
}

// GetFleetOperation reads progress without provider receipts or credentials.
func (c *Client) GetFleetOperation(ctx context.Context, projectID, operationID string) (*fleet.Operation, error) {
	var out fleet.Operation
	err := c.do(ctx, http.MethodGet, fleetPath(projectID)+"/operations/"+url.PathEscape(operationID), nil, &out)
	return &out, err
}

// GetFleetRelease reads an immutable template release.
func (c *Client) GetFleetRelease(ctx context.Context, projectID, releaseID string) (*fleet.TemplateRelease, error) {
	var out fleet.TemplateRelease
	err := c.do(ctx, http.MethodGet, fleetPath(projectID)+"/releases/"+url.PathEscape(releaseID), nil, &out)
	return &out, err
}

// CreateFleetInstance submits a stable request identity. Acceptance can await review.
func (c *Client) CreateFleetInstance(ctx context.Context, projectID string, in fleet.CreateInstanceInput) (*fleet.OperationResult, error) {
	var out fleet.OperationResult
	err := c.do(ctx, http.MethodPost, fleetPath(projectID)+"/instances", in, &out)
	return &out, err
}
