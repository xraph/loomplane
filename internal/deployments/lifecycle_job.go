package deployments

import (
	"context"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

type LifecycleJobRun = owned.LifecycleJobRun
type LifecycleJobReceipt = owned.LifecycleJobReceipt

// LifecycleJobState is a durable execution cursor, scoped by Run.ID and Digest.
type LifecycleJobState struct {
	Receipt         LifecycleJobReceipt
	Issued, Cleaned bool
}

type LifecycleJobStore interface {
	BeginLifecycleJob(context.Context, LifecycleJobRun) (*LifecycleJobState, error)
	IssueLifecycleJob(context.Context, LifecycleJobRun, int) (bool, error)
	RecordLifecycleJob(context.Context, LifecycleJobRun, LifecycleJobReceipt) error
	CleanLifecycleJob(context.Context, LifecycleJobRun, LifecycleJobReceipt) error
}

type LifecycleJobs struct {
	Store   LifecycleJobStore
	Backend owned.LifecycleJobBackend
	// Authorize rechecks the saved operation, epoch, namespace and reviewed demand.
	// It must also validate every dependency and the image's registry platform proof.
	Authorize func(context.Context, LifecycleJobRun) error
}

// RunLifecycleJob performs one durable reconciliation. Unknown provider outcomes
// stay pending; only a recorded terminal failure permits the next attempt.
func (s *LifecycleJobs) RunLifecycleJob(ctx context.Context, run LifecycleJobRun) (*LifecycleJobReceipt, error) {
	if err := run.Validate(); err != nil {
		return nil, err
	}
	if s.Store == nil || s.Backend == nil || s.Authorize == nil {
		return nil, owned.ErrLifecycleAuthority
	}
	if err := s.Authorize(ctx, run); err != nil {
		return nil, err
	}
	state, err := s.Store.BeginLifecycleJob(ctx, run)
	if err != nil {
		return nil, err
	}
	receipt := state.Receipt
	if receipt.Terminal() {
		if !state.Cleaned {
			if checkErr := s.Authorize(ctx, run); checkErr != nil {
				return &receipt, checkErr
			}
			if checkErr := s.Backend.CleanupLifecycleJob(ctx, run, receipt); checkErr != nil {
				return &receipt, checkErr
			}
			if checkErr := s.Store.CleanLifecycleJob(ctx, run, receipt); checkErr != nil {
				return &receipt, checkErr
			}
		}
		if receipt.State == "succeeded" {
			return &receipt, nil
		}
		if receipt.Attempt >= run.Job.MaxAttempts {
			return &receipt, owned.ErrLifecycleFailed
		}
		return &receipt, owned.ErrLifecyclePending
	}
	if checkErr := s.Authorize(ctx, run); checkErr != nil {
		return nil, checkErr
	}
	issued, err := s.Store.IssueLifecycleJob(ctx, run, receipt.Attempt)
	if err != nil {
		return nil, err
	}
	var observed *LifecycleJobReceipt
	if issued {
		observed, err = s.Backend.CreateLifecycleJob(ctx, run, receipt.Attempt)
	} else {
		observed, err = s.Backend.ObserveLifecycleJob(ctx, run, receipt.Attempt)
	}
	if err != nil {
		return nil, err
	}
	if observed == nil || observed.RunID != run.ID() || observed.Digest != run.Digest() || observed.NamespaceUID != run.NamespaceUID || observed.Name != run.AttemptName(receipt.Attempt) || observed.Attempt != receipt.Attempt || observed.JobUID == "" || observed.SecretUID == "" {
		return nil, owned.ErrLifecycleAuthority
	}
	if checkErr := s.Store.RecordLifecycleJob(ctx, run, *observed); checkErr != nil {
		return nil, checkErr
	}
	if !observed.Terminal() {
		return observed, owned.ErrLifecyclePending
	}
	// The next reconciliation cleans only the UIDs in the saved receipt.
	return observed, owned.ErrLifecyclePending
}
