package fleets

import (
	"context"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type ActiveOperationStore interface {
	GetActiveOperation(context.Context, FleetScope, string) (*fleet.Operation, error)
}

// GetActiveOperation returns the instance's sole unresolved mutation. An older
// generation remains visible for recovery; reading never authorizes cancellation.
func (s *Service) GetActiveOperation(ctx context.Context, p security.Principal, id string) (*fleet.Operation, error) {
	instance, err := s.GetInstance(ctx, p, id)
	if err != nil {
		return nil, err
	}
	f, err := s.GetFleet(ctx, p, instance.FleetID)
	if err != nil {
		return nil, err
	}
	store, ok := s.store.(ActiveOperationStore)
	if !ok {
		return nil, ErrInvalid
	}
	return store.GetActiveOperation(ctx, FleetScope{OrgID: p.OrgID, ProjectID: f.ProjectID, FleetID: f.ID}, id)
}

func (s *PgStore) GetActiveOperation(ctx context.Context, scope FleetScope, id string) (*fleet.Operation, error) {
	var op fleet.Operation
	err := s.db.QueryRow(ctx, `SELECT o.id,o.instance_id,o.kind,o.status,o.request_hash,o.generation,o.error_code,o.created_at FROM loomplane_fleet_operations o JOIN loomplane_fleet_instances i ON i.org_id=o.org_id AND i.fleet_id=o.fleet_id AND i.id=o.instance_id WHERE o.org_id=$1 AND i.project_id=$2 AND o.fleet_id=$3 AND o.instance_id=$4 AND o.status NOT IN ('succeeded','failed','cancelled')`, scope.OrgID, scope.ProjectID, scope.FleetID, id).Scan(&op.ID, &op.InstanceID, &op.Kind, &op.Status, &op.RequestHash, &op.Generation, &op.ErrorCode, &op.CreatedAt)
	if err != nil {
		return nil, storeError(err)
	}
	op.CreatedAt = op.CreatedAt.UTC()
	return &op, nil
}
