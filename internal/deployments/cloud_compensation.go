package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

type compensationRunKey struct{}
type compensationRoutes interface {
	PrepareCompensationRoutes(context.Context, security.Principal, cloudinfra.ReleaseRouteRequest) error
	ReconcileCompensationRoutes(context.Context, security.Principal, cloudinfra.ReleaseRouteRequest) error
}

func (r *Runner) runCloudCompensation(ctx context.Context, d *Deployment, plan *planning.Plan, a planning.Action, step *Step, heartbeat func([]byte) error) (runErr error) {
	c := a.Compensation
	st, ok := r.store.(*PgStore)
	if !ok {
		return errors.New("compensation requires durable attempt storage")
	}
	auth, ok := r.cloudSyncAuth.(interface {
		ValidateCloudSyncExecution(context.Context, *planning.Plan) (security.Principal, error)
	})
	if !ok {
		return errors.New("compensation execution authorization is unavailable")
	}
	actor, err := auth.ValidateCloudSyncExecution(ctx, plan)
	if err != nil {
		return err
	}
	original, err := r.plans.GetPlan(ctx, d.OrgID, c.PlanID)
	if err != nil {
		return err
	}
	var old planning.Action
	for _, action := range original.Actions {
		if action.ID == c.ActionID {
			old = action
		}
	}
	if old.ID == "" {
		return errors.New("original compensation action is missing")
	}
	infra := cloudinfra.NewPgStore(st.db)
	next := cloudinfra.WorkloadAttempt{Org: d.OrgID, Project: d.ProjectID, Environment: d.EnvironmentID, Service: a.Name, Plan: plan.ID, Deployment: d.ID}
	var owner string
	var settled, legacy bool
	err = st.db.QueryRow(ctx, `SELECT plan_id,settled,legacy_unfenced FROM loomplane_workload_attempts WHERE org_id=$1 AND environment_id=$2 AND service=$3`, d.OrgID, d.EnvironmentID, a.Name).Scan(&owner, &settled, &legacy)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	priorSource := owner == c.Source.PlanID && settled && !legacy
	if owner != "" && owner != original.ID && owner != plan.ID && !priorSource {
		return errors.New("another release owns the service attempt")
	}
	var transferred *cloudinfra.WorkloadAttempt
	if owner == plan.ID {
		transferred, err = infra.BeginWorkloadAttempt(ctx, next, nil, true)
		if err != nil {
			return err
		}
	} else {
		previous, readErr := r.store.ForPlan(ctx, d.OrgID, original.ID)
		if errors.Is(readErr, ErrNotFound) {
			previous = &Deployment{ID: original.ID + "/unstarted", OrgID: d.OrgID, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, PlanID: original.ID, StartedBy: actor.UserID}
		} else if readErr != nil {
			return readErr
		}
		if priorSource {
			if priorStep := previous.Step(old.ID); priorStep != nil && (priorStep.StartedAt != nil || len(priorStep.Checkpoint) != 0 || priorStep.Replaced != nil) {
				return errors.New("original effect has no matching durable attempt")
			}
		}
		execution := *previous
		execution.StartedBy = actor.UserID
		oldCtx := ctx
		lock, lockErr := r.beginPoolExecution(ctx, original, old)
		if lockErr != nil {
			return lockErr
		}
		if lock != nil {
			defer lock.Close()
			oldCtx = poolEffectContext(lock.Context(oldCtx))
		}
		oldCtx, _, err = r.beginWorkloadAttempt(oldCtx, previous, old, !priorSource)
		if err != nil {
			return err
		}
		attempt, found := cloudinfra.WorkloadAttemptFrom(oldCtx)
		if !found {
			return errors.New("original service has no fenced attempt")
		}
		if checkErr := r.markWorkloadEffects(oldCtx); checkErr != nil {
			return checkErr
		}
		job := &Job{BuildCredentials: r.credentials, Deployment: &execution, Plan: original, Action: old, cleanupSyncID: plan.CloudSyncID, Progress: func(string, string) {}}
		if checkErr := r.prepareCompensationEndpoint(oldCtx, job); checkErr != nil {
			return checkErr
		}
		// The original release can also remove its retained source on another host.
		sourceJob := *job
		sourceJob.Action.Provider, sourceJob.Action.ConnectionID = c.Source.Provider, c.Source.ConnectionID
		sourceJob.Action.Service = nil
		if checkErr := json.Unmarshal(c.Source.Config, &sourceJob.Action.Service); checkErr != nil {
			return checkErr
		}
		if c.Source.Provider == "server" {
			if checkErr := r.prepareCompensationEndpoint(oldCtx, &sourceJob); checkErr != nil {
				return checkErr
			}
		}
		if c.Retained {
			if len(c.Routes) > 0 {
				routes, available := r.routes.(compensationRoutes)
				if !available {
					return errors.New("compensation route coordinator is unavailable")
				}
				if checkErr := routes.PrepareCompensationRoutes(oldCtx, actor, compensationRouteRequest(d, a, nil, false)); checkErr != nil {
					return checkErr
				}
			}
			if checkErr := r.observeCompensationSource(ctx, c.Source); checkErr != nil {
				return checkErr
			}
		}
		if checkErr := r.resolveCompensationPending(oldCtx, job, previous.Step(old.ID), c, heartbeat); checkErr != nil {
			return checkErr
		}
		if _, err = auth.ValidateCloudSyncExecution(oldCtx, plan); err != nil {
			return err
		}
		if checkErr := r.validateCloudPlacement(oldCtx, plan, a, nil); checkErr != nil {
			return checkErr
		}
		if c.Retained {
			restore, available := r.plans.(interface {
				RestoreCloudCompensation(context.Context, cloudinfra.WorkloadAttempt, cloudinfra.WorkloadAttempt, *planning.Plan, planning.Action) (*cloudinfra.WorkloadAttempt, error)
			})
			if !available {
				return errors.New("binding compensation storage is unavailable")
			}
			transferred, err = restore.RestoreCloudCompensation(oldCtx, attempt, next, plan, a)
		} else {
			transferred, err = infra.TransferWorkloadAttempt(oldCtx, attempt, next)
		}
		if err != nil {
			return err
		}
	}
	ctx = transferred.Context(ctx)
	ctx = server.WithEffectFence(ctx, server.EffectFence{Operation: d.OrgID + "/" + d.PlanID + "/" + a.Name, Generation: transferred.Generation})
	if !c.Retained {
		return r.Run(context.WithValue(ctx, compensationRunKey{}, true), d.OrgID, d.ID, a.ID, nil, heartbeat)
	}
	defer func() {
		runErr = errors.Join(runErr, infra.FinishWorkloadAttempt(context.WithoutCancel(ctx), *transferred, false))
	}()
	bindings, err := r.plans.ListBindings(ctx, d.OrgID, d.EnvironmentID)
	if err != nil {
		return err
	}
	for _, b := range bindings {
		if b.ID != c.Source.ID {
			continue
		}
		if b.PlanID != plan.ID || b.ExternalID != c.Source.ExternalID || b.ConnectionID != c.Source.ConnectionID {
			return errors.New("restored source binding ownership changed")
		}
		if checkErr := r.observeCompensationSource(ctx, c.Source); checkErr != nil {
			return checkErr
		}
		now := r.now().UTC()
		step.StartedAt, step.FinishedAt = &now, &now
		step.Status = StepSucceeded
		step.Message = "Retained source restored; routing verification is pending."
		step.URL = b.URL
		return r.store.UpdateStep(ctx, d.OrgID, d.ID, *step)
	}
	return errors.New("restored source binding is missing")
}

