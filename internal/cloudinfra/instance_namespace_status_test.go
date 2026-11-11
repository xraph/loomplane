package cloudinfra

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
	"github.com/xraph/loomplane/pkg/provider"
)

type namespaceStatusReader struct {
	InstanceNamespaceAuthority
	instance fleet.Instance
	err      error
	calls    int
}

func (r *namespaceStatusReader) GetInstance(context.Context, security.Principal, string) (*fleet.Instance, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	result := r.instance
	return &result, nil
}

type namespaceStatusStore struct {
	ClusterStore
	InstanceNamespaceStore
	record     InstanceNamespaceRecord
	operation  ClusterOperation
	attachment RuntimeAttachment
	reads      int
}

func (s *namespaceStatusStore) GetInstanceNamespaceRecord(_ context.Context, org, operation string) (InstanceNamespaceRecord, error) {
	s.reads++
	if org != s.record.OrgID || operation != s.record.OperationID {
		return InstanceNamespaceRecord{}, ErrNotFound
	}
	return s.record, nil
}
func (s *namespaceStatusStore) GetClusterOperation(_ context.Context, org, cloud, id string) (*ClusterOperation, error) {
	if org != s.operation.OrgID || cloud != s.operation.CloudID || id != s.operation.ID {
		return nil, ErrNotFound
	}
	result := s.operation
	return &result, nil
}
func (s *namespaceStatusStore) GetRuntimeAttachment(_ context.Context, org, id string) (*RuntimeAttachment, error) {
	if org != s.attachment.OrgID || id != s.attachment.ID {
		return nil, ErrNotFound
	}
	result := s.attachment
	return &result, nil
}

func namespaceStatusFixture(t *testing.T) (*Service, *namespaceStatusReader, *namespaceStatusStore, security.Principal, InstanceNamespaceReceiptQuery) {
	t.Helper()
	p := security.Principal{OrgID: "org", UserID: "user"}
	query := InstanceNamespaceReceiptQuery{OperationID: "operation", InstanceID: "instance", Generation: 2, OwnershipEpoch: 1, SnapshotDigest: "snapshot"}
	pins := InstanceNamespacePins{FleetID: "fleet", InstanceID: query.InstanceID, EnvironmentID: "environment", Generation: 1, OwnershipEpoch: 1, SnapshotDigest: "creation"}
	r := &namespaceStatusReader{instance: fleet.Instance{ID: query.InstanceID, FleetID: "fleet", EnvironmentID: "environment", DesiredGeneration: 2, OwnershipEpoch: 1}}
	policy := &provider.InstanceIsolationPolicy{Revision: provider.InstanceIsolationRevision, PodSecurityVersion: "v1.35", CPUMillis: 500, MemoryMiB: 256, EphemeralMiB: 64, MaxPods: 1}
	digest, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	s := &namespaceStatusStore{
		record:     InstanceNamespaceRecord{OrgID: "org", ProjectID: "project", CloudID: "cloud", ClusterID: "cluster", OperationID: query.OperationID, Namespace: "namespace", Pins: pins, CurrentGeneration: 2, CurrentSnapshotDigest: query.SnapshotDigest},
		operation:  ClusterOperation{ID: query.OperationID, OrgID: "org", CloudID: "cloud", ResourceID: "cluster", Kind: InstanceNamespaceKind, Status: "succeeded", Revision: 4, Input: ClusterInput{Attachment: &AttachmentInput{ProjectID: "project", Namespace: "namespace", TrustFingerprint: "ca", Instance: &pins, InstancePolicy: policy}}, Enrollment: &ClusterEnrollment{AttachmentID: "attachment", RuntimeConnectionID: "connection", State: kubernetes.EnrollmentState{NamespaceUID: "uid", ServiceAccountUID: "service-uid", Isolation: &kubernetes.InstanceIsolationState{Digest: digest, Objects: map[string]string{"ResourceQuota": "quota", "LimitRange": "limit", "NetworkPolicy": "network"}}}}},
		attachment: RuntimeAttachment{ID: "attachment", OrgID: "org", ProjectID: "project", CloudID: "cloud", ClusterID: "cluster", Namespace: "namespace", NamespaceUID: "uid", ServiceAccountUID: "service-uid", RuntimeConnectionID: "connection", OperationID: query.OperationID, SchemaVersion: 1, TrustFingerprint: "ca", Revision: 7},
	}
	svc := NewService(Deps{InstanceNamespaces: &InstanceNamespaceTransactions{}})
	svc.InstanceAuthority = r
	svc.Clusters = s
	return svc, r, s, p, query
}

