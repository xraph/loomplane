package cloudinfra

import (
	"context"
	"reflect"
	"time"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/currentauthority"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

// FleetObservationRecords supplies persisted identities after API authorization,
// or from the bounded background sampler. It does not accept caller replacements.
type FleetObservationRecords interface {
	GetInstance(context.Context, string, string) (*fleet.Instance, error)
	GetFleet(context.Context, string, string) (*fleet.Fleet, error)
}

// FleetObservationReader resolves and opens an existing namespace for reads only.
type FleetObservationReader struct {
	service    *Service
	records    FleetObservationRecords
	namespaces fleetObservationNamespaces
}

func NewFleetObservationReader(service *Service, records FleetObservationRecords) *FleetObservationReader {
	r := &FleetObservationReader{service: service, records: records}
	if service != nil {
		r.namespaces = service.InstanceNamespaces
	}
	return r
}

type fleetObservationNamespaces interface {
	beginNamespaceCall(context.Context) (context.Context, func(), error)
	readOriginalReservation(context.Context, string, string) (namespaceAction, bool, error)
}

// BeginObservation retains namespace adapter ownership until the complete sample
// finishes. Its context is canceled by caller cancellation or adapter shutdown.
func (r *FleetObservationReader) BeginObservation(ctx context.Context) (context.Context, func(), error) {
	if r == nil || r.namespaces == nil {
		return nil, nil, currentauthority.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return r.namespaces.beginNamespaceCall(ctx)
}

func (r *FleetObservationReader) validateOriginal(ctx context.Context, record InstanceNamespaceRecord) error {
	original, found, err := r.namespaces.readOriginalReservation(ctx, record.OrgID, record.OperationID)
	if err != nil {
		return err
	}
	if !found {
		return ErrConflict
	}
	before := original.Record
	if before.OrgID != record.OrgID || before.ProjectID != record.ProjectID || before.CloudID != record.CloudID || before.ClusterID != record.ClusterID || before.OperationID != record.OperationID || before.Namespace != record.Namespace || before.Pins != record.Pins || original.AttemptID != record.CreationAttemptID || !namespaceEqual(before.CreationAuthority, record.CreationAuthority) {
		return ErrConflict
	}
	return ctx.Err()
}

// FleetObservationTarget contains correlation and fencing pins, never a Vault key.
type FleetObservationTarget struct {
	OrgID, ProjectID, FleetID, InstanceID, EnvironmentID              string
	OperationID, CloudID, ClusterID, SnapshotDigest                   string
	Generation, OwnershipEpoch                                        int64
	AttachmentID, ConnectionID, Namespace, NamespaceUID               string
	ServiceAccount, ServiceAccountUID, ClusterCA, EndpointFingerprint string
	AttachmentRevision, TrustRevision, CredentialVersion              int64
	CredentialExpiry                                                  time.Time
}

func (r *FleetObservationReader) Target(ctx context.Context, org, fleetID, instanceID string) (FleetObservationTarget, error) {
	empty := FleetObservationTarget{}
	ctx, finish, err := r.BeginObservation(ctx)
	if err != nil {
		return empty, err
	}
	defer finish()
	if r == nil || r.service == nil || r.records == nil || r.service.Clusters == nil || org == "" || fleetID == "" || instanceID == "" {
		return empty, ErrInvalid
	}
	store, ok := r.service.Clusters.(InstanceNamespaceStore)
	if !ok {
		return empty, ErrInvalid
	}
	instance, err := r.records.GetInstance(ctx, org, instanceID)
	if err != nil {
		return empty, err
	}
	if instance == nil || instance.ID != instanceID || instance.FleetID != fleetID || instance.EnvironmentID == "" || instance.DesiredGeneration < 1 || instance.OwnershipEpoch < 1 {
		return empty, ErrConflict
	}
	f, err := r.records.GetFleet(ctx, org, fleetID)
	if err != nil {
		return empty, err
	}
	if f == nil || f.ID != fleetID || f.OrgID != org || f.ProjectID == "" {
		return empty, ErrConflict
	}
	operationID, namespace := instanceNamespaceIdentity(org, instanceID, instance.OwnershipEpoch)
	record, err := store.GetInstanceNamespaceRecord(ctx, org, operationID)
	if err != nil {
		return empty, err
	}
	if record.OrgID != org || record.ProjectID != f.ProjectID || record.OperationID != operationID || record.Namespace != namespace || record.Pins.FleetID != f.ID || record.Pins.InstanceID != instance.ID || record.Pins.EnvironmentID != instance.EnvironmentID || record.Pins.OwnershipEpoch != instance.OwnershipEpoch || record.CurrentGeneration != instance.DesiredGeneration || record.CurrentSnapshotDigest == "" {
		return empty, ErrConflict
	}
	if checkErr := r.validateOriginal(ctx, record); checkErr != nil {
		return empty, checkErr
	}
	operation, err := r.service.Clusters.GetClusterOperation(ctx, org, record.CloudID, operationID)
	if err != nil {
		return empty, err
	}
	if operation == nil || operation.ID != record.OperationID || operation.OrgID != record.OrgID || operation.CloudID != record.CloudID || operation.ResourceID != record.ClusterID || operation.Kind != InstanceNamespaceKind || operation.Status != "succeeded" || operation.CancelRequested || operation.Revision < 1 || operation.Input.Attachment == nil {
		return empty, ErrConflict
	}
	input := operation.Input.Attachment
	if input.Instance == nil || *input.Instance != record.Pins || input.ProjectID != record.ProjectID || input.Namespace != record.Namespace {
		return empty, ErrConflict
	}
	receipt, err := instanceNamespaceReceipt(operation)
	if err != nil || !receipt.Ready {
		return empty, ErrConflict
	}
	a, err := r.service.Clusters.GetRuntimeAttachment(ctx, org, receipt.AttachmentID)
	if err != nil {
		return empty, err
	}
	if a == nil || a.ID != receipt.AttachmentID || a.OrgID != record.OrgID || a.ProjectID != record.ProjectID || a.CloudID != record.CloudID || a.ClusterID != record.ClusterID || a.OperationID != record.OperationID || a.Namespace != record.Namespace || a.NamespaceUID != receipt.NamespaceUID || a.ServiceAccountUID == "" || a.ServiceAccountUID != operation.Enrollment.State.ServiceAccountUID || a.ServiceAccountName == "" || a.ServiceAccountName != input.ServiceAccountName || a.RuntimeConnectionID != receipt.ConnectionID || a.TrustFingerprint != receipt.ClusterCA || a.EndpointFingerprint == "" || a.Revision < 1 || a.SchemaVersion != 1 || a.TrustRevision < 1 || a.CredentialVersion < 1 || a.DeletedAt != nil || a.RetirementOperationID != "" {
		return empty, ErrConflict
	}
	return FleetObservationTarget{OrgID: org, ProjectID: f.ProjectID, FleetID: f.ID, InstanceID: instance.ID, EnvironmentID: instance.EnvironmentID, OperationID: record.OperationID, CloudID: record.CloudID, ClusterID: record.ClusterID, SnapshotDigest: record.CurrentSnapshotDigest, Generation: instance.DesiredGeneration, OwnershipEpoch: instance.OwnershipEpoch, AttachmentID: a.ID, ConnectionID: a.RuntimeConnectionID, Namespace: a.Namespace, NamespaceUID: a.NamespaceUID, ServiceAccount: a.ServiceAccountName, ServiceAccountUID: a.ServiceAccountUID, ClusterCA: a.TrustFingerprint, EndpointFingerprint: a.EndpointFingerprint, AttachmentRevision: a.Revision, TrustRevision: a.TrustRevision, CredentialVersion: a.CredentialVersion, CredentialExpiry: a.CredentialExpiry}, nil
}

// Open reveals only an existing, unexpired envelope after revalidating all pins.
// It never invokes generic Credential, token renewal or runtime access upgrades.
func (r *FleetObservationReader) Open(ctx context.Context, target FleetObservationTarget) (*kubernetes.Client, error) {
	ctx, finish, err := r.BeginObservation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	current, err := r.Target(ctx, target.OrgID, target.FleetID, target.InstanceID)
	if err != nil {
		return nil, err
	}
	if current != target {
		return nil, ErrConflict
	}
	s := r.service
	if s.RuntimeConnections == nil || s.Secrets == nil {
		return nil, ErrInvalid
	}
	if !target.CredentialExpiry.After(s.now().Add(time.Minute)) {
		return nil, connections.ErrCredentialRefreshNeeded
	}
	c, err := s.RuntimeConnections.GetConnection(ctx, target.OrgID, target.ConnectionID)
	if err != nil {
		return nil, err
	}
	if !fleetObservationConnection(c, target) {
		return nil, connections.ErrChanged
	}
	if checkErr := r.revalidate(ctx, target); checkErr != nil {
		return nil, checkErr
	}
	a, err := s.Clusters.GetRuntimeAttachment(ctx, target.OrgID, target.AttachmentID)
	if err != nil {
		return nil, err
	}
	if a == nil || a.VaultKey == "" || a.Revision != target.AttachmentRevision || a.CredentialVersion != target.CredentialVersion || !a.CredentialExpiry.Equal(target.CredentialExpiry) {
		return nil, ErrConflict
	}
	raw, err := s.revealManagedCredential(ctx, a.OrgID, a.VaultKey, a.CredentialExpiry)
	if err != nil {
		return nil, connections.ErrCredentialRefreshNeeded
	}
	cfg, err := kubernetes.ParseConfig(raw, c.Details)
	if err != nil {
		return nil, connections.ErrChanged
	}
	if cfg.Namespace != target.Namespace || cfg.Details["cluster_ca"] != target.ClusterCA || endpointFingerprint(cfg.REST.Host) != target.EndpointFingerprint {
		return nil, connections.ErrChanged
	}
	client, err := kubernetes.NewFleetObservationClient(cfg, target.ServiceAccount)
	if err != nil {
		return nil, err
	}
	if err = kubernetes.VerifyRuntimeIdentity(ctx, client.Kube, target.Namespace, target.NamespaceUID, target.ServiceAccount, target.ServiceAccountUID); err != nil {
		return nil, kubernetes.ErrFleetMetricIdentity
	}
	if checkErr := r.revalidate(ctx, target); checkErr != nil {
		return nil, checkErr
	}
	again, err := r.Target(ctx, target.OrgID, target.FleetID, target.InstanceID)
	if err != nil {
		return nil, err
	}
	if again != target {
		return nil, ErrConflict
	}
	connection, err := s.RuntimeConnections.GetConnection(ctx, target.OrgID, target.ConnectionID)
	if err != nil {
		return nil, err
	}
	if !fleetObservationConnection(connection, target) || !reflect.DeepEqual(c.Details, connection.Details) {
		return nil, connections.ErrChanged
	}
	return client, nil
}
func fleetObservationConnection(c *connections.Connection, target FleetObservationTarget) bool {
	return c != nil && c.OrgID == target.OrgID && c.ID == target.ConnectionID && c.Cloud == connections.Kubernetes && c.Method == connections.MethodManagedKubernetes && c.Status == connections.StatusReady && c.Message == "" && c.Generation == target.TrustRevision && c.Details["managed_attachment_id"] == target.AttachmentID && c.Details["managed_project_id"] == target.ProjectID && c.Details["managed_resource_id"] == target.ClusterID && c.Details["managed_instance_id"] == target.InstanceID && c.Details["managed_environment_id"] == target.EnvironmentID && c.Details["namespace"] == target.Namespace && c.Details["namespace_uid"] == target.NamespaceUID && c.Details["cluster_ca"] == target.ClusterCA && endpointFingerprint(c.Details["endpoint"]) == target.EndpointFingerprint
}
func (r *FleetObservationReader) revalidate(ctx context.Context, target FleetObservationTarget) error {
	s := r.service
	if err := s.clusterEnabled(); err != nil {
		return err
	}
	store, ok := s.Clusters.(InstanceNamespaceStore)
	if !ok || s.InstanceAuthority == nil || s.InstanceIsolation == nil || s.Clouds == nil {
		return security.ErrForbidden
	}
	record, err := store.GetInstanceNamespaceRecord(ctx, target.OrgID, target.OperationID)
	if err != nil {
		return err
	}
	if record.CurrentGeneration != target.Generation || record.Pins.OwnershipEpoch != target.OwnershipEpoch || record.CurrentSnapshotDigest != target.SnapshotDigest {
		return ErrConflict
	}
	if checkErr := r.validateOriginal(ctx, record); checkErr != nil {
		return checkErr
	}
	if checkErr := s.InstanceAuthority.RevalidateAuthority(ctx, record.CurrentAuthority); checkErr != nil {
		return checkErr
	}
	if checkErr := store.CheckInstanceNamespaceRecord(ctx, record); checkErr != nil {
		return checkErr
	}
	operation, err := s.Clusters.GetClusterOperation(ctx, target.OrgID, target.CloudID, target.OperationID)
	if err != nil {
		return err
	}
	if operation == nil || operation.Status != "succeeded" || operation.CancelRequested || operation.Input.Attachment == nil {
		return ErrConflict
	}
	if checkErr := s.InstanceIsolation.RevalidateInstanceNamespaceQualification(ctx, record, operation); checkErr != nil {
		return checkErr
	}
	profile, err := s.Clouds.GetProfile(ctx, target.OrgID, "", target.CloudID)
	if err != nil {
		return err
	}
	if profile == nil || profile.PlatformOwned || profile.OrgID != target.OrgID || profile.ID != target.CloudID || profile.Revision != operation.Input.CloudRevision {
		return ErrConflict
	}
	grants, ok := s.Clouds.(cloudprofiles.GrantStore)
	if !ok {
		return security.ErrForbidden
	}
	grant, err := grants.GetProjectGrant(ctx, target.OrgID, target.CloudID, target.ProjectID)
	if err != nil {
		return err
	}
	if grant == nil || grant.OrgID != target.OrgID || grant.CloudID != target.CloudID || grant.ProjectID != target.ProjectID || grant.Revoked || grant.Revision != operation.Input.Attachment.GrantRevision || len(grant.TargetIDs) != 0 {
		return security.ErrForbidden
	}
	cluster, err := s.Clusters.GetManagedResource(ctx, target.OrgID, target.ClusterID)
	if err != nil {
		return err
	}
	if cluster == nil || cluster.OrgID != target.OrgID || cluster.ID != target.ClusterID || cluster.CloudID != target.CloudID || cluster.DeletedAt != nil || cluster.Observation == nil || cluster.Status != "running" || cluster.Revision != operation.Input.ResourceRevision || cluster.Provider != operation.Input.Provider || cluster.AccountID != operation.Input.AccountID || cluster.ConnectionID != operation.Input.ConnectionID || cluster.Observation.Ref != operation.Input.Ref || cluster.Incarnation != operation.Input.Ref.Incarnation || cluster.ExternalID != operation.Input.Ref.ID || cluster.Observation.State != "running" || endpointFingerprint(cluster.Observation.Endpoint) != target.EndpointFingerprint || (cluster.Observation.CAHash == "" || cluster.Observation.CAHash != target.ClusterCA) {
		return ErrConflict
	}
	infra, err := s.RuntimeConnections.GetConnection(ctx, target.OrgID, operation.Input.ConnectionID)
	if err != nil {
		return err
	}
	if infra == nil || infra.OrgID != target.OrgID || infra.ID != operation.Input.ConnectionID || infra.Cloud != operation.Input.Provider || infra.Status != connections.StatusReady || infra.Message != "" || infra.Generation != operation.Input.ConnectionGeneration || fleetObservationAccount(infra) != operation.Input.AccountID {
		return ErrConflict
	}
	return nil
}

func fleetObservationAccount(c *connections.Connection) string {
	switch c.Cloud {
	case connections.DigitalOcean:
		if team := c.Details["team_uuid"]; team != "" {
			return team
		}
		return c.Details["account_uuid"]
	case connections.Vercel:
		if team := c.Details["team_id"]; team != "" {
			return team
		}
		return c.Details["user_id"]
	case connections.Cloudflare:
		return c.Details["account_id"]
	default:
		return ""
	}
}

// Revalidate checks the persisted authority again after a remote metric read.
// It does not open, renew or replace any credential.
func (r *FleetObservationReader) Revalidate(ctx context.Context, target FleetObservationTarget) error {
	ctx, finish, err := r.BeginObservation(ctx)
	if err != nil {
		return err
	}
	defer finish()
	current, err := r.Target(ctx, target.OrgID, target.FleetID, target.InstanceID)
	if err != nil {
		return err
	}
	if current != target {
		return ErrConflict
	}
	if r.service.RuntimeConnections == nil {
		return ErrInvalid
	}
	connection, err := r.service.RuntimeConnections.GetConnection(ctx, target.OrgID, target.ConnectionID)
	if err != nil {
		return err
	}
	if !fleetObservationConnection(connection, target) {
		return connections.ErrChanged
	}
	if checkErr := r.revalidate(ctx, target); checkErr != nil {
		return checkErr
	}
	current, err = r.Target(ctx, target.OrgID, target.FleetID, target.InstanceID)
	if err != nil {
		return err
	}
	if current != target {
		return ErrConflict
	}
	connection, err = r.service.RuntimeConnections.GetConnection(ctx, target.OrgID, target.ConnectionID)
	if err != nil {
		return err
	}
	if !fleetObservationConnection(connection, target) {
		return connections.ErrChanged
	}
	return nil
}
