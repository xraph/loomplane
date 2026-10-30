package cloudinfra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type runtimeAccessTransport struct {
	next  http.RoundTripper
	path  string
	guard func(context.Context) error
}

func (t *runtimeAccessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		if req.Method != http.MethodPut || req.URL.Path != t.path || t.guard == nil {
			return nil, security.ErrForbidden
		}
		if err := t.guard(req.Context()); err != nil {
			return nil, err
		}
	}
	return t.next.RoundTrip(req)
}

// runtimeAccessClient issues management credentials under the current upgrade
// actor. Its transport can only update the exact reviewed namespaced Role.
func (s *Service) runtimeAccessClient(ctx context.Context, p security.Principal, cloud string, in ClusterInput, facts *runtimeAccessContext, reader provider.ClusterReader, access provider.AccountAccess, guard func(context.Context) error) (kubeclient.Interface, error) {
	if err := s.Adapters.Require(in.Provider, provider.ClusterBootstrapAccess); err != nil {
		return nil, err
	}
	bootstrap, ok := reader.(provider.ClusterBootstrap)
	if !ok {
		return nil, provider.ErrInfrastructureUnsupported
	}
	observation, err := reader.GetCluster(ctx, access, in.Ref)
	if err != nil {
		return nil, err
	}
	a := facts.attachment
	if observation.Ref != in.Ref || observation.State != "running" || observation.Endpoint != facts.resource.Observation.Endpoint || endpointFingerprint(observation.Endpoint) != a.EndpointFingerprint || observation.CAHash != "" && observation.CAHash != a.TrustFingerprint {
		return nil, ErrConflict
	}
	now := s.now().UTC()
	digest := clusterDigest(in)
	audit := &ClusterOperation{ID: uuid.NewString(), OrgID: p.OrgID, CloudID: cloud, Kind: "management-observation", SchemaVersion: 1, Input: in, Digest: digest, Revision: 1, Status: "preview", Principal: p, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute), Steps: []ClusterStep{{Name: "management-access", State: "pending", RequestDigest: digest}}}
	if err = s.Clusters.CreateClusterPreview(ctx, audit); err != nil {
		return nil, err
	}
	check := func(call context.Context) error {
		if err = s.clusterEnabled(); err != nil {
			return err
		}
		if _, _, checkErr := s.clusterAuthority(call, p, cloud, in); checkErr != nil {
			return checkErr
		}
		current, checkErr := s.runtimeAccessContext(call, p, cloud, in)
		if checkErr != nil || current.attachment.Revision != a.Revision {
			return errors.Join(ErrConflict, checkErr)
		}
		if _, checkErr = s.runtimeAccessGrant(call, p, cloud, a); checkErr != nil {
			return checkErr
		}
		if guard != nil {
			if checkErr = guard(call); checkErr != nil {
				return checkErr
			}
		}
		if audit.Steps[0].State == "pending" {
			at := s.now().UTC()
			audit.Steps[0].State = "attempted"
			audit.Steps[0].AttemptedAt = &at
			return s.saveCluster(call, audit)
		}
		return nil
	}
	access.BeforeEffect = check
	if err = check(ctx); err != nil {
		return nil, err
	}
	material, err := bootstrap.BootstrapCluster(ctx, access, in.Ref, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(material.CAData)
	if material.Endpoint != observation.Endpoint || hex.EncodeToString(sum[:]) != a.TrustFingerprint || !material.ExpiresAt.After(s.now().Add(time.Minute)) {
		return nil, ErrConflict
	}
	audit.Status = "succeeded"
	audit.Steps[0].State = "completed"
	if err = s.saveCluster(ctx, audit); err != nil {
		return nil, err
	}
	cfg := &rest.Config{Host: material.Endpoint, BearerToken: material.Token, Timeout: 10 * time.Second, TLSClientConfig: rest.TLSClientConfig{CAData: material.CAData, CertData: material.ClientCertificate, KeyData: material.ClientKey}}
	var client kubeclient.Interface
	liveGuard := func(call context.Context) error {
		if guard == nil || client == nil {
			return security.ErrForbidden
		}
		if checkErr := guard(call); checkErr != nil {
			return checkErr
		}
		role, binding, checkErr := kube.ReadRuntimeAccess(call, client, *in.RuntimeAccess)
		if checkErr != nil {
			return checkErr
		}
		if role.ResourceVersion != in.RuntimeAccess.RoleResourceVersion || kube.RuntimeAccessDigest(role.Rules) != in.RuntimeAccess.RoleRulesDigest || binding.ResourceVersion != in.RuntimeAccess.RoleBindingResourceVersion || kube.RuntimeBindingDigest(binding) != in.RuntimeAccess.RoleBindingSubjectsRefDigest {
			return ErrConflict
		}
		return guard(call)
	}
	cfg.WrapTransport = func(next http.RoundTripper) http.RoundTripper {
		return &runtimeAccessTransport{next: next, path: "/apis/rbac.authorization.k8s.io/v1/namespaces/" + a.Namespace + "/roles/" + a.ServiceAccountName, guard: liveGuard}
	}
	client, err = kubeclient.NewForConfig(cfg)
	return client, err
}
