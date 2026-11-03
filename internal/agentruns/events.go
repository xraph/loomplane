package agentruns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/engine"
	"github.com/xraph/cortex/suspension"

	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

const (
	// flushEvery and flushBytes coalesce a run's text: what came in the
	// last 250ms, or 4 KiB of it, becomes one event.
	flushEvery = 250 * time.Millisecond
	flushBytes = 4 << 10
	// eventsPerRead bounds one read of a watch.
	eventsPerRead = 200
	// Hidden is what SafeArguments shows in place of a secret-looking
	// value.
	Hidden = "[hidden]"
	// MaxArgumentBytes bounds the arguments a tool_call event keeps.
	MaxArgumentBytes = 4 << 10
	// maxOutcomeMessage bounds the message of a tool_result event.
	maxOutcomeMessage = 300
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
		// A run whose hook came but whose pump never ended (its stream
		// never started) finishes here.
		s.settleOutcome(context.WithoutCancel(bound), t, s.untrackLocal(t.ID))
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
// cancel, or the attempt over) isn't started. The run's writer is
// registered before cortex is called, so a tool hook that fires before
// the pump reads anything still finds it.
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
	w := s.newWriter(runCtx, t)
	defer s.dropWriter(w)
	if err := start(runCtx, events); err != nil {
		return err
	}
	s.pumpWith(w, events)
	return nil
}

// pump writes a run's events to its log until events closes, with a
// writer of its own.
func (s *Service) pump(ctx context.Context, t *Task, events <-chan engine.StreamEvent) {
	w := s.newWriter(ctx, t)
	defer s.dropWriter(w)
	s.pumpWith(w, events)
}

// pumpWith writes a run's events to its log until events closes. Text
// tokens are coalesced; anything else flushes them first, so the log
// keeps the run's order. Writes outlive ctx: the end of a run is what a
// watcher most wants to see.
func (s *Service) pumpWith(w *eventWriter, events <-chan engine.StreamEvent) {
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
				// Cortex fires every tool hook before it closes the
				// stream: what's still waiting has no call to follow.
				s.dropWriter(w)
				w.dropWaiting()
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

// eventWriter is one run's events on their way to the log. The pump
// writes through it, and so do the run's tool hooks, which cortex fires
// on the run's own goroutine; mu keeps their writes, and the matching of
// an outcome to its call, in one order.
type eventWriter struct {
	s    *Service
	ctx  context.Context
	task *Task
	// text is the pump's alone.
	text strings.Builder

	mu sync.Mutex
	// open is the calls written with no outcome yet, by tool name,
	// oldest first; waiting is the outcomes that came before their call.
	open    map[string][]string
	waiting map[string][]toolOutcome
}

// toolOutcome is how one tool call ended, as its hook tells it.
type toolOutcome struct {
	tool    string
	outcome string
	message string
}

// newWriter makes the task's run's writer and registers it under the
// run's ID. A run that resumes from a pause gets the outcomes of the
// calls it paused on first, so those calls start out open.
func (s *Service) newWriter(ctx context.Context, t *Task) *eventWriter {
	w := &eventWriter{
		s: s, ctx: context.WithoutCancel(ctx), task: t,
		open: map[string][]string{}, waiting: map[string][]toolOutcome{},
	}
	w.openPaused()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writers == nil {
		s.writers = map[string]*eventWriter{}
	}
	s.writers[t.RunID] = w
	return w
}

// dropWriter lets the run's writer go, if it's still w.
func (s *Service) dropWriter(w *eventWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writers[w.task.RunID] == w {
		delete(s.writers, w.task.RunID)
	}
}

// writer is the writer of the run this process is pumping, if any.
func (s *Service) writer(runID string) *eventWriter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.writers[runID]
}

// toolOutcome hands a tool hook's outcome to the run's writer. A run
// this process isn't pumping (its hook fired on another replica, or
// after its stream ended) has nowhere to write it, and it's dropped.
func (s *Service) toolOutcome(runID string, o toolOutcome) {
	w := s.writer(runID)
	if w == nil {
		s.debug("agentruns: a tool outcome came for a run this process isn't writing", "run_id", runID, "tool", o.tool)
		return
	}
	w.outcome(o)
}

