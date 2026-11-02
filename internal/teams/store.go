package teams

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/security"
)

// querier is the database or a transaction on it.
type querier interface {
	Exec(ctx context.Context, query string, args ...any) (driver.Result, error)
	Query(ctx context.Context, query string, args ...any) (driver.Rows, error)
	QueryRow(ctx context.Context, query string, args ...any) driver.Row
}

// currentMember joins a loomplane_team_members row (alias tm) to the
// authsome membership it was added for (alias am). A row whose membership
// is gone, or was replaced by a later invitation, joins nothing. Every read
// that turns a membership row into rights, notices or a listed member uses
// it. It matches on the member ID, never on timestamps: authsome stamps
// created_at with the application's clock and added_at is the database's.
const currentMember = `JOIN authsome_members am ON am.id = tm.member_id AND am.org_id = tm.org_id AND am.user_id = tm.user_id`

// grantInWorkspace keeps a grant (alias g, with its project left-joined as
// p) only when its project, if it has one, is in the grant's workspace.
// The schema can't stop a row that points across workspaces; this makes
// one grant nothing.
const grantInWorkspace = `(g.project_id IS NULL OR p.org_id = g.org_id)`

// Postgres SQLSTATEs the store tells apart.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
)

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func rollback(tx driver.Tx) { _ = tx.Rollback() } //nolint:errcheck // rollback is a no-op after commit

// collect runs query and reads every row with scan (see each).
func collect(ctx context.Context, q querier, query string, args []any, scan func(driver.Rows) error) error {
	rows, err := q.Query(ctx, query, args...)
	return each(rows, err, scan)
}

