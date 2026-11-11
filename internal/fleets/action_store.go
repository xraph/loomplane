package fleets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type rolloutActionStore interface {
	CheckRolloutActionTx(context.Context, driver.Tx, FleetScope, security.Principal, string, fleet.ActionInput) error
}
type scalingActionStore interface {
	ReserveScalingPolicyTx(context.Context, driver.Tx, FleetScope, string, string, int64, int64, fleet.ScaleInput) error
}

func (s *PgStore) ReserveAction(ctx context.Context, scope FleetScope, p security.Principal, id string, in fleet.ActionInput) (out *fleet.Operation, err error) {
	if p.OrgID != scope.OrgID || p.UserID == "" || p.ReadOnly {
		return nil, ErrInvalid
	}
	hash, err := actionRequestHash(in)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = rollback(tx, err) }()
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM loomplane_fleets WHERE org_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, scope.OrgID, scope.ProjectID, scope.FleetID).Scan(&status); err != nil {
		return nil, storeError(err)
	}
	instance, err := scanInstance(tx.QueryRow(ctx, `SELECT `+instanceColumns+` FROM loomplane_fleet_instances WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND id=$4 FOR UPDATE`, scope.OrgID, scope.ProjectID, scope.FleetID, id))
	if err != nil {
		return nil, err
	}
	var replay fleet.Operation
	err = tx.QueryRow(ctx, `SELECT id,instance_id,kind,status,request_hash,generation,error_code,created_at FROM loomplane_fleet_operations WHERE org_id=$1 AND fleet_id=$2 AND instance_id=$3 AND kind=$4 AND idempotency_key=$5`, scope.OrgID, scope.FleetID, id, in.Kind, in.IdempotencyKey).Scan(&replay.ID, &replay.InstanceID, &replay.Kind, &replay.Status, &replay.RequestHash, &replay.Generation, &replay.ErrorCode, &replay.CreatedAt)
	if err == nil {
		if replay.RequestHash != hash {
			return nil, ErrConflict
		}
		var present bool
		if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_fleet_operation_inputs WHERE operation_id=$1)`, replay.ID).Scan(&present); checkErr != nil {
			return nil, checkErr
		}
		if !present {
			return nil, ErrConflict
		}
		replay.CreatedAt = replay.CreatedAt.UTC()
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, commitErr
		}
		return &replay, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	err = nil
	if status != "active" || instance.Revision != in.ExpectedRevision || instance.DesiredGeneration != in.ExpectedGeneration || instance.Revision == math.MaxInt64 || instance.DesiredGeneration == math.MaxInt64 {
		return nil, ErrConflict
	}
	if stateErr := actionStateAllowed(in.Kind, instance.Status); stateErr != nil {
		return nil, stateErr
	}
	var active bool
	if queryErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_fleet_operations WHERE org_id=$1 AND instance_id=$2 AND status NOT IN ('succeeded','failed','cancelled'))`, scope.OrgID, id).Scan(&active); queryErr != nil {
		return nil, queryErr
	}
	if active {
		return nil, ErrConflict
	}
	if in.Update != nil && in.Update.Rollout != nil {
		fence, ok := any(s).(rolloutActionStore)
		if !ok {
			return nil, ErrApprovalRequired
		}
		if fenceErr := fence.CheckRolloutActionTx(ctx, tx, scope, p, id, in); fenceErr != nil {
			return nil, fenceErr
		}
	}
	snapshot, err := actionSnapshotTx(ctx, tx, scope, *instance, in)
	if err != nil {
		return nil, err
	}
	var scalingStore scalingActionStore
	if in.Scale != nil {
		if validationErr := ValidateScalingSnapshot(*snapshot, *in.Scale); validationErr != nil {
			return nil, validationErr
		}
		adapter, ok := any(s).(scalingActionStore)
		if !ok {
			return nil, ErrApprovalRequired
		}
		scalingStore = adapter
	}
	now := time.Now().UTC()
	op := &fleet.Operation{ID: uuid.NewString(), InstanceID: id, Kind: in.Kind, Status: "waiting_approval", RequestHash: hash, Generation: snapshot.Generation, CreatedAt: now}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_operations(id,org_id,fleet_id,instance_id,kind,status,idempotency_key,request_hash,generation,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, op.ID, scope.OrgID, scope.FleetID, id, op.Kind, op.Status, in.IdempotencyKey, hash, op.Generation, now); err != nil {
		return nil, storeError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_fleet_instances SET desired_release_id=$1,profile_id=$2,desired_generation=$3,revision=revision+1,status='waiting_approval',updated_at=$4 WHERE org_id=$5 AND id=$6`, snapshot.ReleaseID, snapshot.ProfileID, snapshot.Generation, now, scope.OrgID, id); err != nil {
		return nil, err
	}
	if in.Scale != nil {
		// The assertion was checked before inserting the operation. Both changes
		// remain in this transaction; a failed policy reservation rolls back all.
		if policyErr := scalingStore.ReserveScalingPolicyTx(ctx, tx, scope, id, op.ID, op.Generation, instance.OwnershipEpoch, *in.Scale); policyErr != nil {
			return nil, policyErr
		}
	}
	raw, err := json.Marshal(ActionIntent{Principal: p, Input: in, Snapshot: *snapshot, PreviousStatus: instance.Status, Deadline: now.Add(24 * time.Hour)})
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_operation_inputs(operation_id,org_id,fleet_id,instance_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6)`, op.ID, scope.OrgID, scope.FleetID, id, string(raw), now); err != nil {
		return nil, storeError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_operation_outbox(operation_id,org_id,fleet_id,instance_id,next_at) VALUES($1,$2,$3,$4,$5)`, op.ID, scope.OrgID, scope.FleetID, id, now); err != nil {
		return nil, storeError(err)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	return op, nil
}

func actionSnapshotTx(ctx context.Context, tx driver.Tx, scope FleetScope, instance fleet.Instance, in fleet.ActionInput) (*InstanceSnapshot, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT p.payload FROM loomplane_fleet_operation_inputs p JOIN loomplane_fleet_operations o ON o.id=p.operation_id WHERE o.org_id=$1 AND o.fleet_id=$2 AND o.instance_id=$3 AND o.generation=$4 ORDER BY o.created_at DESC,o.id DESC LIMIT 1`, scope.OrgID, scope.FleetID, instance.ID, instance.DesiredGeneration).Scan(&raw); err != nil {
		return nil, storeError(err)
	}
	var saved struct {
		Snapshot InstanceSnapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		return nil, err
	}
	release, err := actionReleaseTx(ctx, tx, scope, instance.DesiredReleaseID)
	if err != nil {
		return nil, err
	}
	input := fleet.CreateInstanceInput{StageID: instance.StageID, ProfileID: instance.ProfileID, ReleaseID: instance.DesiredReleaseID, ExternalRef: instance.ExternalRef, Parameters: saved.Snapshot.Parameters, SecretRefs: saved.Snapshot.SecretRefs}
	previous, err := RenderInstance(*release, instance, input)
	if err != nil {
		return nil, err
	}
	want, err := json.Marshal(previous)
	if err != nil {
		return nil, err
	}
	got, err := json.Marshal(saved.Snapshot)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(want, got) {
		return nil, ErrConflict
	}
	instance.DesiredGeneration++
	if in.Update != nil {
		instance.DesiredReleaseID = in.Update.ReleaseID
		input.ReleaseID = in.Update.ReleaseID
		if in.Update.Parameters != nil {
			input.Parameters = in.Update.Parameters
		}
		if in.Update.SecretRefs != nil {
			input.SecretRefs = in.Update.SecretRefs
		}
		release, err = actionReleaseTx(ctx, tx, scope, in.Update.ReleaseID)
		if err != nil {
			return nil, err
		}
	}
	if in.Resize != nil {
		instance.ProfileID = in.Resize.ProfileID
		input.ProfileID = in.Resize.ProfileID
	}
	return RenderInstance(*release, instance, input)
}

func actionReleaseTx(ctx context.Context, tx driver.Tx, scope FleetScope, id string) (*fleet.TemplateRelease, error) {
	return scanRelease(tx.QueryRow(ctx, `SELECT id,fleet_id,digest,snapshot,created_at FROM loomplane_fleet_releases WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND id=$4`, scope.OrgID, scope.ProjectID, scope.FleetID, id))
}
