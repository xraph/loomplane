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

// Project kinds. An app project builds and deploys from its repositories.
// A service project is the one a catalog service, such as a database,
// gets of its own: it has no repository, only the service. The kind is set
// when the project is created and never changes, so a project's icon,
// which anyone can pick, doesn't decide what a project is.
const (
	ProjectKindApp     = "app"
	ProjectKindService = "service"
)

// Errors the service and stores return.
var (
	ErrFleetManaged        = errors.New("manage this project or environment through its instance fleet")
	ErrNotFound            = errors.New("project not found")
	ErrSlugTaken           = errors.New("a project with that slug already exists in this organization")
	ErrInvalid             = errors.New("invalid project")
	ErrEnvironmentNotFound = errors.New("environment not found")
	ErrEnvironmentExists   = errors.New("this project already has an environment with that name")
	ErrLastEnvironment     = errors.New("a project needs at least one environment, so its last one can't be deleted")
	ErrRepository          = errors.New("the repository couldn't be attached")
	ErrTeamNotFound        = errors.New("that team isn't in this workspace")
	ErrHistoryRetained     = errors.New("a deployment of this project has cleanup history that has to be kept, so it can't be deleted: finish or cancel the cleanup of its abandoned deployments first, or keep the project")
	// ErrFrontendsHeld is a deletion refused because a frontend it
	// deployed may still run in a provider, and still counts against its
	// cloud's frontend capacity until Loomplane confirms it's gone.
	ErrFrontendsHeld = errors.New("a frontend this deployed may still run on its provider and counts against your cloud's frontend capacity, so it can't be deleted until Loomplane confirms the frontend is gone")
)

// Project is an application an organization deploys with Loomplane. Its
// Icon and Color draw its tile in the dashboard; style.go lists the names
// each can take.
//
// The forge:"id" tags mark each type's identity for the dashboard's
// generated client, which keeps one normalized copy per ID. A tag, not a
// Forge interface, so this package stays free of Forge.
type Project struct {
	DeploymentMode string `json:"deployment_mode"`
	ID             string `json:"id" forge:"id"`
	OrgID          string `json:"org_id"`
	Name           string `json:"name"`
	Slug           string `json:"slug"`
	Description    string `json:"description"`
	Icon           string `json:"icon"`
	Color          string `json:"color"`
	// Kind is ProjectKindApp or ProjectKindService. It's read-only: the
	// project is created as one or the other, and stays it.
	Kind string `json:"kind" enum:"app,service"`
	// Repositories are the "owner/name" of each repository the project
	// builds from, each once, by its first alias. Its sources belong to discovery; the store
	// reads them so a list of projects can show them.
	Repositories []string `json:"repositories"`
	// Exclusions keep analysis-found services out of the graph, plans
	// and deploys. See Exclusion.
	Exclusions []Exclusion `json:"exclusions"`
	// AutonomousPromotion lets the AI agent promote a release it was asked
	// for to production without asking a person first, once the release
	// has passed every other check. Every project starts at "ask first"
	// (false), and only a person who may approve deployments can change
	// it (Service.SetAutonomy). It never covers continuous delivery, and
	// never a change that deletes or replaces production data or
	// resources: that always needs a person, whatever this says. The JSON
	// names are the column names, so realtime project events agree with
	// the API.
	AutonomousPromotion bool `json:"autonomous_promotion"`
	// MinStagingSoakSeconds is how long a release must have run in staging
	// before the agent promotes it alone, in whole minutes up to a week.
	// 0 asks for no minimum beyond the agent's own rule.
	MinStagingSoakSeconds int64 `json:"min_staging_soak_seconds"`
	// ResponsibleTeamID is the team that answers for the project: the
	// agent sends it the project's requests, and its members hear about
	// plans waiting there. Empty is none, and workspace admins hear
	// instead. Deleting the team clears it.
	ResponsibleTeamID string    `json:"responsible_team_id"`
	Status            string    `json:"status"`
	CreatedBy         string    `json:"created_by"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Environment is a place a project deploys to, such as production.
type Environment struct {
	InstanceID string    `json:"instance_id,omitempty"`
	ID         string    `json:"id" forge:"id"`
	OrgID      string    `json:"org_id"`
	ProjectID  string    `json:"project_id"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// DefaultEnvironments are the environments every new project starts with.
func DefaultEnvironments() []Environment {
	return []Environment{
		{Name: "production", Kind: KindProduction},
		{Name: "preview", Kind: KindPreview},
	}
}
