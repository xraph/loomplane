package workloadidentity

import (
	"context"
	"errors"
	"fmt"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
)

const workloadCleanupEligible = `r.next_attempt<=clock_timestamp() AND (
 (r.phase='draining' AND r.release_after<=clock_timestamp()) OR
 (r.phase<>'draining' AND (r.phase='revoking' OR r.revoke_after<=clock_timestamp() OR NOT EXISTS(
 SELECT 1 FROM ` + workloadCurrentFrom + ` WHERE w.org_id=r.org_id AND w.id=r.id AND ` + workloadCurrentWhere + `))))`

// Revoke persists local revocation intent before deleting the remote entry.
// Parents remain retained through the maximum certificate and policy lifetime.
func (s *WorkloadStore) Revoke(ctx context.Context, org, id string, issuer WorkloadRevoker) error {
	if issuer == nil {
		return ErrIdentity
	}
	r, _, err := s.beginWorkloadRevoke(ctx, org, id, false)
	if err != nil || r == nil {
		return err
	}
	return s.completeWorkloadRevoke(ctx, r, issuer)
}
func (s *WorkloadStore) beginWorkloadRevoke(ctx context.Context, org, id string, eligibleOnly bool) (record *WorkloadRecord, deleted bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { err = enrollmentFinish(tx, err) }()
	r, err := loadWorkload(ctx, tx, org, id)
	if errors.Is(err, ErrIdentity) {
		return nil, false, tx.Commit()
	}
	if err != nil {
		return nil, false, err
	}
	if eligibleOnly {
		var eligible bool
		if stepErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_workload_registrations r WHERE r.org_id=$1 AND r.id=$2 AND `+workloadCleanupEligible+`)`, org, id).Scan(&eligible); stepErr != nil {
			return nil, false, stepErr
		}
		if !eligible {
			return nil, false, tx.Commit()
		}
	}
	if r.Phase == "draining" {
		var due bool
		if stepErr := tx.QueryRow(ctx, `SELECT release_after<=clock_timestamp() FROM loomplane_workload_registrations WHERE org_id=$1 AND id=$2`, org, id).Scan(&due); stepErr != nil {
			return nil, false, stepErr
		}
		if !due {
			return nil, false, tx.Commit()
		}
		if stepErr := workloadEvent(ctx, tx, r, "retired"); stepErr != nil {
			return nil, false, stepErr
		}
		if _, err = tx.Exec(ctx, `DELETE FROM loomplane_workload_registrations WHERE org_id=$1 AND id=$2`, org, id); err != nil {
			return nil, false, err
		}
		return nil, true, tx.Commit()
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_workload_registrations SET phase='revoking',next_attempt=clock_timestamp()+interval '30 seconds',updated_at=clock_timestamp() WHERE org_id=$1 AND id=$2`, org, id); err != nil {
		return nil, false, err
	}
	if r.Phase != "revoking" {
		if stepErr := workloadEvent(ctx, tx, r, "revoking"); stepErr != nil {
			return nil, false, stepErr
		}
	}
	return r, false, tx.Commit()
}
func (s *WorkloadStore) completeWorkloadRevoke(ctx context.Context, r *WorkloadRecord, issuer WorkloadRevoker) (err error) {
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if stepErr := issuer.Revoke(call, r.Intent.Workload.Binding.EntryID); stepErr != nil {
		return stepErr
	}
	// A successful delete may precede a delayed create. Only a creation already
	// settled before this delete can advance to draining, even if it settles now.
	if !r.Settled {
		return ErrWorkloadUncertain
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = enrollmentFinish(tx, err) }()
	current, err := loadWorkload(ctx, tx, r.Intent.Controller.OrgID, r.ID)
	if errors.Is(err, ErrIdentity) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if current.Phase == "draining" {
		return tx.Commit()
	}
	if current.Phase != "revoking" || !current.Settled {
		return ErrWorkloadUncertain
	}
	drain := max(r.Intent.Workload.Binding.TTL, domain.MaxPolicyLease) + 2*domain.MaxClockSkew
	if _, err = tx.Exec(ctx, `UPDATE loomplane_workload_registrations SET phase='draining',release_after=clock_timestamp()+($3*interval '1 second'),next_attempt=clock_timestamp(),updated_at=clock_timestamp() WHERE org_id=$1 AND id=$2`, r.Intent.Controller.OrgID, r.ID, int64(drain/time.Second)); err != nil {
		return err
	}
	if stepErr := workloadEvent(ctx, tx, r, "draining"); stepErr != nil {
		return stepErr
	}
	return tx.Commit()
}

// Cleanup shares the existing identity worker. Unknown creates are repeatedly
// revoked but remain visible as failed cleanup until their outcome is settled.
func (s *WorkloadStore) Cleanup(ctx context.Context, org string, limit int, issuer EnrollmentIssuer) (CleanupResult, error) {
	result := CleanupResult{}
	if !domain.ValidIdentitySegment(org) || limit < 1 || limit > 100 || issuer == nil {
		return result, ErrIdentity
	}
	ids, err := s.workloadCleanupIDs(ctx, org, limit)
	if err != nil {
		return result, err
	}
	var failures error
	for _, id := range ids {
		if ctx.Err() != nil {
			return result, errors.Join(failures, ctx.Err())
		}
		result.Attempted++
		r, deleted, stepErr := s.beginWorkloadRevoke(ctx, org, id, true)
		if stepErr == nil && r != nil {
			stepErr = s.completeWorkloadRevoke(ctx, r, issuer)
		}
		switch {
		case stepErr != nil:
			result.Failed++
			failures = errors.Join(failures, fmt.Errorf("revoke workload %s: %w", id, stepErr))
		case deleted:
			result.Deleted++
		default:
			result.Skipped++
		}
	}
	return result, failures
}
func (s *WorkloadStore) workloadCleanupIDs(ctx context.Context, org string, limit int) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT r.id FROM loomplane_workload_registrations r WHERE r.org_id=$1 AND `+workloadCleanupEligible+` ORDER BY r.next_attempt,r.id LIMIT $2`, org, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
