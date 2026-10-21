package deployments

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/planning"
)

func poolAction(a planning.Action) bool {
	return a.Service != nil && a.Service.CloudPlacement != nil && a.Service.CloudPlacement.PoolID != ""
}
func (r *Runner) beginPoolExecution(ctx context.Context, plan *planning.Plan, a planning.Action) (*cloudinfra.PoolExecutionLock, error) {
	if !poolAction(a) {
		return nil, nil
	}
	st, ok := r.store.(*PgStore)
	if !ok {
		return nil, errors.New("pooled execution requires durable fencing")
	}
	return cloudinfra.NewPgStore(st.db).BeginPoolExecution(ctx, plan.OrgID, plan.ID, a.Name)
}

func poolEffectContext(ctx context.Context) context.Context {
	scope, ok := cloudinfra.ExecutionFrom(ctx)
	if !ok {
		return ctx
	}
	return server.WithEffectFence(ctx, server.EffectFence{Operation: scope.Org + "/" + scope.Plan + "/" + scope.Service, Generation: scope.Generation})
}

type poolEffectPreparer interface {
	PreparePoolEffects(context.Context, *Job) error
}

func preparePoolEffects(ctx context.Context, executor Executor, job *Job) error {
	preparer, ok := executor.(poolEffectPreparer)
	if !ok {
		return errors.New("runtime cannot establish a physical effect fence")
	}
	return preparer.PreparePoolEffects(ctx, job)
}
func (e *OwnedTarget) PreparePoolEffects(ctx context.Context, job *Job) error {
	fence, ok := server.EffectFenceFrom(ctx)
	if !ok {
		return errors.New("pooled effect generation is missing")
	}
	if e.Backend != nil {
		backend, err := e.registeredBackend(e.Register(job.Deployment.OrgID, job.Connection.ID))
		if err != nil {
			return err
		}
		if f, ok := backend.(interface {
			ArmEffectFence(context.Context, server.EffectFence) error
		}); ok {
			return f.ArmEffectFence(ctx, fence)
		}
		return errors.New("runtime cannot establish a physical effect fence")
	}
	_, labels := ownedSpec(job, "", ownedState{})
	client, err := e.serverSession(job.Deployment.OrgID, job.Connection.ID)(ctx, labels)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.ArmEffectFence(ctx, fence)
}

// preparePoolRemoval drains mutations on the recorded source before exact
// removal. It does not acquire new placement authority or delete storage.
func preparePoolRemoval(ctx context.Context, executor Executor, job *Job, bound *planning.Binding) (context.Context, error) {
	if !poolBinding(bound) {
		return ctx, nil
	}
	var cfg planning.ServiceConfig
	if err := json.Unmarshal(bound.Config, &cfg); err != nil {
		return ctx, err
	}
	removal := *job
	removal.Action.Service = &cfg
	if _, ok := server.EffectFenceFrom(ctx); !ok {
		ctx = server.WithEffectFence(ctx, server.EffectFence{Operation: job.Deployment.OrgID + "/cleanup/" + job.Deployment.ID + "/" + bound.ExternalID, Generation: 1})
	}
	return ctx, preparePoolEffects(ctx, executor, &removal)
}
