package planning

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/xraph/grove/driver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/connections"
)

var (
	ErrCleanupConverging    = errors.New("managed cleanup is waiting for Kubernetes to finish a reviewed deletion")
	ErrCleanupUnprovedChild = errors.New("managed cleanup cannot prove the current owner of a workload child")
)

type recoveredChild struct {
	Child   cleanupChild
	Parent  kube.ManagedRequest
	Source  string
	SetName string
	Pods    map[string]cleanupChild
}

const capturedCleanupParents = ` FROM loomplane_managed_workload_issuances w
 WHERE w.org_id=$1 AND w.scope->>'Connection'=$2 AND w.scope->>'Instance'=$3
 AND w.scope->>'NamespaceUID'=$4 AND w.scope->>'ClusterCA'=$5
 AND w.cleanup_grant_id IS NOT NULL AND w.outcome->>'State'='accepted'
 AND w.request->>'UID'=w.outcome->>'UID' AND w.request->>'PropagationPolicy'='Orphan'
 AND w.request->>'Method'='DELETE' AND w.request->>'Group'='apps' AND w.request->>'Version'='v1' AND w.request->>'Resource'='deployments'`

func (s *cleanupSession) captured(ctx context.Context) ([]recoveredChild, error) {
	scope := s.grant.Target.Scope
	rows, err := s.store.db.Query(ctx, `SELECT w.id,w.request,w.cleanup_children`+capturedCleanupParents+` AND EXISTS(SELECT 1 FROM jsonb_array_elements(w.cleanup_children) c WHERE c->>'release'=$6) ORDER BY w.issued_at DESC,w.id`, scope.Org, scope.Connection, scope.Instance, scope.NamespaceUID, scope.ClusterCA, scope.Release)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var sets, pods []recoveredChild
	for rows.Next() {
		var source string
		var req, graph []byte
		if scanErr := rows.Scan(&source, &req, &graph); scanErr != nil {
			return nil, scanErr
		}
		var parent kube.ManagedRequest
		var children []cleanupChild
		if json.Unmarshal(req, &parent) != nil || json.Unmarshal(graph, &children) != nil {
			return nil, kube.ErrManagedAuthority
		}
		byUID := map[string]cleanupChild{}
		bySet := map[string]map[string]cleanupChild{}
		for _, c := range children {
			if c.Release != scope.Release {
				continue
			}
			if c.Name == "" || c.UID == "" || c.Version == "" || c.OwnerUID == "" {
				return nil, kube.ErrManagedAuthority
			}
			byUID[c.UID] = c
			if c.Resource == "pods" {
				if bySet[c.OwnerUID] == nil {
					bySet[c.OwnerUID] = map[string]cleanupChild{}
				}
				bySet[c.OwnerUID][c.UID] = c
			}
		}
		for _, c := range children {
			if c.Release != scope.Release || seen[c.UID] {
				continue
			}
			seen[c.UID] = true
			entry := recoveredChild{Child: c, Parent: parent, Source: source}
			switch c.Resource {
			case "replicasets":
				if c.OwnerUID != parent.UID {
					return nil, kube.ErrManagedAuthority
				}
				entry.Pods = bySet[c.UID]
				sets = append(sets, entry)
			case "pods":
				set := byUID[c.OwnerUID]
				if set.Resource != "replicasets" || set.OwnerUID != parent.UID {
					return nil, kube.ErrManagedAuthority
				}
				entry.SetName = set.Name
				pods = append(pods, entry)
			default:
				return nil, kube.ErrManagedAuthority
			}
		}
	}
	return append(sets, pods...), rows.Err()
}

// RecoverCleanupChildren resumes from accepted parent orphaning effects. Each
// call sends at most one child DELETE and can restart with a fresh credential.
func (s *ManagedCleanupStore) RecoverCleanupChildren(ctx context.Context, sel ManagedCleanupSelection, credentials *connections.Service) error {
	sessions, err := s.cleanupSessions(ctx, sel, credentials)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if session.grant.Target.Kind == ManagedCleanupRetireDependents {
			continue
		}
		entries, readErr := session.captured(ctx)
		if readErr != nil {
			return readErr
		}
		for _, entry := range entries {
			r := entry.request(session.grant.Target.Scope.Namespace)
			obj, getErr := session.childObject(ctx, r)
			if apierrors.IsNotFound(getErr) {
				continue
			}
			if getErr != nil {
				return getErr
			}
			if string(obj.GetUID()) != r.UID {
				return ErrCleanupUnprovedChild
			}
			if obj.GetDeletionTimestamp() != nil {
				return ErrCleanupConverging
			}
			if err := session.delete(ctx, r, obj); err != nil {
				return err
			}
			return ErrCleanupConverging
		}
	}
	for _, session := range sessions {
		if checkErr := session.drained(ctx); checkErr != nil {
			return checkErr
		}
	}
	return nil
}

func (e recoveredChild) request(namespace string) kube.ManagedRequest {
	group := ""
	if e.Child.Resource == "replicasets" {
		group = "apps"
	}
	return kube.ManagedRequest{Group: group, Version: "v1", Resource: e.Child.Resource, Namespace: namespace, Name: e.Child.Name, UID: e.Child.UID, Method: http.MethodDelete, PropagationPolicy: string(metav1.DeletePropagationOrphan)}
}

func (s *cleanupSession) childObject(ctx context.Context, r kube.ManagedRequest) (*unstructured.Unstructured, error) {
	return s.client.Dynamic.Resource(schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Resource}).Namespace(r.Namespace).Get(ctx, r.Name, metav1.GetOptions{})
}