func (r *Runner) prepareCompensationEndpoint(ctx context.Context, job *Job) error {
	var err error
	job.Connection, job.Credential, err = r.credentials.Credential(ctx, job.Deployment.OrgID, job.Action.ConnectionID)
	if err != nil {
		return err
	}
	executor := r.executors[job.Action.Provider]
	if executor == nil {
		return ErrNoExecutor
	}
	return preparePoolEffects(ctx, executor, job)
}
func (r *Runner) observeCompensationSource(ctx context.Context, source planning.Binding) error {
	conn, credential, err := r.credentials.Credential(ctx, source.OrgID, source.ConnectionID)
	if err != nil {
		return err
	}
	executor := r.executors[source.Provider]
	if executor == nil {
		return ErrNoExecutor
	}
	observation, err := executor.Observe(ctx, conn, credential, &source)
	if err != nil {
		return err
	}
	if observation == nil || !observation.Exists || observation.Revision != "" && observation.Revision != source.Revision {
		return errors.New("the retained source instance no longer matches its saved revision")
	}
	return nil
}
func compensationRouteRequest(d *Deployment, a planning.Action, checkpoint func(cloudprofiles.SyncRoute) error, cleanup bool) cloudinfra.ReleaseRouteRequest {
	c := a.Compensation
	return cloudinfra.ReleaseRouteRequest{Key: c.PlanID + "/" + c.ActionID, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, Service: a.Name, InstanceID: c.Source.ExternalID, Routes: c.Routes, DrainSeconds: 60, Checkpoint: checkpoint, CleanupOnly: cleanup}
}

