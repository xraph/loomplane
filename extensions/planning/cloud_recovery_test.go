package planning

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/orchestration"
)

type recoveryProbe struct{ run func(context.Context) error }

func (p recoveryProbe) RecoverCloudSyncs(ctx context.Context) error { return p.run(ctx) }
func (recoveryProbe) ReconcileCloudSync(context.Context, string, string, string, func([]byte) error) error {
	return nil
}

type recoveryWarning struct {
	message string
	fields  []forge.Field
}
type recoveryLog struct {
	forge.Logger
	warnings chan recoveryWarning
}

func (l recoveryLog) Warn(message string, fields ...forge.Field) {
	l.warnings <- recoveryWarning{message, fields}
}

func recoveryExtension(run func(context.Context) error) (*Extension, chan recoveryWarning) {
	warnings := make(chan recoveryWarning, 4)
	d := orchestration.NewDeployments()
	d.BindCloudSync(recoveryProbe{run: run})
	e := &Extension{BaseExtension: forge.NewBaseExtension(Name, "test", "test"), deployments: d}
	e.SetLogger(recoveryLog{Logger: forge.NewNoopLogger(), warnings: warnings})
	return e, warnings
}

func TestCloudRecoveryStartupFailureRetriesAndStops(t *testing.T) {
	startup := errors.New("durable invalid request: execution does not match worker routing")
	periodic := errors.New("temporary recovery store failure")
	var calls atomic.Int32
	recovered, inFlight := make(chan struct{}), make(chan struct{})
	e, warnings := recoveryExtension(func(ctx context.Context) error {
		switch calls.Add(1) {
		case 1:
			return startup
		case 2:
			return periodic
		case 3:
			close(recovered)
			return nil
		case 4:
			close(inFlight)
			<-ctx.Done()
			return ctx.Err()
		default:
			t.Error("recovery ran after stop")
			return nil
		}
	})
	if err := e.startCloudRecovery(t.Context(), time.Millisecond); err != nil {
		t.Fatalf("recovery failure prevented startup: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelCleanup()
		if err := e.Stop(cleanupCtx); err != nil {
			t.Error(err)
		}
	})
	for _, signal := range []chan struct{}{recovered, inFlight} {
		select {
		case <-signal:
		case <-time.After(3 * time.Second):
			t.Fatal("periodic recovery did not resume")
		}
	}
	stop, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := e.Stop(stop); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.cloudRecoveryDone:
	default:
		t.Fatal("Stop returned before recovery exited")
	}
	if calls.Load() != 4 {
		t.Fatalf("recovery calls=%d", calls.Load())
	}
	for _, expected := range []error{startup, periodic} {
		select {
		case warning := <-warnings:
			if warning.message != "Cloud recovery will retry" || len(warning.fields) != 1 || warning.fields[0].Key() != "error" || warning.fields[0].Value() != expected.Error() {
				t.Fatalf("recovery failure was not visible: %+v", warning)
			}
		default:
			t.Fatal("recovery failure was not logged")
		}
	}
	if len(warnings) != 0 {
		t.Fatal("shutdown cancellation logged as recovery failure")
	}
}

func TestCloudRecoveryCanceledStartupDoesNotLaunch(t *testing.T) {
	for _, when := range []string{"before", "during", "during successful pass"} {
		t.Run(when, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls int
			e, warnings := recoveryExtension(func(context.Context) error {
				calls++
				cancel()
				if when == "during successful pass" {
					return nil
				}
				return context.Canceled
			})
			if when == "before" {
				cancel()
			}
			err := e.startCloudRecovery(ctx, time.Millisecond)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("startup cancellation ignored: %v", err)
			}
			if when == "before" && calls != 0 {
				t.Fatal("recovery ran with canceled startup context")
			}
			if e.cloudRecoveryCancel != nil || e.cloudRecoveryDone != nil {
				t.Fatal("canceled startup launched background recovery")
			}
			if len(warnings) != 0 {
				t.Fatal("startup cancellation logged as retryable failure")
			}
		})
	}
}
