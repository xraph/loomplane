package projects

import "context"

// Store persists projects and environments. Every method is scoped to an
// organization: a project in another organization reads as not found.
type Store interface {
	CreateProject(ctx context.Context, p *Project) error
	GetProject(ctx context.Context, orgID, id string) (*Project, error)
	ListProjects(ctx context.Context, orgID string) ([]*Project, error)
	UpdateProject(ctx context.Context, p *Project) error
	DeleteProject(ctx context.Context, orgID, id string) error

	// EnsureEnvironment creates the environment unless the project already
	// has one by that name. It's idempotent, which is what lets the
	// initialization job run again after a crash.
	EnsureEnvironment(ctx context.Context, e *Environment) error
	ListEnvironments(ctx context.Context, orgID, projectID string) ([]*Environment, error)
}
