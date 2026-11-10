package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
)

// pgUniqueViolation is Postgres's SQLSTATE for a unique constraint failure.
const pgUniqueViolation = "23505"

// pgForeignKeyViolation is Postgres's SQLSTATE for a foreign key failure.
const pgForeignKeyViolation = "23503"

// pgCheckViolation is Postgres's SQLSTATE for a check failure, which the
// cleanup history guards raise to refuse a delete.
const pgCheckViolation = "23514"

// pgRestrictViolation is Postgres's SQLSTATE for an ON DELETE RESTRICT
// key, which it raises at once rather than at the end of the statement.
const pgRestrictViolation = "23001"

// PgStore is the Postgres Store, over the grove pgdriver pool the grove
// extension owns. The schema is migration group "loomplane"
// (database/migrations).
type PgStore struct {
	db *pgdriver.PgDB
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

const projectColumns = `id, org_id, name, slug, description, icon, color, kind, status, created_by, created_at, updated_at`

// projectSelect reads a project with the repositories its live sources
// name, each once even when several directories of it are attached. The sources table is discovery's; reading it here saves the
// dashboard a request per project.
const projectSelect = `SELECT p.id, p.org_id, p.name, p.slug, p.description, p.icon, p.color, p.kind, p.status,
	p.created_by, p.created_at, p.updated_at, p.exclusions, p.deployment_mode, coalesce(p.responsible_team_id, ''),
	p.autonomous_promotion, p.min_staging_soak_seconds,
	COALESCE((SELECT array_agg(r.repository ORDER BY r.first_alias)
		FROM (SELECT s.repository, min(s.alias) AS first_alias FROM loomplane_project_sources s
			WHERE s.project_id = p.id AND s.removed_at IS NULL GROUP BY s.repository) r), '{}')
	FROM loomplane_projects p`

func scanProject(row pgx.Row) (*Project, error) {
	var p Project
	var exclusions []byte
	if err := row.Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.Description, &p.Icon, &p.Color, &p.Kind, &p.Status,
		&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &exclusions, &p.DeploymentMode, &p.ResponsibleTeamID,
		&p.AutonomousPromotion, &p.MinStagingSoakSeconds, &p.Repositories); err != nil {
		return nil, err
	}
	if len(exclusions) > 0 {
		if err := json.Unmarshal(exclusions, &p.Exclusions); err != nil {
			return nil, fmt.Errorf("decode exclusions: %w", err)
		}
	}
	p.normalize()
	return &p, nil
}

