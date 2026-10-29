package cloudinfra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"go.jetify.com/typeid/v2"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type ClusterEnrollmentStore interface {
	ReserveClusterEnrollment(context.Context, *ClusterOperation) error
	FindClusterEnrollment(context.Context, string, string, string) (*ClusterOperation, error)
	PublishClusterEnrollment(context.Context, *ClusterOperation, *RuntimeAttachment, *connections.Connection) error
}

func supportedClusterPrerequisites(p AttachmentPrerequisites) error {
	if p.RequireBuilds || p.RequirePersistentStorage || p.StorageClass != "" || p.RegistryHostname != "" {
		return fmt.Errorf("%w: managed namespace enrollment currently supports digest-pinned private workers; storage and builds require separate scoped qualification", ErrInvalid)
	}
	return nil
}
func endpointFingerprint(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:])
}
func enrollmentRequest(o *ClusterOperation) kubernetes.EnrollmentRequest {
	in := o.Input.Attachment
	return kubernetes.EnrollmentRequest{AttachmentID: o.Enrollment.AttachmentID, ProjectID: in.ProjectID, Namespace: in.Namespace, NamespaceUID: in.NamespaceUID, ServiceAccountName: in.ServiceAccountName, AdoptNamespace: in.AdoptNamespace, RequireIngress: in.Prerequisites.RequireIngress || in.Prerequisites.RequireTLS, RequireTLS: in.Prerequisites.RequireTLS}
}
func (s *Service) enrollmentAuthority(ctx context.Context, o *ClusterOperation) (provider.ClusterReader, provider.AccountAccess, error) {
	if checkErr := s.clusterEnabled(); checkErr != nil {
		return nil, provider.AccountAccess{}, checkErr
	}
	if o.CancelRequested || o.Input.Attachment == nil || o.Enrollment == nil {
		return nil, provider.AccountAccess{}, ErrConflict
	}
	r, err := s.Clusters.GetManagedResource(ctx, o.OrgID, o.ResourceID)
	if err != nil {
		return nil, provider.AccountAccess{}, err
	}
	if r.DeletedAt != nil || (o.Status != "succeeded" && r.Revision != o.Enrollment.ResourceRevision) || r.CloudID != o.CloudID || r.ConnectionID != o.Input.ConnectionID || r.AccountID != o.Input.AccountID {
		return nil, provider.AccountAccess{}, ErrConflict
	}
	if o.Status == "succeeded" {
		a, getErr := s.Clusters.GetRuntimeAttachment(ctx, o.OrgID, o.Enrollment.AttachmentID)
		if getErr != nil {
			return nil, provider.AccountAccess{}, getErr
		}
		if !clusterAttachmentPins(o, r, a) {
			return nil, provider.AccountAccess{}, ErrConflict
		}
	}
	return s.clusterAuthority(ctx, o.Principal, o.CloudID, o.Input)
}

