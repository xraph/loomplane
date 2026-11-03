package agentruns

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/xraph/cortex"
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

// errTaskCancelled is why a run this process drives was stopped for a
// cancel: a plain cause, which cortex ends the run on.
var errTaskCancelled = errors.New("agentruns: the task was cancelled")

// begin is a job's hold on the task's run in this process. The run's
// context keeps bound's values but not its end: when the Dispatch attempt
// ends (its lease lost, its timeout, a shutdown) the run is cancelled with
// cortex.ErrRunInterrupted, which cortex hands off for another worker to
// continue, and a cancel of the task stops it with a plain cause, which
// cortex ends it on. It's registered before the job claims the task, so a
// cancel never misses it. release lets it go.
func (s *Service) begin(bound context.Context, t *Task) (runCtx context.Context, release func()) {
	if s.beforeClaim != nil {
		s.beforeClaim()
	}
	ctx, cancel := context.WithCancelCause(context.WithoutCancel(bound))
	s.trackLocal(t.ID, func() { cancel(errTaskCancelled) })
	stop := context.AfterFunc(bound, func() { cancel(cortex.ErrRunInterrupted) })
	return ctx, func() {
		stop()
		s.untrackLocal(t.ID)
		cancel(nil)
	}
}

// claim reports whether the job may drive the task's run. With from set
// it first moves the task from one of from to to. Either way a task that
// has finished, a cancel among them, is never driven.
func (s *Service) claim(ctx context.Context, t *Task, from []string, to string) bool {
	if len(from) > 0 {
		moved, err := s.store.Transition(ctx, t.OrgID, t.ID, Transition{From: from, Status: to, Event: statusEvent(to, "")})
		if err != nil {
			s.warn("agentruns: couldn't record a task's status", err)
		}
		if moved {
			return true
		}
	}
	cur, err := s.store.Get(ctx, t.OrgID, t.ID)
	if err != nil {
		s.warn("agentruns: couldn't read a task before driving it", err)
		return false
	}
	return !Terminal(cur.Status)
}

// drive starts a run with start, which closes events when the run ends,
// and writes its events until then. A run whose context already ended (a
// cancel, or the attempt over) isn't started.
func (s *Service) drive(runCtx context.Context, t *Task, start func(ctx context.Context, events chan<- engine.StreamEvent) error) error {
	if s.beforeDrive != nil {
		s.beforeDrive()
	}
	if runCtx.Err() != nil {
		if cause := context.Cause(runCtx); !errors.Is(cause, errTaskCancelled) {
			return cause
		}
		return nil
	}
	events := make(chan engine.StreamEvent, 64)
	if err := start(runCtx, events); err != nil {
		return err
	}
	s.pump(runCtx, t, events)
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
//
// Who may see it is checked again before each read, so a watcher who
// loses access stops getting events.
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
			if ok, verr := s.visible(wctx, p, t); verr != nil || !ok {
				return
			}
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

// RecordRequest writes a "request" event to the task's log: the request a
// person was asked, by ID and kind only, so whoever follows the run sees it
// and reads the rest through the requests API. agentrequests calls it once
// per new request.
func (s *Service) RecordRequest(ctx context.Context, t *Task, requestID, kind string) error {
	raw, err := json.Marshal(map[string]string{"request_id": requestID, "kind": kind})
	if err != nil {
		return err
	}
	_, err = s.store.AppendEvents(ctx, t.OrgID, t.ID, []NewEvent{{Kind: EventRequest, Data: raw}})
	return err
}
