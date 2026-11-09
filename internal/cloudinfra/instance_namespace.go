package cloudinfra

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

const InstanceNamespaceKind = "instance-namespace"

type InstanceNamespaceInput struct {
	Authority fleets.OperationAuthority
	Snapshot  fleets.InstanceSnapshot
	ClusterID string
}

// InstanceNamespacePins are immutable creation identity. Current authority is
// kept separately so later operations can renew access without rewriting history.
type InstanceNamespacePins struct {
	FleetID         string `json:"fleet_id"`
	InstanceID      string `json:"instance_id"`
	EnvironmentID   string `json:"environment_id"`
	Generation      int64  `json:"generation"`
	OwnershipEpoch  int64  `json:"ownership_epoch"`
	SnapshotDigest  string `json:"snapshot_digest"`
	QualificationID string `json:"qualification_id"`
}

type InstanceNamespaceReceipt struct {
	OperationID, Namespace, NamespaceUID, ClusterCA, AttachmentID, ConnectionID, PolicyDigest string
	AttachmentGeneration                                                                      int64
	Ready                                                                                     bool
}

type InstanceNamespaceRecord struct {
	CurrentGeneration                                            int64
	CurrentSnapshotDigest                                        string
	OrgID, ProjectID, CloudID, ClusterID, OperationID, Namespace string
	Pins                                                         InstanceNamespacePins
	CreationAuthority                                            fleets.OperationAuthority
	CurrentAuthority                                             fleets.OperationAuthority
}

// InstanceNamespaceAuthority uses saved Fleet grants, never a manufactured user.
type InstanceNamespaceAuthority interface {
	VerifyInstanceSnapshot(context.Context, fleets.OperationAuthority, fleets.InstanceSnapshot) (*fleets.InstanceSnapshot, error)
	RevalidateAuthority(context.Context, fleets.OperationAuthority) error
	RevalidateAuthorityTx(context.Context, driver.Tx, fleets.OperationAuthority) error
}

// NamespaceQualification is returned only by a server-side qualification reader.
// Its network evidence must come from active allow/deny probes on this cluster.
type NamespaceQualification struct {
	ID                                                     string
	ClusterID, ClusterCA, Incarnation, EndpointFingerprint string
	ClusterRevision                                        int64
	ExpiresAt                                              time.Time
	Policy                                                 provider.InstanceIsolationPolicy
	Prerequisites                                          AttachmentPrerequisites
}

type InstanceNamespaceQualifier interface {
	QualifyInstanceNamespace(context.Context, fleets.OperationAuthority, fleets.InstanceSnapshot, ManagedResource) (NamespaceQualification, error)
	RevalidateInstanceNamespaceQualification(context.Context, InstanceNamespaceRecord, *ClusterOperation) error
}

type InstanceNamespaceStore interface {
	ReserveInstanceNamespaceOperation(context.Context, InstanceNamespaceRecord, *ClusterOperation, func(context.Context, driver.Tx) error) (*ClusterOperation, error)
	GetInstanceNamespaceRecord(context.Context, string, string) (InstanceNamespaceRecord, error)
	CheckInstanceNamespaceRecord(context.Context, InstanceNamespaceRecord) error
	AdvanceInstanceNamespaceAuthority(context.Context, InstanceNamespaceRecord, InstanceNamespaceRecord, func(context.Context, driver.Tx) error) error
}

