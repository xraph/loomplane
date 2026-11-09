package agentsettings

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
)

// PgStore is the Postgres Store, over the agent's columns of
// loomplane_ai_settings (database/migrations 20261220000100). It reads and
// writes those columns, updated_by, updated_at and revision, and nothing
// else of the row.
type PgStore struct {
	db  *pgdriver.PgDB
	now func() time.Time
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore {
	return &PgStore{db: db, now: time.Now}
}

var _ Store = (*PgStore)(nil)

const columns = `org_id, agent_enabled, agent_provider_key, agent_model, agent_knowledge, nexus_tenant_id,
	revision, updated_by, updated_at`

func scan(row driver.Row) (*Settings, error) {
	var s Settings
	if err := row.Scan(&s.OrgID, &s.Enabled, &s.ProviderKey, &s.Model, &s.Knowledge, &s.NexusTenantID,
		&s.Revision, &s.UpdatedBy, &s.UpdatedAt); err != nil {
		return nil, err
	}
	s.UpdatedAt = s.UpdatedAt.UTC()
	return &s, nil
}

// Get implements Store.
func (s *PgStore) Get(ctx context.Context, orgID string) (*Settings, error) {
	got, err := scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_ai_settings WHERE org_id = $1`, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		return &Settings{OrgID: orgID}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent settings: %w", err)
	}
	return got, nil
}

// ByNexusTenant implements Store.
func (s *PgStore) ByNexusTenant(ctx context.Context, tenantID string) (*Settings, error) {
	if tenantID == "" {
		return nil, ErrNotFound
	}
	got, err := scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_ai_settings WHERE nexus_tenant_id = $1`, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get agent settings by nexus tenant: %w", err)
	}
	return got, nil
}

// ListEnabled implements Store.
func (s *PgStore) ListEnabled(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT org_id FROM loomplane_ai_settings WHERE agent_enabled ORDER BY org_id`)
	if err != nil {
		return nil, fmt.Errorf("list workspaces with the agent on: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list workspaces with the agent on: %w", err)
		}
		out = append(out, id)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("list workspaces with the agent on: %w", err)
	}
	return out, nil
}

// Save implements Store.
func (s *PgStore) Save(ctx context.Context, in *Settings, expectRevision int64) error {
	now := s.now().UTC()
	var row driver.Row
	if expectRevision == 0 {
		// Nobody has written this workspace's row: the first writer wins.
		row = s.db.QueryRow(ctx, `INSERT INTO loomplane_ai_settings (org_id, agent_enabled, agent_provider_key, agent_model,
				agent_knowledge, nexus_tenant_id, updated_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
			ON CONFLICT (org_id) DO NOTHING
			RETURNING `+columns,
			in.OrgID, in.Enabled, in.ProviderKey, in.Model, in.Knowledge, in.NexusTenantID, in.UpdatedBy, now)
	} else {
		row = s.db.QueryRow(ctx, `UPDATE loomplane_ai_settings
			SET agent_enabled = $3, agent_provider_key = $4, agent_model = $5, agent_knowledge = $6, nexus_tenant_id = $7,
				updated_by = $8, updated_at = $9, revision = revision + 1
			WHERE org_id = $1 AND revision = $2
			RETURNING `+columns,
			in.OrgID, expectRevision, in.Enabled, in.ProviderKey, in.Model, in.Knowledge, in.NexusTenantID, in.UpdatedBy, now)
	}
	got, err := scan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrStale
	}
	if err != nil {
		return fmt.Errorf("save agent settings: %w", err)
	}
	*in = *got
	return nil
}
