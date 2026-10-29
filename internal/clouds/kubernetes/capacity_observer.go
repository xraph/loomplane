package kubernetes

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeclient "k8s.io/client-go/kubernetes"
)

// CapacityInventory uses management credentials. Namespace runtime roles do not grant these reads.
type CapacityInventory struct {
	Nodes          []corev1.Node
	Pods           []corev1.Pod
	Volumes        []corev1.PersistentVolume
	StorageClasses []storagev1.StorageClass
	CheckedAt      time.Time
	Complete       bool
}

func CapacityObserverRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"nodes", "pods", "persistentvolumes"}, Verbs: []string{"get", "list"}},
		{APIGroups: []string{"storage.k8s.io"}, Resources: []string{"storageclasses"}, Verbs: []string{"get", "list"}},
	}
}

// ReadCapacityInventory bounds every list. A continuation is incomplete inventory.
func ReadCapacityInventory(ctx context.Context, client kubeclient.Interface) (CapacityInventory, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out := CapacityInventory{CheckedAt: time.Now().UTC()}
	opts := metav1.ListOptions{Limit: 5000}
	nodes, err := client.CoreV1().Nodes().List(ctx, opts)
	if err != nil {
		return out, err
	}
	out.Nodes = nodes.Items
	pods, err := client.CoreV1().Pods("").List(ctx, opts)
	if err != nil {
		return out, err
	}
	out.Pods = pods.Items
	volumes, err := client.CoreV1().PersistentVolumes().List(ctx, opts)
	if err != nil {
		return out, err
	}
	out.Volumes = volumes.Items
	classes, err := client.StorageV1().StorageClasses().List(ctx, opts)
	if err != nil {
		return out, err
	}
	out.StorageClasses = classes.Items
	out.Complete = nodes.Continue == "" && pods.Continue == "" && volumes.Continue == "" && classes.Continue == ""
	return out, nil
}
