package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
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

const columns = `id, org_id, project_id, environment_id, environment, plan_id, commit_sha, status, source,
	error, steps, started_by, started_at, finished_at`

func scan(row pgx.Row) (*Deployment, error) {
	var d Deployment
	var steps []byte
	if err := row.Scan(&d.ID, &d.OrgID, &d.ProjectID, &d.EnvironmentID, &d.Environment, &d.PlanID, &d.CommitSHA,
		&d.Status, &d.Source, &d.Error, &steps, &d.StartedBy, &d.StartedAt, &d.FinishedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(steps, &d.Steps); err != nil {
		return nil, fmt.Errorf("decode steps: %w", err)
	}
	d.normalize()
	return &d, nil
}

// CreateDeployment implements Store. The plan's unique index makes a
// second start for the same plan return the first deployment.
func (s *PgStore) CreateDeployment(ctx context.Context, d *Deployment) (*Deployment, error) {
	d.normalize()
	steps, err := json.Marshal(d.Steps)
	if err != nil {
		return nil, fmt.Errorf("encode steps: %w", err)
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_deployments (`+columns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (plan_id) DO NOTHING`,
		d.ID, d.OrgID, d.ProjectID, d.EnvironmentID, d.Environment, d.PlanID, d.CommitSHA, d.Status, d.Source,
		d.Error, steps, d.StartedBy, d.StartedAt, d.FinishedAt)
	if err != nil {
		return nil, fmt.Errorf("insert deployment: %w", err)
	}
	return s.ForPlan(ctx, d.OrgID, d.PlanID)
}

// GetDeployment implements Store.
func (s *PgStore) GetDeployment(ctx context.Context, orgID, id string) (*Deployment, error) {
	d, err := scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_deployments WHERE org_id = $1 AND id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get deployment: %w", err)
	}
	return d, nil
}

// ForPlan implements Store.
func (s *PgStore) ForPlan(ctx context.Context, orgID, planID string) (*Deployment, error) {
	d, err := scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_deployments WHERE org_id = $1 AND plan_id = $2`, orgID, planID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get deployment: %w", err)
	}
	return d, nil
}

// ListDeployments implements Store.
func (s *PgStore) ListDeployments(ctx context.Context, orgID, projectID string, limit int) ([]*Deployment, error) {
	rows, err := s.db.Query(ctx, `SELECT `+columns+` FROM loomplane_deployments
		WHERE org_id = $1 AND project_id = $2 ORDER BY started_at DESC, id DESC LIMIT $3`, orgID, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Deployment{}
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan deployment: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	return out, nil
}

// SetSource implements Store.
func (s *PgStore) SetSource(ctx context.Context, orgID, id, source string) error {
	if _, err := s.db.Exec(ctx, `UPDATE loomplane_deployments SET source = $3 WHERE org_id = $1 AND id = $2`, orgID, id, source); err != nil {
		return fmt.Errorf("set source: %w", err)
	}
	return nil
}

func rollback(tx driver.Tx, err error) error {
	if err == nil {
		return nil
	}
	if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, pgx.ErrTxClosed) {
		return errors.Join(err, fmt.Errorf("rollback: %w", rerr))
	}
	return err
}

// UpdateStep implements Store. It locks the row, so two steps finishing at
// once can't overwrite each other.
func (s *PgStore) UpdateStep(ctx context.Context, orgID, id string, step Step) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { err = rollback(tx, err) }()
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT steps FROM loomplane_deployments WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read steps: %w", err)
	}
	var steps []Step
	if err = json.Unmarshal(raw, &steps); err != nil {
		return fmt.Errorf("decode steps: %w", err)
	}
	found := false
	for i := range steps {
		if steps[i].ActionID == step.ActionID {
			steps[i], found = step, true
		}
	}
	if !found {
		return fmt.Errorf("%w: no step %s", ErrNotFound, step.ActionID)
	}
	if raw, err = json.Marshal(steps); err != nil {
		return fmt.Errorf("encode steps: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_deployments SET steps = $3 WHERE org_id = $1 AND id = $2`, orgID, id, raw); err != nil {
		return fmt.Errorf("write steps: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// Finish implements Store. A deployment that's already settled stays as
// it is.
func (s *PgStore) Finish(ctx context.Context, orgID, id, status, reason string, at time.Time) error {
	if _, err := s.db.Exec(ctx, `UPDATE loomplane_deployments SET status = $3, error = $4, finished_at = $5
		WHERE org_id = $1 AND id = $2 AND status = 'running'`, orgID, id, status, reason, at); err != nil {
		return fmt.Errorf("finish deployment: %w", err)
	}
	return nil
}
