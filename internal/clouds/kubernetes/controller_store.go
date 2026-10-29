package kubernetes

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
)

var ErrControllerMissing = errors.New("no trusted Kubernetes controller receipt")

type ControllerStore struct{ db *pgdriver.PgDB }

func NewControllerStore(db *pgdriver.PgDB) *ControllerStore { return &ControllerStore{db: db} }

// RecordController accepts evidence only from the successful runtime mutation.
// Retries retain one receipt per runtime attempt. A replaced UID needs a fresh
// attempt; older generations and same-generation changes never replace evidence.
func (s *ControllerStore) RecordController(ctx context.Context, r ControllerReceipt) (err error) {
	if !r.Valid() {
		return ErrOwned
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	var details []byte
	if readErr := tx.QueryRow(ctx, `SELECT c.details FROM loomplane_cloud_connections c JOIN loomplane_environments e ON e.org_id=c.org_id WHERE c.org_id=$1 AND c.id=$2 AND c.cloud='kubernetes' AND c.status='ready' AND e.project_id=$3 AND e.id=$4 FOR SHARE OF c,e`, r.OrgID, r.ConnectionID, r.ProjectID, r.EnvironmentID).Scan(&details); readErr != nil {
		return readErr
	}
	var identity map[string]string
	if json.Unmarshal(details, &identity) != nil || identity["cluster_ca"] != r.ClusterCA || identity["namespace_uid"] != r.NamespaceUID || identity["namespace"] != r.Namespace {
		return ErrOwned
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `INSERT INTO loomplane_workload_controllers(org_id,project_id,environment_id,connection_id,instance_id,runtime_ref,body) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(org_id,connection_id,instance_id,runtime_ref) DO NOTHING`, r.OrgID, r.ProjectID, r.EnvironmentID, r.ConnectionID, r.InstanceID, r.RuntimeRef, raw)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		var previous []byte
		if readErr := tx.QueryRow(ctx, `SELECT body FROM loomplane_workload_controllers WHERE org_id=$1 AND connection_id=$2 AND instance_id=$3 AND runtime_ref=$4 FOR UPDATE`, r.OrgID, r.ConnectionID, r.InstanceID, r.RuntimeRef).Scan(&previous); readErr != nil {
			return readErr
		}
		var old ControllerReceipt
		if json.Unmarshal(previous, &old) != nil || !old.Valid() {
			return ErrOwned
		}
		if old == r {
			return tx.Commit()
		}
		expected := old
		expected.DeploymentGeneration = r.DeploymentGeneration
		expected.RuntimeRevision = r.RuntimeRevision
		expected.ReleaseID = r.ReleaseID
		if expected != r || r.DeploymentGeneration <= old.DeploymentGeneration {
			return ErrOwned
		}
		if _, err = tx.Exec(ctx, `UPDATE loomplane_workload_controllers SET body=$5,updated_at=clock_timestamp() WHERE org_id=$1 AND connection_id=$2 AND instance_id=$3 AND runtime_ref=$4`, r.OrgID, r.ConnectionID, r.InstanceID, r.RuntimeRef, raw); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `SELECT loomplane_append_event($1,$2,'workload.controller.recorded',$3,$4)`, r.OrgID, r.ProjectID, r.InstanceID, raw); err != nil {
		return err
	}
	return tx.Commit()
}

// GetController requires the exact server-owned attempt reference. It performs
// no discovery, adopts no live object and grants no authority on its own.
func (s *ControllerStore) GetController(ctx context.Context, org, connection, instance, runtimeRef string) (*ControllerReceipt, error) {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT body FROM loomplane_workload_controllers WHERE org_id=$1 AND connection_id=$2 AND instance_id=$3 AND runtime_ref=$4`, org, connection, instance, runtimeRef).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrControllerMissing
		}
		return nil, err
	}
	var r ControllerReceipt
	if json.Unmarshal(raw, &r) != nil || !r.Valid() || r.OrgID != org || r.ConnectionID != connection || r.InstanceID != instance || r.RuntimeRef != runtimeRef {
		return nil, ErrOwned
	}
	return &r, nil
}
