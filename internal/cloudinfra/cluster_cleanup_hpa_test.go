package cloudinfra

import (
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
)

func TestCleanupScopesIndependentOfSupply(t *testing.T) {
	for _, mode := range []string{"absent", "wrong-namespace", "partial-rs", "hpa-denied", "hpa-live"} {
		t.Run(mode, func(t *testing.T) {
			uid := "known"
			if mode == "wrong-namespace" {
				uid = "replacement"
			}
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps", UID: types.UID(uid)}}
			k := fake.NewSimpleClientset(ns)
			k.PrependReactor("list", "deployments", func(kubetesting.Action) (bool, runtime.Object, error) {
				return true, &appsv1.DeploymentList{ListMeta: metav1.ListMeta{ResourceVersion: "d"}}, nil
			})
			k.PrependReactor("list", "replicasets", func(kubetesting.Action) (bool, runtime.Object, error) {
				list := &appsv1.ReplicaSetList{ListMeta: metav1.ListMeta{ResourceVersion: "r"}}
				if mode == "partial-rs" {
					list.Continue = "remaining"
				}
				return true, list, nil
			})
			k.PrependReactor("list", "pods", func(kubetesting.Action) (bool, runtime.Object, error) {
				return true, &corev1.PodList{ListMeta: metav1.ListMeta{ResourceVersion: "p"}}, nil
			})
			k.PrependReactor("list", "horizontalpodautoscalers", func(kubetesting.Action) (bool, runtime.Object, error) {
				if mode == "hpa-denied" {
					return true, nil, errors.New("denied")
				}
				list := &autoscalingv2.HorizontalPodAutoscalerList{ListMeta: metav1.ListMeta{ResourceVersion: "h"}}
				if mode == "hpa-live" {
					list.Items = []autoscalingv2.HorizontalPodAutoscaler{{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps", UID: "old-hpa"}}}
				}
				return true, list, nil
			})
			target := ClusterCleanupTombstone{SchemaVersion: 1, Namespace: "apps", NamespaceUID: "known", ReleaseID: "old", HPAs: []ClusterCleanupHPA{{Name: "app", UID: "old-hpa"}}}
			projection := observeCleanupScopes(t.Context(), k, []CleanupCapacityScope{{Namespace: target.Namespace, NamespaceUID: target.NamespaceUID, HPAs: target.HPAs}}, kubernetes.CapacityInventory{Complete: false})
			m := indexCleanupInventory(&projection).match(&target, nil)
			wantComplete := mode == "absent" || mode == "hpa-live"
			wantResiduals := 0
			if mode == "hpa-live" {
				wantResiduals = 1
			}
			if m.complete != wantComplete || len(m.residuals) != wantResiduals {
				t.Fatalf("scope coverage: %+v", m)
			}
		})
	}
}
