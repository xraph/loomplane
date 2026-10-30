package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
)

func (s *Service) ResolveManagedCredential(ctx context.Context, c connections.Connection) (connections.ManagedCredential, error) {
	empty := connections.ManagedCredential{}
	if c.Method != connections.MethodManagedKubernetes || c.Cloud != connections.Kubernetes || s.Clusters == nil || s.Secrets == nil {
		return empty, connections.ErrNotReady
	}
	a, err := s.Clusters.GetRuntimeAttachment(ctx, c.OrgID, c.Details["managed_attachment_id"])
	if err != nil {
		return empty, err
	}
	if a.DeletedAt != nil || a.RuntimeConnectionID != c.ID || a.ProjectID != c.Details["managed_project_id"] || a.TrustRevision != c.Generation || a.NamespaceUID != c.Details["namespace_uid"] || a.TrustFingerprint != c.Details["cluster_ca"] || a.EndpointFingerprint != endpointFingerprint(c.Details["endpoint"]) {
		return empty, connections.ErrChanged
	}
	o, err := s.Clusters.GetClusterOperation(ctx, a.OrgID, a.CloudID, a.OperationID)
	if err != nil {
		return empty, err
	}
	if o.Status != "succeeded" || o.Enrollment == nil || o.Enrollment.AttachmentID != a.ID {
		return empty, ErrConflict
	}
	reader, access, err := s.enrollmentAuthority(ctx, o)
	if err != nil {
		return empty, errors.Join(ErrRuntimeCredentialDelegationUnavailable, err)
	}
	r, err := s.Clusters.GetManagedResource(ctx, a.OrgID, a.ClusterID)
	if err != nil {
		return empty, err
	}
	observed, err := reader.GetCluster(ctx, access, r.Observation.Ref)
	if err != nil {
		return empty, err
	}
	if observed.Ref != r.Observation.Ref || observed.State != "running" || observed.Endpoint != c.Details["endpoint"] || (observed.CAHash != "" && observed.CAHash != a.TrustFingerprint) {
		return empty, connections.ErrChanged
	}
	raw, err := s.revealManagedCredential(ctx, a.OrgID, a.VaultKey, a.CredentialExpiry)
	if err != nil {
		return empty, err
	}
	if !a.CredentialExpiry.After(s.now().Add(kubernetes.RuntimeTokenRenewalLead)) {
		raw, err = s.refreshManagedCredential(ctx, o, r, a)
		if err != nil {
			return empty, err
		}
	}
	cfg, err := kubernetes.ParseConfig(raw, c.Details)
	if err != nil {
		return empty, err
	}
	if cfg.Details["cluster_ca"] != a.TrustFingerprint || endpointFingerprint(cfg.REST.Host) != a.EndpointFingerprint {
		return empty, connections.ErrChanged
	}
	runtimeClient, err := kubernetes.New(cfg)
	if err != nil {
		return empty, err
	}
	if checkErr := runtimeClient.CheckRuntimeBoundaries(ctx); checkErr != nil {
		return empty, checkErr
	}
	if checkErr := kubernetes.VerifyRuntimeIdentity(ctx, runtimeClient.Kube, a.Namespace, a.NamespaceUID, a.ServiceAccountName, a.ServiceAccountUID); checkErr != nil {
		return empty, checkErr
	}
	s.cleanupManagedCredentialOrphans(ctx, a, o)
	if _, _, checkErr := s.enrollmentAuthority(ctx, o); checkErr != nil {
		return empty, errors.Join(ErrRuntimeCredentialDelegationUnavailable, checkErr)
	}
	current, err := s.Clusters.GetRuntimeAttachment(ctx, a.OrgID, a.ID)
	if err != nil {
		return empty, err
	}
	if current.DeletedAt != nil || current.Revision != a.Revision || current.TrustRevision != a.TrustRevision || current.CredentialVersion != a.CredentialVersion {
		return empty, connections.ErrChanged
	}
	if checkErr := s.verifyRuntimeAccessCredential(ctx, a, o, r, runtimeClient); checkErr != nil {
		return empty, checkErr
	}
	return connections.ManagedCredential{Kubeconfig: raw, ExpiresAt: a.CredentialExpiry, TrustRevision: a.TrustRevision, CredentialVersion: a.CredentialVersion}, nil
}
func (s *Service) refreshManagedCredential(ctx context.Context, o *ClusterOperation, r *ManagedResource, a *RuntimeAttachment) (credential []byte, renewalErr error) {
	defer func() {
		reason := "runtime_credential_renewed"
		if renewalErr != nil {
			reason = "runtime_credential_renewal_failed"
		}
		s.recordClusterOutcome(ctx, "cloud.cluster.renewal", o, reason)
	}()
	store, ok := s.Clusters.(ClusterCredentialStore)
	if !ok {
		return nil, ErrInvalid
	}
	refresh, err := store.BeginClusterCredentialRefresh(ctx, a, uuid.NewString(), "cluster-runtime-"+uuid.NewString())
	if err != nil {
		return nil, err
	}
	authority := func() error {
		if _, _, checkErr := s.enrollmentAuthority(ctx, o); checkErr != nil {
			return checkErr
		}
		return store.CheckClusterCredentialRefresh(ctx, a, refresh)
	}
	bootstrap, err := s.clusterBootstrapChecked(ctx, o, r, authority)
	if err != nil {
		return nil, err
	}
	if checkErr := kubernetes.VerifyRuntimeIdentity(ctx, bootstrap.Kube, a.Namespace, a.NamespaceUID, a.ServiceAccountName, a.ServiceAccountUID); checkErr != nil {
		return nil, checkErr
	}

	var raw []byte
	var expiry time.Time
	if refresh.RecoveryKey != "" && refresh.RecoveryExpiry != nil && refresh.RecoveryExpiry.After(s.now().Add(kubernetes.RuntimeTokenRenewalLead)) {
		recovered, recoveryErr := s.revealManagedCredential(ctx, a.OrgID, refresh.RecoveryKey, *refresh.RecoveryExpiry)
		if recoveryErr == nil {
			raw = recovered
			expiry = *refresh.RecoveryExpiry
		}
	}
	if len(raw) == 0 {
		if checkErr := authority(); checkErr != nil {
			return nil, checkErr
		}
		token, tokenExpiry, tokenErr := kubernetes.RequestRuntimeToken(ctx, bootstrap.Kube, a.Namespace, a.ServiceAccountName, s.now())
		if tokenErr != nil {
			return nil, tokenErr
		}
		expiry = tokenExpiry
		raw, err = kubernetes.RuntimeKubeconfig(bootstrap.Config.REST.Host, bootstrap.Config.REST.CAData, a.Namespace, token)
		if err != nil {
			return nil, err
		}
	}
	if checkErr := authority(); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := store.RecordClusterCredentialExpiry(ctx, a, refresh, expiry); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := s.putManagedCredential(ctx, a.OrgID, refresh.VaultKey, raw, expiry); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := authority(); checkErr != nil {
		return nil, checkErr
	}
	oldKey := a.VaultKey
	a.CredentialVersion = refresh.Version
	a.VaultKey = refresh.VaultKey
	a.CredentialExpiry = *refresh.ExpiresAt
	a.Revision++
	a.UpdatedAt = s.now().UTC()
	if checkErr := store.FinishClusterCredentialRefresh(ctx, a, refresh); checkErr != nil {
		return nil, checkErr
	}
	if oldKey != "" && oldKey != a.VaultKey {
		if deleteErr := s.Secrets.Delete(context.WithoutCancel(ctx), a.OrgID, oldKey); deleteErr != nil {
			s.warn("remove retired managed runtime credential", deleteErr)
		}
	}
	return raw, nil
}

