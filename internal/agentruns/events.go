package agentruns

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/xraph/cortex/engine"
	"github.com/xraph/cortex/suspension"

	"github.com/xraph/loomplane/internal/security"
)

const (
	// flushEvery and flushBytes coalesce a run's text: what came in the
	// last 250ms, or 4 KiB of it, becomes one event.
	flushEvery = 250 * time.Millisecond
	flushBytes = 4 << 10
	// eventsPerRead bounds one read of a watch.
	eventsPerRead = 200
)

// drive starts a run with start, which closes events when the run ends,
// writes its events until then, and lets this process stop it.
func (s *Service) drive(ctx context.Context, t *Task, start func(ctx context.Context, events chan<- engine.StreamEvent) error) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.trackLocal(t.ID, cancel)
	defer s.untrackLocal(t.ID)
	events := make(chan engine.StreamEvent, 64)
	if err := start(runCtx, events); err != nil {
		return err
	}
	s.pump(ctx, t, events)
	return nil
}

// pump writes a run's events to its log until events closes. Text tokens
// are coalesced; anything else flushes them first, so the log keeps the
// run's order. Writes outlive ctx: the end of a run is what a watcher
// most wants to see.
func (s *Service) pump(ctx context.Context, t *Task, events <-chan engine.StreamEvent) {
	w := &eventWriter{s: s, ctx: context.WithoutCancel(ctx), task: t}
	var timer *time.Timer
	var tick <-chan time.Time
	stop := func() {
		if timer != nil {
			timer.Stop()
			timer, tick = nil, nil
		}
	}
	defer stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				w.flush()
				w.end()
				return
			}
			if ev.Type == engine.EventToken {
				if text, ok := ev.Data["content"].(string); ok {
					w.text.WriteString(text)
				}
				if w.text.Len() >= flushBytes {
					stop()
					w.flush()
				} else if timer == nil {
					timer = time.NewTimer(flushEvery)
					tick = timer.C
				}
				continue
			}
			stop()
			w.add(ev)
		case <-tick:
			timer, tick = nil, nil
			w.flush()
		}
	}
}

// eventWriter is one run's events on their way to the log.
type eventWriter struct {
	s    *Service
	ctx  context.Context
	task *Task
	text strings.Builder
}

func (w *eventWriter) flush() {
	if w.text.Len() == 0 {
		return
	}
	data, err := json.Marshal(map[string]string{"text": w.text.String()})
	w.text.Reset()
	if err != nil {
		w.s.warn("agentruns: couldn't encode a run's text", err)
		return
	}
	w.write(NewEvent{Kind: string(engine.EventToken), Data: data})
}

// end writes the task's final status once the run's last event is in,
// when the run finished while it was being driven.
func (w *eventWriter) end() {
	t, err := w.s.store.Get(w.ctx, w.task.OrgID, w.task.ID)
	if err != nil || !Terminal(t.Status) {
		return
	}
	w.write(*statusEvent(t.Status, t.Error))
}

func (w *eventWriter) add(ev engine.StreamEvent) {
	w.flush()
	if ev.Type == engine.EventRunStarted {
		w.s.move(w.ctx, w.task, []string{StatusQueued, StatusWaiting}, StatusRunning)
	}
	w.write(NewEvent{Kind: string(ev.Type), Data: w.s.eventData(ev)})
}

func (w *eventWriter) write(e NewEvent) {
	if _, err := w.s.store.AppendEvents(w.ctx, w.task.OrgID, w.task.ID, []NewEvent{e}); err != nil {
		w.s.warn("agentruns: couldn't write a run's event", err)
	}
}

// eventFields are the fields of cortex's events the log keeps. Arguments
// never are: a tool's arguments can carry a secret, and the person reads
// what matters in the request it makes.
var eventFields = []string{
	"index", "type", "resumed", "reason", "message", "state", "stop_reason", "output", "tokens_used",
	"publication_status", "verification_status", "duration_ms", "checkpoint_id",
}

