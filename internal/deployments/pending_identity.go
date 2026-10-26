package deployments

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/planning"
)

// PendingCloudIdentity is cleanup evidence, never a ready deployment binding.
// Proof retains the provider checkpoint that pins the original operation scope.
type PendingCloudIdentity struct {
	ExternalID     string                     `json:"external_id"`
	PhysicalID     string                     `json:"physical_id"`
	OwnedResources map[string]json.RawMessage `json:"owned_resources,omitempty"`
	Proof          json.RawMessage            `json:"proof"`
}

type pendingCloudResolver interface {
	ResolvePendingCloudResource(context.Context, *Job) (*PendingCloudIdentity, error)
	RemovePendingCloudResource(context.Context, *Job, *PendingCloudIdentity) error
}

func (r *Runner) resolvePendingIdentity(ctx context.Context, job *Job, rc *replaceCheckpoint, save func([]byte) error) (*planning.Binding, error) {
	exec, ok := r.executors[job.Action.Provider]
	if !ok {
		return nil, ErrNoExecutor
	}
	rich, richOK := exec.(pendingCloudResolver)
	legacy, legacyOK := exec.(PendingCloudResource)
	if !richOK && !legacyOK {
		return nil, errors.New("original executor cannot resolve its unfinished resource")
	}
	job.Resume = rc.Apply
	job.Checkpoint = func(raw []byte) error {
		rc.Apply = append([]byte(nil), raw...)
		return save(rc.encode())
	}
	if richOK {
		if rc.PendingIdentity == nil {
			identity, err := rich.ResolvePendingCloudResource(ctx, job)
			if err != nil {
				return nil, err
			}
			if identity == nil || !json.Valid(identity.Proof) || identity.ExternalID != "" && identity.PhysicalID == "" {
				return nil, errors.New("pending resource lacks original identity evidence")
			}
			if rc.PendingResource != "" && rc.PendingResource != identity.ExternalID {
				return nil, errors.New("pending resource identity changed")
			}
			rc.PendingIdentity, rc.PendingResource = identity, identity.ExternalID
			if err := save(rc.encode()); err != nil {
				return nil, err
			}
		}
		if rc.PendingIdentity.ExternalID != rc.PendingResource {
			return nil, errors.New("pending resource identity changed")
		}
		job.pendingIdentity = rc.PendingIdentity
	} else {
		if rc.PendingIdentity != nil {
			return nil, errors.New("pending resource ownership requires its original cleanup executor")
		}
		if rc.PendingResource == "" {
			id, err := legacy.PendingCloudResource(ctx, job)
			if err != nil {
				return nil, err
			}
			rc.PendingResource = id
			if err := save(rc.encode()); err != nil {
				return nil, err
			}
		}
	}
	a := job.Action
	var config any = a.Service
	if a.Resource != nil {
		config = a.Resource
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	bound := &planning.Binding{OrgID: job.Deployment.OrgID, ProjectID: job.Deployment.ProjectID, EnvironmentID: job.Deployment.EnvironmentID, Subject: a.Subject, Name: a.Name, Provider: a.Provider, Region: a.Region, Engine: a.Engine, ConnectionID: a.ConnectionID, ExternalID: rc.PendingResource, Config: raw, PlanID: job.Plan.ID}
	if rc.PendingIdentity != nil {
		bound.PhysicalID, bound.OwnedResources = rc.PendingIdentity.PhysicalID, rc.PendingIdentity.OwnedResources
	}
	return bound, nil
}

func (r *Runner) pendingCheckpoint(ctx context.Context, job *Job, step *Step, heartbeat func([]byte) error) func([]byte) error {
	return func(raw []byte) error {
		if err := r.authorizeCloudRemoval(ctx, job); err != nil {
			return err
		}
		if heartbeat == nil {
			return errors.New("pending cleanup requires a durable activity heartbeat")
		}
		if err := heartbeat(raw); err != nil {
			return err
		}
		step.Checkpoint = append([]byte(nil), raw...)
		return r.store.UpdateStep(ctx, job.Deployment.OrgID, job.Deployment.ID, *step)
	}
}

func removePendingOrBound(ctx context.Context, exec Executor, job *Job, bound *planning.Binding) error {
	if job.pendingIdentity == nil {
		return exec.Remove(ctx, job, bound)
	}
	pending, ok := exec.(pendingCloudResolver)
	if !ok || bound.ExternalID != job.pendingIdentity.ExternalID || bound.PhysicalID != job.pendingIdentity.PhysicalID {
		return errors.New("pending cleanup executor or identity changed")
	}
	return pending.RemovePendingCloudResource(ctx, job, job.pendingIdentity)
}

func (r *Runner) canResolvePendingAction(a planning.Action) bool {
	if a.Op == planning.Replace {
		return true
	}
	_, ok := r.executors[a.Provider].(pendingCloudResolver)
	return a.Op == planning.Create && ok
}
