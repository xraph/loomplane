package federation

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestWorkloadEvidenceFetchesAfterOwnershipAndRechecks(t *testing.T) {
	for _, scenario := range []string{"valid", "unowned", "changed pod", "changed node", "expired parent", "bad proof", "oversized proof", "cancelled", "fetch denied"} {
		t.Run(scenario, func(t *testing.T) {
			r, d, deployment, rs, pod := workloadKubernetesFixture(t)
			digest := "sha256:" + strings.Repeat("b", 64)
			raw := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":1234,"platform":{"os":"linux","architecture":"arm64"}}]}`, digest))
			r.binding.Image = fmt.Sprintf("registry.example/api@sha256:%x", sha256.Sum256(raw))
			deployment.Spec.Template.Spec.Containers[0].Image = r.binding.Image
			rs.Spec.Template.Spec.Containers[0].Image = r.binding.Image
			pod.Spec.Containers[0].Image = r.binding.Image
			pod.Status.ContainerStatuses[0].ImageID = "runtime.example/api@" + digest
			if scenario == "unowned" {
				deployment.UID = "replacement"
			}
			kube := r.attestation.client.Kube
			if _, err := kube.AppsV1().Deployments(pod.Namespace).Update(t.Context(), deployment, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := kube.AppsV1().ReplicaSets(pod.Namespace).Update(t.Context(), rs, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := kube.CoreV1().Pods(pod.Namespace).Update(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			node.Status.NodeInfo.OperatingSystem = "linux"
			node.Status.NodeInfo.Architecture = "arm64"
			if _, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			r.images = func(check context.Context, b WorkloadDeployment, reported, os, arch string) ([][]byte, error) {
				calls++
				if check.Err() != nil || b != r.binding || reported != pod.Status.ContainerStatuses[0].ImageID || os != "linux" || arch != "arm64" {
					t.Fatal("untrusted image input")
				}
				switch scenario {
				case "changed pod":
					changedPod := pod.DeepCopy()
					changedPod.ResourceVersion = "2"
					if _, err = kube.CoreV1().Pods(pod.Namespace).Update(check, changedPod, metav1.UpdateOptions{}); err != nil {
						t.Fatal(err)
					}
				case "changed node":
					node.Status.NodeInfo.Architecture = "amd64"
					if _, err = kube.CoreV1().Nodes().Update(check, node, metav1.UpdateOptions{}); err != nil {
						t.Fatal(err)
					}
				case "expired parent":
					r.attestation.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
				case "bad proof":
					return [][]byte{[]byte(`{}`)}, nil
				case "oversized proof":
					return [][]byte{make([]byte, 256*1024+1)}, nil
				case "cancelled":
					cancel()
				case "fetch denied":
					return nil, ErrUnverified
				}
				return [][]byte{raw}, nil
			}
			evidence, err := r.ResolveEvidence(ctx, d)
			if scenario != "valid" {
				if err == nil || evidence.Workload.ContainerImage != "" {
					t.Fatal("unsafe evidence returned", err)
				}
				if scenario == "unowned" && calls != 0 {
					t.Fatal("unowned workload reached registry")
				}
				return
			}
			if err != nil || calls != 1 || evidence.Workload.ContainerImage != pod.Status.ContainerStatuses[0].ImageID || evidence.OperatingSystem != "linux" || evidence.Architecture != "arm64" || len(evidence.ImageIndexes) != 1 {
				t.Fatal("incomplete evidence", err, evidence)
			}
			raw[0] = '!'
			if evidence.ImageIndexes[0][0] == '!' || len(r.profile.ImageIndexes) != 0 {
				t.Fatal("evidence aliases loader or mutates retained profile")
			}
		})
	}
}
