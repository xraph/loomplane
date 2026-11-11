package fleets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type ActionReadStore interface {
	FindAction(context.Context, FleetScope, string, fleet.ActionInput, string) (*fleet.Operation, error)
}

func actionRequestHash(in fleet.ActionInput) (string, error) {
	if err := in.Validate(); err != nil {
		return "", ErrInvalid
	}
	in.IdempotencyKey = ""
	raw, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	if len(raw) > 64<<10 {
		return "", ErrInvalid
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// FindAction recovers a lost response without admitting a new mutation. Saved
// revision pins remain part of the hash even if the instance has since advanced.
func (s *Service) FindAction(ctx context.Context, p security.Principal, instanceID string, in fleet.ActionInput) (*fleet.Operation, error) {
	instance, err := s.GetInstance(ctx, p, instanceID)
	if err != nil {
		return nil, err
	}
	f, err := s.GetFleet(ctx, p, instance.FleetID)
	if err != nil {
		return nil, err
	}
	hash, err := actionRequestHash(in)
	if err != nil {
		return nil, err
	}
	store, ok := s.store.(ActionReadStore)
	if !ok {
		return nil, ErrInvalid
	}
	return store.FindAction(ctx, FleetScope{OrgID: p.OrgID, ProjectID: f.ProjectID, FleetID: f.ID}, instanceID, in, hash)
}

func (s *PgStore) FindAction(ctx context.Context, scope FleetScope, instanceID string, in fleet.ActionInput, hash string) (*fleet.Operation, error) {
	var op fleet.Operation
	err := s.db.QueryRow(ctx, `SELECT o.id,o.instance_id,o.kind,o.status,o.request_hash,o.generation,o.error_code,o.created_at FROM loomplane_fleet_operations o JOIN loomplane_fleet_instances i ON i.org_id=o.org_id AND i.fleet_id=o.fleet_id AND i.id=o.instance_id WHERE o.org_id=$1 AND i.project_id=$2 AND o.fleet_id=$3 AND o.instance_id=$4 AND o.kind=$5 AND o.idempotency_key=$6`, scope.OrgID, scope.ProjectID, scope.FleetID, instanceID, in.Kind, in.IdempotencyKey).Scan(&op.ID, &op.InstanceID, &op.Kind, &op.Status, &op.RequestHash, &op.Generation, &op.ErrorCode, &op.CreatedAt)
	if err != nil {
		return nil, storeError(err)
	}
	if op.RequestHash != hash {
		return nil, ErrConflict
	}
	op.CreatedAt = op.CreatedAt.UTC()
	return &op, nil
}
