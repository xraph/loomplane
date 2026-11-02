package kubernetes

import (
	"context"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

// LifecycleJobUnissued distinguishes a crash before transmission from an unknown
// response. The next POST still needs ordinary current journal admission.
func (s *ControllerStore) LifecycleJobUnissued(ctx context.Context, scope ManagedScope) (bool, error) {
	if !lifecycleManagedScopeValid(scope) {
		return false, ErrManagedAuthority
	}
	var absent bool
	err := s.db.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances WHERE org_id=$1 AND scope->>'Connection'=$2 AND scope->>'NamespaceUID'=$3 AND request->>'Resource'='jobs' AND request->>'Name'=$4 AND request->>'Method'='POST' AND (outcome IS NULL OR outcome->>'State'<>'rejected'))`, scope.Org, scope.Connection, scope.NamespaceUID, scope.LifecycleJobName).Scan(&absent)
	return absent, err
}
func (r *LifecycleRuntime) recoverUnissuedJob(ctx context.Context, c *Client, run owned.LifecycleJobRun, attempt int) (*owned.LifecycleJobReceipt, error) {
	history, ok := c.Config.ManagedJournal.(interface {
		LifecycleJobUnissued(context.Context, ManagedScope) (bool, error)
	})
	if !ok {
		return nil, owned.ErrLifecyclePending
	}
	unissued, err := history.LifecycleJobUnissued(ctx, c.Config.ManagedScope)
	if err != nil {
		return nil, err
	}
	if !unissued {
		return nil, owned.ErrLifecyclePending
	}
	return r.CreateLifecycleJob(ctx, run, attempt)
}
