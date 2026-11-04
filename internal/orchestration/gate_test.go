package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/metering"
)

func TestStartsAreRefusedByTheGateBeforeTheEngine(t *testing.T) {
	d := NewDeployments()
	d.BindGate(gateFunc(func(context.Context, string, metering.Kind, bool) error {
		return &metering.Blocked{Reason: "Add a card"}
	}))
	ctx := t.Context()
	for name, start := range map[string]func() error{
		"infrastructure": func() error { return d.StartInfrastructure(ctx, "org", "id", 0) },
		"cluster":        func() error { return d.StartClusterOperation(ctx, "org", "id", 0) },
		"cloud sync":     func() error { return d.StartCloudSync(ctx, "org", "project", "id", 0) },
		"route":          func() error { return d.StartRoute(ctx, "org", "id", 0) },
		"registration":   func() error { return d.StartRegistration(ctx, "org", "id", 0) },
		"runtime access": func() error { return d.StartRuntimeAccessOperation(ctx, "org", "id", 0) },
	} {
		if err := start(); !errors.Is(err, metering.ErrBlocked) {
			t.Errorf("%s: %v, want ErrBlocked", name, err)
		}
	}
	// Cleanups always run: with no engine they fail on the engine, not the gate.
	if err := d.StartFailedCleanup(ctx, "org", "id", 0); errors.Is(err, metering.ErrBlocked) {
		t.Error("a failed cleanup was gated")
	}
}

type gateFunc func(context.Context, string, metering.Kind, bool) error

func (f gateFunc) CanRun(ctx context.Context, org string, k metering.Kind, owned bool) error {
	return f(ctx, org, k, owned)
}