func clusterAttachmentPins(o *ClusterOperation, r *ManagedResource, a *RuntimeAttachment) bool {
	ref := o.Input.Ref
	if o.Kind == "create" && len(o.Receipts) == 1 {
		ref = o.Receipts[0].Ref
	}
	return a.DeletedAt == nil && a.OperationID == o.ID && a.ClusterID == r.ID &&
		r.DeletedAt == nil && r.CloudID == o.CloudID && r.ConnectionID == o.Input.ConnectionID &&
		r.Provider == o.Input.Provider && r.AccountID == o.Input.AccountID &&
		r.ExternalID == ref.ID && r.Incarnation == ref.Incarnation && r.Observation != nil &&
		r.Observation.Ref == ref && r.Observation.CAHash != "" &&
		r.Observation.CAHash == a.TrustFingerprint && endpointFingerprint(r.Observation.Endpoint) == a.EndpointFingerprint
}
func (s *Service) enrollmentCheckpoint(ctx context.Context, o *ClusterOperation) error {
	if _, _, checkErr := s.enrollmentAuthority(ctx, o); checkErr != nil {
		return checkErr
	}
	if o.LeaseExpiresAt == nil || !o.LeaseExpiresAt.After(s.now().Add(15*time.Second)) {
		return ErrConflict
	}
	return s.saveCluster(ctx, o)
}
func (s *Service) clusterBootstrap(ctx context.Context, o *ClusterOperation, r *ManagedResource) (*kubernetes.Client, error) {
	return s.clusterBootstrapChecked(ctx, o, r, func() error { return s.enrollmentCheckpoint(ctx, o) })
}
func (s *Service) clusterBootstrapChecked(ctx context.Context, o *ClusterOperation, r *ManagedResource, checkpoint func() error) (*kubernetes.Client, error) {
	reader, access, err := s.enrollmentAuthority(ctx, o)
	if err != nil {
		return nil, err
	}
	observation, err := reader.GetCluster(ctx, access, r.Observation.Ref)
	if err != nil {
		return nil, err
	}
	if observation.Ref != r.Observation.Ref || observation.State != "running" || observation.Endpoint == "" || observation.Endpoint != r.Observation.Endpoint || (observation.CAHash != "" && r.Observation.CAHash != "" && observation.CAHash != r.Observation.CAHash) {
		return nil, ErrConflict
	}
	bootstrap, ok := reader.(provider.ClusterBootstrap)
	if !ok {
		return nil, ErrInvalid
	}
	// Refresh authority after the provider read and checkpoint before obtaining management access.
	if checkErr := checkpoint(); checkErr != nil {
		return nil, checkErr
	}
	access.BeforeEffect = func(context.Context) error { return checkpoint() }
	accessMaterial, err := bootstrap.BootstrapCluster(ctx, access, observation.Ref, kubernetes.RuntimeTokenLifetime)
	if err != nil {
		return nil, err
	}
	ca := sha256.Sum256(accessMaterial.CAData)
	fingerprint := hex.EncodeToString(ca[:])
	if accessMaterial.Endpoint != observation.Endpoint || (observation.CAHash != "" && fingerprint != observation.CAHash) || (r.Observation.CAHash != "" && fingerprint != r.Observation.CAHash) || !accessMaterial.ExpiresAt.After(s.now().Add(time.Minute)) {
		return nil, ErrConflict
	}
	in := o.Input.Attachment
	if in.TrustFingerprint != "" && in.TrustFingerprint != fingerprint || in.EndpointFingerprint != "" && in.EndpointFingerprint != endpointFingerprint(observation.Endpoint) {
		return nil, ErrConflict
	}
	// Discovery may omit the CA. Pin authenticated bootstrap trust before runtime effects.
	if r.Observation.CAHash == "" {
		store, yes := s.Clusters.(ClusterLifecycleStore)
		if !yes || o.Status == "succeeded" {
			return nil, ErrConflict
		}
		if checkErr := checkpoint(); checkErr != nil {
			return nil, checkErr
		}
		old := o.Revision
		r.Revision++
		r.ObservedRevision = r.Revision
		r.Observation.CAHash = fingerprint
		r.UpdatedAt = s.now().UTC()
		o.Enrollment.ResourceRevision = r.Revision
		o.Revision++
		o.UpdatedAt = s.now().UTC()
		if checkErr := store.CheckpointClusterResource(ctx, o, r, old); checkErr != nil {
			return nil, checkErr
		}
	}
	raw, err := clientcmd.Write(clientapi.Config{Clusters: map[string]*clientapi.Cluster{"bootstrap": {Server: accessMaterial.Endpoint, CertificateAuthorityData: accessMaterial.CAData}}, AuthInfos: map[string]*clientapi.AuthInfo{"bootstrap": {Token: accessMaterial.Token, ClientCertificateData: accessMaterial.ClientCertificate, ClientKeyData: accessMaterial.ClientKey}}, Contexts: map[string]*clientapi.Context{"bootstrap": {Cluster: "bootstrap", AuthInfo: "bootstrap", Namespace: in.Namespace}}, CurrentContext: "bootstrap"})
	if err != nil {
		return nil, err
	}
	cfg, err := kubernetes.ParseConfig(raw, map[string]string{"namespace": in.Namespace, "ingress_address": in.Prerequisites.IngressAddress})
	if err != nil {
		return nil, err
	}
	return kubernetes.New(cfg)
}
func (s *Service) enrollClusterOperation(ctx context.Context, o *ClusterOperation) error {
	store, ok := s.Clusters.(ClusterEnrollmentStore)
	if !ok {
		return ErrInvalid
	}
	if o.Input.Attachment == nil || len(o.Steps) != 2 || o.Steps[0].State != "completed" || o.CancelRequested {
		return ErrConflict
	}
	if checkErr := supportedClusterPrerequisites(o.Input.Attachment.Prerequisites); checkErr != nil {
		return checkErr
	}
	r, err := s.Clusters.GetManagedResource(ctx, o.OrgID, o.ResourceID)
	if err != nil {
		return err
	}
	if r.Observation == nil || r.Status != "running" || r.DeletedAt != nil {
		return ErrConflict
	}
	if o.Enrollment == nil {
		connectionID, idErr := typeid.Generate("conn")
		if idErr != nil {
			return idErr
		}
		targetID, targetErr := typeid.Generate("target")
		if targetErr != nil {
			return targetErr
		}
		o.Enrollment = &ClusterEnrollment{AttachmentID: uuid.NewString(), RuntimeConnectionID: connectionID.String(), TargetID: targetID.String(), ResourceRevision: r.Revision}
	}
	if _, _, checkErr := s.enrollmentAuthority(ctx, o); checkErr != nil {
		return checkErr
	}
	if checkErr := store.ReserveClusterEnrollment(ctx, o); checkErr != nil {
		return checkErr
	}
	if o.Steps[1].State == "pending" {
		now := s.now().UTC()
		o.Steps[1].State = "attempted"
		o.Steps[1].AttemptedAt = &now
	}
	if checkErr := s.enrollmentCheckpoint(ctx, o); checkErr != nil {
		return checkErr
	}
	client, err := s.clusterBootstrap(ctx, o, r)
	if err != nil {
		return &clusterEnrollmentError{reason: "runtime_bootstrap_unavailable", cause: err}
	}
	if checkErr := client.CheckEnrollmentPrerequisites(ctx, o.Input.Attachment.Prerequisites.RequireIngress || o.Input.Attachment.Prerequisites.RequireTLS, o.Input.Attachment.Prerequisites.RequireTLS); checkErr != nil {
		return &clusterEnrollmentError{reason: "runtime_permissions_unavailable", cause: checkErr}
	}
	checkpoint := func(call context.Context, state *kubernetes.EnrollmentState) error {
		o.Enrollment.State = *state
		return s.enrollmentCheckpoint(call, o)
	}
	if checkErr := kubernetes.EnrollRuntime(ctx, client.Kube, enrollmentRequest(o), &o.Enrollment.State, checkpoint); checkErr != nil {
		return &clusterEnrollmentError{reason: "runtime_namespace_setup_failed", cause: checkErr}
	}
	raw, expiry, err := s.enrollmentToken(ctx, o, client)
	if err != nil {
		return &clusterEnrollmentError{reason: "runtime_token_unavailable", cause: err}
	}
	cfg, err := kubernetes.ParseConfig(raw, map[string]string{"namespace": o.Input.Attachment.Namespace, "ingress_address": o.Input.Attachment.Prerequisites.IngressAddress})
	if err != nil {
		return err
	}
	runtimeClient, err := kubernetes.New(cfg)
	if err != nil {
		return err
	}
	if checkErr := runtimeClient.CheckRuntimeBoundaries(ctx); checkErr != nil {
		return checkErr
	}
	result, err := runtimeClient.Check(ctx)
	if err != nil {
		return err
	}
	if o.Input.Attachment.Prerequisites.RequireIngress && result.Details["ingress"] != "ready" || o.Input.Attachment.Prerequisites.RequireTLS && result.Details["route_prerequisites"] != "ready" {
		return ErrInvalid
	}
	if len(result.Missing) != 0 || result.Details["namespace_uid"] != o.Enrollment.State.NamespaceUID {
		return ErrInvalid
	}
	if checkErr := s.enrollmentCheckpoint(ctx, o); checkErr != nil {
		return checkErr
	}
	now := s.now().UTC()
	in := o.Input.Attachment
	e := o.Enrollment
	a := &RuntimeAttachment{SchemaVersion: 1, CreatedAt: now, UpdatedAt: now, Ownership: "external", ServiceAccountName: in.ServiceAccountName, CredentialVersion: 1, ID: e.AttachmentID, OrgID: o.OrgID, CloudID: o.CloudID, ClusterID: o.ResourceID, ProjectID: in.ProjectID, Namespace: in.Namespace, NamespaceUID: e.State.NamespaceUID, ServiceAccountUID: e.State.ServiceAccountUID, RuntimeConnectionID: e.RuntimeConnectionID, EndpointFingerprint: endpointFingerprint(cfg.REST.Host), TrustFingerprint: cfg.Details["cluster_ca"], TrustRevision: 1, Revision: 1, CredentialExpiry: expiry, OperationID: o.ID, VaultKey: e.VaultKey}
	if !in.AdoptNamespace {
		a.Ownership = "managed"
	}
	for kind, uid := range e.State.Objects {
		name, namespace := in.ServiceAccountName, in.Namespace
		if kind == "ClusterRole" || kind == "ClusterRoleBinding" {
			name, namespace = "loomplane-"+e.AttachmentID, ""
		}
		a.RBACObjects = append(a.RBACObjects, RBACObjectReceipt{Kind: kind, Name: name, Namespace: namespace, UID: uid})
	}
	result.Details["managed_attachment_id"] = a.ID
	result.Details["managed_project_id"] = a.ProjectID
	result.Details["managed_resource_id"] = a.ClusterID
	c := &connections.Connection{ID: a.RuntimeConnectionID, OrgID: o.OrgID, Cloud: connections.Kubernetes, Name: "Managed " + a.ID, Method: connections.MethodManagedKubernetes, Generation: a.TrustRevision, Account: cfg.Context + "/" + in.Namespace, Details: result.Details, Status: connections.StatusReady, Missing: []string{}, CheckedAt: &now, CreatedBy: o.Principal.UserID, CreatedAt: now, UpdatedAt: now}
	o.Steps[1].State = "completed"
	o.Steps[1].CompletedAt = &now
	o.Status = "succeeded"
	o.Error = ""
	return store.PublishClusterEnrollment(ctx, o, a, c)
}
func (s *Service) enrollmentToken(ctx context.Context, o *ClusterOperation, client *kubernetes.Client) ([]byte, time.Time, error) {
	e := o.Enrollment
	// An attempted Vault key survives process loss. Reuse it only after checking runtime identities.
	if checkErr := kubernetes.VerifyRuntimeIdentity(ctx, client.Kube, o.Input.Attachment.Namespace, e.State.NamespaceUID, o.Input.Attachment.ServiceAccountName, e.State.ServiceAccountUID); checkErr != nil {
		return nil, time.Time{}, checkErr
	}
	if e.VaultKey != "" && e.CredentialExpiry.After(s.now().Add(kubernetes.RuntimeTokenRenewalLead)) {
		raw, revealErr := s.revealManagedCredential(ctx, o.OrgID, e.VaultKey, e.CredentialExpiry)
		if revealErr == nil {
			return raw, e.CredentialExpiry, nil
		}
	}
	if checkErr := s.enrollmentCheckpoint(ctx, o); checkErr != nil {
		return nil, time.Time{}, checkErr
	}
	token, expiry, err := kubernetes.RequestRuntimeToken(ctx, client.Kube, o.Input.Attachment.Namespace, o.Input.Attachment.ServiceAccountName, s.now())
	if err != nil {
		return nil, time.Time{}, err
	}
	raw, err := kubernetes.RuntimeKubeconfig(client.Config.REST.Host, client.Config.REST.CAData, o.Input.Attachment.Namespace, token)
	if err != nil {
		return nil, time.Time{}, err
	}
	if e.VaultKey != "" {
		e.RetiredVaultKeys = append(e.RetiredVaultKeys, e.VaultKey)
	}
	e.VaultKey = "cluster-runtime-" + uuid.NewString()
	e.CredentialExpiry = expiry
	if checkErr := s.enrollmentCheckpoint(ctx, o); checkErr != nil {
		return nil, time.Time{}, checkErr
	}
	if checkErr := s.putManagedCredential(ctx, o.OrgID, e.VaultKey, raw, expiry); checkErr != nil {
		return nil, time.Time{}, checkErr
	}
	return raw, expiry, nil
}

