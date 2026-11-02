package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type NativeRetirementRequest struct {
	RequestID           string            `json:"request_id"`
	Policy              RetentionSettings `json:"policy"`
	AcknowledgeDeletion bool              `json:"acknowledge_deletion"`
}

type NativeRetirement struct {
	ID             string            `json:"id"`
	OrgID          string            `json:"org_id"`
	ProjectID      string            `json:"project_id"`
	SourceID       string            `json:"source_id"`
	PointID        string            `json:"point_id"`
	WitnessID      string            `json:"witness_id"`
	Status         string            `json:"status"`
	Policy         RetentionSettings `json:"policy"`
	PolicyID       string            `json:"policy_id,omitempty"`
	PolicyRevision int64             `json:"policy_revision,omitempty"`
	CreatedBy      string            `json:"created_by"`
	CreatedAt      time.Time         `json:"created_at"`
	CompletedAt    *time.Time        `json:"completed_at,omitempty"`
	Error          string            `json:"error,omitempty"`
}

func (c *Client) NativeRetentionPreview(ctx context.Context, source string, days, keep int) (*RetentionPreview, error) {
	values := url.Values{"older_than_days": {strconv.Itoa(days)}, "keep_last": {strconv.Itoa(keep)}}
	var out RetentionPreview
	err := c.do(ctx, http.MethodGet, "/api/v1/native-recovery-sources/"+esc(source)+"/retention-preview?"+values.Encode(), nil, &out)
	return &out, err
}
func (c *Client) RetireNativePoint(ctx context.Context, source, point string, in NativeRetirementRequest) (*NativeRetirement, error) {
	var out NativeRetirement
	err := c.do(ctx, http.MethodPost, "/api/v1/native-recovery-sources/"+esc(source)+"/points/"+esc(point)+"/retirements", in, &out)
	return &out, err
}
func (c *Client) NativeRetirement(ctx context.Context, id string) (*NativeRetirement, error) {
	var out NativeRetirement
	err := c.do(ctx, http.MethodGet, "/api/v1/native-recovery-retirements/"+esc(id), nil, &out)
	return &out, err
}
func (c *Client) RetryNativeRetirement(ctx context.Context, id string) (*NativeRetirement, error) {
	var out NativeRetirement
	err := c.do(ctx, http.MethodPost, "/api/v1/native-recovery-retirements/"+esc(id)+"/retry", nil, &out)
	return &out, err
}
func (c *Client) NativeRetirementHistory(ctx context.Context, id string, in RetirementPageInput) (*RetirementHistory, error) {
	var out RetirementHistory
	err := c.do(ctx, http.MethodGet, "/api/v1/native-recovery-retirements/"+esc(id)+"/history"+retirementQuery(in), nil, &out)
	return &out, err
}
func (c *Client) WaitNativeRetirement(ctx context.Context, id string) (*NativeRetirement, error) {
	var last *NativeRetirement
	return wait(ctx, func() (*NativeRetirement, bool, error) {
		r, err := c.NativeRetirement(ctx, id)
		if err != nil {
			return last, false, err
		}
		last = r
		return r, r.Status == "succeeded" || r.Status == "failed", nil
	})
}

func (c *Client) NativeRetirements(ctx context.Context, source string, in NativeRecoveryPage) (*NativeRetirements, error) {
	query, err := nativeRecoveryQuery(in)
	if err != nil {
		return nil, err
	}
	var out NativeRetirements
	err = c.do(ctx, http.MethodGet, "/api/v1/native-recovery-sources/"+esc(source)+"/retirements"+query, nil, &out)
	return &out, err
}

type NativeRetirements struct {
	Retirements []NativeRetirement `json:"retirements"`
	NextBefore  string             `json:"next_before,omitempty"`
}
