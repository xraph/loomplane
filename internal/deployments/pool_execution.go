package deployments

import (
	"context"
	"errors"

	"github.com/xraph/loomplane/internal/cloudinfra"
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
