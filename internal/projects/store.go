package projects

import (
	"context"
	"time"
)

// Store persists projects and environments. Every method is scoped to an
// organization: a project in another organization reads as not found.
type Store interface {
	// CreateProject inserts the project and its environments together, so
	// no project ever exists without an environment.
	CreateProject(ctx context.Context, p *Project, envs []*Environment) error
	GetProject(ctx context.Context, orgID, id string) (*Project, error)
	ListProjects(ctx context.Context, orgID string) ([]*Project, error)
	UpdateProject(ctx context.Context, p *Project) error
	// UpdateExclusions replaces the project's exclusions with what change
	// returns for the current ones, holding the row so two changes can't
	// lose each other. An error from change aborts and is returned.
	UpdateExclusions(ctx context.Context, orgID, id string, change func([]Exclusion) ([]Exclusion, error)) (*Project, error)
	DeleteProject(ctx context.Context, orgID, id string) error
	// SetAutonomy writes the fields of in that aren't nil, and updated_at,
	// in one statement, and returns the project with the autonomy that
	// statement wrote, and the autonomy it replaced, both read under its
	// row lock.
	// UpdateProject never writes them.
	SetAutonomy(ctx context.Context, orgID, id string, in AutonomyInput, at time.Time) (*Project, Autonomy, error)
	// SetResponsibleTeam names the team responsible for the project, or
	// clears it for "". A team outside orgID is ErrTeamNotFound; a missing
	// project is ErrNotFound.
	SetResponsibleTeam(ctx context.Context, orgID, id, teamID string) (*Project, error)

	// CreateEnvironment adds an environment. A name the project already
	// has is ErrEnvironmentExists; a missing project is ErrNotFound.
	CreateEnvironment(ctx context.Context, e *Environment) error
	GetEnvironment(ctx context.Context, orgID, projectID, id string) (*Environment, error)
	ListEnvironments(ctx context.Context, orgID, projectID string) ([]*Environment, error)
	// DeleteEnvironment removes an environment, unless it's the project's
	// last: that's ErrLastEnvironment. Two deletes racing for the last two
	// can't both win.
	DeleteEnvironment(ctx context.Context, orgID, projectID, id string) error
}
