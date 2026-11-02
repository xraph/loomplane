package planning

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/orchestration"
)

// Call only from commitCleanupObservations after every currently reviewed
// release has qualified and settlement has completed, under its organization lock.
func retireObservedInstances(ctx context.Context, tx driver.Tx, observations []cleanupObservation) error {
	instances := map[string][]cleanupObservation{}
	for _, o := range observations {
		if !o.tombstone.Shared {
			instances[o.tombstone.InstanceID] = append(instances[o.tombstone.InstanceID], o)
		}
	}
	for id, group := range instances {
		if err := retireObservedInstance(ctx, tx, id, group); err != nil {
			return err
		}
	}
	return nil
}

func retireObservedInstance(ctx context.Context, tx driver.Tx, instance string, group []cleanupObservation) error {
	first := group[0].tombstone
	if first.OrgID == "" || instance == "" {
		return kube.ErrManagedAuthority
	}
	if _, err := tx.Exec(ctx, `SELECT loomplane_lock_managed_instance($1,$2)`, first.OrgID, instance); err != nil {
		return err
	}
	owners := make([]ManagedCleanupTarget, 0, len(group))
	for _, o := range group {
		t := o.session.grant.Target
		if t.Scope.Instance != instance || t.Scope.Org != first.OrgID || t.Kind == ManagedCleanupRetireDependents {
			return kube.ErrManagedAuthority
		}
		owners = append(owners, t)
	}
	reviewed, err := json.Marshal(owners)
	if err != nil {
		return err
	}
	var qualified bool
	if readErr := tx.QueryRow(ctx, `SELECT loomplane_can_retire_managed_instance($1,$2,$3::jsonb)`, first.OrgID, instance, reviewed).Scan(&qualified); readErr != nil {
		return readErr
	}
	if !qualified {
		return kube.ErrManagedAuthority
	}
	var labelsRaw []byte
	var provider string
	readErr := tx.QueryRow(ctx, `SELECT provider_name,labels FROM cp_instances WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, first.OrgID, instance).Scan(&provider, &labelsRaw)
	if readErr != nil && !errors.Is(readErr, pgx.ErrNoRows) {
		return readErr
	}
	runtimeRef, connection := "", first.ConnectionID
	runtimeFiles := false
	if readErr == nil {
		var labels map[string]string
		if decodeErr := json.Unmarshal(labelsRaw, &labels); decodeErr != nil {
			return decodeErr
		}
		ownedHere := false
		for _, owner := range owners {
			s := owner.Scope
			if provider == "kubernetes."+s.Connection && labels[owned.LabelOrg] == s.Org && labels[owned.LabelProject] == s.Project && labels[owned.LabelEnvironment] == s.Environment && labels[owned.LabelService] == s.Service && labels[owned.LabelPlan] == s.Plan && labels[owned.LabelRuntime] == s.RuntimeRef && labels[owned.LabelRevision] == s.RuntimeRevision {
				runtimeRef, connection = s.RuntimeRef, s.Connection
				runtimeFiles = labels[owned.LabelRuntimeFiles] == "true"
				ownedHere = true
				break
			}
		}
		if !ownedHere {
			return kube.ErrManagedAuthority
		}
	}
	if runtimeRef != "" {
		if _, lockErr := tx.Exec(ctx, `SELECT loomplane_lock_retired_runtime_ref($1,$2)`, first.OrgID, runtimeRef); lockErr != nil {
			return lockErr
		}
		var shared bool
		if refErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cp_instances WHERE tenant_id=$1 AND id<>$2 AND labels->>'loomplane.runtime.ref'=$3) OR EXISTS(SELECT 1 FROM loomplane_managed_workload_authority WHERE org_id=$1 AND body->>'Instance'<>$2 AND body->>'RuntimeRef'=$3 AND revoked_at IS NULL)`, first.OrgID, instance, runtimeRef).Scan(&shared); refErr != nil {
			return refErr
		}
		if shared {
			return kube.ErrManagedAuthority
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_managed_retired_instances(org_id,instance_id,release_id,connection_id,runtime_ref,runtime_files) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(org_id,instance_id) DO NOTHING`, first.OrgID, instance, first.ReleaseID, connection, runtimeRef, runtimeFiles)
	return err
}

// RetiredCleanupGrants selects the whole releases from the current review and
// verifies that each exact instance was finalized. Shared retirements have no
// authority over the instance row.
func (s *ManagedCleanupStore) RetiredCleanupGrants(ctx context.Context, selection ManagedCleanupSelection) ([]ManagedCleanupGrant, error) {
	selections, err := s.effectSelections(ctx, selection)
	if err != nil {
		return nil, err
	}
	var result []ManagedCleanupGrant
	for _, sel := range selections {
		g, readErr := s.Open(ctx, sel)
		if readErr != nil {
			return nil, readErr
		}
		if g.Target.Kind == ManagedCleanupRetireDependents {
			continue
		}
		if checkErr := s.CheckRetiredInstance(ctx, g.Target.Scope.Org, g.ID, g.Target.Scope.Instance); checkErr != nil {
			return nil, checkErr
		}
		result = append(result, *g)
	}
	return result, nil
}

// CheckRetiredInstance checks the current operation before completing metadata
// removal. A late unresolved request or residual remains visible after retirement.
func (s *ManagedCleanupStore) CheckRetiredInstance(ctx context.Context, org, grantID, instance string) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := s.checkRetiredInstance(ctx, tx, org, grantID, instance); checkErr != nil {
		return checkErr
	}
	return tx.Commit()
}

func (s *ManagedCleanupStore) checkRetiredInstance(ctx context.Context, tx driver.Tx, org, grantID, instance string) error {
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, org); checkErr != nil {
		return checkErr
	}
	g, err := s.grant(ctx, tx, org, grantID)
	if err != nil {
		return err
	}
	if g.RevokedAt != nil || g.Target.Scope.Instance != instance || g.Target.Kind != ManagedCleanupRemoveRelease && g.Target.Kind != ManagedCleanupAbandonFailed {
		return kube.ErrManagedAuthority
	}
	if _, checkErr := s.loadTarget(ctx, tx, g.Selection); checkErr != nil {
		return checkErr
	}
	if checkErr := s.guard(ctx, tx, g.Target, g.AuthorityRevision); checkErr != nil {
		return checkErr
	}
	var done bool
	if readErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_managed_retired_instances r JOIN loomplane_managed_cleanup_tombstones t ON t.org_id=r.org_id AND t.body->>'instance_id'=r.instance_id WHERE r.org_id=$1 AND r.instance_id=$2 AND t.release_id=$3)
 AND NOT EXISTS(SELECT 1 FROM loomplane_managed_workload_authority a WHERE a.org_id=$1 AND a.body->>'Instance'=$2 AND (a.revoked_at IS NULL OR EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances w WHERE w.org_id=a.org_id AND w.release_id=a.release_id AND w.outcome IS NULL) OR EXISTS(SELECT 1 FROM loomplane_managed_cleanup_current_drift d WHERE d.org_id=a.org_id AND d.release_id=a.release_id AND d.present)))`, org, instance, g.Target.Scope.Release).Scan(&done); readErr != nil {
		return readErr
	}
	if !done {
		return kube.ErrManagedAuthority
	}
	return nil
}

