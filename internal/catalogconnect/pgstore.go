package catalogconnect

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

const columns = `id, org_id, name, engine, host, port, database, username, tls, created_by, created_at, revision`

func scan(row pgx.Row) (*Endpoint, error) {
	var e Endpoint
	if err := row.Scan(&e.ID, &e.OrgID, &e.Name, &e.Engine, &e.Host, &e.Port, &e.Database, &e.Username,
		&e.TLS, &e.CreatedBy, &e.CreatedAt, &e.Revision); err != nil {
		return nil, err
	}
	return &e, nil
}

// Create implements Store.
func (s *PgStore) Create(ctx context.Context, e *Endpoint) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_catalog_endpoints (`+columns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		e.ID, e.OrgID, e.Name, e.Engine, e.Host, e.Port, e.Database, e.Username, e.TLS, e.CreatedBy, e.CreatedAt, e.Revision)
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
	e, err := scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_catalog_endpoints
		WHERE org_id = $1 AND id = $2`, orgID, id))
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
	rows, err := s.db.Query(ctx, `SELECT `+columns+` FROM loomplane_catalog_endpoints
		WHERE org_id = $1 ORDER BY name`, orgID)
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
	AND jsonb_path_exists(b.config, '$.composition.dependencies.*.external ? (@ == $id)', jsonb_build_object('id', $2::text)))`

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
