//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestClusterScalingInstalledHPA(t *testing.T) {
	if os.Getenv("LOOMPLANE_TEST_KIND_METRICS") != "1" {
		t.Skip("enable temporary metrics on the dedicated kind cluster")
	}
	managedCleanupEffectsRealAPI(t, false, "application", "hpa")
}

func checkInstalledHPA(ctx context.Context, t *testing.T, e *enrollmentServiceFixture, infra *cloudinfra.Service, f *poolFixture, receipt kube.ControllerReceipt, live *appsv1.Deployment, cfg *planning.ServiceConfig, proof *kube.ControllerStore) {
	t.Helper()
	dep := e.bootstrap.Kube.AppsV1().Deployments(live.Namespace)
	autos := e.bootstrap.Kube.AutoscalingV2().HorizontalPodAutoscalers(live.Namespace)
	h, err := autos.Get(ctx, live.Name, metav1.GetOptions{})
	if err != nil || h.UID == "" || h.Spec.MinReplicas == nil || *h.Spec.MinReplicas != 2 || h.Spec.MaxReplicas != 3 {
		t.Fatal("reviewed autoscaler", err)
	}
	// Lower the scale below the accepted minimum. Only the fixture writes 1.
	// The HPA controller must restore 2 under its reviewed policy.
	scale, err := dep.GetScale(ctx, live.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	scale.Spec.Replicas = 1
	if _, err = dep.UpdateScale(ctx, live.Name, scale, metav1.UpdateOptions{FieldManager: "fixture-scale-stimulus"}); err != nil {
		t.Fatal(err)
	}
	client := e.bootstrap.Kube
	var observed *appsv1.Deployment
	if pollErr := wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 50*time.Second, true, func(call context.Context) (bool, error) {
		d, e := dep.Get(call, live.Name, metav1.GetOptions{})
		if e != nil {
			return false, e
		}
		current, e := autos.Get(call, h.Name, metav1.GetOptions{})
		if e != nil {
			return false, e
		}
		if current.UID != h.UID || d.UID != live.UID || d.Generation < live.Generation+2 || d.Spec.Replicas == nil || *d.Spec.Replicas != 2 || current.Status.LastScaleTime == nil || current.Status.DesiredReplicas != 2 || d.Status.AvailableReplicas != 2 {
			return false, nil
		}
		observed = d
		gen, e := proof.ReplicaProgression(call, client, receipt.OrgID, receipt.NamespaceUID, receipt.ClusterCA, d, current)
		return gen == receipt.DeploymentGeneration, e
	}); pollErr != nil {
		if observed != nil {
			for _, m := range observed.ManagedFields {
				t.Log("manager", m.Manager, m.Subresource)
			}
		}
		t.Fatal("HPA controller did not produce qualified replica progression", pollErr)
	}
	t.Logf("HPA %s restored 2 replicas at Deployment generation %d; accepted generation %d", h.UID, observed.Generation, receipt.DeploymentGeneration)
	if eventErr := wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 15*time.Second, true, func(call context.Context) (bool, error) {
		events, eventsErr := client.CoreV1().Events(live.Namespace).List(call, metav1.ListOptions{FieldSelector: "involvedObject.uid=" + string(h.UID) + ",reason=SuccessfulRescale"})
		if eventsErr != nil {
			return false, eventsErr
		}
		for _, event := range events.Items {
			if event.InvolvedObject.Kind == "HorizontalPodAutoscaler" && event.InvolvedObject.UID == h.UID && event.Reason == "SuccessfulRescale" && strings.Contains(event.Message, "New size: 2") && (event.Source.Component == "horizontal-pod-autoscaler" || event.ReportingController == "horizontal-pod-autoscaler") {
				t.Logf("HPA event %s: %s, %s", event.UID, event.Reason, event.Message)
				return true, nil
			}
		}
		return false, nil
	}); eventErr != nil {
		t.Fatal("missing HPA controller scale event", eventErr)
	}
	current, readErr := autos.Get(ctx, h.Name, metav1.GetOptions{})
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, which := range []string{"image", "env", "pod_security", "selector", "strategy", "hpa_bounds", "hpa_uid", "target", "replicas_above_max", "manual_scale", "rotated_key", "missing_key", "namespace", "ca", "deployment_uid", "release"} {
		d := observed.DeepCopy()
		a := current.DeepCopy()
		v := proof
		ns, ca := receipt.NamespaceUID, receipt.ClusterCA
		switch which {
		case "image":
			d.Spec.Template.Spec.Containers[0].Image = "another-image"
		case "env":
			d.Spec.Template.Spec.Containers[0].Env = append(d.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "PRIVATE_TEST", Value: "changed"})
		case "pod_security":
			yes := true
			d.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{RunAsNonRoot: &yes}
		case "selector":
			d.Spec.Selector.MatchLabels["other"] = "changed"
		case "strategy":
			d.Spec.Strategy.Type = appsv1.RecreateDeploymentStrategyType
		case "hpa_bounds":
			a.Spec.MaxReplicas++
		case "hpa_uid":
			a.UID = "a-new-hpa"
		case "target":
			a.Spec.ScaleTargetRef.Name += "changed"
		case "replicas_above_max":
			n := a.Spec.MaxReplicas + 1
			d.Spec.Replicas = &n
			a.Status.DesiredReplicas = n
		case "manual_scale":
			for i := range d.ManagedFields {
				if d.ManagedFields[i].Subresource == "scale" {
					d.ManagedFields[i].Manager = "kubectl-scale"
				}
			}
		case "rotated_key":
			v = kube.NewControllerStore(e.pg).WithReplicaProof(make([]byte, 32))
		case "missing_key":
			v = kube.NewControllerStore(e.pg)
		case "namespace":
			ns = "other"
		case "ca":
			ca = "other"
		case "deployment_uid":
			d.UID = "other"
		case "release":
			d.Spec.Template.Annotations[kube.WorkloadReleaseAnnotation] = "other"
		}
		if gen, proofErr := v.ReplicaProgression(ctx, e.bootstrap.Kube, receipt.OrgID, ns, ca, d, a); proofErr != nil || gen != 0 {
			t.Fatal("unqualified replica progression", which, gen, proofErr)
		}
	}
	var raw []byte
	if err = e.sql.QueryRow(ctx, `SELECT body FROM loomplane_workload_controllers WHERE org_id=$1 AND runtime_ref=$2`, receipt.OrgID, receipt.RuntimeRef).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var kept kube.ControllerReceipt
	if err = json.Unmarshal(raw, &kept); err != nil || kept != receipt {
		t.Fatal("replica-only progression replaced original receipt", err)
	}
	adapters := cloudinfra.NewAdapters()
	if err = adapters.RegisterCluster("digitalocean", clusterCapacityKindReader{e.provider}, provider.ClusterDiscovery, provider.ClusterBootstrapAccess, provider.ClusterCapacityObservation); err != nil {
		t.Fatal(err)
	}
	infra.Adapters = adapters
	observedSnapshot, err := infra.ObserveClusterCapacity(ctx, e.principal, "cloud", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	var qualified int
	for _, w := range observedSnapshot.Workloads {
		if w.DeploymentUID == receipt.DeploymentUID && w.ReleaseID == receipt.ReleaseID && w.ReplicaProofGeneration == receipt.DeploymentGeneration {
			qualified++
		}
	}
	if qualified != 2 {
		t.Fatalf("observed %d proved replicas, want 2 (workloads complete=%v; observations=%+v)", qualified, observedSnapshot.WorkloadsComplete, observedSnapshot.Workloads)
	}
	var totalsRaw []byte
	if err := f.sql.QueryRow(ctx, `SELECT totals FROM loomplane_cluster_workload_observed_accounting WHERE org_id=$1 AND resource_id=$2 AND revision=$3`, receipt.OrgID, cfg.ClusterDemand.Input.ResourceID, observedSnapshot.ObservationRevision).Scan(&totalsRaw); err != nil {
		t.Fatal(err)
	}
	var totals map[string]cloudinfra.ClusterWorkloadVector
	if err := json.Unmarshal(totalsRaw, &totals); err != nil {
		t.Fatal(err)
	}
	cpu := cfg.ClusterDemand.Input.Maximum.CPUMillis
	if totals["maximum"].CPUMillis != cpu || totals["charged"].CPUMillis != totals["actual"].CPUMillis+cpu/3 || totals["running"].CPUMillis < cpu*2/3 {
		t.Fatal("current usage plus unobserved maximum", totals)
	}
}

