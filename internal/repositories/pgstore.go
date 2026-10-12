package repositories

import (
	"context"
	"fmt"

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

// SaveInstallation connects an installation, or refreshes its account
// details if it's already connected.
func (s *PgStore) SaveInstallation(ctx context.Context, r *InstallationRecord) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_github_installations
		(org_id, installation_id, account, account_type, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (org_id, installation_id) DO UPDATE SET account = EXCLUDED.account, account_type = EXCLUDED.account_type`,
		r.OrgID, r.ID, r.Account, r.AccountType, r.CreatedBy, r.CreatedAt)
	if err != nil {
		return fmt.Errorf("save GitHub installation: %w", err)
	}
	return nil
}

// ListInstallations returns the organization's installations, oldest first.
func (s *PgStore) ListInstallations(ctx context.Context, orgID string) ([]*InstallationRecord, error) {
	rows, err := s.db.Query(ctx, `SELECT org_id, installation_id, account, account_type, created_by, created_at
		FROM loomplane_github_installations WHERE org_id = $1 ORDER BY created_at, installation_id`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list GitHub installations: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*InstallationRecord{}
	for rows.Next() {
		var r InstallationRecord
		if err := rows.Scan(&r.OrgID, &r.ID, &r.Account, &r.AccountType, &r.CreatedBy, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan GitHub installation: %w", err)
		}
		r.CreatedAt = r.CreatedAt.UTC()
		out = append(out, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list GitHub installations: %w", err)
	}
	return out, nil
}

// DeleteInstallation implements Store.
func (s *PgStore) DeleteInstallation(ctx context.Context, orgID string, id int64) error {
	res, err := s.db.Exec(ctx, `DELETE FROM loomplane_github_installations WHERE org_id = $1 AND installation_id = $2`, orgID, id)
	if err != nil {
		return fmt.Errorf("delete GitHub installation: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrInstallationNotFound
	}
	return nil
}