func (r *Runner) resolveCompensationPending(ctx context.Context, job *Job, step *Step, c *planning.CloudCompensation, heartbeat func([]byte) error) error {
	if step == nil {
		return r.releaseCompensationReservation(ctx, job)
	}
	if step.Status == StepSucceeded || step.CloudCleanupComplete {
		return nil
	}
	if step.StartedAt == nil && len(step.Checkpoint) == 0 && step.Replaced == nil {
		return r.releaseCompensationReservation(ctx, job)
	}
	if job.Action.Op != planning.Replace {
		return nil
	}
	pending, ok := r.executors[job.Action.Provider].(PendingCloudResource)
	if !ok {
		return errors.New("original executor cannot resolve its unfinished resource")
	}
	rc := readReplaceCheckpoint(step.Checkpoint)
	job.Resume = rc.Apply
	id, err := pending.PendingCloudResource(ctx, job)
	if err != nil {
		return err
	}
	if id != "" && id != c.Source.ExternalID && id != c.Candidate.ExternalID {
		raw, encodeErr := json.Marshal(job.Action.Service)
		if encodeErr != nil {
			return encodeErr
		}
		bound := &planning.Binding{OrgID: job.Deployment.OrgID, ProjectID: job.Deployment.ProjectID, EnvironmentID: job.Deployment.EnvironmentID, Name: job.Action.Name, Subject: job.Action.Subject, Provider: job.Action.Provider, ConnectionID: job.Action.ConnectionID, ExternalID: id, Config: raw, PlanID: job.Plan.ID}
		rc.PendingResource = id
		checkpoint := func(data []byte) error {
			step.Checkpoint = data
			if saveErr := r.store.UpdateStep(ctx, job.Deployment.OrgID, job.Deployment.ID, *step); saveErr != nil {
				return saveErr
			}
			return heartbeat(data)
		}
		if done, removeErr := r.removeOne(ctx, job, bound, rc, checkpoint); removeErr != nil {
			return removeErr
		} else if !done {
			return fmt.Errorf("unfinished candidate %s still needs cleanup", id)
		}
	}
	if checkErr := r.releaseCompensationReservation(ctx, job); checkErr != nil {
		return checkErr
	}
	step.CloudCleanupComplete = true
	return r.store.UpdateStep(ctx, job.Deployment.OrgID, job.Deployment.ID, *step)
}

func (r *Runner) releaseCompensationReservation(ctx context.Context, job *Job) error {
	st, ok := r.store.(*PgStore)
	if !ok {
		return errors.New("compensation requires durable reservation storage")
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // rollback after commit is harmless
	if checkErr := cloudinfra.GuardPoolWrite(ctx, tx); checkErr != nil {
		return checkErr
	}
	if _, err = tx.Exec(ctx, `DELETE FROM loomplane_capacity_reservations WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND service=$4 AND plan_id=$5 AND binding_id=''`, job.Deployment.OrgID, job.Deployment.ProjectID, job.Deployment.EnvironmentID, job.Action.Name, job.Plan.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Runner) validateCompensationCapability(ctx context.Context, d *Deployment, plan *planning.Plan, action planning.Action) error {
	c := action.Compensation
	original, err := r.plans.GetPlan(ctx, d.OrgID, c.PlanID)
	if err != nil {
		return err
	}
	var found bool
	for _, prior := range original.Actions {
		if prior.ID == c.ActionID {
			found = true
			if (prior.Provider == "server") != c.ServerAttempt {
				return errors.New("original runtime capability differs from the accepted compensation")
			}
		}
	}
	if !found {
		return errors.New("original compensation action is missing")
	}
	if c.ServerAttempt == (c.Source.Provider == "server") {
		return nil
	}
	if !c.OriginalCompleted {
		return errors.New("finish or reconcile the original mixed-runtime change, then review its revert again")
	}
	previous, err := r.store.ForPlan(ctx, d.OrgID, original.ID)
	if err != nil {
		return err
	}
	if previous.Status != StatusSucceeded {
		return errors.New("mixed-runtime compensation requires a completed original deployment")
	}
	if !c.ServerAttempt {
		return nil
	}
	st, ok := r.store.(*PgStore)
	if !ok {
		return errors.New("mixed-runtime compensation requires durable attempt storage")
	}
	var safe bool
	err = st.db.QueryRow(ctx, `SELECT NOT legacy_unfenced AND (plan_id=$4 OR (plan_id=$5 AND settled AND NOT active)) FROM loomplane_workload_attempts WHERE org_id=$1 AND environment_id=$2 AND service=$3`, d.OrgID, d.EnvironmentID, action.Name, plan.ID, original.ID).Scan(&safe)
	if err != nil || !safe {
		return errors.New("finish the original server attempt before recreating a managed source")
	}
	return nil
}