// each reads every row of rows with scan and closes them, so no caller
// holds a connection after it returns.
func each(rows driver.Rows, err error, scan func(driver.Rows) error) error {
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// loadTeams reads the workspace's teams, or only teamID when it isn't
// empty, with their current members and grants.
func loadTeams(ctx context.Context, q querier, orgID, teamID string) ([]Team, error) {
	var teams []Team
	err := collect(ctx, q, `SELECT id, org_id, name, slug, description, created_at, updated_at
		FROM loomplane_teams WHERE org_id = $1 AND ($2 = '' OR id = $2) ORDER BY name, id`,
		[]any{orgID, teamID}, func(rows driver.Rows) error {
			t := Team{Members: []TeamMember{}, Grants: []TeamGrant{}}
			if scanErr := rows.Scan(&t.ID, &t.OrgID, &t.Name, &t.Slug, &t.Description, &t.CreatedAt, &t.UpdatedAt); scanErr != nil {
				return scanErr
			}
			teams = append(teams, t)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	if len(teams) == 0 {
		return teams, nil
	}
	byID := make(map[string]*Team, len(teams))
	for i := range teams {
		byID[teams[i].ID] = &teams[i]
	}

	err = collect(ctx, q, `SELECT tm.team_id, tm.user_id, u.email, trim(u.first_name || ' ' || u.last_name) AS name, tm.added_at
		FROM loomplane_team_members tm
		JOIN loomplane_teams t ON t.id = tm.team_id AND t.org_id = tm.org_id
		`+currentMember+`
		JOIN authsome_users u ON u.id = tm.user_id
		WHERE t.org_id = $1 AND ($2 = '' OR t.id = $2)
		ORDER BY tm.team_id, name, u.email`,
		[]any{orgID, teamID}, func(rows driver.Rows) error {
			var id string
			var m TeamMember
			if scanErr := rows.Scan(&id, &m.UserID, &m.Email, &m.Name, &m.AddedAt); scanErr != nil {
				return scanErr
			}
			if t := byID[id]; t != nil {
				t.Members = append(t.Members, m)
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("list team members: %w", err)
	}

	err = collect(ctx, q, `SELECT g.team_id, g.id, g.role, coalesce(g.project_id, '')
		FROM loomplane_team_grants g
		JOIN loomplane_teams t ON t.id = g.team_id AND t.org_id = g.org_id
		LEFT JOIN loomplane_projects p ON p.id = g.project_id
		WHERE t.org_id = $1 AND ($2 = '' OR t.id = $2) AND `+grantInWorkspace+`
		ORDER BY g.team_id, g.project_id NULLS FIRST`,
		[]any{orgID, teamID}, func(rows driver.Rows) error {
			var id string
			var g TeamGrant
			if scanErr := rows.Scan(&id, &g.ID, &g.Role, &g.ProjectID); scanErr != nil {
				return scanErr
			}
			if t := byID[id]; t != nil {
				t.Grants = append(t.Grants, g)
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("list team grants: %w", err)
	}
	return teams, nil
}

func insertTeam(ctx context.Context, q querier, t *Team, createdBy string) error {
	err := q.QueryRow(ctx, `INSERT INTO loomplane_teams (id, org_id, name, slug, description, created_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at, updated_at`,
		t.ID, t.OrgID, t.Name, t.Slug, t.Description, createdBy).Scan(&t.CreatedAt, &t.UpdatedAt)
	if pgCode(err) == pgUniqueViolation {
		return ErrSlugTaken
	}
	if err != nil {
		return fmt.Errorf("create team: %w", err)
	}
	return nil
}

// updateTeam sets the fields that aren't nil. It reports whether the team
// exists.
func updateTeam(ctx context.Context, q querier, orgID, id string, name, slug, description *string) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE loomplane_teams
		SET name = coalesce($3, name), slug = coalesce($4, slug), description = coalesce($5, description), updated_at = now()
		WHERE id = $1 AND org_id = $2`, id, orgID, name, slug, description)
	if pgCode(err) == pgUniqueViolation {
		return false, ErrSlugTaken
	}
	if err != nil {
		return false, fmt.Errorf("update team: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// lockTeam locks the team's row until tx ends, so changes to one team's
// members and grants take turns: each one's queue then sees the other's
// committed rows. A team that isn't in orgID is ErrNotFound.
func lockTeam(ctx context.Context, tx querier, orgID, id string) error {
	var got string
	err := tx.QueryRow(ctx, `SELECT id FROM loomplane_teams WHERE id = $1 AND org_id = $2 FOR UPDATE`, id, orgID).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock team: %w", err)
	}
	return nil
}

// teamGrantRoles is the role of each of the team's grants, whatever its
// scope.
func teamGrantRoles(ctx context.Context, q querier, orgID, teamID string) ([]string, error) {
	var roles []string
	err := collect(ctx, q, `SELECT role FROM loomplane_team_grants WHERE team_id = $1 AND org_id = $2`,
		[]any{teamID, orgID}, func(rows driver.Rows) error {
			var role string
			if scanErr := rows.Scan(&role); scanErr != nil {
				return scanErr
			}
			roles = append(roles, role)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("read team grants: %w", err)
	}
	return roles, nil
}

func deleteTeam(ctx context.Context, tx querier, orgID, id string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM loomplane_teams WHERE id = $1 AND org_id = $2`, id, orgID); err != nil {
		return fmt.Errorf("delete team: %w", err)
	}
	return nil
}

// addMember adds the person to the team, recording the authsome member ID
// they hold now. A row already there is refreshed to that member ID, so it
// never names an older membership. It reports false when the person has
// no membership in orgID.
func addMember(ctx context.Context, tx querier, orgID, teamID, userID, addedBy string) (bool, error) {
	res, err := tx.Exec(ctx, `INSERT INTO loomplane_team_members (team_id, org_id, user_id, member_id, added_by)
		SELECT $1, am.org_id, am.user_id, am.id, $4 FROM authsome_members am WHERE am.org_id = $2 AND am.user_id = $3
		ON CONFLICT (team_id, user_id) DO UPDATE SET member_id = EXCLUDED.member_id, added_at = now(), added_by = EXCLUDED.added_by`,
		teamID, orgID, userID, addedBy)
	if err != nil {
		return false, fmt.Errorf("add team member: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func removeMember(ctx context.Context, tx querier, teamID, userID string) (bool, error) {
	res, err := tx.Exec(ctx, `DELETE FROM loomplane_team_members WHERE team_id = $1 AND user_id = $2`, teamID, userID)
	if err != nil {
		return false, fmt.Errorf("remove team member: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// upsertGrant sets the team's role on the scope, keeping the ID of a grant
// already there, and returns the grant's ID. A project that isn't in
// orgID is ErrInvalid.
func upsertGrant(ctx context.Context, tx querier, orgID, teamID, id, role, projectID, createdBy string) (string, error) {
	var got string
	err := tx.QueryRow(ctx, `INSERT INTO loomplane_team_grants (id, team_id, org_id, role, project_id, created_by)
		SELECT $1, $2, $3, $4, NULLIF($5::text, ''), $6
		WHERE $5::text = '' OR EXISTS (SELECT 1 FROM loomplane_projects WHERE id = $5::text AND org_id = $3)
		ON CONFLICT (team_id, (coalesce(project_id, ''))) DO UPDATE SET role = EXCLUDED.role
		RETURNING id`, id, teamID, orgID, role, projectID, createdBy).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) || pgCode(err) == pgForeignKeyViolation {
		return "", errNoSuchProject
	}
	if err != nil {
		return "", fmt.Errorf("set team grant: %w", err)
	}
	return got, nil
}

var errNoSuchProject = fmt.Errorf("%w: no such project in this workspace", ErrInvalid)

// deleteGrant removes one of the team's grants and returns what it was.
func deleteGrant(ctx context.Context, tx querier, orgID, teamID, grantID string) (TeamGrant, error) {
	g := TeamGrant{ID: grantID}
	err := tx.QueryRow(ctx, `DELETE FROM loomplane_team_grants WHERE id = $1 AND team_id = $2 AND org_id = $3
		RETURNING role, coalesce(project_id, '')`, grantID, teamID, orgID).Scan(&g.Role, &g.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrGrantNotFound
	}
	if err != nil {
		return g, fmt.Errorf("remove team grant: %w", err)
	}
	return g, nil
}

// queued is one person in loomplane_authz_sync, as of queuedAt.
type queued struct {
	orgID, userID string
	queuedAt      time.Time
}

// queueUpsert ends every insert into the queue: a person already queued
// gets a new queued_at, so a sync that started before this change can't
// take their row away. An insert of several people orders them by
// (org_id, user_id), so two changes that share people lock their rows in
// the same order and can't deadlock.
const queueUpsert = ` ON CONFLICT (org_id, user_id) DO UPDATE SET queued_at = clock_timestamp()
	RETURNING org_id, user_id, queued_at`

func scanQueued(rows driver.Rows, err error) ([]queued, error) {
	var out []queued
	err = each(rows, err, func(rows driver.Rows) error {
		var q queued
		if scanErr := rows.Scan(&q.orgID, &q.userID, &q.queuedAt); scanErr != nil {
			return scanErr
		}
		out = append(out, q)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("queue role syncs: %w", err)
	}
	return out, nil
}

// queuePerson queues one person.
func queuePerson(ctx context.Context, tx querier, orgID, userID string) ([]queued, error) {
	return scanQueued(tx.Query(ctx, `INSERT INTO loomplane_authz_sync (org_id, user_id) VALUES ($1, $2)`+queueUpsert, orgID, userID))
}

// queueTeam queues every member row of the team, current or not: syncing
// someone who has left only revokes what they still hold.
func queueTeam(ctx context.Context, tx querier, orgID, teamID string) ([]queued, error) {
	return scanQueued(tx.Query(ctx, `INSERT INTO loomplane_authz_sync (org_id, user_id)
		SELECT DISTINCT tm.org_id, tm.user_id FROM loomplane_team_members tm WHERE tm.team_id = $1 AND tm.org_id = $2
		ORDER BY tm.org_id, tm.user_id`+queueUpsert,
		teamID, orgID))
}

// queueProject queues the members of every team with a grant on the
// project.
func queueProject(ctx context.Context, tx querier, orgID, projectID string) ([]queued, error) {
	return scanQueued(tx.Query(ctx, `INSERT INTO loomplane_authz_sync (org_id, user_id)
		SELECT DISTINCT tm.org_id, tm.user_id FROM loomplane_team_grants g
		JOIN loomplane_team_members tm ON tm.team_id = g.team_id AND tm.org_id = g.org_id
		WHERE g.org_id = $1 AND g.project_id = $2
		ORDER BY tm.org_id, tm.user_id`+queueUpsert, orgID, projectID))
}

// dequeue removes the person's row only if nobody queued them again since
// queuedAt: that later change still needs its own sync.
func dequeue(ctx context.Context, q querier, row queued) error {
	_, err := q.Exec(ctx, `DELETE FROM loomplane_authz_sync WHERE org_id = $1 AND user_id = $2 AND queued_at = $3`,
		row.orgID, row.userID, row.queuedAt)
	return err
}

// toBack gives a row a new queued_at, unless someone queued the person
// again since (their row already moved).
func toBack(ctx context.Context, q querier, row queued) error {
	_, err := q.Exec(ctx, `UPDATE loomplane_authz_sync SET queued_at = clock_timestamp()
		WHERE org_id = $1 AND user_id = $2 AND queued_at = $3`, row.orgID, row.userID, row.queuedAt)
	return err
}

// due is up to limit queued people, oldest first.
func due(ctx context.Context, q querier, limit int) ([]queued, error) {
	return scanQueued(q.Query(ctx, `SELECT org_id, user_id, queued_at FROM loomplane_authz_sync
		ORDER BY queued_at, org_id, user_id LIMIT $1`, limit))
}

// grantsOf is every grant the person holds through a team, as a current
// member, workspace first.
func grantsOf(ctx context.Context, q querier, orgID, userID string) ([]security.ScopedRole, error) {
	var out []security.ScopedRole
	err := collect(ctx, q, `SELECT g.role, coalesce(g.project_id, ''), g.team_id
		FROM loomplane_team_members tm
		`+currentMember+`
		JOIN loomplane_teams t ON t.id = tm.team_id AND t.org_id = tm.org_id
		JOIN loomplane_team_grants g ON g.team_id = t.id AND g.org_id = t.org_id
		LEFT JOIN loomplane_projects p ON p.id = g.project_id
		WHERE tm.org_id = $1 AND tm.user_id = $2 AND `+grantInWorkspace+`
		ORDER BY g.project_id NULLS FIRST, g.team_id`,
		[]any{orgID, userID}, func(rows driver.Rows) error {
			var r security.ScopedRole
			if scanErr := rows.Scan(&r.Role, &r.ProjectID, &r.TeamID); scanErr != nil {
				return scanErr
			}
			out = append(out, r)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("list team grants of %s: %w", userID, err)
	}
	return out, nil
}
