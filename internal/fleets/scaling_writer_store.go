package fleets

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
)

// ScalingWriterJournal is historical recovery input. Neither the desired policy
// nor its last recorded observation establishes current execution authority.
type ScalingWriterJournal struct {
	Policy    ScalingPolicyRecord
	LastEvent *ScalingWriterEvent
}

// GetScalingWriterJournal reads an immutable policy and a committed event prefix.
// A pending policy has no event. Recovery rechecks current authority and observes
// these saved native identities before retrying any ambiguous provider effect.
func (s *PgStore) GetScalingWriterJournal(ctx context.Context, scope FleetScope, instanceID, operationID, service string, revision int64) (*ScalingWriterJournal, error) {
	if scope.OrgID == "" || scope.ProjectID == "" || scope.FleetID == "" || instanceID == "" || operationID == "" || service == "" || revision < 1 {
		return nil, ErrInvalid
	}
	out := &ScalingWriterJournal{Policy: ScalingPolicyRecord{Scope: scope, InstanceID: instanceID, OperationID: operationID}}
	p := &out.Policy
	var input string
	err := s.db.QueryRow(ctx, `SELECT environment_id,generation,ownership_epoch,input,created_at FROM loomplane_fleet_scaling_policies WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND instance_id=$4 AND operation_id=$5 AND service=$6 AND revision=$7`, scope.OrgID, scope.ProjectID, scope.FleetID, instanceID, operationID, service, revision).Scan(&p.EnvironmentID, &p.Generation, &p.OwnershipEpoch, &input, &p.CreatedAt)
	if err != nil {
		return nil, storeError(err)
	}
	if json.Unmarshal([]byte(input), &p.Input) != nil || p.Input.Service != service || p.Input.Policy.Revision != revision {
		return nil, ErrInvalid
	}
	if checkErr := p.Input.Validate(); checkErr != nil {
		return nil, checkErr
	}
	p.CreatedAt = p.CreatedAt.UTC()
	var sequence int64
	var payload string
	err = s.db.QueryRow(ctx, `SELECT sequence,payload FROM loomplane_fleet_scaling_writer_events WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND instance_id=$4 AND operation_id=$5 AND service=$6 AND policy_revision=$7 ORDER BY sequence DESC LIMIT 1`, scope.OrgID, scope.ProjectID, scope.FleetID, instanceID, operationID, service, revision).Scan(&sequence, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out.LastEvent = new(ScalingWriterEvent)
	if json.Unmarshal([]byte(payload), out.LastEvent) != nil || out.LastEvent.Sequence != sequence {
		return nil, ErrInvalid
	}
	return out, nil
}

// AdvanceScalingWriterTx appends one verified step to the current scale operation.
// The caller commits its domain transition in this transaction. A returned event
// is a journal acknowledgement, never a token authorizing a provider mutation.
// Exact replay returns the recorded step without renewing its proof or authority.
func (s *PgStore) AdvanceScalingWriterTx(ctx context.Context, tx driver.Tx, scope FleetScope, instanceID, operationID, service string, revision int64, next ScalingWriterEvent, verifier ScalingWriterVerifier) (*ScalingWriterEvent, error) {
	if verifier == nil {
		return nil, ErrScalingQualification
	}
	if operationID == "" || service == "" || revision < 1 {
		return nil, ErrInvalid
	}
	i, err := lockScalingInstanceTx(ctx, tx, scope, instanceID)
	if err != nil {
		return nil, err
	}
	p := ScalingPolicyRecord{Scope: scope, InstanceID: instanceID}
	var input string
	var currentRevision int64
	err = tx.QueryRow(ctx, `SELECT environment_id,operation_id,generation,ownership_epoch,input,created_at,revision FROM loomplane_fleet_scaling_policies WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND instance_id=$4 AND service=$5 ORDER BY revision DESC LIMIT 1 FOR UPDATE`, scope.OrgID, scope.ProjectID, scope.FleetID, instanceID, service).Scan(&p.EnvironmentID, &p.OperationID, &p.Generation, &p.OwnershipEpoch, &input, &p.CreatedAt, &currentRevision)
	if err != nil {
		return nil, storeError(err)
	}
	if p.OperationID != operationID || p.Generation != i.generation || p.OwnershipEpoch != i.epoch || p.EnvironmentID != i.environment || currentRevision != revision {
		return nil, ErrConflict
	}
	if err = json.Unmarshal([]byte(input), &p.Input); err != nil {
		return nil, ErrInvalid
	}
	if p.Input.Service != service || p.Input.Policy.Revision != revision {
		return nil, ErrConflict
	}
	if checkErr := p.Input.Validate(); checkErr != nil {
		return nil, checkErr
	}
	var kind, status string
	var opGeneration int64
	err = tx.QueryRow(ctx, `SELECT kind,status,generation FROM loomplane_fleet_operations WHERE org_id=$1 AND fleet_id=$2 AND instance_id=$3 AND id=$4 FOR UPDATE`, scope.OrgID, scope.FleetID, instanceID, operationID).Scan(&kind, &status, &opGeneration)
	if err != nil {
		return nil, storeError(err)
	}
	if kind != "scale" || status != "provisioning" || opGeneration != p.Generation {
		return nil, ErrConflict
	}
	var sequence int64
	var previousJSON string
	err = tx.QueryRow(ctx, `SELECT sequence,payload FROM loomplane_fleet_scaling_writer_events WHERE org_id=$1 AND instance_id=$2 AND service=$3 AND policy_revision=$4 ORDER BY sequence DESC LIMIT 1 FOR UPDATE`, scope.OrgID, instanceID, service, revision).Scan(&sequence, &previousJSON)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	next.ObservedAt = next.ObservedAt.UTC()
	next.ExpiresAt = next.ExpiresAt.UTC()
	raw, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	if len(raw) > 16384 {
		return nil, ErrInvalid
	}
	var previous *ScalingWriterEvent
	if sequence > 0 {
		previous = new(ScalingWriterEvent)
		if json.Unmarshal([]byte(previousJSON), previous) != nil || previous.Sequence != sequence {
			return nil, ErrInvalid
		}
		if next.Sequence == sequence {
			if string(raw) != previousJSON {
				return nil, ErrConflict
			}
			return previous, nil
		}
	}
	if checkErr := validateScalingWriterTransition(p, previous, next, time.Now().UTC()); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := verifier.VerifyScalingWriterTransition(ctx, tx, p, next); checkErr != nil {
		return nil, checkErr
	}
	// A slow receipt lookup must not admit a proof that expired during validation.
	if checkErr := validateScalingWriterTransition(p, previous, next, time.Now().UTC()); checkErr != nil {
		return nil, checkErr
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_scaling_writer_events(org_id,project_id,fleet_id,instance_id,operation_id,service,policy_revision,sequence,phase,proof_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, scope.OrgID, scope.ProjectID, scope.FleetID, instanceID, operationID, service, revision, next.Sequence, next.Phase, next.ProofID, string(raw), time.Now().UTC())
	if err != nil {
		return nil, storeError(err)
	}
	return &next, nil
}
