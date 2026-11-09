package checkpoints

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/chronicle/checkpoint"
	"github.com/xraph/chronicle/id"
	"github.com/xraph/chronicle/stream"
	"github.com/xraph/forge"
)

// Streams is what Scheduler reads from Chronicle's store.
type Streams interface {
	ListStreams(ctx context.Context, opts stream.ListOpts) ([]*stream.Stream, error)
	LatestCheckpoint(ctx context.Context, streamID id.ID) (*checkpoint.Checkpoint, error)
}

// Taker takes one checkpoint. *checkpoint.Checkpointer is one.
type Taker interface {
	CheckpointStream(ctx context.Context, st checkpoint.StreamHead) (*checkpoint.Checkpoint, error)
}

// Policy says when a stream is due a checkpoint: once EveryInterval has
// passed since its last one, or once it has gained EveryEvents events,
// whichever comes first. EveryEvents 0 leaves only the interval.
type Policy struct {
	EveryEvents   int
	EveryInterval time.Duration
}

// The scheduler looks at every stream twelve times per EveryInterval, and
// no more than once a second. That keeps the EveryEvents trigger close to
// the count it names without a busy stream waiting out the interval.
const (
	pollDivisor = 12
	pollFloor   = time.Second
)

// Scheduler takes due checkpoints on a ticker. It is Chronicle v1.11.0's
// own scheduler, which is private to its Forge extension, copied for an
// app that builds Chronicle itself.
type Scheduler struct {
	streams Streams
	taker   Taker
	policy  Policy
	log     forge.Logger
	now     func() time.Time
}

// NewScheduler returns a Scheduler. A nil log logs nothing.
func NewScheduler(streams Streams, taker Taker, policy Policy, log forge.Logger) *Scheduler {
	if log == nil {
		log = forge.NewNoopLogger()
	}
	return &Scheduler{streams: streams, taker: taker, policy: policy, log: log, now: time.Now}
}

// PollInterval is how often Run looks for due streams.
func (s *Scheduler) PollInterval() time.Duration {
	return max(s.policy.EveryInterval/pollDivisor, pollFloor)
}

// Run ticks until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.PollInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Tick logs each failure itself.
			if taken, err := s.Tick(ctx); err == nil && taken > 0 {
				s.log.Debug("checkpoints: signed streams", forge.F("count", taken))
			}
		}
	}
}

// Tick checkpoints every due stream once and says how many it took. A
// quiet stream, or a checkpoint another replica took first, isn't an
// error. A stream that fails is logged and the rest still run; the first
// failure is returned.
func (s *Scheduler) Tick(ctx context.Context) (taken int, err error) {
	streams, err := s.streams.ListStreams(ctx, stream.ListOpts{})
	if err != nil {
		s.log.Error("checkpoints: list streams", forge.F("error", err.Error()))
		return 0, err
	}
	var firstErr error
	fail := func(msg string, st *stream.Stream, err error) {
		s.log.Error(msg, forge.F("stream_id", st.ID.String()), forge.F("error", err.Error()))
		if firstErr == nil {
			firstErr = err
		}
	}
	for _, st := range streams {
		due, dueErr := s.due(ctx, st)
		if dueErr != nil {
			fail("checkpoints: is the stream due", st, dueErr)
			continue
		}
		if !due {
			continue
		}
		_, cpErr := s.taker.CheckpointStream(ctx, checkpoint.StreamHead{
			ID: st.ID, AppID: st.AppID, TenantID: st.TenantID, HeadSeq: st.HeadSeq, HeadHash: st.HeadHash,
		})
		switch {
		case cpErr == nil:
			taken++
		case errors.Is(cpErr, checkpoint.ErrNothingToCheckpoint), errors.Is(cpErr, checkpoint.ErrExists):
		default:
			fail("checkpoints: take a checkpoint", st, cpErr)
		}
	}
	return taken, firstErr
}

// due reports whether st has gained an event since its last checkpoint and
// crossed one of the policy's triggers. A stream never checkpointed is due
// as soon as it has an event.
func (s *Scheduler) due(ctx context.Context, st *stream.Stream) (bool, error) {
	latest, err := s.streams.LatestCheckpoint(ctx, st.ID)
	switch {
	case errors.Is(err, checkpoint.ErrNotFound):
		return st.HeadSeq > 0, nil
	case err != nil:
		return false, err
	}
	if st.HeadSeq <= latest.ToSeq {
		return false, nil
	}
	if s.now().Sub(latest.CreatedAt) >= s.policy.EveryInterval {
		return true, nil
	}
	return s.policy.EveryEvents > 0 && st.HeadSeq-latest.ToSeq >= uint64(s.policy.EveryEvents), nil
}
