package cloudinfra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func (s *Service) PreviewRuntimeDetach(ctx context.Context, p security.Principal, cloud, attachment string) (*ClusterOperation, error) {
	if checkErr := s.clusterEnabled(); checkErr != nil {
		return nil, checkErr
	}
	a, err := s.Clusters.GetRuntimeAttachment(ctx, p.OrgID, attachment)
	if err != nil {
		return nil, err
	}
	if a.CloudID != cloud || a.DeletedAt != nil {
		return nil, ErrConflict
	}
	r, err := s.Clusters.GetManagedResource(ctx, p.OrgID, a.ClusterID)
	if err != nil {
		return nil, err
	}
	if r.Observation == nil {
		return nil, ErrConflict
	}
	profile, err := s.authorize(ctx, p, cloud)
	if err != nil {
		return nil, err
	}
	_, access, err := s.clusterAccount(ctx, p, r.ConnectionID, provider.ClusterDiscovery)
	if err != nil {
		return nil, err
	}
	in := ClusterInput{Kind: "detach-runtime", Provider: r.Provider, AccountID: r.AccountID, ConnectionID: r.ConnectionID, ConnectionGeneration: access.Generation, CloudRevision: profile.Revision, ResourceID: r.ID, ResourceRevision: r.Revision, Ref: r.Observation.Ref, Retirement: &ClusterRetirementInput{Attachment: a}}
	reader, access, err := s.clusterAuthority(ctx, p, cloud, in)
	if err != nil {
		return nil, err
	}
	review, err := s.prepareClusterRetirement(ctx, p, cloud, in, reader, access, "")
	if err != nil {
		return nil, err
	}
	in.Retirement.ReviewDigest = review.Digest
	return s.PreviewCluster(ctx, p, cloud, in)
}

func validDetachObjects(a *RuntimeAttachment) bool {
	if a == nil || a.ID == "" || a.NamespaceUID == "" || a.ServiceAccountUID == "" || len(a.RBACObjects) != 5 {
		return false
	}
	seen := map[string]bool{}
	for _, obj := range a.RBACObjects {
		if obj.UID == "" || seen[obj.Kind] {
			return false
		}
		seen[obj.Kind] = true
		switch obj.Kind {
		case "ServiceAccount", "Role", "RoleBinding":
			if obj.Name != a.ServiceAccountName || obj.Namespace != a.Namespace {
				return false
			}
		case "ClusterRole", "ClusterRoleBinding":
			if obj.Name != "loomplane-"+a.ID || obj.Namespace != "" {
				return false
			}
		default:
			return false
		}
		if obj.Kind == "ServiceAccount" && obj.UID != a.ServiceAccountUID {
			return false
		}
	}
	return true
}

func (s *Service) detachClient(ctx context.Context, o *ClusterOperation, reader provider.ClusterReader, access provider.AccountAccess) (kubeclient.Interface, error) {
	if checkErr := s.Adapters.Require(o.Input.Provider, provider.ClusterBootstrapAccess); checkErr != nil {
		return nil, checkErr
	}
	r, err := s.retirementResource(ctx, o.Principal, o.CloudID, o.Input)
	if err != nil {
		return nil, err
	}
	a := o.Input.Retirement.Attachment
	observation, err := reader.GetCluster(ctx, access, o.Input.Ref)
	if err != nil {
		return nil, err
	}
	if observation.Ref != o.Input.Ref || observation.Endpoint != r.Observation.Endpoint || endpointFingerprint(observation.Endpoint) != a.EndpointFingerprint || a.TrustFingerprint == "" {
		return nil, ErrConflict
	}
	bootstrap, ok := reader.(provider.ClusterBootstrap)
	if !ok {
		return nil, ErrInvalid
	}
	now := s.now().UTC()
	digest := clusterDigest(o.Input)
	issuance := &ClusterOperation{ID: uuid.NewString(), OrgID: o.OrgID, CloudID: o.CloudID, Kind: "management-observation", SchemaVersion: 1, Input: o.Input, Digest: digest, Revision: 1, Status: "preview", Principal: o.Principal, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute), Steps: []ClusterStep{{Name: "management-access", State: "pending", RequestDigest: digest}}}
	if checkErr := s.Clusters.CreateClusterPreview(ctx, issuance); checkErr != nil {
		return nil, checkErr
	}
	access.BeforeEffect = func(effectCtx context.Context) error {
		if checkErr := s.Adapters.Require(o.Input.Provider, provider.ClusterBootstrapAccess); checkErr != nil {
			return checkErr
		}
		if checkErr := s.retirementEffectGuard(effectCtx, o); checkErr != nil {
			return checkErr
		}
		if issuance.Steps[0].State == "pending" {
			at := s.now().UTC()
			issuance.Steps[0].State = "attempted"
			issuance.Steps[0].AttemptedAt = &at
			return s.saveCluster(effectCtx, issuance)
		}
		return nil
	}
	if checkErr := access.BeforeEffect(ctx); checkErr != nil {
		return nil, checkErr
	}
	material, err := bootstrap.BootstrapCluster(ctx, access, o.Input.Ref, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	issuance.Status = "succeeded"
	issuance.Steps[0].State = "completed"
	if checkErr := s.saveCluster(ctx, issuance); checkErr != nil {
		return nil, checkErr
	}
	hash := sha256.Sum256(material.CAData)
	if material.Endpoint != observation.Endpoint || hex.EncodeToString(hash[:]) != a.TrustFingerprint || !material.ExpiresAt.After(s.now().Add(time.Minute)) {
		return nil, ErrConflict
	}
	return kubeclient.NewForConfig(&rest.Config{Host: material.Endpoint, BearerToken: material.Token, Timeout: 10 * time.Second, TLSClientConfig: rest.TLSClientConfig{CAData: material.CAData, CertData: material.ClientCertificate, KeyData: material.ClientKey}})
}