// RetiredRuntimeRef preserves the verified ctrlplane reference through a crash
// between removing the instance row and removing its runtime values.
func (s *ManagedCleanupStore) RetiredRuntimeRef(ctx context.Context, g ManagedCleanupGrant) (runtimeReference string, runtimeFiles bool, readError error) {
	if err := s.CheckRetiredInstance(ctx, g.Target.Scope.Org, g.ID, g.Target.Scope.Instance); err != nil {
		return "", false, err
	}
	var ref string
	var files bool
	err := s.db.QueryRow(ctx, `SELECT runtime_ref,runtime_files FROM loomplane_managed_retired_instances WHERE org_id=$1 AND instance_id=$2 AND connection_id=$3`, g.Target.Scope.Org, g.Target.Scope.Instance, g.Target.Scope.Connection).Scan(&ref, &files)
	return ref, files, err
}

var ErrRetiredRuntimeUnresolved = errors.New("the previous deletion of this instance's runtime values has an unknown outcome")

func (s *ManagedCleanupStore) BeginRetiredRuntimeRemoval(ctx context.Context, g ManagedCleanupGrant) (_ string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := s.checkRetiredInstance(ctx, tx, g.Target.Scope.Org, g.ID, g.Target.Scope.Instance); checkErr != nil {
		return "", checkErr
	}
	token := uuid.NewString()
	var id string
	var resolved bool
	err = tx.QueryRow(ctx, `INSERT INTO loomplane_retired_runtime_removals(org_id,instance_id,id,grant_id) SELECT org_id,instance_id,$3,$4 FROM loomplane_managed_retired_instances WHERE org_id=$1 AND instance_id=$2 AND runtime_ref<>'' AND NOT EXISTS(SELECT 1 FROM cp_instances WHERE tenant_id=$1 AND id=$2)
 ON CONFLICT(org_id,instance_id) DO UPDATE SET id=loomplane_retired_runtime_removals.id RETURNING id,resolved_at IS NOT NULL`, g.Target.Scope.Org, g.Target.Scope.Instance, token, g.ID).Scan(&id, &resolved)
	if err != nil {
		return "", err
	}
	if resolved {
		return "", tx.Commit()
	}
	if token != id {
		return "", ErrRetiredRuntimeUnresolved
	}
	return id, tx.Commit()
}