func (s *cleanupSession) recoveryProof(ctx context.Context, r kube.ManagedRequest) (*recoveredChild, error) {
	entries, err := s.captured(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		expected := e.request(s.grant.Target.Scope.Namespace)
		expected.ResourceVersion = r.ResourceVersion
		if expected != r {
			continue
		}
		if err := s.verifyOrphan(ctx, r, e); err != nil {
			return nil, err
		}
		return &e, nil
	}
	return nil, ErrCleanupUnprovedChild
}

func (s *cleanupSession) verifyOrphan(ctx context.Context, r kube.ManagedRequest, e recoveredChild) error {
	scope := s.grant.Target.Scope
	d, err := s.client.Kube.AppsV1().Deployments(scope.Namespace).Get(ctx, e.Parent.Name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && string(d.UID) == e.Parent.UID {
		return ErrCleanupConverging
	}
	if r.Resource == "pods" {
		rs, rsErr := s.client.Kube.AppsV1().ReplicaSets(scope.Namespace).Get(ctx, e.SetName, metav1.GetOptions{})
		if rsErr != nil && !apierrors.IsNotFound(rsErr) {
			return rsErr
		}
		if rsErr == nil && string(rs.UID) == e.Child.OwnerUID {
			return ErrCleanupConverging
		}
	}
	obj, err := s.childObject(ctx, r)
	if err != nil {
		return err
	}
	if obj.GetDeletionTimestamp() != nil {
		return ErrCleanupConverging
	}
	if string(obj.GetUID()) != r.UID || obj.GetResourceVersion() != r.ResourceVersion || obj.GetGeneration() != e.Child.Generation || len(obj.GetOwnerReferences()) != 0 || !cleanupObjectOwned(obj, scope) {
		return ErrCleanupUnprovedChild
	}
	annotations := obj.GetAnnotations()
	if r.Resource == "replicasets" {
		annotations, _, err = unstructured.NestedStringMap(obj.Object, "spec", "template", "metadata", "annotations")
		if err != nil {
			return err
		}
		// Orphaning must not strand an unreviewed child that arrived since capture.
		_, pods, inventoryErr := s.nativeInventory(ctx)
		if inventoryErr != nil {
			return inventoryErr
		}
		for i := range pods.Items {
			pod := &pods.Items[i]
			owner := metav1.GetControllerOf(pod)
			if owner != nil && string(owner.UID) == r.UID {
				captured, known := e.Pods[string(pod.UID)]
				if !known || captured.Name != pod.Name || captured.Generation != pod.Generation || len(pod.OwnerReferences) != 1 || owner.APIVersion != "apps/v1" || owner.Kind != "ReplicaSet" || owner.Name != e.Child.Name || !cleanupObjectOwned(pod, scope) || pod.Annotations[kube.WorkloadReleaseAnnotation] != scope.Release || pod.Annotations[owned.LabelRevision] != scope.RuntimeRevision {
					return ErrCleanupUnprovedChild
				}
			}
		}
	}
	if annotations[kube.WorkloadReleaseAnnotation] != scope.Release || annotations[owned.LabelRevision] != scope.RuntimeRevision {
		return ErrCleanupUnprovedChild
	}
	return nil
}

func (s *cleanupSession) guardRecovery(ctx context.Context, tx driver.Tx, target ManagedCleanupTarget, r kube.ManagedRequest, coowners []byte, e recoveredChild) error {
	// The original accepted Deployment mutation still needs the current review's
	// complete owner set. A retained child alone never grants a new deletion.
	if err := guardCleanupRequest(ctx, tx, target, e.Parent, coowners); err != nil {
		return err
	}
	child, err := json.Marshal(e.Child)
	if err != nil {
		return err
	}
	parent, err := json.Marshal(e.Parent)
	if err != nil {
		return err
	}
	scope := target.Scope
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1`+capturedCleanupParents+` AND w.id=$7 AND EXISTS(SELECT 1 FROM jsonb_array_elements(w.cleanup_children) c WHERE c->>'release'=$6 AND c=$8::jsonb) AND w.request=$9::jsonb)`, scope.Org, scope.Connection, scope.Instance, scope.NamespaceUID, scope.ClusterCA, scope.Release, e.Source, child, parent).Scan(&valid)
	if err != nil || !valid {
		return errors.Join(ErrCleanupUnprovedChild, err)
	}
	if r.Resource == "pods" {
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances w WHERE w.org_id=$1 AND w.release_id=$2 AND w.scope->>'Instance'=$3 AND w.scope->>'Connection'=$4 AND w.scope->>'NamespaceUID'=$5 AND w.scope->>'ClusterCA'=$6 AND w.cleanup_grant_id IS NOT NULL AND w.request->>'Group'='apps' AND w.request->>'Version'='v1' AND w.request->>'Resource'='replicasets' AND w.request->>'Method'='DELETE' AND w.request->>'UID'=$7 AND w.request->>'Name'=$8 AND w.request->>'CleanupSource'=$9 AND w.request->>'PropagationPolicy'='Orphan' AND w.outcome->>'State'='accepted' AND w.outcome->>'UID'=$7)`, scope.Org, scope.Release, scope.Instance, scope.Connection, scope.NamespaceUID, scope.ClusterCA, e.Child.OwnerUID, e.SetName, e.Source).Scan(&valid)
		if err != nil || !valid {
			return errors.Join(ErrCleanupUnprovedChild, err)
		}
	}
	return nil
}
