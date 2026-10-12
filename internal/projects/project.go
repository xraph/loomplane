// Package projects is Loomplane's project and environment model: what an
// organization deploys, and the environments it deploys into.
//
// It knows nothing of HTTP or Forge. The service checks every action
// against the caller's role (internal/security) itself, so the API, the
// CLI and future agents all go through the same checks.
package projects

import (
	"errors"
	"time"
)

// Project statuses. A project is initializing until its environments
// exist; a Dispatch job does that (internal/orchestration).
const (
	StatusInitializing = "initializing"
	StatusReady        = "ready"
)

// Environment kinds.
const (
	KindProduction = "production"
	KindPreview    = "preview"
)

// Errors the service and stores return.
var (
	ErrNotFound  = errors.New("project not found")
	ErrSlugTaken = errors.New("a project with that slug already exists in this organization")
	ErrInvalid   = errors.New("invalid project")
)

// Project is an application an organization deploys with Loomplane.
//
// The forge:"id" tags mark each type's identity for the dashboard's
// generated client, which keeps one normalized copy per ID. A tag, not a
// Forge interface, so this package stays free of Forge.
type Project struct {
	ID            string    `json:"id" forge:"id"`
	OrgID         string    `json:"org_id"`
	Name          string    `json:"name"`
	Slug          string    `json:"slug"`
	Description   string    `json:"description"`
	RepositoryURL string    `json:"repository_url"`
	Status        string    `json:"status"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Environment is a place a project deploys to, such as production.
type Environment struct {
	ID        string    `json:"id" forge:"id"`
	OrgID     string    `json:"org_id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DefaultEnvironments are the environments every new project starts with.
func DefaultEnvironments() []Environment {
	return []Environment{
		{Name: "production", Kind: KindProduction},
		{Name: "preview", Kind: KindPreview},
	}
}
