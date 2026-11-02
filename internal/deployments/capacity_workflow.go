package deployments

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
)

var ErrManagedCleanupReviewRequired = errors.New("managed cleanup requires a fresh review")
var ErrManagedCleanupProtocol = errors.New("managed cleanup requires a compatible cleanup workflow")

type capacityFenceError string

func (e capacityFenceError) Error() string          { return "deployment capacity state: " + string(e) }
func (e capacityFenceError) CapacityStatus() string { return string(e) }

// AssessWorkflowCapacity exposes durable state without sharing mutable runner types.
func (r *Runner) AssessWorkflowCapacity(ctx context.Context, org, id string) (orchestration.CapacityAssessment, error) {
	wait, err := r.AssessCapacity(ctx, org, id)
	if errors.Is(err, ErrCapacityCancelled) {
		return orchestration.CapacityAssessment{Status: StatusCancelled}, nil
	}
	if errors.Is(err, ErrCapacityWaitConflict) {
		d, e := r.store.GetDeployment(ctx, org, id)
		if e != nil {
			return orchestration.CapacityAssessment{}, e
		}
		if d.Status == StatusPaused {
			return orchestration.CapacityAssessment{Status: StatusPaused, Revision: d.CapacityRevision}, nil
		}
		if d.Status == StatusSucceeded || d.Status == StatusFailed {
			return orchestration.CapacityAssessment{Status: StatusRunning}, nil
		}
		if d.Status == StatusRunning {
			return orchestration.CapacityAssessment{Status: StatusPaused, Revision: d.CapacityRevision}, nil
		}
	}
	if err != nil {
		return orchestration.CapacityAssessment{}, err
	}
	if wait != nil {
		return orchestration.CapacityAssessment{Status: StatusPaused, Revision: wait.Revision}, nil
	}
	return orchestration.CapacityAssessment{Status: StatusRunning}, nil
}

func deferredPlan(plan *planning.Plan) bool {
	if plan.RequiredRuntimeProtocol != "" {
		return true
	}
	for _, a := range plan.Actions {
		if planning.HasCapacityIntent(a) {
			return true
		}
	}
	return false
}
func legacyCapacityGuard(ctx context.Context, plan *planning.Plan) error {
	required := plan.RequiredRuntimeProtocol
	for _, a := range plan.Actions {
		if len(a.CleanupTargets) > 0 && required != orchestration.ManagedCleanupProtocol {
			return ErrManagedCleanupReviewRequired
		}
		if a.Service != nil && a.Service.ClusterDemand != nil && required == "" {
			return errors.New("managed workload is missing its required execution protocol; plan again")
		}
	}
	if required == orchestration.ManagedCleanupProtocol {
		if !orchestration.IsManagedCleanupContext(ctx) {
			return ErrManagedCleanupProtocol
		}
	} else if required != "" && (required != orchestration.ManagedWorkloadProtocol || !orchestration.IsManagedWorkloadContext(ctx)) {
		return errors.New("managed workload requires a compatible journal workflow")
	}

	if deferredPlan(plan) && !orchestration.IsCapacityContext(ctx) {
		return errors.New("deferred capacity requires a v3 workflow; plan again")
	}
	return nil
}

// executionPlan copies the review, then loads only placements admitted in storage.
func (r *Runner) executionPlan(ctx context.Context, plan *planning.Plan) (*planning.Plan, error) {
	if !deferredPlan(plan) {
		return plan, nil
	}
	if err := legacyCapacityGuard(ctx, plan); err != nil {
		return nil, err
	}
	reader, ok := r.plans.(interface {
		ResolvedCapacityPlacements(context.Context, string, string) (map[string]planning.CloudPlacement, error)
	})
	if !ok {
		return nil, errors.New("admitted placement reader is unavailable")
	}
	pins, err := reader.ResolvedCapacityPlacements(ctx, plan.OrgID, plan.ID)
	if err != nil {
		return nil, err
	}
	copyPlan := *plan
	copyPlan.Actions = slices.Clone(plan.Actions)
	for i, a := range copyPlan.Actions {
		if a.Service == nil || a.Service.CapacityIntent == nil {
			continue
		}
		pin, found := pins[a.Name]
		if !found {
			return nil, ErrCapacityPaused
		}
		intent := a.Service.CapacityIntent
		if pin.PoolID != intent.Demand.PoolID || pin.TargetID != intent.Demand.TargetID || pin.CloudID != intent.Demand.CloudID || pin.CPUMillis != intent.Demand.Resources.CPUMillis || pin.MemoryBytes != intent.Demand.Resources.MemoryBytes {
			return nil, fmt.Errorf("admitted placement for %s differs from approved demand", a.Name)
		}
		conn, _, e := r.credentials.Credential(ctx, plan.OrgID, pin.ConnectionID)
		if e != nil {
			return nil, e
		}
		if conn == nil || conn.Generation != pin.ConnectionGeneration {
			return nil, errors.New("admitted runtime connection changed")
		}
		cfg := *a.Service
		cfg.CapacityIntent = nil
		cfg.CloudPlacement = &pin
		cfg.Target = pin.ConnectionID
		cfg.Provider = pin.Provider
		cfg.Region = pin.Region
		cfg.TargetSettings = planning.ReviewedConnectionSettings(conn.Cloud, conn.Details)
		a.Service = &cfg
		a.ConnectionID = pin.ConnectionID
		a.Provider = pin.Provider
		a.Region = pin.Region
		if placementErr := r.validateCloudPlacement(ctx, &copyPlan, a, conn); placementErr != nil {
			return nil, placementErr
		}
		copyPlan.Actions[i] = a
	}
	return &copyPlan, nil
}

func (r *Runner) StartCapacityCloudPlan(ctx context.Context, org, plan string) (id string, steps [][]string, err error) {
	id, steps, err = r.Start(ctx, org, plan)
	if err != nil || id == "" {
		return id, steps, err
	}
	d, err := r.store.GetDeployment(ctx, org, id)
	if err != nil {
		return id, steps, err
	}
	if d.Status == StatusFailed {
		_, err = r.store.Reopen(context.WithValue(ctx, cloudRetryKey{}, true), org, id)
	}
	return id, steps, err
}
func (r *Runner) CapacityCloudPlanStatus(ctx context.Context, org, plan string) (string, error) {
	d, err := r.store.ForPlan(ctx, org, plan)
	if err != nil {
		return "", err
	}
	return d.Status, nil
}

func (r *Runner) WorkflowCapacityState(ctx context.Context, org, id string) (orchestration.CapacityAssessment, error) {
	d, err := r.store.GetDeployment(ctx, org, id)
	if err != nil {
		return orchestration.CapacityAssessment{}, err
	}
	return orchestration.CapacityAssessment{Status: d.Status, Revision: d.CapacityRevision}, nil
}
