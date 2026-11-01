package client

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

type AttachmentInput struct {
	RuntimeAccess       *provider.RuntimeAccessProfile `json:"runtime_access,omitempty"`
	TargetSlug          string                         `json:"target_slug"`
	CloudRevision       int64                          `json:"cloud_revision"`
	ResourceRevision    int64                          `json:"resource_revision"`
	GrantRevision       int64                          `json:"grant_revision"`
	TrustFingerprint    string                         `json:"trust_fingerprint"`
	EndpointFingerprint string                         `json:"endpoint_fingerprint"`
	Prerequisites       AttachmentPrerequisites        `json:"prerequisites"`
	ProjectID           string                         `json:"project_id"`
	Namespace           string                         `json:"namespace"`
	NamespaceUID        string                         `json:"namespace_uid,omitempty"`
	ServiceAccountName  string                         `json:"service_account_name"`
	AdoptNamespace      bool                           `json:"adopt_namespace"`
}

type ClusterInput struct {
	Scaling        *ClusterScalingInput     `json:"scaling,omitempty"`
	Maintenance    *ClusterMaintenanceInput `json:"maintenance,omitempty"`
	Retirement     *ClusterRetirementInput  `json:"retirement,omitempty"`
	RuntimeAccess  *RuntimeAccessReview     `json:"runtime_access,omitempty"`
	PolicyRevision int64                    `json:"policy_revision"`
	// ManagementReachable is caller acknowledgement, never path evidence.
	ManagementReachable     bool                `json:"management_reachable"`
	AcknowledgeUnknownCosts bool                `json:"acknowledge_unknown_costs"`
	AcknowledgeNodeCosts    bool                `json:"acknowledge_node_costs"`
	WorkerImage             string              `json:"worker_image"`
	Kind                    string              `json:"kind"`
	Provider                string              `json:"provider"`
	ConnectionID            string              `json:"connection_id"`
	AccountID               string              `json:"account_id"`
	ResourceID              string              `json:"resource_id,omitempty"`
	ResourceRevision        int64               `json:"resource_revision"`
	ConnectionGeneration    int64               `json:"connection_generation"`
	CloudRevision           int64               `json:"cloud_revision"`
	OptionsDigest           string              `json:"options_digest"`
	OptionsCheckedAt        time.Time           `json:"options_checked_at"`
	Spec                    ClusterSpec         `json:"spec"`
	Ref                     provider.ClusterRef `json:"ref"`
	Attachment              *AttachmentInput    `json:"attachment,omitempty"`
}

type AttachmentPrerequisites struct {
	RegistryHostname         string `json:"registry_hostname,omitempty"`
	IngressAddress           string `json:"ingress_address,omitempty"`
	StorageClass             string `json:"storage_class,omitempty"`
	RequirePersistentStorage bool   `json:"require_persistent_storage"`
	RequireBuilds            bool   `json:"require_builds"`
	RequireIngress           bool   `json:"require_ingress"`
	RequireTLS               bool   `json:"require_tls"`
}

