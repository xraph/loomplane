package planning

import (
	"context"
	"encoding/json"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/security"
)

// GuardManagedDemandEffect consumes only the persisted approval. Runtime callers
// supply an identity, never a principal or a substitute approved specification.
func (s *Service) GuardManagedDemandEffect(ctx context.Context, tx driver.Tx, scope kube.ManagedScope) error {
	st, ok := s.store.(*PgStore)
	if !ok || tx == nil || s.authz == nil {
		return ErrUnavailable
	}
	authority, ok := s.capacity.(clusterWorkloadAuthority)
	if !ok {
		return ErrUnavailable
	}
	if err := authority.CheckClusterWorkloadCapacity(ctx); err != nil {
		return err
	}
	var raw, actor []byte
	if err := tx.QueryRow(ctx, `SELECT i.body,i.actor FROM loomplane_cluster_workload_intents i JOIN loomplane_plans p ON p.org_id=i.org_id AND p.id=i.plan_id AND p.status='approved' AND p.revision=i.plan_revision WHERE i.org_id=$1 AND i.plan_id=$2 AND i.service=$3 AND loomplane_cluster_workload_scope_current(p,i.body) AND NOT EXISTS(SELECT 1 FROM loomplane_cluster_demand_settlements s WHERE s.org_id=i.org_id AND s.plan_id=i.plan_id AND s.service=i.service) FOR SHARE OF p,i`, scope.Org, scope.Plan, scope.Service).Scan(&raw, &actor); err != nil {
		return err
	}
	var d ClusterWorkloadDemand
	var principal security.Principal
	if json.Unmarshal(raw, &d) != nil || json.Unmarshal(actor, &principal) != nil || d.SchemaVersion != 1 || principal.OrgID != scope.Org || principal.UserID == "" || principal.ReadOnly || d.Input.ActionID != scope.Action || d.Input.ConnectionID != scope.Connection {
		return ErrConflict
	}
	if principal.KeyID != "" {
		if s.capacityPrincipal == nil {
			return ErrUnavailable
		}
		if err := s.capacityPrincipal.ValidatePrincipal(ctx, principal); err != nil {
			return err
		}
	}
	if err := s.authz.Require(ctx, principal, security.DeploymentApprove, d.Input.ProjectID); err != nil {
		return err
	}
	if d.Mode == "best_effort" {
		return nil
	}
	if d.Mode != "guaranteed" || d.Profile == nil {
		return ErrConflict
	}
	if n := d.Input.Native; n != nil {
		policies, ok := s.capacity.(interface {
			CurrentClusterScalingPolicyTx(context.Context, driver.Tx, string, string, string, string) (*cloudinfra.ClusterScalingPolicy, error)
		})
		if !ok {
			return ErrUnavailable
		}
		p, err := policies.CurrentClusterScalingPolicyTx(ctx, tx, scope.Org, d.Input.CloudID, d.Input.ResourceID, n.PoolID)
		if err != nil {
			return err
		}
		if p.ID != n.PolicyID || p.Revision != n.PolicyRevision || p.MaxNodes != n.MaximumNodes {
			return ErrConflict
		}
	}
	return cloudinfra.NewPgStore(st.db).ValidateClusterWorkloadEffect(ctx, tx, d.Input, *d.Profile)
}
