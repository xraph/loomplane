package cloudinfra

import (
	"context"
	"errors"
	"reflect"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

var ErrRuntimeCredentialDelegationUnavailable = errors.New("runtime_credential_delegation_unavailable")

const RuntimeAccessUpgrade = "runtime-access-upgrade"

type runtimeAccessContext struct {
	attachment *RuntimeAttachment
	resource   *ManagedResource
	enrollment *ClusterOperation
	current    *RuntimeAccessRecord
}

func (s *Service) runtimeAccessContext(ctx context.Context, p security.Principal, cloud string, in ClusterInput) (*runtimeAccessContext, error) {
	if in.Kind != RuntimeAccessUpgrade || in.RuntimeAccess == nil || !in.RuntimeAccess.Profile.Valid() || in.Attachment != nil || in.Maintenance != nil || in.Retirement != nil || !reflect.DeepEqual(in.Spec, provider.ClusterSpec{}) {
		return nil, ErrInvalid
	}
	store, ok := s.Clusters.(RuntimeAccessStore)
	if !ok {
		return nil, ErrInvalid
	}
	a, err := s.Clusters.GetRuntimeAttachment(ctx, p.OrgID, in.RuntimeAccess.AttachmentID)
	if err != nil {
		return nil, err
	}
	if a.DeletedAt != nil || a.CloudID != cloud || a.ClusterID != in.ResourceID {
		return nil, ErrConflict
	}
	r, err := s.Clusters.GetManagedResource(ctx, p.OrgID, a.ClusterID)
	if err != nil {
		return nil, err
	}
	if r.DeletedAt != nil || r.Revision != in.ResourceRevision || r.CloudID != cloud || r.ConnectionID != in.ConnectionID || r.AccountID != in.AccountID || r.Observation == nil || r.Observation.Ref != in.Ref || r.Observation.State != "running" || r.Observation.CAHash != a.TrustFingerprint || endpointFingerprint(r.Observation.Endpoint) != a.EndpointFingerprint {
		return nil, ErrConflict
	}
	enrollment, err := s.Clusters.GetClusterOperation(ctx, p.OrgID, cloud, a.OperationID)
	if err != nil {
		return nil, err
	}
	if enrollment.Status != "succeeded" || enrollment.Enrollment == nil || enrollment.Enrollment.AttachmentID != a.ID || enrollment.Input.Attachment == nil {
		return nil, ErrConflict
	}
	current, err := store.GetRuntimeAccess(ctx, p.OrgID, a.ID)
	if err != nil {
		return nil, err
	}
	if current != nil && !in.RuntimeAccess.Profile.Includes(current.Input.Profile) {
		return nil, ErrConflict
	}
	return &runtimeAccessContext{a, r, enrollment, current}, nil
}

func (s *Service) runtimeAccessGrant(ctx context.Context, p security.Principal, cloud string, a *RuntimeAttachment) (int64, error) {
	grants, ok := s.Clouds.(cloudprofiles.GrantStore)
	if !ok {
		return 0, security.ErrForbidden
	}
	g, err := grants.GetProjectGrant(ctx, p.OrgID, cloud, a.ProjectID)
	if err != nil {
		return 0, err
	}
	if g.Revoked {
		return 0, security.ErrForbidden
	}
	if len(g.TargetIDs) > 0 {
		targets, ok := s.Clouds.(interface {
			GetTarget(context.Context, string, string, string, string) (*cloudprofiles.Target, error)
		})
		if !ok {
			return 0, security.ErrForbidden
		}
		// Restricted grants must name an existing target for this exact attachment.
		found := false
		for _, id := range g.TargetIDs {
			target, getErr := targets.GetTarget(ctx, p.OrgID, a.ProjectID, cloud, id)
			if getErr == nil && target.ConnectionID == a.RuntimeConnectionID && target.ProjectID == a.ProjectID && target.CloudID == cloud {
				found = true
			}
		}
		if !found {
			return 0, security.ErrForbidden
		}
	}
	return g.Revision, nil
}

func runtimeAccessPins(a *RuntimeAttachment, profile provider.RuntimeAccessProfile) (provider.RuntimeAccessInput, error) {
	in := provider.RuntimeAccessInput{AttachmentID: a.ID, AttachmentRevision: a.Revision, ProjectID: a.ProjectID, Namespace: a.Namespace, NamespaceUID: a.NamespaceUID, ServiceAccountName: a.ServiceAccountName, ServiceAccountUID: a.ServiceAccountUID, Profile: profile}
	for _, obj := range a.RBACObjects {
		if obj.Namespace != a.Namespace {
			continue
		}
		switch obj.Kind {
		case "Role":
			if obj.Name != a.ServiceAccountName {
				return in, ErrConflict
			}
			if in.RoleUID != "" {
				return in, ErrConflict
			}
			in.RoleName, in.RoleUID = obj.Name, obj.UID
		case "RoleBinding":
			if obj.Name != a.ServiceAccountName {
				return in, ErrConflict
			}
			if in.RoleBindingUID != "" {
				return in, ErrConflict
			}
			in.RoleBindingName, in.RoleBindingUID = obj.Name, obj.UID
		}
	}
	if in.RoleUID == "" || in.RoleBindingUID == "" {
		return in, ErrConflict
	}
	return in, nil
}

func (s *Service) prepareRuntimeAccess(ctx context.Context, p security.Principal, cloud string, in *ClusterInput, reader provider.ClusterReader, access provider.AccountAccess) error {
	facts, err := s.runtimeAccessContext(ctx, p, cloud, *in)
	if err != nil {
		return err
	}
	pin, err := runtimeAccessPins(facts.attachment, in.RuntimeAccess.Profile)
	if err != nil {
		return err
	}
	pin.GrantRevision, err = s.runtimeAccessGrant(ctx, p, cloud, facts.attachment)
	if err != nil {
		return err
	}
	old := enrollmentRequest(facts.enrollment)
	if facts.current != nil {
		pin.CapabilityRevision = facts.current.Revision
		old.RuntimeAccess = &facts.current.Input.Profile
	}
	oldRules, err := kube.EnrollmentRoleRules(old)
	if err != nil {
		return err
	}
	client, err := s.runtimeAccessClient(ctx, p, cloud, *in, facts, reader, access, nil)
	if err != nil {
		return err
	}
	role, binding, err := kube.ReadRuntimeAccess(ctx, client, pin)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(role.Rules, oldRules) {
		return ErrConflict
	}
	pin.RoleResourceVersion = role.ResourceVersion
	pin.RoleRulesDigest = kube.RuntimeAccessDigest(role.Rules)
	pin.RoleBindingResourceVersion = binding.ResourceVersion
	pin.RoleBindingSubjectsRefDigest = kube.RuntimeBindingDigest(binding)
	in.RuntimeAccess = &pin
	return nil
}

func (s *Service) runtimeAccessGuard(ctx context.Context, o *ClusterOperation) error {
	if o.CancelRequested || o.Kind != RuntimeAccessUpgrade || o.LeaseExpiresAt == nil || !o.LeaseExpiresAt.After(s.now()) {
		return ErrConflict
	}
	if err := s.clusterEnabled(); err != nil {
		return err
	}
	if _, _, err := s.clusterAuthority(ctx, o.Principal, o.CloudID, o.Input); err != nil {
		return err
	}
	facts, err := s.runtimeAccessContext(ctx, o.Principal, o.CloudID, o.Input)
	if err != nil {
		return err
	}
	pin, err := runtimeAccessPins(facts.attachment, o.Input.RuntimeAccess.Profile)
	if err != nil {
		return err
	}
	saved := o.Input.RuntimeAccess
	if pin.AttachmentRevision != saved.AttachmentRevision || pin.RoleUID != saved.RoleUID || pin.RoleBindingUID != saved.RoleBindingUID || pin.NamespaceUID != saved.NamespaceUID || pin.ServiceAccountUID != saved.ServiceAccountUID {
		return ErrConflict
	}
	revision := int64(0)
	if facts.current != nil {
		revision = facts.current.Revision
	}
	grant, err := s.runtimeAccessGrant(ctx, o.Principal, o.CloudID, facts.attachment)
	if err != nil || grant != saved.GrantRevision || revision != saved.CapabilityRevision {
		return errors.Join(ErrConflict, err)
	}
	if o.RuntimeAccess != nil && o.RuntimeAccess.Attempted && o.Steps[0].State == "pending" {
		at := s.now().UTC()
		o.Steps[0].State = "attempted"
		o.Steps[0].AttemptedAt = &at
	}
	return s.saveCluster(ctx, o)
}

func (s *Service) reconcileRuntimeAccess(ctx context.Context, o *ClusterOperation, reader provider.ClusterReader, access provider.AccountAccess) error {
	store, ok := s.Clusters.(RuntimeAccessStore)
	if !ok {
		return ErrInvalid
	}
	facts, err := s.runtimeAccessContext(ctx, o.Principal, o.CloudID, o.Input)
	if err != nil {
		return s.clusterAttention(ctx, o, "runtime_access_identity_changed")
	}
	guard := func(call context.Context) error { return s.runtimeAccessGuard(call, o) }
	client, err := s.runtimeAccessClient(ctx, o.Principal, o.CloudID, o.Input, facts, reader, access, guard)
	if err != nil {
		return s.clusterAttention(ctx, o, "runtime_access_authority_unavailable")
	}
	request := enrollmentRequest(facts.enrollment)
	request.RuntimeAccess = &o.Input.RuntimeAccess.Profile
	rules, err := kube.EnrollmentRoleRules(request)
	if err != nil {
		return err
	}
	if o.RuntimeAccess == nil {
		o.RuntimeAccess = &kube.RuntimeAccessState{}
	}
	if err = kube.ReconcileRuntimeAccess(ctx, client, *o.Input.RuntimeAccess, rules, o.ID, o.Digest, o.RuntimeAccess, guard); err != nil {
		return s.clusterAttention(ctx, o, "runtime_access_update_unconfirmed")
	}
	if err = s.verifyUpgradeRuntime(ctx, facts, o.Input.RuntimeAccess.Profile); err != nil {
		return s.clusterAttention(ctx, o, "cleanup_permissions_unavailable")
	}
	if err = guard(ctx); err != nil {
		return s.clusterAttention(ctx, o, "runtime_access_authority_unavailable")
	}
	return store.PublishRuntimeAccess(ctx, o)
}

// checkRuntimeAccessIdentity checks runtime probes after the management read.
func (s *Service) checkRuntimeAccessIdentity(ctx context.Context, a *RuntimeAttachment, client *kube.Client) (*RuntimeAccessRecord, error) {
	store, ok := s.Clusters.(RuntimeAccessStore)
	if !ok {
		return nil, kube.ErrCleanupPermissionsUnavailable
	}
	record, err := store.GetRuntimeAccess(ctx, a.OrgID, a.ID)
	if err != nil || record == nil || !record.Input.Profile.Valid() {
		return nil, errors.Join(kube.ErrCleanupPermissionsUnavailable, err)
	}
	// Runtime credentials cannot read Role objects. Effective access probes are
	// checked here; the immutable receipt pins the separately reviewed identity.
	pin, err := runtimeAccessPins(a, record.Input.Profile)
	if err != nil || pin.RoleUID != record.Input.RoleUID || pin.RoleBindingUID != record.Input.RoleBindingUID || pin.NamespaceUID != record.Input.NamespaceUID || pin.ServiceAccountUID != record.Input.ServiceAccountUID {
		return nil, errors.Join(kube.ErrCleanupPermissionsUnavailable, err)
	}
	if err := kube.VerifyRuntimeIdentity(ctx, client.Kube, a.Namespace, a.NamespaceUID, a.ServiceAccountName, a.ServiceAccountUID); err != nil {
		return nil, errors.Join(kube.ErrCleanupPermissionsUnavailable, err)
	}
	if err := client.CheckRuntimeBoundaries(ctx); err != nil {
		return nil, err
	}
	if err := client.CheckRuntimeAccess(ctx, record.Input.Profile); err != nil {
		return nil, err
	}
	return record, nil
}

type runtimeAccessContextKey struct{}

func (s *Service) ReconcileRuntimeAccessOperation(ctx context.Context, org, id string, heartbeat func([]byte) error) error {
	return s.ReconcileClusterOperation(context.WithValue(ctx, runtimeAccessContextKey{}, true), org, id, heartbeat)
}
func (s *Service) startClusterOperation(ctx context.Context, o *ClusterOperation) error {
	if o.Kind == RuntimeAccessUpgrade {
		starter, ok := s.ClusterWorkflow.(interface {
			StartRuntimeAccessOperation(context.Context, string, string, int) error
		})
		if !ok {
			return ErrInvalid
		}
		return starter.StartRuntimeAccessOperation(ctx, o.OrgID, o.ID, o.Attempt)
	}
	return s.ClusterWorkflow.StartClusterOperation(ctx, o.OrgID, o.ID, o.Attempt)
}

func (s *Service) verifyRuntimeAccessCredential(ctx context.Context, a *RuntimeAttachment, enrollment *ClusterOperation, r *ManagedResource, client *kube.Client) error {
	store, ok := s.Clusters.(RuntimeAccessStore)
	if !ok {
		return nil
	}
	record, err := store.GetRuntimeAccess(ctx, a.OrgID, a.ID)
	if err != nil {
		return err
	}
	if record == nil {
		return nil
	}
	guard := func() error {
		if _, _, checkErr := s.enrollmentAuthority(ctx, enrollment); checkErr != nil {
			return checkErr
		}
		current, checkErr := s.Clusters.GetRuntimeAttachment(ctx, a.OrgID, a.ID)
		if checkErr != nil || current.DeletedAt != nil || current.Revision != a.Revision || current.TrustRevision != a.TrustRevision {
			return errors.Join(ErrConflict, checkErr)
		}
		access, checkErr := store.GetRuntimeAccess(ctx, a.OrgID, a.ID)
		if checkErr != nil || access == nil || access.Revision != record.Revision {
			return errors.Join(ErrConflict, checkErr)
		}
		return nil
	}
	management, err := s.clusterBootstrapChecked(ctx, enrollment, r, guard)
	if err != nil {
		return errors.Join(kube.ErrCleanupPermissionsUnavailable, err)
	}
	role, binding, err := kube.ReadRuntimeAccess(ctx, management.Kube, record.Input)
	if err != nil {
		return errors.Join(kube.ErrCleanupPermissionsUnavailable, err)
	}
	request := enrollmentRequest(enrollment)
	request.RuntimeAccess = &record.Input.Profile
	rules, err := kube.EnrollmentRoleRules(request)
	if err != nil || !reflect.DeepEqual(role.Rules, rules) || role.ResourceVersion != record.Receipt.ResourceVersion || kube.RuntimeAccessDigest(role.Rules) != record.Receipt.RulesDigest || role.Annotations[kube.RuntimeAccessOperationAnnotation] != record.OperationID || role.Annotations[kube.RuntimeAccessDigestAnnotation] != record.Receipt.RequestDigest || binding.ResourceVersion != record.Input.RoleBindingResourceVersion || kube.RuntimeBindingDigest(binding) != record.Input.RoleBindingSubjectsRefDigest {
		return errors.Join(kube.ErrCleanupPermissionsUnavailable, err)
	}
	if _, err = s.checkRuntimeAccessIdentity(ctx, a, client); err != nil {
		return err
	}
	if err = guard(); err != nil {
		return errors.Join(kube.ErrCleanupPermissionsUnavailable, err)
	}
	return nil
}

func (s *Service) verifyUpgradeRuntime(ctx context.Context, facts *runtimeAccessContext, profile provider.RuntimeAccessProfile) error {
	if _, _, err := s.enrollmentAuthority(ctx, facts.enrollment); err != nil {
		return errors.Join(kube.ErrCleanupPermissionsUnavailable, err)
	}
	a := facts.attachment
	raw, err := s.revealManagedCredential(ctx, a.OrgID, a.VaultKey, a.CredentialExpiry)
	if err != nil {
		return errors.Join(kube.ErrCleanupPermissionsUnavailable, err)
	}
	cfg, err := kube.ParseConfig(raw, map[string]string{"namespace": a.Namespace, "namespace_uid": a.NamespaceUID, "cluster_ca": a.TrustFingerprint})
	if err != nil {
		return err
	}
	if cfg.Details["cluster_ca"] != a.TrustFingerprint || endpointFingerprint(cfg.REST.Host) != a.EndpointFingerprint {
		return ErrConflict
	}
	client, err := kube.New(cfg)
	if err != nil {
		return err
	}
	if checkErr := kube.VerifyRuntimeIdentity(ctx, client.Kube, a.Namespace, a.NamespaceUID, a.ServiceAccountName, a.ServiceAccountUID); checkErr != nil {
		return checkErr
	}
	if checkErr := client.CheckRuntimeBoundaries(ctx); checkErr != nil {
		return checkErr
	}
	if checkErr := client.CheckRuntimeAccess(ctx, profile); checkErr != nil {
		return checkErr
	}
	_, _, err = s.enrollmentAuthority(ctx, facts.enrollment)
	return err
}

// QualifiedRuntimeAccess rechecks current enrollment authority, exact role state
// and runtime permissions. The returned record is evidence for this call only.
func (s *Service) QualifiedRuntimeAccess(ctx context.Context, a *RuntimeAttachment, client *kube.Client) (*RuntimeAccessRecord, error) {
	if a == nil || client == nil {
		return nil, kube.ErrCleanupPermissionsUnavailable
	}
	enrollment, err := s.Clusters.GetClusterOperation(ctx, a.OrgID, a.CloudID, a.OperationID)
	if err != nil {
		return nil, err
	}
	resource, err := s.Clusters.GetManagedResource(ctx, a.OrgID, a.ClusterID)
	if err != nil {
		return nil, err
	}
	record, err := s.checkRuntimeAccessIdentity(ctx, a, client)
	if err != nil {
		return nil, err
	}
	if verifyErr := s.verifyRuntimeAccessCredential(ctx, a, enrollment, resource, client); verifyErr != nil {
		return nil, verifyErr
	}
	store, ok := s.Clusters.(RuntimeAccessStore)
	if !ok {
		return nil, kube.ErrCleanupPermissionsUnavailable
	}
	current, err := store.GetRuntimeAccess(ctx, a.OrgID, a.ID)
	if err != nil || current == nil || current.Revision != record.Revision {
		return nil, errors.Join(kube.ErrCleanupPermissionsUnavailable, err)
	}
	return current, nil
}
