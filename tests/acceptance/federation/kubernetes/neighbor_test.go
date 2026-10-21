package kubernetes_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

func proveNeighborDenied(ctx context.Context, t *testing.T, client kubernetes.Interface, account string) {
	t.Helper()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "neighbor", Namespace: "apps"}, Spec: corev1.PodSpec{ServiceAccountName: account, AutomountServiceAccountToken: ptr.To(false), RestartPolicy: corev1.RestartPolicyNever,
		Containers: []corev1.Container{{Name: "agent", Image: os.Getenv("AGENT_IMAGE"), ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/qualification", "-test.run=^TestNeighborIdentityDenied$", "-test.v"}, Env: []corev1.EnvVar{{Name: "LOOMPLANE_NEIGHBOR_PROBE", Value: "1"}}, VolumeMounts: []corev1.VolumeMount{{Name: "workload", MountPath: "/run/spire/workload", ReadOnly: true}}}},
		Volumes:    []corev1.Volume{{Name: "workload", VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{Driver: "csi.spiffe.io", ReadOnly: ptr.To(true)}}}},
	}}
	_, err := client.CoreV1().Pods("apps").Create(ctx, pod, metav1.CreateOptions{})
	must(t, err)
	wait(ctx, t, "neighbor attestation denial", func() error {
		observed, getErr := client.CoreV1().Pods("apps").Get(ctx, "neighbor", metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		if observed.Status.Phase == corev1.PodFailed {
			t.Fatal("neighbor probe failed")
		}
		if observed.Status.Phase != corev1.PodSucceeded {
			return errors.New("neighbor probe incomplete")
		}
		return nil
	})
	logs, err := client.CoreV1().Pods("apps").GetLogs("neighbor", &corev1.PodLogOptions{Container: "agent"}).DoRaw(ctx)
	must(t, err)
	if !strings.Contains(string(logs), "--- PASS: TestNeighborIdentityDenied (") {
		t.Fatal("neighbor probe did not execute its denial check")
	}
	must(t, client.CoreV1().Pods("apps").Delete(ctx, "neighbor", metav1.DeleteOptions{}))
	t.Log("PASS same-account neighboring pod denied by real Workload API attestation")
}
func TestNeighborIdentityDenied(t *testing.T) {
	if os.Getenv("LOOMPLANE_NEIGHBOR_PROBE") != "1" {
		t.Skip("probe runs in the neighboring pod")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	client, err := workloadapi.New(ctx, workloadapi.WithAddr(workloadAddress))
	must(t, err)
	defer func() { must(t, client.Close()) }()
	_, err = client.FetchX509SVID(ctx)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected explicit attestation denial from live socket, got %v", err)
	}
}
