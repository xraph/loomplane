package deployments

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/security"
)

// CapacityWait records every known blocker for one admission assessment.
type CapacityWait struct {
	Revision  int64                      `json:"revision"`
	Blocks    []cloudinfra.CapacityBlock `json:"blocks"`
	CreatedAt time.Time                  `json:"created_at"`
}

// ErrCapacityWaitConflict requires the caller to reload the current wait.
var ErrCapacityWaitConflict = errors.New("deployment capacity wait changed")

// ErrCapacityPaused stops new workload effects until admission succeeds.
var ErrCapacityPaused = capacityFenceError(StatusPaused)

// ErrCapacityCancelled stops a deployment whose wait was cancelled.
var ErrCapacityCancelled = capacityFenceError(StatusCancelled)

// CapacityResolver checks every approved intent and persists admitted placements.
// Joined CapacityAdmissionErrors report independent blockers. Other errors fail.
type CapacityResolver interface {
	ResolveCapacityIntents(context.Context, string, string) error
}

func capacityBlocks(err error) ([]cloudinfra.CapacityBlock, error) {
	if err == nil {
		return nil, nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var blocks []cloudinfra.CapacityBlock
		for _, part := range joined.Unwrap() {
			b, e := capacityBlocks(part)
			if e != nil {
				return nil, e
			}
			blocks = append(blocks, b...)
		}
		return canonicalBlocks(blocks), nil
	}
	// Admission errors themselves unwrap to ErrConflict. Recognize that leaf
	// before unwrapping, but traverse wrappers around joins to retain every block.
	if admission, ok := err.(*cloudinfra.CapacityAdmissionError); ok { //nolint:errorlint // Match this leaf before unwrapping; errors.As would hide siblings inside wrapped joins.
		return []cloudinfra.CapacityBlock{admission.Block}, nil
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return capacityBlocks(wrapped.Unwrap())
	}
	return nil, err
}

func canonicalBlocks(blocks []cloudinfra.CapacityBlock) []cloudinfra.CapacityBlock {
	out := slices.Clone(blocks)
	key := func(b cloudinfra.CapacityBlock) string { return fmt.Sprintf("%#v", b) }
	slices.SortFunc(out, func(a, b cloudinfra.CapacityBlock) int { return strings.Compare(key(a), key(b)) })
	return slices.Compact(out)
}

// AssessCapacity checks the whole plan before a workload starts. It never edits
// the approved plan. The workflow hydrates its execution copy from admitted pins.
func (r *Runner) AssessCapacity(ctx context.Context, orgID, deploymentID string) (*CapacityWait, error) {
	d, err := r.store.GetDeployment(ctx, orgID, deploymentID)
	if err != nil {
		return nil, err
	}
	if d.Status == StatusCancelled {
		return nil, ErrCapacityCancelled
	}
	if d.Status != StatusRunning && d.Status != StatusPaused {
		return nil, ErrCapacityWaitConflict
	}
	plan, err := r.plans.GetPlan(ctx, orgID, d.PlanID)
	if err != nil {
		return nil, err
	}
	needs := false
	for _, a := range plan.Actions {
		if a.Service != nil && a.Service.CapacityIntent != nil {
			needs = true
		}
	}
	if r.capacity == nil {
		if needs || d.Status == StatusPaused {
			return nil, errors.New("capacity resolver is unavailable")
		}
		return nil, nil
	}
	blocks, err := capacityBlocks(r.capacity.ResolveCapacityIntents(ctx, orgID, d.PlanID))
	if err != nil {
		return nil, err
	}
	st, ok := r.store.(CapacityStore)
	if !ok {
		return nil, errors.New("capacity wait store is unavailable")
	}
	if len(blocks) == 0 {
		if d.Status == StatusPaused {
			_, err = st.TransitionCapacityWait(ctx, orgID, d.ID, d.CapacityRevision, StatusRunning, r.now().UTC())
		}
		return nil, err
	}
	updated, err := st.SetCapacityWait(ctx, orgID, d.ID, d.CapacityRevision, blocks, r.now().UTC())
	if err != nil {
		return nil, err
	}
	return updated.CapacityWait, nil
}

// ResumeCapacity requests another assessment through the durable workflow queue.
// It keeps the original deployment, steps and retry number.
func (s *Service) ResumeCapacity(ctx context.Context, p security.Principal, id string, expectedWaitRevision int64) (*Deployment, error) {
	return s.transitionCapacity(ctx, p, id, expectedWaitRevision, StatusRunning)
}

// CancelCapacityWait detaches this plan's demands and preserves accepted provider
// operations for reconciliation. Cancellation does not roll back completed steps.
func (s *Service) CancelCapacityWait(ctx context.Context, p security.Principal, id string, expectedWaitRevision int64) (*Deployment, error) {
	return s.transitionCapacity(ctx, p, id, expectedWaitRevision, StatusCancelled)
}
func (s *Service) transitionCapacity(ctx context.Context, p security.Principal, id string, revision int64, status string) (*Deployment, error) {
	d, err := s.store.GetDeployment(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if authErr := s.authz.Require(ctx, p, security.DeploymentApprove, d.ProjectID); authErr != nil {
		return nil, authErr
	}
	st, ok := s.store.(CapacityStore)
	if !ok {
		return nil, errors.New("capacity wait store is unavailable")
	}
	return st.TransitionCapacityWait(ctx, p.OrgID, id, revision, status, s.now().UTC())
}
