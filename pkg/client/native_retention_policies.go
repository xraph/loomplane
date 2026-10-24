package client

import (
	"context"
	"net/http"
	"time"
)

type NativeRetentionPolicy struct {
	ID               string            `json:"id"`
	OrgID            string            `json:"org_id"`
	ProjectID        string            `json:"project_id"`
	SourceID         string            `json:"source_id"`
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
type NativeRetentionPolicyState struct {
	Configured bool                   `json:"configured"`
	Policy     *NativeRetentionPolicy `json:"policy,omitempty"`
}

func (c *Client) NativeRetentionPolicy(ctx context.Context, source string) (*NativeRetentionPolicyState, error) {
	var out NativeRetentionPolicyState
	err := c.do(ctx, http.MethodGet, "/api/v1/native-recovery-sources/"+esc(source)+"/retention-policy", nil, &out)
	return &out, err
}
func (c *Client) SetNativeRetentionPolicy(ctx context.Context, source string, in RetentionPolicyInput) (*NativeRetentionPolicy, error) {
	var out NativeRetentionPolicy
	err := c.do(ctx, http.MethodPut, "/api/v1/native-recovery-sources/"+esc(source)+"/retention-policy", in, &out)
	return &out, err
}
func (c *Client) NativeRetentionPolicyHistory(ctx context.Context, source string, in RetirementPageInput) (*RetentionPolicyHistory, error) {
	var out RetentionPolicyHistory
	err := c.do(ctx, http.MethodGet, "/api/v1/native-recovery-sources/"+esc(source)+"/retention-policy/history"+retirementQuery(in), nil, &out)
	return &out, err
}
