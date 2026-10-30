package deployments

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

type ManagedFailedCleanupEngine interface {
	ExecuteManagedFailedCleanup(context.Context, *planning.ManagedFailedCleanup, func([]byte) error) error
}

type managedCleanupStarter interface {
	StartManagedFailedCleanup(context.Context, string, string, string, int) error
}

func (s *FailedCleanups) WithManagedCleanup(store *planning.ManagedCleanupStore, engine ManagedFailedCleanupEngine) *FailedCleanups {
	s.managed = store
	s.managedEngine = engine
	return s
}

func managedCleanupResult(r *planning.ManagedFailedCleanup) *FailedCleanup {
	return &FailedCleanup{DeploymentID: r.Deployment, Revision: r.Digest, Status: r.Status, Attempt: r.Attempt, Error: r.Error, Actor: r.Actor, Variant: "managed_workload", ReviewID: r.ID, ReviewSequence: r.Sequence}
}

func managedCleanupPreview(r *planning.ManagedFailedCleanup) *CleanupReview {
	return &CleanupReview{DeploymentID: r.Deployment, Revision: r.Digest, Variant: "managed_workload", ReviewSequence: r.Sequence, Targets: r.Targets, Blockers: r.Blockers, PreviousReviewID: r.Predecessor, Effect: "Stop retries for this failed deployment and remove only its reviewed owned objects. Serving resources, shared controllers and unresolved writes block cleanup."}
}

func (s *FailedCleanups) deliverManaged(ctx context.Context, r *planning.ManagedFailedCleanup) error {
	starter, ok := s.start.(managedCleanupStarter)
	if !ok {
		return planning.ErrManagedFailedCleanupUnavailable
	}
	if err := starter.StartManagedFailedCleanup(ctx, r.Org, r.Deployment, r.ID, r.Attempt); err != nil {
		return err
	}
	return s.managed.MarkFailedCleanupDelivered(ctx, r.Org, r.ID, r.Attempt)
}

func (s *FailedCleanups) recoverManaged(ctx context.Context) error {
	if s.managed == nil {
		return nil
	}
	pending, err := s.managed.PendingFailedCleanups(ctx)
	if err != nil {
		return err
	}
	for i := range pending {
		if checkErr := s.deliverManaged(ctx, &pending[i]); checkErr != nil {
			return checkErr
		}
	}
	return nil
}

// RunManagedFailedCleanup requires the saved review and the current attempt.
func (s *FailedCleanups) RunManagedFailedCleanup(ctx context.Context, org, deployment, review string, attempt int, heartbeat func([]byte) error) error {
	if !orchestration.IsManagedCleanupContext(ctx) {
		return ErrManagedCleanupProtocol
	}
	if s.managed == nil {
		return planning.ErrManagedFailedCleanupUnavailable
	}
	if heartbeat == nil {
		return errors.New("cleanup requires an activity heartbeat")
	}
	ctx, heartbeat, stop := keepHeartbeat(ctx, nil, heartbeat, 30*time.Second)
	defer stop()
	r, runErr := s.managed.ValidateFailedCleanupRun(ctx, org, review, attempt)
	if runErr == nil && r.Deployment != deployment {
		return planning.ErrManagedFailedCleanupConflict
	}
	if runErr == nil {
		if s.managedEngine == nil {
			runErr = planning.ErrManagedFailedCleanupUnavailable
		} else {
			runErr = s.managedEngine.ExecuteManagedFailedCleanup(ctx, r, heartbeat)
		}
	}
	if ctx.Err() != nil {
		return errors.Join(runErr, ctx.Err())
	}
	if err := heartbeat(nil); err != nil {
		return err
	}
	saveErr := s.managed.FinishFailedCleanup(ctx, org, review, attempt, runErr)
	return errors.Join(runErr, saveErr)
}

// Cancel revokes only the current managed review. SQL cleanup keeps its lifecycle.
func (s *FailedCleanups) Cancel(ctx context.Context, p security.Principal, id, reviewID string) (*FailedCleanup, error) {
	if s.managed == nil {
		return nil, planning.ErrManagedFailedCleanupUnavailable
	}
	if err := s.managed.RevokeFailedCleanup(ctx, p, id, reviewID); err != nil {
		return nil, err
	}
	r, err := s.managed.CurrentFailedCleanup(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	return managedCleanupResult(r), nil
}