type ClusterStep struct {
	Name        string     `json:"name"`
	State       string     `json:"state"`
	AttemptedAt *time.Time `json:"attempted_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type ClusterEnrollmentSummary struct {
	AttachmentID        string    `json:"attachment_id"`
	RuntimeConnectionID string    `json:"runtime_connection_id"`
	TargetID            string    `json:"target_id"`
	CredentialExpiry    time.Time `json:"credential_expiry"`
	RuntimeVerified     bool      `json:"runtime_verified"`
}

type CloudClusterOperation struct {
	MoneyEstimate               *MoneyEstimate            `json:"money_estimate,omitempty"`
	BudgetReview                *BudgetReview             `json:"budget_review,omitempty"`
	EnvelopeReview              *ClusterEnvelopeReview    `json:"envelope_review,omitempty"`
	ID                          string                    `json:"id" forge:"id"`
	CloudID                     string                    `json:"cloud_id"`
	ResourceID                  string                    `json:"resource_id"`
	Kind                        string                    `json:"kind"`
	Input                       ClusterInput              `json:"input"`
	Digest                      string                    `json:"digest"`
	Revision                    int64                     `json:"revision"`
	Status                      string                    `json:"status"`
	Error                       string                    `json:"error"`
	Attempt                     int                       `json:"attempt"`
	CancelRequested             bool                      `json:"cancel_requested"`
	EstimatedNodeHourlyMicroUSD int64                     `json:"estimated_node_hourly_micro_usd"`
	UnknownCosts                []string                  `json:"unknown_costs"`
	Steps                       []ClusterStep             `json:"steps"`
	Enrollment                  *ClusterEnrollmentSummary `json:"enrollment,omitempty"`
	CreatedAt                   time.Time                 `json:"created_at"`
	UpdatedAt                   time.Time                 `json:"updated_at"`
	ExpiresAt                   time.Time                 `json:"expires_at"`
}

type CloudCluster struct {
	ScalingPolicies        map[string]ClusterScalingPolicy `json:"scaling_policies,omitempty"`
	RetainedResources      []RetainedResource              `json:"retained_resources,omitempty"`
	RetainedPriceCheckedAt time.Time                       `json:"retained_price_checked_at,omitzero"`
	DesiredRevision        int64                           `json:"desired_revision"`
	ObservedRevision       int64                           `json:"observed_revision"`
	ID                     string                          `json:"id" forge:"id"`
	CloudID                string                          `json:"cloud_id"`
	Provider               string                          `json:"provider"`
	AccountID              string                          `json:"account_id"`
	ConnectionID           string                          `json:"connection_id"`
	ExternalID             string                          `json:"external_id"`
	Ownership              string                          `json:"ownership"`
	LifecycleAuthority     []string                        `json:"lifecycle_authority"`
	Revision               int64                           `json:"revision"`
	Status                 string                          `json:"status"`
	Observation            *ClusterObservation             `json:"observation,omitempty"`
	UpdatedAt              time.Time                       `json:"updated_at"`
}

type CloudClusterPage struct {
	Items      []CloudCluster `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

type CloudClusterOperationPage struct {
	Items      []CloudClusterOperation `json:"items"`
	NextCursor string                  `json:"next_cursor,omitempty"`
}

func (c *Client) CloudClusters(ctx context.Context, cloud, cursor string) (*CloudClusterPage, error) {
	var out CloudClusterPage
	err := c.do(ctx, http.MethodGet, infrastructurePath(cloud)+"/clusters?cursor="+url.QueryEscape(cursor), nil, &out)
	return &out, err
}
func (c *Client) CloudCluster(ctx context.Context, cloud, id string) (*CloudCluster, error) {
	var out CloudCluster
	err := c.do(ctx, http.MethodGet, infrastructurePath(cloud)+"/clusters/"+esc(id), nil, &out)
	return &out, err
}
func (c *Client) CloudClusterOptions(ctx context.Context, cloud, connection, region string) (*provider.ClusterOptions, error) {
	var out provider.ClusterOptions
	err := c.do(ctx, http.MethodGet, infrastructurePath(cloud)+"/cluster-options/"+esc(connection)+"?region="+url.QueryEscape(region), nil, &out)
	return &out, err
}
func (c *Client) DiscoverClusters(ctx context.Context, connection, cursor string) (*ClusterPage, error) {
	var out ClusterPage
	err := c.do(ctx, http.MethodGet, "/api/v1/connections/"+esc(connection)+"/clusters?cursor="+url.QueryEscape(cursor), nil, &out)
	return &out, err
}
func (c *Client) PreviewCloudCluster(ctx context.Context, cloud string, in ClusterInput) (*CloudClusterOperation, error) {
	var out CloudClusterOperation
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/clusters/preview", in, &out)
	return &out, err
}
func (c *Client) CloudClusterOperations(ctx context.Context, cloud, cursor string) (*CloudClusterOperationPage, error) {
	var out CloudClusterOperationPage
	err := c.do(ctx, http.MethodGet, infrastructurePath(cloud)+"/cluster-operations?cursor="+url.QueryEscape(cursor), nil, &out)
	return &out, err
}
func (c *Client) CloudClusterOperation(ctx context.Context, cloud, id string) (*CloudClusterOperation, error) {
	var out CloudClusterOperation
	err := c.do(ctx, http.MethodGet, infrastructurePath(cloud)+"/cluster-operations/"+esc(id), nil, &out)
	return &out, err
}
func (c *Client) ApplyCloudCluster(ctx context.Context, cloud, id, review string) (*CloudClusterOperation, error) {
	return c.controlCloudCluster(ctx, cloud, id, "apply", InfrastructureApplyInput{Review: review})
}
func (c *Client) RetryCloudCluster(ctx context.Context, cloud, id string) (*CloudClusterOperation, error) {
	return c.controlCloudCluster(ctx, cloud, id, "retry", nil)
}
func (c *Client) CancelCloudCluster(ctx context.Context, cloud, id string) (*CloudClusterOperation, error) {
	return c.controlCloudCluster(ctx, cloud, id, "cancel", nil)
}
func (c *Client) controlCloudCluster(ctx context.Context, cloud, id, action string, in any) (*CloudClusterOperation, error) {
	var out CloudClusterOperation
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/cluster-operations/"+esc(id)+"/"+action, in, &out)
	return &out, err
}

// WaitCloudClusterOperation retains the last checkpoint when your wait expires.
func (c *Client) WaitCloudClusterOperation(ctx context.Context, cloud, id string) (*CloudClusterOperation, error) {
	var latest *CloudClusterOperation
	out, err := wait(ctx, func() (*CloudClusterOperation, bool, error) {
		v, checkErr := c.CloudClusterOperation(ctx, cloud, id)
		if checkErr != nil {
			return nil, false, checkErr
		}
		latest = v
		return v, v.Status != "accepted" && v.Status != "running" && v.Status != "waiting_provider", nil
	})
	if err != nil {
		return latest, err
	}
	return out, nil
}
