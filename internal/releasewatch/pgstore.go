package releasewatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
)

// PgStore is the Postgres Store, over loomplane_release_watches.
type PgStore struct{ db *pgdriver.PgDB }

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

// claimAfter is how long a claimed watch rests before another tick may
// claim it.
const claimAfter = 50 * time.Second

const watchColumns = `id, org_id, project_id, environment_id, environment, deployment_id, plan_id, status, verdict, reason,
	thresholds, services, unwatched, held, held_released, unverified, rollback_plan_id, rollback_approved_at,
	starts_at, ends_at, bound_at, checked_at, settled_at, revision, created_at, updated_at`

func scanWatch(row pgx.Row) (*Watch, error) {
	var w Watch
	var thresholds, services, unwatched, held []byte
	if err := row.Scan(&w.ID, &w.OrgID, &w.ProjectID, &w.EnvironmentID, &w.Environment, &w.DeploymentID, &w.PlanID,
		&w.Status, &w.Verdict, &w.Reason, &thresholds, &services, &unwatched, &held, &w.HeldReleased, &w.Unverified,
		&w.RollbackPlanID, &w.RollbackApprovedAt, &w.StartsAt, &w.EndsAt, &w.BoundAt, &w.CheckedAt, &w.SettledAt,
		&w.Revision, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		raw  []byte
		into any
		name string
	}{
		{thresholds, &w.Thresholds, "thresholds"}, {services, &w.Services, "services"},
		{unwatched, &w.Unwatched, "unwatched"}, {held, &w.Held, "held"},
	} {
		if err := json.Unmarshal(field.raw, field.into); err != nil {
			return nil, fmt.Errorf("decode the watch's %s: %w", field.name, err)
		}
	}
	return &w, nil
}

// encoded is a watch's JSON columns.
type encoded struct{ thresholds, services, unwatched, held []byte }

func encode(w *Watch) (encoded, error) {
	var out encoded
	var err error
	if out.thresholds, err = json.Marshal(w.Thresholds); err != nil {
		return out, err
	}
	if out.services, err = json.Marshal(nonNil(w.Services)); err != nil {
		return out, err
	}
	if out.unwatched, err = json.Marshal(nonNil(w.Unwatched)); err != nil {
		return out, err
	}
	out.held, err = json.Marshal(nonNil(w.Held))
	return out, err
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Insert implements Store.
func (s *PgStore) Insert(ctx context.Context, w *Watch) (bool, error) {
	e, err := encode(w)
	if err != nil {
		return false, err
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO loomplane_release_watches (`+watchColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,1,$24,$25)
		ON CONFLICT (deployment_id) DO NOTHING`,
		w.ID, w.OrgID, w.ProjectID, w.EnvironmentID, w.Environment, w.DeploymentID, w.PlanID, w.Status, w.Verdict, w.Reason,
		e.thresholds, e.services, e.unwatched, e.held, w.HeldReleased, w.Unverified, w.RollbackPlanID, w.RollbackApprovedAt,
		w.StartsAt, w.EndsAt, w.BoundAt, w.CheckedAt, w.SettledAt, w.CreatedAt, w.UpdatedAt)
	if err != nil {
		return false, fmt.Errorf("insert release watch: %w", err)
	}
	if n, countErr := tag.RowsAffected(); countErr != nil || n == 0 {
		return false, countErr
	}
	w.Revision = 1
	return true, nil
}

func (s *PgStore) one(ctx context.Context, query string, args ...any) (*Watch, error) {
	w, err := scanWatch(s.db.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read release watch: %w", err)
	}
	return w, nil
}

// ForDeployment implements Store.
func (s *PgStore) ForDeployment(ctx context.Context, orgID, deploymentID string) (*Watch, error) {
	return s.one(ctx, `SELECT `+watchColumns+` FROM loomplane_release_watches WHERE org_id=$1 AND deployment_id=$2`, orgID, deploymentID)
}

// Latest implements Store.
func (s *PgStore) Latest(ctx context.Context, orgID, environmentID string) (*Watch, error) {
	return s.one(ctx, `SELECT `+watchColumns+` FROM loomplane_release_watches WHERE org_id=$1 AND environment_id=$2
		ORDER BY starts_at DESC, id DESC LIMIT 1`, orgID, environmentID)
}

// Due implements Store. It claims and stamps in one statement: a plain
// SELECT ... SKIP LOCKED would let go of its locks before the caller
// stamped anything.
func (s *PgStore) Due(ctx context.Context, now time.Time, limit int) ([]*Watch, error) {
	rows, err := s.db.Query(ctx, `UPDATE loomplane_release_watches SET checked_at=$1
		WHERE id IN (
			SELECT id FROM loomplane_release_watches
			WHERE status IN ('watching','rolling_back') AND (checked_at IS NULL OR checked_at <= $2)
			ORDER BY checked_at NULLS FIRST, id
			LIMIT $3
			FOR UPDATE SKIP LOCKED)
		RETURNING `+watchColumns, now, now.Add(-claimAfter), limit)
	if err != nil {
		return nil, fmt.Errorf("claim due release watches: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Watch{}
	for rows.Next() {
		w, scanErr := scanWatch(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Save implements Store.
func (s *PgStore) Save(ctx context.Context, w *Watch) error {
	e, err := encode(w)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE loomplane_release_watches SET status=$3, verdict=$4, reason=$5, thresholds=$6, services=$7,
		unwatched=$8, held=$9, held_released=$10, unverified=$11, rollback_plan_id=$12, rollback_approved_at=$13,
		starts_at=$14, ends_at=$15, bound_at=$16, checked_at=$17, settled_at=$18, updated_at=$19, revision=revision+1
		WHERE org_id=$1 AND id=$2 AND revision=$20`,
		w.OrgID, w.ID, w.Status, w.Verdict, w.Reason, e.thresholds, e.services, e.unwatched, e.held, w.HeldReleased, w.Unverified,
		w.RollbackPlanID, w.RollbackApprovedAt, w.StartsAt, w.EndsAt, w.BoundAt, w.CheckedAt, w.SettledAt, w.UpdatedAt, w.Revision)
	if err != nil {
		return fmt.Errorf("save release watch: %w", err)
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrConflict
	}
	w.Revision++
	return nil
}
