package planning

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/xraph/grove/driver"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
)

func (s *ManagedCleanupStore) guard(ctx context.Context, tx driver.Tx, target ManagedCleanupTarget, revision int64) error {
	scope := target.Scope
	raw, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT true FROM loomplane_managed_workload_authority a
 JOIN loomplane_cluster_workload_intents i ON i.org_id=a.org_id AND i.plan_id=a.body->>'Plan' AND i.service=a.body->>'Service'
 JOIN loomplane_cloud_targets t ON t.org_id=a.org_id AND t.provider='kubernetes' AND t.runtime='managed' AND t.id=i.body->'input'->>'target_id' AND t.project_id=a.body->>'Project' AND t.cloud_id=i.body->'input'->>'cloud_id' AND t.managed_resource_id=i.body->'input'->>'resource_id' AND t.connection_id=a.body->>'Connection'
 JOIN loomplane_cloud_profiles c ON c.org_id=t.org_id AND c.id=t.cloud_id
 JOIN loomplane_cloud_project_grants g ON g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=t.project_id AND NOT g.revoked AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids))
 JOIN loomplane_managed_resources mr ON mr.org_id=t.org_id AND mr.cloud_id=t.cloud_id AND mr.id=t.managed_resource_id AND mr.deleted_at IS NULL AND mr.status NOT IN ('missing','identity_changed')
 JOIN loomplane_cloud_connections cn ON cn.org_id=t.org_id AND cn.id=t.connection_id AND cn.cloud='kubernetes' AND cn.status='ready'
 JOIN loomplane_runtime_attachments rt ON rt.org_id=t.org_id AND rt.cloud_id=t.cloud_id AND rt.id=$6 AND rt.cluster_id=mr.id AND rt.project_id=t.project_id AND rt.runtime_connection_id=cn.id AND rt.deleted_at IS NULL
 WHERE a.org_id=$1 AND a.release_id=$2 AND a.epoch=$3 AND a.body=$4 AND a.revision=$5
 AND rt.revision=$7 AND rt.body->>'trust_revision'=$8 AND cn.generation=$9
 AND cn.details->>'namespace'=a.body->>'Namespace' AND cn.details->>'namespace_uid'=a.body->>'NamespaceUID' AND cn.details->>'cluster_ca'=a.body->>'ClusterCA'
 AND rt.body->>'namespace_uid'=a.body->>'NamespaceUID' AND rt.body->>'trust_fingerprint'=a.body->>'ClusterCA'
 FOR UPDATE OF a FOR SHARE OF i,t,c,g,mr,cn,rt`, scope.Org, scope.Release, scope.Epoch, raw, revision, target.AttachmentID, target.AttachmentRevision, target.TrustRevision, target.ConnectionGeneration).Scan(&valid)
	if err != nil || !valid {
		return errors.Join(kube.ErrManagedAuthority, err)
	}
	// Cleanup retries remain separate unresolved records. Only unresolved
	// application writes prevent further exact cleanup issuance.
	err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances WHERE org_id=$1 AND release_id=$2 AND cleanup_grant_id IS NULL AND outcome IS NULL)`, scope.Org, scope.Release).Scan(&valid)
	if err != nil || !valid {
		return errors.Join(kube.ErrManagedAuthority, err)
	}
	return nil
}

// Journal returns a restricted adapter for the existing pre-send transport.
// Creating the adapter grants nothing; Issue always loads current authority.
func (s *ManagedCleanupStore) Journal(org, id string) kube.ManagedJournal {
	return &managedCleanupJournal{store: s, org: org, id: id}
}

type managedCleanupJournal struct {
	store   *ManagedCleanupStore
	org, id string
}

