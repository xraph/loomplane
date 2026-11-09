package fleets

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/xraph/loomplane/pkg/fleet"
)

// CreateExecution is private worker input. Loading it does not authorize effects.
type CreateExecution struct {
	Scope     FleetScope
	Instance  fleet.Instance
	Operation fleet.Operation
	Intent    CreateIntent
}

type CreateExecutionStore interface {
	LoadCreateExecution(context.Context, string) (*CreateExecution, error)
}

// LoadCreateExecution verifies recorded identities before the worker requests
// current authority. No caller-selected organization or privileged actor is used.
func (s *Service) LoadCreateExecution(ctx context.Context, id string) (*CreateExecution, error) {
	store, ok := s.store.(CreateExecutionStore)
	if !ok {
		return nil, ErrInvalid
	}
	run, err := store.LoadCreateExecution(ctx, id)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, ErrNotFound
	}
	scope, i, op, intent := run.Scope, run.Instance, run.Operation, run.Intent
	snap := intent.Snapshot
	if op.ID != id || op.Kind != "create" || op.InstanceID != i.ID || scope.OrgID == "" || scope.ProjectID == "" || scope.FleetID != i.FleetID || intent.Principal.OrgID != scope.OrgID || intent.Principal.UserID == "" || snap.InstanceID != i.ID || snap.FleetID != i.FleetID || snap.EnvironmentID != i.EnvironmentID || snap.Generation != op.Generation || i.DesiredGeneration != op.Generation || snap.OwnershipEpoch != i.OwnershipEpoch || snap.StageID != i.StageID || snap.ProfileID != i.ProfileID || snap.ReleaseID != i.DesiredReleaseID || intent.Input.ExternalRef != i.ExternalRef {
		return nil, ErrConflict
	}
	hash, err := reservationHash(intent.Input)
	if err != nil || hash != op.RequestHash {
		return nil, ErrConflict
	}
	return run, nil
}

func (s *PgStore) LoadCreateExecution(ctx context.Context, id string) (*CreateExecution, error) {
	var run CreateExecution
	var payload []byte
	i, op := &run.Instance, &run.Operation
	columns := "i." + strings.ReplaceAll(instanceColumns, ",", ",i.")
	err := s.db.QueryRow(ctx, `SELECT `+columns+`,i.org_id,i.project_id,o.id,o.instance_id,o.kind,o.status,o.request_hash,o.generation,o.error_code,o.created_at,p.payload FROM loomplane_fleet_operations o JOIN loomplane_fleet_instances i ON i.org_id=o.org_id AND i.fleet_id=o.fleet_id AND i.id=o.instance_id JOIN loomplane_fleet_operation_inputs p ON p.operation_id=o.id WHERE o.id=$1`, id).Scan(&i.ID, &i.FleetID, &i.StageID, &i.EnvironmentID, &i.ExternalRef, &i.ProfileID, &i.DesiredReleaseID, &i.ObservedReleaseID, &i.Status, &i.Revision, &i.DesiredGeneration, &i.ObservedGeneration, &i.OwnershipEpoch, &i.CreatedAt, &i.UpdatedAt, &run.Scope.OrgID, &run.Scope.ProjectID, &op.ID, &op.InstanceID, &op.Kind, &op.Status, &op.RequestHash, &op.Generation, &op.ErrorCode, &op.CreatedAt, &payload)
	if err != nil {
		return nil, storeError(err)
	}
	run.Scope.FleetID = i.FleetID
	if checkErr := json.Unmarshal(payload, &run.Intent); checkErr != nil {
		return nil, checkErr
	}
	i.CreatedAt, i.UpdatedAt, op.CreatedAt = i.CreatedAt.UTC(), i.UpdatedAt.UTC(), op.CreatedAt.UTC()
	return &run, nil
}
