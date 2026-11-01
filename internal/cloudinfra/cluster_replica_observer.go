package cloudinfra

import (
	"context"
	"encoding/json"

	"github.com/xraph/grove/driver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeclient "k8s.io/client-go/kubernetes"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
)

func (s *Service) replicaProgressions(ctx context.Context, org, ca string, client kubeclient.Interface, in kubernetes.CapacityInventory) map[string]int64 {
	results := map[string]int64{}
	ns := map[string]string{}
	for _, n := range in.Namespaces {
		if n.DeletionTimestamp == nil {
			ns[n.Name] = string(n.UID)
		}
	}
	for i := range in.Deployments {
		d := &in.Deployments[i]
		if ctx.Err() != nil {
			break
		}
		if d.Spec.Template.Annotations[kubernetes.WorkloadReleaseAnnotation] == "" || ns[d.Namespace] == "" || d.Generation <= 1 {
			continue
		}
		h, err := client.AutoscalingV2().HorizontalPodAutoscalers(d.Namespace).Get(ctx, d.Name, metav1.GetOptions{})
		if err != nil {
			continue
		}
		if gen, err := s.ReplicaVerifier.ReplicaProgression(ctx, client, org, ns[d.Namespace], ca, d, h); err == nil && gen > 0 {
			results[string(d.UID)] = gen
		}
	}
	return results
}

type replicaObservationKey struct{}
type replicaObservation struct {
	org, resource string
	revision      int64
	proved        map[string]int64
	shortages     map[string]ClusterObservedWorkload
}

func saveObservedWorkloadAccounting(ctx context.Context, tx driver.Tx, r *ManagedResource, snapshot ClusterCapacitySnapshot) error {
	holds, err := clusterWorkloadHolds(ctx, tx, r.OrgID, r.ID)
	if err != nil {
		return err
	}
	charged, err := chargedClusterWorkloads(ctx, tx, snapshot, holds)
	if err != nil {
		return err
	}
	var running, actual, maximum ClusterWorkloadVector
	for _, w := range snapshot.Workloads {
		actual, err = clusterWorkloadUsage(actual, []clusterWorkloadCoverage{{Maximum: w.Requests}})
		if err != nil {
			return err
		}
		if w.Running {
			running, err = clusterWorkloadUsage(running, []clusterWorkloadCoverage{{Maximum: w.Requests}})
			if err != nil {
				return err
			}
		}
	}
	for _, h := range holds {
		maximum, err = clusterWorkloadUsage(maximum, []clusterWorkloadCoverage{{Maximum: h.Input.Maximum}})
		if err != nil {
			return err
		}
	}
	totals, err := json.Marshal(map[string]ClusterWorkloadVector{"charged": charged, "actual": actual, "running": running, "maximum": maximum})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cluster_workload_observed_accounting(org_id,cloud_id,resource_id,revision,checked_at,totals) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(org_id,resource_id) DO UPDATE SET revision=excluded.revision,checked_at=excluded.checked_at,totals=excluded.totals WHERE loomplane_cluster_workload_observed_accounting.revision<excluded.revision`, r.OrgID, r.CloudID, r.ID, snapshot.ObservationRevision, snapshot.StorageProvenance.CheckedAt, totals)
	return err
}
