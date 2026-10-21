package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type RetentionSettings struct {
	OlderThanDays int `json:"older_than_days"`
	KeepLast      int `json:"keep_last"`
}

type RetirementRequest struct {
	RequestID string            `json:"request_id"`
	Policy    RetentionSettings `json:"policy"`
}

type ServiceRetirement struct {
	ID           string            `json:"id"`
	OrgID        string            `json:"org_id"`
	ProjectID    string            `json:"project_id"`
	BackupID     string            `json:"backup_id"`
	WitnessID    string            `json:"witness_id"`
	Status       string            `json:"status"`
	ExecutionBy  string            `json:"execution_by"`
	AttemptLimit int               `json:"attempt_limit"`
	Attempt      int               `json:"attempt"`
	Error        string            `json:"error,omitempty"`
	Policy       RetentionSettings `json:"policy"`
	CreatedBy    string            `json:"created_by"`
	CreatedAt    time.Time         `json:"created_at"`
	CompletedAt  *time.Time        `json:"completed_at,omitempty"`
}

type RetirementPageInput struct {
	Before string
	Limit  int
}

type RetirementPage struct {
	Retirements []ServiceRetirement `json:"retirements"`
	NextBefore  string              `json:"next_before,omitempty"`
}

type RetirementEvent struct {
	Cursor        string    `json:"cursor"`
	Kind          string    `json:"kind"`
	Status        string    `json:"status"`
	Attempt       int       `json:"attempt"`
	AttemptLimit  int       `json:"attempt_limit"`
	Actor         string    `json:"actor"`
	PreviousActor string    `json:"previous_actor,omitempty"`
	Error         string    `json:"error,omitempty"`
	PreviousError string    `json:"previous_error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type RetirementHistory struct {
	Events     []RetirementEvent `json:"events"`
	NextBefore string            `json:"next_before,omitempty"`
}

func retirementQuery(in RetirementPageInput) string {
	values := url.Values{}
	if in.Before != "" {
		values.Set("before", in.Before)
	}
	if in.Limit != 0 {
		values.Set("limit", strconv.Itoa(in.Limit))
	}
	if len(values) == 0 {
		return ""
	}
	return "?" + values.Encode()
}

func (c *Client) RetireServiceBackup(ctx context.Context, id string, in RetirementRequest) (*ServiceRetirement, error) {
	var out ServiceRetirement
	err := c.do(ctx, http.MethodPost, "/api/v1/service-backups/"+esc(id)+"/retire", in, &out)
	return &out, err
}

func (c *Client) ServiceRetirement(ctx context.Context, id string) (*ServiceRetirement, error) {
	var out ServiceRetirement
	err := c.do(ctx, http.MethodGet, "/api/v1/service-backup-retirements/"+esc(id), nil, &out)
	return &out, err
}

func (c *Client) RetryServiceRetirement(ctx context.Context, id, requestID string) (*ServiceRetirement, error) {
	var out ServiceRetirement
	err := c.do(ctx, http.MethodPost, "/api/v1/service-backup-retirements/"+esc(id)+"/retry", map[string]string{"request_id": requestID}, &out)
	return &out, err
}

func (c *Client) ServiceRetirements(ctx context.Context, project string, in RetirementPageInput) (*RetirementPage, error) {
	var out RetirementPage
	err := c.do(ctx, http.MethodGet, "/api/v1/projects/"+esc(project)+"/service-backup-retirements"+retirementQuery(in), nil, &out)
	return &out, err
}

func (c *Client) ServiceRetirementHistory(ctx context.Context, id string, in RetirementPageInput) (*RetirementHistory, error) {
	var out RetirementHistory
	err := c.do(ctx, http.MethodGet, "/api/v1/service-backup-retirements/"+esc(id)+"/history"+retirementQuery(in), nil, &out)
	return &out, err
}

func (c *Client) WaitServiceRetirement(ctx context.Context, id string) (*ServiceRetirement, error) {
	return wait(ctx, func() (*ServiceRetirement, bool, error) {
		r, err := c.ServiceRetirement(ctx, id)
		if err != nil {
			return nil, false, err
		}
		return r, r.Status == "succeeded" || r.Status == "failed", nil
	})
}