func TestClusterScalingInstalledHPAMissingMetrics(t *testing.T) {
	managedCleanupEffectsRealAPI(t, false, "application", "hpa-missing")
}

// The node mapping here is a fixture. The kubelet's pod and allocation status
// are read from the dedicated cluster without changing either request.
func TestClusterScalingKindReportedRequests(t *testing.T) {
	e := newEnrollmentServiceFixture(t)
	in, err := kube.ReadCapacityInventory(t.Context(), e.bootstrap.Kube)
	if err != nil || len(in.Nodes) != 1 {
		t.Fatal("dedicated one-node inventory", err)
	}
	var pod *corev1.Pod
	for i := range in.Pods {
		p := &in.Pods[i]
		if p.Namespace == "kube-system" && p.Labels["k8s-app"] == "kube-dns" && p.Status.Phase == corev1.PodRunning {
			pod = p
			break
		}
	}
	if pod == nil || pod.Spec.Resources != nil || len(pod.Spec.Containers) != 1 || len(pod.Status.ContainerStatuses) != 1 || pod.Status.Resources == nil {
		t.Fatal("missing running CoreDNS container allocation")
	}
	req := pod.Spec.Containers[0].Resources.Requests.Cpu()
	if req.Sign() <= 0 || pod.Status.ContainerStatuses[0].AllocatedResources.Cpu().Cmp(*req) != 0 || pod.Status.Resources.Requests.Cpu().Cmp(*req) >= 0 {
		t.Fatal("fixture did not expose lower pod cgroup CPU")
	}
	in.Pods = []corev1.Pod{*pod}
	in.Nodes[0].Spec.ProviderID = "fixture://arithmetic"
	ref := provider.ClusterRef{ID: "arithmetic", AccountID: "fixture", Incarnation: "fixture"}
	facts := provider.ProviderClusterCapacity{Provider: "fixture", Ref: ref, CheckedAt: in.CheckedAt, Complete: true, Nodes: []provider.ClusterCapacityNode{{ExternalVMID: "arithmetic", PoolID: "fixture"}}}
	snapshot, err := cloudinfra.MergeClusterCapacity(facts, cloudinfra.KubernetesClusterCapacity{Provider: "fixture", Ref: ref, Inventory: in})
	if err != nil || !snapshot.WorkloadsComplete || len(snapshot.Workloads) != 1 || snapshot.Workloads[0].Requests.CPUMillis != req.MilliValue() || len(snapshot.Nodes) != 1 || snapshot.Nodes[0].Requests.CPUMillis == nil || *snapshot.Nodes[0].Requests.CPUMillis != req.MilliValue() {
		t.Fatal("node or workload request changed", err)
	}
	for _, condition := range snapshot.Conditions {
		if condition.Reason == "pod_resize_accounting_unknown" {
			t.Fatal(condition)
		}
	}
	t.Logf("CoreDNS pod %s: requested %s, pod cgroup %s, node and workload each count %dm", pod.UID, req, pod.Status.Resources.Requests.Cpu(), req.MilliValue())
}

