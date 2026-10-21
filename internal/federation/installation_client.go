package federation

import (
	"context"
	"time"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// Shared by live attestation and launch delivery. No ambient kubeconfig or
// unverified credential can choose the cluster used for a destination operation.
func openInstallationKubernetes(ctx context.Context, credentials InstallationCredentials, org string, d domain.Destination) (*kube.Client, error) {
	if credentials == nil || org == "" || d.OrgID != org || d.Identity.Provider != "kubernetes" || d.Quarantined {
		return nil, ErrUnverified
	}
	connection, raw, err := credentials.Credential(ctx, org, d.ConnectionID)
	defer clear(raw)
	if err != nil {
		return nil, err
	}
	if connection == nil || connection.OrgID != org || connection.ID != d.ConnectionID {
		return nil, ErrIdentityChanged
	}
	physical, err := ConnectionIdentity(connection, time.Now())
	if err != nil {
		return nil, err
	}
	if physical != d.Identity {
		return nil, ErrIdentityChanged
	}
	cfg, err := kube.ParseConfig(raw, connection.Details)
	if err != nil {
		return nil, err
	}
	if cfg.Details["cluster_ca"] != d.Identity.Account {
		return nil, ErrIdentityChanged
	}
	return kube.New(cfg)
}
