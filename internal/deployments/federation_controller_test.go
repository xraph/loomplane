package deployments

import (
	"context"
	"maps"
	"strings"
	"testing"

	ctrlplane "github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/planning"
	domain "github.com/xraph/loomplane/pkg/federation"
	"github.com/xraph/loomplane/pkg/provider"
)

type controllerReceiptFixture struct {
	receipt kube.ControllerReceipt
	calls   int
}

func (f *controllerReceiptFixture) RecordController(context.Context, kube.ControllerReceipt) error {
	return nil
}
func (f *controllerReceiptFixture) GetController(_ context.Context, org, connection, instanceID, ref string) (*kube.ControllerReceipt, error) {
	f.calls++
	if org != f.receipt.OrgID || connection != f.receipt.ConnectionID || instanceID != f.receipt.InstanceID || ref != f.receipt.RuntimeRef {
		return nil, kube.ErrControllerMissing
	}
	r := f.receipt
	return &r, nil
}

func TestFederationControllerUsesCommittedBindingAndAttempt(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	scope := domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}
	image := "registry.example/api@sha256:" + strings.Repeat("a", 64)
	ref := resourceKey("org", "env", "api", "runtime_attempt")
	receipt := kube.ControllerReceipt{Scope: scope, ConnectionID: "connection", InstanceID: iid.String(), PlanID: "plan", RuntimeRef: ref, ServiceID: "api", Container: mainService, Image: image, Namespace: "apps", NamespaceUID: "namespace-uid", ClusterCA: strings.Repeat("b", 64), ServiceAccount: "default", DeploymentUID: "deployment-uid", DeploymentGeneration: 7, RuntimeRevision: "release"}
	binding := planning.Binding{ID: "binding", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "api", Provider: "kubernetes", Engine: provider.CtrlPlane, ExternalID: iid.String(), ConnectionID: "connection", Revision: image, PlanID: "plan"}
	inst := instance.Instance{Entity: ctrlplane.Entity{ID: iid}, TenantID: "org", ProviderName: "kubernetes.connection", State: cpprovider.StateRunning, Services: []cpprovider.ServiceSpec{{Name: mainService, Image: image}}, Labels: map[string]string{owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "env", owned.LabelConnection: "connection", owned.LabelService: "api", owned.LabelPlan: "plan", owned.LabelRuntime: ref}}
	d := domain.Destination{ID: "dest", Scope: scope, ConnectionID: "connection", Identity: domain.PhysicalIdentity{Provider: "kubernetes", Account: receipt.ClusterCA, NamespaceUID: receipt.NamespaceUID, ResourceID: receipt.NamespaceUID}}
	base := federation.KubernetesAttestationProfile{Scope: scope, DestinationID: d.ID, ConnectionID: d.ConnectionID, IdentityRevision: d.Identity.Revision(), PodName: "pod", PodUID: "pod-uid", SPIRECluster: "cluster", SPIRENamespace: "spire", SPIREServiceAccount: "spire-agent"}
	store := &controllerReceiptFixture{receipt: receipt}
	profile, err := LoadFederationController(t.Context(), store, &binding, &inst, d, base)
	if err != nil || profile.DeploymentUID != receipt.DeploymentUID || profile.DeploymentGeneration != 7 || profile.RuntimeRevision != "release" || profile.ServiceAccount != "default" || profile.Container != mainService || profile.BindingID != "binding" {
		t.Fatal("receipt not projected", profile, err)
	}
	for name, change := range map[string]func(*planning.Binding, *instance.Instance, *kube.ControllerReceipt, *domain.Destination){
		"uncommitted plan": func(_ *planning.Binding, i *instance.Instance, _ *kube.ControllerReceipt, _ *domain.Destination) {
			i.Labels[owned.LabelPlan] = "next-plan"
		},
		"unrelated receipt plan": func(_ *planning.Binding, _ *instance.Instance, r *kube.ControllerReceipt, _ *domain.Destination) {
			r.PlanID = "other"
		},
		"stopped": func(_ *planning.Binding, i *instance.Instance, _ *kube.ControllerReceipt, _ *domain.Destination) {
			i.State = cpprovider.StateStopped
		},
		"different instance": func(_ *planning.Binding, i *instance.Instance, _ *kube.ControllerReceipt, _ *domain.Destination) {
			i.ID = id.New(id.PrefixInstance)
		},
		"changed image": func(_ *planning.Binding, i *instance.Instance, _ *kube.ControllerReceipt, _ *domain.Destination) {
			i.Services = []cpprovider.ServiceSpec{{Name: mainService, Image: "changed"}}
		},
		"foreign provider": func(_ *planning.Binding, i *instance.Instance, _ *kube.ControllerReceipt, _ *domain.Destination) {
			i.ProviderName = "kubernetes.other"
		},
		"other attempt": func(_ *planning.Binding, i *instance.Instance, _ *kube.ControllerReceipt, _ *domain.Destination) {
			i.Labels[owned.LabelRuntime] = resourceKey("org", "env", "api", "runtime_other")
		},
		"different cluster": func(_ *planning.Binding, _ *instance.Instance, _ *kube.ControllerReceipt, d *domain.Destination) {
			d.Identity.Account = strings.Repeat("c", 64)
		},
		"replaced namespace": func(_ *planning.Binding, _ *instance.Instance, r *kube.ControllerReceipt, _ *domain.Destination) {
			r.NamespaceUID = "replacement"
		},
		"foreign service": func(_ *planning.Binding, i *instance.Instance, _ *kube.ControllerReceipt, _ *domain.Destination) {
			i.Labels[owned.LabelService] = "neighbor"
		},
	} {
		t.Run(name, func(t *testing.T) {
			b, i, r, dst := binding, inst, receipt, d
			i.Labels = maps.Clone(inst.Labels)
			change(&b, &i, &r, &dst)
			if _, err := LoadFederationController(t.Context(), &controllerReceiptFixture{receipt: r}, &b, &i, dst, base); err == nil {
				t.Fatal("mismatched receipt accepted")
			}
		})
	}
}

func TestKubernetesBackendUsesControllerRecorder(t *testing.T) {
	target := &OwnedTarget{Controllers: &controllerReceiptFixture{}}
	runtime := target.kubernetesBackend("org", "connection").(*kube.Runtime)
	if runtime.RecordController == nil {
		t.Fatal("controller store not wired into runtime")
	}
	target.Controllers = nil
	if target.kubernetesBackend("org", "connection").(*kube.Runtime).RecordController != nil {
		t.Fatal("legacy fixture unexpectedly requires a store")
	}
}
