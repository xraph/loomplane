package workloadidentity

import (
	"context"
	"errors"
	"fmt"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// Eligibility is rechecked while holding the enrollment row lock. Reserving a
// newer epoch also makes abandoned pending registrations safe to retire early.
const cleanupEligible = `(e.phase='revoking'
 OR (e.phase IN ('pending','ready') AND (e.expires_at<=clock_timestamp()
 OR EXISTS(SELECT 1 FROM loomplane_federation_enrollment_epochs n WHERE n.org_id=e.org_id AND n.destination_id=e.destination_id AND n.epoch>e.epoch)
 OR NOT EXISTS(SELECT 1 FROM loomplane_federation_destinations d WHERE d.org_id=e.org_id AND d.id=e.destination_id AND NOT d.quarantined AND d.generation=e.destination_generation)))
 OR (e.phase='consumed' AND NOT EXISTS(SELECT 1 FROM loomplane_federation_observers a JOIN loomplane_federation_destinations d ON d.id=a.destination_id AND d.org_id=a.org_id WHERE a.org_id=e.org_id AND a.agent_id=e.agent_id AND a.active AND NOT d.quarantined AND a.destination_generation=d.generation)))`

type CleanupResult struct {
	Attempted int
	Deleted   int
	Failed    int
	Skipped   int
}

// Cleanup retries bounded work in one organization. Failed entries retain trust
// references and move behind older attempts so one outage cannot starve others.
func (s *EnrollmentStore) Cleanup(ctx context.Context, org string, limit int, issuer EnrollmentIssuer) (CleanupResult, error) {
	result := CleanupResult{}
	if issuer == nil {
		return result, ErrIdentity
	}
	ids, err := s.CleanupCandidates(ctx, org, limit)
	if err != nil {
		return result, err
	}
	var failures error
	for _, id := range ids {
		if ctx.Err() != nil {
			return result, errors.Join(failures, ctx.Err())
		}
		result.Attempted++
		r, beginErr := s.beginRevoke(ctx, org, id, true)
		if beginErr != nil {
			result.Failed++
			failures = errors.Join(failures, fmt.Errorf("prepare enrollment cleanup %s: %w", id, beginErr))
			continue
		}
		if r == nil {
			result.Skipped++
			continue
		}
		if revokeErr := s.completeRevoke(ctx, r, issuer); revokeErr != nil {
			result.Failed++
			failures = errors.Join(failures, fmt.Errorf("revoke enrollment %s: %w", id, revokeErr))
			continue
		}
		result.Deleted++
	}
	return result, failures
}

type CleanupStore interface {
	Cleanup(context.Context, string, int, EnrollmentIssuer) (CleanupResult, error)
}

// CleanupWorker runs sequential bounded sweeps for its configured organization.
// OnResult must return promptly; callers wire logs, audit and metrics there.
type CleanupWorker struct {
	OrgID    string
	Store    CleanupStore
	Issuer   EnrollmentIssuer
	Interval time.Duration
	OnResult func(CleanupResult, error)
}

func (w *CleanupWorker) Run(ctx context.Context) error {
	if !domain.ValidIdentitySegment(w.OrgID) || w.Store == nil || w.Issuer == nil || w.Interval < 0 {
		return ErrIdentity
	}
	interval := w.Interval
	if interval == 0 {
		interval = 30 * time.Second
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		sweep, stop := context.WithTimeout(ctx, 30*time.Second)
		result, err := w.Store.Cleanup(sweep, w.OrgID, 20, w.Issuer)
		stop()
		if w.OnResult != nil {
			w.OnResult(result, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
