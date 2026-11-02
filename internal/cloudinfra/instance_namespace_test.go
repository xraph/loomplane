package cloudinfra

import (
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestInstanceNamespaceStableIdentity(t *testing.T) {
	a, ns := instanceNamespaceIdentity("org", "instance", 1)
	b, ns2 := instanceNamespaceIdentity("org", "instance", 1)
	if a != b || ns != ns2 || len(ns) > 63 {
		t.Fatal("retry changed namespace identity")
	}
	for _, pair := range [][2]string{{"other", "instance"}, {"org", "other"}} {
		id, _ := instanceNamespaceIdentity(pair[0], pair[1], 1)
		if id == a {
			t.Fatal("namespace identity crossed owner")
		}
	}
	id, _ := instanceNamespaceIdentity("org", "instance", 2)
	if id == a {
		t.Fatal("epoch reused namespace identity")
	}
}

func TestInstanceIsolationPolicyUnavailable(t *testing.T) {
	s := NewService(Deps{})
	if _, err := s.ReserveInstanceNamespace(t.Context(), security.Principal{}, InstanceNamespaceInput{}); err == nil {
		t.Fatal("namespace accepted without isolation authority")
	}
	r := ManagedResource{ID: "cluster", Incarnation: "physical", Revision: 1, Observation: &provider.ClusterObservation{CAHash: "ca", Endpoint: "https://cluster.example.test"}}
	a := fleets.OperationAuthority{Request: fleets.OperationRequest{Demand: fleets.OperationDemand{CPUMillis: 500, MemoryMiB: 256, StorageGiB: 1}}}
	q := NamespaceQualification{ID: "evidence", ClusterID: r.ID, ClusterCA: "ca", Incarnation: r.Incarnation, ClusterRevision: 1, EndpointFingerprint: endpointFingerprint(r.Observation.Endpoint), ExpiresAt: time.Now().Add(time.Minute), Policy: provider.InstanceIsolationPolicy{Revision: provider.InstanceIsolationRevision, PodSecurityVersion: "v1.35", CPUMillis: 500, MemoryMiB: 256, EphemeralMiB: 64, MaxPods: 1}}
	if err := validateNamespaceQualification(q, r, a); err != nil {
		t.Fatal(err)
	}
	q.Policy.CPUMillis++
	if err := validateNamespaceQualification(q, r, a); err == nil {
		t.Fatal("namespace bypassed grant ceiling")
	}
}

func TestInstanceNamespaceReceiptRequiresPolicyProof(t *testing.T) {
	policy := &provider.InstanceIsolationPolicy{Revision: provider.InstanceIsolationRevision, PodSecurityVersion: "v1.35", CPUMillis: 500, MemoryMiB: 256, EphemeralMiB: 64, MaxPods: 1}
	o := &ClusterOperation{ID: "operation", Kind: InstanceNamespaceKind, Status: "succeeded", Input: ClusterInput{Attachment: &AttachmentInput{Namespace: "instance", Instance: &InstanceNamespacePins{}, InstancePolicy: policy}}, Enrollment: &ClusterEnrollment{AttachmentID: "attachment", RuntimeConnectionID: "connection", State: kubernetes.EnrollmentState{NamespaceUID: "uid"}}}
	if _, err := instanceNamespaceReceipt(o); err == nil {
		t.Fatal("ready receipt had no isolation receipt")
	}
	digest, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	o.Enrollment.State.Isolation = &kubernetes.InstanceIsolationState{Digest: digest, Objects: map[string]string{"ResourceQuota": "quota", "LimitRange": "limits", "NetworkPolicy": "policy"}}
	receipt, err := instanceNamespaceReceipt(o)
	if err != nil || !receipt.Ready || receipt.NamespaceUID != "uid" {
		t.Fatal(receipt, err)
	}
}

func TestInstanceNamespaceRejectsOrdinaryAuthority(t *testing.T) {
	s := NewService(Deps{})
	if _, _, err := s.clusterAuthority(t.Context(), security.Principal{}, "cloud", ClusterInput{Kind: InstanceNamespaceKind}); err == nil {
		t.Fatal("instance operation accepted ordinary authority")
	}
}
