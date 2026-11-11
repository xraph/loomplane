package cloudinfra

import (
	"context"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

// InstanceNamespaceReceiptQuery pins the instance version a worker is observing.
// Organization scope comes from the currently authenticated principal.
type InstanceNamespaceReceiptQuery struct {
	OperationID    string
	InstanceID     string
	Generation     int64
	OwnershipEpoch int64
	SnapshotDigest string
}

// InstanceNamespaceStatus describes persisted progress. Effect authorization and
// live runtime qualification happen separately before a worker changes resources.
type InstanceNamespaceStatus struct {
	KnownOutcome      *NamespaceKnownOutcome
	Receipt           InstanceNamespaceReceipt
	Status            string
	OperationRevision int64
	ErrorCode         string
	CancelRequested   bool
}

type instanceNamespaceReader interface {
	GetInstance(context.Context, security.Principal, string) (*fleet.Instance, error)
}

// GetInstanceNamespaceReceipt reads recovery progress through current Fleet read
// permissions. It preserves failure visibility after a provisioning grant expires.
func (s *Service) GetInstanceNamespaceReceipt(ctx context.Context, p security.Principal, in InstanceNamespaceReceiptQuery) (*InstanceNamespaceStatus, error) {
	ctx, finish, err := s.InstanceNamespaces.beginNamespaceCall(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	status, err := s.getInstanceNamespaceStatus(ctx, p, in)
	if err != nil {
		return nil, err
	}
	if _, found, err := s.InstanceNamespaces.readOriginalReservation(ctx, p.OrgID, in.OperationID); err != nil {
		return nil, err
	} else if !found {
		return nil, ErrConflict
	}
	return status, nil
}

func (s *Service) getInstanceNamespaceStatus(ctx context.Context, p security.Principal, in InstanceNamespaceReceiptQuery) (*InstanceNamespaceStatus, error) {
	if p.OrgID == "" {
		return nil, security.ErrNoOrganization
	}
	if in.OperationID == "" || in.InstanceID == "" || in.Generation < 1 || in.OwnershipEpoch < 1 || in.SnapshotDigest == "" {
		return nil, ErrInvalid
	}
	reader, ok := s.InstanceAuthority.(instanceNamespaceReader)
	store, stores := s.Clusters.(InstanceNamespaceStore)
	if !ok || !stores {
		return nil, security.ErrForbidden
	}
	instance, err := reader.GetInstance(ctx, p, in.InstanceID)
	if err != nil {
		return nil, err
	}
	if instance == nil || instance.ID != in.InstanceID || instance.DesiredGeneration != in.Generation || instance.OwnershipEpoch != in.OwnershipEpoch {
		return nil, ErrConflict
	}
	record, err := store.GetInstanceNamespaceRecord(ctx, p.OrgID, in.OperationID)
	if err != nil {
		return nil, err
	}
	if record.OrgID != p.OrgID || record.OperationID != in.OperationID || record.Pins.InstanceID != in.InstanceID || record.Pins.FleetID != instance.FleetID || record.Pins.EnvironmentID != instance.EnvironmentID || record.Pins.OwnershipEpoch != in.OwnershipEpoch || record.CurrentGeneration != in.Generation || record.CurrentSnapshotDigest != in.SnapshotDigest {
		return nil, ErrConflict
	}
	operation, err := s.Clusters.GetClusterOperation(ctx, p.OrgID, record.CloudID, in.OperationID)
	if err != nil {
		return nil, err
	}
	if operation == nil || operation.ID != record.OperationID || operation.OrgID != record.OrgID || operation.CloudID != record.CloudID || operation.ResourceID != record.ClusterID || operation.Kind != InstanceNamespaceKind || operation.Revision < 1 || operation.Input.Attachment == nil {
		return nil, ErrConflict
	}
	attachmentInput := operation.Input.Attachment
	if attachmentInput.Instance == nil || *attachmentInput.Instance != record.Pins || attachmentInput.ProjectID != record.ProjectID || attachmentInput.Namespace != record.Namespace {
		return nil, ErrConflict
	}
	receipt, err := instanceNamespaceReceipt(operation)
	if err != nil {
		return nil, err
	}
	result := &InstanceNamespaceStatus{Receipt: *receipt, Status: operation.Status, OperationRevision: operation.Revision, CancelRequested: operation.CancelRequested}
	if reader, ok := s.Clusters.(interface {
		GetInstanceNamespaceKnownOutcome(context.Context, string, string, int64, int64) (*NamespaceKnownOutcome, error)
	}); ok {
		result.KnownOutcome, err = reader.GetInstanceNamespaceKnownOutcome(ctx, p.OrgID, in.OperationID, in.Generation, in.OwnershipEpoch)
		if err != nil {
			return nil, err
		}
	}

	switch operation.Status {
	case "accepted", "running", "waiting_provider", "cancelled":
	case "failed", "needs_attention":
		result.ErrorCode = "namespace_operation_failed"
	case "succeeded":
		attachment, err := s.Clusters.GetRuntimeAttachment(ctx, p.OrgID, receipt.AttachmentID)
		if err != nil {
			return nil, err
		}
		if attachment == nil || attachment.ID != receipt.AttachmentID || attachment.OrgID != record.OrgID || attachment.ProjectID != record.ProjectID || attachment.CloudID != record.CloudID || attachment.ClusterID != record.ClusterID || attachment.OperationID != record.OperationID || attachment.Namespace != record.Namespace || attachment.NamespaceUID != receipt.NamespaceUID || attachment.ServiceAccountUID != operation.Enrollment.State.ServiceAccountUID || attachment.RuntimeConnectionID != receipt.ConnectionID || attachment.TrustFingerprint != receipt.ClusterCA || attachment.Revision < 1 || attachment.SchemaVersion != 1 || attachment.DeletedAt != nil || attachment.RetirementOperationID != "" {
			return nil, ErrConflict
		}
		result.Receipt.AttachmentGeneration = attachment.Revision
		result.Receipt.Ready = !operation.CancelRequested
	default:
		return nil, ErrConflict
	}
	return result, nil
}
