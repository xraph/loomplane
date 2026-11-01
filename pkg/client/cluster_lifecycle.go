package client

import (
	"context"
	"net/http"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

// MoneyEstimate uses decimal integer strings in micro-USD so clients retain every unit.
type MoneyEstimate struct {
	HourlyMicroUSD    string           `json:"hourly_micro_usd"`
	HorizonMicroUSD   string           `json:"horizon_micro_usd"`
	HorizonSeconds    int64            `json:"horizon_seconds"`
	Known             bool             `json:"known"`
	MissingComponents []string         `json:"missing_components"`
	PriceDigest       string           `json:"price_digest"`
	CheckedAt         time.Time        `json:"checked_at"`
	Components        []MoneyComponent `json:"components"`
}
type MoneyComponent struct {
	Name           string     `json:"name"`
	Category       string     `json:"category"`
	Source         string     `json:"source"`
	Quantity       string     `json:"quantity"`
	HourlyMicroUSD *string    `json:"hourly_micro_usd"`
	PriceCheckedAt *time.Time `json:"price_checked_at,omitempty"`
}
type CapacityBudget struct {
	MaxHourlyMicroUSD  string `json:"max_hourly_micro_usd"`
	MaxHorizonMicroUSD string `json:"max_horizon_micro_usd"`
	HorizonSeconds     int64  `json:"horizon_seconds"`
}
type BudgetReview struct {
	CloudID        string        `json:"cloud_id"`
	PolicyRevision int64         `json:"policy_revision"`
	Estimate       MoneyEstimate `json:"estimate"`
	Digest         string        `json:"digest"`
}

// ClusterScalingInput selects one controller. Revisions and authority are assigned by the server.
type ClusterScalingInput struct {
	ExpectedRevision int64  `json:"expected_revision"`
	PoolID           string `json:"pool_id"`
	Mode             string `json:"mode"`
	MinNodes         int    `json:"min_nodes"`
	MaxNodes         int    `json:"max_nodes"`
	CooldownSeconds  int64  `json:"cooldown_seconds"`
	ContinuityPoolID string `json:"continuity_pool_id,omitempty"`
	ContinuityFloor  int    `json:"continuity_floor"`
}
type ClusterScalingPolicy struct {
	ClusterScalingInput
	Revision    int64     `json:"revision"`
	PauseReason string    `json:"pause_reason,omitempty"`
	Revoked     bool      `json:"revoked"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type ClusterMaintenanceWindow struct {
	Timezone        string    `json:"timezone"`
	LocalStart      string    `json:"local_start"`
	DurationSeconds int64     `json:"duration_seconds"`
	StartUTC        time.Time `json:"start_utc"`
	EndUTC          time.Time `json:"end_utc"`
}
type ClusterMaintenanceInput struct {
	ObservationDigest string                    `json:"observation_digest"`
	PoolID            string                    `json:"pool_id,omitempty"`
	ObservedRevision  int64                     `json:"observed_revision"`
	Pool              *NodePoolSpec             `json:"pool,omitempty"`
	Update            *provider.ClusterUpdate   `json:"update,omitempty"`
	Version           string                    `json:"version,omitempty"`
	DrainDigest       string                    `json:"drain_digest,omitempty"`
	Window            *ClusterMaintenanceWindow `json:"window,omitempty"`
}
type ClusterRetirementInput struct {
	ReviewDigest               string `json:"review_digest"`
	AttachmentID               string `json:"attachment_id,omitempty"`
	ExpectedAttachmentRevision int64  `json:"expected_attachment_revision,omitempty"`
}
type RetainedResource struct {
	ID                      string  `json:"id"`
	Kind                    string  `json:"kind"`
	Ownership               string  `json:"ownership"`
	SurvivesClusterDeletion bool    `json:"survives_cluster_deletion"`
	EstimatedHourlyMicroUSD *string `json:"estimated_hourly_micro_usd,omitempty"`
}
type ClusterRetirementReview struct {
	ResourceID                       string             `json:"resource_id"`
	Blockers                         []string           `json:"blockers"`
	DeletionCandidates               []RetainedResource `json:"deletion_candidates"`
	RetainedResources                []RetainedResource `json:"retained_resources"`
	EstimatedRemainingHourlyMicroUSD string             `json:"estimated_remaining_hourly_micro_usd"`
	UnknownCosts                     []string           `json:"unknown_costs"`
	CheckedAt                        time.Time          `json:"checked_at"`
	Digest                           string             `json:"digest"`
}
type ClusterEnvelopeReview struct {
	Automatic bool                        `json:"automatic"`
	Pools     []ClusterPoolEnvelopeReview `json:"pools"`
}
type ClusterEnvelopeResources ResourceVector

type ClusterPoolEnvelopeReview struct {
	PoolID   string                   `json:"pool_id"`
	Maximum  ClusterEnvelopeResources `json:"maximum"`
	Initial  ClusterEnvelopeResources `json:"initial"`
	Baseline ClusterEnvelopeResources `json:"baseline"`
	Money    MoneyEstimate            `json:"money"`
}

func (c *Client) PreviewCloudClusterRetirement(ctx context.Context, cloud, id string) (*ClusterRetirementReview, error) {
	var out ClusterRetirementReview
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/clusters/"+esc(id)+"/retirement/preview", nil, &out)
	return &out, err
}

type RuntimeAccessReview struct {
	AttachmentID       string                        `json:"attachment_id"`
	AttachmentRevision int64                         `json:"attachment_revision,omitempty"`
	CapabilityRevision int64                         `json:"capability_revision,omitempty"`
	ProjectID          string                        `json:"project_id,omitempty"`
	Namespace          string                        `json:"namespace,omitempty"`
	Profile            provider.RuntimeAccessProfile `json:"profile"`
}

type ClusterRetirementPreviewInput struct {
	AttachmentID               string `json:"attachment_id,omitempty"`
	ExpectedAttachmentRevision int64  `json:"expected_attachment_revision,omitempty"`
}

func (c *Client) PreviewCloudClusterDetach(ctx context.Context, cloud, id, attachment string, revision int64) (*ClusterRetirementReview, error) {
	var out ClusterRetirementReview
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/clusters/"+esc(id)+"/retirement/preview", ClusterRetirementPreviewInput{AttachmentID: attachment, ExpectedAttachmentRevision: revision}, &out)
	return &out, err
}
