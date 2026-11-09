package identity

import (
	"context"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/chronicle/checkpoint"
	"github.com/xraph/chronicle/id"
	"github.com/xraph/chronicle/stream"
	"github.com/xraph/forge"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultmem "github.com/xraph/vault/store/memory"

	"github.com/xraph/loomplane/internal/checkpoints"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/security"
)

// passInFlight is a Chronicle store whose stream listing is a slow pass:
// it says it has started, holds until its context ends, then takes a
// moment more to finish.
type passInFlight struct {
	started  chan struct{}
	once     atomic.Bool
	finished atomic.Bool
}

func (p *passInFlight) ListStreams(ctx context.Context, _ stream.ListOpts) ([]*stream.Stream, error) {
	if p.once.CompareAndSwap(false, true) {
		close(p.started)
	}
	<-ctx.Done()
	time.Sleep(50 * time.Millisecond)
	p.finished.Store(true)
	return nil, ctx.Err()
}

func (*passInFlight) LatestCheckpoint(context.Context, id.ID) (*checkpoint.Checkpoint, error) {
	return nil, checkpoint.ErrNotFound
}

// schedulerRunning reports whether any goroutine is still in the
// checkpoint scheduler's loop.
func schedulerRunning() bool {
	buf := make([]byte, 1<<20)
	return strings.Contains(string(buf[:runtime.Stack(buf, true)]), "checkpoints.(*Scheduler).Run")
}

// The ticker outlives the context Start ran with, and Stop cancels it and
// waits for a pass in flight, so no scheduler goroutine is left behind.
func TestStartAndStopCheckpoints(t *testing.T) {
	cfg := config.Defaults()
	cfg.Chronicle.Checkpoints.EveryInterval = time.Minute
	e := New(cfg)
	e.SetLogger(forge.NewNoopLogger())

	key, err := security.DeriveKey(cfg.SecretKey, checkpoints.KeyLabel)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	keys := checkpoints.NewKeys(secret.NewService(vaultmem.New(), enc, secret.WithAppID(checkpoints.VaultAppID)))
	if _, err = keys.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	pass := &passInFlight{started: make(chan struct{})}
	e.checkpoints = &auditCheckpoints{keys: keys, sched: checkpoints.NewScheduler(pass, nil,
		checkpoints.Policy{EveryInterval: time.Second}, nil)}

	startCtx, endStart := context.WithCancel(t.Context())
	if err = e.startCheckpoints(startCtx); err != nil {
		t.Fatal(err)
	}
	// Start's context ends once the app has started; the ticker keeps
	// going.
	endStart()
	select {
	case <-pass.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the scheduler never ran a pass")
	}

	e.stopCheckpoints()
	if !pass.finished.Load() {
		t.Fatal("stopCheckpoints returned before the pass in flight finished")
	}
	if schedulerRunning() {
		t.Fatal("a scheduler goroutine is still running after stopCheckpoints")
	}
	// A second Stop, as a failed start can call, is harmless.
	e.stopCheckpoints()
}