// eventData is what the log keeps of a cortex event.
func (s *Service) eventData(ev engine.StreamEvent) json.RawMessage {
	out := map[string]any{}
	switch ev.Type {
	case engine.EventToolCall:
		name := ""
		if n, ok := ev.Data["tool_name"].(string); ok {
			name = n
		}
		out["tool"] = name
		out["call_id"] = ev.Data["tool_id"]
		out["risk"] = s.risk(name)
	case engine.EventSuspended:
		out["reason"] = ev.Data["reason"]
		if calls, ok := ev.Data["pending"].([]suspension.PendingCall); ok {
			pending := make([]map[string]string, 0, len(calls))
			for _, c := range calls {
				pending = append(pending, map[string]string{"tool": c.Name, "call_id": c.ID, "risk": s.risk(c.Name)})
			}
			out["pending"] = pending
		}
	default:
		for _, k := range eventFields {
			if v, ok := ev.Data[k]; ok {
				out[k] = v
			}
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}

// risk is an operation's class as its definition states it.
func (s *Service) risk(tool string) string {
	if s.registry == nil {
		return ""
	}
	if op, ok := s.registry.Lookup(tool); ok {
		return string(op.Def.Risk)
	}
	return ""
}

// statusEvent is the log's record of a status change.
func statusEvent(status, why string) *NewEvent {
	data := map[string]string{"status": status}
	if why != "" {
		data["error"] = why
	}
	raw, err := json.Marshal(data)
	if err != nil {
		raw = json.RawMessage(`{"status":"` + status + `"}`)
	}
	return &NewEvent{Kind: EventStatus, Data: raw}
}

// Watch follows a task p may see: the events after `after`, in order, and
// each new one as it's written. The channel closes once the task has
// finished and its last event is sent, or when stop is called or ctx
// ends. The realtime bus wakes it when the task's row changes, which
// every write to its log does.
func (s *Service) Watch(ctx context.Context, p security.Principal, taskID string, after int64) (events <-chan Event, stop func(), err error) {
	t, gerr := s.Get(ctx, p, taskID)
	if gerr != nil {
		return nil, nil, gerr
	}
	wctx, cancel := context.WithCancel(ctx)
	var wake <-chan struct{}
	unsubscribe := func() {}
	if s.wake != nil {
		wake, unsubscribe = s.wake.Watch(t.OrgID)
	}
	out := make(chan Event)
	go func() {
		defer close(out)
		defer unsubscribe()
		ticker := time.NewTicker(s.poll)
		defer ticker.Stop()
		cursor, last := after, Event{}
		for {
			evs, rerr := s.store.Events(wctx, t.OrgID, t.ID, cursor, eventsPerRead)
			if rerr != nil {
				if wctx.Err() != nil {
					return
				}
				s.warn("agentruns: a watch couldn't read the run's events", rerr)
			}
			for _, e := range evs {
				select {
				case out <- e:
					cursor, last = e.Seq, e
				case <-wctx.Done():
					return
				}
			}
			if len(evs) == eventsPerRead {
				continue
			}
			cur, gerr := s.store.Get(wctx, t.OrgID, t.ID)
			if gerr == nil && finished(cur, cursor, after, last, s.now()) {
				return
			}
			select {
			case <-wctx.Done():
				return
			case <-wake:
			case <-ticker.C:
			}
		}
	}()
	return out, cancel, nil
}

// finished reports whether a watch has everything: the task ended and its
// final status event was sent, or the watcher had it all already, or the
// task ended a while ago and nothing more came (its driver died before
// writing the final status).
func finished(t *Task, cursor, after int64, last Event, now time.Time) bool {
	if !Terminal(t.Status) || t.EventSeq > cursor {
		return false
	}
	if cursor == after || isFinalStatus(last) {
		return true
	}
	return now.Sub(t.UpdatedAt) > finalGrace
}

// finalGrace is how long a watch waits for a finished task's last events.
const finalGrace = 10 * time.Second

func isFinalStatus(e Event) bool {
	if e.Kind != EventStatus {
		return false
	}
	var data struct {
		Status string `json:"status"`
	}
	return json.Unmarshal(e.Data, &data) == nil && Terminal(data.Status)
}
