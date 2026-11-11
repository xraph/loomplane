package fleets

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/pkg/fleet"
)

var _ MetricSampleStore = (*PgStore)(nil)
var _ MetricHistoryStore = (*PgStore)(nil)

// ClaimMetricSample seeds a bounded number of new schedules and leases one due
// instance. A slow or failed source does not keep later instances at the head.
func (s *PgStore) ClaimMetricSample(ctx context.Context) (claim *MetricClaim, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = rollback(tx, err) }()
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_metric_schedule(org_id,fleet_id,instance_id,next_at) SELECT i.org_id,i.fleet_id,i.id,clock_timestamp() FROM loomplane_fleet_instances i WHERE i.status NOT IN ('retired','deleted') AND NOT EXISTS(SELECT 1 FROM loomplane_fleet_metric_schedule s WHERE s.org_id=i.org_id AND s.instance_id=i.id) ORDER BY i.org_id,i.id LIMIT 100 ON CONFLICT DO NOTHING`); err != nil {
		return nil, err
	}
	c := &MetricClaim{Lease: uuid.NewString()}
	err = tx.QueryRow(ctx, `SELECT s.org_id,s.fleet_id,s.instance_id,i.environment_id,i.desired_generation,i.ownership_epoch FROM loomplane_fleet_metric_schedule s JOIN loomplane_fleet_instances i ON i.org_id=s.org_id AND i.fleet_id=s.fleet_id AND i.id=s.instance_id WHERE s.next_at<=clock_timestamp() AND (s.lease_until IS NULL OR s.lease_until<=clock_timestamp()) AND i.status NOT IN ('retired','deleted') ORDER BY s.next_at,s.org_id,s.instance_id LIMIT 1 FOR UPDATE OF s SKIP LOCKED`).Scan(&c.Scope.OrgID, &c.Scope.FleetID, &c.Scope.InstanceID, &c.Scope.EnvironmentID, &c.Generation, &c.OwnershipEpoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_fleet_metric_schedule SET lease_id=$3,lease_until=clock_timestamp()+interval '1 minute' WHERE org_id=$1 AND instance_id=$2`, c.Scope.OrgID, c.Scope.InstanceID, c.Lease); err != nil {
		return nil, err
	}
	if checkErr := tx.Commit(); checkErr != nil {
		return nil, checkErr
	}
	return c, nil
}
func (s *PgStore) FinishMetricSample(ctx context.Context, c MetricClaim, h *fleet.MetricSnapshot) (err error) {
	now := time.Now().UTC()
	if h == nil || c.Lease == "" || h.InstanceID != c.Scope.InstanceID || h.Generation != c.Generation || h.OwnershipEpoch != c.OwnershipEpoch || h.Semantics != "snapshot" || h.CapturedAt.IsZero() || h.CapturedAt.After(now.Add(time.Second)) || now.Sub(h.CapturedAt) > time.Minute || !h.Hour.Equal(h.CapturedAt.UTC().Truncate(time.Hour)) || !h.Summary.AsOf.Equal(h.CapturedAt) || h.Generation < 1 || h.OwnershipEpoch < 1 {
		return ErrInvalid
	}
	stored := *h
	stored.CapturedAt = h.CapturedAt.UTC().Truncate(time.Microsecond)
	stored.Hour = h.Hour.UTC()
	stored.Summary.AsOf = stored.CapturedAt
	h = &stored
	raw, err := json.Marshal(h)
	if err != nil || len(raw) > 1<<20 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = rollback(tx, err) }()
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		return err
	}
	var generation, epoch int64
	var environment, status string
	if err = tx.QueryRow(ctx, `SELECT desired_generation,ownership_epoch,environment_id,status FROM loomplane_fleet_instances WHERE org_id=$1 AND fleet_id=$2 AND id=$3 FOR UPDATE`, c.Scope.OrgID, c.Scope.FleetID, c.Scope.InstanceID).Scan(&generation, &epoch, &environment, &status); err != nil {
		return storeError(err)
	}
	if generation != c.Generation || epoch != c.OwnershipEpoch || environment != c.Scope.EnvironmentID || status == "retired" || status == "deleted" {
		return ErrConflict
	}
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT lease_id=$3 AND lease_until>clock_timestamp() FROM loomplane_fleet_metric_schedule WHERE org_id=$1 AND instance_id=$2 FOR UPDATE`, c.Scope.OrgID, c.Scope.InstanceID, c.Lease).Scan(&valid); err != nil {
		return storeError(err)
	}
	if !valid {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_metric_history(org_id,fleet_id,instance_id,hour,captured_at,generation,ownership_epoch,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(org_id,instance_id,hour) DO UPDATE SET captured_at=EXCLUDED.captured_at,generation=EXCLUDED.generation,ownership_epoch=EXCLUDED.ownership_epoch,body=EXCLUDED.body WHERE loomplane_fleet_metric_history.captured_at<EXCLUDED.captured_at`, c.Scope.OrgID, c.Scope.FleetID, c.Scope.InstanceID, h.Hour, h.CapturedAt, h.Generation, h.OwnershipEpoch, raw); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_fleet_metric_schedule SET lease_id='',lease_until=NULL,next_at=$3 WHERE org_id=$1 AND instance_id=$2`, c.Scope.OrgID, c.Scope.InstanceID, h.Hour.Add(time.Hour)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *PgStore) FailMetricSample(ctx context.Context, c MetricClaim) error {
	result, err := s.db.Exec(ctx, `UPDATE loomplane_fleet_metric_schedule SET lease_id='',lease_until=NULL,next_at=clock_timestamp()+interval '5 minutes' WHERE org_id=$1 AND instance_id=$2 AND lease_id=$3 AND lease_until>clock_timestamp()`, c.Scope.OrgID, c.Scope.InstanceID, c.Lease)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func (s *PgStore) PruneMetricHistory(ctx context.Context, before time.Time, limit int) (int64, error) {
	if before.IsZero() || before.After(time.Now()) || limit < 1 || limit > 1000 {
		return 0, ErrInvalid
	}
	result, err := s.db.Exec(ctx, `WITH expired AS(SELECT org_id,instance_id,hour FROM loomplane_fleet_metric_history WHERE hour<$1 ORDER BY hour,org_id,instance_id LIMIT $2 FOR UPDATE SKIP LOCKED) DELETE FROM loomplane_fleet_metric_history h USING expired e WHERE h.org_id=e.org_id AND h.instance_id=e.instance_id AND h.hour=e.hour`, before.UTC(), limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
func (s *PgStore) ListMetricHistory(ctx context.Context, scope FleetScope, id string, before time.Time, limit int) (*fleet.MetricHistoryPage, error) {
	if scope.OrgID == "" || scope.ProjectID == "" || scope.FleetID == "" || id == "" || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT h.hour,h.captured_at,h.generation,h.ownership_epoch,h.body FROM loomplane_fleet_metric_history h JOIN loomplane_fleet_instances i ON i.org_id=h.org_id AND i.fleet_id=h.fleet_id AND i.id=h.instance_id WHERE h.org_id=$1 AND i.project_id=$2 AND h.fleet_id=$3 AND h.instance_id=$4 AND ($5::timestamptz IS NULL OR h.hour<$5) ORDER BY h.hour DESC LIMIT $6`, scope.OrgID, scope.ProjectID, scope.FleetID, id, nullableMetricTime(before), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &fleet.MetricHistoryPage{Items: []fleet.MetricSnapshot{}}
	for rows.Next() {
		var hour, captured time.Time
		var generation, epoch int64
		var raw []byte
		if checkErr := rows.Scan(&hour, &captured, &generation, &epoch, &raw); checkErr != nil {
			return nil, checkErr
		}
		var h fleet.MetricSnapshot
		if len(raw) > 1<<20 || json.Unmarshal(raw, &h) != nil || h.InstanceID != id || !h.Hour.Equal(hour) || !h.CapturedAt.Equal(captured) || h.Generation != generation || h.OwnershipEpoch != epoch || h.Semantics != "snapshot" {
			return nil, ErrInvalid
		}
		page.Items = append(page.Items, h)
		page.NextCursor = metricHistoryCursor(id, h.Hour)
	}
	if checkErr := rows.Err(); checkErr != nil {
		return nil, checkErr
	}
	return page, nil
}
func nullableMetricTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}
