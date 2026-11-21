package cloudprofiles

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

// ErrBusy identifies an environment or target with unfinished release work.
var ErrBusy = errors.New("environment has unfinished deployment or Cloud sync work")

// ChangeInput selects exactly one change: a target's placement (TargetID), a
// Cloud's default placement (Default), or the project's default Cloud
// (ProjectDefault). Revision is the target revision, the Cloud revision for a
// default placement switch, or the current default Cloud's grant revision for
// a project default change (zero when the project has none). A default
// placement switch with no provider clears it; a project default change with
// no CloudID leaves the project without a default Cloud.
type ChangeInput struct {
	Runtime        string `json:"runtime,omitempty"`
	PoolID         string `json:"pool_id,omitempty"`
	Default        bool   `json:"default,omitempty"`
	ProjectDefault bool   `json:"project_default,omitempty"`

	Revision     int64  `json:"revision"`
	CloudID      string `json:"cloud_id"`
	TargetID     string `json:"target_id,omitempty"`
	Provider     string `json:"provider,omitempty"`
	ConnectionID string `json:"connection_id,omitempty"`
	Region       string `json:"region,omitempty"`
}

// EnvironmentChange describes the reviewed work in one environment.
type EnvironmentChange struct {
	CompensationMode    string      `json:"compensation_mode,omitempty"`
	CompensationMessage string      `json:"compensation_message,omitempty"`
	ProjectID           string      `json:"project_id,omitempty"`
	EnvironmentID       string      `json:"environment_id"`
	Environment         string      `json:"environment"`
	Services            []string    `json:"services"`
	Summary             PlanSummary `json:"summary"`
	PlanID              string      `json:"plan_id,omitempty"`
	DeploymentID        string      `json:"deployment_id,omitempty"`
	Status              string      `json:"status"`
	Error               string      `json:"error,omitempty"`
	ExternalRouting     bool        `json:"external_routing"`
	RoutingEvidence     string      `json:"routing_evidence,omitempty"`
	RoutingStatus       string      `json:"routing_status,omitempty"`
	Routes              []SyncRoute `json:"routes,omitempty"`
}

// SyncRoute identifies the durable handoff and the DNS record you need to serve.
type SyncRoute struct {
	RouteID    string    `json:"route_id"`
	PrepareID  string    `json:"prepare_id"`
	CutoverID  string    `json:"cutover_id,omitempty"`
	ReleaseID  string    `json:"release_id,omitempty"`
	Status     string    `json:"status"`
	Message    string    `json:"message,omitempty"`
	Hostname   string    `json:"hostname"`
	RecordType string    `json:"record_type,omitempty"`
	Value      string    `json:"value,omitempty"`
	TTL        int       `json:"ttl,omitempty"`
	NotBefore  time.Time `json:"not_before"`
}

// PlanSummary counts the reviewed provider changes and metadata adoptions.
type PlanSummary struct {
	Create  int `json:"create"`
	Update  int `json:"update"`
	Replace int `json:"replace"`
	Delete  int `json:"delete"`
	Keep    int `json:"keep"`
	Adopt   int `json:"adopt"`
}

