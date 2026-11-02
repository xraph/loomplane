package connections

import (
	"context"
	"fmt"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
)

// RegistryCredential is for the registry reconciler, which also handles a
// connection waiting for its ingress address. It never grants deployment access.
func (s *Service) RegistryCredential(ctx context.Context, org, id string) (*Connection, []byte, error) {
	c, err := s.store.GetConnection(ctx, org, id)
	if err != nil {
		return nil, nil, err
	}
	if c.Cloud != Kubernetes || c.Details["registry_hostname"] == "" || c.Message == replacementInProgressMessage || c.Method == MethodManagedKubernetes {
		return nil, nil, ErrNotReady
	}
	var raw []byte
	switch c.Method {
	case MethodToken:
		raw, err = s.secrets.Reveal(ctx, org, id)
	case MethodInCluster:
		raw = kubernetes.InClusterCredential()
	default:
		return nil, nil, ErrNotReady
	}
	if err != nil {
		return nil, nil, err
	}
	return c, raw, nil
}

// WithRegistryLock serializes builds and registry maintenance with credential
// replacement across processes. Registry connections cannot use OAuth grants.
func (s *Service) WithRegistryLock(ctx context.Context, org, id string, run func() error) error {
	if s.grants == nil {
		return fmt.Errorf("%w: registry coordination is unavailable", ErrUnavailable)
	}
	return s.grants.WithConnectionLock(ctx, org, id, run)
}

func (s *Service) RefreshRegistry(ctx context.Context, org, id string) (*Connection, []byte, error) {
	c, raw, err := s.RegistryCredential(ctx, org, id)
	if err != nil {
		return nil, nil, err
	}
	checker, err := s.checker(c.Cloud)
	if err != nil {
		return nil, nil, err
	}
	checked, err := s.check(ctx, c.Cloud, checker, raw, c.Details)
	if err != nil {
		return nil, nil, err
	}
	before := AccountIdentity(c.Cloud, c.Account, c.Details)
	if before == "" || before != AccountIdentity(c.Cloud, checked.Account, checked.Details) {
		return nil, nil, ErrChanged
	}
	apply(c, checked, s.now().UTC())
	if operationErr := s.store.UpdateConnection(ctx, c); operationErr != nil {
		return nil, nil, operationErr
	}
	return c, raw, nil
}

func (s *PgStore) RegistryAccessAllowed(ctx context.Context, org, id string) error {
	var blocked bool
	if operationErr := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_registry_states WHERE org_id=$1 AND connection_id=$2 AND (status='removed' OR body->'action'->>'kind' IN ('backup','restore','cleanup','remove')))`, org, id).Scan(&blocked); operationErr != nil {
		return operationErr
	}
	if blocked {
		return ErrNotReady
	}
	return nil
}
