package kubernetes

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	domain "github.com/xraph/loomplane/pkg/federation"
)

func groupIngressFixture(t *testing.T) (*GroupIngress, domain.MemberReceipt, *fake.Clientset) {
	t.Helper()
	identity, err := domain.NewMemberIdentity("group", 1, "east")
	if err != nil {
		t.Fatal(err)
	}
	receipt := domain.MemberReceipt{Scope: domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "environment"}, Identity: identity, SchemaVersion: 1, ID: "receipt-one", Revision: 1, IngressClass: "nginx", Namespace: "apps", NamespaceUID: "ns-uid", ConnectionID: "connection", ConfigRevision: "config", PrivateService: domain.RuntimeObject{Kind: "Service", Name: "service-one", UID: "service-uid-one", ResourceVersion: "1"}, ServicePort: 8080}
	client := fake.NewClientset(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: receipt.PrivateService.Name, Namespace: receipt.Namespace, UID: types.UID(receipt.PrivateService.UID), ResourceVersion: "1", Labels: groupMemberLabels(receipt)}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 8080}}}})
	client.PrependReactor("create", "ingresses", func(action ktesting.Action) (bool, runtime.Object, error) {
		in := action.(ktesting.CreateAction).GetObject().(*networkingv1.Ingress)
		in.UID, in.ResourceVersion = "stable-uid", "1"
		return false, nil, nil
	})
	client.PrependReactor("update", "ingresses", func(action ktesting.Action) (bool, runtime.Object, error) {
		in := action.(ktesting.UpdateAction).GetObject().(*networkingv1.Ingress)
		old, getErr := client.Tracker().Get(networkingv1.SchemeGroupVersion.WithResource("ingresses"), "apps", in.Name)
		if getErr != nil {
			return true, nil, getErr
		}
		if in.ResourceVersion != old.(*networkingv1.Ingress).ResourceVersion {
			return true, nil, errors.New("resourceVersion conflict")
		}
		revision, parseErr := strconv.Atoi(old.(*networkingv1.Ingress).ResourceVersion)
		if parseErr != nil {
			return true, nil, parseErr
		}
		in.ResourceVersion = strconv.Itoa(revision + 1)
		return false, nil, nil
	})
	adapter := &GroupIngress{Client: &Client{Config: &Config{Namespace: "apps"}, Kube: client}, Guard: func(context.Context, domain.QualifiedGroupPlan, domain.MemberReceipt) error { return nil }, Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	adapter.Group = domain.QualifiedGroupPlan{Scope: receipt.Scope, GroupID: identity.GroupID, Generation: identity.Generation, ConfigRevision: receipt.ConfigRevision, Routing: domain.GroupRoutingReview{Hostname: "api.example.com"}, Members: []domain.GroupMemberPlan{{Identity: identity, ConnectionID: receipt.ConnectionID, Prerequisite: domain.PrerequisitePin{Namespace: receipt.Namespace, NamespaceUID: receipt.NamespaceUID, TLSSecretName: "tls"}}}}
	adapter.Recover = func(context.Context, domain.QualifiedGroupPlan, domain.ServingMemberReceipt, domain.ServingMemberReceipt) error {
		return nil
	}
	return adapter, receipt, client
}

