package kubernetes

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/xraph/grove/driver"
)

var (
	ErrManagedCapacityDisabled = errors.New("managed workload capacity profile is disabled")
	ErrManagedReviewRequired   = errors.New("managed workload requires a fresh capacity review")
)

type ManagedRuntimeProfile string

const (
	ManagedProfileLegacy  ManagedRuntimeProfile = "legacy-unreserved"
	ManagedProfileJournal ManagedRuntimeProfile = "managed-workload-journal-v1"
)

// ManagedRuntimeIdentity selects persisted evidence. It grants no write rights.
type ManagedRuntimeIdentity struct {
	Org, Project, Environment, Service, Plan, Connection string
	Instance, RuntimeRef, RuntimeRevision                string
	Namespace, NamespaceUID, ClusterCA                   string
}

type ManagedLegacyGuard interface {
	CheckLegacyManagedWrite(context.Context, ManagedRuntimeIdentity) error
}

// WithClusterCapacity returns a store with the process's configured profile.
// Disabling new effects does not disable journal reads or outcome recovery.
func (s *ControllerStore) WithClusterCapacity(enabled bool) *ControllerStore {
	copyStore := *s
	copyStore.capacityEnabled = enabled
	return &copyStore
}

func runtimeIdentityForReceipt(r ControllerReceipt) ManagedRuntimeIdentity {
	return ManagedRuntimeIdentity{Org: r.OrgID, Project: r.ProjectID, Environment: r.EnvironmentID, Service: r.ServiceID, Plan: r.PlanID, Connection: r.ConnectionID, Instance: r.InstanceID, RuntimeRef: r.RuntimeRef, RuntimeRevision: r.RuntimeRevision, Namespace: r.Namespace, NamespaceUID: r.NamespaceUID, ClusterCA: r.ClusterCA}
}

func (i ManagedRuntimeIdentity) valid() bool {
	return i.Org != "" && i.Project != "" && i.Environment != "" && i.Service != "" && i.Plan != "" && i.Connection != "" && i.Instance != "" && i.RuntimeRevision != "" && strings.HasPrefix(i.RuntimeRef, i.Org+"/"+i.Environment+"/"+i.Service+"/runtime_")
}

// managedProfile checks all saved releases for the instance as well as the plan.
// Relabeling a journaled instance with a legacy plan or runtime reference cannot
// restore unfenced writes, even after its binding has been removed.
func managedProfile(ctx context.Context, tx driver.Tx, i ManagedRuntimeIdentity) (ManagedRuntimeProfile, error) {
	if !i.valid() {
		return "", ErrManagedAuthority
	}
	var required bool
	err := tx.QueryRow(ctx, `SELECT p.required_runtime_protocol<>''
 OR EXISTS(SELECT 1 FROM loomplane_managed_workload_authority a WHERE a.org_id=p.org_id AND (a.body->>'Instance'=$7 OR a.body->>'RuntimeRef'=$8))
 OR EXISTS(SELECT 1 FROM loomplane_workload_controllers c WHERE c.org_id=p.org_id AND (c.instance_id=$7 OR c.runtime_ref=$8) AND coalesce(c.body->>'release_id','')<>'')
 FROM loomplane_plans p JOIN loomplane_cloud_connections cn ON cn.org_id=p.org_id AND cn.id=$6
 WHERE cn.status='ready' AND cn.cloud='kubernetes' AND cn.details->>'namespace'=$9 AND cn.details->>'namespace_uid'=$10 AND cn.details->>'cluster_ca'=$11 AND p.org_id=$1 AND p.project_id=$2 AND p.environment_id=$3 AND p.id=$4
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(coalesce(p.result->'actions','[]')) a WHERE a->>'name'=$5 AND a->>'subject'='service' AND a->>'provider'='kubernetes'
 AND coalesce(nullif(a->>'connection_id',''),p.result->'connections'->>'kubernetes')=$6)
 FOR SHARE OF p,cn`, i.Org, i.Project, i.Environment, i.Plan, i.Service, i.Connection, i.Instance, i.RuntimeRef, i.Namespace, i.NamespaceUID, i.ClusterCA).Scan(&required)
	if err != nil {
		return "", errors.Join(ErrManagedAuthority, err)
	}
	if required {
		return ManagedProfileJournal, nil
	}
	return ManagedProfileLegacy, nil
}

func (s *ControllerStore) ManagedProfileFor(ctx context.Context, i ManagedRuntimeIdentity) (ManagedRuntimeProfile, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // no-op after commit
	if checkErr := lockManagedOrganization(ctx, tx, i.Org); checkErr != nil {
		return "", checkErr
	}
	profile, err := managedProfile(ctx, tx, i)
	if err != nil {
		return "", err
	}
	return profile, tx.Commit()
}

func (s *ControllerStore) checkLegacyManagedWrite(ctx context.Context, tx driver.Tx, i ManagedRuntimeIdentity) error {
	if s.capacityEnabled {
		return ErrManagedReviewRequired
	}
	if err := lockManagedOrganization(ctx, tx, i.Org); err != nil {
		return err
	}
	profile, err := managedProfile(ctx, tx, i)
	if err != nil {
		return err
	}
	if profile != ManagedProfileLegacy {
		return ErrManagedAuthority
	}
	var approved bool
	err = tx.QueryRow(ctx, `SELECT status='approved' FROM loomplane_plans WHERE org_id=$1 AND id=$2 FOR SHARE`, i.Org, i.Plan).Scan(&approved)
	if err != nil || !approved {
		return errors.Join(ErrManagedAuthority, err)
	}
	return nil
}

func (s *ControllerStore) CheckLegacyManagedWrite(ctx context.Context, i ManagedRuntimeIdentity) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // no-op after commit
	if checkErr := s.checkLegacyManagedWrite(ctx, tx, i); checkErr != nil {
		return checkErr
	}
	return tx.Commit()
}

// Legacy writes keep Plan01 behavior only while the saved identity remains
// unreserved and capacity is disabled. This check also covers client-go retries.
type managedLegacyTransport struct {
	next     http.RoundTripper
	guard    ManagedLegacyGuard
	identity ManagedRuntimeIdentity
}

func (t *managedLegacyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet || req.Method == http.MethodHead || req.Method == http.MethodPost && req.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
		return t.next.RoundTrip(req)
	}
	if t.guard == nil {
		return nil, ErrManagedAuthority
	}
	// Bind the request to the identity checked in storage. A legacy identity
	// cannot be used to write a different instance's journaled controller.
	scope := ManagedScope{Org: t.identity.Org, Project: t.identity.Project, Environment: t.identity.Environment, Instance: t.identity.Instance, Namespace: t.identity.Namespace}
	parser := managedTransport{scope: scope}
	if _, err := parser.request(req); err != nil {
		return nil, err
	}
	if err := t.guard.CheckLegacyManagedWrite(req.Context(), t.identity); err != nil {
		return nil, err
	}
	return t.next.RoundTrip(req)
}
