package planning

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/xraph/ctrlplane/id"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
)

func (s *cleanupSession) retire(ctx context.Context, send bool) error {
	scope := s.grant.Target.Scope
	sets, pods, err := s.nativeInventory(ctx)
	if err != nil {
		return err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Annotations[kube.WorkloadReleaseAnnotation] != scope.Release {
			continue
		}
		if checkErr := s.retireChild(ctx, kube.ManagedRequest{Version: "v1", Resource: "pods"}, pod, send); checkErr != nil {
			return checkErr
		}
	}
	for i := range sets.Items {
		set := &sets.Items[i]
		if set.Spec.Template.Annotations[kube.WorkloadReleaseAnnotation] != scope.Release {
			continue
		}
		if checkErr := s.retireChild(ctx, kube.ManagedRequest{Group: "apps", Version: "v1", Resource: "replicasets"}, set, send); checkErr != nil {
			return checkErr
		}
	}
	if !send {
		return nil
	}
	return s.drained(ctx)
}

func (s *cleanupSession) retireChild(ctx context.Context, r kube.ManagedRequest, obj metav1.Object, send bool) error {
	if send {
		return s.delete(ctx, r, obj)
	}
	r.Method, r.Namespace, r.Name, r.UID, r.ResourceVersion = http.MethodDelete, obj.GetNamespace(), obj.GetName(), string(obj.GetUID()), obj.GetResourceVersion()
	_, err := s.store.InspectDescendant(ctx, s.grant.Target.Scope.Org, s.grant.ID, r, s)
	return err
}

func (s *cleanupSession) ReadCleanupDescendant(ctx context.Context, target ManagedCleanupTarget, r kube.ManagedRequest) (ManagedCleanupDescendantEvidence, error) {
	var e ManagedCleanupDescendantEvidence
	scope := target.Scope
	if scope != s.grant.Target.Scope || r.Method != http.MethodDelete || r.Version != "v1" || r.Namespace != scope.Namespace || r.UID == "" || r.ResourceVersion == "" {
		return e, kube.ErrManagedAuthority
	}
	setName := r.Name
	expectedSetUID := r.UID
	if r.Resource == "pods" && r.Group == "" {
		pod, err := s.client.Kube.CoreV1().Pods(scope.Namespace).Get(ctx, r.Name, metav1.GetOptions{})
		if err != nil {
			return e, err
		}
		owner := metav1.GetControllerOf(pod)
		if string(pod.UID) != r.UID || pod.ResourceVersion != r.ResourceVersion || !cleanupObjectOwned(pod, scope) || pod.Annotations[kube.WorkloadReleaseAnnotation] != scope.Release || pod.Annotations[owned.LabelRevision] != scope.RuntimeRevision || len(pod.OwnerReferences) != 1 || owner == nil || owner.Kind != "ReplicaSet" || owner.APIVersion != "apps/v1" || owner.UID == "" {
			return e, kube.ErrManagedAuthority
		}
		setName, expectedSetUID = owner.Name, string(owner.UID)
		e.PodUID = string(pod.UID)
	} else if r.Resource != "replicasets" || r.Group != "apps" {
		return e, kube.ErrManagedAuthority
	}
	set, err := s.client.Kube.AppsV1().ReplicaSets(scope.Namespace).Get(ctx, setName, metav1.GetOptions{})
	if err != nil {
		return e, err
	}
	owner := metav1.GetControllerOf(set)
	iid, err := id.ParseWithPrefix(scope.Instance, id.PrefixInstance)
	if err != nil {
		return e, kube.ErrManagedAuthority
	}
	if string(set.UID) != expectedSetUID || !cleanupObjectOwned(set, scope) || set.Spec.Template.Annotations[kube.WorkloadReleaseAnnotation] != scope.Release || set.Spec.Template.Annotations[owned.LabelRevision] != scope.RuntimeRevision || set.Spec.Replicas == nil || *set.Spec.Replicas != 0 || len(set.OwnerReferences) != 1 || owner == nil || owner.APIVersion != "apps/v1" || owner.Kind != "Deployment" || owner.Name != kube.WorkloadName(iid) || owner.UID == "" {
		return e, kube.ErrManagedAuthority
	}
	if r.Resource == "replicasets" && set.ResourceVersion != r.ResourceVersion {
		return e, kube.ErrManagedAuthority
	}
	d, err := s.client.Kube.AppsV1().Deployments(scope.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
	if err != nil {
		return e, err
	}
	if d.UID != owner.UID || d.DeletionTimestamp != nil || !cleanupObjectOwned(d, scope) {
		return e, kube.ErrManagedAuthority
	}
	marker := d.Spec.Template.Annotations[kube.WorkloadReleaseAnnotation]
	var raw []byte
	err = s.store.db.QueryRow(ctx, `SELECT body FROM loomplane_workload_controllers WHERE org_id=$1 AND connection_id=$2 AND instance_id=$3 AND body->>'release_id'=$4 AND body->>'deployment_uid'=$5`, scope.Org, scope.Connection, scope.Instance, marker, string(d.UID)).Scan(&raw)
	if err != nil {
		return e, err
	}
	var receipt kube.ControllerReceipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.RuntimeRevision != d.Spec.Template.Annotations[owned.LabelRevision] {
		return e, kube.ErrManagedAuthority
	}
	if receipt.DeploymentGeneration != d.Generation {
		gen, err := s.replicaGeneration(ctx, scope, d)
		if err != nil || gen != receipt.DeploymentGeneration {
			return e, kube.ErrManagedAuthority
		}
	}
	e.Scope, e.Release, e.Request, e.ObservedAt = scope, scope.Release, r, time.Now().UTC()
	e.ResourceVersion, e.ReplicaSetResourceVersion = r.ResourceVersion, set.ResourceVersion
	e.DeploymentUID, e.ReplicaSetUID, e.NewerController = string(d.UID), string(set.UID), &receipt
	return e, nil
}

func (s *cleanupSession) replicaGeneration(ctx context.Context, scope kube.ManagedScope, d *appsv1.Deployment) (int64, error) {
	h, err := s.client.Kube.AutoscalingV2().HorizontalPodAutoscalers(scope.Namespace).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		return 0, err
	}
	return s.store.journal.ReplicaProgression(ctx, s.client.Kube, scope.Org, scope.NamespaceUID, scope.ClusterCA, d, h)
}
