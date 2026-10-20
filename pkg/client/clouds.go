package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Cloud is the public Cloud API record.
type Cloud struct {
	ID        string    `json:"id" forge:"id"`
	OrgID     string    `json:"org_id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CloudTarget is the public Cloud API record.
type CloudTarget struct {
	ID           string    `json:"id" forge:"id"`
	OrgID        string    `json:"org_id"`
	ProjectID    string    `json:"project_id"`
	CloudID      string    `json:"cloud_id"`
	Name         string    `json:"name"`
	DisplayName  string    `json:"display_name"`
	Provider     string    `json:"provider"`
	ConnectionID string    `json:"connection_id"`
	Region       string    `json:"region"`
	Revision     int64     `json:"revision"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// CloudAssignment is the public Cloud API record.
type CloudAssignment struct {
	CloudRevision int64         `json:"cloud_revision"`
	OrgID         string        `json:"org_id"`
	ProjectID     string        `json:"project_id"`
	EnvironmentID string        `json:"environment_id"`
	CloudID       string        `json:"cloud_id"`
	Revision      int64         `json:"revision"`
	Targets       []CloudTarget `json:"targets"`
}

// CloudChangeInput is the public Cloud API record.
type CloudChangeInput struct {
	Revision      int64  `json:"revision"`
	CloudID       string `json:"cloud_id"`
	TargetID      string `json:"target_id,omitempty"`
	EnvironmentID string `json:"environment_id,omitempty"`
	Provider      string `json:"provider,omitempty"`
	ConnectionID  string `json:"connection_id,omitempty"`
	Region        string `json:"region,omitempty"`
}

// CloudPlanSummary is the public Cloud API record.
type CloudPlanSummary struct {
	Create  int `json:"create"`
	Update  int `json:"update"`
	Replace int `json:"replace"`
	Delete  int `json:"delete"`
	Keep    int `json:"keep"`
	Adopt   int `json:"adopt"`
}

// CloudEnvironmentChange is the public Cloud API record.
type CloudEnvironmentChange struct {
	EnvironmentID   string           `json:"environment_id"`
	Environment     string           `json:"environment"`
	Services        []string         `json:"services"`
	Summary         CloudPlanSummary `json:"summary"`
	PlanID          string           `json:"plan_id,omitempty"`
	DeploymentID    string           `json:"deployment_id,omitempty"`
	Status          string           `json:"status"`
	Error           string           `json:"error,omitempty"`
	ExternalRouting bool             `json:"external_routing"`
	RoutingEvidence string           `json:"routing_evidence,omitempty"`
}

// CloudPreview is the public Cloud API record.
type CloudPreview struct {
	ID               string                   `json:"id" forge:"id"`
	OrgID            string                   `json:"org_id"`
	ProjectID        string                   `json:"project_id"`
	Revision         string                   `json:"revision"`
	Input            CloudChangeInput         `json:"input"`
	Before           *CloudTarget             `json:"before,omitempty"`
	After            *CloudTarget             `json:"after,omitempty"`
	BeforeAssignment *CloudAssignment         `json:"before_assignment,omitempty"`
	AfterAssignment  *CloudAssignment         `json:"after_assignment,omitempty"`
	Environments     []CloudEnvironmentChange `json:"environments"`
	Blockers         []string                 `json:"blockers"`
	Reverts          string                   `json:"reverts,omitempty"`
	CreatedAt        time.Time                `json:"created_at"`
}

// CloudSync is the public Cloud API record.
type CloudSync struct {
	ID           string                   `json:"id" forge:"id"`
	OrgID        string                   `json:"org_id"`
	ProjectID    string                   `json:"project_id"`
	CloudID      string                   `json:"cloud_id"`
	TargetID     string                   `json:"target_id,omitempty"`
	PreviewID    string                   `json:"preview_id"`
	Status       string                   `json:"status"`
	CreatedBy    string                   `json:"created_by"`
	Before       *CloudTarget             `json:"before,omitempty"`
	After        *CloudTarget             `json:"after,omitempty"`
	Environments []CloudEnvironmentChange `json:"environments"`
	Attempt      int                      `json:"attempt"`
	Error        string                   `json:"error,omitempty"`
	Reverts      string                   `json:"reverts,omitempty"`
	CreatedAt    time.Time                `json:"created_at"`
	UpdatedAt    time.Time                `json:"updated_at"`
}

type CloudCreateInput struct {
	Name string `json:"name"`
}

type CloudUpdateInput struct {
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
}

type CloudCreateTargetInput struct {
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	Provider     string `json:"provider"`
	ConnectionID string `json:"connection_id"`
	Region       string `json:"region"`
}

type CloudUpdateTargetInput struct {
	DisplayName string `json:"display_name"`
	Revision    int64  `json:"revision"`
}

type EnvironmentCloud struct {
	Assignment *CloudAssignment `json:"assignment"`
	Revision   int64            `json:"revision"`
}
type EnvironmentCloudInput struct {
	CloudID  string `json:"cloud_id"`
	Revision int64  `json:"revision"`
}
type ApplyCloudInput struct {
	PreviewID      string `json:"preview_id"`
	Revision       string `json:"revision"`
	IdempotencyKey string `json:"idempotency_key"`
}
type CompleteCloudRoutingInput struct {
	EnvironmentID string `json:"environment_id"`
	Evidence      string `json:"evidence"`
}
type CloudSyncPage struct {
	Syncs []CloudSync `json:"syncs"`
	Next  string      `json:"next,omitempty"`
}

func cloudPath(project string) string { return "/api/v1/projects/" + esc(project) }
func (c *Client) Clouds(ctx context.Context, project string) ([]Cloud, error) {
	var out struct {
		Clouds []Cloud `json:"clouds"`
	}
	err := c.do(ctx, http.MethodGet, cloudPath(project)+"/clouds", nil, &out)
	return out.Clouds, err
}
func (c *Client) CreateCloud(ctx context.Context, project string, in CloudCreateInput) (*Cloud, error) {
	var out Cloud
	err := c.do(ctx, http.MethodPost, cloudPath(project)+"/clouds", in, &out)
	return &out, err
}
func (c *Client) Cloud(ctx context.Context, project, id string) (*Cloud, error) {
	var out Cloud
	err := c.do(ctx, http.MethodGet, cloudPath(project)+"/clouds/"+esc(id), nil, &out)
	return &out, err
}
func (c *Client) UpdateCloud(ctx context.Context, project, id string, in CloudUpdateInput) (*Cloud, error) {
	var out Cloud
	err := c.do(ctx, http.MethodPut, cloudPath(project)+"/clouds/"+esc(id), in, &out)
	return &out, err
}
func (c *Client) DeleteCloud(ctx context.Context, project, id string) error {
	return c.do(ctx, http.MethodDelete, cloudPath(project)+"/clouds/"+esc(id), nil, nil)
}
func targetPath(project, cloud string) string {
	return cloudPath(project) + "/clouds/" + esc(cloud) + "/targets"
}
func (c *Client) CloudTargets(ctx context.Context, project, cloud string) ([]CloudTarget, error) {
	var out struct {
		Targets []CloudTarget `json:"targets"`
	}
	err := c.do(ctx, http.MethodGet, targetPath(project, cloud), nil, &out)
	return out.Targets, err
}
func (c *Client) CloudTarget(ctx context.Context, project, cloud, id string) (*CloudTarget, error) {
	var out CloudTarget
	err := c.do(ctx, http.MethodGet, targetPath(project, cloud)+"/"+esc(id), nil, &out)
	return &out, err
}
func (c *Client) CreateCloudTarget(ctx context.Context, project, cloud string, in CloudCreateTargetInput) (*CloudTarget, error) {
	var out CloudTarget
	err := c.do(ctx, http.MethodPost, targetPath(project, cloud), in, &out)
	return &out, err
}
func (c *Client) UpdateCloudTarget(ctx context.Context, project, cloud, id string, in CloudUpdateTargetInput) (*CloudTarget, error) {
	var out CloudTarget
	err := c.do(ctx, http.MethodPut, targetPath(project, cloud)+"/"+esc(id), in, &out)
	return &out, err
}
func (c *Client) DeleteCloudTarget(ctx context.Context, project, cloud, id string) error {
	return c.do(ctx, http.MethodDelete, targetPath(project, cloud)+"/"+esc(id), nil, nil)
}
func (c *Client) EnvironmentCloud(ctx context.Context, project, environment string) (*EnvironmentCloud, error) {
	var out EnvironmentCloud
	err := c.do(ctx, http.MethodGet, cloudPath(project)+"/environments/"+esc(environment)+"/cloud", nil, &out)
	return &out, err
}
func (c *Client) PreviewEnvironmentCloud(ctx context.Context, project, environment string, in EnvironmentCloudInput) (*CloudPreview, error) {
	var out CloudPreview
	err := c.do(ctx, http.MethodPost, cloudPath(project)+"/environments/"+esc(environment)+"/cloud/preview", in, &out)
	return &out, err
}
func (c *Client) PreviewCloudChange(ctx context.Context, project string, in CloudChangeInput) (*CloudPreview, error) {
	var out CloudPreview
	err := c.do(ctx, http.MethodPost, cloudPath(project)+"/cloud-changes/preview", in, &out)
	return &out, err
}
func (c *Client) ApplyCloudChange(ctx context.Context, project string, in ApplyCloudInput) (*CloudSync, error) {
	var out CloudSync
	err := c.do(ctx, http.MethodPost, cloudPath(project)+"/cloud-changes/apply", in, &out)
	return &out, err
}
func (c *Client) CloudSyncs(ctx context.Context, project, before string, limit int) (*CloudSyncPage, error) {
	q := url.Values{}
	if before != "" {
		q.Set("before", before)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out CloudSyncPage
	err := c.do(ctx, http.MethodGet, cloudPath(project)+"/cloud-syncs?"+q.Encode(), nil, &out)
	return &out, err
}
func syncPath(project, id string) string { return cloudPath(project) + "/cloud-syncs/" + esc(id) }
func (c *Client) CloudSync(ctx context.Context, project, id string) (*CloudSync, error) {
	var out CloudSync
	err := c.do(ctx, http.MethodGet, syncPath(project, id), nil, &out)
	return &out, err
}
func (c *Client) RetryCloudSync(ctx context.Context, project, id string) (*CloudSync, error) {
	var out CloudSync
	err := c.do(ctx, http.MethodPost, syncPath(project, id)+"/retry", nil, &out)
	return &out, err
}
func (c *Client) PreviewCloudRevert(ctx context.Context, project, id string) (*CloudPreview, error) {
	var out CloudPreview
	err := c.do(ctx, http.MethodPost, syncPath(project, id)+"/revert/preview", nil, &out)
	return &out, err
}
func (c *Client) ApplyCloudRevert(ctx context.Context, project string, in ApplyCloudInput) (*CloudSync, error) {
	var out CloudSync
	err := c.do(ctx, http.MethodPost, cloudPath(project)+"/cloud-syncs/revert/apply", in, &out)
	return &out, err
}
func (c *Client) CompleteCloudRouting(ctx context.Context, project, id string, in CompleteCloudRoutingInput) (*CloudSync, error) {
	var out CloudSync
	err := c.do(ctx, http.MethodPost, syncPath(project, id)+"/routing", in, &out)
	return &out, err
}

// WaitCloudSync stops at success, failure, or a checkpoint requiring your action.
// Always give ctx a deadline. On timeout the latest persisted record is returned.
func (c *Client) WaitCloudSync(ctx context.Context, project, id string) (*CloudSync, error) {
	var latest *CloudSync
	out, err := wait(ctx, func() (*CloudSync, bool, error) {
		v, e := c.CloudSync(ctx, project, id)
		if e != nil {
			return nil, false, e
		}
		latest = v
		return v, v.Status == "succeeded" || v.Status == "reverted" || v.Status == "failed" || v.Status == "routing_pending" || (v.Status == "cleanup_pending" && v.Error != ""), nil
	})
	if err != nil {
		return latest, err
	}
	return out, nil
}

func (c *Client) CloudPreview(ctx context.Context, project, id string) (*CloudPreview, error) {
	var out CloudPreview
	err := c.do(ctx, http.MethodGet, cloudPath(project)+"/cloud-changes/"+esc(id), nil, &out)
	return &out, err
}
