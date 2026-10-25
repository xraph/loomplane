package connections

import (
	"context"
	"encoding/json"
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

const columns = `id, org_id, cloud, name, account, details, status, missing, message,
	checked_at, created_by, created_at, updated_at, generation, method`

func scan(row pgx.Row) (*Connection, error) {
	var c Connection
	var details, missing []byte
	if err := row.Scan(&c.ID, &c.OrgID, &c.Cloud, &c.Name, &c.Account, &details, &c.Status, &missing, &c.Message,
		&c.CheckedAt, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt, &c.Generation, &c.Method); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(details, &c.Details); err != nil {
		return nil, fmt.Errorf("decode details: %w", err)
	}
	if err := json.Unmarshal(missing, &c.Missing); err != nil {
		return nil, fmt.Errorf("decode missing: %w", err)
	}
	c.normalize()
	return &c, nil
}

func encode(c *Connection) (details, missing []byte, err error) {
	c.normalize()
	if details, err = json.Marshal(c.Details); err != nil {
		return nil, nil, err
	}
	if missing, err = json.Marshal(c.Missing); err != nil {
		return nil, nil, err
	}
	return details, missing, nil
}

// CreateConnection implements Store.
func (s *PgStore) CreateConnection(ctx context.Context, c *Connection) error {
	details, missing, err := encode(c)
	if err != nil {
		return fmt.Errorf("encode connection: %w", err)
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_cloud_connections (`+columns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		c.ID, c.OrgID, c.Cloud, c.Name, c.Account, details, c.Status, missing, c.Message,
		c.CheckedAt, c.CreatedBy, c.CreatedAt, c.UpdatedAt, c.Generation, c.Method)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("insert connection: %w", err)
	}
	return nil
}

// GetConnection implements Store.
func (s *PgStore) GetConnection(ctx context.Context, orgID, id string) (*Connection, error) {
	c, err := scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_cloud_connections
		WHERE org_id = $1 AND id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get connection: %w", err)
	}
	return c, nil
}

// ListConnections implements Store.
func (s *PgStore) ListConnections(ctx context.Context, orgID string) ([]*Connection, error) {
	rows, err := s.db.Query(ctx, `SELECT `+columns+` FROM loomplane_cloud_connections
		WHERE org_id = $1 ORDER BY cloud, name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Connection{}
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan connection: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	return out, nil
}

// UpdateConnection implements Store.
func (s *PgStore) UpdateConnection(ctx context.Context, c *Connection) error {
	previous, err := s.GetConnection(ctx, c.OrgID, c.ID)
	if err != nil {
		return err
	}
	c.normalize()
	if c.Generation != previous.Generation {
		return ErrChanged
	}
	generation := c.Generation
	if AccountIdentity(c.Cloud, c.Account, c.Details) != AccountIdentity(previous.Cloud, previous.Account, previous.Details) {
		generation++
	}

	details, missing, err := encode(c)
	if err != nil {
		return fmt.Errorf("encode connection: %w", err)
	}
	tag, err := s.db.Exec(ctx, `UPDATE loomplane_cloud_connections
		SET account = $3, details = $4, status = $5, missing = $6, message = $7, checked_at = $8, updated_at = $9, generation = $10, method = $12
		WHERE org_id = $1 AND id = $2 AND generation = $11`,
		c.OrgID, c.ID, c.Account, details, c.Status, missing, c.Message, c.CheckedAt, c.UpdatedAt, generation, c.Generation, c.Method)
	if err != nil {
		return fmt.Errorf("update connection: %w", err)
	}
	if n, err := tag.RowsAffected(); err == nil && n == 0 {
		return ErrChanged
	}
	c.Generation = generation
	return nil
}

// DeleteConnection implements Store.
func (s *PgStore) DeleteConnection(ctx context.Context, orgID, id string) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin connection deletion: %w", err)
	}
	defer func() {
		if err != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
				err = errors.Join(err, fmt.Errorf("rollback connection deletion: %w", rollbackErr))
			}
		}
	}()
	tag, err := tx.Exec(ctx, `DELETE FROM loomplane_cloud_connections WHERE org_id = $1 AND id = $2`, orgID, id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return ErrInUse
	}
	if err != nil {
		return fmt.Errorf("delete connection: %w", err)
	}
	if n, rowsErr := tag.RowsAffected(); rowsErr == nil && n == 0 {
		return ErrNotFound
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit connection deletion: %w", err)
	}
	return nil
}

// BeginCredentialReplacement invalidates reviewed work before Vault is changed.
func (s *PgStore) BeginCredentialReplacement(ctx context.Context, c *Connection) error {
	c.normalize()
	tag, err := s.db.Exec(ctx, `UPDATE loomplane_cloud_connections SET generation=generation+1,status='incomplete',message=$4 WHERE org_id=$1 AND id=$2 AND generation=$3`, c.OrgID, c.ID, c.Generation, replacementInProgressMessage)
	if err != nil {
		return err
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrChanged
	}
	c.Generation++
	return nil
}

// WithCredentialReplacement serializes Vault writes as well as generation changes.
func (s *PgStore) WithCredentialReplacement(ctx context.Context, c *Connection, replace func() error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("release credential replacement lock: %w", rollbackErr))
		}
	}()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "connection-credential/"+c.OrgID+"/"+c.ID); err != nil {
		return err
	}
	if err := s.BeginCredentialReplacement(ctx, c); err != nil {
		return err
	}
	if err := replace(); err != nil {
		return err
	}
	return tx.Commit()
}
