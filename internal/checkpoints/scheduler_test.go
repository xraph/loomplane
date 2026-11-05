package checkpoints

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/chronicle"
	"github.com/xraph/chronicle/checkpoint"
	chronstore "github.com/xraph/chronicle/store"
	chronmem "github.com/xraph/chronicle/store/memory"
	vaultmem "github.com/xraph/vault/store/memory"
)

type fixture struct {
	store  *chronmem.Store
	audit  *chronicle.Chronicle
	keys   *Keys
	signer *checkpoint.Ed25519Signer
	sched  *Scheduler
}

func newFixture(t *testing.T, policy Policy) *fixture {
	t.Helper()
	f := &fixture{store: chronmem.New(), keys: NewKeys(vaultService(t, vaultmem.New()))}
	if _, err := f.keys.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.signer = checkpoint.NewEd25519Signer(f.keys)
	var err error
	f.audit, err = chronicle.New(chronicle.WithStore(chronstore.NewAdapter(f.store)), chronicle.WithCheckpointSigner(f.signer))
	if err != nil {
		t.Fatal(err)
	}
	f.sched = NewScheduler(f.store, checkpoint.NewCheckpointer(f.store, f.signer, nil), policy, nil)
	return f
}

func (f *fixture) record(t *testing.T, tenant string, n int) {
	t.Helper()
	for range n {
		if err := f.audit.Info(context.Background(), "project.create", "project", "p1").
			Category("projects").AppID("aapp_test").TenantID(tenant).UserID("u1").Record(); err != nil {
			t.Fatal(err)
		}
	}
}

// One pass signs every stream with events, and the signature verifies under
// the key in Vault.
func TestTickSignsACheckpointThatVerifies(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Policy{EveryEvents: 1000, EveryInterval: 15 * time.Minute})
	f.record(t, "platform", 3)

	taken, err := f.sched.Tick(ctx)
	if err != nil || taken != 1 {
		t.Fatalf("Tick = %d, %v; want one checkpoint", taken, err)
	}
	info, err := f.store.GetStreamByScope(ctx, "aapp_test", "platform")
	if err != nil {
		t.Fatal(err)
	}
	cp, err := f.store.LatestCheckpoint(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cp.FromSeq != 1 || cp.ToSeq != 3 || cp.Algorithm != checkpoint.AlgorithmEd25519 {
		t.Fatalf("checkpoint %d..%d %s", cp.FromSeq, cp.ToSeq, cp.Algorithm)
	}
	_, keyID, err := f.keys.PublicKey(ctx)
	if err != nil || cp.SignKeyID != keyID {
		t.Fatalf("signed with %q, the key is %q (%v)", cp.SignKeyID, keyID, err)
	}
	if err = f.signer.Verify(ctx, []byte(cp.SignedPayload), cp.Signature, cp.SignKeyID); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if cp.SignedPayload != checkpoint.CanonicalPayload(cp) {
		t.Fatal("the signed payload isn't the checkpoint's canonical payload")
	}
}

// A stream with nothing new since its checkpoint isn't due, and isn't an
// error.
func TestTickLeavesAQuietStreamAlone(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Policy{EveryEvents: 1000, EveryInterval: time.Second})
	f.record(t, "org_1", 2)
	if taken, err := f.sched.Tick(ctx); err != nil || taken != 1 {
		t.Fatalf("first Tick = %d, %v", taken, err)
	}
	f.sched.now = func() time.Time { return time.Now().Add(time.Hour) }
	if taken, err := f.sched.Tick(ctx); err != nil || taken != 0 {
		t.Fatalf("quiet Tick = %d, %v; want nothing and no error", taken, err)
	}
}

// Within the interval, a stream is signed again only once it gains
// EveryEvents events; past it, any new event is enough.
func TestTickTriggers(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Policy{EveryEvents: 5, EveryInterval: time.Hour})
	f.record(t, "org_1", 1)
	if taken, err := f.sched.Tick(ctx); err != nil || taken != 1 {
		t.Fatalf("first Tick = %d, %v", taken, err)
	}
	f.record(t, "org_1", 4)
	if taken, err := f.sched.Tick(ctx); err != nil || taken != 0 {
		t.Fatalf("Tick at 4 new events = %d, %v; want none", taken, err)
	}
	f.record(t, "org_1", 1)
	if taken, err := f.sched.Tick(ctx); err != nil || taken != 1 {
		t.Fatalf("Tick at 5 new events = %d, %v; want one", taken, err)
	}
	f.record(t, "org_1", 1)
	f.sched.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if taken, err := f.sched.Tick(ctx); err != nil || taken != 1 {
		t.Fatalf("Tick past the interval = %d, %v; want one", taken, err)
	}
}

type takerFunc func(context.Context, checkpoint.StreamHead) (*checkpoint.Checkpoint, error)

func (f takerFunc) CheckpointStream(ctx context.Context, st checkpoint.StreamHead) (*checkpoint.Checkpoint, error) {
	return f(ctx, st)
}

// A lost race and a stream with nothing new are normal; anything else is
// reported, and the other streams still run.
func TestTickErrors(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Policy{EveryEvents: 1, EveryInterval: time.Minute})
	f.record(t, "org_1", 1)
	f.record(t, "org_2", 1)
	for _, tt := range []struct {
		name    string
		err     error
		wantErr bool
	}{
		{"lost race", checkpoint.ErrExists, false},
		{"nothing new", checkpoint.ErrNothingToCheckpoint, false},
		{"failure", errors.New("disk full"), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			s := NewScheduler(f.store, takerFunc(func(context.Context, checkpoint.StreamHead) (*checkpoint.Checkpoint, error) {
				calls++
				return nil, tt.err
			}), f.sched.policy, nil)
			taken, err := s.Tick(ctx)
			if taken != 0 || (err != nil) != tt.wantErr || calls != 2 {
				t.Fatalf("Tick = %d, %v after %d calls", taken, err, calls)
			}
		})
	}
}

func TestPollInterval(t *testing.T) {
	for _, tt := range []struct {
		every, want time.Duration
	}{
		{15 * time.Minute, 75 * time.Second},
		{5 * time.Second, time.Second},
	} {
		if got := NewScheduler(nil, nil, Policy{EveryInterval: tt.every}, nil).PollInterval(); got != tt.want {
			t.Errorf("PollInterval for %s = %s, want %s", tt.every, got, tt.want)
		}
	}
}
