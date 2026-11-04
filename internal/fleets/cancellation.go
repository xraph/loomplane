package fleets

import (
	"context"
	"strings"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type QueuedCancellationStore interface {
	CancelQueuedCreate(context.Context, FleetScope, string, int64) error
}

// CancelOperation immediately cancels only a create that has never held effect
// authority. Admitted or ambiguous work requires its reviewed recovery workflow.
func (s *Service) CancelOperation(ctx context.Context, p security.Principal, id string, expectedRevision int64) (*fleet.Operation, error) {
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	op, err := s.GetOperation(ctx, p, id)
	if err != nil {
		return nil, err
	}
	instance, err := s.GetInstance(ctx, p, op.InstanceID)
	if err != nil {
		return nil, err
	}
	f, err := s.GetFleet(ctx, p, instance.FleetID)
	if err != nil {
		return nil, err
	}
	if !p.IsFleet() {
		if checkErr := s.authz.Require(ctx, p, security.DeploymentCreate, f.ProjectID); checkErr != nil {
			return nil, checkErr
		}
	}
	store, ok := s.store.(QueuedCancellationStore)
	if !ok {
		return nil, ErrInvalid
	}
	if checkErr := store.CancelQueuedCreate(ctx, FleetScope{OrgID: p.OrgID, ProjectID: f.ProjectID, FleetID: f.ID}, id, expectedRevision); checkErr != nil {
		return nil, checkErr
	}
	s.record(ctx, p, f.ProjectID, "fleet.operation.cancel", id)
	return s.GetOperation(ctx, p, id)
}

func (s *PgStore) CancelQueuedCreate(ctx context.Context, scope FleetScope, id string, expectedRevision int64) (err error) {
	if expectedRevision < 1 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = rollback(tx, err) }()
	var fleetID string
	if err = tx.QueryRow(ctx, `SELECT id FROM loomplane_fleets WHERE org_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, scope.OrgID, scope.ProjectID, scope.FleetID).Scan(&fleetID); err != nil {
		return storeError(err)
	}
	columns := "i." + strings.ReplaceAll(instanceColumns, ",", ",i.")
	instance, err := scanInstance(tx.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_fleet_instances i JOIN loomplane_fleet_operations o ON o.org_id=i.org_id AND o.fleet_id=i.fleet_id AND o.instance_id=i.id WHERE o.org_id=$1 AND o.fleet_id=$2 AND o.id=$3 FOR UPDATE OF i`, scope.OrgID, scope.FleetID, id))
	if err != nil {
		return err
	}
	var kind, status string
	var generation int64
	if err = tx.QueryRow(ctx, `SELECT kind,status,generation FROM loomplane_fleet_operations WHERE org_id=$1 AND fleet_id=$2 AND id=$3 FOR UPDATE`, scope.OrgID, scope.FleetID, id).Scan(&kind, &status, &generation); err != nil {
		return storeError(err)
	}
	if kind != "create" {
		return ErrConflict
	}
	if status == "cancelled" {
		return tx.Commit()
	}
	if instance.Revision != expectedRevision || instance.DesiredGeneration != generation || status != "waiting_approval" || instance.Status != "waiting_approval" {
		return ErrConflict
	}
	// ReserveAuthority holds the fleet parent while recording admission. Once an
	// authority exists, absence of resource receipts does not prove no effects.
	var admitted bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_fleet_authorities WHERE org_id=$1 AND fleet_id=$2 AND external_ref=$3)`, scope.OrgID, scope.FleetID, instance.ExternalRef).Scan(&admitted); checkErr != nil {
		return checkErr
	}
	if admitted {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_fleet_operations SET status='cancelled',error_code='' WHERE org_id=$1 AND id=$2`, scope.OrgID, id); err != nil {
		return err
	}
	// Retain the immutable environment/reference tombstone while releasing the
	// unused stage slot. No resource or grant reservation has been admitted.
	if _, err = tx.Exec(ctx, `UPDATE loomplane_fleet_instances SET status='deleted',revision=revision+1,updated_at=clock_timestamp() WHERE org_id=$1 AND id=$2`, scope.OrgID, instance.ID); err != nil {
		return err
	}
	return tx.Commit()
}