func (j *managedCleanupJournal) Issue(ctx context.Context, scope kube.ManagedScope, request kube.ManagedRequest) (id string, err error) {
	if scope.Org != j.org {
		return "", kube.ErrManagedAuthority
	}
	tx, err := j.store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, j.org); checkErr != nil {
		return "", checkErr
	}
	grant, err := j.store.grant(ctx, tx, j.org, j.id)
	if err != nil {
		return "", err
	}
	if grant.RevokedAt != nil || grant.Target.Scope != scope {
		return "", kube.ErrManagedAuthority
	}
	if _, err = j.store.loadTarget(ctx, tx, grant.Selection); err != nil {
		return "", err
	}
	if checkErr := j.store.guard(ctx, tx, grant.Target, grant.AuthorityRevision); checkErr != nil {
		return "", checkErr
	}
	if checkErr := guardCleanupRequest(ctx, tx, grant.Target, request); checkErr != nil {
		return "", checkErr
	}
	issuance, err := typeid.Generate("issuance")
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return "", err
	}
	req, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	// An unresolved cleanup request permits only an exact retry, not progress
	// to a different object. Every retry remains independently unresolved.
	var retryOnly bool
	err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances WHERE org_id=$1 AND release_id=$2 AND outcome IS NULL AND (cleanup_grant_id IS NULL OR request<>$3::jsonb))`, scope.Org, scope.Release, req).Scan(&retryOnly)
	if err != nil || !retryOnly {
		return "", errors.Join(kube.ErrManagedAuthority, err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_managed_workload_issuances(id,org_id,release_id,epoch,scope,request,cleanup_grant_id,cleanup_grant_revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, issuance.String(), scope.Org, scope.Release, scope.Epoch, raw, req, grant.ID, grant.Revision)
	if err != nil {
		return "", err
	}
	return issuance.String(), tx.Commit()
}

func guardCleanupRequest(ctx context.Context, tx driver.Tx, target ManagedCleanupTarget, request kube.ManagedRequest) error {
	if request.Method != http.MethodDelete || request.Namespace != target.Scope.Namespace || request.Name == "" || request.UID == "" {
		return kube.ErrManagedAuthority
	}
	if target.Kind == ManagedCleanupRetireDependents {
		return ErrManagedCleanupUnavailable
	}
	if target.Kind != ManagedCleanupRemoveRelease {
		return kube.ErrManagedAuthority
	}
	allowed := map[string]string{"secrets": "", "services": "", "serviceaccounts": "", "deployments": "apps", "horizontalpodautoscalers": "autoscaling", "ingresses": "networking.k8s.io", "roles": "rbac.authorization.k8s.io", "rolebindings": "rbac.authorization.k8s.io"}
	group, ok := allowed[request.Resource]
	validVersion := request.Version == "v1" || request.Resource == "horizontalpodautoscalers" && request.Version == "v2"
	if !ok || group != request.Group || !validVersion {
		return kube.ErrManagedAuthority
	}
	var accepted *ManagedCleanupOutcome
	for i := range target.Accepted {
		candidate := &target.Accepted[i]
		r := candidate.Request
		if candidate.Outcome.State == "accepted" && (r.Method == http.MethodPost || r.Method == http.MethodPut) && r.Group == request.Group && r.Version == request.Version && r.Resource == request.Resource && r.Namespace == request.Namespace && r.Name == request.Name && candidate.Outcome.UID == request.UID {
			accepted = candidate
			break
		}
	}
	if accepted == nil {
		return kube.ErrManagedAuthority
	}
	// Require the original safe outcome, not a supplied UID or a cleanup receipt.
	rawRequest, err := json.Marshal(accepted.Request)
	if err != nil {
		return err
	}
	rawOutcome, err := json.Marshal(accepted.Outcome)
	if err != nil {
		return err
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT true FROM loomplane_managed_workload_issuances WHERE id=$1 AND org_id=$2 AND release_id=$3 AND epoch=$4 AND request=$5 AND outcome=$6 AND cleanup_grant_id IS NULL FOR SHARE`, accepted.ID, target.Scope.Org, target.Scope.Release, accepted.Epoch, rawRequest, rawOutcome).Scan(&valid)
	if err != nil || !valid {
		return errors.Join(kube.ErrManagedAuthority, err)
	}
	// Binding updates also occur outside the organization gate. Lock existing
	// physical and service bindings before checking newer release ownership.
	if _, err = tx.Exec(ctx, `SELECT id FROM loomplane_bindings WHERE org_id=$1 AND ((connection_id=$2 AND external_id=$3) OR (environment_id=$4 AND subject='service' AND name=$5)) FOR SHARE`, target.Scope.Org, target.Scope.Connection, target.Scope.Instance, target.Scope.Environment, target.Scope.Service); err != nil {
		return err
	}
	// A newer release can share names and UIDs. Any such evidence makes
	// whole-object deletion unavailable, including after binding removal.
	err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances w WHERE w.org_id=$1 AND w.release_id<>$2 AND w.scope->>'Connection'=$3 AND w.scope->>'NamespaceUID'=$4 AND w.request->>'Resource'=$5 AND w.request->>'Name'=$6 AND w.outcome->>'State'='accepted' AND w.outcome->>'UID'=$7)
 AND NOT EXISTS(SELECT 1 FROM loomplane_managed_workload_authority a WHERE a.org_id=$1 AND a.release_id<>$2 AND a.body->>'Connection'=$3 AND a.body->>'Instance'=$8)
 AND NOT EXISTS(SELECT 1 FROM loomplane_bindings b WHERE b.org_id=$1 AND b.connection_id=$3 AND b.external_id=$8 AND b.plan_id<>$9)
 AND NOT EXISTS(SELECT 1 FROM loomplane_workload_controllers c WHERE c.org_id=$1 AND c.connection_id=$3 AND c.instance_id=$8 AND coalesce(c.body->>'release_id','')<>$2 AND c.body->>'deployment_uid'=$7)`, target.Scope.Org, target.Scope.Release, target.Scope.Connection, target.Scope.NamespaceUID, request.Resource, request.Name, request.UID, target.Scope.Instance, target.Scope.Plan).Scan(&valid)
	if err != nil || !valid {
		return errors.Join(kube.ErrManagedAuthority, err)
	}
	return nil
}

func (j *managedCleanupJournal) Resolve(ctx context.Context, id string, scope kube.ManagedScope, outcome kube.ManagedOutcome) error {
	if scope.Org != j.org {
		return kube.ErrManagedAuthority
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	var valid bool
	err = j.store.db.QueryRow(ctx, `SELECT true FROM loomplane_managed_workload_issuances WHERE org_id=$1 AND id=$2 AND cleanup_grant_id=$3 AND scope=$4`, j.org, id, j.id, raw).Scan(&valid)
	if err != nil || !valid {
		return errors.Join(kube.ErrManagedAuthority, err)
	}
	return j.store.journal.Resolve(ctx, id, scope, outcome)
}
