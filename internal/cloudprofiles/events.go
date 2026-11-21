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

// ProjectCloudEvent says a project's default Cloud changed. CloudID is the
// new default, or null when the project has none.
type ProjectCloudEvent struct {
	OrgID     string  `json:"org_id"`
	ProjectID string  `json:"project_id"`
	CloudID   *string `json:"cloud_id" nullable:"true" required:"true"`
}
