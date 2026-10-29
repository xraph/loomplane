package kubernetes

import (
	"context"
	"io"
	"maps"
	"strings"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
)

func selectKata(t *testing.T, c *Client) {
	t.Helper()
	c.Config.Details["build_workers"] = "shared"
	c.Config.Details["build_runtime_class"] = "isolated-builds"
	_, err := c.Kube.NodeV1().RuntimeClasses().Create(t.Context(), &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "isolated-builds", UID: "runtime-uid"}, Handler: "kata-qemu"}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := &Result{Details: maps.Clone(c.Config.Details)}
	c.checkBuildIsolation(t.Context(), out)
	if out.Details["build_isolation"] != "ready" {
		t.Fatal(out.Details)
	}
	c.Config.Details = out.Details
}

func TestBuildIsolationRejectsBeforeCreatingJobOrReadingSource(t *testing.T) {
	for _, change := range []string{"unknown", "shared", "missing", "forbidden", "handler", "replaced", "scheduling", "overhead"} {
		t.Run(change, func(t *testing.T) {
			c := managedRegistryFixture(t)
			registry, err := c.EnsureManagedRegistry(t.Context(), "org", "conn")
			if err != nil {
				t.Fatal(err)
			}
			selectKata(t, c)
			kube := c.Kube.(*fake.Clientset)
			class, err := kube.NodeV1().RuntimeClasses().Get(t.Context(), "isolated-builds", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "unknown", "shared":
				delete(c.Config.Details, "build_runtime_class")
				if change == "unknown" {
					delete(c.Config.Details, "build_workers")
				}
			case "missing":
				err = kube.NodeV1().RuntimeClasses().Delete(t.Context(), class.Name, metav1.DeleteOptions{})
			case "forbidden":
				kube.PrependReactor("get", "runtimeclasses", func(kubetesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "node.k8s.io", Resource: "runtimeclasses"}, class.Name, nil)
				})
			case "handler":
				class.Handler = "runc"
			case "replaced":
				class.UID = "replacement"
			case "scheduling":
				class.Scheduling = &nodev1.Scheduling{NodeSelector: map[string]string{"pool": "different"}}
			case "overhead":
				class.Overhead = &nodev1.Overhead{PodFixed: corev1.ResourceList{}}
			}
			if err != nil {
				t.Fatal(err)
			}
			if change != "missing" {
				if _, err = kube.NodeV1().RuntimeClasses().Update(t.Context(), class, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			kube.ClearActions()
			builder := &ClusterBuilder{Client: c, Exec: func(context.Context, *Client, *corev1.Pod, []string, io.Reader, io.Writer, string) error {
				t.Fatal("untrusted source reached exec")
				return nil
			}}
			_, err = builder.Build(t.Context(), ClusterImageBuild{Org: "org", Connection: "conn", Operation: "blocked", Dockerfile: ".loomplane-build/Dockerfile", Image: registry.Host + "/loomplane/api:" + strings.Repeat("a", 24), Source: strings.NewReader("source")}, registry, io.Discard)
			if err == nil {
				t.Fatal("unsafe build accepted")
			}
			for _, action := range kube.Actions() {
				if action.GetVerb() != "get" {
					t.Fatalf("mutated before isolation check: %s", action.GetVerb())
				}
			}
		})
	}
}

func TestBuildIsolationCheckClearsStaleEvidence(t *testing.T) {
	c := managedRegistryFixture(t)
	selectKata(t, c)
	if err := c.Kube.NodeV1().RuntimeClasses().Delete(t.Context(), "isolated-builds", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	out := &Result{Details: maps.Clone(c.Config.Details)}
	c.checkBuildIsolation(t.Context(), out)
	if out.Details["build_runtime_identity"] != "" || out.Details["build_isolation"] != "unavailable" {
		t.Fatal("retained stale isolation evidence")
	}
}

func TestBuildIsolationSettingsCannotSupplyEvidence(t *testing.T) {
	raw := kubeconfig(t, "https://cluster.example.com", testCA(t))
	for _, settings := range []map[string]string{
		{"build_workers": "trusted"}, {"build_runtime_class": "../kata"},
	} {
		if _, err := ParseConfig(raw, settings); err == nil {
			t.Fatal("invalid settings accepted")
		}
	}
	c, err := ParseConfig(raw, map[string]string{"build_workers": "shared", "build_runtime_class": "isolated-builds", "build_isolation": "ready", "build_runtime_identity": "forged"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Details["build_runtime_class"] != "isolated-builds" || c.Details["build_runtime_identity"] != "" || c.Details["build_isolation"] != "" {
		t.Fatal("caller forged checked runtime evidence")
	}
}

func TestBuildIsolationUnavailableDoesNotDisableNonBuildAccess(t *testing.T) {
	c := managedRegistryFixture(t)
	delete(c.Config.Details, "build_workers")
	out := &Result{Details: maps.Clone(c.Config.Details)}
	for _, key := range []string{"persistent_storage", "route_prerequisites", "jobs"} {
		out.Details[key] = "ready"
	}
	c.Kube.(*fake.Clientset).PrependReactor("create", "selfsubjectaccessreviews", func(kubetesting.Action) (bool, runtime.Object, error) {
		return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	if err := c.checkManagedBuildPermissions(t.Context(), out); err != nil {
		t.Fatal(err)
	}
	if out.Details["managed_builds"] != "unavailable" || out.Details["build_isolation_error"] == "" || len(out.Missing) != 0 {
		t.Fatalf("build policy disabled connection access: %+v", out)
	}
}
