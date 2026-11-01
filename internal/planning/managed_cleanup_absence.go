package planning

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/xraph/grove/driver"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
)

var ErrCleanupObservationIncomplete = errors.New("managed cleanup needs a newer complete observation of the reviewed workload")

type cleanupObservation struct {
	session    *cleanupSession
	identities cloudinfra.ClusterCleanupInventory
	tombstone  cloudinfra.ClusterCleanupTombstone
}

// SettleManagedCleanup only settles capacity after current authorized namespace
// reads prove release-scoped absence. Recovery of unknown sends stays separate.
func (s *ManagedCleanupStore) SettleManagedCleanup(ctx context.Context, selection ManagedCleanupSelection, creds *connections.Service) error {
	sessions, err := s.cleanupSessions(ctx, selection, creds)
	if err != nil {
		return err
	}
	started, err := s.startCleanupObservation(ctx, sessions)
	if err != nil {
		return err
	}
	observations := make([]cleanupObservation, 0, len(sessions))
	for _, session := range sessions {
		observation, readErr := session.observeAbsence(ctx)
		if readErr != nil {
			return readErr
		}
		observation.tombstone.ObservationStartedAt = started
		observations = append(observations, *observation)
	}
	return s.commitCleanupObservations(ctx, observations)
}

func (s *ManagedCleanupStore) startCleanupObservation(ctx context.Context, sessions []*cleanupSession) (_ time.Time, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, sessions[0].grant.Target.Scope.Org); checkErr != nil {
		return time.Time{}, checkErr
	}
	var started time.Time
	if readErr := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&started); readErr != nil {
		return time.Time{}, readErr
	}
	for _, session := range sessions {
		if checkErr := session.guardObservation(ctx, tx, started); checkErr != nil {
			return time.Time{}, checkErr
		}
	}
	return started, tx.Commit()
}

