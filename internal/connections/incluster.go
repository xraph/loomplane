package connections

import (
	"context"
	"fmt"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/security"
)

// InClusterInput connects the Kubernetes cluster Loomplane runs in. There's
// no credential: Loomplane acts as its own service account, so the
// settings are all it takes, starting with the namespace to deploy into.
type InClusterInput struct {
	Name     string            `json:"name"`
	Settings map[string]string `json:"settings"`
}

// InClusterAvailable says whether organizations may connect the cluster
// Loomplane runs in: the operator turned it on, and Loomplane found its
// pod's service account.
func InClusterAvailable() bool { return kubernetes.CurrentInCluster() != nil }

// CreateInCluster checks the cluster Loomplane runs in, as its own service
// account, and stores the connection if the namespace can be reached.
// Vault holds nothing for it.
func (s *Service) CreateInCluster(ctx context.Context, p security.Principal, in InClusterInput) (*Connection, error) {
	if err := s.authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return nil, err
	}
	if !InClusterAvailable() {
		return nil, fmt.Errorf("%w: this Loomplane doesn't connect the cluster it runs in", ErrInvalid)
	}
	return s.create(ctx, p, "", Kubernetes, in.Name, nil, kubernetes.InClusterCredential(), in.Settings, MethodInCluster)
}

// PastedGrant implements pastedGrantAdvice. A Kubernetes connection's
// grant is only ever the in-cluster credential, which Loomplane makes
// itself and never takes from anyone.
func (KubernetesChecker) PastedGrant() string {
	return "paste a self-contained kubeconfig, or connect the cluster Loomplane runs in from the connect dialog"
}
