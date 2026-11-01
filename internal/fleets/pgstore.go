package fleets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/pkg/fleet"
)

// PgStore enforces fleet scope and admission in PostgreSQL transactions.
type PgStore struct{ db *pgdriver.PgDB }

// NewPgStore uses the application's existing database pool.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

func rollback(tx driver.Tx, err error) error {
	if err == nil {
		return nil
	}
	if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
		return errors.Join(err, rollbackErr)
	}
	return err
}
func storeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505":
			return ErrConflict
		case "23503":
			return ErrNotFound
		case "23514":
			return ErrInvalid
		}
	}
	return err
}

// CreateFleet atomically establishes ownership and updates the project mode.
func (s *PgStore) CreateFleet(ctx context.Context, f *fleet.Fleet) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = rollback(tx, err) }()
	// Order the global source gate before parent and quota row locks.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('variable-sources/v1',0))`); err != nil {
		return err
	}
	var project string
	if err = tx.QueryRow(ctx, `SELECT id FROM loomplane_projects WHERE org_id=$1 AND id=$2 FOR UPDATE`, f.OrgID, f.ProjectID).Scan(&project); err != nil {
		return storeError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleets(id,org_id,project_id,control_project_id,revision,status,created_at,updated_at) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8)`, f.ID, f.OrgID, f.ProjectID, f.ControlProjectID, f.Revision, f.Status, f.CreatedAt, f.UpdatedAt); err != nil {
		return storeError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_projects SET deployment_mode='fleet',updated_at=$3 WHERE org_id=$1 AND id=$2`, f.OrgID, f.ProjectID, f.UpdatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// GetFleet never resolves a fleet outside the supplied organization.
func (s *PgStore) GetFleet(ctx context.Context, org, id string) (*fleet.Fleet, error) {
	var f fleet.Fleet
	err := s.db.QueryRow(ctx, `SELECT id,org_id,project_id,coalesce(control_project_id,''),revision,status,created_at,updated_at FROM loomplane_fleets WHERE org_id=$1 AND id=$2`, org, id).Scan(&f.ID, &f.OrgID, &f.ProjectID, &f.ControlProjectID, &f.Revision, &f.Status, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, storeError(err)
	}
	f.CreatedAt, f.UpdatedAt = f.CreatedAt.UTC(), f.UpdatedAt.UTC()
	return &f, nil
}