func TestInstanceNamespaceStatusUsesCurrentAttachmentAndScope(t *testing.T) {
	svc, r, _, p, q := namespaceStatusFixture(t)
	got, err := svc.getInstanceNamespaceStatus(t.Context(), p, q)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Receipt.Ready || got.Receipt.AttachmentGeneration != 7 || got.OperationRevision != 4 || got.Status != "succeeded" || r.calls == 0 {
		t.Fatalf("status=%+v", got)
	}
}
func TestInstanceNamespaceStatusRejectsStaleAndForeignPins(t *testing.T) {
	for name, change := range map[string]func(*namespaceStatusStore, *InstanceNamespaceReceiptQuery){
		"generation":  func(_ *namespaceStatusStore, q *InstanceNamespaceReceiptQuery) { q.Generation++ },
		"epoch":       func(_ *namespaceStatusStore, q *InstanceNamespaceReceiptQuery) { q.OwnershipEpoch++ },
		"digest":      func(_ *namespaceStatusStore, q *InstanceNamespaceReceiptQuery) { q.SnapshotDigest = "other" },
		"environment": func(s *namespaceStatusStore, _ *InstanceNamespaceReceiptQuery) { s.record.Pins.EnvironmentID = "other" },
		"fleet":       func(s *namespaceStatusStore, _ *InstanceNamespaceReceiptQuery) { s.record.Pins.FleetID = "other" },
		"operation":   func(s *namespaceStatusStore, _ *InstanceNamespaceReceiptQuery) { s.operation.Kind = "enroll" },
		"namespace uid": func(s *namespaceStatusStore, _ *InstanceNamespaceReceiptQuery) {
			s.attachment.NamespaceUID = "replacement"
		},
		"connection": func(s *namespaceStatusStore, _ *InstanceNamespaceReceiptQuery) {
			s.attachment.RuntimeConnectionID = "other"
		},
		"attachment owner": func(s *namespaceStatusStore, _ *InstanceNamespaceReceiptQuery) { s.attachment.ProjectID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			svc, _, store, p, q := namespaceStatusFixture(t)
			change(store, &q)
			if _, err := svc.getInstanceNamespaceStatus(t.Context(), p, q); err == nil {
				t.Fatal("mismatched receipt accepted")
			}
		})
	}
}
func TestInstanceNamespaceStatusRequiresCurrentReadAccess(t *testing.T) {
	svc, r, store, p, q := namespaceStatusFixture(t)
	r.err = security.ErrForbidden
	if _, err := svc.getInstanceNamespaceStatus(t.Context(), p, q); !errors.Is(err, security.ErrForbidden) {
		t.Fatal(err)
	}
	if store.reads != 0 {
		t.Fatal("denied read loaded namespace")
	}
}
func TestInstanceNamespaceStatusReportsFailureWithoutProviderError(t *testing.T) {
	for _, status := range []string{"accepted", "running", "waiting_provider", "needs_attention", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			svc, _, store, p, q := namespaceStatusFixture(t)
			store.operation.Status = status
			store.operation.Error = "credential-bearing provider error"
			store.operation.CancelRequested = true
			got, err := svc.getInstanceNamespaceStatus(t.Context(), p, q)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != status || got.Receipt.Ready || !got.CancelRequested {
				t.Fatalf("status=%+v", got)
			}
			if status == "failed" || status == "needs_attention" {
				if got.ErrorCode != "namespace_operation_failed" {
					t.Fatal(got.ErrorCode)
				}
			}
		})
	}
}
