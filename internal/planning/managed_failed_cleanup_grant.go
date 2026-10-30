package planning

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
)

func (s *ManagedCleanupStore) loadFailedTarget(ctx context.Context, tx driver.Tx, sel ManagedCleanupSelection) (ManagedCleanupTarget, error) {
	var empty ManagedCleanupTarget
	r, err := s.failedReview(ctx, tx, sel.Org, sel.FailedReviewID)
	if err != nil {
		return empty, err
	}
	if r.Status != "pending" || r.RevokedAt != nil || r.SupersededAt != nil || r.Attempt != sel.ExecutionAttempt || r.Deployment != sel.Deployment || sel.TargetIndex < 0 || sel.TargetIndex >= len(r.Targets) {
		return empty, kube.ErrManagedAuthority
	}
	target := r.Targets[sel.TargetIndex]
	if target.Kind != ManagedCleanupAbandonFailed || target.Scope.Org != sel.Org || target.Scope.Plan != sel.Plan || target.Scope.Action != sel.Action || target.Scope.Deployment != sel.Deployment {
		return empty, kube.ErrManagedAuthority
	}
	if checkErr := s.actor(ctx, r.Actor, sel.Org, target.Scope.Project); checkErr != nil {
		return empty, checkErr
	}
	revision, err := s.failedAuthorityRevision(ctx, tx, r, sel.TargetIndex)
	if err != nil {
		return empty, err
	}
	if checkErr := s.failedTargetCurrent(ctx, tx, target, revision); checkErr != nil {
		return empty, checkErr
	}
	return target, nil
}

func (s *ManagedCleanupStore) failedAuthorityRevision(ctx context.Context, tx driver.Tx, r *ManagedFailedCleanup, index int) (int64, error) {
	var revision int64
	err := tx.QueryRow(ctx, `SELECT authority_revision FROM loomplane_managed_cleanup_grants WHERE org_id=$1 AND failed_review_id=$2 AND target_index=$3 ORDER BY execution_attempt DESC LIMIT 1`, r.Org, r.ID, index).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return r.Targets[index].AuthorityRevision, nil
	}
	return revision, err
}

func (s *ManagedCleanupStore) openFailedGrant(ctx context.Context, sel ManagedCleanupSelection) (_ *ManagedCleanupGrant, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, sel.Org); checkErr != nil {
		return nil, checkErr
	}
	target, err := s.loadFailedTarget(ctx, tx, sel)
	if err != nil {
		return nil, err
	}
	var existing string
	err = tx.QueryRow(ctx, `SELECT id FROM loomplane_managed_cleanup_grants WHERE org_id=$1 AND failed_review_id=$2 AND execution_attempt=$3 AND target_index=$4`, sel.Org, sel.FailedReviewID, sel.ExecutionAttempt, sel.TargetIndex).Scan(&existing)
	if err == nil {
		g, e := s.grant(ctx, tx, sel.Org, existing)
		if e != nil {
			return nil, e
		}
		if g.RevokedAt != nil {
			return nil, kube.ErrManagedAuthority
		}
		return g, tx.Commit()
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	r, err := s.failedReview(ctx, tx, sel.Org, sel.FailedReviewID)
	if err != nil {
		return nil, err
	}
	revision, err := s.failedAuthorityRevision(ctx, tx, r, sel.TargetIndex)
	if err != nil {
		return nil, err
	}
	if checkErr := tx.QueryRow(ctx, `UPDATE loomplane_managed_workload_authority SET revoked_at=coalesce(revoked_at,clock_timestamp()),revision=revision+1,updated_at=clock_timestamp() WHERE org_id=$1 AND release_id=$2 AND revision=$3 RETURNING revision`, sel.Org, target.Scope.Release, revision).Scan(&revision); checkErr != nil {
		return nil, checkErr
	}
	id, err := typeid.Generate("cleanupgrant")
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_managed_cleanup_grants(id,org_id,plan_id,action_id,deployment_id,target_index,release_id,authority_revision,failed_review_id,execution_attempt) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id.String(), sel.Org, sel.Plan, sel.Action, sel.Deployment, sel.TargetIndex, target.Scope.Release, revision, sel.FailedReviewID, sel.ExecutionAttempt); err != nil {
		return nil, err
	}
	g, err := s.grant(ctx, tx, sel.Org, id.String())
	if err != nil {
		return nil, err
	}
	return g, tx.Commit()
}

func (s *ManagedCleanupStore) failedGrant(ctx context.Context, tx driver.Tx, org, id string) (*ManagedCleanupGrant, error) {
	out := &ManagedCleanupGrant{ID: id}
	out.Selection.Org = org
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT g.plan_id,g.action_id,g.deployment_id,g.target_index,g.schema_version,g.revision,g.authority_revision,g.created_at,g.updated_at,g.revoked_at,g.failed_review_id,g.execution_attempt,r.targets->g.target_index FROM loomplane_managed_cleanup_grants g JOIN loomplane_managed_failed_cleanups r ON r.org_id=g.org_id AND r.id=g.failed_review_id WHERE g.org_id=$1 AND g.id=$2 FOR UPDATE OF g`, org, id).Scan(&out.Selection.Plan, &out.Selection.Action, &out.Selection.Deployment, &out.Selection.TargetIndex, &out.SchemaVersion, &out.Revision, &out.AuthorityRevision, &out.CreatedAt, &out.UpdatedAt, &out.RevokedAt, &out.Selection.FailedReviewID, &out.Selection.ExecutionAttempt, &raw)
	if err != nil {
		return nil, errors.Join(kube.ErrManagedAuthority, err)
	}
	if json.Unmarshal(raw, &out.Target) != nil || out.SchemaVersion != 1 {
		return nil, kube.ErrManagedAuthority
	}
	return out, nil
}