func (s *cleanupSession) guardObservation(ctx context.Context, tx driver.Tx, started time.Time) error {
	g, err := s.store.grant(ctx, tx, s.grant.Target.Scope.Org, s.grant.ID)
	if err != nil {
		return err
	}
	if g.RevokedAt != nil || g.Revision != s.grant.Revision || g.AuthorityRevision != s.grant.AuthorityRevision || g.Target.Scope != s.grant.Target.Scope {
		return kube.ErrManagedAuthority
	}
	if _, checkErr := s.store.loadTarget(ctx, tx, g.Selection); checkErr != nil {
		return checkErr
	}
	if checkErr := s.store.guard(ctx, tx, g.Target, g.AuthorityRevision); checkErr != nil {
		return checkErr
	}
	coowners, err := s.store.cleanupCoowners(ctx, tx, g)
	if err != nil {
		return err
	}
	if g.Target.Kind != ManagedCleanupRetireDependents {
		// A release can have no accepted controller, but its instance can still
		// be claimed by another active grant, receipt or binding.
		if checkErr := guardCleanupOwners(ctx, tx, g.Target, kube.ManagedRequest{}, coowners); checkErr != nil {
			return checkErr
		}
		for _, a := range g.Target.Accepted {
			r := a.Request
			r.UID = a.Outcome.UID
			if checkErr := guardCleanupOwners(ctx, tx, g.Target, r, coowners); checkErr != nil {
				return checkErr
			}
		}
	}
	var clean bool
	err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances WHERE org_id=$1 AND release_id=$2 AND (outcome IS NULL OR issued_at>=$3 OR resolved_at>=$3))`, g.Target.Scope.Org, g.Target.Scope.Release, started).Scan(&clean)
	if err != nil {
		return err
	}
	if !clean {
		return ErrCleanupWritesUnresolved
	}
	return nil
}

func (s *cleanupSession) observeAbsence(ctx context.Context) (*cleanupObservation, error) {
	scope := s.grant.Target.Scope
	t := cloudinfra.ClusterCleanupTombstone{
		SchemaVersion: 1, GrantID: s.grant.ID, OrgID: scope.Org, ReleaseID: scope.Release, ProjectID: scope.Project, EnvironmentID: scope.Environment, Service: scope.Service, InstanceID: scope.Instance, ConnectionID: scope.Connection, ClusterCA: scope.ClusterCA, Namespace: scope.Namespace, NamespaceUID: scope.NamespaceUID,
		Shared: s.grant.Target.Kind == ManagedCleanupRetireDependents, ResourceVersions: map[string]string{}, OriginalGeneration: map[string]int64{},
	}
	ns, err := s.client.Kube.CoreV1().Namespaces().Get(ctx, scope.Namespace, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if string(ns.UID) != scope.NamespaceUID || ns.DeletionTimestamp != nil {
		return nil, kube.ErrManagedAuthority
	}
	sets, pods, err := s.nativeInventory(ctx)
	if err != nil {
		return nil, err
	}
	deps, err := s.client.Kube.AppsV1().Deployments(scope.Namespace).List(ctx, metav1.ListOptions{Limit: 5000})
	if err != nil {
		return nil, err
	}
	if deps.Continue != "" || deps.ResourceVersion == "" {
		return nil, ErrCleanupObservationIncomplete
	}
	t.ResourceVersions["deployments"], t.ResourceVersions["replicasets"], t.ResourceVersions["pods"] = deps.ResourceVersion, sets.ResourceVersion, pods.ResourceVersion
	in := cloudinfra.NormalizeCleanupInventory(kube.CapacityInventory{Namespaces: []corev1.Namespace{*ns}, Deployments: deps.Items, ReplicaSets: sets.Items, Pods: pods.Items, Complete: true})
	for _, a := range s.grant.Target.Accepted {
		r := a.Request
		if a.Outcome.State != "accepted" || r.Method != http.MethodPost && r.Method != http.MethodPut {
			continue
		}
		switch r.Resource {
		case "deployments":
			t.Deployments = append(t.Deployments, a.Outcome.UID)
			t.DeploymentName = r.Name
			t.OriginalGeneration[a.Outcome.UID] = max(t.OriginalGeneration[a.Outcome.UID], a.Outcome.Generation)
		case "horizontalpodautoscalers":
			h := cloudinfra.ClusterCleanupHPA{Name: r.Name, UID: a.Outcome.UID}
			if !slices.Contains(t.HPAs, h) {
				t.HPAs = append(t.HPAs, h)
			}
		default:
			if t.Shared {
				continue
			}
			obj, readErr := s.childObject(ctx, r)
			if apierrors.IsNotFound(readErr) {
				continue
			}
			if readErr != nil {
				return nil, readErr
			}
			if string(obj.GetUID()) == a.Outcome.UID {
				return nil, ErrCleanupConverging
			}
		}
	}
	captured, err := s.captured(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range captured {
		if e.Child.Resource == "pods" {
			t.Pods = append(t.Pods, e.Child.UID)
		} else {
			t.ReplicaSets = append(t.ReplicaSets, e.Child.UID)
		}
	}
	entries, err := s.store.journal.ManagedIssuances(ctx, scope.Org, scope.Release)
	if err != nil {
		return nil, err
	}
	for _, w := range entries {
		if w.Outcome == nil {
			return nil, ErrCleanupWritesUnresolved
		}
		if w.CleanupGrantID == "" || w.Request.Method != http.MethodDelete {
			continue
		}
		switch w.Request.Resource {
		case "pods":
			t.Pods = append(t.Pods, w.Request.UID)
		case "replicasets":
			t.ReplicaSets = append(t.ReplicaSets, w.Request.UID)
		}
	}
	slices.Sort(t.Pods)
	t.Pods = slices.Compact(t.Pods)
	slices.Sort(t.ReplicaSets)
	t.ReplicaSets = slices.Compact(t.ReplicaSets)
	slices.Sort(t.Deployments)
	t.Deployments = slices.Compact(t.Deployments)
	if len(t.HPAs) > 0 {
		if !s.hpa {
			return nil, kube.ErrCleanupPermissionsUnavailable
		}
		list, readErr := s.client.Kube.AutoscalingV2().HorizontalPodAutoscalers(scope.Namespace).List(ctx, metav1.ListOptions{Limit: 5000})
		if readErr != nil {
			return nil, readErr
		}
		if list.Continue != "" || list.ResourceVersion == "" {
			return nil, ErrCleanupObservationIncomplete
		}
		t.ResourceVersions["horizontalpodautoscalers"] = list.ResourceVersion
		seen := map[string]bool{}
		for _, h := range t.HPAs {
			if seen[h.Name] {
				continue
			}
			seen[h.Name] = true
			read := cloudinfra.ClusterCleanupHPAObservation{Name: h.Name, NamespaceUID: scope.NamespaceUID, Complete: true}
			for i := range list.Items {
				obj := &list.Items[i]
				if obj.Name != h.Name {
					continue
				}
				read.UID, read.Generation, read.Deleting = string(obj.UID), obj.Generation, obj.DeletionTimestamp != nil
				target := obj.Spec.ScaleTargetRef
				if target.Kind == "Deployment" && target.APIVersion == "apps/v1" {
					read.DeploymentName = target.Name
				}
			}
			in.HPAs = append(in.HPAs, read)
		}
	}
	_, cred, err := s.material(ctx)
	if err != nil {
		return nil, err
	}
	if cred.AccessRevision != s.capability || cred.CredentialVersion != s.credential {
		return nil, ErrCleanupReviewStale
	}
	return &cleanupObservation{session: s, identities: in, tombstone: t}, nil
}

func (s *ManagedCleanupStore) commitCleanupObservations(ctx context.Context, observations []cleanupObservation) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, observations[0].tombstone.OrgID); checkErr != nil {
		return checkErr
	}
	var now time.Time
	if readErr := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); readErr != nil {
		return readErr
	}
	for i := range observations {
		o := &observations[i]
		t := &o.tombstone
		if now.Sub(t.ObservationStartedAt) > cloudinfra.ClusterCapacityFreshness || now.Before(t.ObservationStartedAt) {
			return ErrCleanupObservationIncomplete
		}
		if checkErr := o.session.guardObservation(ctx, tx, t.ObservationStartedAt); checkErr != nil {
			return checkErr
		}
		var raw []byte
		err = tx.QueryRow(ctx, `SELECT a.cloud_id,a.cluster_id,r.body,coalesce((SELECT issued_revision FROM loomplane_cluster_capacity_inventory WHERE org_id=r.org_id AND resource_id=r.id),0)
 FROM loomplane_runtime_attachments a JOIN loomplane_managed_resources r ON r.org_id=a.org_id AND r.id=a.cluster_id AND r.cloud_id=a.cloud_id
 WHERE a.org_id=$1 AND a.id=$2 AND a.revision=$3 AND a.deleted_at IS NULL AND r.deleted_at IS NULL FOR SHARE OF a,r`, t.OrgID, o.session.grant.Target.AttachmentID, o.session.grant.Target.AttachmentRevision).Scan(&t.CloudID, &t.ResourceID, &raw, &t.InventoryBarrier)
		if err != nil {
			return err
		}
		var r cloudinfra.ManagedResource
		if json.Unmarshal(raw, &r) != nil || r.Observation == nil || r.Observation.CAHash != t.ClusterCA {
			return kube.ErrManagedAuthority
		}
		t.Ref = r.Observation.Ref
		qualified, proofErr := cloudinfra.QualifyCleanupInventory(ctx, tx, t, &o.identities)
		if proofErr != nil {
			return proofErr
		}
		if !qualified {
			return ErrCleanupObservationIncomplete
		}
		var activeDrift bool
		if readErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_managed_cleanup_current_drift WHERE org_id=$1 AND release_id=$2 AND present)`, t.OrgID, t.ReleaseID).Scan(&activeDrift); readErr != nil {
			return readErr
		}
		if activeDrift {
			return &cloudinfra.CapacityAdmissionError{Block: cloudinfra.CapacityBlock{Reason: "managed_cleanup_drift"}}
		}
		t.ObservedAt = now
		raw, err = json.Marshal(t)
		if err != nil {
			return err
		}
		if _, writeErr := tx.Exec(ctx, `INSERT INTO loomplane_managed_cleanup_tombstones(org_id,release_id,cloud_id,resource_id,grant_id,schema_version,observation_started_at,observed_at,inventory_barrier,body) VALUES($1,$2,$3,$4,$5,1,$6,$7,$8,$9) ON CONFLICT(org_id,release_id) DO NOTHING`, t.OrgID, t.ReleaseID, t.CloudID, t.ResourceID, t.GrantID, t.ObservationStartedAt, t.ObservedAt, t.InventoryBarrier, raw); writeErr != nil {
			return writeErr
		}
	}
	pending := false
	for _, o := range observations {
		t := o.tombstone
		scope := o.session.grant.Target.Scope
		// A hold can cover multiple runtime references across retries. Every one
		// must have permanently revoked application authority and a tombstone.
		if _, writeErr := tx.Exec(ctx, `UPDATE loomplane_cluster_workload_holds h SET released=true
 WHERE h.org_id=$1 AND h.cloud_id=$2 AND h.resource_id=$3 AND h.plan_id=$4 AND h.service=$5 AND NOT h.released
 AND h.body->'input'->>'connection_id'=$6 AND h.body->'input'->>'attachment_id'=$7
 AND h.body->'input'->>'project_id'=$8 AND h.body->'input'->>'environment_id'=$9
 AND EXISTS(SELECT 1 FROM loomplane_cluster_workload_intents i WHERE i.org_id=h.org_id AND i.plan_id=h.plan_id AND i.service=h.service AND i.body->>'mode'='guaranteed' AND i.body->'input'->>'id'=h.id)
 AND NOT EXISTS(SELECT 1 FROM loomplane_managed_workload_authority a WHERE a.org_id=h.org_id AND a.body->>'Plan'=h.plan_id AND a.body->>'Service'=h.service AND
 (a.revoked_at IS NULL OR NOT EXISTS(SELECT 1 FROM loomplane_managed_cleanup_tombstones x WHERE x.org_id=a.org_id AND x.release_id=a.release_id) OR EXISTS(SELECT 1 FROM loomplane_managed_cleanup_current_drift d WHERE d.org_id=a.org_id AND d.release_id=a.release_id AND d.present) OR EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances w WHERE w.org_id=a.org_id AND w.release_id=a.release_id AND w.outcome IS NULL)))`, t.OrgID, t.CloudID, t.ResourceID, scope.Plan, scope.Service, scope.Connection, o.session.grant.Target.AttachmentID, scope.Project, scope.Environment); writeErr != nil {
			return writeErr
		}
		var mode string
		var held *bool
		if readErr := tx.QueryRow(ctx, `SELECT i.body->>'mode',h.released FROM loomplane_cluster_workload_intents i
 LEFT JOIN loomplane_cluster_workload_holds h ON h.org_id=i.org_id AND h.plan_id=i.plan_id AND h.id=i.body->'input'->>'id' AND h.service=i.service AND h.cloud_id=$4 AND h.resource_id=$5
 WHERE i.org_id=$1 AND i.plan_id=$2 AND i.service=$3`, scope.Org, scope.Plan, scope.Service, t.CloudID, t.ResourceID).Scan(&mode, &held); readErr != nil {
			return readErr
		}
		if mode == "guaranteed" && (held == nil || !*held) {
			pending = true
		}
		if mode != "guaranteed" && mode != "best_effort" {
			return ErrCleanupObservationIncomplete
		}
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return commitErr
	}
	if pending {
		return ErrCleanupConverging
	}
	return nil
}
