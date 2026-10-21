package client

import (
	"context"
	"net/http"
	"time"
)

type RetentionPolicy struct {
	ID               string            `json:"id"`
	OrgID            string            `json:"org_id"`
	ProjectID        string            `json:"project_id"`
	AnchorID         string            `json:"anchor_id"`
	Revision         int64             `json:"revision"`
	Policy           RetentionSettings `json:"policy"`
	Enabled          bool              `json:"enabled"`
	CreatedBy        string            `json:"created_by"`
	AuthorizedBy     string            `json:"authorized_by"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	NextAt           time.Time         `json:"next_at"`
	LastError        string            `json:"last_error,omitempty"`
	LastRetirementID string            `json:"last_retirement_id,omitempty"`
}

type RetentionPolicyState struct {
	Configured bool             `json:"configured"`
	Policy     *RetentionPolicy `json:"policy,omitempty"`
}

type RetentionPolicyInput struct {
	Revision int64             `json:"revision"`
	Policy   RetentionSettings `json:"policy"`
	Enabled  bool              `json:"enabled"`
}

type RetentionPolicyRevision struct {
	Revision     int64             `json:"revision"`
	Policy       RetentionSettings `json:"policy"`
	Enabled      bool              `json:"enabled"`
	AuthorizedBy string            `json:"authorized_by"`
	CreatedAt    time.Time         `json:"created_at"`
}

type RetentionPolicyHistory struct {
	Events     []RetentionPolicyRevision `json:"events"`
	NextBefore string                    `json:"next_before,omitempty"`
}

func retentionPolicyPath(id string) string {
	return "/api/v1/service-backups/" + esc(id) + "/retention-policy"
}

func (c *Client) BackupRetentionPolicy(ctx context.Context, id string) (*RetentionPolicyState, error) {
	var out RetentionPolicyState
	err := c.do(ctx, http.MethodGet, retentionPolicyPath(id), nil, &out)
	return &out, err
}
func (c *Client) SetBackupRetentionPolicy(ctx context.Context, id string, in RetentionPolicyInput) (*RetentionPolicy, error) {
	var out RetentionPolicy
	err := c.do(ctx, http.MethodPut, retentionPolicyPath(id), in, &out)
	return &out, err
}
func (c *Client) BackupRetentionPolicyHistory(ctx context.Context, id string, in RetirementPageInput) (*RetentionPolicyHistory, error) {
	var out RetentionPolicyHistory
	// A history cursor cannot filter by a backup ID; id already scopes the route.
	in.BackupID = ""
	err := c.do(ctx, http.MethodGet, retentionPolicyPath(id)+"/history"+retirementQuery(in), nil, &out)
	return &out, err
}
