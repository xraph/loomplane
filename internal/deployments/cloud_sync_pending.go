package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/planning"
)

// PendingCloudResource resolves a provider resource created before binding. An
// empty ID explicitly confirms that the checkpoint created no serving resource.
// An unknown identity must return an error so credentials and guards stay held.
type PendingCloudResource interface {
	PendingCloudResource(context.Context, *Job) (string, error)
}

type cloudCleanupAuthorizer interface {
	ValidateCloudSyncCleanup(context.Context, *planning.Plan, string) error
}

func (r *Runner) authorizeCloudRemoval(ctx context.Context, job *Job) error {
	if job.cleanupSyncID == "" {
		return r.authorizeSync(ctx, job.Plan)
	}
	auth, ok := r.cloudSyncAuth.(cloudCleanupAuthorizer)
	if !ok {
		return errors.New("compensation cleanup authorization is unavailable")
	}
	return auth.ValidateCloudSyncCleanup(ctx, job.Plan, job.cleanupSyncID)
}

// CleanupRevertedCloudPlan resolves unpublished forward resources before the
// compensation releases its original operation's guards and credentials.
func (r *Runner) CleanupRevertedCloudPlan(ctx context.Context, org, planID, compensationID string, heartbeat func([]byte) error) error {
	plan, err := r.plans.GetPlan(ctx, org, planID)
	if err != nil {
		return err
	}
	if plan.CloudSyncID == "" || compensationID == "" {
		return cloudprofiles.ErrInvalid
	}
	if authErr := r.authorizeCloudRemoval(ctx, &Job{Plan: plan, cleanupSyncID: compensationID}); authErr != nil {
		return authErr
	}
	d, err := r.store.ForPlan(ctx, org, planID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	bindings, err := r.plans.ListBindings(ctx, org, plan.EnvironmentID)
	if err != nil {
		return err
	}
	for _, a := range plan.Actions {
		step := d.Step(a.ID)
		if step == nil || step.Status == StepSucceeded || step.CloudCleanupComplete {
			continue
		}
		if step.StartedAt == nil && len(step.Checkpoint) == 0 && step.Replaced == nil {
			continue
		}
		job := &Job{Deployment: d, Plan: plan, Action: a, cleanupSyncID: compensationID, Replaces: step.Replaced, Progress: func(string, string) {}}
		if authErr := r.authorizeCloudRemoval(ctx, job); authErr != nil {
			return authErr
		}
		complete := func() error { step.CloudCleanupComplete = true; return r.store.UpdateStep(ctx, org, d.ID, *step) }
		if a.Op == planning.Keep {
			if completeErr := complete(); completeErr != nil {
				return completeErr
			}
			continue
		}
		if a.Op != planning.Replace {
			restored := false
			for _, b := range bindings {
				if b.Subject == a.Subject && b.Name == a.Name && b.PlanID == fmt.Sprintf("%s.%s", compensationID, plan.EnvironmentID) {
					restored = true
				}
			}
			if !restored {
				return fmt.Errorf("unfinished update of %s has not been restored by compensation; retry its compensation deployment", a.Name)
			}
			if completeErr := complete(); completeErr != nil {
				return completeErr
			}
			continue
		}
		exec, ok := r.executors[a.Provider]
		if !ok {
			return ErrNoExecutor
		}
		pending, ok := exec.(PendingCloudResource)
		if !ok {
			return fmt.Errorf("%s cannot safely resolve the unfinished %s checkpoint; recover its provider resource identity before retrying cleanup", a.Provider, a.Name)
		}
		conn, credential, e := r.credentials.Credential(ctx, org, a.ConnectionID)
		if e != nil {
			return e
		}
		job.Connection, job.Credential = conn, credential
		if a.Service != nil && !reflect.DeepEqual(a.Service.TargetSettings, planning.ReviewedConnectionSettings(conn.Cloud, conn.Details)) {
			return errors.New("the original target settings changed; restore the reviewed settings before retrying cleanup")
		}
		rc := readReplaceCheckpoint(step.Checkpoint)
		job.Resume = rc.Apply
		id := rc.PendingResource
		if id == "" {
			id, e = pending.PendingCloudResource(ctx, job)
			if e != nil {
				return e
			}
			if id != "" {
				rc.PendingResource = id
				step.Checkpoint = rc.encode()
				if saveErr := r.store.UpdateStep(ctx, org, d.ID, *step); saveErr != nil {
					return saveErr
				}
			}
		}
		protected := false
		for _, b := range bindings {
			if b.Provider == a.Provider && b.ConnectionID == a.ConnectionID && b.ExternalID == id {
				protected = true
			}
		}
		if id != "" && !protected {
			raw, e := json.Marshal(a.Service)
			if e != nil {
				return e
			}
			created := &planning.Binding{OrgID: org, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, Subject: a.Subject, Name: a.Name, Provider: a.Provider, Region: a.Region, Engine: a.Engine, ConnectionID: a.ConnectionID, ExternalID: id, Config: raw, PlanID: plan.ID}
			checkpoint := func(data []byte) error {
				step.Checkpoint = append([]byte(nil), data...)
				if e := r.store.UpdateStep(ctx, org, d.ID, *step); e != nil {
					return e
				}
				return heartbeat(data)
			}
			if done, e := r.removeOne(ctx, job, created, rc, checkpoint); e != nil {
				return e
			} else if !done {
				return errors.New("unfinished provider resource still requires cleanup")
			}
		}
		if completeErr := complete(); completeErr != nil {
			return completeErr
		}
	}
	return nil
}

func pendingCheckpointID(raw []byte, key string) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", fmt.Errorf("unreadable provider checkpoint: %w", err)
	}
	var id string
	if err := json.Unmarshal(fields[key], &id); err != nil || id == "" {
		return "", errors.New("the provider resource identity is unavailable; recover the original provider operation before retrying cleanup")
	}
	return id, nil
}

