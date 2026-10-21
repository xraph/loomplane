package workloadidentity

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"

	"github.com/xraph/loomplane/internal/config"
)

type cleanupAdminFixture struct {
	EnrollmentIssuer
	NodeReader
	closed atomic.Int32
}

func (*cleanupAdminFixture) Health(context.Context) error { return nil }
func (a *cleanupAdminFixture) Close() error               { a.closed.Add(1); return nil }

type cleanupRuntimeStore struct {
	entered chan struct{}
	calls   atomic.Int32
}

func (s *cleanupRuntimeStore) Cleanup(ctx context.Context, org string, limit int, issuer EnrollmentIssuer) (CleanupResult, error) {
	if org != "org" || limit != 20 || issuer == nil {
		return CleanupResult{}, ErrIdentity
	}
	if s.calls.Add(1) == 1 {
		close(s.entered)
	}
	<-ctx.Done()
	return CleanupResult{Attempted: 1, Failed: 1}, ctx.Err()
}
func cleanupConfig() config.FederationIdentityConfig {
	return config.FederationIdentityConfig{Enabled: true, OrganizationID: "org", TrustDomain: "loomplane.test", AdminSocket: "unix:///run/spire/server.sock"}
}
func TestCleanupRuntimeLifecycle(t *testing.T) {
	store := &cleanupRuntimeStore{entered: make(chan struct{})}
	runtime, err := NewCleanupRuntime(cleanupConfig(), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	admin := &cleanupAdminFixture{}
	runtime.openAdmin = func(context.Context, string, string, string) (cleanupAdmin, error) { return admin, nil }
	if err := runtime.Health(t.Context()); err == nil {
		t.Fatal("unstarted runtime healthy")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(ctx); err == nil {
		t.Fatal("duplicate start accepted")
	}
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("cleanup not started")
	}
	if err := runtime.Health(t.Context()); err != nil {
		t.Fatal(err)
	}
	cancel()
	stop, stopCancel := context.WithTimeout(t.Context(), time.Second)
	defer stopCancel()
	if err := runtime.Stop(stop); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Stop(stop); err != nil {
		t.Fatal(err)
	}
	if admin.closed.Load() != 1 || store.calls.Load() != 1 {
		t.Fatal("lifecycle did not stop exactly once")
	}
	if err := runtime.Health(t.Context()); err == nil {
		t.Fatal("stopped runtime healthy")
	}
	state := runtime.Status()
	if state.LastSweep.IsZero() || state.Result.Failed != 1 || !errors.Is(state.Err, context.Canceled) {
		t.Fatal("last sweep status missing", state)
	}
}
func TestCleanupRuntimeStartupFailureAndDisabled(t *testing.T) {
	disabled, err := NewCleanupRuntime(config.FederationIdentityConfig{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	disabled.openAdmin = func(context.Context, string, string, string) (cleanupAdmin, error) {
		t.Fatal("disabled runtime opened admin socket")
		return nil, nil
	}
	if stepErr := disabled.Start(t.Context()); stepErr != nil {
		t.Fatal(stepErr)
	}
	if stepErr := disabled.Health(t.Context()); stepErr != nil {
		t.Fatal(stepErr)
	}
	if stepErr := disabled.Stop(t.Context()); stepErr != nil {
		t.Fatal(stepErr)
	}
	if _, configErr := NewCleanupRuntime(cleanupConfig(), nil, nil); configErr == nil {
		t.Fatal("missing store accepted")
	}
	runtime, err := NewCleanupRuntime(cleanupConfig(), &cleanupFixture{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	unavailable := errors.New("admin unavailable")
	runtime.openAdmin = func(context.Context, string, string, string) (cleanupAdmin, error) { return nil, unavailable }
	if err := runtime.Start(t.Context()); !errors.Is(err, unavailable) {
		t.Fatal("startup failure hidden", err)
	}
	if err := runtime.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupRuntimeReportsBacklogWithoutFailingHealth(t *testing.T) {
	reported := make(chan CleanupResult, 1)
	runtime, err := NewCleanupRuntime(cleanupConfig(), &cleanupFixture{}, func(result CleanupResult, _ error) { reported <- result })
	if err != nil {
		t.Fatal(err)
	}
	admin := &cleanupAdminFixture{}
	runtime.openAdmin = func(context.Context, string, string, string) (cleanupAdmin, error) { return admin, nil }
	if stepErr := runtime.Start(t.Context()); stepErr != nil {
		t.Fatal(stepErr)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if stopErr := runtime.Stop(ctx); stopErr != nil {
			t.Error(stopErr)
		}
	})
	select {
	case result := <-reported:
		if result.Failed != 1 {
			t.Fatal("failure hidden")
		}
	case <-time.After(time.Second):
		t.Fatal("missing cleanup result")
	}
	if runtime.Status().Err == nil {
		t.Fatal("backlog failure not retained")
	}
	if healthErr := runtime.Health(t.Context()); healthErr != nil {
		t.Fatal("backlog conflated with worker failure", healthErr)
	}
}

func TestCleanupRuntimeSharesAndFencesAdministrativeAuthority(t *testing.T) {
	fixture := &adminFixture{domain: "loomplane.test", entries: map[string]*spiretypes.Entry{}}
	cfg := cleanupConfig()
	cfg.AdminSocket = serveAdminFixture(t, fixture)
	runtime, err := NewCleanupRuntime(cfg, &cleanupFixture{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding := bindingFixture(t)
	if _, issueErr := runtime.Register(t.Context(), binding); issueErr == nil {
		t.Fatal("authority available before startup")
	}
	if startErr := runtime.Start(t.Context()); startErr != nil {
		t.Fatal(startErr)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if stopErr := runtime.Stop(ctx); stopErr != nil {
			t.Error(stopErr)
		}
	})
	if _, issueErr := runtime.Register(t.Context(), binding); issueErr != nil {
		t.Fatal(issueErr)
	}
	if revokeErr := runtime.Revoke(t.Context(), binding.EntryID); revokeErr != nil {
		t.Fatal(revokeErr)
	}
	if stopErr := runtime.Stop(t.Context()); stopErr != nil {
		t.Fatal(stopErr)
	}
	if _, issueErr := runtime.Register(t.Context(), binding); issueErr == nil {
		t.Fatal("authority retained after shutdown")
	}
	if _, readErr := runtime.GetAgent(t.Context(), nil); readErr == nil {
		t.Fatal("parent reader retained after shutdown")
	}
}
