package client

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type RDSRecoveryNetwork struct {
	VPCID           string `json:"vpc_id"`
	SubnetGroup     string `json:"subnet_group"`
	SecurityGroupID string `json:"security_group_id"`
	VerifierGroupID string `json:"verifier_group_id"`
}

type CloudSQLRecoveryNetwork struct {
	ConsumerProject string `json:"consumer_project"`
	Network         string `json:"network"`
}

type NativeRecoveryTarget struct {
	NativeAccount   string                  `json:"native_account,omitempty"`
	RDSNetwork      RDSRecoveryNetwork      `json:"rds_network,omitempty,omitzero"`
	CloudSQLNetwork CloudSQLRecoveryNetwork `json:"cloud_sql_network,omitempty,omitzero"`
	Provider        string                  `json:"provider"`
	ConnectionID    string                  `json:"connection_id"`
	AccountIdentity string                  `json:"account_identity"`
	NativeProject   string                  `json:"native_project,omitempty"`
	Region          string                  `json:"region"`
	CloudID         string                  `json:"cloud_id,omitempty"`
	TargetID        string                  `json:"target_id,omitempty"`
	CloudRevision   int64                   `json:"cloud_revision,omitempty"`
	TargetRevision  int64                   `json:"target_revision,omitempty"`
	GrantRevision   int64                   `json:"grant_revision,omitempty"`
}

type NativeRecoveryCapacity struct {
	IOPS            int32  `json:"iops,omitempty"`
	ThroughputMBPS  int32  `json:"throughput_mbps,omitempty"`
	MaxStorageGB    int32  `json:"max_storage_gb,omitempty"`
	ProfileID       string `json:"profile_id"`
	ProfileRevision string `json:"profile_revision"`
	ComputeClass    string `json:"compute_class"`
	StorageGB       int32  `json:"storage_gb"`
	StorageType     string `json:"storage_type"`
	Availability    string `json:"availability"`
}

type NativeCopyReviewRequest struct {
	RestoreTime *time.Time `json:"restore_time,omitempty"`
	RequestID   string     `json:"request_id"`
	CloudID     string     `json:"cloud_id"`
	TargetID    string     `json:"target_id"`
	TTLHours    int        `json:"ttl_hours"`
}

type NativeRecoveryReview struct {
	RestoreTime *time.Time             `json:"restore_time,omitempty"`
	RequestID   string                 `json:"request_id"`
	ReviewToken string                 `json:"review_token"`
	SourceID    string                 `json:"source_id"`
	PointID     string                 `json:"point_id"`
	Target      NativeRecoveryTarget   `json:"target"`
	Capacity    NativeRecoveryCapacity `json:"capacity"`
	TTLHours    int                    `json:"ttl_hours"`
	ValidUntil  time.Time              `json:"valid_until"`
	Notice      string                 `json:"notice"`
}

type NativeCopyRequest struct {
	RequestID          string `json:"request_id"`
	ReviewToken        string `json:"review_token"`
	AcknowledgeCharges bool   `json:"acknowledge_charges"`
}

// NativeRecoveryCopy exposes retained status without worker secrets or checkpoints.
type NativeRecoveryCopy struct {
	RestoreTime        *time.Time           `json:"restore_time,omitempty"`
	ID                 string               `json:"id"`
	OrgID              string               `json:"org_id"`
	ProjectID          string               `json:"project_id"`
	EnvironmentID      string               `json:"environment_id"`
	SourceID           string               `json:"source_id"`
	PointID            string               `json:"point_id"`
	Target             NativeRecoveryTarget `json:"target"`
	Status             string               `json:"status"`
	ExternalID         string               `json:"external_id,omitempty"`
	PhysicalID         string               `json:"physical_id,omitempty"`
	Error              string               `json:"error,omitempty"`
	CreatedBy          string               `json:"created_by"`
	CleanupBy          string               `json:"cleanup_by,omitempty"`
	CleanupRequestedAt *time.Time           `json:"cleanup_requested_at,omitempty"`
	CreatedAt          time.Time            `json:"created_at"`
	ExpiresAt          time.Time            `json:"expires_at"`
	VerifiedAt         *time.Time           `json:"verified_at,omitempty"`
	DurationMS         int64                `json:"duration_ms"`
}

type NativeCopyPage = NativeRecoveryPage

type NativeRecoveryCopies struct {
	Copies     []NativeRecoveryCopy `json:"copies"`
	NextBefore string               `json:"next_before,omitempty"`
}

func (c *Client) ReviewNativeRecoveryCopy(ctx context.Context, source, point string, in NativeCopyReviewRequest) (*NativeRecoveryReview, error) {
	var out NativeRecoveryReview
	err := c.do(ctx, http.MethodPost, "/api/v1/native-recovery-sources/"+esc(source)+"/points/"+esc(point)+"/copy-review", in, &out)
	return &out, err
}

func (c *Client) CreateNativeRecoveryCopy(ctx context.Context, in NativeCopyRequest) (*NativeRecoveryCopy, error) {
	if !in.AcknowledgeCharges || in.RequestID == "" || in.ReviewToken == "" {
		return nil, fmt.Errorf("provide the reviewed request, token and charge acknowledgement")
	}
	var out NativeRecoveryCopy
	err := c.do(ctx, http.MethodPost, "/api/v1/native-recovery-copies", in, &out)
	return &out, err
}

func (c *Client) NativeRecoveryCopy(ctx context.Context, id string) (*NativeRecoveryCopy, error) {
	var out NativeRecoveryCopy
	err := c.do(ctx, http.MethodGet, "/api/v1/native-recovery-copies/"+esc(id), nil, &out)
	return &out, err
}

func (c *Client) NativeRecoveryCopies(ctx context.Context, project string, page NativeCopyPage) (*NativeRecoveryCopies, error) {
	query, err := nativeRecoveryQuery(page)
	if err != nil {
		return nil, err
	}
	var out NativeRecoveryCopies
	err = c.do(ctx, http.MethodGet, "/api/v1/projects/"+esc(project)+"/native-recovery-copies"+query, nil, &out)
	return &out, err
}

func (c *Client) CleanupNativeRecoveryCopy(ctx context.Context, id string) (*NativeRecoveryCopy, error) {
	var out NativeRecoveryCopy
	err := c.do(ctx, http.MethodDelete, "/api/v1/native-recovery-copies/"+esc(id), nil, &out)
	return &out, err
}

// WaitNativeRecoveryCopy returns the last retained status if observation fails.
func (c *Client) WaitNativeRecoveryCopy(ctx context.Context, id string) (*NativeRecoveryCopy, error) {
	var last *NativeRecoveryCopy
	return wait(ctx, func() (*NativeRecoveryCopy, bool, error) {
		current, err := c.NativeRecoveryCopy(ctx, id)
		if err != nil {
			return last, false, err
		}
		last = current
		switch current.Status {
		case "succeeded", "failed", "cleanup_failed", "deleted":
			return current, true, nil
		default:
			return current, false, nil
		}
	})
}