// openPaused opens the calls of the run's last pause that have no
// outcome in the log yet.
func (w *eventWriter) openPaused() {
	var paused []map[string]string
	done := map[string]bool{}
	var after int64
	for {
		evs, err := w.s.store.Events(w.ctx, w.task.OrgID, w.task.ID, after, 1000)
		if err != nil {
			w.s.warn("agentruns: couldn't read a run's log for its paused calls", err)
			return
		}
		for _, e := range evs {
			after = e.Seq
			switch e.Kind {
			case string(engine.EventSuspended):
				var data struct {
					Pending []map[string]string `json:"pending"`
				}
				if json.Unmarshal(e.Data, &data) == nil {
					paused = data.Pending
				}
			case EventToolResult:
				var data struct {
					CallID string `json:"call_id"`
				}
				if json.Unmarshal(e.Data, &data) == nil {
					done[data.CallID] = true
				}
			}
		}
		if len(evs) < 1000 {
			break
		}
	}
	for _, c := range paused {
		if c["tool"] != "" && !done[c["call_id"]] {
			w.open[c["tool"]] = append(w.open[c["tool"]], c["call_id"])
		}
	}
}

func (w *eventWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked()
}

func (w *eventWriter) flushLocked() {
	if w.text.Len() == 0 {
		return
	}
	data, err := json.Marshal(map[string]string{"text": w.text.String()})
	w.text.Reset()
	if err != nil {
		w.s.warn("agentruns: couldn't encode a run's text", err)
		return
	}
	w.writeLocked(NewEvent{Kind: string(engine.EventToken), Data: data})
}

// end writes the task's final status once the run's last event is in,
// when the run finished while it was being driven. An outcome the run's
// hook left moves the task and writes its status in one step; a task
// that finished another way (a cancel) gets its status written here.
func (w *eventWriter) end() {
	if w.s.settleOutcome(w.ctx, w.task, w.s.endPump(w.task.ID)) {
		return
	}
	t, err := w.s.store.Get(w.ctx, w.task.OrgID, w.task.ID)
	if err != nil || !Terminal(t.Status) {
		return
	}
	w.write(*statusEvent(t.Status, t.Error))
}

func (w *eventWriter) add(ev engine.StreamEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked()
	switch ev.Type {
	case engine.EventRunStarted:
		w.s.move(w.ctx, w.task, []string{StatusQueued, StatusWaiting}, StatusRunning)
	case engine.EventSafetyBlock:
		// The step's text is what the safety checks stopped: withhold
		// it in the same write that records the stop.
		e := NewEvent{Kind: string(ev.Type), Data: w.s.eventData(ev)}
		if _, err := w.s.store.WithholdStep(w.ctx, w.task.OrgID, w.task.ID, e); err != nil {
			w.s.warn("agentruns: couldn't withhold a stopped run's text", err)
		}
		return
	}
	w.writeLocked(NewEvent{Kind: string(ev.Type), Data: w.s.eventData(ev)})
	if ev.Type == engine.EventToolCall {
		w.called(stringField(ev.Data, "tool_name"), stringField(ev.Data, "tool_id"))
	}
}

// called opens a call just written, or writes the outcome that came
// before it.
func (w *eventWriter) called(tool, callID string) {
	if q := w.waiting[tool]; len(q) > 0 {
		w.waiting[tool] = q[1:]
		w.writeOutcome(callID, q[0])
		return
	}
	w.open[tool] = append(w.open[tool], callID)
}

// outcome writes o after the oldest open call of its tool, or keeps it
// until that call is written.
func (w *eventWriter) outcome(o toolOutcome) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if q := w.open[o.tool]; len(q) > 0 {
		w.open[o.tool] = q[1:]
		w.writeOutcome(q[0], o)
		return
	}
	w.waiting[o.tool] = append(w.waiting[o.tool], o)
}

// dropWaiting lets go of the outcomes no call came for.
func (w *eventWriter) dropWaiting() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for tool, q := range w.waiting {
		if len(q) > 0 {
			w.s.debug("agentruns: a tool outcome came with no call to follow", "run_id", w.task.RunID, "tool", tool)
		}
	}
	clear(w.waiting)
}

func (w *eventWriter) writeOutcome(callID string, o toolOutcome) {
	data, err := json.Marshal(map[string]string{"tool": o.tool, "call_id": callID, "outcome": o.outcome, "message": o.message})
	if err != nil {
		w.s.warn("agentruns: couldn't encode a tool's outcome", err)
		return
	}
	w.writeLocked(NewEvent{Kind: EventToolResult, Data: data})
}

func (w *eventWriter) write(e NewEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeLocked(e)
}

func (w *eventWriter) writeLocked(e NewEvent) {
	if _, err := w.s.store.AppendEvents(w.ctx, w.task.OrgID, w.task.ID, []NewEvent{e}); err != nil {
		w.s.warn("agentruns: couldn't write a run's event", err)
	}
}