// PendingCloudResource reads the exact project created by the Vercel operation.
func (v *Vercel) PendingCloudResource(_ context.Context, job *Job) (string, error) {
	return pendingCheckpointID(job.Resume, "project_id")
}

// PendingCloudResource reads the exact service created by the Render operation.
func (r *Render) PendingCloudResource(_ context.Context, job *Job) (string, error) {
	return pendingCheckpointID(job.Resume, "service_id")
}

// PendingCloudResource reads the exact App Platform app created by the operation.
func (d *DigitalOcean) PendingCloudResource(_ context.Context, job *Job) (string, error) {
	return pendingCheckpointID(job.Resume, "app_id")
}

func pendingInstance(ctx context.Context, ctrl CtrlPlane, job *Job, key string) (string, error) {
	ctx = infrastructure.WithPrincipal(ctx, principal(job))
	inst, _, err := currentInstance(ctx, ctrl, job, key)
	if err != nil {
		return "", err
	}
	if inst == nil {
		return "", nil
	}
	if inst.TenantID != job.Deployment.OrgID {
		return "", errors.New("unfinished instance is outside the operation organization")
	}
	return inst.ID.String(), nil
}

// PendingCloudResource resolves the instance through the original Cloud Run key.
func (c *CloudRun) PendingCloudResource(ctx context.Context, job *Job) (string, error) {
	return pendingInstance(ctx, c.Ctrl, job, c.provider(job))
}

// PendingCloudResource resolves the instance through the original ECS key.
func (e *ECS) PendingCloudResource(ctx context.Context, job *Job) (string, error) {
	return pendingInstance(ctx, e.Ctrl, job, e.provider(job))
}

// PendingCloudResource uses the original target and scoped instance name.
func (e *OwnedTarget) PendingCloudResource(ctx context.Context, job *Job) (string, error) {
	return pendingInstance(ctx, e.Ctrl, job, e.Register(job.Deployment.OrgID, job.Connection.ID))
}

func checkpointCloudIdentity(job *Job, state any) error {
	if job.Plan == nil || job.Plan.CloudSyncID == "" {
		return nil
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if job.Checkpoint == nil {
		return errors.New("cloud sync resource identity needs a durable checkpoint")
	}
	return job.Checkpoint(raw)
}
