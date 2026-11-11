package cloudinfra

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
)

type registryShutdownCredentials struct {
	registryCredentials
	cancel context.CancelFunc
}

func (c registryShutdownCredentials) RefreshRegistry(context.Context, string, string) (*connections.Connection, []byte, error) {
	c.cancel()
	return nil, nil, ErrInvalid
}

type registryFinalizerStore struct {
	registryTestStore
	finalize func(context.Context, *RegistryState) error
}

func (s *registryFinalizerStore) SaveRegistryState(ctx context.Context, state *RegistryState) error {
	return s.finalize(ctx, state)
}

func TestRegistryFailureFinalizerUsesBoundedIndependentContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	saveErr := errors.New("synthetic persistence failure")
	saved := false
	st := &registryFinalizerStore{registryTestStore: registryTestStore{state: &RegistryState{OrgID: "org", ConnectionID: "registry", Status: "waiting"}}}
	st.finalize = func(saveCtx context.Context, state *RegistryState) error {
		saved = true
		if !errors.Is(ctx.Err(), context.Canceled) || saveCtx.Err() != nil {
			t.Fatal("failure finalizer lost its independent cleanup window")
		}
		deadline, ok := saveCtx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 10*time.Second {
			t.Fatal("failure finalizer has no bounded cleanup deadline")
		}
		if state.Status != "needs_attention" || state.Message != ErrInvalid.Error() {
			t.Fatalf("failure state lost: %+v", state)
		}
		return saveErr
	}
	svc := NewService(Deps{})
	err := svc.reconcileManagedRegistry(ctx, st, registryShutdownCredentials{cancel: cancel}, &connections.Connection{OrgID: "org", ID: "registry"}, nil)
	if !saved || !errors.Is(err, ErrInvalid) || !errors.Is(err, saveErr) {
		t.Fatalf("failed persistence was hidden: saved=%v err=%v", saved, err)
	}
}
