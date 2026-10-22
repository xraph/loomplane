package cloudprofiles

import "context"

// Store persists organization configuration. A nonempty project selects its
// granted view; an empty project selects the organization manager view. Callers
// must authorize that view. Updates compare the previous revision atomically.
// Reviewed placement and assignment writes belong to the sync workflow.
type Store interface {
	CreateProfile(context.Context, *Profile) error
	GetProfile(ctx context.Context, orgID, projectID, id string) (*Profile, error)
	ListProfiles(ctx context.Context, orgID, projectID string) ([]*Profile, error)
	UpdateProfile(ctx context.Context, p *Profile, previousRevision int64) error
	DeleteProfile(ctx context.Context, orgID, projectID, id string) error
	CreateTarget(context.Context, *Target) error
	GetTarget(ctx context.Context, orgID, projectID, cloudID, id string) (*Target, error)
	ListTargets(ctx context.Context, orgID, projectID, cloudID string) ([]*Target, error)
	UpdateTarget(ctx context.Context, t *Target, previousRevision int64) error
	DeleteTarget(ctx context.Context, orgID, projectID, cloudID, id string) error
}
