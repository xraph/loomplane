package client

import (
	"context"
	"net/http"
	"time"
)

type ServiceBackup struct {
	ID            string     `json:"id"`
	ProjectID     string     `json:"project_id"`
	EnvironmentID string     `json:"environment_id"`
	Name          string     `json:"name"`
	Status        string     `json:"status"`
	Attempt       int        `json:"attempt"`
	Bytes         int64      `json:"bytes"`
	SHA256        string     `json:"sha256,omitempty"`
	Error         string     `json:"error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	RestoredAt    *time.Time `json:"restored_at,omitempty"`
}
type BackupRequest struct {
	Environment string `json:"environment"`
	Resource    string `json:"resource"`
	RequestID   string `json:"request_id"`
}
type ServiceRestore struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"project_id"`
	BackupID   string     `json:"backup_id"`
	Status     string     `json:"status"`
	ExternalID string     `json:"external_id,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	DurationMS int64      `json:"duration_ms"`
}
type BackupPolicy struct {
	ID          string    `json:"id"`
	Environment string    `json:"environment"`
	Resource    string    `json:"resource"`
	EveryHours  int       `json:"every_hours"`
	Enabled     bool      `json:"enabled"`
	NextAt      time.Time `json:"next_at"`
	LastError   string    `json:"last_error,omitempty"`
}
type BackupPolicyInput struct {
	Environment string `json:"environment"`
	Resource    string `json:"resource"`
	EveryHours  int    `json:"every_hours"`
	Enabled     bool   `json:"enabled"`
}

func (c *Client) ServiceBackups(ctx context.Context, project string) ([]ServiceBackup, error) {
	var out struct {
		Backups []ServiceBackup `json:"backups"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/projects/"+esc(project)+"/service-backups", nil, &out)
	return out.Backups, err
}
func (c *Client) CreateServiceBackup(ctx context.Context, project string, in BackupRequest) (*ServiceBackup, error) {
	var out ServiceBackup
	err := c.do(ctx, http.MethodPost, "/api/v1/projects/"+esc(project)+"/service-backups", in, &out)
	return &out, err
}
func (c *Client) ServiceBackup(ctx context.Context, id string) (*ServiceBackup, error) {
	var out ServiceBackup
	err := c.do(ctx, http.MethodGet, "/api/v1/service-backups/"+esc(id), nil, &out)
	return &out, err
}
func (c *Client) RetryServiceBackup(ctx context.Context, id string) (*ServiceBackup, error) {
	var out ServiceBackup
	err := c.do(ctx, http.MethodPost, "/api/v1/service-backups/"+esc(id)+"/retry", nil, &out)
	return &out, err
}
func (c *Client) RestoreServiceBackup(ctx context.Context, id, requestID string) (*ServiceRestore, error) {
	var out ServiceRestore
	err := c.do(ctx, http.MethodPost, "/api/v1/service-backups/"+esc(id)+"/restore", map[string]string{"request_id": requestID}, &out)
	return &out, err
}
func (c *Client) ServiceRestores(ctx context.Context, project string) ([]ServiceRestore, error) {
	var out struct {
		Restores []ServiceRestore `json:"restores"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/projects/"+esc(project)+"/service-restores", nil, &out)
	return out.Restores, err
}
func (c *Client) ServiceRestore(ctx context.Context, id string) (*ServiceRestore, error) {
	var out ServiceRestore
	err := c.do(ctx, http.MethodGet, "/api/v1/service-restores/"+esc(id), nil, &out)
	return &out, err
}
func (c *Client) DeleteServiceRestore(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/service-restores/"+esc(id), nil, nil)
}
func (c *Client) BackupPolicies(ctx context.Context, project string) ([]BackupPolicy, error) {
	var out struct {
		Policies []BackupPolicy `json:"policies"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/projects/"+esc(project)+"/backup-policies", nil, &out)
	return out.Policies, err
}
func (c *Client) SetBackupPolicy(ctx context.Context, project string, in BackupPolicyInput) (*BackupPolicy, error) {
	var out BackupPolicy
	err := c.do(ctx, http.MethodPut, "/api/v1/projects/"+esc(project)+"/backup-policies", in, &out)
	return &out, err
}