func (s *ManagedCleanupStore) ResolveRetiredRuntimeRemoval(ctx context.Context, g ManagedCleanupGrant, id string) error {
	result, err := s.db.Exec(ctx, `UPDATE loomplane_retired_runtime_removals SET resolved_at=coalesce(resolved_at,clock_timestamp()) WHERE org_id=$1 AND instance_id=$2 AND id=$3 AND grant_id=$4`, g.Target.Scope.Org, g.Target.Scope.Instance, id, g.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return errors.Join(kube.ErrManagedAuthority, err)
	}
	return nil
}

// CheckWorkloadExecutionRoute refuses a missing route, a pre-cutover cleanup
// approval, or a database that has not installed the ctrlplane ownership fence.
func (s *ManagedCleanupStore) CheckWorkloadExecutionRoute(ctx context.Context, p *Plan, a Action) error {
	if s == nil || s.db == nil || s.authz == nil || s.journal == nil {
		return ErrManagedCleanupUnavailable
	}
	if p == nil || a.Service == nil || a.Service.ClusterDemand == nil || p.RequiredRuntimeProtocol != orchestration.ManagedWorkloadProtocol && p.RequiredRuntimeProtocol != orchestration.ManagedCleanupProtocol {
		return kube.ErrManagedAuthority
	}
	if checkErr := a.Service.CheckManagedServiceFinalizers(); checkErr != nil {
		return checkErr
	}
	var valid bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_plans p JOIN loomplane_cluster_workload_intents i ON i.org_id=p.org_id AND i.plan_id=p.id
 WHERE p.org_id=$1 AND p.id=$2 AND p.required_runtime_protocol=$3 AND p.status='approved' AND i.service=$4 AND i.body->'input'->>'action_id'=$5
 AND NOT loomplane_legacy_cleanup_blocked(p.org_id,p.project_id,p.environment_id,i.service,'')
 AND EXISTS(SELECT 1 FROM loomplane_deployment_workflow_runs r WHERE r.org_id=p.org_id AND (r.plan_id=p.id OR EXISTS(SELECT 1 FROM loomplane_deployment_workflow_participants w WHERE w.org_id=p.org_id AND w.plan_id=p.id AND w.workflow_id=r.workflow_id)) AND loomplane_managed_protocol_compatible(p.required_runtime_protocol,r.request->>'build_id')))
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='cp_instances'::regclass AND tgname='loomplane_guard_retired_ctrlplane_instance' AND tgenabled IN ('O','A'))`, p.OrgID, p.ID, p.RequiredRuntimeProtocol, a.Name, a.ID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return workloadBlock("managed_workload_execution_unqualified")
	}
	return nil
}
