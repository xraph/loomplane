package deployments

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/plugins"
	"github.com/xraph/loomplane/pkg/provider/sdk"
)

type pluginOutput struct {
	client            *plugins.Client
	collector         *Collector
	operation, cursor string
	gap, disabled     bool
	retryAt           time.Time
	cancel            context.CancelFunc
	done              chan struct{}
	finishOnce        sync.Once
	stop              chan struct{}
	stopWatch         func() bool
}

func (p *Plugin) output(ctx context.Context, job *Job, operation, attempt, execution string, gap bool) *pluginOutput {
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	out := &pluginOutput{client: p.Client, collector: job.Collector(operation, attempt), operation: operation, gap: gap}
	for _, value := range p.Client.RedactionValues() {
		out.collector.addCredential([]byte(value))
	}
	if !p.OperationLogs || execution == "" {
		out.disabled = true
		out.collector.RecordState(ctx, "", "unavailable", "This provider reports progress without operation build output.")
		return out
	}
	if saved, err := out.collector.Saved(ctx); err == nil {
		out.cursor = saved.Cursor
		out.gap = gap || saved.State == "incomplete"
	}
	out.collector.RecordState(ctx, out.cursor, "collecting", "Collecting provider operation output.")
	return out
}

func (o *pluginOutput) collect(ctx context.Context, terminal bool) bool {
	if o.disabled {
		return true
	}
	if time.Now().Before(o.retryAt) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	maxPages := 4
	if terminal {
		maxPages = 20
	}
	for range maxPages {
		page, err := o.client.OperationLogs(ctx, o.operation, o.cursor, sdk.MaxLogPage)
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			state := "error"
			var response *plugins.Error
			if errors.As(err, &response) {
				switch response.Status {
				case 401, 403:
					o.disabled = true
					state = "unavailable"
				case 429:
					o.retryAt = time.Now().Add(5 * time.Second)
				}
			}
			message, _ := o.collector.message("Provider build output could not be read: " + err.Error())
			o.collector.RecordState(ctx, o.cursor, state, message)
			return true
		}
		if page.Gap {
			o.gap = true
		}
		for _, line := range page.Lines {
			if err := o.collector.Emit(ctx, buildLine{ID: line.ID, Stream: line.Stream, Message: line.Message, Time: line.Time, Source: "provider", Level: "info", Truncated: line.Truncated}); err != nil {
				o.collector.RecordState(ctx, o.cursor, "error", "Provider output could not be retained. Deployment status is independent of log collection.")
				return true
			}
		}
		if page.Cursor != "" {
			o.cursor = page.Cursor
		}
		state, message := "collecting", "Collecting provider operation output."
		if o.gap {
			state, message = "incomplete", "Some provider output is unavailable after a restart or retention gap."
		}
		if terminal && !page.More && !o.gap {
			o.collector.clearCancelledError()
			state, message = "complete", "Provider operation output retained."
		}
		o.collector.RecordState(ctx, o.cursor, state, message)
		if !page.More {
			return true
		}
	}
	if terminal {
		o.collector.RecordState(ctx, o.cursor, "incomplete", "Provider output exceeded the bounded terminal catch-up. Some output remains unavailable.")
	}
	return !terminal
}

func (o *pluginOutput) start(ctx context.Context, every time.Duration) {
	worker, cancel := context.WithCancel(context.WithoutCancel(ctx))
	o.cancel = cancel
	o.done = make(chan struct{})
	o.stop = make(chan struct{})
	o.stopWatch = context.AfterFunc(ctx, cancel)
	go func() {
		defer close(o.done)
		for {
			o.collect(worker, false)
			select {
			case <-worker.Done():
				return
			case <-o.stop:
				return
			case <-time.After(every):
			}
		}
	}()
}

// finish stays inside the durable activity. It joins the active writer before
// final catch-up, so a retry cannot race an older collection finalizer.
func (o *pluginOutput) finish(ctx context.Context, terminal bool) {
	o.finishOnce.Do(func() {
		if o.stopWatch != nil {
			o.stopWatch()
		}
		if o.stop != nil {
			close(o.stop)
		}
		budget := 3 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline) / 4
			if remaining < budget {
				budget = remaining
			}
		}
		if budget < time.Millisecond {
			budget = time.Millisecond
		}
		final, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
		defer cancel()
		if o.done != nil {
			select {
			case <-o.done:
			case <-final.Done():
				if o.cancel != nil {
					o.cancel()
				}
				<-o.done
			}
		}
		if o.cancel != nil {
			defer o.cancel()
		}
		incomplete := false
		if terminal {
			incomplete = !o.collect(final, true)
		} else if !o.disabled {
			record, closeRecord := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			o.collector.RecordState(record, o.cursor, "error", "Provider output collection was interrupted before the operation finished.")
			closeRecord()
		}
		if incomplete || final.Err() != nil {
			record, closeRecord := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			o.collector.RecordState(record, o.cursor, "incomplete", "Provider output did not finish before the final catch-up deadline.")
			closeRecord()
		}
	})
}
