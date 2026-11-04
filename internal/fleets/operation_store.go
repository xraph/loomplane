package fleets

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

var _ OperationStore = (*PgStore)(nil)

// ReserveCreate commits the request, rendered snapshot and dispatch intent with
// the instance. A duplicate request never replaces the original actor or input.
func (s *PgStore) ReserveCreate(ctx context.Context, scope FleetScope, p security.Principal, in fleet.CreateInstanceInput) (*fleet.OperationResult, error) {
	if p.OrgID != scope.OrgID || p.UserID == "" || p.ReadOnly {
		return nil, ErrInvalid
	}
	return s.reserveInstance(ctx, scope, in, func(tx driver.Tx, result *fleet.OperationResult, created bool) error {
		if !created {
			var present bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_fleet_operation_inputs WHERE operation_id=$1)`, result.Operation.ID).Scan(&present); err != nil {
				return err
			}
			if !present {
				return ErrConflict
			}
			return nil
		}
		release, err := scanRelease(tx.QueryRow(ctx, `SELECT id,fleet_id,digest,snapshot,created_at FROM loomplane_fleet_releases WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND id=$4`, scope.OrgID, scope.ProjectID, scope.FleetID, in.ReleaseID))
		if err != nil {
			return err
		}
		snapshot, err := RenderInstance(*release, result.Instance, in)
		if err != nil {
			return err
		}
		intent := CreateIntent{Principal: p, Input: in, Snapshot: *snapshot, Deadline: result.Operation.CreatedAt.Add(24 * time.Hour)}
		raw, err := json.Marshal(intent)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_operation_inputs(operation_id,payload,created_at,org_id,fleet_id,instance_id) VALUES($1,$2,$3,$4,$5,$6)`, result.Operation.ID, string(raw), result.Operation.CreatedAt, scope.OrgID, scope.FleetID, result.Instance.ID); err != nil {
			return storeError(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_operation_outbox(operation_id,next_at,org_id,fleet_id,instance_id) VALUES($1,$2,$3,$4,$5)`, result.Operation.ID, result.Operation.CreatedAt, scope.OrgID, scope.FleetID, result.Instance.ID)
		return storeError(err)
	})
}