// Preview is immutable. Snapshot references and actor identity remain server-side.
type Preview struct {
	ID            string            `json:"id" forge:"id"`
	OrgID         string            `json:"org_id"`
	ProjectID     string            `json:"project_id"`
	Revision      string            `json:"revision"`
	Input         ChangeInput       `json:"input"`
	Before        *Target           `json:"before,omitempty"`
	After         *Target           `json:"after,omitempty"`
	BeforeDefault *DefaultPlacement `json:"before_default,omitempty"`
	AfterDefault  *DefaultPlacement `json:"after_default,omitempty"`
	// BeforeCloud and AfterCloud are the project's default Cloud either side
	// of a project default change.
	BeforeCloud  *ProjectCloud       `json:"before_cloud,omitempty"`
	AfterCloud   *ProjectCloud       `json:"after_cloud,omitempty"`
	Environments []EnvironmentChange `json:"environments"`
	Blockers     []string            `json:"blockers"`
	Reverts      string              `json:"reverts,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
}

// Sync statuses retain guards until cleanup or compensation finishes.
const (
	SyncPending   = "pending"
	SyncRunning   = "running"
	SyncFailed    = "failed"
	SyncRouting   = "routing_pending"
	SyncCleanup   = "cleanup_pending"
	SyncSucceeded = "succeeded"
	SyncReverted  = "reverted"
	SyncReverting = "reverting"
)

// Sync tracks deployment and routing checkpoints across all affected environments.
type Sync struct {
	ID        string             `json:"id" forge:"id"`
	OrgID     string             `json:"org_id"`
	ProjectID string             `json:"project_id"`
	CloudID   string             `json:"cloud_id"`
	TargetID  string             `json:"target_id,omitempty"`
	Default   bool               `json:"default,omitempty"`
	PreviewID string             `json:"preview_id"`
	Status    string             `json:"status"`
	CreatedBy string             `json:"created_by"`
	Actor     security.Principal `json:"-"`
	Before    *Target            `json:"before,omitempty"`
	After     *Target            `json:"after,omitempty"`
	// BeforeDefault and AfterDefault record a reviewed default switch.
	BeforeDefault *DefaultPlacement   `json:"before_default,omitempty"`
	AfterDefault  *DefaultPlacement   `json:"after_default,omitempty"`
	Environments  []EnvironmentChange `json:"environments"`
	Attempt       int                 `json:"attempt"`
	Version       int64               `json:"-"`
	Error         string              `json:"error,omitempty"`
	Reverts       string              `json:"reverts,omitempty"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

// Snapshot is stored separately from API JSON. It contains references, never
// plaintext variables or credentials. Plans hold per-binding applied inputs.
type Snapshot struct {
	Shared               bool                  `json:"shared,omitempty"`
	Grants               []ProjectGrant        `json:"grants,omitempty"`
	ProfileRevisions     map[string]int64      `json:"profile_revisions,omitempty"`
	TargetRevisions      map[string]int64      `json:"target_revisions,omitempty"`
	ConnectionIdentities map[string]string     `json:"connection_identities,omitempty"`
	Actor                security.Principal    `json:"actor"`
	Environments         []EnvironmentSnapshot `json:"environments"`
	Connections          []string              `json:"connections"`
}

// AcceptedPlan is the exact plan materialized by reviewed sync acceptance.
type AcceptedPlan struct {
	ID               string          `json:"id"`
	Revision         string          `json:"revision"`
	ManifestID       string          `json:"manifest_id"`
	ManifestRevision int             `json:"manifest_revision"`
	Result           json.RawMessage `json:"result"`
}

// EnvironmentSnapshot compares the complete serving set during acceptance.
type EnvironmentSnapshot struct {
	LocationReviews []*LocationReview `json:"location_reviews,omitempty"`
	Routes          json.RawMessage   `json:"routes,omitempty"`
	AcceptedPlan    *AcceptedPlan     `json:"accepted_plan,omitempty"`

	ProjectID     string `json:"project_id,omitempty"`
	EnvironmentID string `json:"environment_id"`
	// DefaultCloud is the project's default Cloud as reviewed, "" for none.
	// Acceptance refuses the review once it has changed.
	DefaultCloud string          `json:"default_cloud"`
	Bindings     json.RawMessage `json:"bindings"`
	Plan         json.RawMessage `json:"plan"`
}

// Shared reports whether a change reaches every environment using the Cloud,
// in every project, rather than one project's default Cloud.
func (in ChangeInput) Shared() bool { return in.TargetID != "" || in.Default }

// ParticipantProject preserves the original scope of legacy operation snapshots.
func ParticipantProject(project, original string) string {
	if project != "" {
		return project
	}
	return original
}
