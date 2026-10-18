package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/drivers/pgdriver"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// PgStore persists scoped destinations in Postgres.
type PgStore struct{ db *pgdriver.PgDB }

// NewPgStore builds a store using the centrally migrated database.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

const columns = `id,org_id,project_id,environment_id,name,connection_id,ownership,identity,generation,capabilities,quarantined,created_at,updated_at`

// Create inserts a destination; database constraints serialize parent deletion.
func (s *PgStore) Create(ctx context.Context, d *domain.Destination) error {
	identity, capabilities, err := encode(d)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_federation_destinations (`+columns+`,identity_revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, d.ID, d.OrgID, d.ProjectID, d.EnvironmentID, d.Name, d.ConnectionID, d.Ownership, identity, d.Generation, capabilities, d.Quarantined, d.CreatedAt, d.UpdatedAt, d.Identity.Revision())
	return storeError(err)
}

// Get reads a destination only in the caller's organization.
func (s *PgStore) Get(ctx context.Context, orgID, id string) (*domain.Destination, error) {
	var d domain.Destination
	var identity, capabilities []byte
	err := s.db.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_federation_destinations WHERE org_id=$1 AND id=$2`, orgID, id).Scan(&d.ID, &d.OrgID, &d.ProjectID, &d.EnvironmentID, &d.Name, &d.ConnectionID, &d.Ownership, &identity, &d.Generation, &capabilities, &d.Quarantined, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, storeError(err)
	}
	if err = json.Unmarshal(identity, &d.Identity); err != nil {
		return nil, fmt.Errorf("decode destination identity: %w", err)
	}
	if err = json.Unmarshal(capabilities, &d.Capabilities); err != nil {
		return nil, fmt.Errorf("decode destination capabilities: %w", err)
	}
	return &d, nil
}

// Update changes observations only. Scope and physical identity remain immutable.
func (s *PgStore) Update(ctx context.Context, d *domain.Destination, previousGeneration int64) error {
	if d.Generation != previousGeneration+1 {
		return ErrConflict
	}
	capabilities, err := json.Marshal(d.Capabilities)
	if err != nil {
		return fmt.Errorf("encode capabilities: %w", err)
	}
	tag, err := s.db.Exec(ctx, `UPDATE loomplane_federation_destinations SET generation=$3,capabilities=$4,quarantined=$5,updated_at=$6 WHERE org_id=$1 AND id=$2 AND generation=$7 AND identity_revision=$8 AND (NOT quarantined OR $5)`, d.OrgID, d.ID, d.Generation, capabilities, d.Quarantined, d.UpdatedAt, previousGeneration, d.Identity.Revision())
	if err != nil {
		return storeError(err)
	}
	affected, err := tag.RowsAffected()
	if err != nil {
		return fmt.Errorf("destination update count: %w", err)
	}
	if affected != 1 {
		return ErrConflict
	}
	return nil
}

func encode(d *domain.Destination) (identity, capabilities []byte, err error) {
	identity, err = json.Marshal(d.Identity)
	if err != nil {
		return nil, nil, fmt.Errorf("encode identity: %w", err)
	}
	capabilities, err = json.Marshal(d.Capabilities)
	if err != nil {
		return nil, nil, fmt.Errorf("encode capabilities: %w", err)
	}
	return identity, capabilities, nil
}

func storeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrConflict
		case "23503":
			return ErrNotFound
		case "23514":
			return ErrInvalid
		}
	}
	return fmt.Errorf("federation store: %w", err)
}
