package catalogproxy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
)

// PgStore is the Postgres Store. The schema is migration group "loomplane"
// (database/migrations).
type PgStore struct {
	db *pgdriver.PgDB
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

const columns = `token_hash, org_id, project_id, environment_id, resource, kind, user_id, created_at, last_used_at, expires_at`

// Create implements Store.
func (s *PgStore) Create(ctx context.Context, r *Record) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_catalog_proxy_sessions (`+columns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		r.TokenHash, r.OrgID, r.ProjectID, r.EnvironmentID, r.Resource, r.Kind, r.UserID, r.CreatedAt, r.LastUsedAt, r.ExpiresAt)
	if err != nil {
		return fmt.Errorf("keep catalog admin session: %w", err)
	}
	return nil
}

// Get implements Store.
func (s *PgStore) Get(ctx context.Context, tokenHash string) (*Record, error) {
	var r Record
	err := s.db.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_catalog_proxy_sessions WHERE token_hash = $1`, tokenHash).
		Scan(&r.TokenHash, &r.OrgID, &r.ProjectID, &r.EnvironmentID, &r.Resource, &r.Kind, &r.UserID, &r.CreatedAt, &r.LastUsedAt, &r.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read catalog admin session: %w", err)
	}
	r.CreatedAt, r.LastUsedAt, r.ExpiresAt = r.CreatedAt.UTC(), r.LastUsedAt.UTC(), r.ExpiresAt.UTC()
	return &r, nil
}

// Touch implements Store. A session is only ever moved forward.
func (s *PgStore) Touch(ctx context.Context, tokenHash string, usedAt, expiresAt time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE loomplane_catalog_proxy_sessions
		SET last_used_at = GREATEST(last_used_at, $2), expires_at = GREATEST(expires_at, $3)
		WHERE token_hash = $1`, tokenHash, usedAt, expiresAt)
	if err != nil {
		return fmt.Errorf("touch catalog admin session: %w", err)
	}
	return nil
}

// Delete implements Store.
func (s *PgStore) Delete(ctx context.Context, tokenHash string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM loomplane_catalog_proxy_sessions WHERE token_hash = $1`, tokenHash); err != nil {
		return fmt.Errorf("end catalog admin session: %w", err)
	}
	return nil
}

// DeleteResource implements Store.
func (s *PgStore) DeleteResource(ctx context.Context, orgID, environmentID, resource string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM loomplane_catalog_proxy_sessions
		WHERE org_id = $1 AND environment_id = $2 AND resource = $3`, orgID, environmentID, resource); err != nil {
		return fmt.Errorf("end catalog admin sessions: %w", err)
	}
	return nil
}

// Prune implements Store.
func (s *PgStore) Prune(ctx context.Context, before time.Time) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM loomplane_catalog_proxy_sessions WHERE expires_at < $1`, before); err != nil {
		return fmt.Errorf("prune catalog admin sessions: %w", err)
	}
	return nil
}
