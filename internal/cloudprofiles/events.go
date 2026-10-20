package cloudprofiles

import "time"

// CloudSyncEvent is a status notification. Fetch the authorized sync for its
// full review; internal actors and retained input references stay in storage.
type CloudSyncEvent struct {
	ID           string              `json:"id"`
	OrgID        string              `json:"org_id"`
	ProjectID    string              `json:"project_id"`
	CloudID      string              `json:"cloud_id"`
	TargetID     string              `json:"target_id"`
	Status       string              `json:"status"`
	Attempt      int                 `json:"attempt"`
	Environments []EnvironmentChange `json:"environments"`
	Reverts      string              `json:"reverts,omitempty"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

// EnvironmentCloudEvent invalidates the environment's nullable assignment.
type EnvironmentCloudEvent struct {
	OrgID         string  `json:"org_id"`
	ProjectID     string  `json:"project_id"`
	EnvironmentID string  `json:"environment_id"`
	CloudID       *string `json:"cloud_id" nullable:"true" required:"true"`
	Revision      int64   `json:"revision"`
}
