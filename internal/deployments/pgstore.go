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

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/planning"
)

// PgStore is the Postgres Store. The schema is migration group "loomplane"
// (database/migrations).
type PgStore struct {
	frontendWake cloudinfra.CapacityCloudWakeup
	db           *pgdriver.PgDB
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var (
	_ Store       = (*PgStore)(nil)
	_ CanaryStore = (*PgStore)(nil)
)

const columns = `id, org_id, project_id, environment_id, environment, plan_id, commit_sha, status, sources,
	error, steps, retries, started_by, started_at, finished_at, capacity_revision, capacity_wait, canary`

func scan(row pgx.Row) (*Deployment, error) {
	var d Deployment
	var sources, steps, wait, canary []byte
	if err := row.Scan(&d.ID, &d.OrgID, &d.ProjectID, &d.EnvironmentID, &d.Environment, &d.PlanID, &d.CommitSHA,
		&d.Status, &sources, &d.Error, &steps, &d.Retries, &d.StartedBy, &d.StartedAt, &d.FinishedAt, &d.CapacityRevision, &wait, &canary); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(sources, &d.Sources); err != nil {
		return nil, fmt.Errorf("decode sources: %w", err)
	}
	var err error
	if d.Steps, err = decodeSteps(steps); err != nil {
		return nil, fmt.Errorf("decode steps: %w", err)
	}
	if len(wait) > 0 {
		if err = json.Unmarshal(wait, &d.CapacityWait); err != nil {
			return nil, fmt.Errorf("decode capacity wait: %w", err)
		}
	}
	if d.Canary, err = decodeCanary(canary); err != nil {
		return nil, err
	}
	d.normalize()
	return &d, nil
}

// CreateDeployment implements Store. The plan's unique index makes a
// second start for the same plan return the first deployment.
func (s *PgStore) CreateDeployment(ctx context.Context, d *Deployment) (*Deployment, error) {
	d.normalize()
	steps, err := encodeSteps(d.Steps)
	if err != nil {
		return nil, fmt.Errorf("encode steps: %w", err)
	}
	sources, err := json.Marshal(d.Sources)
	if err != nil {
		return nil, fmt.Errorf("encode sources: %w", err)
	}
	canary, err := encodeCanary(d.Canary)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_deployments (`+columns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, 0, NULL, $16)
		ON CONFLICT (plan_id) DO NOTHING`,
		d.ID, d.OrgID, d.ProjectID, d.EnvironmentID, d.Environment, d.PlanID, d.CommitSHA, d.Status, sources,
		d.Error, steps, d.Retries, d.StartedBy, d.StartedAt, d.FinishedAt, canary)
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
func (s *PgStore) ListDeployments(ctx context.Context, orgID, projectID string, before page.Cursor, limit int) ([]*Deployment, error) {
	rows, err := s.db.Query(ctx, `SELECT `+columns+` FROM loomplane_deployments
		WHERE org_id = $1 AND project_id = $2 AND ($4 = '' OR (started_at, id) < ($5, $4))
		ORDER BY started_at DESC, id DESC LIMIT $3`, orgID, projectID, limit, before.ID, before.At)
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

// SetSources implements Store.
func (s *PgStore) SetSources(ctx context.Context, orgID, id string, sources []SourceArtifact) error {
	raw, err := json.Marshal(sources)
	if err != nil {
		return fmt.Errorf("encode sources: %w", err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE loomplane_deployments SET sources = $3 WHERE org_id = $1 AND id = $2`, orgID, id, raw); err != nil {
		return fmt.Errorf("set sources: %w", err)
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
	if guardErr := cloudinfra.GuardPoolWrite(ctx, tx); guardErr != nil {
		return guardErr
	}
	var raw []byte
	var status string
	err = tx.QueryRow(ctx, `SELECT steps, status FROM loomplane_deployments WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, id).Scan(&raw, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read steps: %w", err)
	}
	// A concurrent pause or cancellation keeps pending steps resumable.
	if (step.Status == StepSkipped || step.Status == StepFailed) && (status == StatusPaused || status == StatusCancelled) {
		return tx.Commit()
	}
	steps, err := decodeSteps(raw)
	if err != nil {
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
	if raw, err = encodeSteps(steps); err != nil {
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
	if key := orchestration.FinalizationKey(ctx); key != "" {
		return s.finishActivity(ctx, orgID, id, key, status, reason, at)
	}
	if _, err := s.db.Exec(ctx, `UPDATE loomplane_deployments SET status = $3, error = $4, finished_at = $5
		WHERE org_id = $1 AND id = $2 AND status = 'running'`, orgID, id, status, reason, at); err != nil {
		return fmt.Errorf("finish deployment: %w", err)
	}
	return nil
}

// Reopen implements Store, in one transaction, so of two retries at once
// one reopens the deployment and the other finds it running.
func (s *PgStore) Reopen(ctx context.Context, orgID, id string) (ok bool, err error) {
	if allowed, ok := ctx.Value(cloudRetryKey{}).(bool); !ok || !allowed {
		var syncID string
		e := s.db.QueryRow(ctx, `SELECT coalesce(p.result->>'cloud_sync_id','') FROM loomplane_deployments d JOIN loomplane_plans p ON p.id=d.plan_id WHERE d.org_id=$1 AND d.id=$2`, orgID, id).Scan(&syncID)
		if e != nil {
			return false, e
		}
		if syncID != "" {
			return false, errors.New("retry this deployment through its Cloud sync operation")
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { err = rollback(tx, err) }()
	var raw, rawCanary []byte
	err = tx.QueryRow(ctx, `SELECT steps, canary FROM loomplane_deployments
		WHERE org_id = $1 AND id = $2 AND status = 'failed' FOR UPDATE`, orgID, id).Scan(&raw, &rawCanary)
	if errors.Is(err, pgx.ErrNoRows) {
		// Not failed: end the transaction here, since the deferred
		// rollback only runs on an error.
		if rerr := tx.Rollback(); rerr != nil {
			return false, fmt.Errorf("rollback: %w", rerr)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read deployment: %w", err)
	}
	steps, err := decodeSteps(raw)
	if err != nil {
		return false, fmt.Errorf("decode steps: %w", err)
	}
	for i := range steps {
		if retried(steps[i].Status) {
			steps[i].Status, steps[i].Message, steps[i].StartedAt, steps[i].FinishedAt = StepPending, "Waiting to retry", nil, nil
		}
	}
	if raw, err = encodeSteps(steps); err != nil {
		return false, fmt.Errorf("encode steps: %w", err)
	}
	// A canary starts over; its verdicts stay as the record of the failed run.
	if len(rawCanary) > 0 {
		c, decodeErr := decodeCanary(rawCanary)
		if decodeErr != nil {
			return false, decodeErr
		}
		if c != nil {
			c.reopen()
		}
		if rawCanary, err = encodeCanary(c); err != nil {
			return false, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_deployments
		SET status = 'running', error = '', finished_at = NULL, steps = $3, canary = $4, retries = retries + 1
		WHERE org_id = $1 AND id = $2`, orgID, id, raw, rawCanary); err != nil {
		return false, fmt.Errorf("reopen deployment: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

// storedStep is a step as it's stored: with what it replaces, which the
// API leaves out, since it's only there for a retry.
type storedStep struct {
	Checkpoint           []byte `json:"checkpoint,omitempty"`
	CloudCleanupComplete bool   `json:"cloud_cleanup_complete,omitempty"`
	Step
	Replaced *planning.Binding `json:"replaced,omitempty"`
}

func encodeSteps(steps []Step) ([]byte, error) {
	out := make([]storedStep, len(steps))
	for i, s := range steps {
		out[i] = storedStep{Step: s, Replaced: s.Replaced, Checkpoint: s.Checkpoint, CloudCleanupComplete: s.CloudCleanupComplete}
	}
	return json.Marshal(out)
}

func decodeSteps(raw []byte) ([]Step, error) {
	var in []storedStep
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	out := make([]Step, len(in))
	for i, s := range in {
		out[i] = s.Step
		out[i].Replaced = s.Replaced
		out[i].Checkpoint = s.Checkpoint
		out[i].CloudCleanupComplete = s.CloudCleanupComplete
	}
	return out, nil
}
