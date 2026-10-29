package kubernetes

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

func TestRuntimeRecordsControllerAfterReadiness(t *testing.T) {
	for _, fail := range []string{"", "readiness", "record", "managed"} {
		t.Run(fail, func(t *testing.T) {
			iid := id.New(id.PrefixInstance)
			client := fake.NewClientset()
			client.PrependReactor("create", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
				d := a.(ktesting.CreateAction).GetObject().(*appsv1.Deployment)
				d.UID = "controller-uid"
				d.Generation = 3
				d.ResourceVersion = "7"
				return false, nil, nil
			})
			cfg := &Config{ManagedWorkload: fail == "managed", Namespace: "apps", Details: map[string]string{"namespace_uid": "namespace-uid", "cluster_ca": strings.Repeat("a", 64)}}
			labels := map[string]string{owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "env", owned.LabelConnection: "connection", owned.LabelService: "api", owned.LabelPlan: "plan", owned.LabelRuntime: "runtime-ref", owned.LabelRevision: "release"}
			image := "registry.example/api@sha256:" + strings.Repeat("b", 64)
			ready := false
			records := 0
			r := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
				return &Client{Config: cfg, Kube: client}, nil
			}, WaitReady: func(context.Context, *Client, string) error {
				ready = true
				if fail == "readiness" {
					return errors.New("not ready")
				}
				return nil
			}, RecordController: func(_ context.Context, receipt ControllerReceipt) error {
				records++
				if fail == "managed" && receipt.ReleaseID != WorkloadReleaseID(labels[owned.LabelRuntime], labels[owned.LabelRevision]) {
					t.Fatal("managed release marker absent")
				}
				if fail != "managed" && receipt.ReleaseID != "" {
					t.Fatal("static receipt changed")
				}
				if !ready || !receipt.Valid() || receipt.InstanceID != iid.String() || receipt.DeploymentUID != "controller-uid" || receipt.DeploymentGeneration != 3 || receipt.PlanID != "plan" || receipt.RuntimeRef != "runtime-ref" || receipt.Image != image || receipt.Container != "main" || receipt.ServiceID != "api" || receipt.ServiceAccount != "default" || receipt.ClusterCA != cfg.Details["cluster_ca"] {
					t.Fatalf("incorrect receipt: %+v", receipt)
				}
				if fail == "record" {
					return errors.New("database unavailable")
				}
				return nil
			}}
			_, err := r.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: image}, labels)
			if (err != nil) != (fail != "" && fail != "managed") {
				t.Fatal(err)
			}
			expected := 1
			if fail == "readiness" {
				expected = 0
			}
			if records != expected {
				t.Fatal("recording order", records)
			}
			if fail != "" && fail != "managed" {
				if _, getErr := client.AppsV1().Deployments("apps").Get(t.Context(), WorkloadName(iid), metav1.GetOptions{}); getErr == nil {
					t.Fatal("failed operation did not roll back")
				}
			}
		})
	}
}
