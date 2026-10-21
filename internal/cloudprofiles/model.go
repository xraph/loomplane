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
type Profile struct {
	ID        string    `json:"id" forge:"id"`
	OrgID     string    `json:"org_id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Target is one immutable slug and its desired deployment placement.
type Target struct {
	Runtime      string    `json:"runtime"`
	PoolID       string    `json:"pool_id,omitempty"`
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
}

// Resolver reads placement without changing it. A valid unassigned environment
// returns nil; an environment outside the requested scope returns ErrNotFound.
type Resolver interface {
	Resolve(ctx context.Context, orgID, projectID, environmentID string) (*Assignment, error)
}
