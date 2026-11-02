package deployments

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

var _ LifecycleJobStore = (*PgStore)(nil)

func (s *PgStore) lifecycleTransaction(ctx context.Context, fn func(driver.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // no-op after commit
	if checkErr := fn(tx); checkErr != nil {
		return checkErr
	}
	return tx.Commit()
}
func guardLifecycleOperation(ctx context.Context, tx driver.Tx, run LifecycleJobRun) error {
	if err := run.Validate(); err != nil {
		return err
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT true FROM loomplane_fleet_instances i JOIN loomplane_fleet_operations o ON o.org_id=i.org_id AND o.fleet_id=i.fleet_id AND o.instance_id=i.id
 WHERE i.org_id=$1 AND i.project_id=$2 AND i.fleet_id=$3 AND i.id=$4 AND i.environment_id=$5 AND i.desired_generation=$6 AND i.ownership_epoch=$7
 AND o.id=$8 AND o.generation=$6 AND o.status='running' FOR SHARE OF i FOR UPDATE OF o`, run.OrgID, run.ProjectID, run.FleetID, run.InstanceID, run.EnvironmentID, run.Generation, run.OwnershipEpoch, run.OperationID).Scan(&valid)
	if err != nil || !valid {
		return errors.Join(owned.ErrLifecycleAuthority, err)
	}
	return nil
}
func lifecycleState(ctx context.Context, tx driver.Tx, run LifecycleJobRun) (*LifecycleJobState, error) {
	var state LifecycleJobState
	var digest, raw string
	var attempt int
	err := tx.QueryRow(ctx, `SELECT digest,attempt,issued,cleaned,receipt FROM loomplane_fleet_job_runs WHERE org_id=$1 AND id=$2 FOR UPDATE`, run.OrgID, run.ID()).Scan(&digest, &attempt, &state.Issued, &state.Cleaned, &raw)
	if err != nil {
		return nil, err
	}
	if digest != run.Digest() || json.Unmarshal([]byte(raw), &state.Receipt) != nil || state.Receipt.Attempt != attempt {
		return nil, owned.ErrLifecycleAuthority
	}
	return &state, nil
}
func lifecycleReceipt(run LifecycleJobRun, attempt int) LifecycleJobReceipt {
	return LifecycleJobReceipt{RunID: run.ID(), Digest: run.Digest(), Name: run.AttemptName(attempt), NamespaceUID: run.NamespaceUID, Attempt: attempt, State: "pending"}
}
func (s *PgStore) BeginLifecycleJob(ctx context.Context, run LifecycleJobRun) (*LifecycleJobState, error) {
	var state *LifecycleJobState
	err := s.lifecycleTransaction(ctx, func(tx driver.Tx) error {
		if err := guardLifecycleOperation(ctx, tx, run); err != nil {
			return err
		}
		body, err := json.Marshal(run)
		if err != nil {
			return err
		}
		receipt, err := json.Marshal(lifecycleReceipt(run, 1))
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_job_runs(id,org_id,fleet_id,instance_id,operation_id,digest,body,attempt,receipt) VALUES($1,$2,$3,$4,$5,$6,$7,1,$8) ON CONFLICT(id) DO NOTHING`, run.ID(), run.OrgID, run.FleetID, run.InstanceID, run.OperationID, run.Digest(), string(body), string(receipt)); err != nil {
			return err
		}
		state, err = lifecycleState(ctx, tx, run)
		if err != nil {
			return err
		}
		if state.Cleaned && (state.Receipt.State == "succeeded" || state.Receipt.Attempt >= run.Job.MaxAttempts) {
			return nil
		}
		if run.Job.SchemaLock != "" {
			if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_schema_locks(org_id,resource_id,run_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, run.OrgID, run.SchemaLockID, run.ID()); err != nil {
				return err
			}
			var owner string
			if checkErr := tx.QueryRow(ctx, `SELECT run_id FROM loomplane_fleet_schema_locks WHERE org_id=$1 AND resource_id=$2 FOR UPDATE`, run.OrgID, run.SchemaLockID).Scan(&owner); checkErr != nil {
				return checkErr
			}
			if owner != run.ID() {
				return owned.ErrLifecyclePending
			}
		}
		if state.Cleaned && state.Receipt.State == "failed" {
			state = &LifecycleJobState{Receipt: lifecycleReceipt(run, state.Receipt.Attempt+1)}
			raw, encodeErr := json.Marshal(state.Receipt)
			if encodeErr != nil {
				return encodeErr
			}
			_, err = tx.Exec(ctx, `UPDATE loomplane_fleet_job_runs SET attempt=$3,issued=false,cleaned=false,receipt=$4 WHERE org_id=$1 AND id=$2`, run.OrgID, run.ID(), state.Receipt.Attempt, string(raw))
			return err
		}
		return nil
	})
	return state, err
}
func (s *PgStore) IssueLifecycleJob(ctx context.Context, run LifecycleJobRun, attempt int) (bool, error) {
	issued := false
	err := s.lifecycleTransaction(ctx, func(tx driver.Tx) error {
		if err := guardLifecycleOperation(ctx, tx, run); err != nil {
			return err
		}
		state, err := lifecycleState(ctx, tx, run)
		if err != nil {
			return err
		}
		if state.Receipt.Attempt != attempt || state.Receipt.Terminal() || state.Cleaned {
			return owned.ErrLifecycleAuthority
		}
		if state.Issued {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE loomplane_fleet_job_runs SET issued=true WHERE org_id=$1 AND id=$2`, run.OrgID, run.ID())
		issued = err == nil
		return err
	})
	return issued, err
}
func (s *PgStore) RecordLifecycleJob(ctx context.Context, run LifecycleJobRun, receipt LifecycleJobReceipt) error {
	if receipt.RunID != run.ID() || receipt.Digest != run.Digest() || receipt.Name != run.AttemptName(receipt.Attempt) || receipt.NamespaceUID != run.NamespaceUID || receipt.Attempt < 1 || receipt.Attempt > run.Job.MaxAttempts || receipt.JobUID == "" || receipt.SecretUID == "" || len(receipt.JobUID) > 128 || len(receipt.SecretUID) > 128 || len(receipt.Reason) > 256 || receipt.State != "pending" && !receipt.Terminal() {
		return owned.ErrLifecycleAuthority
	}
	return s.lifecycleTransaction(ctx, func(tx driver.Tx) error {
		state, err := lifecycleState(ctx, tx, run)
		if err != nil {
			return err
		}
		previous := state.Receipt
		if !state.Issued || state.Cleaned || previous.Attempt != receipt.Attempt || previous.JobUID != "" && previous.JobUID != receipt.JobUID || previous.SecretUID != "" && previous.SecretUID != receipt.SecretUID || previous.Terminal() && previous != receipt {
			return owned.ErrLifecycleAuthority
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		if receipt.Terminal() {
			if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_job_receipts(run_id,attempt,body) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, run.ID(), receipt.Attempt, string(raw)); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE loomplane_fleet_job_runs SET receipt=$3 WHERE org_id=$1 AND id=$2`, run.OrgID, run.ID(), string(raw))
		return err
	})
}
func (s *PgStore) CleanLifecycleJob(ctx context.Context, run LifecycleJobRun, receipt LifecycleJobReceipt) error {
	return s.lifecycleTransaction(ctx, func(tx driver.Tx) error {
		state, err := lifecycleState(ctx, tx, run)
		if err != nil {
			return err
		}
		if !receipt.Terminal() || state.Receipt != receipt {
			return owned.ErrLifecycleAuthority
		}
		if _, err = tx.Exec(ctx, `UPDATE loomplane_fleet_job_runs SET cleaned=true WHERE org_id=$1 AND id=$2`, run.OrgID, run.ID()); err != nil {
			return err
		}
		if receipt.State == "succeeded" || receipt.Attempt >= run.Job.MaxAttempts {
			_, err = tx.Exec(ctx, `DELETE FROM loomplane_fleet_schema_locks WHERE org_id=$1 AND resource_id=$2 AND run_id=$3`, run.OrgID, run.SchemaLockID, run.ID())
		}
		return err
	})
}