// CreateStage validates every Cloud attachment in the same transaction.
func (s *PgStore) CreateStage(ctx context.Context, scope FleetScope, stage *fleet.Stage) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = rollback(tx, err) }()
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_stages(id,org_id,project_id,fleet_id,name,environment_kind,max_instances,policy_revision,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, stage.ID, scope.OrgID, scope.ProjectID, scope.FleetID, stage.Name, stage.EnvironmentKind, stage.MaxInstances, stage.PolicyRevision, stage.CreatedAt); err != nil {
		return storeError(err)
	}
	for _, cloud := range stage.AllowedCloudIDs {
		if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_stage_clouds(org_id,fleet_id,stage_id,cloud_id) VALUES($1,$2,$3,$4)`, scope.OrgID, scope.FleetID, stage.ID, cloud); err != nil {
			return storeError(err)
		}
	}
	return tx.Commit()
}

const instanceColumns = `id,fleet_id,stage_id,environment_id,external_ref,profile_id,desired_release_id,observed_release_id,status,revision,desired_generation,observed_generation,ownership_epoch,created_at,updated_at`

type scanner interface{ Scan(...any) error }

func scanInstance(row scanner) (*fleet.Instance, error) {
	var i fleet.Instance
	if err := row.Scan(&i.ID, &i.FleetID, &i.StageID, &i.EnvironmentID, &i.ExternalRef, &i.ProfileID, &i.DesiredReleaseID, &i.ObservedReleaseID, &i.Status, &i.Revision, &i.DesiredGeneration, &i.ObservedGeneration, &i.OwnershipEpoch, &i.CreatedAt, &i.UpdatedAt); err != nil {
		return nil, storeError(err)
	}
	i.CreatedAt, i.UpdatedAt = i.CreatedAt.UTC(), i.UpdatedAt.UTC()
	return &i, nil
}

// ManagedEnvironment resolves the immutable environment-to-instance mapping.
func (s *PgStore) ManagedEnvironment(ctx context.Context, org, environment string) (*fleet.Instance, error) {
	return scanInstance(s.db.QueryRow(ctx, `SELECT `+instanceColumns+` FROM loomplane_fleet_instances WHERE org_id=$1 AND environment_id=$2`, org, environment))
}

func reservationHash(in fleet.CreateInstanceInput) (string, error) {
	for _, v := range []string{in.StageID, in.ReleaseID, in.ProfileID, in.ExternalRef, in.IdempotencyKey} {
		if strings.TrimSpace(v) == "" || len(v) > 128 {
			return "", ErrInvalid
		}
	}
	for _, v := range in.Parameters {
		count := 0
		if v.StringValue != nil {
			count++
		}
		if v.IntValue != nil {
			count++
		}
		if v.BoolValue != nil {
			count++
		}
		validKind := (v.Kind == "string" && v.StringValue != nil) || (v.Kind == "integer" && v.IntValue != nil) || (v.Kind == "boolean" && v.BoolValue != nil)
		if count != 1 || !validKind {
			return "", ErrInvalid
		}
	}
	for _, v := range in.SecretRefs {
		if v.ID == "" || v.Version < 1 {
			return "", ErrInvalid
		}
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

// ReserveInstance reserves identity and quota only. It performs no cloud work.
// Callers must validate release and operation authority before scheduling it.
func (s *PgStore) ReserveInstance(ctx context.Context, scope FleetScope, in fleet.CreateInstanceInput) (result *fleet.OperationResult, err error) {
	hash, err := reservationHash(in)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = rollback(tx, err) }()
	// Order the global source gate before parent and quota row locks.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('variable-sources/v1',0))`); err != nil {
		return nil, err
	}
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM loomplane_fleets WHERE org_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, scope.OrgID, scope.ProjectID, scope.FleetID).Scan(&status); err != nil {
		return nil, storeError(err)
	}
	replay, err := reservationReplay(ctx, tx, scope, in.IdempotencyKey, hash)
	if err != nil {
		return nil, err
	}
	if replay != nil {
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, commitErr
		}
		return replay, nil
	}
	if status != "active" {
		return nil, ErrConflict
	}
	var exists bool
	if lookupErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_fleet_instances WHERE org_id=$1 AND fleet_id=$2 AND external_ref=$3)`, scope.OrgID, scope.FleetID, in.ExternalRef).Scan(&exists); lookupErr != nil {
		return nil, lookupErr
	}
	if exists {
		return nil, ErrConflict
	}
	var maximum, count int
	var kind string
	if err = tx.QueryRow(ctx, `SELECT max_instances,environment_kind FROM loomplane_fleet_stages WHERE org_id=$1 AND fleet_id=$2 AND id=$3 FOR UPDATE`, scope.OrgID, scope.FleetID, in.StageID).Scan(&maximum, &kind); err != nil {
		return nil, storeError(err)
	}
	if countErr := tx.QueryRow(ctx, `SELECT count(*) FROM loomplane_fleet_instances WHERE org_id=$1 AND fleet_id=$2 AND stage_id=$3 AND status<>'deleted'`, scope.OrgID, scope.FleetID, in.StageID).Scan(&count); countErr != nil {
		return nil, countErr
	}
	if count >= maximum {
		return nil, ErrCapacity
	}
	now := time.Now().UTC()
	i := fleet.Instance{ID: uuid.NewString(), FleetID: scope.FleetID, StageID: in.StageID, EnvironmentID: uuid.NewString(), ExternalRef: in.ExternalRef, ProfileID: in.ProfileID, DesiredReleaseID: in.ReleaseID, Status: "waiting_approval", Revision: 1, DesiredGeneration: 1, OwnershipEpoch: 1, CreatedAt: now, UpdatedAt: now}
	op := fleet.Operation{ID: uuid.NewString(), InstanceID: i.ID, Kind: "create", Status: "waiting_approval", RequestHash: hash, Generation: 1, CreatedAt: now}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$6)`, i.EnvironmentID, scope.OrgID, scope.ProjectID, "instance-"+i.ID, kind, now); err != nil {
		return nil, storeError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_instances(id,org_id,project_id,fleet_id,stage_id,environment_id,external_ref,profile_id,desired_release_id,status,revision,desired_generation,observed_generation,ownership_epoch,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,1,1,0,1,$11,$11)`, i.ID, scope.OrgID, scope.ProjectID, scope.FleetID, i.StageID, i.EnvironmentID, i.ExternalRef, i.ProfileID, i.DesiredReleaseID, i.Status, now); err != nil {
		return nil, storeError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_operations(id,org_id,fleet_id,instance_id,kind,status,idempotency_key,request_hash,generation,created_at) VALUES($1,$2,$3,$4,'create',$5,$6,$7,1,$8)`, op.ID, scope.OrgID, scope.FleetID, i.ID, op.Status, in.IdempotencyKey, hash, now); err != nil {
		return nil, storeError(err)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("reserve instance: %w", err)
	}
	return &fleet.OperationResult{Instance: i, Operation: op}, nil
}

func reservationReplay(ctx context.Context, tx driver.Tx, scope FleetScope, key, hash string) (*fleet.OperationResult, error) {
	var op fleet.Operation
	err := tx.QueryRow(ctx, `SELECT id,instance_id,kind,status,request_hash,generation,error_code,created_at FROM loomplane_fleet_operations WHERE org_id=$1 AND fleet_id=$2 AND kind='create' AND idempotency_key=$3`, scope.OrgID, scope.FleetID, key).Scan(&op.ID, &op.InstanceID, &op.Kind, &op.Status, &op.RequestHash, &op.Generation, &op.ErrorCode, &op.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if op.RequestHash != hash {
		return nil, ErrConflict
	}
	i, err := scanInstance(tx.QueryRow(ctx, `SELECT `+instanceColumns+` FROM loomplane_fleet_instances WHERE org_id=$1 AND fleet_id=$2 AND id=$3`, scope.OrgID, scope.FleetID, op.InstanceID))
	if err != nil {
		return nil, err
	}
	op.CreatedAt = op.CreatedAt.UTC()
	return &fleet.OperationResult{Instance: *i, Operation: op}, nil
}
