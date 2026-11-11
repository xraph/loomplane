package fleets

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/pkg/fleet"
)

// ScalingPolicyRecord is immutable desired state. Its presence grants no writer
// authority; the journal records each verified transfer step separately.
type ScalingPolicyRecord struct {
	Scope          FleetScope
	InstanceID     string
	EnvironmentID  string
	OperationID    string
	Generation     int64
	OwnershipEpoch int64
	Input          fleet.ScaleInput
	CreatedAt      time.Time
}

type scalingInstance struct {
	generation, epoch   int64
	environment, status string
}

func lockScalingInstanceTx(ctx context.Context, tx driver.Tx, scope FleetScope, instanceID string) (scalingInstance, error) {
	var i scalingInstance
	if tx == nil || scope.OrgID == "" || scope.ProjectID == "" || scope.FleetID == "" || instanceID == "" {
		return i, ErrInvalid
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM loomplane_fleets WHERE org_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, scope.OrgID, scope.ProjectID, scope.FleetID).Scan(&status); err != nil {
		return i, storeError(err)
	}
	if status != "active" {
		return i, ErrConflict
	}
	err := tx.QueryRow(ctx, `SELECT desired_generation,ownership_epoch,environment_id,status FROM loomplane_fleet_instances WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND id=$4 FOR UPDATE`, scope.OrgID, scope.ProjectID, scope.FleetID, instanceID).Scan(&i.generation, &i.epoch, &i.environment, &i.status)
	if err != nil {
		return i, storeError(err)
	}
	if i.status == "deleted" || i.status == "retired" {
		return i, ErrConflict
	}
	return i, nil
}

func checkScalingRevisionTx(ctx context.Context, tx driver.Tx, scope FleetScope, instanceID, service string, expected int64) error {
	var revision int64
	err := tx.QueryRow(ctx, `SELECT revision FROM loomplane_fleet_scaling_policies WHERE org_id=$1 AND fleet_id=$2 AND instance_id=$3 AND service=$4 ORDER BY revision DESC LIMIT 1 FOR UPDATE`, scope.OrgID, scope.FleetID, instanceID, service).Scan(&revision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if revision != expected {
		return ErrConflict
	}
	return nil
}

// CheckScalingPolicyRevisionTx locks parent, instance and policy in that order.
// The instance lock also serializes the first revision when no policy exists.
func CheckScalingPolicyRevisionTx(ctx context.Context, tx driver.Tx, scope FleetScope, instanceID, service string, expected int64) error {
	if expected < 0 || expected == math.MaxInt64 {
		return ErrInvalid
	}
	probe := fleet.ScaleInput{Service: service, ExpectedPolicyRevision: expected, Policy: fleet.ScalingPolicy{Version: 1, Revision: expected + 1, Mode: "manual", MinReplicas: 1, MaxReplicas: 1}}
	if err := probe.Validate(); err != nil {
		return err
	}
	if _, err := lockScalingInstanceTx(ctx, tx, scope, instanceID); err != nil {
		return err
	}
	return checkScalingRevisionTx(ctx, tx, scope, instanceID, service, expected)
}

// ReserveScalingPolicyTx joins the action transaction after it inserts the
// operation. Replay belongs to that transaction and precedes this reservation.
// Failed and cancelled revisions remain in history and consume their revision.
func (s *PgStore) ReserveScalingPolicyTx(ctx context.Context, tx driver.Tx, scope FleetScope, instanceID, operationID string, generation, epoch int64, in fleet.ScaleInput) error {
	if err := in.Validate(); err != nil {
		return err
	}
	if operationID == "" || generation < 1 || epoch < 1 {
		return ErrInvalid
	}
	i, err := lockScalingInstanceTx(ctx, tx, scope, instanceID)
	if err != nil {
		return err
	}
	if i.generation != generation || i.epoch != epoch {
		return ErrConflict
	}
	var kind, status string
	var opGeneration int64
	if err = tx.QueryRow(ctx, `SELECT kind,status,generation FROM loomplane_fleet_operations WHERE org_id=$1 AND fleet_id=$2 AND instance_id=$3 AND id=$4 FOR UPDATE`, scope.OrgID, scope.FleetID, instanceID, operationID).Scan(&kind, &status, &opGeneration); err != nil {
		return storeError(err)
	}
	if kind != "scale" || status != "waiting_approval" || opGeneration != generation {
		return ErrConflict
	}
	if checkErr := checkScalingRevisionTx(ctx, tx, scope, instanceID, in.Service, in.ExpectedPolicyRevision); checkErr != nil {
		return checkErr
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_scaling_policies(org_id,project_id,fleet_id,instance_id,environment_id,operation_id,service,revision,generation,ownership_epoch,input,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, scope.OrgID, scope.ProjectID, scope.FleetID, instanceID, i.environment, operationID, in.Service, in.Policy.Revision, generation, epoch, string(raw), time.Now().UTC())
	return storeError(err)
}