func TestPlacementGroupStableIngressOwnership(t *testing.T) {
	a, first, client := groupIngressFixture(t)
	serving, err := a.Promote(t.Context(), first, domain.ServingMemberReceipt{}, "api.example.com", "tls", "nginx")
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.Identity, err = domain.NewMemberIdentity("group", 2, "east")
	if err != nil {
		t.Fatal(err)
	}
	second.ID = "receipt-two"
	a.Group.Generation = 2
	a.Group.Members[0].Identity = second.Identity
	second.PrivateService = domain.RuntimeObject{Kind: "Service", Name: "service-two", UID: "service-uid-two", ResourceVersion: "1"}
	_, err = client.CoreV1().Services("apps").Create(t.Context(), &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: second.PrivateService.Name, Namespace: "apps", UID: types.UID(second.PrivateService.UID), ResourceVersion: "1", Labels: groupMemberLabels(second)}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 8080}}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := client.NetworkingV1().Ingresses("apps").Get(t.Context(), serving.Ingress.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if before.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name != first.PrivateService.Name {
		t.Fatal("preparing the second Service switched traffic")
	}
	next, err := a.Promote(t.Context(), second, serving, "api.example.com", "tls", "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if next.Ingress.UID != serving.Ingress.UID || next.Ingress.Name != serving.Ingress.Name || next.OldService != first.PrivateService || next.NewService != second.PrivateService || next.Generation != 2 {
		t.Fatalf("incorrect promotion receipt: %+v", next)
	}
	all, err := client.NetworkingV1().Ingresses("apps").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(all.Items) != 1 {
		t.Fatalf("duplicate member Ingress: %v %v", len(all.Items), err)
	}
	if _, err = client.CoreV1().Services("apps").Get(t.Context(), first.PrivateService.Name, metav1.GetOptions{}); err != nil {
		t.Fatal("promotion removed the old Service")
	}
	// A crash after the API accepted the switch must recover the exact receipt.
	a.Recover = nil
	if _, err = a.Promote(t.Context(), second, serving, "api.example.com", "tls", "nginx"); !errors.Is(err, ErrManagedAuthority) {
		t.Fatalf("matching ingress without durable issuance was adopted: %v", err)
	}
	a.Recover = func(_ context.Context, _ domain.QualifiedGroupPlan, old, found domain.ServingMemberReceipt) error {
		if old != serving || found != next {
			return ErrManagedAuthority
		}
		return nil
	}
	recovered, err := a.Promote(t.Context(), second, serving, "api.example.com", "tls", "nginx")
	if err != nil || !reflect.DeepEqual(recovered, next) {
		t.Fatalf("lost switch response did not reconcile: %+v %v", recovered, err)
	}
	// A reviewed rollback has new promotion authority and retains the original
	// backend identity. It does not rename or adopt the old Service.
	a.Group.Generation = 3
	a.Group.Members[0].Identity, err = domain.NewMemberIdentity("group", 3, "east")
	if err != nil {
		t.Fatal(err)
	}
	a.Group.Members[0].RetainedBackend = &domain.RetainedMemberBackend{ReceiptID: first.ID, ReceiptRevision: first.Revision, Identity: first.Identity, ConfigRevision: first.ConfigRevision, SnapshotID: first.SnapshotID}
	rolledBack, err := a.Promote(t.Context(), first, next, "api.example.com", "tls", "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Generation != 3 || rolledBack.BackendIdentity != first.Identity || rolledBack.NewService != first.PrivateService || rolledBack.Ingress.UID != next.Ingress.UID {
		t.Fatalf("rollback fabricated backend identity: %+v", rolledBack)
	}
}

func TestPlacementGroupIngressRefusesRevocationAndRecreatedService(t *testing.T) {
	a, receipt, client := groupIngressFixture(t)
	a.Guard = func(context.Context, domain.QualifiedGroupPlan, domain.MemberReceipt) error {
		return ErrManagedAuthority
	}
	if _, err := a.Promote(t.Context(), receipt, domain.ServingMemberReceipt{}, "api.example.com", "tls", "nginx"); !errors.Is(err, ErrManagedAuthority) {
		t.Fatalf("revoked promotion: %v", err)
	}
	a.Guard = func(context.Context, domain.QualifiedGroupPlan, domain.MemberReceipt) error { return nil }
	receipt.PrivateService.UID = "recreated"
	if _, err := a.Promote(t.Context(), receipt, domain.ServingMemberReceipt{}, "api.example.com", "tls", "nginx"); err == nil {
		t.Fatal("recreated Service accepted")
	}
	for _, action := range client.Actions() {
		if action.GetResource().Resource == "ingresses" && action.GetVerb() != "get" {
			t.Fatalf("unauthorized ingress effect: %+v", action)
		}
	}
}
