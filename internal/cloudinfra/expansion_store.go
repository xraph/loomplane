package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/security"
)

// withExpansionLease bounds each pool worker and fences every persisted change.
// Provider calls use the existing operation fence after this short lease claim.
func (s *PgStore) withExpansionLease(ctx context.Context, org, cloud, pool string, run func(context.Context, string) error) error {
	token, err := randomID("lease")
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO loomplane_cloud_expansion_leases(org_id,cloud_id,pool_id,owner,expires_at) VALUES($1,$2,$3,$4,now()+interval '90 seconds') ON CONFLICT(org_id,cloud_id,pool_id) DO UPDATE SET owner=excluded.owner,expires_at=excluded.expires_at WHERE loomplane_cloud_expansion_leases.expires_at<now()`, org, cloud, pool, token)
	if err != nil {
		return storeError(err)
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrBusy
	}
	defer func() {
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = s.db.Exec(release, `DELETE FROM loomplane_cloud_expansion_leases WHERE org_id=$1 AND cloud_id=$2 AND pool_id=$3 AND owner=$4`, org, cloud, pool, token) //nolint:errcheck // The lease expires if cancellation prevents cleanup.
	}()
	bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	return run(bounded, token)
}
func expansionLeaseTx(ctx context.Context, tx driver.Tx, e *Expansion, token string) error {
	var valid bool
	if scanErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cloud_expansion_leases WHERE org_id=$1 AND cloud_id=$2 AND pool_id=$3 AND owner=$4 AND expires_at>now())`, e.OrgID, e.CloudID, e.PoolID, token).Scan(&valid); scanErr != nil {
		return scanErr
	}
	if !valid {
		return ErrBusy
	}
	return nil
}
func (s *PgStore) expansionAuthority(ctx context.Context, org, cloud string, revision int64) (security.Principal, error) {
	var raw []byte
	var p security.Principal
	if scanErr := s.db.QueryRow(ctx, `SELECT authority FROM loomplane_cloud_capacity_policies WHERE org_id=$1 AND cloud_id=$2 AND revision=$3`, org, cloud, revision).Scan(&raw); scanErr != nil {
		return p, storeError(scanErr)
	}
	if decodeErr := json.Unmarshal(raw, &p); decodeErr != nil {
		return p, decodeErr
	}
	if p.OrgID != org || p.UserID == "" || p.ReadOnly {
		return p, security.ErrForbidden
	}
	return p, nil
}
func (s *PgStore) GetExpansion(ctx context.Context, org, cloud, id string) (*Expansion, error) {
	var raw []byte
	if scanErr := s.db.QueryRow(ctx, `SELECT body FROM loomplane_cloud_expansions WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, org, cloud, id).Scan(&raw); scanErr != nil {
		return nil, storeError(scanErr)
	}
	var e Expansion
	if decodeErr := json.Unmarshal(raw, &e); decodeErr != nil {
		return nil, decodeErr
	}
	return &e, nil
}
func (s *PgStore) poolExpansions(ctx context.Context, org, cloud, pool string) ([]*Expansion, error) {
	rows, err := s.db.Query(ctx, `SELECT body FROM loomplane_cloud_expansions WHERE org_id=$1 AND cloud_id=$2 AND pool_id=$3 AND status<>'cancelled' ORDER BY created_at,id`, org, cloud, pool)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Expansion{}
	for rows.Next() {
		var raw []byte
		var e Expansion
		if scanErr := rows.Scan(&raw); scanErr != nil {
			return nil, scanErr
		}
		if decodeErr := json.Unmarshal(raw, &e); decodeErr != nil {
			return nil, decodeErr
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}
func (s *PgStore) expansionDemands(ctx context.Context, e *Expansion) ([]CapacityDemand, error) {
	rows, err := s.db.Query(ctx, `SELECT body FROM loomplane_cloud_expansion_demands WHERE org_id=$1 AND cloud_id=$2 AND expansion_id=$3 ORDER BY intent_hash`, e.OrgID, e.CloudID, e.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CapacityDemand{}
	for rows.Next() {
		var raw []byte
		var d CapacityDemand
		if scanErr := rows.Scan(&raw); scanErr != nil {
			return nil, scanErr
		}
		if decodeErr := json.Unmarshal(raw, &d); decodeErr != nil {
			return nil, decodeErr
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *PgStore) demandExpansion(ctx context.Context, d CapacityDemand) (*Expansion, error) {
	var id string
	if scanErr := s.db.QueryRow(ctx, `SELECT expansion_id FROM loomplane_cloud_expansion_demands WHERE org_id=$1 AND cloud_id=$2 AND intent_hash=$3 AND plan_id=$4 AND service=$5`, d.OrgID, d.CloudID, d.IntentHash, d.PlanID, d.Service).Scan(&id); scanErr != nil {
		return nil, storeError(scanErr)
	}
	return s.GetExpansion(ctx, d.OrgID, d.CloudID, id)
}
func expansionPolicyTx(ctx context.Context, tx driver.Tx, e *Expansion) (*CapacityPolicy, error) {
	p, err := capacityPolicyTx(ctx, tx, e.OrgID, e.CloudID)
	if err != nil {
		return nil, err
	}
	if !p.Enabled || p.Revision != e.PolicyRevision {
		return nil, &CapacityAdmissionError{Block: *capacityBlock(p, CapacityDemand{CloudID: e.CloudID, PoolID: e.PoolID}, "review_required", "policy")}
	}
	for _, pool := range p.Pools {
		if pool.PoolID == e.PoolID && sameExpansionTemplate(pool, e.Template) {
			return p, nil
		}
	}
	return nil, ErrConflict
}
func (s *PgStore) createExpansion(ctx context.Context, e *Expansion, token string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if scopeErr := lockCapacityCloud(ctx, tx, e.OrgID, e.CloudID); scopeErr != nil {
		return scopeErr
	}
	if leaseErr := expansionLeaseTx(ctx, tx, e, token); leaseErr != nil {
		return leaseErr
	}
	if _, policyErr := expansionPolicyTx(ctx, tx, e); policyErr != nil {
		return policyErr
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_expansions(org_id,cloud_id,id,pool_id,status,intent_hash,policy_revision,servers,cpu_millis,memory_bytes,storage_bytes,provider,account_id,connection_id,connection_generation,region,size,architecture,image,body,revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`, e.OrgID, e.CloudID, e.ID, e.PoolID, e.Status, e.IntentHash, e.PolicyRevision, e.Resources.Servers, e.Resources.CPUMillis, e.Resources.MemoryBytes, e.Resources.StorageBytes, e.Provider, e.AccountID, e.ConnectionID, e.ConnectionGeneration, e.Region, e.Size, e.Architecture, e.Image, raw, e.Revision)
	if err != nil {
		return storeError(err)
	}
	return tx.Commit()
}
func saveExpansionTx(ctx context.Context, tx driver.Tx, e *Expansion, previous int64) error {
	next := *e
	next.Revision = previous + 1
	next.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE loomplane_cloud_expansions SET status=$4,body=$5,revision=$6,operation_id=nullif($7,''),approved_by=nullif($8,''),approved_at=$9,policy_revision=$11,updated_at=now() WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND revision=$10`, e.OrgID, e.CloudID, e.ID, e.Status, raw, next.Revision, e.OperationID, e.ApprovedBy, e.ApprovedAt, previous, e.PolicyRevision)
	if err != nil {
		return storeError(err)
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	*e = next
	return nil
}
func (s *PgStore) saveExpansion(ctx context.Context, e *Expansion, token string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if scopeErr := lockCapacityCloud(ctx, tx, e.OrgID, e.CloudID); scopeErr != nil {
		return scopeErr
	}
	if leaseErr := expansionLeaseTx(ctx, tx, e, token); leaseErr != nil {
		return leaseErr
	}
	if saveErr := saveExpansionTx(ctx, tx, e, e.Revision); saveErr != nil {
		return saveErr
	}
	return tx.Commit()
}
func (s *PgStore) attachExpansionDemand(ctx context.Context, e *Expansion, d CapacityDemand, token string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if scopeErr := lockCapacityCloud(ctx, tx, e.OrgID, e.CloudID); scopeErr != nil {
		return scopeErr
	}
	var cancelled bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_deployments WHERE org_id=$1 AND plan_id=$2 AND status='cancelled')`, d.OrgID, d.PlanID).Scan(&cancelled); checkErr != nil {
		return checkErr
	}
	if cancelled {
		return security.ErrForbidden
	}
	if leaseErr := expansionLeaseTx(ctx, tx, e, token); leaseErr != nil {
		return leaseErr
	}
	var raw []byte
	if scanErr := tx.QueryRow(ctx, `SELECT body FROM loomplane_cloud_expansions WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, e.OrgID, e.CloudID, e.ID).Scan(&raw); scanErr != nil {
		return storeError(scanErr)
	}
	var current Expansion
	if decodeErr := json.Unmarshal(raw, &current); decodeErr != nil {
		return decodeErr
	}
	if current.Status == "cancelled" || current.Status == "blocked" || current.Status == "ready" {
		return ErrConflict
	}
	rows, err := tx.Query(ctx, `SELECT body FROM loomplane_cloud_expansion_demands WHERE org_id=$1 AND cloud_id=$2 AND expansion_id=$3`, e.OrgID, e.CloudID, e.ID)
	if err != nil {
		return err
	}
	var used ResourceVector
	exists := false
	for rows.Next() {
		var b []byte
		var other CapacityDemand
		if scanErr := rows.Scan(&b); scanErr != nil {
			rows.Close()
			return scanErr
		}
		if decodeErr := json.Unmarshal(b, &other); decodeErr != nil {
			rows.Close()
			return decodeErr
		}
		if other.IntentHash == d.IntentHash {
			exists = other == d
			if !exists {
				rows.Close()
				return ErrConflict
			}
			continue
		}
		used, err = addCapacity(used, other.Resources)
		if err != nil {
			rows.Close()
			return err
		}
	}
	rows.Close()
	if rowsErr := rows.Err(); rowsErr != nil {
		return rowsErr
	}
	if exists {
		return tx.Commit()
	}
	if !expansionDemandFits(current.Resources, current.Template.SystemReserve, used, d.Resources) {
		return ErrConflict
	}
	raw, err = json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_expansion_demands(org_id,cloud_id,expansion_id,intent_hash,project_id,environment_id,plan_id,service,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, d.OrgID, d.CloudID, e.ID, d.IntentHash, d.ProjectID, d.EnvironmentID, d.PlanID, d.Service, raw)
	if err != nil {
		return storeError(err)
	}
	return tx.Commit()
}

// acceptExpansion shares manual operation acceptance and capacity ledger checks.
// The expansion link is durable before the accepted operation becomes runnable.
func (s *PgStore) acceptExpansion(ctx context.Context, e *Expansion, o *Operation, p security.Principal, token string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if scopeErr := lockCapacityCloud(ctx, tx, e.OrgID, e.CloudID); scopeErr != nil {
		return scopeErr
	}
	if leaseErr := expansionLeaseTx(ctx, tx, e, token); leaseErr != nil {
		return leaseErr
	}
	policy, err := expansionPolicyTx(ctx, tx, e)
	if err != nil {
		return err
	}
	if minimumErr := expansionMinimumAdmissionTx(ctx, tx, e, policy); minimumErr != nil {
		return minimumErr
	}
	if policy.RequireExpansionApproval && (e.ApprovedBy != p.UserID || e.ApprovedAt == nil) {
		return security.ErrForbidden
	}
	if o.Input.Kind != "create" || o.OrgID != e.OrgID || o.CloudID != e.CloudID || o.AccountID != e.AccountID || o.ConnectionID != e.ConnectionID || o.ConnectionGeneration != e.ConnectionGeneration || o.Input.Size != e.Size || o.Input.Region != e.Region || o.Input.Image != e.Image || o.CapacityTarget == nil || *o.CapacityTarget != e.Resources || o.Principal != p {
		return ErrConflict
	}
	b, err := s.ReserveInfrastructureCapacity(ctx, tx, o.ID, CapacityDemand{OrgID: e.OrgID, CloudID: e.CloudID, PoolID: e.PoolID, PolicyRevision: e.PolicyRevision}, e.Resources)
	if err != nil {
		return err
	}
	if b != nil {
		return &CapacityAdmissionError{Block: *b}
	}
	if acceptErr := s.acceptOperationTx(ctx, tx, o); acceptErr != nil {
		return acceptErr
	}
	e.OperationID = o.ID
	e.Status = "provisioning"
	e.Block = nil
	if saveErr := saveExpansionTx(ctx, tx, e, e.Revision); saveErr != nil {
		return saveErr
	}
	principal, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if _, checkErr := tx.Exec(ctx, `UPDATE loomplane_cloud_expansions SET approval_principal=$4 WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, e.OrgID, e.CloudID, e.ID, principal); checkErr != nil {
		return checkErr
	}
	return tx.Commit()
}
func (s *PgStore) expansionForOperation(ctx context.Context, o *Operation) (*Expansion, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT body FROM loomplane_cloud_expansions WHERE org_id=$1 AND cloud_id=$2 AND operation_id=$3`, o.OrgID, o.CloudID, o.ID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var e Expansion
	if decodeErr := json.Unmarshal(raw, &e); decodeErr != nil {
		return nil, decodeErr
	}
	return &e, nil
}
func (s *PgStore) detachCapacityDemand(ctx context.Context, org, plan, service string) error {
	// Attachments contain no provider reservation. Accepted operations retain their
	// ledger rows and remain visible even when the last deployment stops waiting.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	// The organization gate also serializes attachments from different Clouds.
	if lockErr := cloudprofiles.LockOrganization(ctx, tx, org); lockErr != nil {
		return lockErr
	}
	_, err = tx.Exec(ctx, `DELETE FROM loomplane_cloud_expansion_demands WHERE org_id=$1 AND plan_id=$2 AND service=$3`, org, plan, service)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PgStore) capacityCandidate(ctx context.Context, d CapacityDemand, server string) (*Placement, error) {
	var revision int64
	if scanErr := s.db.QueryRow(ctx, `SELECT revision FROM loomplane_cloud_targets WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND pool_id=$4`, d.OrgID, d.CloudID, d.TargetID, d.PoolID).Scan(&revision); scanErr != nil {
		return nil, storeError(scanErr)
	}
	return s.PreviewPlacement(ctx, PlacementRequest{OrgID: d.OrgID, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, PlanID: d.PlanID, CloudID: d.CloudID, TargetID: d.TargetID, PoolID: d.PoolID, Service: d.Service, TargetRevision: revision, CPUMillis: d.Resources.CPUMillis, MemoryBytes: d.Resources.MemoryBytes, Architecture: d.Architecture, StickyServerID: d.StickyServerID, DestinationServerID: server, LocalData: d.LocalData, Replicas: 1})
}

func (s *PgStore) expansionApprovalPrincipal(ctx context.Context, e *Expansion) (security.Principal, error) {
	var raw []byte
	var p security.Principal
	if err := s.db.QueryRow(ctx, `SELECT approval_principal FROM loomplane_cloud_expansions WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND revision=$4`, e.OrgID, e.CloudID, e.ID, e.Revision).Scan(&raw); err != nil {
		return p, storeError(err)
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	if p.OrgID != e.OrgID || p.UserID == "" || p.ReadOnly {
		return p, security.ErrForbidden
	}
	return p, nil
}

// revalidateExpansion keeps the accepted operation and its allocation intact.
// Only an unattempted create can refresh its policy approval before execution.
func (s *PgStore) revalidateExpansion(ctx context.Context, e *Expansion, o *Operation, p security.Principal, token string, update bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if scopeErr := lockCapacityCloud(ctx, tx, e.OrgID, e.CloudID); scopeErr != nil {
		return scopeErr
	}
	if update {
		if leaseErr := expansionLeaseTx(ctx, tx, e, token); leaseErr != nil {
			return leaseErr
		}
	}
	if admissionErr := s.revalidateExpansionTx(ctx, tx, e, o, p); admissionErr != nil {
		return admissionErr
	}
	if !update {
		return tx.Commit()
	}
	e.Status = "provisioning"
	e.Block = nil
	if saveErr := saveExpansionTx(ctx, tx, e, e.Revision); saveErr != nil {
		return saveErr
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_cloud_expansions SET approval_principal=$4 WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, e.OrgID, e.CloudID, e.ID, raw); err != nil {
		return err
	}
	previous := o.StateRevision
	o.StateRevision++
	if o.Status == "failed" {
		o.Attempt++
	}
	o.Status = "pending"
	o.Message = ""
	o.UpdatedAt = time.Now().UTC()
	raw, err = json.Marshal(o)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE loomplane_infrastructure_operations SET status='pending',body=$4,updated_at=now() WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND coalesce((body->>'state_revision')::bigint,0)=$5 AND body->>'attempted'<>'true' AND status NOT IN ('preview','cancelled','succeeded')`, o.OrgID, o.CloudID, o.ID, raw, previous)
	if err != nil {
		return err
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

func (s *PgStore) revalidateExpansionTx(ctx context.Context, tx driver.Tx, e *Expansion, o *Operation, p security.Principal) error {
	policy, err := expansionPolicyTx(ctx, tx, e)
	if err != nil {
		return err
	}
	var revision int64
	if scanErr := tx.QueryRow(ctx, `SELECT revision FROM loomplane_cloud_expansions WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND operation_id=$4`, e.OrgID, e.CloudID, e.ID, o.ID).Scan(&revision); scanErr != nil {
		return storeError(scanErr)
	}
	if revision != e.Revision {
		return ErrConflict
	}
	if minimumErr := expansionMinimumAdmissionTx(ctx, tx, e, policy); minimumErr != nil {
		return minimumErr
	}
	if policy.RequireExpansionApproval && (e.ApprovedBy != p.UserID || e.ApprovedAt == nil) {
		return &CapacityAdmissionError{Block: *capacityBlock(policy, CapacityDemand{CloudID: e.CloudID, PoolID: e.PoolID}, "expansion_approval", "policy")}
	}
	if !policy.RequireExpansionApproval {
		var raw []byte
		var current security.Principal
		if scanErr := tx.QueryRow(ctx, `SELECT authority FROM loomplane_cloud_capacity_policies WHERE org_id=$1 AND cloud_id=$2`, e.OrgID, e.CloudID).Scan(&raw); scanErr != nil {
			return scanErr
		}
		if decodeErr := json.Unmarshal(raw, &current); decodeErr != nil {
			return decodeErr
		}
		if current != p {
			return security.ErrForbidden
		}
	}
	if o.CapacityTarget == nil || *o.CapacityTarget != e.Resources {
		return ErrConflict
	}
	block, err := s.reserveInfrastructureCapacity(ctx, tx, o.ID, CapacityDemand{OrgID: e.OrgID, CloudID: e.CloudID, PoolID: e.PoolID, PolicyRevision: e.PolicyRevision}, e.Resources, true)
	if err != nil {
		return err
	}
	if block != nil {
		return &CapacityAdmissionError{Block: *block}
	}
	return nil
}

// checkpointExpansionAttempt is the first-effect admission boundary. Later
// policy changes leave this possibly attempted operation available for recovery.
func (s *PgStore) checkpointExpansionAttempt(ctx context.Context, e *Expansion, o *Operation, p security.Principal, authority func(context.Context) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if scopeErr := lockInfrastructureScope(ctx, tx, o); scopeErr != nil {
		return scopeErr
	}
	if admissionErr := s.revalidateExpansionTx(ctx, tx, e, o, p); admissionErr != nil {
		return admissionErr
	}
	if authorityErr := authority(ctx); authorityErr != nil {
		return authorityErr
	}
	next := *o
	next.Attempted = true
	next.StateRevision++
	next.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE loomplane_infrastructure_operations SET body=$4,updated_at=now() WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND coalesce((body->>'state_revision')::bigint,0)=$5 AND body->>'attempted'<>'true' AND status='running'`, o.OrgID, o.CloudID, o.ID, raw, o.StateRevision)
	if err != nil {
		return err
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return commitErr
	}
	*o = next
	return nil
}

// Minimum slots go to concrete members, attempted or workload-backed capacity,
// then older minimum proposals. Excluding this proposal avoids double counting;
// stable priority lets retained unattempted proposals resume after a reduction.
func expansionMinimumNeededTx(ctx context.Context, tx driver.Tx, e *Expansion, policy *CapacityPolicy) (bool, error) {
	if !e.Minimum {
		return true, nil
	}
	var attached bool
	if scanErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cloud_expansion_demands WHERE org_id=$1 AND cloud_id=$2 AND expansion_id=$3)`, e.OrgID, e.CloudID, e.ID).Scan(&attached); scanErr != nil {
		return false, scanErr
	}
	if attached {
		return true, nil
	}
	template, found := expansionPool(policy, e.PoolID)
	if !policy.Enabled || !found || template.MinServers == 0 {
		return false, nil
	}
	var count int64
	if scanErr := tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM loomplane_server_pool_members WHERE org_id=$1 AND pool_id=$3) +
 (SELECT count(*) FROM loomplane_cloud_expansions x LEFT JOIN loomplane_infrastructure_operations o ON o.org_id=x.org_id AND o.cloud_id=x.cloud_id AND o.id=x.operation_id
 WHERE x.org_id=$1 AND x.cloud_id=$2 AND x.pool_id=$3 AND x.id<>$4 AND x.status NOT IN ('cancelled','ready')
 AND NOT EXISTS(SELECT 1 FROM loomplane_server_pool_members m WHERE m.org_id=$1 AND m.pool_id=$3 AND m.server_id=o.server_id)
 AND (coalesce((o.body->>'attempted')::boolean,false)
 OR EXISTS(SELECT 1 FROM loomplane_cloud_expansion_demands d WHERE d.org_id=x.org_id AND d.cloud_id=x.cloud_id AND d.expansion_id=x.id)
 OR (coalesce((x.body->>'minimum')::boolean,false) AND (x.created_at,x.id)<(SELECT created_at,id FROM loomplane_cloud_expansions WHERE org_id=$1 AND cloud_id=$2 AND id=$4))))`, e.OrgID, e.CloudID, e.PoolID, e.ID).Scan(&count); scanErr != nil {
		return false, scanErr
	}
	return count < template.MinServers, nil
}
func expansionMinimumAdmissionTx(ctx context.Context, tx driver.Tx, e *Expansion, policy *CapacityPolicy) error {
	needed, err := expansionMinimumNeededTx(ctx, tx, e, policy)
	if err != nil {
		return err
	}
	if !needed {
		return &CapacityAdmissionError{Block: *capacityBlock(policy, CapacityDemand{CloudID: e.CloudID, PoolID: e.PoolID}, "review_required", "minimum_servers")}
	}
	return nil
}

func (s *PgStore) retireObsoleteMinimum(ctx context.Context, e *Expansion, token string) (bool, error) {
	if !e.Minimum || e.Status == "ready" || e.Status == "cancelled" {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	if scopeErr := lockCapacityCloud(ctx, tx, e.OrgID, e.CloudID); scopeErr != nil {
		return false, scopeErr
	}
	if leaseErr := expansionLeaseTx(ctx, tx, e, token); leaseErr != nil {
		return false, leaseErr
	}
	policy, err := capacityPolicyTx(ctx, tx, e.OrgID, e.CloudID)
	if err != nil {
		return false, err
	}
	needed, err := expansionMinimumNeededTx(ctx, tx, e, policy)
	if err != nil || needed {
		return false, err
	}
	accepted := false
	if e.OperationID != "" {
		o, readErr := scanOperation(tx.QueryRow(ctx, `SELECT body,principal FROM loomplane_infrastructure_operations WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, e.OrgID, e.CloudID, e.OperationID))
		if readErr != nil {
			return false, readErr
		}
		if o.Attempted || o.Status == "succeeded" {
			return false, nil
		}
		accepted = o.Status != "preview"
		if !accepted {
			previous := o.StateRevision
			o.StateRevision++
			o.Status = "cancelled"
			o.UpdatedAt = time.Now().UTC()
			raw, marshalErr := json.Marshal(o)
			if marshalErr != nil {
				return false, marshalErr
			}
			tag, updateErr := tx.Exec(ctx, `UPDATE loomplane_infrastructure_operations SET status='cancelled',body=$4,updated_at=now() WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND status='preview' AND coalesce((body->>'state_revision')::bigint,0)=$5`, o.OrgID, o.CloudID, o.ID, raw, previous)
			if updateErr != nil {
				return false, updateErr
			}
			n, rowsErr := tag.RowsAffected()
			if rowsErr != nil {
				return false, rowsErr
			}
			if n != 1 {
				return false, ErrConflict
			}
		}
	}
	e.Status = "cancelled"
	e.Block = nil
	if accepted {
		e.Status = "blocked"
		e.Block = capacityBlock(policy, CapacityDemand{CloudID: e.CloudID, PoolID: e.PoolID}, "review_required", "minimum_servers")
	}
	if saveErr := saveExpansionTx(ctx, tx, e, e.Revision); saveErr != nil {
		return false, saveErr
	}
	return true, tx.Commit()
}
