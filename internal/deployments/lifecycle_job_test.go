package deployments

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/pkg/manifest"
)

type inaccessibleLifecycleStore struct {
	LifecycleJobStore
	called bool
}

func (s *inaccessibleLifecycleStore) BeginLifecycleJob(context.Context, LifecycleJobRun) (*LifecycleJobState, error) {
	s.called = true
	return nil, errors.New("unexpected store access")
}
func TestLifecycleJobNeedsCurrentAuthority(t *testing.T) {
	run := LifecycleJobRun{OrgID: "org", ProjectID: "project", FleetID: "fleet", InstanceID: "instance", EnvironmentID: "environment", OperationID: "operation", Generation: 1, OwnershipEpoch: 1, Name: "bootstrap", Namespace: "apps", NamespaceUID: "namespace-uid", ConnectionID: "connection", ClusterCA: strings.Repeat("a", 64), Architecture: "arm64", CapabilityRevision: owned.LifecycleCapabilityRevision, Job: manifest.LifecycleJob{Phase: "bootstrap", Image: "registry.example.test/bootstrap@sha256:" + strings.Repeat("a", 64), TimeoutSeconds: 30, MaxAttempts: 1}}
	store := &inaccessibleLifecycleStore{}
	runner := &LifecycleJobs{Store: store}
	if _, err := runner.RunLifecycleJob(t.Context(), run); !errors.Is(err, owned.ErrLifecycleAuthority) || store.called {
		t.Fatal("missing runtime authority accessed the store", err)
	}
}