// EnrollClusterTarget resumes a previously approved operation with identical input.
func (s *Service) EnrollClusterTarget(ctx context.Context, p security.Principal, cloudID, resourceID string, in AttachmentInput) (*RuntimeAttachment, error) {
	store, ok := s.Clusters.(ClusterEnrollmentStore)
	if !ok {
		return nil, ErrInvalid
	}
	o, err := store.FindClusterEnrollment(ctx, p.OrgID, cloudID, resourceID)
	if err != nil {
		return nil, err
	}
	if o.Input.Attachment == nil || !reflect.DeepEqual(*o.Input.Attachment, in) || o.Principal.UserID != p.UserID || o.Principal.KeyID != p.KeyID {
		return nil, security.ErrForbidden
	}
	if _, _, checkErr := s.clusterAuthority(ctx, p, cloudID, o.Input); checkErr != nil {
		return nil, checkErr
	}
	if o.Status == "needs_attention" {
		if _, retryErr := s.RetryCluster(ctx, p, cloudID, o.ID); retryErr != nil {
			return nil, retryErr
		}
	}
	if o.Status != "succeeded" {
		if checkErr := s.ReconcileClusterOperation(ctx, p.OrgID, o.ID, nil); checkErr != nil {
			return nil, checkErr
		}
	}
	current, err := s.Clusters.GetClusterOperation(ctx, p.OrgID, cloudID, o.ID)
	if err != nil {
		return nil, err
	}
	if current.Status != "succeeded" || current.Enrollment == nil {
		return nil, ErrPending
	}
	return s.Clusters.GetRuntimeAttachment(ctx, p.OrgID, current.Enrollment.AttachmentID)
}
