package connections

import (
	"context"
	"errors"
	"reflect"
	"time"
)

// ErrCredentialRefreshNeeded means observation needs an already usable runtime
// credential. Observation never renews the namespace token itself.
var ErrCredentialRefreshNeeded = errors.New("runtime credential needs refresh before observation")

type ManagedObservationCredentialResolver interface {
	ResolveManagedCredentialForObservation(context.Context, Connection) (ManagedCredential, error)
}

// ObservationCredential authenticates read-only prerequisite observations.
// Managed namespace maintenance is excluded; provider authentication may still
// exchange short-lived access tokens through its existing credential path.
func (s *Service) ObservationCredential(ctx context.Context, org, id string) (*Connection, []byte, error) {
	c, err := s.store.GetConnection(ctx, org, id)
	if err != nil {
		return nil, nil, err
	}
	if c.OrgID != org || c.Cloud != Kubernetes || c.Status != StatusReady || c.Message == replacementInProgressMessage {
		return nil, nil, ErrNotReady
	}
	if c.Method != MethodManagedKubernetes {
		if c.Method != MethodToken && c.Method != MethodInCluster {
			return nil, nil, ErrNotReady
		}
		return s.Credential(ctx, org, id)
	}
	resolver, ok := s.ManagedCredentials.(ManagedObservationCredentialResolver)
	if !ok {
		return nil, nil, ErrCredentialRefreshNeeded
	}
	credential, err := resolver.ResolveManagedCredentialForObservation(ctx, *c)
	if err != nil {
		return nil, nil, err
	}
	if len(credential.Kubeconfig) == 0 || !credential.ExpiresAt.After(s.now().Add(time.Minute)) || credential.TrustRevision != c.Generation {
		return nil, nil, ErrCredentialRefreshNeeded
	}
	current, err := s.store.GetConnection(ctx, org, id)
	if err != nil {
		return nil, nil, err
	}
	if current.Status != c.Status || current.Generation != c.Generation || current.Message == replacementInProgressMessage || !reflect.DeepEqual(current.Details, c.Details) {
		return nil, nil, ErrChanged
	}
	return current, credential.Kubeconfig, nil
}
