package kubernetes

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kuberuntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
)

func TestClusterBuildApprovedSourceDigestAndCleanup(t *testing.T) {
	for _, failure := range []string{"", "upload", "build", "digest", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			c := managedRegistryFixture(t)
			registry, err := c.EnsureManagedRegistry(t.Context(), "org", "conn")
			if err != nil {
				t.Fatal(err)
			}
			kube := c.Kube.(*fake.Clientset)
			var started *batchv1.Job
			kube.PrependReactor("create", "jobs", func(action kubetesting.Action) (bool, kuberuntime.Object, error) {
				job := action.(kubetesting.CreateAction).GetObject().(*batchv1.Job)
				job.UID = types.UID("job-uid")
				started = job.DeepCopy()
				pod := &corev1.Pod{ObjectMeta: job.Spec.Template.ObjectMeta, Spec: job.Spec.Template.Spec, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
				pod.Name = job.Name + "-pod"
				pod.UID = types.UID("pod-uid")
				pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(job, batchv1.SchemeGroupVersion.WithKind("Job"))}
				return false, nil, kube.Tracker().Create(corev1.SchemeGroupVersion.WithResource("pods"), pod, "selected")
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			builder := &ClusterBuilder{Client: c, Exec: func(_ context.Context, _ *Client, pod *corev1.Pod, args []string, input io.Reader, out io.Writer, _ string) error {
				calls++
				if pod.UID != "pod-uid" {
					t.Fatal("exec did not pin pod identity")
				}
				switch args[0] {
				case "tar":
					raw, e := io.ReadAll(input)
					if e != nil || string(raw) != "approved archive" {
						t.Fatal("wrong source")
					}
					if failure == "upload" {
						return errors.New("upload failed")
					}
				case "sh":
					if args[len(args)-1] != registry.Host+"/loomplane/api:"+strings.Repeat("a", 24) || args[len(args)-2] != "amd64" {
						t.Fatalf("wrong build destination: %v", args)
					}
					if failure == "build" {
						return errors.New("build failed")
					}
					if failure == "cancel" {
						cancel()
						return context.Canceled
					}
				case "cat":
					if failure == "digest" {
						_, _ = io.WriteString(out, `{"containerimage.digest":"latest"}`)
					} else {
						_, _ = io.WriteString(out, `{"containerimage.digest":"sha256:`+strings.Repeat("b", 64)+`"}`)
					}
				}
				return nil
			}}
			digest, err := builder.Build(ctx, ClusterImageBuild{Org: "org", Connection: "conn", Operation: "build-one", Dockerfile: ".loomplane-build/Dockerfile", Image: registry.Host + "/loomplane/api:" + strings.Repeat("a", 24), Source: strings.NewReader("approved archive")}, registry, io.Discard)
			if (err != nil) != (failure != "") {
				t.Fatalf("unexpected result: %s %v", digest, err)
			}
			if failure == "" && (digest != "sha256:"+strings.Repeat("b", 64) || calls != 3) {
				t.Fatalf("missing published digest: %s", digest)
			}
			if started == nil || *started.Spec.BackoffLimit != 0 || *started.Spec.TTLSecondsAfterFinished != 3600 || *started.Spec.ActiveDeadlineSeconds != 1800 {
				t.Fatal("job does not bound retries, lifetime and cleanup")
			}
			podSpec := started.Spec.Template.Spec
			if *podSpec.AutomountServiceAccountToken || *podSpec.SecurityContext.RunAsUser == 0 || podSpec.HostNetwork || podSpec.Containers[0].SecurityContext.Privileged != nil {
				t.Fatal("build has ambient cluster or host privileges")
			}
			jobs, e := kube.BatchV1().Jobs("selected").List(t.Context(), metav1.ListOptions{})
			if e != nil || len(jobs.Items) != 0 {
				t.Fatalf("build job leaked: %v", e)
			}
		})
	}
}
