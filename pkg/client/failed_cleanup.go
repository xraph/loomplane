package client

import (
	"context"
	"net/http"
)

type CleanupObject struct {
	Resource  string `json:"resource"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}
type CleanupTarget struct {
	Kind             string          `json:"kind"`
	Service          string          `json:"service"`
	PlanID           string          `json:"plan_id"`
	DeploymentID     string          `json:"deployment_id"`
	Namespace        string          `json:"namespace"`
	Objects          []CleanupObject `json:"objects"`
	UnresolvedWrites int             `json:"unresolved_writes"`
	IdentityCurrent  bool            `json:"identity_current"`
}
type CleanupReview struct {
	Blockers         []string        `json:"blockers,omitempty"`
	PreviousReviewID string          `json:"previous_review_id,omitempty"`
	Variant          string          `json:"variant,omitempty"`
	ReviewSequence   int64           `json:"review_sequence,omitempty"`
	Targets          []CleanupTarget `json:"targets,omitempty"`
	DeploymentID     string          `json:"deployment_id"`
	Revision         string          `json:"revision"`
	Resource         string          `json:"resource"`
	Provider         string          `json:"provider"`
	Region           string          `json:"region"`
	ConnectionID     string          `json:"connection_id"`
	Effect           string          `json:"effect"`
}
type FailedCleanup struct {
	Variant        string `json:"variant,omitempty"`
	ReviewID       string `json:"review_id,omitempty"`
	ReviewSequence int64  `json:"review_sequence,omitempty"`
	DeploymentID   string `json:"deployment_id"`
	Revision       string `json:"revision"`
	Status         string `json:"status"`
	Attempt        int    `json:"attempt"`
	Error          string `json:"error,omitempty"`
	ErrorCode      string `json:"error_code,omitempty"`
}

func (c *Client) PreviewFailedCleanup(ctx context.Context, id string) (*CleanupReview, error) {
	var out CleanupReview
	err := c.do(ctx, http.MethodPost, "/api/v1/deployments/"+esc(id)+"/cleanup/preview", nil, &out)
	return &out, err
}
func (c *Client) FailedCleanup(ctx context.Context, id string) (*FailedCleanup, error) {
	var out FailedCleanup
	err := c.do(ctx, http.MethodGet, "/api/v1/deployments/"+esc(id)+"/cleanup", nil, &out)
	return &out, err
}
func (c *Client) ApplyFailedCleanup(ctx context.Context, id, revision string) (*FailedCleanup, error) {
	return c.controlFailedCleanup(ctx, id, "apply", map[string]string{"revision": revision})
}
func (c *Client) RetryFailedCleanup(ctx context.Context, id string) (*FailedCleanup, error) {
	return c.controlFailedCleanup(ctx, id, "retry", nil)
}
func (c *Client) CancelFailedCleanup(ctx context.Context, id, review string) (*FailedCleanup, error) {
	return c.controlFailedCleanup(ctx, id, "cancel", map[string]string{"expected_review_id": review})
}
func (c *Client) controlFailedCleanup(ctx context.Context, id, action string, in any) (*FailedCleanup, error) {
	var out FailedCleanup
	err := c.do(ctx, http.MethodPost, "/api/v1/deployments/"+esc(id)+"/cleanup/"+action, in, &out)
	return &out, err
}
