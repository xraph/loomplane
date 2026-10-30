package planning

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/security"
)

// RetryFailedCleanup continues the same actor's review under a new attempt.
func (s *ManagedCleanupStore) RetryFailedCleanup(ctx context.Context, p security.Principal, deployment string) (_ *ManagedFailedCleanup, err error) {
	if caller, ok := security.PrincipalFrom(ctx); !ok || caller != p {
		return nil, security.ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, p.OrgID); checkErr != nil {
		return nil, checkErr
	}
	r, err := scanManagedFailed(tx.QueryRow(ctx, `SELECT `+managedFailedColumns+` FROM loomplane_managed_failed_cleanups WHERE org_id=$1 AND deployment_id=$2 AND superseded_at IS NULL FOR UPDATE`, p.OrgID, deployment))
	if err != nil {
		return nil, err
	}
	if r.Status != "failed" || r.RevokedAt != nil || r.Actor != p {
		return nil, ErrManagedFailedCleanupConflict
	}
	if checkErr := s.actor(ctx, r.Actor, r.Org, r.Targets[0].Scope.Project); checkErr != nil {
		return nil, checkErr
	}
	var revoked bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_managed_cleanup_grants WHERE org_id=$1 AND failed_review_id=$2 AND execution_attempt=$3 AND revoked_at IS NOT NULL)`, r.Org, r.ID, r.Attempt).Scan(&revoked); checkErr != nil {
		return nil, checkErr
	}
	if revoked {
		return nil, ErrManagedFailedCleanupConflict
	}
	for index, target := range r.Targets {
		revision, e := s.failedAuthorityRevision(ctx, tx, r, index)
		if e != nil {
			return nil, e
		}
		if checkErr := s.failedTargetCurrent(ctx, tx, target, revision); checkErr != nil {
			return nil, checkErr
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_managed_cleanup_grants SET revoked_at=clock_timestamp(),revision=revision+1,updated_at=clock_timestamp() WHERE org_id=$1 AND failed_review_id=$2 AND execution_attempt=$3 AND revoked_at IS NULL`, r.Org, r.ID, r.Attempt); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_managed_failed_cleanups SET status='pending',attempt=attempt+1,error='',revision=revision+1,updated_at=clock_timestamp() WHERE org_id=$1 AND id=$2`, r.Org, r.ID); err != nil {
		return nil, err
	}
	r.Attempt++
	r.Status = "pending"
	r.Error = ""
	return r, tx.Commit()
}

func (s *ManagedCleanupStore) ValidateFailedCleanupRun(ctx context.Context, org, id string, attempt int) (_ *ManagedFailedCleanup, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, org); checkErr != nil {
		return nil, checkErr
	}
	r, err := s.failedReview(ctx, tx, org, id)
	if err != nil {
		return nil, err
	}
	if r.Status != "pending" || r.Attempt != attempt || r.RevokedAt != nil || r.SupersededAt != nil {
		return nil, ErrManagedFailedCleanupConflict
	}
	if checkErr := s.actor(ctx, r.Actor, org, r.Targets[0].Scope.Project); checkErr != nil {
		return nil, checkErr
	}
	for index, target := range r.Targets {
		revision, e := s.failedAuthorityRevision(ctx, tx, r, index)
		if e != nil {
			return nil, e
		}
		if checkErr := s.failedTargetCurrent(ctx, tx, target, revision); checkErr != nil {
			return nil, checkErr
		}
	}
	return r, tx.Commit()
}

func (s *ManagedCleanupStore) FinishFailedCleanup(ctx context.Context, org, id string, attempt int, runErr error) error {
	status, message := "succeeded", ""
	if runErr != nil {
		status, message = "failed", runErr.Error()
	}
	_, err := s.db.Exec(ctx, `UPDATE loomplane_managed_failed_cleanups SET status=$4,error=$5,revision=revision+1,updated_at=clock_timestamp() WHERE org_id=$1 AND id=$2 AND attempt=$3 AND status='pending' AND revoked_at IS NULL AND superseded_at IS NULL`, org, id, attempt, status, message)
	return err
}

func (s *ManagedCleanupStore) PendingFailedCleanups(ctx context.Context) ([]ManagedFailedCleanup, error) {
	rows, err := s.db.Query(ctx, `SELECT `+managedFailedColumns+` FROM loomplane_managed_failed_cleanups WHERE status='pending' AND revoked_at IS NULL AND superseded_at IS NULL AND delivered_attempt<attempt ORDER BY updated_at LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ManagedFailedCleanup
	for rows.Next() {
		r, e := scanManagedFailed(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *ManagedCleanupStore) MarkFailedCleanupDelivered(ctx context.Context, org, id string, attempt int) error {
	_, err := s.db.Exec(ctx, `UPDATE loomplane_managed_failed_cleanups SET delivered_attempt=$3,revision=revision+1,updated_at=clock_timestamp() WHERE org_id=$1 AND id=$2 AND attempt=$3 AND delivered_attempt<$3`, org, id, attempt)
	return err
}

// RevokeFailedCleanup fences pending work while retaining every accepted outcome.
func (s *ManagedCleanupStore) RevokeFailedCleanup(ctx context.Context, p security.Principal, deployment, reviewID string) (err error) {
	if caller, ok := security.PrincipalFrom(ctx); !ok || caller != p {
		return security.ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, p.OrgID); checkErr != nil {
		return checkErr
	}
	r, err := scanManagedFailed(tx.QueryRow(ctx, `SELECT `+managedFailedColumns+` FROM loomplane_managed_failed_cleanups WHERE org_id=$1 AND deployment_id=$2 AND superseded_at IS NULL FOR UPDATE`, p.OrgID, deployment))
	if err != nil {
		return err
	}
	if reviewID == "" || r.ID != reviewID || r.Status == "succeeded" {
		return ErrManagedFailedCleanupConflict
	}
	if checkErr := s.actor(ctx, p, p.OrgID, r.Targets[0].Scope.Project); checkErr != nil {
		return checkErr
	}
	actor, marshalErr := json.Marshal(p)
	if marshalErr != nil {
		return marshalErr
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('loomplane.capacity_actor',$1,true)`, string(actor)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_managed_failed_cleanups SET revoked_at=coalesce(revoked_at,clock_timestamp()),status='cancelled',cancelled_by=coalesce(cancelled_by,$3::jsonb),revision=revision+1,updated_at=clock_timestamp() WHERE org_id=$1 AND id=$2`, r.Org, r.ID, actor); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_managed_cleanup_grants SET revoked_at=coalesce(revoked_at,clock_timestamp()),revision=revision+1,updated_at=clock_timestamp() WHERE org_id=$1 AND failed_review_id=$2`, r.Org, r.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// IsManagedFailedCleanupMissing distinguishes SQL fallback from a managed refusal.
func IsManagedFailedCleanupMissing(err error) bool { return errors.Is(err, ErrNotFound) }
