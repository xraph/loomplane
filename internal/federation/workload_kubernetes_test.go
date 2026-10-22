package federation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/xraph/ctrlplane/id"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func workloadKubernetesFixture(t *testing.T) (*kubernetesWorkloadResolver, domain.Destination, *appsv1.Deployment, *appsv1.ReplicaSet, *corev1.Pod) {
	t.Helper()
	base, d, pod := kubernetesAttestationFixture()
	instance := id.New(id.PrefixInstance)
	binding := WorkloadDeployment{Scope: d.Scope, BindingID: "binding", ServiceID: "api", Container: "main", InstanceID: instance.String(), ConnectionID: d.ConnectionID, Image: "registry.example/api@sha256:" + strings.Repeat("a", 64)}
	labels := map[string]string{owned.LabelOrg: d.OrgID, owned.LabelProject: d.ProjectID, owned.LabelEnvironment: d.EnvironmentID, owned.LabelInstance: instance.String()}
	controller := true
	replicas := int32(1)
	pod.Name, pod.ResourceVersion = "api-pod", "1"
	pod.Labels = maps.Clone(labels)
	pod.Annotations = map[string]string{owned.LabelRevision: "release"}
	pod.Spec.Containers = []corev1.Container{{Name: "main", Image: binding.Image}}
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "api-rs", UID: "rs-uid", Controller: &controller}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", Image: binding.Image, ImageID: binding.Image, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: maps.Clone(labels), Annotations: maps.Clone(pod.Annotations)}, Spec: *pod.Spec.DeepCopy()}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: kube.WorkloadName(instance), Namespace: pod.Namespace, UID: "deployment-uid", ResourceVersion: "1", Generation: 1, Labels: maps.Clone(labels)}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: maps.Clone(labels)}, Template: template}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "api-rs", Namespace: pod.Namespace, UID: "rs-uid", ResourceVersion: "1", Labels: maps.Clone(labels), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Name, UID: deployment.UID, Controller: &controller}}}, Spec: appsv1.ReplicaSetSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: maps.Clone(labels)}, Template: *template.DeepCopy()}}
	if _, err := base.client.Kube.CoreV1().Pods(pod.Namespace).Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := base.client.Kube.AppsV1().Deployments(pod.Namespace).Create(t.Context(), deployment, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := base.client.Kube.AppsV1().ReplicaSets(pod.Namespace).Create(t.Context(), rs, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	base.profile.PodName, base.profile.Container = pod.Name, "main"
	profile := KubernetesWorkloadProfile{KubernetesAttestationProfile: base.profile, BindingID: binding.BindingID, DeploymentUID: string(deployment.UID), DeploymentGeneration: 1, RuntimeRevision: "release"}
	return &kubernetesWorkloadResolver{attestation: base, profile: profile, binding: binding}, d, deployment, rs, pod
}

func TestKubernetesWorkloadResolvesOwnedDeploymentChain(t *testing.T) {
	r, d, _, _, pod := workloadKubernetesFixture(t)
	got, err := r.Resolve(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.WorkloadSelector{Scope: d.Scope, DestinationID: d.ID, ServiceID: "api"}
	if got.Workload != want || got.Binding.PodUID != string(pod.UID) || got.ContainerImage != r.binding.Image || got.Binding.EntryID != "" || !got.PruneAfter.IsZero() {
		t.Fatalf("wrong evidence: %+v", got)
	}
}

func TestKubernetesWorkloadRejectsUnownedOrChangedChain(t *testing.T) {
	for name, change := range map[string]func(*kubernetesWorkloadResolver, *appsv1.Deployment, *appsv1.ReplicaSet, *corev1.Pod){
		"foreign binding": func(r *kubernetesWorkloadResolver, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, _ *corev1.Pod) {
			r.binding.OrgID = "foreign"
		},
		"foreign connection": func(r *kubernetesWorkloadResolver, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, _ *corev1.Pod) {
			r.binding.ConnectionID = "foreign"
		},
		"missing service": func(r *kubernetesWorkloadResolver, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, _ *corev1.Pod) {
			r.binding.ServiceID = ""
		},
		"missing deployment pin": func(r *kubernetesWorkloadResolver, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, _ *corev1.Pod) {
			r.profile.DeploymentUID = ""
		},
		"wrong binding id": func(r *kubernetesWorkloadResolver, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, _ *corev1.Pod) {
			r.profile.BindingID = "other"
		},
		"mutable image": func(r *kubernetesWorkloadResolver, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, _ *corev1.Pod) {
			r.binding.Image = "registry.example/api:latest"
		},
		"replaced deployment": func(_ *kubernetesWorkloadResolver, d *appsv1.Deployment, _ *appsv1.ReplicaSet, _ *corev1.Pod) {
			d.UID = "replacement"
		},
		"changed deployment generation": func(_ *kubernetesWorkloadResolver, d *appsv1.Deployment, _ *appsv1.ReplicaSet, _ *corev1.Pod) {
			d.Generation++
		},
		"deployment label spoof": func(_ *kubernetesWorkloadResolver, d *appsv1.Deployment, _ *appsv1.ReplicaSet, _ *corev1.Pod) {
			d.Labels[owned.LabelInstance] = "neighbor"
		},
		"deployment image changed": func(_ *kubernetesWorkloadResolver, d *appsv1.Deployment, _ *appsv1.ReplicaSet, _ *corev1.Pod) {
			d.Spec.Template.Spec.Containers[0].Image = "neighbor"
		},
		"deployment paused": func(_ *kubernetesWorkloadResolver, d *appsv1.Deployment, _ *appsv1.ReplicaSet, _ *corev1.Pod) {
			d.Spec.Paused = true
		},
		"replica set adopted": func(_ *kubernetesWorkloadResolver, _ *appsv1.Deployment, rs *appsv1.ReplicaSet, _ *corev1.Pod) {
			rs.OwnerReferences[0].UID = "neighbor"
		},
		"replica set revision changed": func(_ *kubernetesWorkloadResolver, _ *appsv1.Deployment, rs *appsv1.ReplicaSet, _ *corev1.Pod) {
			rs.Spec.Template.Annotations[owned.LabelRevision] = "old-release"
		},
		"replica set service account changed": func(_ *kubernetesWorkloadResolver, _ *appsv1.Deployment, rs *appsv1.ReplicaSet, _ *corev1.Pod) {
			rs.Spec.Template.Spec.ServiceAccountName = "neighbor"
		},
		"pod adopted": func(_ *kubernetesWorkloadResolver, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, p *corev1.Pod) {
			p.OwnerReferences[0].UID = "neighbor"
		},
		"noncontroller owner": func(_ *kubernetesWorkloadResolver, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, p *corev1.Pod) {
			p.OwnerReferences[0].Controller = nil
		},
		"ambiguous owners": func(_ *kubernetesWorkloadResolver, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, p *corev1.Pod) {
			p.OwnerReferences = append(p.OwnerReferences, p.OwnerReferences[0])
		},
		"pod image changed": func(_ *kubernetesWorkloadResolver, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, p *corev1.Pod) {
			p.Spec.Containers[0].Image = "neighbor"
		},
		"pod image status changed": func(_ *kubernetesWorkloadResolver, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, p *corev1.Pod) {
			p.Status.ContainerStatuses[0].ImageID = "neighbor"
		},
		"pod image id missing": func(_ *kubernetesWorkloadResolver, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, p *corev1.Pod) {
			p.Status.ContainerStatuses[0].ImageID = ""
		},
		"pod container stopped": func(_ *kubernetesWorkloadResolver, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, p *corev1.Pod) {
			p.Status.ContainerStatuses[0].State.Running = nil
		},
		"pod revision changed": func(_ *kubernetesWorkloadResolver, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, p *corev1.Pod) {
			p.Annotations[owned.LabelRevision] = "old-release"
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, d, deployment, rs, pod := workloadKubernetesFixture(t)
			change(r, deployment, rs, pod)
			if _, err := r.attestation.client.Kube.AppsV1().Deployments(pod.Namespace).Update(t.Context(), deployment, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.attestation.client.Kube.AppsV1().ReplicaSets(pod.Namespace).Update(t.Context(), rs, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.attestation.client.Kube.CoreV1().Pods(pod.Namespace).Update(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Resolve(t.Context(), d); err == nil {
				t.Fatal("untrusted application accepted")
			}
		})
	}
}

func TestKubernetesWorkloadRejectsChangeDuringAttestation(t *testing.T) {
	r, d, _, _, _ := workloadKubernetesFixture(t)
	client := r.attestation.client.Kube.(*fake.Clientset)
	calls := 0
	client.PrependReactor("get", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.GetAction).GetName() != r.profile.PodName {
			return false, nil, nil
		}
		calls++
		if calls == 2 {
			return true, nil, errors.New("pod replaced during verification")
		}
		return false, nil, nil
	})
	if _, err := r.Resolve(t.Context(), d); err == nil {
		t.Fatal("stale pod evidence accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	client.ClearActions()
	if _, err := r.Resolve(ctx, d); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(client.Actions()) != 0 {
		t.Fatal("cancelled verification contacted Kubernetes")
	}
}

func TestKubernetesWorkloadRejectsResourceVersionRaces(t *testing.T) {
	for _, resource := range []string{"deployments", "replicasets", "pods"} {
		t.Run(resource, func(t *testing.T) {
			r, d, deployment, rs, pod := workloadKubernetesFixture(t)
			client := r.attestation.client.Kube.(*fake.Clientset)
			calls := 0
			client.PrependReactor("get", resource, func(a k8stesting.Action) (bool, runtime.Object, error) {
				if resource == "pods" && a.(k8stesting.GetAction).GetName() != pod.Name {
					return false, nil, nil
				}
				calls++
				if calls != 2 {
					return false, nil, nil
				}
				switch resource {
				case "deployments":
					object := deployment.DeepCopy()
					object.ResourceVersion = "2"
					return true, object, nil
				case "replicasets":
					object := rs.DeepCopy()
					object.ResourceVersion = "2"
					return true, object, nil
				default:
					object := pod.DeepCopy()
					object.ResourceVersion = "2"
					return true, object, nil
				}
			})
			if _, err := r.Resolve(t.Context(), d); err == nil {
				t.Fatal("changing ownership snapshot accepted")
			}
		})
	}
}

func TestKubernetesWorkloadSupportsDefaultAccountAndDigestImageID(t *testing.T) {
	r, d, deployment, rs, pod := workloadKubernetesFixture(t)
	r.profile.ServiceAccount = "default"
	r.attestation.profile.ServiceAccount = "default"
	deployment.Spec.Template.Spec.ServiceAccountName = ""
	rs.Spec.Template.Spec.ServiceAccountName = ""
	pod.Spec.ServiceAccountName = "default"
	pod.Status.ContainerStatuses[0].Image = "registry.example/api:cached-name"
	pod.Status.ContainerStatuses[0].ImageID = r.binding.Image
	if _, err := r.attestation.client.Kube.AppsV1().Deployments(pod.Namespace).Update(t.Context(), deployment, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.attestation.client.Kube.AppsV1().ReplicaSets(pod.Namespace).Update(t.Context(), rs, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.attestation.client.Kube.CoreV1().Pods(pod.Namespace).Update(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(t.Context(), d); err != nil {
		t.Fatal(err)
	}
}

func TestKubernetesWorkloadRequiresProofForResolvedImage(t *testing.T) {
	r, d, deployment, rs, pod := workloadKubernetesFixture(t)
	digest := "sha256:" + strings.Repeat("b", 64)
	index := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":1234,"platform":{"os":"linux","architecture":"arm64"}}]}`, digest))
	sum := sha256.Sum256(index)
	r.binding.Image = "registry.example/api@sha256:" + hex.EncodeToString(sum[:])
	deployment.Spec.Template.Spec.Containers[0].Image = r.binding.Image
	rs.Spec.Template.Spec.Containers[0].Image = r.binding.Image
	pod.Spec.Containers[0].Image = r.binding.Image
	pod.Status.ContainerStatuses[0].Image = r.binding.Image
	pod.Status.ContainerStatuses[0].ImageID = "runtime.example/import@" + digest
	if _, err := r.attestation.client.Kube.AppsV1().Deployments(pod.Namespace).Update(t.Context(), deployment, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.attestation.client.Kube.AppsV1().ReplicaSets(pod.Namespace).Update(t.Context(), rs, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.attestation.client.Kube.CoreV1().Pods(pod.Namespace).Update(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	node, err := r.attestation.client.Kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Status.NodeInfo.OperatingSystem = "linux"
	node.Status.NodeInfo.Architecture = "arm64"
	if _, updateErr := r.attestation.client.Kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); updateErr != nil {
		t.Fatal(updateErr)
	}
	if _, verifyErr := r.Resolve(t.Context(), d); verifyErr == nil {
		t.Fatal("unproved platform digest accepted")
	}
	r.profile.ImageIndexes = [][]byte{index}
	got, err := r.Resolve(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContainerImage != pod.Status.ContainerStatuses[0].ImageID {
		t.Fatal("runtime image selector not retained")
	}
	node.Status.NodeInfo.Architecture = "amd64"
	if _, updateErr := r.attestation.client.Kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); updateErr != nil {
		t.Fatal(updateErr)
	}
	if _, verifyErr := r.Resolve(t.Context(), d); verifyErr == nil {
		t.Fatal("wrong platform proof accepted")
	}
}
