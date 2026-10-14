package deployments

import (
	"context"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/clouds/buildlogs"
)

type managedOutput struct {
	collector     *Collector
	read          buildlogs.Read
	cursor        string
	gap, disabled bool
	retryAt       time.Time
	cancel        context.CancelFunc
	done          chan struct{}
	once          sync.Once
}

func startManagedOutput(ctx context.Context, job *Job, operation, attempt string, read buildlogs.Read) *managedOutput {
	out := &managedOutput{collector: job.Collector(operation, attempt), read: read, done: make(chan struct{})}
	if job.Logs == nil {
		out.disabled = true
		out.cancel = func() {}
		close(out.done)
		return out
	}
	initial, closeInitial := context.WithTimeout(ctx, 250*time.Millisecond)
	if saved, err := out.collector.Saved(initial); err == nil {
		out.cursor = saved.Cursor
		out.gap = saved.State == "incomplete"
		out.disabled = saved.State == "complete"
	}
	if !out.disabled {
		out.collector.RecordState(initial, out.cursor, "collecting", "Collecting managed build output.")
	}
	closeInitial()
	worker, cancel := context.WithCancel(ctx)
	out.cancel = cancel
	go func() {
		defer close(out.done)
		for {
			out.collect(worker, true)
			select {
			case <-worker.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	return out
}

func (o *managedOutput) collect(ctx context.Context, follow bool) bool {
	if o.disabled {
		return true
	}
	if time.Now().Before(o.retryAt) {
		return false
	}
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cursor, more, err := o.read(readCtx, o.cursor, follow, func(line buildlogs.Line) error {
		return o.collector.Emit(readCtx, buildLine{ID: line.ID, Stream: line.Stream, Message: line.Message, Source: line.Source, Level: line.Level, Time: line.Time})
	})
	if err != nil {
		if readCtx.Err() != nil {
			return false
		}
		state, message := "error", "Managed build output could not be read: "+err.Error()
		switch buildlogs.Status(err) {
		case 401, 403:
			o.disabled = true
			state = "unavailable"
		case 429:
			o.retryAt = time.Now().Add(buildlogs.RetryAfter(err))
			state = "collecting"
			message = "Build output is rate limited. Collection will retry."
		case 410:
			o.gap = true
			o.cursor = ""
			state = "incomplete"
			message = "The provider log cursor expired. Collection restarted with a visible gap."
		}
		o.collector.RecordState(readCtx, o.cursor, state, message)
		return false
	}
	o.cursor = cursor
	state, message := "collecting", "Collecting managed build output."
	if o.gap {
		state, message = "incomplete", "Some build output is unavailable after a provider retention gap."
	}
	o.collector.RecordState(readCtx, o.cursor, state, message)
	return !more
}

// finish owns final catch-up inside the durable action. A fixed grace period
// catches output ingested after the provider reports terminal status.
func (o *managedOutput) finish(ctx context.Context, terminal bool) {
	o.once.Do(func() {
		o.cancel()
		<-o.done
		if o.disabled {
			return
		}
		budget := 8 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			budget = min(budget, max(time.Millisecond, time.Until(deadline)/4))
		}
		final, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
		defer cancel()
		state, message := "error", "Build output collection was interrupted before the build finished."
		if terminal {
			started := time.Now()
			for {
				complete := o.collect(final, false)
				if o.disabled {
					return
				}
				// Historical APIs may repeat the complete range. Stable provider
				// IDs deduplicate it while the grace window catches late entries.
				if complete && time.Since(started) >= 2*time.Second {
					state, message = "complete", "Managed build output retained."
					if o.gap {
						state, message = "incomplete", "Some build output is unavailable after a provider retention gap."
					}
					break
				}
				select {
				case <-final.Done():
					state, message = "incomplete", "Build output did not finish before the bounded final catch-up deadline."
				case <-time.After(250 * time.Millisecond):
					continue
				}
				break
			}
		}
		record, closeRecord := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer closeRecord()
		o.collector.clearCancelledError()
		o.collector.RecordState(record, o.cursor, state, message)
	})
}
