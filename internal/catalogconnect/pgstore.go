package catalogconnect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
)

const pgUniqueViolation = "23505"

// PgStore is the Postgres Store. The schema is migration group "loomplane"
// (database/migrations).
type PgStore struct {
	db *pgdriver.PgDB
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

var _ WorkspaceStore = (*PgStore)(nil)

const columns = `id, org_id, name, engine, host, port, database, username, tls, created_by, created_at, revision,
	origin, description, environment_label, restricted, admin_set`

// selected are columns, then the ones only a read returns: when it last
// changed, its newest check, and the projects it's shared with.
const selected = columns + `, updated_at, last_check,
	ARRAY(SELECT g.project_id FROM loomplane_catalog_endpoint_grants g WHERE g.endpoint_id = e.id ORDER BY g.project_id)`

func scan(row pgx.Row) (*Endpoint, error) {
	var e Endpoint
	var check []byte
	if err := row.Scan(&e.ID, &e.OrgID, &e.Name, &e.Engine, &e.Host, &e.Port, &e.Database, &e.Username,
		&e.TLS, &e.CreatedBy, &e.CreatedAt, &e.Revision, &e.Origin, &e.Description, &e.EnvironmentLabel,
		&e.Restricted, &e.AdminSet, &e.UpdatedAt, &check, &e.Projects); err != nil {
		return nil, err
	}
	if len(check) > 0 && string(check) != "null" {
		e.LastCheck = &LastCheck{}
		if err := json.Unmarshal(check, e.LastCheck); err != nil {
			return nil, fmt.Errorf("read the last check: %w", err)
		}
	}
	if e.Projects == nil {
		e.Projects = []string{}
	}
	return &e, nil
}

// Create implements Store.
func (s *PgStore) Create(ctx context.Context, e *Endpoint) error {
	origin := e.Origin
	if origin == "" {
		origin = OriginRegistered
	}
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_catalog_endpoints (`+columns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		e.ID, e.OrgID, e.Name, e.Engine, e.Host, e.Port, e.Database, e.Username, e.TLS, e.CreatedBy, e.CreatedAt, e.Revision,
		origin, e.Description, e.EnvironmentLabel, e.Restricted, e.AdminSet)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return ErrNameTaken
	}
	if err != nil {
		return fmt.Errorf("insert catalog endpoint: %w", err)
	}
	return nil
}

// Get implements Store.
func (s *PgStore) Get(ctx context.Context, orgID, id string) (*Endpoint, error) {
	e, err := scan(s.db.QueryRow(ctx, `SELECT `+selected+` FROM loomplane_catalog_endpoints e
		WHERE e.org_id = $1 AND e.id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get catalog endpoint: %w", err)
	}
	return e, nil
}

// List implements Store.
func (s *PgStore) List(ctx context.Context, orgID string) ([]*Endpoint, error) {
	rows, err := s.db.Query(ctx, `SELECT `+selected+` FROM loomplane_catalog_endpoints e
		WHERE e.org_id = $1 ORDER BY e.name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list catalog endpoints: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Endpoint{}
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan catalog endpoint: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list catalog endpoints: %w", err)
	}
	return out, nil
}

// inUse finds a binding in the organization whose composition connects a
// dependency to the endpoint $2.
const inUse = `EXISTS (SELECT 1 FROM loomplane_bindings b WHERE b.org_id = $1
	AND (jsonb_path_exists(b.config, '$.composition.dependencies.*.external ? (@ == $id)', jsonb_build_object('id', $2::text))
	OR b.config #>> '{existing_resource,endpoint_id}' = $2))`

// Delete implements Store. The use check and the delete are one statement;
// its existence check sees the table as it was before the delete.
func (s *PgStore) Delete(ctx context.Context, orgID, id string) error {
	var deleted, existed bool
	err := s.db.QueryRow(ctx, `WITH gone AS (
			DELETE FROM loomplane_catalog_endpoints WHERE org_id = $1 AND id = $2 AND NOT `+inUse+`
			RETURNING id)
		SELECT EXISTS (SELECT 1 FROM gone),
			EXISTS (SELECT 1 FROM loomplane_catalog_endpoints WHERE org_id = $1 AND id = $2)`, orgID, id).Scan(&deleted, &existed)
	switch {
	case err != nil:
		return fmt.Errorf("delete catalog endpoint: %w", err)
	case deleted:
		return nil
	case existed:
		return ErrInUse
	}
	return ErrNotFound
}

// Update implements WorkspaceStore.
func (s *PgStore) Update(ctx context.Context, orgID, id string, in Details, at time.Time) (*Endpoint, error) {
	tag, err := s.db.Exec(ctx, `UPDATE loomplane_catalog_endpoints
		SET name = $3, description = $4, environment_label = $5, updated_at = $6
		WHERE org_id = $1 AND id = $2`, orgID, id, in.Name, in.Description, in.EnvironmentLabel, at)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return nil, ErrNameTaken
	}
	if err != nil {
		return nil, fmt.Errorf("update catalog endpoint: %w", err)
	}
	if n, rerr := tag.RowsAffected(); rerr != nil || n == 0 {
		return nil, notFoundOr(rerr)
	}
	return s.Get(ctx, orgID, id)
}

// Replace implements WorkspaceStore.
func (s *PgStore) Replace(ctx context.Context, e *Endpoint, from int, at time.Time) error {
	tag, err := s.db.Exec(ctx, `UPDATE loomplane_catalog_endpoints
		SET host = $4, port = $5, database = $6, username = $7, tls = $8, revision = revision + 1, updated_at = $9
		WHERE org_id = $1 AND id = $2 AND revision = $3`,
		e.OrgID, e.ID, from, e.Host, e.Port, e.Database, e.Username, e.TLS, at)
	if err != nil {
		return fmt.Errorf("replace catalog endpoint: %w", err)
	}
	if n, rerr := tag.RowsAffected(); rerr != nil {
		return rerr
	} else if n == 0 {
		if _, gerr := s.Get(ctx, e.OrgID, e.ID); gerr != nil {
			return gerr
		}
		return ErrConflict
	}
	return nil
}

// SetSharing implements WorkspaceStore. The flag and the grants change in
// one transaction.
func (s *PgStore) SetSharing(ctx context.Context, orgID, id string, restricted bool, projects []string, by string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("share catalog endpoint: %w", err)
	}
	defer rollback(tx)
	tag, err := tx.Exec(ctx, `UPDATE loomplane_catalog_endpoints SET restricted = $3, updated_at = $4
		WHERE org_id = $1 AND id = $2`, orgID, id, restricted, at)
	if err != nil {
		return fmt.Errorf("share catalog endpoint: %w", err)
	}
	if n, rerr := tag.RowsAffected(); rerr != nil || n == 0 {
		return notFoundOr(rerr)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM loomplane_catalog_endpoint_grants
		WHERE endpoint_id = $1 AND NOT (project_id = ANY($2))`, id, projects); err != nil {
		return fmt.Errorf("share catalog endpoint: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_catalog_endpoint_grants (endpoint_id, org_id, project_id, created_by, created_at)
		SELECT $1, $2, p, $4, $5 FROM unnest($3::text[]) AS p
		ON CONFLICT (endpoint_id, project_id) DO NOTHING`, id, orgID, projects, by, at); err != nil {
		return fmt.Errorf("share catalog endpoint: %w", err)
	}
	return tx.Commit()
}

// SetAdmin implements WorkspaceStore.
func (s *PgStore) SetAdmin(ctx context.Context, orgID, id string, set bool, at time.Time) error {
	tag, err := s.db.Exec(ctx, `UPDATE loomplane_catalog_endpoints SET admin_set = $3, updated_at = $4
		WHERE org_id = $1 AND id = $2`, orgID, id, set, at)
	if err != nil {
		return fmt.Errorf("set the admin credential: %w", err)
	}
	if n, rerr := tag.RowsAffected(); rerr != nil || n == 0 {
		return notFoundOr(rerr)
	}
	return nil
}

// SaveCheck implements WorkspaceStore.
func (s *PgStore) SaveCheck(ctx context.Context, orgID, id string, c *LastCheck) error {
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE loomplane_catalog_endpoints SET last_check = $3
		WHERE org_id = $1 AND id = $2`, orgID, id, body)
	if err != nil {
		return fmt.Errorf("keep the endpoint's check: %w", err)
	}
	if n, rerr := tag.RowsAffected(); rerr != nil || n == 0 {
		return notFoundOr(rerr)
	}
	return nil
}

// Uses implements WorkspaceStore. Its condition is inUse's, row by row.
func (s *PgStore) Uses(ctx context.Context, orgID, id string) ([]Use, error) {
	rows, err := s.db.Query(ctx, `SELECT b.project_id, coalesce(p.name, ''), b.environment_id, coalesce(env.name, ''), b.name
		FROM loomplane_bindings b
		LEFT JOIN loomplane_projects p ON p.id = b.project_id
		LEFT JOIN loomplane_environments env ON env.id = b.environment_id
		WHERE b.org_id = $1
			AND (jsonb_path_exists(b.config, '$.composition.dependencies.*.external ? (@ == $id)', jsonb_build_object('id', $2::text))
			OR b.config #>> '{existing_resource,endpoint_id}' = $2)
		ORDER BY p.name, env.name, b.name`, orgID, id)
	if err != nil {
		return nil, fmt.Errorf("list the endpoint's uses: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []Use{}
	for rows.Next() {
		var u Use
		if err := rows.Scan(&u.ProjectID, &u.Project, &u.EnvironmentID, &u.Environment, &u.Resource); err != nil {
			return nil, fmt.Errorf("scan the endpoint's uses: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the endpoint's uses: %w", err)
	}
	return out, nil
}

// notFoundOr is err, or ErrNotFound when an update changed no row.
func notFoundOr(err error) error {
	if err != nil {
		return err
	}
	return ErrNotFound
}

func rollback(tx driver.Tx) { _ = tx.Rollback() } //nolint:errcheck // a committed transaction cannot roll back
