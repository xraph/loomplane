package kubernetes_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/grove/drivers/pgdriver"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/deployments"
	fed "github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
	"github.com/xraph/loomplane/pkg/provider"
)

func proveApplicationIdentity(ctx context.Context, t *testing.T, db *pgdriver.PgDB, client kubernetes.Interface, credentials *fixtureCredentials, admin *workloadidentity.Admin, d domain.Destination) {
	t.Helper()
	instance := id.New(id.PrefixInstance)
	image := os.Getenv("AGENT_IMAGE")
	var imageIndexes [][]byte
	must(t, json.Unmarshal([]byte(os.Getenv("APPLICATION_IMAGE_INDEXES")), &imageIndexes))
	saved := planning.Binding{ID: "application-binding", OrgID: d.OrgID, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, Subject: planning.ServiceSubject, Name: "api", Provider: "kubernetes", Engine: provider.CtrlPlane, ExternalID: instance.String(), ConnectionID: d.ConnectionID, Revision: image, PlanID: "fixture-plan"}
	binding, err := deployments.FederationWorkloadDeployment(&saved)
	must(t, err)
	selector := domain.WorkloadSelector{Scope: d.Scope, DestinationID: d.ID, ServiceID: binding.ServiceID}
	uri, err := workloadidentity.WorkloadURI("loomplane.test", selector)
	must(t, err)
	selector.ServiceID = "wrong-image"
	wrongURI, err := workloadidentity.WorkloadURI("loomplane.test", selector)
	must(t, err)
	labels := map[string]string{owned.LabelOrg: d.OrgID, owned.LabelProject: d.ProjectID, owned.LabelEnvironment: d.EnvironmentID, owned.LabelInstance: instance.String()}
	revision := id.New(id.PrefixRelease).String()
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: kube.WorkloadName(instance), Namespace: "apps", Labels: labels}, Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: map[string]string{owned.LabelRevision: revision}}, Spec: corev1.PodSpec{AutomountServiceAccountToken: ptr.To(false), Containers: []corev1.Container{{Name: binding.Container, Image: image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/qualification", "-test.run=^TestApplicationWorkloadProbe$", "-test.v"}, Env: []corev1.EnvVar{{Name: "APPLICATION_URI", Value: uri}, {Name: "WRONG_IMAGE_URI", Value: wrongURI}}, VolumeMounts: []corev1.VolumeMount{{Name: "workload", MountPath: "/run/spire/workload", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "workload", VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{Driver: "csi.spiffe.io", ReadOnly: ptr.To(true)}}}}}}}}
	created, err := client.AppsV1().Deployments("apps").Create(ctx, deployment, metav1.CreateOptions{})
	must(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		must(t, client.AppsV1().Deployments("apps").Delete(cleanup, created.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &created.UID}, PropagationPolicy: ptr.To(metav1.DeletePropagationForeground)}))
	})
	verify := fed.VerifyKubernetesWorkload(credentials, admin, "loomplane.test", d.OrgID)
	var application workloadidentity.WorkloadBinding
	var podName string
	var profile fed.KubernetesWorkloadProfile
	wait(ctx, t, "owned application controller and running image", func() error {
		pods, listErr := client.CoreV1().Pods("apps").List(ctx, metav1.ListOptions{LabelSelector: owned.LabelInstance + "=" + instance.String()})
		if listErr != nil {
			return listErr
		}
		if len(pods.Items) != 1 {
			return errors.New("application pod missing")
		}
		pod := pods.Items[0]
		podName = pod.Name
		profile = fed.KubernetesWorkloadProfile{KubernetesAttestationProfile: fed.KubernetesAttestationProfile{Scope: d.Scope, DestinationID: d.ID, ConnectionID: d.ConnectionID, IdentityRevision: d.Identity.Revision(), PodName: pod.Name, PodUID: string(pod.UID), ServiceAccount: pod.Spec.ServiceAccountName, Container: binding.Container, SPIRECluster: "qualification", SPIRENamespace: "spire", SPIREServiceAccount: "spire-agent"}, BindingID: binding.BindingID, DeploymentUID: string(created.UID), DeploymentGeneration: created.Generation, RuntimeRevision: revision, ImageIndexes: imageIndexes}
		var verifyErr error
		application, verifyErr = verify(ctx, d, binding, profile)
		return verifyErr
	})
	application.Binding.EntryID = uuid.NewString()
	application.Binding.TTL = 30 * time.Second
	application.PruneAfter = time.Now().Add(3 * time.Minute).Truncate(time.Second)
	wrong := application
	wrong.Binding.EntryID = uuid.NewString()
	wrong.Workload.ServiceID = "wrong-image"
	wrong.ContainerImage = "localhost/loomplane-psat@sha256:" + strings.Repeat("0", 64)
	_, err = admin.RegisterWorkload(ctx, wrong)
	must(t, err)
	// The fixture created this controller and retained the API response above.
	receipt := kube.ControllerReceipt{Scope: d.Scope, ConnectionID: d.ConnectionID, InstanceID: instance.String(), PlanID: saved.PlanID, RuntimeRef: "application-fixture", ServiceID: saved.Name, Container: binding.Container, Image: image, Namespace: "apps", NamespaceUID: d.Identity.NamespaceUID, ClusterCA: d.Identity.Account, ServiceAccount: application.Binding.ServiceAccount, DeploymentUID: string(created.UID), DeploymentGeneration: created.Generation, RuntimeRevision: revision}
	must(t, kube.NewControllerStore(db).RecordController(ctx, receipt))
	must(t, planning.NewPgStore(db).PutBinding(ctx, &saved))
	application.Binding.EntryID = ""
	journal := workloadidentity.NewWorkloadStore(db, "loomplane.test")
	guard := func(check context.Context) error {
		verified, verifyErr := verify(check, d, binding, profile)
		if verifyErr != nil {
			return verifyErr
		}
		if verified.Binding.ParentURI != application.Binding.ParentURI || verified.ContainerImage != application.ContainerImage {
			return workloadidentity.ErrIdentity
		}
		return nil
	}
	pod, podErr := client.CoreV1().Pods("apps").Get(ctx, podName, metav1.GetOptions{})
	must(t, podErr)
	node, nodeErr := client.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
	must(t, nodeErr)
	record, reserveErr := journal.Reserve(ctx, workloadidentity.WorkloadIntent{BindingID: saved.ID, Controller: receipt, Workload: application, ImageIndexes: imageIndexes, OperatingSystem: node.Status.NodeInfo.OperatingSystem, Architecture: node.Status.NodeInfo.Architecture}, guard)
	if reserveErr != nil {
		t.Fatalf("reserve application journal: %v", reserveErr)
	}
	if issueErr := journal.Issue(ctx, d.OrgID, record.ID, admin, guard); issueErr != nil {
		t.Fatalf("issue application journal: %v", issueErr)
	}
	application.Binding.EntryID = record.Intent.Workload.Binding.EntryID
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		must(t, admin.Revoke(cleanup, application.Binding.EntryID))
		must(t, admin.Revoke(cleanup, wrong.Binding.EntryID))
	})
	probeLog := func(marker string) error {
		logs, logErr := client.CoreV1().Pods("apps").GetLogs(podName, &corev1.PodLogOptions{Container: binding.Container}).DoRaw(ctx)
		if logErr != nil {
			return logErr
		}
		if !strings.Contains(string(logs), marker) {
			return errors.New("application probe pending: " + string(logs))
		}
		return nil
	}
	wait(ctx, t, "application SPIRE identity and image selector", func() error { return probeLog("APPLICATION_IDENTITY_VERIFIED") })
	reopened := workloadidentity.NewWorkloadStore(db, "loomplane.test")
	must(t, reopened.Revoke(ctx, d.OrgID, record.ID, admin))
	retained, readErr := reopened.Get(ctx, d.OrgID, record.ID)
	must(t, readErr)
	if retained.Phase != "draining" || !retained.Settled {
		t.Fatal("journal lost revocation drain", retained)
	}
	wait(ctx, t, "application revocation through Workload API", func() error { return probeLog("APPLICATION_IDENTITY_REVOKED") })
	t.Log("PASS deployment-owned application obtains exact CSI/SPIRE identity through durable journal, wrong image denied and reopened journal revokes with retained drain")
}