var _ connections.ManagedCredentialResolver = (*Service)(nil)

// Vault envelopes bind recovery metadata to the token material actually written.
type managedVaultCredential struct {
	Kubeconfig []byte    `json:"kubeconfig"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func (s *Service) putManagedCredential(ctx context.Context, org, key string, raw []byte, expiry time.Time) error {
	payload, err := json.Marshal(managedVaultCredential{Kubeconfig: raw, ExpiresAt: expiry})
	if err != nil {
		return err
	}
	return s.Secrets.Put(ctx, org, key, payload)
}
func (s *Service) revealManagedCredential(ctx context.Context, org, key string, expiry time.Time) ([]byte, error) {
	payload, err := s.Secrets.Reveal(ctx, org, key)
	if err != nil {
		return nil, err
	}
	var credential managedVaultCredential
	if json.Unmarshal(payload, &credential) != nil || !credential.ExpiresAt.Equal(expiry) || len(credential.Kubeconfig) == 0 {
		return nil, connections.ErrChanged
	}
	return credential.Kubeconfig, nil
}

func (s *Service) cleanupManagedCredentialOrphans(ctx context.Context, a *RuntimeAttachment, o *ClusterOperation) {
	store, ok := s.Clusters.(ClusterCredentialStore)
	if !ok {
		return
	}
	keys, err := store.OrphanClusterCredentialKeys(ctx, a)
	if err != nil {
		s.warn("read retired runtime credential attempts", err)
		return
	}
	keys = append(keys, o.Enrollment.RetiredVaultKeys...)
	for _, key := range keys {
		if key == "" || key == a.VaultKey {
			continue
		}
		if deleteErr := s.Secrets.Delete(ctx, a.OrgID, key); deleteErr != nil {
			s.warn("remove orphan runtime credential", deleteErr)
		}
	}
}
