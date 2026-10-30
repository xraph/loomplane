package planning

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/cloudprofiles"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
)

// ManagedCleanupDescendantEvidence is safe identity from trusted runtime reads.
// It is not an issuance token. The runtime must read the owner chain itself,
// bind it to the reviewed release and recheck mutable ownership with both UID
// and resourceVersion delete preconditions. Labels alone never prove ownership.
type ManagedCleanupDescendantEvidence struct {
	Scope                                      kube.ManagedScope
	Request                                    kube.ManagedRequest
	ObservedAt                                 time.Time
	DeploymentUID, ReplicaSetUID, PodUID       string
	ResourceVersion, ReplicaSetResourceVersion string
	Release                                    string
	NewerController                            *kube.ControllerReceipt
}

// ManagedCleanupDescendantReader is the later runtime integration boundary.
// Implementations must read the exact cluster/namespace and return current
// owner/release evidence, including the newer receipt for shared controllers.
// No implementation is installed here, so descendant issuance stays unavailable.
type ManagedCleanupDescendantReader interface {
	ReadCleanupDescendant(context.Context, ManagedCleanupTarget, kube.ManagedRequest) (ManagedCleanupDescendantEvidence, error)
}

// InspectDescendant checks a runtime reader's evidence without granting writes.
// Even valid evidence cannot enable descendant issuance until the cleanup
// transport enforces its resourceVersion preconditions and execution protocol.
func (s *ManagedCleanupStore) InspectDescendant(ctx context.Context, org, grantID string, request kube.ManagedRequest, reader ManagedCleanupDescendantReader) (_ ManagedCleanupDescendantEvidence, err error) {
	var empty ManagedCleanupDescendantEvidence
	if reader == nil {
		return empty, ErrManagedCleanupUnavailable
	}
	// Runtime reads happen before the organization lock. The transaction below
	// rechecks the immutable target and current grant after that read returns.
	target, err := s.inspectTarget(ctx, org, grantID)
	if err != nil {
		return empty, err
	}
	evidence, err := reader.ReadCleanupDescendant(ctx, target, request)
	if err != nil {
		return empty, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, org); checkErr != nil {
		return empty, checkErr
	}
	grant, err := s.grant(ctx, tx, org, grantID)
	if err != nil {
		return empty, err
	}
	if grant.RevokedAt != nil || grant.Target.Kind != ManagedCleanupRetireDependents {
		return empty, kube.ErrManagedAuthority
	}
	if _, err = s.loadTarget(ctx, tx, grant.Selection); err != nil {
		return empty, err
	}
	if checkErr := s.guard(ctx, tx, grant.Target, grant.AuthorityRevision); checkErr != nil {
		return empty, checkErr
	}
	if !validCleanupDescendant(grant.Target, request, evidence, time.Now()) {
		return empty, kube.ErrManagedAuthority
	}
	receipt := evidence.NewerController
	raw, err := json.Marshal(receipt)
	if err != nil {
		return empty, err
	}
	var current bool
	err = tx.QueryRow(ctx, `SELECT true FROM loomplane_workload_controllers c
 JOIN loomplane_managed_workload_authority a ON a.org_id=c.org_id AND a.release_id=c.body->>'release_id' AND a.revoked_at IS NULL
 JOIN loomplane_bindings b ON b.org_id=c.org_id AND b.connection_id=c.connection_id AND b.external_id=c.instance_id AND b.plan_id=c.body->>'plan_id'
 WHERE c.org_id=$1 AND c.connection_id=$2 AND c.instance_id=$3 AND c.runtime_ref=$4 AND c.body=$5
 AND a.body->>'RuntimeRevision'=c.body->>'runtime_revision'
 AND EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances w WHERE w.org_id=a.org_id AND w.release_id=a.release_id AND w.epoch=a.epoch AND w.cleanup_grant_id IS NULL AND w.request->>'Resource'='deployments' AND w.outcome->>'State'='accepted' AND w.outcome->>'UID'=c.body->>'deployment_uid' AND w.outcome->>'Generation'=c.body->>'deployment_generation')
 FOR SHARE OF c,a,b`, org, receipt.ConnectionID, receipt.InstanceID, receipt.RuntimeRef, raw).Scan(&current)
	if err != nil || !current {
		return empty, errors.Join(kube.ErrManagedAuthority, err)
	}
	return evidence, tx.Commit()
}

func validCleanupDescendant(target ManagedCleanupTarget, request kube.ManagedRequest, e ManagedCleanupDescendantEvidence, now time.Time) bool {
	if e.Scope != target.Scope || e.Request != request || e.Release != target.Scope.Release || e.ResourceVersion == "" || e.ReplicaSetResourceVersion == "" || e.ReplicaSetUID == "" || e.DeploymentUID == "" || e.ObservedAt.IsZero() || e.ObservedAt.After(now) || now.Sub(e.ObservedAt) > 2*time.Minute || request.Method != "DELETE" || request.Namespace != target.Scope.Namespace || request.Name == "" || request.UID == "" || request.Version != "v1" {
		return false
	}
	validResource := request.Resource == "pods" && request.Group == "" && request.UID == e.PodUID || request.Resource == "replicasets" && request.Group == "apps" && request.UID == e.ReplicaSetUID && e.PodUID == ""
	if !validResource {
		return false
	}
	found := false
	var oldGeneration int64
	for _, accepted := range target.Accepted {
		if accepted.Request.Resource == "deployments" && accepted.Request.Group == "apps" && (accepted.Request.Method == "POST" || accepted.Request.Method == "PUT") && accepted.Outcome.State == "accepted" && accepted.Outcome.UID == e.DeploymentUID {
			found = true
			if accepted.Outcome.Generation > oldGeneration {
				oldGeneration = accepted.Outcome.Generation
			}
		}
	}
	r := e.NewerController
	s := target.Scope
	return found && r != nil && r.Valid() && r.DeploymentGeneration > oldGeneration && r.ReleaseID != "" && r.ReleaseID != s.Release && r.OrgID == s.Org && r.ProjectID == s.Project && r.EnvironmentID == s.Environment && r.ConnectionID == s.Connection && r.InstanceID == s.Instance && r.Namespace == s.Namespace && r.NamespaceUID == s.NamespaceUID && r.ClusterCA == s.ClusterCA && r.ServiceID == s.Service && r.DeploymentUID == e.DeploymentUID
}

// inspectTarget authorizes the read before any runtime client is invoked.
func (s *ManagedCleanupStore) inspectTarget(ctx context.Context, org, id string) (_ ManagedCleanupTarget, err error) {
	var empty ManagedCleanupTarget
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, org); checkErr != nil {
		return empty, checkErr
	}
	grant, err := s.grant(ctx, tx, org, id)
	if err != nil {
		return empty, err
	}
	if grant.RevokedAt != nil || grant.Target.Kind != ManagedCleanupRetireDependents {
		return empty, kube.ErrManagedAuthority
	}
	if _, err = s.loadTarget(ctx, tx, grant.Selection); err != nil {
		return empty, err
	}
	if checkErr := s.guard(ctx, tx, grant.Target, grant.AuthorityRevision); checkErr != nil {
		return empty, checkErr
	}
	return grant.Target, tx.Commit()
}
