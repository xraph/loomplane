package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

type RuntimeRemovalGuard interface {
	ValidateRuntimeRemoval(context.Context, string, string, string) error
}
type ReleaseRoutes interface {
	RuntimeRemovalGuard
	ReconcileReleaseRoutes(context.Context, security.Principal, cloudinfra.ReleaseRouteRequest) error
}

type routePlanStore interface {
	CurrentReleaseRoutes(context.Context, string, string, string, string) ([]cloudinfra.Route, error)
}

func (r *Runner) validateReleaseRoutes(ctx context.Context, job *Job, current *planning.Binding) error {
	store, ok := r.plans.(routePlanStore)
	if !ok {
		if len(job.Action.Routes) > 0 {
			return errors.New("release route storage is unavailable")
		}
		return nil
	}
	routes, err := store.CurrentReleaseRoutes(ctx, job.Deployment.OrgID, job.Deployment.ProjectID, job.Deployment.EnvironmentID, job.Action.Name)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(routes, job.Action.Routes) && (len(routes) > 0 || len(job.Action.Routes) > 0) {
		return errors.New("the service routes changed since review; plan again before changing its runtime")
	}
	if len(routes) > 0 {
		if current == nil || job.Action.Op != planning.Replace || job.Action.Service == nil || job.Action.Service.CloudPlacement == nil || job.Action.Service.CloudPlacement.Allocation == "" {
			return errors.New("routed release requires reviewed retained-instance capacity; plan again")
		}
		var source planning.ServiceConfig
		if decodeErr := json.Unmarshal(current.Config, &source); decodeErr != nil {
			return decodeErr
		}
		candidate := job.Action.Service.CloudPlacement
		if candidate.Allocation != job.Plan.ID || (source.CloudPlacement != nil && source.CloudPlacement.Allocation == candidate.Allocation) {
			return errors.New("routed candidate allocation differs from its accepted plan")
		}
		if r.routes == nil {
			return errors.New("release route coordinator is unavailable")
		}
	}
	return nil
}

func (r *Runner) finishRoutedRelease(ctx context.Context, d *Deployment) error {
	plan, err := r.plans.GetPlan(ctx, d.OrgID, d.PlanID)
	if err != nil {
		return err
	}
	if plan.CloudSyncID != "" {
		return nil
	}
	return r.cleanupReleaseRoutes(ctx, d, plan)
}

func (r *Runner) cleanupReleaseRoutes(ctx context.Context, d *Deployment, plan *planning.Plan) error {
	hasRoutes := false
	for _, a := range plan.Actions {
		hasRoutes = hasRoutes || len(a.Routes) > 0
	}
	if !hasRoutes {
		return nil
	}
	if err := reviewedActionsComplete(d, plan); err != nil {
		return err
	}
	for _, a := range plan.Actions {
		if len(a.Routes) == 0 {
			continue
		}
		if err := r.cleanupRoutedAction(ctx, d, plan, a); err != nil {
			return err
		}
	}
	return nil
}

func reviewedActionsComplete(d *Deployment, plan *planning.Plan) error {
	for _, a := range plan.Actions {
		if a.Op == planning.Keep && !a.AdoptCloudPlacement {
			continue
		}
		step := d.Step(a.ID)
		if step == nil || step.Status != StepSucceeded {
			return errors.New("all reviewed release actions must finish before route cleanup")
		}
	}
	return nil
}

func (r *Runner) cleanupRoutedAction(ctx context.Context, d *Deployment, plan *planning.Plan, a planning.Action) (cleanupErr error) {
	poolLock, err := r.beginPoolExecution(ctx, plan, a)
	if err != nil {
		return err
	}
	if poolLock != nil {
		ctx = poolEffectContext(poolLock.Context(ctx))
		defer func() { cleanupErr = errors.Join(cleanupErr, poolLock.Finish(context.WithoutCancel(ctx))) }()
	}
	ctx, finishAttempt, err := r.beginWorkloadAttempt(ctx, d, a, true)
	if err != nil {
		return err
	}
	defer func() { cleanupErr = errors.Join(cleanupErr, finishAttempt(cleanupErr == nil)) }()
	fresh, err := r.store.GetDeployment(ctx, d.OrgID, d.ID)
	if err != nil {
		return err
	}
	if checkErr := reviewedActionsComplete(fresh, plan); checkErr != nil {
		return checkErr
	}
	bindings, err := r.plans.ListBindings(ctx, d.OrgID, d.EnvironmentID)
	if err != nil {
		return err
	}
	for _, bound := range bindings {
		if bound.Subject != a.Subject || bound.Name != a.Name {
			continue
		}
		if bound.PlanID != plan.ID {
			return errors.New("a newer release owns the current binding")
		}
		if r.routes == nil {
			return errors.New("release route coordinator is unavailable")
		}
		step := fresh.Step(a.ID)
		job := &Job{Deployment: fresh, Plan: plan, Action: a, Binding: bound, Progress: func(string, string) {}}
		in := cloudinfra.ReleaseRouteRequest{Key: plan.ID + "/" + a.ID, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, Service: a.Name, CloudID: a.Service.CloudPlacement.CloudID, InstanceID: bound.ExternalID, Routes: a.Routes, DrainSeconds: 60}
		if routeErr := r.routes.ReconcileReleaseRoutes(ctx, principal(job), in); routeErr != nil {
			return fmt.Errorf("route %s: %w", a.Name, routeErr)
		}
		rc := readReplaceCheckpoint(step.Checkpoint)
		checkpoint := func(raw []byte) error {
			step.Checkpoint = append([]byte(nil), raw...)
			return r.store.UpdateStep(ctx, d.OrgID, d.ID, *step)
		}
		if removeErr := r.removeReplaced(ctx, job, bound, step.Replaced, nil, rc, checkpoint); removeErr != nil {
			return removeErr
		}
		step.Replaced = nil
		return r.store.UpdateStep(ctx, d.OrgID, d.ID, *step)
	}
	return errors.New("the routed release binding is missing")
}

func (r *Runner) beginWorkloadAttempt(ctx context.Context, d *Deployment, a planning.Action, finalizing bool) (context.Context, func(bool) error, error) {
	st, ok := r.store.(*PgStore)
	if !ok || a.Provider != "server" || a.Op == planning.Keep {
		return ctx, func(bool) error { return nil }, nil
	}
	infra := cloudinfra.NewPgStore(st.db)
	attempt, err := infra.BeginWorkloadAttempt(ctx, cloudinfra.WorkloadAttempt{Org: d.OrgID, Project: d.ProjectID, Environment: d.EnvironmentID, Service: a.Name, Plan: d.PlanID, Deployment: d.ID}, a.Routes, finalizing)
	if err != nil {
		return ctx, nil, err
	}
	ctx = attempt.Context(ctx)
	if _, pooled := cloudinfra.ExecutionFrom(ctx); !pooled {
		ctx = server.WithEffectFence(ctx, server.EffectFence{Operation: d.OrgID + "/" + d.PlanID + "/" + a.Name, Generation: attempt.Generation})
	}
	return ctx, func(settled bool) error {
		return infra.FinishWorkloadAttempt(context.WithoutCancel(ctx), *attempt, settled)
	}, nil
}