func (s *Service) reconcileRuntimeDetach(ctx context.Context, o *ClusterOperation, reader provider.ClusterReader, access provider.AccountAccess) error {
	a := o.Input.Retirement.Attachment
	if !validDetachObjects(a) || len(o.Steps) != len(a.RBACObjects)+2 {
		return ErrInvalid
	}
	store, ok := s.Clusters.(ClusterRetirementStore)
	if !ok {
		return ErrInvalid
	}
	if o.Steps[0].State == "pending" {
		if checkErr := s.retirementEffectGuard(ctx, o); checkErr != nil {
			return s.clusterAttention(ctx, o, "Detach authority or dependencies changed.")
		}
		now := s.now().UTC()
		o.Steps[0].State = "completed"
		o.Steps[0].CompletedAt = &now
		if checkErr := store.DisableClusterAttachment(ctx, o); checkErr != nil {
			return fmt.Errorf("disable runtime: %w", checkErr)
		}
	}
	needsClient := false
	for i := range a.RBACObjects {
		if o.Steps[i+1].State != "completed" {
			needsClient = true
		}
	}
	if needsClient {
		client, err := s.detachClient(ctx, o, reader, access)
		if err != nil {
			return s.clusterAttention(ctx, o, "Detached runtime cleanup needs current management access.")
		}
		for i, obj := range a.RBACObjects {
			step := &o.Steps[i+1]
			if step.State == "completed" {
				continue
			}
			get, remove := detachObject(client, obj)
			current, err := get(ctx)
			if apierrors.IsNotFound(err) {
				if step.State == "pending" {
					return s.clusterAttention(ctx, o, "Runtime object disappeared before its reviewed cleanup attempt.")
				}
				now := s.now().UTC()
				step.State = "completed"
				step.CompletedAt = &now
				if checkErr := s.saveCluster(ctx, o); checkErr != nil {
					return checkErr
				}
				continue
			}
			if err != nil {
				return s.clusterAttention(ctx, o, "Runtime cleanup observation failed.")
			}
			if string(current.GetUID()) != obj.UID {
				return s.clusterAttention(ctx, o, "Runtime object UID changed. Replacement objects are preserved.")
			}
			if step.State != "pending" {
				return s.clusterAttention(ctx, o, "Runtime delete outcome is unresolved. No repeated delete was sent.")
			}
			ns, err := client.CoreV1().Namespaces().Get(ctx, a.Namespace, metav1.GetOptions{})
			if err != nil || string(ns.UID) != a.NamespaceUID {
				return s.clusterAttention(ctx, o, "Runtime namespace identity changed.")
			}
			if checkErr := s.retirementEffectGuard(ctx, o); checkErr != nil {
				return s.clusterAttention(ctx, o, "Runtime cleanup authority changed.")
			}
			now := s.now().UTC()
			step.State = "attempted"
			step.AttemptedAt = &now
			step.RemoteResourceID = obj.UID
			if checkErr := s.saveCluster(ctx, o); checkErr != nil {
				return fmt.Errorf("save runtime cleanup: %w", checkErr)
			}
			if checkErr := s.retirementEffectGuard(ctx, o); checkErr != nil {
				return s.clusterAttention(ctx, o, "Runtime cleanup authority changed.")
			}
			uid := types.UID(obj.UID)
			if checkErr := remove(ctx, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); checkErr != nil {
				return s.clusterWait(ctx, o, checkErr)
			}
			step.State = "observed"
			if checkErr := s.saveCluster(ctx, o); checkErr != nil {
				return fmt.Errorf("save runtime cleanup: %w", checkErr)
			}
			current, err = get(ctx)
			if !apierrors.IsNotFound(err) {
				if err == nil && string(current.GetUID()) != obj.UID {
					return s.clusterAttention(ctx, o, "Replacement runtime object is retained.")
				}
				return s.clusterWait(ctx, o, ErrPending)
			}
			completedAt := s.now().UTC()
			step.State = "completed"
			step.CompletedAt = &completedAt
			if checkErr := s.saveCluster(ctx, o); checkErr != nil {
				return fmt.Errorf("save runtime cleanup: %w", checkErr)
			}
		}
	}
	keysStore, ok := s.Clusters.(interface {
		ClusterRetirementVaultKeys(context.Context, *ClusterOperation) ([]string, error)
	})
	if !ok {
		return ErrInvalid
	}
	keys, err := keysStore.ClusterRetirementVaultKeys(ctx, o)
	if err != nil {
		return err
	}
	if s.Secrets == nil {
		return s.clusterAttention(ctx, o, "Vault cleanup is unavailable.")
	}
	vaultStep := &o.Steps[len(o.Steps)-1]
	if vaultStep.State == "pending" {
		at := s.now().UTC()
		vaultStep.State = "attempted"
		vaultStep.AttemptedAt = &at
		if checkErr := s.saveCluster(ctx, o); checkErr != nil {
			return checkErr
		}
	}
	for _, key := range keys {
		if checkErr := s.retirementEffectGuard(ctx, o); checkErr != nil {
			return s.clusterAttention(ctx, o, "Vault cleanup authority changed.")
		}
		if err := s.Secrets.Delete(ctx, o.OrgID, key); err != nil && !errors.Is(err, connections.ErrNotFound) {
			return s.clusterAttention(ctx, o, "Runtime detached. Vault cleanup must be retried.")
		}
	}
	now := s.now().UTC()
	last := &o.Steps[len(o.Steps)-1]
	last.State = "completed"
	last.CompletedAt = &now
	o.Status = "succeeded"
	o.Error = ""
	return s.saveCluster(ctx, o)
}

