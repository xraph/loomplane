package planning

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/grove/driver"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
)

// cleanupCoowners reads only the immutable action's targets. Each shared owner
// needs its own current grant before you can remove a shared object.
func (s *ManagedCleanupStore) cleanupCoowners(ctx context.Context, tx driver.Tx, grant *ManagedCleanupGrant) ([]byte, error) {
	sel := grant.Selection
	owners := []ManagedCleanupTarget{grant.Target}
	if sel.FailedReviewID != "" {
		return json.Marshal(owners)
	}
	var raw []byte
	var operation string
	err := tx.QueryRow(ctx, `SELECT i.body,a->>'op' FROM loomplane_managed_cleanup_intents i
 JOIN loomplane_plans p ON p.org_id=i.org_id AND p.id=i.plan_id
 CROSS JOIN LATERAL jsonb_array_elements(p.result->'actions') a
 WHERE i.org_id=$1 AND i.plan_id=$2 AND i.action_id=$3 AND a->>'id'=i.action_id`, sel.Org, sel.Plan, sel.Action).Scan(&raw, &operation)
	if err != nil {
		return nil, err
	}
	if operation != string(Delete) && operation != string(Replace) {
		return json.Marshal(owners)
	}
	var targets []ManagedCleanupTarget
	if json.Unmarshal(raw, &targets) != nil {
		return nil, kube.ErrManagedAuthority
	}
	for index, target := range targets {
		if index == sel.TargetIndex || !sameCleanupPhysicalScope(target.Scope, grant.Target.Scope) {
			continue
		}
		if target.Kind != ManagedCleanupRemoveRelease {
			return nil, kube.ErrManagedAuthority
		}
		peerSelection := sel
		peerSelection.TargetIndex = index
		if _, err = s.loadTarget(ctx, tx, peerSelection); err != nil {
			return nil, err
		}
		var id string
		err = tx.QueryRow(ctx, `SELECT g.id FROM loomplane_managed_cleanup_grants g
 JOIN loomplane_managed_workload_authority a ON a.org_id=g.org_id AND a.release_id=g.release_id AND a.revoked_at IS NOT NULL
 WHERE g.org_id=$1 AND g.plan_id=$2 AND g.action_id=$3 AND g.deployment_id=$4 AND g.target_index=$5 AND g.revoked_at IS NULL AND g.failed_review_id IS NULL`, sel.Org, sel.Plan, sel.Action, sel.Deployment, index).Scan(&id)
		if err != nil {
			return nil, errors.Join(kube.ErrManagedAuthority, err)
		}
		peer, readErr := s.grant(ctx, tx, sel.Org, id)
		if readErr != nil {
			return nil, readErr
		}
		if checkErr := s.guard(ctx, tx, peer.Target, peer.AuthorityRevision); checkErr != nil {
			return nil, checkErr
		}
		owners = append(owners, target)
	}
	return json.Marshal(owners)
}

func sameCleanupPhysicalScope(a, b kube.ManagedScope) bool {
	return a.Org == b.Org && a.Project == b.Project && a.Environment == b.Environment && a.Service == b.Service && a.Connection == b.Connection && a.Instance == b.Instance && a.Namespace == b.Namespace && a.NamespaceUID == b.NamespaceUID && a.ClusterCA == b.ClusterCA
}
