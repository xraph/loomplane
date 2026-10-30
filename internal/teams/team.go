// Package teams keeps a workspace's teams: named groups of members that
// carry a role on the whole workspace or on one project, and that a
// project can name as responsible.
//
// Teams live in Loomplane's own tables, never authsome's. A team's grants
// reach Warden as ordinary per-person assignments, which only
// security.Authorizer.Sync writes. A change that moves anyone's rights
// queues each affected person in loomplane_authz_sync in the same
// transaction as the change, and syncs them once it has committed; the
// teams.sync job drains whatever a failed or interrupted sync left behind.
//
// A membership row counts only while the authsome membership it was added
// for still exists: every read that turns one into rights, notices or a
// listed member joins authsome_members on the member ID the row recorded.
// A person who leaves and is invited back gets a new member ID, so a row
// that survived their leaving matches nothing.
//
// Two rules keep the sync lock from starving the connection pool, and
// every caller here keeps them:
//
//   - Nothing calls Sync, TrySync, Assign or Require (whose repair syncs)
//     inside a database transaction or while holding a pooled connection,
//     open rows included. Commit or close first, then sync.
//   - Syncs never nest. Grants (Sync's TeamGrants) and Leave (the member
//     removal's Leaver) run inside a sync or a removal, and never sync
//     anyone themselves.
package teams

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/security"
)

// Team is a named group of workspace members.
type Team struct {
	ID          string    `json:"id" forge:"id"` // typeid "team_..."
	OrgID       string    `json:"org_id"`
	Name        string    `json:"name"`        // 1 to 80 characters
	Slug        string    `json:"slug"`        // from the name, unique in the workspace; what the agent names
	Description string    `json:"description"` // up to 500 characters; the agent reads it to pick a team
	Members     []Member  `json:"members"`
	Grants      []Grant   `json:"grants"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Member is a current workspace member in a team.
type Member struct {
	UserID  string    `json:"user_id"`
	Email   string    `json:"email"`
	Name    string    `json:"name"`
	AddedAt time.Time `json:"added_at"`
}

// Grant is the role a team gives its members on one scope.
type Grant struct {
	ID        string `json:"id"`
	Role      string `json:"role"`       // viewer, developer or admin
	ProjectID string `json:"project_id"` // "" for the whole workspace
}

// TeamList is every team of a workspace.
type TeamList struct {
	Teams []Team `json:"teams"`
}

// CreateTeamInput is what a caller sends to create a team.
type CreateTeamInput struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// UpdateTeamInput changes the fields it sets. A new name re-derives the
// slug.
type UpdateTeamInput struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

// SetGrantInput sets a team's role on a project, or on the whole workspace
// when ProjectID is empty. A scope that already has a grant gets the new
// role.
type SetGrantInput struct {
	Role      string `json:"role"`
	ProjectID string `json:"project_id,omitempty"`
}

var (
	// ErrNotFound means no team with that ID in the caller's workspace.
	ErrNotFound = errors.New("team not found")
	// ErrGrantNotFound means the team has no grant with that ID.
	ErrGrantNotFound = errors.New("grant not found")
	// ErrSlugTaken means another team in the workspace has the name's slug.
	ErrSlugTaken = errors.New("a team with that name already exists in this workspace")
	// ErrInvalid means the input breaks a rule; the error says which.
	ErrInvalid = errors.New("invalid team")
	// ErrNotMember means the person isn't a member of the workspace.
	ErrNotMember = errors.New("that person isn't a member of this workspace")
)

// Authorizer is the part of security.Authorizer the service uses.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
	// Sync waits for the person's lock; TrySync gives up at once when
	// it's held. Never call either while holding a connection.
	Sync(ctx context.Context, orgID, userID string) error
	TrySync(ctx context.Context, orgID, userID string) (bool, error)
}

// AuditEvent is one audited team change.
type AuditEvent struct {
	// Action is team.create, team.update, team.delete, team.member.add,
	// team.member.remove, team.grant.set or team.grant.remove.
	Action    string
	TeamID    string
	Principal security.Principal
	// Attributes are user_id, role, project_id, grant_id and fields. Never
	// an email.
	Attributes map[string]any
}

// Auditor records team changes. Recording must not fail the change, so it
// returns nothing; the implementation logs its own failures.
type Auditor interface {
	Record(ctx context.Context, e AuditEvent)
}

// Deps is what the service needs.
type Deps struct {
	DB      *pgdriver.PgDB
	Authz   Authorizer
	Members security.MembershipFunc
	Audit   Auditor                     // nil records nothing
	Log     func(msg string, err error) // sync failures; nil discards
}