func detachObject(k kubeclient.Interface, obj RBACObjectReceipt) (get func(context.Context) (metav1.Object, error), remove func(context.Context, metav1.DeleteOptions) error) {
	switch obj.Kind {
	case "RoleBinding":
		return func(ctx context.Context) (metav1.Object, error) {
				return k.RbacV1().RoleBindings(obj.Namespace).Get(ctx, obj.Name, metav1.GetOptions{})
			}, func(ctx context.Context, o metav1.DeleteOptions) error {
				return k.RbacV1().RoleBindings(obj.Namespace).Delete(ctx, obj.Name, o)
			}
	case "ClusterRoleBinding":
		return func(ctx context.Context) (metav1.Object, error) {
				return k.RbacV1().ClusterRoleBindings().Get(ctx, obj.Name, metav1.GetOptions{})
			}, func(ctx context.Context, o metav1.DeleteOptions) error {
				return k.RbacV1().ClusterRoleBindings().Delete(ctx, obj.Name, o)
			}
	case "Role":
		return func(ctx context.Context) (metav1.Object, error) {
				return k.RbacV1().Roles(obj.Namespace).Get(ctx, obj.Name, metav1.GetOptions{})
			}, func(ctx context.Context, o metav1.DeleteOptions) error {
				return k.RbacV1().Roles(obj.Namespace).Delete(ctx, obj.Name, o)
			}
	case "ClusterRole":
		return func(ctx context.Context) (metav1.Object, error) {
				return k.RbacV1().ClusterRoles().Get(ctx, obj.Name, metav1.GetOptions{})
			}, func(ctx context.Context, o metav1.DeleteOptions) error {
				return k.RbacV1().ClusterRoles().Delete(ctx, obj.Name, o)
			}
	default:
		return func(ctx context.Context) (metav1.Object, error) {
				return k.CoreV1().ServiceAccounts(obj.Namespace).Get(ctx, obj.Name, metav1.GetOptions{})
			}, func(ctx context.Context, o metav1.DeleteOptions) error {
				return k.CoreV1().ServiceAccounts(obj.Namespace).Delete(ctx, obj.Name, o)
			}
	}
}