// CreateProject inserts p and its environments in one transaction. A slug
// already used in the organization is ErrSlugTaken.
func (s *PgStore) CreateProject(ctx context.Context, p *Project, envs []*Environment) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { err = rollback(tx, err) }()
	if p.Kind == "" {
		p.Kind = ProjectKindApp
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_projects (`+projectColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		p.ID, p.OrgID, p.Name, p.Slug, p.Description, p.Icon, p.Color, p.Kind, p.Status, p.CreatedBy, p.CreatedAt, p.UpdatedAt)
	if isUniqueViolation(err) {
		return ErrSlugTaken
	}
	if err != nil {
		return fmt.Errorf("insert project: %w", err)
	}
	for _, e := range envs {
		if _, err = tx.Exec(ctx, `INSERT INTO loomplane_environments (`+environmentColumns+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			e.ID, e.OrgID, e.ProjectID, e.Name, e.Kind, e.CreatedAt, e.UpdatedAt); err != nil {
			return fmt.Errorf("insert environment %s: %w", e.Name, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// rollback undoes tx when err says the transaction failed, and returns err
// with anything the rollback itself reports.
func rollback(tx driver.Tx, err error) error {
	if err == nil {
		return nil
	}
	if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, pgx.ErrTxClosed) {
		return errors.Join(err, fmt.Errorf("rollback: %w", rerr))
	}
	return err
}

// GetProject returns the project, or ErrNotFound.
func (s *PgStore) GetProject(ctx context.Context, orgID, id string) (*Project, error) {
	p, err := scanProject(s.db.QueryRow(ctx, projectSelect+` WHERE p.org_id = $1 AND p.id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	return p, nil
}

// ListProjects returns the organization's projects, newest first.
func (s *PgStore) ListProjects(ctx context.Context, orgID string) ([]*Project, error) {
	rows, err := s.db.Query(ctx, projectSelect+` WHERE p.org_id = $1 ORDER BY p.created_at DESC, p.id DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return out, nil
}

// UpdateProject writes p's mutable fields: name, description, icon, color,
// status and updated_at. Exclusions and autonomy have writes of their own.
func (s *PgStore) UpdateProject(ctx context.Context, p *Project) error {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_projects
		SET name = $3, description = $4, icon = $5, color = $6, status = $7, updated_at = $8
		WHERE org_id = $1 AND id = $2`,
		p.OrgID, p.ID, p.Name, p.Description, p.Icon, p.Color, p.Status, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	return requireOneRow(res)
}

// UpdateExclusions rewrites the project's exclusions in a transaction that
// locks the row, so a concurrent change waits instead of being lost.
func (s *PgStore) UpdateExclusions(ctx context.Context, orgID, id string, change func([]Exclusion) ([]Exclusion, error)) (p *Project, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { err = rollback(tx, err) }()
	// Order the global source gate before parent and quota row locks.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('variable-sources/v1',0))`); err != nil {
		return nil, err
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT exclusions FROM loomplane_projects WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read exclusions: %w", err)
	}
	list := []Exclusion{}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("decode exclusions: %w", err)
		}
	}
	if list, err = change(list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Exclusion{}
	}
	enc, err := json.Marshal(list)
	if err != nil {
		return nil, fmt.Errorf("encode exclusions: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_projects SET exclusions = $3, updated_at = now() WHERE org_id = $1 AND id = $2`, orgID, id, enc); err != nil {
		return nil, fmt.Errorf("update exclusions: %w", err)
	}
	if p, err = scanProject(tx.QueryRow(ctx, projectSelect+` WHERE p.org_id = $1 AND p.id = $2`, orgID, id)); err != nil {
		return nil, fmt.Errorf("reread project: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return p, nil
}

// SetAutonomy implements Store. A nil field keeps the column's value, in
// the same statement, so two changes to different fields can't lose each
// other. The values before and after both come from that statement, under
// its row lock, so they're the ones this change replaced and wrote; a
// change that lands after it can't show up in its result.
func (s *PgStore) SetAutonomy(ctx context.Context, orgID, id string, in AutonomyInput, at time.Time) (*Project, Autonomy, error) {
	var before, after Autonomy
	err := s.db.QueryRow(ctx, `WITH old AS (
			SELECT autonomous_promotion, min_staging_soak_seconds FROM loomplane_projects
			WHERE org_id = $1 AND id = $2 FOR UPDATE
		)
		UPDATE loomplane_projects p
		SET autonomous_promotion = COALESCE($3, p.autonomous_promotion),
			min_staging_soak_seconds = COALESCE($4, p.min_staging_soak_seconds),
			updated_at = $5
		FROM old
		WHERE p.org_id = $1 AND p.id = $2
		RETURNING old.autonomous_promotion, old.min_staging_soak_seconds, p.autonomous_promotion, p.min_staging_soak_seconds`,
		orgID, id, in.AutonomousPromotion, in.MinStagingSoakSeconds, at).
		Scan(&before.AutonomousPromotion, &before.MinStagingSoakSeconds, &after.AutonomousPromotion, &after.MinStagingSoakSeconds)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, Autonomy{}, ErrNotFound
	}
	if err != nil {
		return nil, Autonomy{}, fmt.Errorf("update autonomy: %w", err)
	}
	// The rest of the project is read after; its autonomy is what this
	// statement wrote.
	p, err := s.GetProject(ctx, orgID, id)
	if err != nil {
		return nil, Autonomy{}, err
	}
	p.AutonomousPromotion, p.MinStagingSoakSeconds = after.AutonomousPromotion, after.MinStagingSoakSeconds
	return p, before, nil
}

// SetResponsibleTeam implements Store. The team must be in the project's
// organization; when nothing changes, one more read tells a missing project
// from a team that isn't there.
func (s *PgStore) SetResponsibleTeam(ctx context.Context, orgID, id, teamID string) (*Project, error) {
	res, execErr := s.db.Exec(ctx, `UPDATE loomplane_projects
		SET responsible_team_id = NULLIF($3, ''), updated_at = now()
		WHERE org_id = $1 AND id = $2
			AND ($3 = '' OR EXISTS (SELECT 1 FROM loomplane_teams t WHERE t.id = $3 AND t.org_id = $1))`,
		orgID, id, teamID)
	if err := teamWriteError(execErr); err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("rows affected: %w", err)
	}
	p, err := s.GetProject(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrTeamNotFound
	}
	return p, nil
}

// DeletionRefusal says, before anything else happens, whether
// DeleteProject (environmentID empty) or DeleteEnvironment would refuse:
// ErrNotFound, ErrEnvironmentNotFound, ErrFleetManaged or
// ErrLastEnvironment. It's nil otherwise. The deletion checks again.
func (s *PgStore) DeletionRefusal(ctx context.Context, orgID, projectID, environmentID string) error {
	var exists, managed bool
	var envs int
	var envExists bool
	err := s.db.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM loomplane_projects WHERE org_id=$1 AND id=$2),
		CASE WHEN $3='' THEN EXISTS(SELECT 1 FROM loomplane_fleets WHERE org_id=$1 AND (project_id=$2 OR control_project_id=$2))
			ELSE EXISTS(SELECT 1 FROM loomplane_fleet_instances WHERE org_id=$1 AND project_id=$2 AND environment_id=$3) END,
		(SELECT count(*) FROM loomplane_environments WHERE org_id=$1 AND project_id=$2),
		EXISTS(SELECT 1 FROM loomplane_environments WHERE org_id=$1 AND project_id=$2 AND id=$3)`,
		orgID, projectID, environmentID).Scan(&exists, &managed, &envs, &envExists)
	switch {
	case err != nil:
		return fmt.Errorf("check the deletion: %w", err)
	case !exists:
		return ErrNotFound
	case managed:
		return ErrFleetManaged
	case environmentID == "":
		return nil
	case !envExists:
		return ErrEnvironmentNotFound
	case envs <= 1:
		return ErrLastEnvironment
	}
	return nil
}

// DeleteProject removes the project; its environments go with it, and so
// does the frontend capacity bookkeeping nothing holds any more. A
// frontend that may still run keeps the project: ErrFrontendsHeld.
func (s *PgStore) DeleteProject(ctx context.Context, orgID, id string) (err error) {
	var managed bool
	if managedErr := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_fleets WHERE org_id=$1 AND (project_id=$2 OR control_project_id=$2))`, orgID, id).Scan(&managed); managedErr != nil {
		return managedErr
	}
	if managed {
		return ErrFleetManaged
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { err = rollback(tx, err) }()
	if cerr := clearReleasedFrontends(ctx, tx, orgID, id, ""); cerr != nil {
		return cerr
	}
	res, err := tx.Exec(ctx, `DELETE FROM loomplane_projects WHERE org_id = $1 AND id = $2`, orgID, id)
	if rerr := retainedError(err); rerr != nil {
		return rerr
	}
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if nerr := requireOneRow(res); nerr != nil {
		return nerr
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// clearReleasedFrontends deletes the frontend capacity rows of a project,
// or of one of its environments, that no longer count: released slots,
// whose uses all have provider absence or never started. Both restrict
// their plans' deletion. A slot still reserved stays, and its restriction
// refuses the delete. The plans' frontend intents go with the plans.
func clearReleasedFrontends(ctx context.Context, tx driver.Tx, orgID, projectID, environmentID string) error {
	released := `s.org_id=$1 AND s.project_id=$2 AND ($3='' OR s.environment_id=$3) AND s.status='released'
		AND NOT EXISTS(SELECT 1 FROM loomplane_frontend_uses l WHERE l.org_id=s.org_id AND l.cloud_id=s.cloud_id AND l.slot_id=s.id AND l.status NOT IN ('removed','unused'))`
	if _, err := tx.Exec(ctx, `DELETE FROM loomplane_frontend_uses u USING loomplane_frontend_slots s
		WHERE u.org_id=s.org_id AND u.cloud_id=s.cloud_id AND u.slot_id=s.id AND `+released, orgID, projectID, environmentID); err != nil {
		return fmt.Errorf("clear released frontend uses: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM loomplane_frontend_slots s WHERE `+released, orgID, projectID, environmentID); err != nil {
		return fmt.Errorf("clear released frontend slots: %w", err)
	}
	return nil
}

const environmentColumns = `id, org_id, project_id, name, kind, created_at, updated_at`

const environmentReadColumns = environmentColumns + `,coalesce((SELECT i.id FROM loomplane_fleet_instances i WHERE i.org_id=loomplane_environments.org_id AND i.environment_id=loomplane_environments.id),'')`

// CreateEnvironment implements Store.
func (s *PgStore) CreateEnvironment(ctx context.Context, e *Environment) error {
	res, err := s.db.Exec(ctx, `INSERT INTO loomplane_environments (`+environmentColumns+`)
		SELECT $1, $2, $3, $4, $5, $6, $7
		WHERE EXISTS (SELECT 1 FROM loomplane_projects WHERE org_id = $2 AND id = $3)`,
		e.ID, e.OrgID, e.ProjectID, e.Name, e.Kind, e.CreatedAt, e.UpdatedAt)
	if isUniqueViolation(err) {
		return ErrEnvironmentExists
	}
	if err != nil {
		return fmt.Errorf("insert environment: %w", err)
	}
	return requireOneRow(res)
}

// GetEnvironment returns one environment, or ErrEnvironmentNotFound.
func (s *PgStore) GetEnvironment(ctx context.Context, orgID, projectID, id string) (*Environment, error) {
	var e Environment
	err := s.db.QueryRow(ctx, `SELECT `+environmentReadColumns+` FROM loomplane_environments
		WHERE org_id = $1 AND project_id = $2 AND id = $3`, orgID, projectID, id).
		Scan(&e.ID, &e.OrgID, &e.ProjectID, &e.Name, &e.Kind, &e.CreatedAt, &e.UpdatedAt, &e.InstanceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEnvironmentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get environment: %w", err)
	}
	e.CreatedAt, e.UpdatedAt = e.CreatedAt.UTC(), e.UpdatedAt.UTC()
	return &e, nil
}

// DeleteEnvironment implements Store. It locks the project's row first, so
// two deletes can't each see the other's environment and leave none.
func (s *PgStore) DeleteEnvironment(ctx context.Context, orgID, projectID, id string) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { err = rollback(tx, err) }()
	// Take the source gate before parent row locks used by variable cascades.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('variable-sources/v1',0))`); err != nil {
		return err
	}
	var locked string
	err = tx.QueryRow(ctx, `SELECT id FROM loomplane_projects WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, projectID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock project: %w", err)
	}
	var managed bool
	if managedErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_fleet_instances WHERE org_id=$1 AND project_id=$2 AND environment_id=$3)`, orgID, projectID, id).Scan(&managed); managedErr != nil {
		return managedErr
	}
	if managed {
		return ErrFleetManaged
	}
	var total int
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT count(*), bool_or(id = $2) FROM loomplane_environments WHERE project_id = $1`,
		projectID, id).Scan(&total, &exists); err != nil {
		return fmt.Errorf("count environments: %w", err)
	}
	switch {
	case !exists:
		return ErrEnvironmentNotFound
	case total <= 1:
		return ErrLastEnvironment
	}
	if cerr := clearReleasedFrontends(ctx, tx, orgID, projectID, id); cerr != nil {
		return cerr
	}
	if _, err = tx.Exec(ctx, `DELETE FROM loomplane_environments WHERE project_id = $1 AND id = $2`, projectID, id); err != nil {
		if rerr := retainedError(err); rerr != nil {
			return rerr
		}
		return fmt.Errorf("delete environment: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// ListEnvironments returns the project's environments, production first.
func (s *PgStore) ListEnvironments(ctx context.Context, orgID, projectID string) ([]*Environment, error) {
	rows, err := s.db.Query(ctx, `SELECT `+environmentReadColumns+`
		FROM loomplane_environments WHERE org_id = $1 AND project_id = $2
		ORDER BY array_position(ARRAY['production', 'staging', 'preview', 'development'], kind), name`, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Environment{}
	for rows.Next() {
		var e Environment
		if err := rows.Scan(&e.ID, &e.OrgID, &e.ProjectID, &e.Name, &e.Kind, &e.CreatedAt, &e.UpdatedAt, &e.InstanceID); err != nil {
			return nil, fmt.Errorf("scan environment: %w", err)
		}
		e.CreatedAt, e.UpdatedAt = e.CreatedAt.UTC(), e.UpdatedAt.UTC()
		out = append(out, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	return out, nil
}

// normalize reports times in UTC: pgx scans TIMESTAMPTZ into the process's
// local zone, and the API should answer the same however the server's
// clock is set.
func (p *Project) normalize() {
	if p.DeploymentMode == "" {
		p.DeploymentMode = "application"
	}
	if p.Kind == "" {
		p.Kind = ProjectKindApp
	}
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	if p.Repositories == nil {
		p.Repositories = []string{}
	}
	if p.Exclusions == nil {
		p.Exclusions = []Exclusion{}
	}
	for i := range p.Exclusions {
		p.Exclusions[i].CreatedAt = p.Exclusions[i].CreatedAt.UTC()
	}
}

func requireOneRow(res interface{ RowsAffected() (int64, error) }) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// teamWriteError maps the error of the responsible-team update. A foreign
// key violation means the team was deleted between the check and the write,
// so the caller named a team that isn't there.
func teamWriteError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
		return ErrTeamNotFound
	}
	return fmt.Errorf("set responsible team: %w", err)
}

// retainedError turns a delete the database refused to keep history into
// the error its caller shows, or returns nil when err isn't one of those.
//
// The refusals arrive as *pgconn.PgError with one of three codes:
//   - pgRestrictViolation (23001): an ON DELETE RESTRICT key, raised at
//     once. Every loomplane_frontend_* key is one.
//   - pgForeignKeyViolation (23503): a NO ACTION key, or a guard trigger
//     that raises it.
//   - pgCheckViolation (23514): a cleanup history guard.
//
// pgErr.ConstraintName names the key ("" for a trigger), and
// pgErr.TableName the referencing table.
//
// A frontend's capacity row (ErrFrontendsHeld) and cleanup history
// (ErrHistoryRetained) need different next steps from the person deleting,
// so each wraps its own sentinel around err, as fmt.Errorf("%w: %w", ...).
func retainedError(err error) error {
	var pgErr *pgconn.PgError
	if !isRetained(err) || !errors.As(err, &pgErr) {
		return nil
	}
	// The same frontend key comes back as 23001 or 23503, depending on
	// whether the delete reached it directly or through a cascade.
	if pgErr.Code != pgCheckViolation &&
		(strings.HasPrefix(pgErr.ConstraintName, "loomplane_frontend_") || strings.HasPrefix(pgErr.TableName, "loomplane_frontend_")) {
		return fmt.Errorf("%w: %w", ErrFrontendsHeld, err)
	}
	return fmt.Errorf("%w: %w", ErrHistoryRetained, err)
}

// isRetained reports whether the database refused a delete to keep history:
// a foreign key that restricts it, at once or at the statement's end, or a
// guard that raises a check violation for cleanup records that must be
// retained.
func isRetained(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case pgRestrictViolation, pgForeignKeyViolation, pgCheckViolation:
		return true
	}
	return false
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