func TestClusterScalingAcceptedMACGuard(t *testing.T) {
	db := managedClusterDB(t)
	if _, err := db.Exec(t.Context(), `INSERT INTO loomplane_managed_workload_authority(org_id,release_id,epoch,body) VALUES ('org','fixture',1,'{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO loomplane_managed_workload_issuances(id,org_id,release_id,epoch,scope,request) VALUES ('pending','org','fixture',1,'{}','{"Method":"POST","Resource":"deployments"}'),('historical','org','fixture',1,'{}','{"Method":"POST","Resource":"deployments"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE loomplane_managed_workload_issuances SET replica_spec_mac=decode(repeat('ab',32),'hex') WHERE id='pending'`); err == nil {
		t.Fatal("pending issuance acquired evidence")
	}
	if _, err := db.Exec(t.Context(), `UPDATE loomplane_managed_workload_issuances SET outcome='{"State":"accepted"}',resolved_at=clock_timestamp(),replica_spec_mac=CASE WHEN id='pending' THEN decode(repeat('ab',32),'hex') END`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"pending", "historical"} {
		if _, err := db.Exec(t.Context(), `UPDATE loomplane_managed_workload_issuances SET replica_spec_mac=decode(repeat('cd',32),'hex') WHERE id=$1`, id); err == nil {
			t.Fatal("replaced historical evidence", id)
		}
	}
}
