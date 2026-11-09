// Package fleet defines portable instance fleet API contracts.
package fleet

import "time"

// Fleet groups independently deployed instances in one project.
type Fleet struct {
	ID               string    `json:"id" forge:"id"`
	OrgID            string    `json:"org_id"`
	ProjectID        string    `json:"project_id"`
	ControlProjectID string    `json:"control_project_id,omitempty"`
	Revision         int64     `json:"revision"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Stage bounds the placements and number of instances admitted together.
type Stage struct {
	ID              string    `json:"id" forge:"id"`
	FleetID         string    `json:"fleet_id"`
	Name            string    `json:"name"`
	EnvironmentKind string    `json:"environment_kind"`
	AllowedCloudIDs []string  `json:"allowed_cloud_ids"`
	MaxInstances    int       `json:"max_instances"`
	PolicyRevision  int64     `json:"policy_revision"`
	CreatedAt       time.Time `json:"created_at"`
}

// CreateStageInput supplies explicit admission limits; zero admits nothing.
type CreateStageInput struct {
	Name            string   `json:"name"`
	EnvironmentKind string   `json:"environment_kind"`
	AllowedCloudIDs []string `json:"allowed_cloud_ids"`
	MaxInstances    int      `json:"max_instances"`
}

// Instance owns one deployment environment for its entire lifetime.
type Instance struct {
	ID                 string    `json:"id" forge:"id"`
	FleetID            string    `json:"fleet_id"`
	StageID            string    `json:"stage_id"`
	EnvironmentID      string    `json:"environment_id"`
	ExternalRef        string    `json:"external_ref"`
	ProfileID          string    `json:"profile_id"`
	DesiredReleaseID   string    `json:"desired_release_id"`
	ObservedReleaseID  string    `json:"observed_release_id,omitempty"`
	Status             string    `json:"status"`
	Revision           int64     `json:"revision"`
	DesiredGeneration  int64     `json:"desired_generation"`
	ObservedGeneration int64     `json:"observed_generation"`
	OwnershipEpoch     int64     `json:"ownership_epoch"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// ParameterValue carries exactly one typed scalar, validated by its template.
type ParameterValue struct {
	Kind        string  `json:"kind"`
	StringValue *string `json:"string_value,omitempty"`
	IntValue    *int64  `json:"int_value,omitempty"`
	BoolValue   *bool   `json:"bool_value,omitempty"`
}

// SecretRef pins an authorized secret version without exposing its value.
type SecretRef struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

// CreateInstanceInput selects immutable release and profile inputs.
type CreateInstanceInput struct {
	StageID        string                    `json:"stage_id"`
	ReleaseID      string                    `json:"release_id"`
	ProfileID      string                    `json:"profile_id"`
	ExternalRef    string                    `json:"external_ref"`
	IdempotencyKey string                    `json:"idempotency_key"`
	Parameters     map[string]ParameterValue `json:"parameters,omitempty"`
	SecretRefs     map[string]SecretRef      `json:"secret_refs,omitempty"`
}

// Operation describes durable work; provider receipts remain internal.
type Operation = FleetOperation

// FleetOperation has a distinct API schema name.
//
//nolint:revive // Keep the API schema name distinct; Go callers retain the short alias.
type FleetOperation struct {
	ID          string    `json:"id" forge:"id"`
	InstanceID  string    `json:"instance_id"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	RequestHash string    `json:"request_hash"`
	ErrorCode   string    `json:"error_code,omitempty"`
	Generation  int64     `json:"generation"`
	CreatedAt   time.Time `json:"created_at"`
}

// OperationResult returns the stable identities of an accepted request.
type OperationResult struct {
	Instance  Instance  `json:"instance"`
	Operation Operation `json:"operation"`
}
