package cloudinfra

import (
	"context"
	"encoding/json"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeclient "k8s.io/client-go/kubernetes"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
)

type CleanupCapacityScope struct {
	Namespace    string
	NamespaceUID string
	HPAs         []ClusterCleanupHPA
}

func (s *PgStore) CleanupCapacityScopes(ctx context.Context, org, cloud, resource string) ([]CleanupCapacityScope, error) {
	rows, err := s.db.Query(ctx, `SELECT body->>'namespace',body->>'namespace_uid',coalesce(body->'hpas','[]'::jsonb) FROM loomplane_managed_cleanup_tombstones WHERE org_id=$1 AND cloud_id=$2 AND resource_id=$3 ORDER BY release_id`, org, cloud, resource)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []CleanupCapacityScope
	for rows.Next() {
		var r CleanupCapacityScope
		var raw []byte
		if err := rows.Scan(&r.Namespace, &r.NamespaceUID, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &r.HPAs); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// Capacity observers need HPA coverage only in namespaces with previously
// reviewed HPAs. A denied read contributes no absence proof.
func observeCleanupHPAs(ctx context.Context, k kubeclient.Interface, scopes []CleanupCapacityScope, in kubernetes.CapacityInventory) []ClusterCleanupHPAObservation {
	namespaces := map[string]string{}
	for _, n := range in.Namespaces {
		if n.DeletionTimestamp == nil {
			namespaces[n.Name] = string(n.UID)
		}
	}
	names := map[string]map[string]bool{}
	for _, s := range scopes {
		if s.NamespaceUID == "" || namespaces[s.Namespace] != s.NamespaceUID {
			continue
		}
		for _, h := range s.HPAs {
			if names[s.Namespace] == nil {
				names[s.Namespace] = map[string]bool{}
			}
			names[s.Namespace][h.Name] = true
		}
	}
	var result []ClusterCleanupHPAObservation
	for ns, targets := range names {
		list, err := k.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, metav1.ListOptions{Limit: 5000})
		qualified := err == nil && list.Continue == "" && list.ResourceVersion != ""
		observations := map[string]ClusterCleanupHPAObservation{}
		if qualified {
			for _, obj := range list.Items {
				if !targets[obj.Name] {
					continue
				}
				target := obj.Spec.ScaleTargetRef
				r := ClusterCleanupHPAObservation{NamespaceUID: namespaces[ns], Name: obj.Name, UID: string(obj.UID), Generation: obj.Generation, Complete: obj.UID != "", Deleting: obj.DeletionTimestamp != nil}
				if target.APIVersion == "apps/v1" && target.Kind == "Deployment" {
					r.DeploymentName = target.Name
				}
				observations[obj.Name] = r
			}
		}
		for name := range targets {
			r, ok := observations[name]
			if !ok {
				r = ClusterCleanupHPAObservation{NamespaceUID: namespaces[ns], Name: name, Complete: qualified}
			}
			result = append(result, r)
		}
	}
	return result
}

// observeCleanupScopes can reuse the workload inventory even when unrelated
// provider supply cannot be qualified. If a global list was incomplete, read
// only the previously reviewed namespaces with the authorized management client.
func observeCleanupScopes(ctx context.Context, k kubeclient.Interface, scopes []CleanupCapacityScope, in kubernetes.CapacityInventory) ClusterCleanupInventory {
	if in.Complete && in.WorkloadListsVersioned {
		out := NormalizeCleanupInventory(in)
		out.HPAs = observeCleanupHPAs(ctx, k, scopes, in)
		return out
	}
	out := ClusterCleanupInventory{SchemaVersion: 1, Complete: true, CheckedAt: time.Now().UTC()}
	seen := map[string]bool{}
	opts := metav1.ListOptions{Limit: 5000}
	for _, scope := range scopes {
		if seen[scope.NamespaceUID] {
			continue
		}
		seen[scope.NamespaceUID] = true
		ns, err := k.CoreV1().Namespaces().Get(ctx, scope.Namespace, metav1.GetOptions{})
		if err != nil || string(ns.UID) != scope.NamespaceUID || ns.DeletionTimestamp != nil {
			continue
		}
		deps, err := k.AppsV1().Deployments(scope.Namespace).List(ctx, opts)
		if err != nil || deps.Continue != "" || deps.ResourceVersion == "" {
			continue
		}
		sets, err := k.AppsV1().ReplicaSets(scope.Namespace).List(ctx, opts)
		if err != nil || sets.Continue != "" || sets.ResourceVersion == "" {
			continue
		}
		pods, err := k.CoreV1().Pods(scope.Namespace).List(ctx, opts)
		if err != nil || pods.Continue != "" || pods.ResourceVersion == "" {
			continue
		}
		inventory := kubernetes.CapacityInventory{Complete: true, CheckedAt: out.CheckedAt, Namespaces: []corev1.Namespace{*ns}, Deployments: deps.Items, ReplicaSets: sets.Items, Pods: pods.Items}
		scoped := NormalizeCleanupInventory(inventory)
		if !scoped.Complete {
			continue
		}
		out.NamespaceUIDs = append(out.NamespaceUIDs, scoped.NamespaceUIDs...)
		out.Objects = append(out.Objects, scoped.Objects...)
		out.HPAs = append(out.HPAs, observeCleanupHPAs(ctx, k, scopes, inventory)...)
	}
	return out
}
