package deployments

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/planning"
)

type poolWorkloadCleaner interface {
	CleanupPoolWorkload(context.Context, *Job, []byte, func([]byte) error) error
}

// CleanupPoolReservation uses the reservation's retained cleanup authority. It
// deliberately does not authorize a new deployment or resolve a current target.
func (r *Runner) CleanupPoolReservation(ctx context.Context, org, planID, service string, checkpoint []byte, save func([]byte) error) error {
	ctx = poolEffectContext(ctx)
	plan, err := r.plans.GetPlan(ctx, org, planID)
	if err != nil {
		return err
	}
	d, err := r.store.ForPlan(ctx, org, planID)
	if err != nil {
		return err
	}
	var action planning.Action
	for _, a := range plan.Actions {
		if a.Subject == planning.ServiceSubject && a.Name == service {
			action = a
		}
	}
	if !poolAction(action) || action.Provider != "server" {
		return errors.New("cleanup has no recorded pooled server action")
	}
	step := d.Step(action.ID)
	if step == nil {
		return errors.New("cleanup has no durable deployment step")
	}
	bindings, err := r.plans.ListBindings(ctx, org, plan.EnvironmentID)
	if err != nil {
		return err
	}
	for _, b := range bindings {
		if b.Subject == action.Subject && b.Name == service && b.ConnectionID == action.ConnectionID {
			return errors.New("a serving binding still owns this host; recover the accepted deployment before cleanup")
		}
	}
	conn, credential, err := r.credentials.Credential(ctx, org, action.ConnectionID)
	if err != nil {
		return err
	}
	if conn == nil || conn.OrgID != org || conn.ID != action.ConnectionID || conn.Cloud != connections.Server {
		return errors.New("cleanup connection identity differs")
	}
	cleaner, ok := r.executors[action.Provider].(poolWorkloadCleaner)
	if !ok {
		return errors.New("provider cannot verify exact pending compute cleanup")
	}
	job := &Job{Deployment: d, Plan: plan, Action: action, Connection: conn, Credential: credential, BuildCredentials: r.credentials, Replaces: step.Replaced, Resume: readReplaceCheckpoint(step.Checkpoint).Apply, Progress: func(string, string) {}}
	if fenceErr := preparePoolEffects(ctx, r.executors[action.Provider], job); fenceErr != nil {
		return fenceErr
	}
	return cleaner.CleanupPoolWorkload(ctx, job, checkpoint, save)
}

type poolCleanupCheckpoint struct {
	InstanceID string            `json:"instance_id"`
	Labels     map[string]string `json:"labels"`
	RuntimeRef string            `json:"runtime_ref"`
}

// CleanupPoolWorkload removes only owned compute. Server Runtime.Remove keeps
// volumes (v=0); no catalog data-deletion authorization is inferred here.
func (e *OwnedTarget) CleanupPoolWorkload(ctx context.Context, job *Job, raw []byte, save func([]byte) error) error {
	if e.Cloud != connections.Server {
		return errors.New("pool cleanup requires a server runtime")
	}
	ctx = infrastructure.WithPrincipal(ctx, principal(job))
	key := e.Register(job.Deployment.OrgID, job.Connection.ID)
	var cp poolCleanupCheckpoint
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cp); err != nil {
			return err
		}
	}
	if cp.InstanceID == "" {
		var state ownedState
		if len(job.Resume) == 0 {
			return errors.New("the interrupted operation has no runtime checkpoint")
		}
		if err := json.Unmarshal(job.Resume, &state); err != nil {
			return err
		}
		if state.EffectStage == "building" {
			// Preparation drained every accepted participant. This durable stage was
			// saved before building and never advanced to a Ctrl/runtime mutation.
			if state.RuntimeRef != "" {
				if err := validateRuntimeRef(job.Deployment.OrgID, job.Deployment.EnvironmentID, job.Action.Name, state.RuntimeRef); err != nil {
					return err
				}
				return e.deleteRuntime(ctx, job.Deployment.OrgID, job.Deployment.EnvironmentID, job.Action.Name, state.RuntimeRef)
			}
			return nil
		}
		inst, _, err := currentInstance(ctx, e.Ctrl, job, key)
		if err != nil {
			return err
		}
		if inst == nil {
			// An unknown ID cannot prove Docker absence. Even a control-plane record
			// removed by an earlier failed cleanup does not authorize capacity release.
			return errors.New("the pending runtime identity is unavailable")
		}
		if state.RuntimeRef == "" || inst.Labels[owned.LabelRuntime] != state.RuntimeRef || inst.TenantID != job.Deployment.OrgID || inst.ProviderName != key || inst.Labels[owned.LabelProject] != job.Deployment.ProjectID || inst.Labels[owned.LabelEnvironment] != job.Deployment.EnvironmentID || inst.Labels[owned.LabelService] != job.Action.Name {
			return errors.New("pending runtime ownership differs from the accepted checkpoint")
		}
		cp = poolCleanupCheckpoint{InstanceID: inst.ID.String(), Labels: inst.Labels, RuntimeRef: state.RuntimeRef}
		data, err := json.Marshal(cp)
		if err != nil {
			return err
		}
		if saveErr := save(data); saveErr != nil {
			return saveErr
		}
	}
	if cp.Labels[owned.LabelOrg] != job.Deployment.OrgID || cp.Labels[owned.LabelProject] != job.Deployment.ProjectID || cp.Labels[owned.LabelEnvironment] != job.Deployment.EnvironmentID || cp.Labels[owned.LabelService] != job.Action.Name || cp.Labels[owned.LabelConnection] != job.Connection.ID || cp.RuntimeRef == "" || cp.Labels[owned.LabelRuntime] != cp.RuntimeRef {
		return errors.New("cleanup checkpoint ownership differs")
	}
	if err := validateRuntimeRef(job.Deployment.OrgID, job.Deployment.EnvironmentID, job.Action.Name, cp.RuntimeRef); err != nil {
		return err
	}
	iid, err := id.Parse(cp.InstanceID)
	if err != nil {
		return err
	}
	backend, err := e.registeredBackend(key)
	if err != nil {
		return err
	}
	// Always contact the exact runtime, including after its Ctrl Plane record
	// disappeared. This checks all owned previous-container names as well.
	pending, ok := backend.(interface {
		RemovePending(context.Context, id.ID, map[string]string) error
	})
	if !ok {
		return errors.New("runtime cannot prove exact pending ownership")
	}
	if removeErr := pending.RemovePending(ctx, iid, cp.Labels); removeErr != nil {
		return removeErr
	}
	observed, err := backend.Status(ctx, iid, cp.Labels)
	if err != nil {
		return err
	}
	if observed == nil || observed.Metadata["exists"] != "false" {
		return errors.New("pending compute absence is not verified")
	}
	job.Checkpoint = func([]byte) error { return nil }
	job.Resume = nil
	b := &planning.Binding{OrgID: job.Deployment.OrgID, ProjectID: job.Deployment.ProjectID, EnvironmentID: job.Deployment.EnvironmentID, Name: job.Action.Name, ConnectionID: job.Connection.ID, ExternalID: cp.InstanceID}
	if removeErr := e.Remove(ctx, job, b); removeErr != nil {
		return removeErr
	}
	// Remove also handles a surviving instance. This explicit retry handles a
	// crash after the instance record vanished but before Vault cleanup finished.
	return e.deleteRuntime(ctx, b.OrgID, b.EnvironmentID, b.Name, cp.RuntimeRef)
}