func TestApplicationWorkloadProbe(t *testing.T) {
	uri := os.Getenv("APPLICATION_URI")
	if uri == "" {
		t.Skip("runs only in the application deployment")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	var source *workloadidentity.Source
	wait(ctx, t, "application registration", func() error {
		var err error
		source, err = workloadidentity.OpenSource(ctx, workloadAddress, uri)
		return err
	})
	defer func() { must(t, source.Close()) }()
	svid, err := source.GetX509SVID()
	must(t, err)
	if svid.ID.String() != uri || svid.Certificates[0].NotAfter.After(time.Now().Add(31*time.Second)) {
		t.Fatal("application identity or lifetime differs")
	}
	wrong, err := workloadidentity.OpenSource(ctx, workloadAddress, os.Getenv("WRONG_IMAGE_URI"))
	if err == nil {
		must(t, wrong.Close())
		t.Fatal("wrong image selector obtained identity")
	}
	if !strings.Contains(err.Error(), "missing X509-SVID") {
		t.Fatal("image selector denial unproven", err)
	}
	t.Log("APPLICATION_IDENTITY_VERIFIED")
	wait(ctx, t, "application identity revoked", func() error {
		if _, getErr := source.GetX509SVID(); getErr != nil {
			return nil
		}
		return errors.New("identity still available")
	})
	t.Log("APPLICATION_IDENTITY_REVOKED")
	<-ctx.Done()
}