// eventFields are the fields of cortex's events the log keeps. A tool
// call's arguments are kept apart, through SafeArguments.
var eventFields = []string{
	"index", "type", "resumed", "reason", "message", "state", "stop_reason", "output", "tokens_used",
	"publication_status", "verification_status", "duration_ms", "checkpoint_id",
}

// eventData is what the log keeps of a cortex event.
func (s *Service) eventData(ev engine.StreamEvent) json.RawMessage {
	out := map[string]any{}
	switch ev.Type {
	case engine.EventToolCall:
		name := stringField(ev.Data, "tool_name")
		out["tool"] = name
		out["call_id"] = ev.Data["tool_id"]
		out["risk"] = s.risk(name)
		if args := rawArguments(ev.Data["arguments"]); s.keepsArguments(name, args) {
			out["arguments"] = SafeArguments(args)
		}
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

// stringField is data[key] when it's a string, else "".
func stringField(data map[string]any, key string) string {
	if v, ok := data[key].(string); ok {
		return v
	}
	return ""
}

// rawArguments is a tool call's arguments as cortex sends them: a JSON
// string.
func rawArguments(v any) json.RawMessage {
	switch a := v.(type) {
	case nil:
		return nil
	case string:
		return json.RawMessage(a)
	case []byte:
		return json.RawMessage(a)
	case json.RawMessage:
		return a
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}

// keepsArguments reports whether a call's arguments may go in the log.
// A secret_or_external operation's never do: a request to a person or a
// secret's value, whatever SafeArguments would hide. Neither do the
// arguments of a call that flags a secret (a secret set_variable, which
// is secret_or_external by its input), nor those of a tool that isn't an
// operation.
func (s *Service) keepsArguments(tool string, args json.RawMessage) bool {
	if s.registry == nil {
		return false
	}
	op, ok := s.registry.Lookup(tool)
	if !ok || op.External() || op.Def.Risk == defs.RiskSecretOrExternal {
		return false
	}
	v, ok := decodeJSON(args)
	return !ok || !flagsSecret(v)
}

// secretKeys are the keys whose values SafeArguments always hides.
var secretKeys = []string{"password", "token", "api_key", "secret_value", "private_key"}

// SafeArguments is a tool call's arguments as Loomplane shows them: at
// any depth, the values of password, token, api_key, secret_value and
// private_key (in any case), and value beside "secret": true, become
// Hidden. Arguments longer than MaxArgumentBytes once hidden become
// {"truncated":true}, and ones that aren't JSON become {}.
func SafeArguments(raw json.RawMessage) json.RawMessage {
	v, ok := decodeJSON(raw)
	if !ok {
		return json.RawMessage(`{}`)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(hide(v)); err != nil {
		return json.RawMessage(`{}`)
	}
	out := bytes.TrimRight(b.Bytes(), "\n")
	if len(out) > MaxArgumentBytes {
		return json.RawMessage(`{"truncated":true}`)
	}
	return out
}

// decodeJSON decodes one JSON value, keeping numbers as they're written.
func decodeJSON(raw json.RawMessage) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return v, true
}

func hide(v any) any {
	switch x := v.(type) {
	case map[string]any:
		secret := x["secret"] == true
		for k, val := range x {
			if slices.Contains(secretKeys, strings.ToLower(k)) || (secret && strings.EqualFold(k, "value")) {
				x[k] = Hidden
				continue
			}
			x[k] = hide(val)
		}
	case []any:
		for i := range x {
			x[i] = hide(x[i])
		}
	}
	return v
}

// flagsSecret reports whether any object in v says "secret": true.
func flagsSecret(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if x["secret"] == true {
			return true
		}
		for _, val := range x {
			if flagsSecret(val) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(x, flagsSecret)
	}
	return false
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
		// A stream that picks up where it left off needs the event it
		// stopped at, to know whether that was the end.
		if after > 0 {
			if prev, perr := s.store.Events(wctx, t.OrgID, t.ID, after-1, 1); perr == nil && len(prev) == 1 && prev[0].Seq == after {
				last = prev[0]
			}
		}
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
			if gerr == nil && finished(cur, cursor, last, s.now()) {
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

// finished reports whether a watch has everything: the task ended and the
// last event the watcher has is its final status, or the task ended a
// while ago and nothing more came (its driver died before writing the
// final status). A finished row alone isn't enough: a run this process
// drives can be cancelled before its last events are written.
func finished(t *Task, cursor int64, last Event, now time.Time) bool {
	if !Terminal(t.Status) || t.EventSeq > cursor {
		return false
	}
	if isFinalStatus(last) {
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
