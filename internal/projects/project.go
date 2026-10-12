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

// Project statuses. A project is initializing until the background job
// that sets it up has run (internal/orchestration). It has its
// environments from the moment it exists.
const (
	StatusInitializing = "initializing"
	StatusReady        = "ready"
)

// Environment kinds. The kind says what an environment is for, which
// deployment strategies (milestone 4) go by; the name is free.
const (
	KindProduction  = "production"
	KindStaging     = "staging"
	KindPreview     = "preview"
	KindDevelopment = "development"
)

// Kinds lists the environment kinds, most important first.
func Kinds() []string { return []string{KindProduction, KindStaging, KindPreview, KindDevelopment} }

// Errors the service and stores return.
var (
	ErrNotFound            = errors.New("project not found")
	ErrSlugTaken           = errors.New("a project with that slug already exists in this organization")
	ErrInvalid             = errors.New("invalid project")
	ErrEnvironmentNotFound = errors.New("environment not found")
	ErrEnvironmentExists   = errors.New("this project already has an environment with that name")
	ErrLastEnvironment     = errors.New("a project needs at least one environment, so its last one can't be deleted")
	ErrRepository          = errors.New("the repository couldn't be attached")
)

// Project is an application an organization deploys with Loomplane. Its
// Icon and Color draw its tile in the dashboard; style.go lists the names
// each can take.
//
// The forge:"id" tags mark each type's identity for the dashboard's
// generated client, which keeps one normalized copy per ID. A tag, not a
// Forge interface, so this package stays free of Forge.
type Project struct {
	ID          string `json:"id" forge:"id"`
	OrgID       string `json:"org_id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Color       string `json:"color"`
	// Repositories are the "owner/name" of each repository the project
	// builds from, each once, by its first alias. Its sources belong to discovery; the store
	// reads them so a list of projects can show them.
	Repositories []string  `json:"repositories"`
	Status       string    `json:"status"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
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