// RenewInstanceNamespaceAuthority replaces current authority while retaining the
// original namespace, policy and physical cluster receipts.
func (s *Service) RenewInstanceNamespaceAuthority(ctx context.Context, p security.Principal, in InstanceNamespaceInput) (*InstanceNamespaceReceipt, error) {
	store, ok := s.Clusters.(InstanceNamespaceStore)
	if !ok || s.InstanceAuthority == nil || s.InstanceIsolation == nil || p.ReadOnly || !p.IsFleet() || !reflect.DeepEqual(p, in.Authority.Principal) {
		return nil, security.ErrForbidden
	}
	if err := s.clusterEnabled(); err != nil {
		return nil, err
	}
	snapshot, err := s.InstanceAuthority.VerifyInstanceSnapshot(ctx, in.Authority, in.Snapshot)
	if err != nil {
		return nil, err
	}
	id, _ := instanceNamespaceIdentity(p.OrgID, snapshot.InstanceID, snapshot.OwnershipEpoch)
	prior, err := store.GetInstanceNamespaceRecord(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if prior.ClusterID != in.ClusterID || prior.CloudID != in.Authority.Request.CloudID || prior.ProjectID != in.Authority.Scope.ProjectID || prior.Pins.FleetID != snapshot.FleetID || prior.Pins.InstanceID != snapshot.InstanceID || prior.Pins.EnvironmentID != snapshot.EnvironmentID || prior.Pins.OwnershipEpoch != snapshot.OwnershipEpoch || snapshot.Generation < prior.CurrentGeneration {
		return nil, ErrConflict
	}
	operation, err := s.Clusters.GetClusterOperation(ctx, p.OrgID, prior.CloudID, id)
	if err != nil {
		return nil, err
	}
	if operation.Kind != InstanceNamespaceKind || operation.Status != "succeeded" || operation.CancelRequested {
		return nil, ErrConflict
	}
	next := prior
	next.CurrentAuthority = in.Authority
	next.CurrentGeneration = snapshot.Generation
	next.CurrentSnapshotDigest = snapshot.Digest
	if checkErr := s.InstanceIsolation.RevalidateInstanceNamespaceQualification(ctx, next, operation); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := store.AdvanceInstanceNamespaceAuthority(ctx, prior, next, func(call context.Context, tx driver.Tx) error {
		return s.InstanceAuthority.RevalidateAuthorityTx(call, tx, in.Authority)
	}); checkErr != nil {
		return nil, checkErr
	}
	return instanceNamespaceReceipt(operation)
}

type instanceNamespacePublicationStore interface {
	PublishInstanceClusterEnrollment(context.Context, *ClusterOperation, *RuntimeAttachment, *connections.Connection, InstanceNamespaceRecord, func(context.Context, driver.Tx) error) error
	SaveInstanceNamespaceCheckpoint(context.Context, *ClusterOperation, int64, InstanceNamespaceRecord, func(context.Context, driver.Tx) error) error
}

func (s *Service) saveInstanceNamespaceCheckpoint(ctx context.Context, o *ClusterOperation) error {
	store, ok := s.Clusters.(InstanceNamespaceStore)
	publisher, publishes := s.Clusters.(instanceNamespacePublicationStore)
	if !ok || !publishes || s.InstanceAuthority == nil {
		return security.ErrForbidden
	}
	record, err := store.GetInstanceNamespaceRecord(ctx, o.OrgID, o.ID)
	if err != nil {
		return err
	}
	old := o.Revision
	o.Revision++
	o.UpdatedAt = s.now().UTC()
	return publisher.SaveInstanceNamespaceCheckpoint(ctx, o, old, record, func(call context.Context, tx driver.Tx) error {
		return s.InstanceAuthority.RevalidateAuthorityTx(call, tx, record.CurrentAuthority)
	})
}

func (s *Service) publishInstanceNamespace(ctx context.Context, o *ClusterOperation, a *RuntimeAttachment, c *connections.Connection) error {
	store, ok := s.Clusters.(InstanceNamespaceStore)
	publisher, publishes := s.Clusters.(instanceNamespacePublicationStore)
	if !ok || !publishes || s.InstanceAuthority == nil {
		return security.ErrForbidden
	}
	record, err := store.GetInstanceNamespaceRecord(ctx, o.OrgID, o.ID)
	if err != nil {
		return err
	}
	return publisher.PublishInstanceClusterEnrollment(ctx, o, a, c, record, func(call context.Context, tx driver.Tx) error {
		return s.InstanceAuthority.RevalidateAuthorityTx(call, tx, record.CurrentAuthority)
	})
}

func instanceNamespaceIdentity(org, instance string, epoch int64) (operationID, namespace string) {
	raw, _ := json.Marshal([]any{org, instance, epoch}) //nolint:errcheck // identity contains only strings and integers
	id := uuid.NewSHA1(uuid.NameSpaceOID, raw)
	return id.String(), fmt.Sprintf("lp-instance-%x", id[:])
}

func (s *Service) ReserveInstanceNamespace(ctx context.Context, p security.Principal, in InstanceNamespaceInput) (*InstanceNamespaceReceipt, error) {
	if err := s.clusterEnabled(); err != nil {
		return nil, err
	}
	store, ok := s.Clusters.(InstanceNamespaceStore)
	if !ok || s.InstanceAuthority == nil || s.InstanceIsolation == nil || s.Clouds == nil || s.RuntimeConnections == nil {
		return nil, ErrInvalid
	}
	authority := in.Authority
	if p.ReadOnly || !p.IsFleet() || !reflect.DeepEqual(p, authority.Principal) || in.ClusterID == "" {
		return nil, security.ErrForbidden
	}
	snapshot, err := s.InstanceAuthority.VerifyInstanceSnapshot(ctx, authority, in.Snapshot)
	if err != nil {
		return nil, err
	}
	resource, err := s.Clusters.GetManagedResource(ctx, authority.Scope.OrgID, in.ClusterID)
	if err != nil {
		return nil, err
	}
	if resource.DeletedAt != nil || resource.Status != "running" || resource.Observation == nil || resource.CloudID != authority.Request.CloudID || resource.Observation.Ref.Region != authority.Request.Region || resource.Observation.CAHash == "" {
		return nil, ErrConflict
	}
	qualification, err := s.InstanceIsolation.QualifyInstanceNamespace(ctx, authority, *snapshot, *resource)
	if err != nil {
		return nil, err
	}
	if checkErr := validateNamespaceQualification(qualification, *resource, authority); checkErr != nil {
		return nil, checkErr
	}
	profile, err := s.Clouds.GetProfile(ctx, authority.Scope.OrgID, "", resource.CloudID)
	if err != nil {
		return nil, err
	}
	grants, ok := s.Clouds.(cloudprofiles.GrantStore)
	if !ok {
		return nil, security.ErrForbidden
	}
	grant, err := grants.GetProjectGrant(ctx, authority.Scope.OrgID, resource.CloudID, authority.Scope.ProjectID)
	if err != nil {
		return nil, err
	}
	if grant.Revoked || len(grant.TargetIDs) != 0 {
		return nil, security.ErrForbidden
	}
	account, err := s.RuntimeConnections.GetConnection(ctx, authority.Scope.OrgID, resource.ConnectionID)
	if err != nil {
		return nil, err
	}
	if account == nil || account.Status != connections.StatusReady || account.Cloud != resource.Provider || account.Generation < 1 {
		return nil, ErrConflict
	}
	id, namespace := instanceNamespaceIdentity(authority.Scope.OrgID, snapshot.InstanceID, snapshot.OwnershipEpoch)
	pins := InstanceNamespacePins{FleetID: snapshot.FleetID, InstanceID: snapshot.InstanceID, EnvironmentID: snapshot.EnvironmentID, Generation: snapshot.Generation, OwnershipEpoch: snapshot.OwnershipEpoch, SnapshotDigest: snapshot.Digest, QualificationID: qualification.ID}
	now := s.now().UTC()
	input := ClusterInput{Kind: InstanceNamespaceKind, Provider: resource.Provider, ConnectionID: resource.ConnectionID, AccountID: resource.AccountID, ResourceID: resource.ID, ResourceRevision: resource.Revision, ConnectionGeneration: account.Generation, CloudRevision: profile.Revision, Ref: resource.Observation.Ref, Attachment: &AttachmentInput{Instance: &pins, InstancePolicy: &qualification.Policy, RuntimeAccess: &provider.RuntimeAccessProfile{SchemaVersion: 1, CleanupDependents: true, LifecycleJobs: true}, TargetSlug: namespace, CloudRevision: profile.Revision, ResourceRevision: resource.Revision, GrantRevision: grant.Revision, TrustFingerprint: resource.Observation.CAHash, EndpointFingerprint: endpointFingerprint(resource.Observation.Endpoint), Prerequisites: qualification.Prerequisites, ProjectID: authority.Scope.ProjectID, Namespace: namespace, ServiceAccountName: "runtime"}}
	operation := &ClusterOperation{ID: id, OrgID: authority.Scope.OrgID, CloudID: resource.CloudID, ResourceID: resource.ID, Kind: InstanceNamespaceKind, SchemaVersion: 1, Input: input, Digest: clusterDigest(input), Revision: 1, Status: "accepted", Principal: p, Attempt: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: authority.ExpiresAt, Steps: []ClusterStep{{Name: "resolve-cluster", State: "completed", CompletedAt: &now}, {Name: "enroll-runtime", State: "pending"}}}
	for i := range operation.Steps {
		operation.Steps[i].RequestDigest = operation.Digest
	}
	record := InstanceNamespaceRecord{CurrentGeneration: snapshot.Generation, CurrentSnapshotDigest: snapshot.Digest, OrgID: operation.OrgID, ProjectID: authority.Scope.ProjectID, CloudID: resource.CloudID, ClusterID: resource.ID, OperationID: id, Namespace: namespace, Pins: pins, CreationAuthority: authority, CurrentAuthority: authority}
	operation, err = store.ReserveInstanceNamespaceOperation(ctx, record, operation, func(call context.Context, tx driver.Tx) error {
		return s.InstanceAuthority.RevalidateAuthorityTx(call, tx, authority)
	})
	if err != nil {
		return nil, err
	}
	receipt, err := instanceNamespaceReceipt(operation)
	if err != nil {
		return nil, err
	}
	if !receipt.Ready && s.ClusterWorkflow != nil {
		if checkErr := s.startClusterOperation(ctx, operation); checkErr != nil {
			return receipt, checkErr
		}
	}
	return receipt, nil
}

func validateNamespaceQualification(q NamespaceQualification, r ManagedResource, a fleets.OperationAuthority) error {
	if q.ID == "" || q.ClusterID != r.ID || q.Incarnation != r.Incarnation || q.ClusterRevision != r.Revision || r.Observation == nil || q.ClusterCA != r.Observation.CAHash || q.EndpointFingerprint != endpointFingerprint(r.Observation.Endpoint) || !q.ExpiresAt.After(time.Now()) {
		return ErrInvalid
	}
	if err := q.Policy.Validate(); err != nil {
		return err
	}
	demand := a.Request.Demand
	if q.Policy.CPUMillis > demand.CPUMillis || q.Policy.MemoryMiB > demand.MemoryMiB || (q.Policy.EphemeralMiB+1023)/1024 > demand.StorageGiB {
		return fleets.ErrCapacity
	}
	return supportedClusterPrerequisites(q.Prerequisites)
}

func instanceNamespaceReceipt(o *ClusterOperation) (*InstanceNamespaceReceipt, error) {
	if o == nil || o.Kind != InstanceNamespaceKind || o.Input.Attachment == nil || o.Input.Attachment.Instance == nil || o.Input.Attachment.InstancePolicy == nil {
		return nil, ErrInvalid
	}
	policy, err := o.Input.Attachment.InstancePolicy.Digest()
	if err != nil {
		return nil, err
	}
	receipt := &InstanceNamespaceReceipt{OperationID: o.ID, Namespace: o.Input.Attachment.Namespace, ClusterCA: o.Input.Attachment.TrustFingerprint, PolicyDigest: policy}
	if o.Status == "succeeded" {
		if o.Enrollment == nil || o.Enrollment.State.NamespaceUID == "" || o.Enrollment.State.Isolation == nil || o.Enrollment.State.Isolation.Digest != policy {
			return nil, ErrConflict
		}
		if o.Enrollment.AttachmentID == "" || o.Enrollment.RuntimeConnectionID == "" {
			return nil, ErrConflict
		}
		for _, kind := range []string{"ResourceQuota", "LimitRange", "NetworkPolicy"} {
			if o.Enrollment.State.Isolation.Objects[kind] == "" {
				return nil, ErrConflict
			}
		}
		receipt.NamespaceUID = o.Enrollment.State.NamespaceUID
		receipt.AttachmentID = o.Enrollment.AttachmentID
		receipt.ConnectionID = o.Enrollment.RuntimeConnectionID
		receipt.AttachmentGeneration = 1
		receipt.Ready = true
	}
	return receipt, nil
}

func (s *Service) instanceNamespaceAuthority(ctx context.Context, o *ClusterOperation) (provider.ClusterReader, provider.AccountAccess, error) {
	empty := provider.AccountAccess{}
	if err := s.clusterEnabled(); err != nil {
		return nil, empty, err
	}
	store, ok := s.Clusters.(InstanceNamespaceStore)
	if !ok || s.InstanceAuthority == nil || s.InstanceIsolation == nil || o.Kind != InstanceNamespaceKind || o.Input.Kind != InstanceNamespaceKind || o.Input.Attachment == nil || o.Input.Attachment.Instance == nil || o.Input.Attachment.InstancePolicy == nil || o.CancelRequested {
		return nil, empty, security.ErrForbidden
	}
	record, err := store.GetInstanceNamespaceRecord(ctx, o.OrgID, o.ID)
	if err != nil {
		return nil, empty, err
	}
	if record.OperationID != o.ID || record.CloudID != o.CloudID || record.ClusterID != o.ResourceID || record.ProjectID != o.Input.Attachment.ProjectID || record.Namespace != o.Input.Attachment.Namespace || record.Pins != *o.Input.Attachment.Instance {
		return nil, empty, ErrConflict
	}
	if checkErr := s.InstanceAuthority.RevalidateAuthority(ctx, record.CurrentAuthority); checkErr != nil {
		return nil, empty, checkErr
	}
	if checkErr := store.CheckInstanceNamespaceRecord(ctx, record); checkErr != nil {
		return nil, empty, checkErr
	}
	if checkErr := s.InstanceIsolation.RevalidateInstanceNamespaceQualification(ctx, record, o); checkErr != nil {
		return nil, empty, checkErr
	}
	profile, err := s.Clouds.GetProfile(ctx, o.OrgID, "", o.CloudID)
	if err != nil {
		return nil, empty, err
	}
	if profile.Revision != o.Input.CloudRevision {
		return nil, empty, ErrConflict
	}
	grants, ok := s.Clouds.(cloudprofiles.GrantStore)
	if !ok {
		return nil, empty, security.ErrForbidden
	}
	grant, err := grants.GetProjectGrant(ctx, o.OrgID, o.CloudID, record.ProjectID)
	if err != nil {
		return nil, empty, err
	}
	if grant.Revoked || grant.Revision != o.Input.Attachment.GrantRevision || len(grant.TargetIDs) != 0 {
		return nil, empty, security.ErrForbidden
	}
	conn, raw, err := s.Connections.Credential(ctx, o.OrgID, o.Input.ConnectionID)
	if err != nil {
		return nil, empty, err
	}
	if conn == nil || conn.OrgID != o.OrgID || conn.ID != o.Input.ConnectionID || conn.Status != connections.StatusReady || conn.Generation != o.Input.ConnectionGeneration || conn.Cloud != o.Input.Provider {
		return nil, empty, ErrConflict
	}
	credential, err := Credential(conn, raw)
	if err != nil {
		return nil, empty, err
	}
	if credential.AccountID != o.Input.AccountID {
		return nil, empty, ErrConflict
	}
	adapter, err := s.Adapters.Cluster(conn.Cloud, provider.ClusterDiscovery)
	if err != nil {
		return nil, empty, err
	}
	return adapter, provider.AccountAccess{Provider: conn.Cloud, AccountID: credential.AccountID, ConnectionID: conn.ID, Generation: conn.Generation, Credential: raw}, nil
}
