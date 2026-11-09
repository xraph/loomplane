package cloudinfra

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
)

type instanceCredentialStore struct {
	*clusterMemory
	reads int
}

func (s *instanceCredentialStore) GetRuntimeAttachment(context.Context, string, string) (*RuntimeAttachment, error) {
	s.reads++
	return nil, errors.New("instance marker reached attachment lookup")
}

func TestInstanceGenericManagedResolversRefused(t *testing.T) {
	for _, details := range []map[string]string{
		{"managed_instance_id": "instance"},
		{"managed_environment_id": "environment"},
		{"managed_instance_id": "instance", "managed_environment_id": "environment"},
	} {
		s := NewService(Deps{})
		store := &instanceCredentialStore{}
		s.Clusters = store
		s.Secrets = bootstrapSecrets{}
		c := connections.Connection{Cloud: connections.Kubernetes, Method: connections.MethodManagedKubernetes, Details: details}
		for _, resolve := range []func(context.Context, connections.Connection) (connections.ManagedCredential, error){s.ResolveManagedCredential, s.ResolveManagedCredentialForObservation} {
			got, err := resolve(t.Context(), c)
			if !errors.Is(err, connections.ErrChanged) || len(got.Kubeconfig) != 0 {
				t.Errorf("generic resolver admitted instance scope: %v", err)
			}
		}
		if store.reads != 0 {
			t.Fatal("instance marker reached attachment lookup")
		}
	}
}
