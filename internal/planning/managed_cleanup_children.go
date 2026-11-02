package planning

import (
	"context"
	"net/http"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
)

// The parent DELETE issuance pins the Deployment UID and resourceVersion.
// Keep only the child identities needed to recognize the reviewed owner chain.
type cleanupChild struct {
	Resource   string `json:"resource"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Version    string `json:"resource_version"`
	Generation int64  `json:"generation,omitempty"`
	Release    string `json:"release"`
	OwnerUID   string `json:"owner_uid"`
}

func (s *cleanupSession) preflight(ctx context.Context) error {
	if s.grant.Target.Kind == ManagedCleanupRetireDependents {
		return s.retire(ctx, false)
	}
	scope := s.grant.Target.Scope
	for _, a := range s.grant.Target.Accepted {
		r := a.Request
		if r.Resource != "deployments" || a.Outcome.State != "accepted" || (r.Method != http.MethodPost && r.Method != http.MethodPut) {
			continue
		}
		d, err := s.client.Kube.AppsV1().Deployments(scope.Namespace).Get(ctx, r.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		r.Method, r.UID, r.ResourceVersion = http.MethodDelete, a.Outcome.UID, d.ResourceVersion
		if checkErr := s.checkWhole(ctx, r); checkErr != nil {
			return checkErr
		}
		if _, err = s.readChildren(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func (s *cleanupSession) nativeInventory(ctx context.Context) (*appsv1.ReplicaSetList, *corev1.PodList, error) {
	ns := s.grant.Target.Scope.Namespace
	opts := metav1.ListOptions{Limit: 5000}
	sets, err := s.client.Kube.AppsV1().ReplicaSets(ns).List(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	pods, err := s.client.Kube.CoreV1().Pods(ns).List(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	if sets.Continue != "" || pods.Continue != "" || sets.ResourceVersion == "" || pods.ResourceVersion == "" {
		return nil, nil, ErrManagedCleanupUnavailable
	}
	return sets, pods, nil
}

func (s *cleanupSession) readChildren(ctx context.Context, r kube.ManagedRequest) ([]cleanupChild, error) {
	scope := s.grant.Target.Scope
	owners := map[string]kube.ManagedScope{}
	for _, peer := range s.peers {
		target := peer.grant.Target
		if !sameCleanupPhysicalScope(target.Scope, scope) || target.Kind == ManagedCleanupRetireDependents {
			continue
		}
		for _, a := range target.Accepted {
			if a.Request.Resource == r.Resource && a.Request.Name == r.Name && a.Outcome.State == "accepted" && a.Outcome.UID == r.UID && (a.Request.Method == http.MethodPost || a.Request.Method == http.MethodPut) {
				owners[target.Scope.Release] = target.Scope
			}
		}
	}
	sets, pods, err := s.nativeInventory(ctx)
	if err != nil {
		return nil, err
	}
	children := []cleanupChild{}
	setsByUID := map[string]cleanupChild{}
	for i := range sets.Items {
		set := &sets.Items[i]
		owner := metav1.GetControllerOf(set)
		release := set.Spec.Template.Annotations[kube.WorkloadReleaseAnnotation]
		reviewed, known := owners[release]
		if owner == nil || string(owner.UID) != r.UID {
			if known {
				return nil, kube.ErrManagedAuthority
			}
			continue
		}
		if !known || len(set.OwnerReferences) != 1 || owner.APIVersion != "apps/v1" || owner.Kind != "Deployment" || owner.Name != r.Name || !cleanupObjectOwned(set, reviewed) || set.Spec.Template.Annotations[owned.LabelRevision] != reviewed.RuntimeRevision {
			return nil, kube.ErrManagedAuthority
		}
		child := cleanupChild{Resource: "replicasets", Name: set.Name, UID: string(set.UID), Version: set.ResourceVersion, Generation: set.Generation, Release: release, OwnerUID: r.UID}
		children = append(children, child)
		setsByUID[child.UID] = child
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		owner := metav1.GetControllerOf(pod)
		release := pod.Annotations[kube.WorkloadReleaseAnnotation]
		reviewed, known := owners[release]
		if owner == nil {
			if known {
				return nil, kube.ErrManagedAuthority
			}
			continue
		}
		set, found := setsByUID[string(owner.UID)]
		if !found {
			if known {
				return nil, kube.ErrManagedAuthority
			}
			continue
		}
		if !known || len(pod.OwnerReferences) != 1 || release != set.Release || owner.Kind != "ReplicaSet" || owner.APIVersion != "apps/v1" || owner.Name != set.Name || !cleanupObjectOwned(pod, reviewed) || pod.Annotations[owned.LabelRevision] != reviewed.RuntimeRevision {
			return nil, kube.ErrManagedAuthority
		}
		children = append(children, cleanupChild{Resource: "pods", Name: pod.Name, UID: string(pod.UID), Version: pod.ResourceVersion, Generation: pod.Generation, Release: release, OwnerUID: set.UID})
	}
	return children, nil
}
