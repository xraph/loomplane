// Package cloudprofiles keeps organization Clouds and their named deployment targets.
package cloudprofiles

import (
	"context"
	"errors"
	"time"
)

// Errors returned by scoped Cloud operations.
var (
	ErrNotFound = errors.New("cloud or target not found")
	ErrInvalid  = errors.New("invalid cloud configuration")
	ErrExists   = errors.New("a cloud or target with that name already exists")
	ErrConflict = errors.New("cloud configuration changed; reload and try again")
	ErrInUse    = errors.New("this cloud or target is assigned to an environment")
)

// Profile is a named set of targets owned by one organization. ProjectID records creation provenance.
// DefaultPlacement is the one provider and account a Cloud uses for services
// that name no target or tag. A Cloud without targets therefore holds exactly
// one provider; each target added later brings its own.
type Profile struct {
	ID               string            `json:"id" forge:"id"`
	OrgID            string            `json:"org_id"`
	ProjectID        string            `json:"project_id"`
	Name             string            `json:"name"`
	DefaultPlacement *DefaultPlacement `json:"default_placement" nullable:"true" required:"true"`
	Revision         int64             `json:"revision"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

// DefaultPlacement is a single provider, verified account and optional region.
type DefaultPlacement struct {
	Provider     string `json:"provider"`
	ConnectionID string `json:"connection_id"`
	Region       string `json:"region"`
}

// Target is one immutable slug and its desired deployment placement.
type Target struct {
	Runtime      string `json:"runtime"`
	PoolID       string `json:"pool_id,omitempty"`
	ID           string `json:"id" forge:"id"`
	OrgID        string `json:"org_id"`
	ProjectID    string `json:"project_id"`
	CloudID      string `json:"cloud_id"`
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	Provider     string `json:"provider"`
	ConnectionID string `json:"connection_id"`
	Region       string `json:"region"`
	// Tags let a manifest select this target by what it is rather than its name.
	Tags      []string  `json:"tags"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Assignment resolves an environment's desired Cloud and all of its targets.
type Assignment struct {
	GrantRevision int64    `json:"grant_revision"`
	CloudRevision int64    `json:"cloud_revision"`
	OrgID         string   `json:"org_id"`
	ProjectID     string   `json:"project_id"`
	EnvironmentID string   `json:"environment_id"`
	CloudID       string   `json:"cloud_id"`
	Revision      int64    `json:"revision"`
	Targets       []Target `json:"targets"`
	// DefaultPlacement is the Cloud's default when the project's grant covers
	// the whole Cloud. A grant limited to chosen targets never reaches it.
	DefaultPlacement *DefaultPlacement `json:"default_placement,omitempty"`
}

// Resolver reads placement without changing it. A valid unassigned environment
// returns nil; an environment outside the requested scope returns ErrNotFound.
type Resolver interface {
	Resolve(ctx context.Context, orgID, projectID, environmentID string) (*Assignment, error)
}
