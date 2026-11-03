package agentruns

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/cortex/engine"
	"github.com/xraph/cortex/id"
	"github.com/xraph/cortex/run"
	"github.com/xraph/cortex/suspension"

	"github.com/xraph/loomplane/internal/realtime"
)

func TestTokensAreCoalesced(t *testing.T) {
	w := newWorld(t)
	task := w.adaChat(t)
	events := make(chan engine.StreamEvent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.svc.pump(context.Background(), task, events)
	}()
	var want strings.Builder
	start := time.Now()
	for i := range 500 {
		s := string(rune('a' + i%26))
		want.WriteString(s)
		events <- engine.StreamEvent{Type: engine.EventToken, Data: map[string]any{"content": s, "index": i}}
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Logf("sending took %s", time.Since(start))
	}
	close(events)
	<-done
	evs := w.store.eventsOf(task.ID)
	if len(evs) != 1 || evs[0].Kind != string(engine.EventToken) {
		t.Fatalf("events %+v", evs)
	}
	var data struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(evs[0].Data, &data); err != nil || data.Text != want.String() {
		t.Fatalf("text %q, %v", data.Text, err)
	}
	if got := w.store.task(t, task.ID); got.EventSeq != 1 {
		t.Fatalf("event_seq %d", got.EventSeq)
	}
}

func TestTokensFlushAtTheirLimitAndInOrder(t *testing.T) {
	w := newWorld(t)
	task := w.adaChat(t)
	events := make(chan engine.StreamEvent, 16)
	big := strings.Repeat("y", flushBytes)
	events <- engine.StreamEvent{Type: engine.EventToken, Data: map[string]any{"content": big}}
	events <- engine.StreamEvent{Type: engine.EventToken, Data: map[string]any{"content": "a"}}
	events <- engine.StreamEvent{Type: engine.EventStep, Data: map[string]any{"step_id": "s1", "index": 1, "type": "generation"}}
	events <- engine.StreamEvent{Type: engine.EventToken, Data: map[string]any{"content": "b"}}
	events <- engine.StreamEvent{Type: engine.EventDone, Data: map[string]any{"output": "done", "tokens_used": 9, "state": "completed"}}
	close(events)
	w.svc.pump(context.Background(), task, events)
	evs := w.store.eventsOf(task.ID)
	kinds := make([]string, 0, len(evs))
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	if got := strings.Join(kinds, ","); got != "token,token,step,token,done" {
		t.Fatalf("kinds %s", got)
	}
}

func TestToolCallEventsHideSecretArguments(t *testing.T) {
	w := newWorld(t)
	task := w.adaChat(t)
	events := make(chan engine.StreamEvent, 4)
	events <- engine.StreamEvent{Type: engine.EventToolCall, Data: map[string]any{
		"tool_name": "set_variable", "tool_id": "call_7",
		"arguments": `{"project":"mono","key":"DB_PASSWORD","value":"hunter2","secret":true}`,
	}}
	events <- engine.StreamEvent{Type: engine.EventSuspended, Data: map[string]any{
		"run_id": task.RunID, "reason": "external_tool",
		"pending": []suspension.PendingCall{{ID: "call_8", Name: "request_secret", Arguments: `{"key":"API_TOKEN","secret":"hunter3"}`}},
	}}
	close(events)
	w.svc.pump(context.Background(), task, events)
	evs := w.store.eventsOf(task.ID)
	if len(evs) != 2 {
		t.Fatalf("events %+v", evs)
	}
	for _, e := range evs {
		raw := string(e.Data)
		for _, leak := range []string{"hunter2", "hunter3", "DB_PASSWORD", "API_TOKEN", "arguments", `"secret"`} {
			if strings.Contains(raw, leak) {
				t.Errorf("%s event carries %q: %s", e.Kind, leak, raw)
			}
		}
	}
	var call struct {
		Tool   string `json:"tool"`
		CallID string `json:"call_id"`
		Risk   string `json:"risk"`
	}
	if err := json.Unmarshal(evs[0].Data, &call); err != nil || call.Tool != "set_variable" || call.CallID != "call_7" || call.Risk != "change" {
		t.Fatalf("tool call %+v, %v (%s)", call, err, evs[0].Data)
	}
	if !strings.Contains(string(evs[1].Data), "request_secret") || !strings.Contains(string(evs[1].Data), "call_8") {
		t.Fatalf("suspended %s", evs[1].Data)
	}
}

// fakeWake is the realtime bus: one wake channel per subscriber.
type fakeWake struct {
	mu   sync.Mutex
	subs map[string][]chan struct{}
}

func (f *fakeWake) source() realtime.WakeSource {
	return func(org string) (<-chan struct{}, func()) {
		f.mu.Lock()
		defer f.mu.Unlock()
		ch := make(chan struct{}, 1)
		f.subs[org] = append(f.subs[org], ch)
		return ch, func() {}
	}
}

func (f *fakeWake) wake(org string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.subs[org] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (f *fakeWake) subscribed(org string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs[org]) > 0
}

func TestWatchFollowsUntilDone(t *testing.T) {
	w := newWorld(t)
	wake := &fakeWake{subs: map[string][]chan struct{}{}}
	w.svc.wake = wake.source()
	w.svc.poll = time.Hour // only wakes move it
	task := w.adaChat(t)
	ctx := context.Background()
	mustMove(t, w, task, StatusRunning)
	if _, err := w.store.AppendEvents(ctx, orgA, task.ID, []NewEvent{{Kind: "run_started", Data: json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Get(ctx, person(orgA, bo), task.ID); err == nil {
		t.Fatal("Bo reads Ada's chat")
	}
	if _, _, err := w.svc.Watch(ctx, person(orgA, bo), task.ID, 0); err == nil {
		t.Fatal("Bo watches Ada's chat")
	}
	ch, stop, err := w.svc.Watch(ctx, person(orgA, ada), task.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	first := <-ch
	if first.Seq != 1 || first.Kind != "run_started" {
		t.Fatalf("first %+v", first)
	}
	for !wake.subscribed(orgA) {
		time.Sleep(time.Millisecond)
	}
	if _, err = w.store.AppendEvents(ctx, orgA, task.ID, []NewEvent{
		{Kind: "token", Data: json.RawMessage(`{"text":"hi"}`)},
		{Kind: "done", Data: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatal(err)
	}
	wake.wake(orgA)
	if e := <-ch; e.Seq != 2 || e.Kind != "token" {
		t.Fatalf("second %+v", e)
	}
	if e := <-ch; e.Seq != 3 || e.Kind != "done" {
		t.Fatalf("third %+v", e)
	}
	if _, err = w.svc.finish(runCtx(task), task, StatusCompleted, 0, ""); err != nil {
		t.Fatal(err)
	}
	wake.wake(orgA)
	last := <-ch
	if last.Kind != EventStatus || last.Seq != 4 {
		t.Fatalf("last %+v", last)
	}
	select {
	case e, ok := <-ch:
		if ok {
			t.Fatalf("an event after the end: %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the watch didn't close after the task finished")
	}
}

// A watcher who loses the right to see the task stops getting its events.
func TestWatchStopsWhenTheWatcherLosesAccess(t *testing.T) {
	w := newWorld(t)
	wake := &fakeWake{subs: map[string][]chan struct{}{}}
	w.svc.wake = wake.source()
	w.svc.poll = time.Hour
	if err := w.svc.RegisterKind(KindSpec{Kind: KindInvestigation, Agent: "loomplane-investigation", MaxSteps: 1, DefaultMaxTokens: 1, MaxTokens: 1}); err != nil {
		t.Fatal(err)
	}
	task := w.start(t, StartInput{Kind: KindInvestigation, ProjectID: "prj_mono"})
	ch, stop, err := w.svc.Watch(context.Background(), person(orgA, vi), task.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for !wake.subscribed(orgA) {
		time.Sleep(time.Millisecond)
	}
	w.members.set(orgA, vi, "")
	if err = w.authz.Revoke(context.Background(), orgA, vi); err != nil {
		t.Fatal(err)
	}
	if _, err = w.store.AppendEvents(context.Background(), orgA, task.ID, []NewEvent{{Kind: "token", Data: json.RawMessage(`{"text":"x"}`)}}); err != nil {
		t.Fatal(err)
	}
	wake.wake(orgA)
	select {
	case e, ok := <-ch:
		if ok {
			t.Fatalf("an event after access went: %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the watch kept going")
	}
}

// Cortex fires a run's terminal hook before it sends done, while the pump
// still holds text it hasn't written. The task mustn't read as finished
// until its log has everything: the text, done and the final status.
func TestTaskFinishesOnlyAfterItsLastEvent(t *testing.T) {
	w := newWorld(t)
	h := &Hooks{Service: w.svc}
	task := w.adaChat(t)
	w.engine.setRun(task.RunID, &run.Run{State: run.StateCompleted, TokensUsed: 42})
	hooked, sendDone := make(chan struct{}), make(chan struct{})
	w.engine.script = func(events chan<- engine.StreamEvent) {
		events <- engine.StreamEvent{Type: engine.EventRunStarted, Data: map[string]any{}}
		events <- engine.StreamEvent{Type: engine.EventToken, Data: map[string]any{"content": "all green"}}
		for w.store.task(t, task.ID).EventSeq < 2 {
			time.Sleep(time.Millisecond)
		}
		if err := h.OnRunCompleted(runCtx(task), id.Nil, mustRunID(t, task.RunID), "all green", time.Second); err != nil {
			t.Error(err)
		}
		close(hooked)
		<-sendDone
		events <- engine.StreamEvent{Type: engine.EventDone, Data: map[string]any{"output": "all green", "state": "completed"}}
	}
	jobDone := make(chan error, 1)
	go func() { jobDone <- w.runStart(t, task) }()
	<-hooked

	// Between the hook and done, the task is still open, so a reader who
	// sees it finished always finds the whole log.
	mid := w.store.task(t, task.ID)
	if Terminal(mid.Status) {
		t.Errorf("the task reads %s before its log has done (event_seq %d)", mid.Status, mid.EventSeq)
	}
	// If this process died now, the sweep would continue the run and
	// settle the task from cortex's record.
	w.store.mu.Lock()
	_, marked := w.store.queued[task.ID]
	w.store.mu.Unlock()
	if !marked {
		t.Error("a run waiting on its last events isn't marked for the sweep")
	}
	// A stream reconnecting now, with everything written so far, stays
	// open until done and the final status.
	ch, stop, err := w.svc.Watch(context.Background(), person(orgA, ada), task.ID, mid.EventSeq)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	var seen []string
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for e := range ch {
			seen = append(seen, e.Kind)
		}
	}()
	select {
	case <-closed:
		t.Error("a stream that reconnected between the hook and done closed before done")
	case <-time.After(50 * time.Millisecond):
	}

	close(sendDone)
	if err = <-jobDone; err != nil {
		t.Fatal(err)
	}
	got := w.store.task(t, task.ID)
	if got.Status != StatusCompleted || got.Tokens != 42 {
		t.Fatalf("task %+v", got)
	}
	evs := w.store.eventsOf(task.ID)
	kinds := make([]string, 0, len(evs))
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	if want := "status,run_started,token,done,status"; strings.Join(kinds, ",") != want {
		t.Fatalf("kinds %s, want %s", strings.Join(kinds, ","), want)
	}
	if !isFinalStatus(evs[len(evs)-1]) {
		t.Fatalf("last event %+v", evs[len(evs)-1])
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the reconnected stream didn't close after the final status")
	}
	if n := len(seen); n < 2 || seen[n-2] != "done" || seen[n-1] != EventStatus {
		t.Fatalf("the reconnected stream got %v, want it to end with done,status", seen)
	}
	if _, finished := w.handler.all(); !slices.Equal(finished, []string{task.ID}) {
		t.Fatalf("finished %v", finished)
	}
}

// A finished task whose final status isn't written yet (a cancel of a run
// this process drives writes it after the run's last events) keeps a
// reconnecting stream open until the status comes. One that has it closes.
func TestWatchFromTheEndWaitsForTheFinalStatus(t *testing.T) {
	w := newWorld(t)
	wake := &fakeWake{subs: map[string][]chan struct{}{}}
	w.svc.wake = wake.source()
	w.svc.poll = time.Hour
	task := w.adaChat(t)
	ctx := context.Background()
	if _, err := w.store.AppendEvents(ctx, orgA, task.ID, []NewEvent{{Kind: "token", Data: json.RawMessage(`{"text":"hi"}`)}}); err != nil {
		t.Fatal(err)
	}
	mustMove(t, w, task, StatusCancelled)
	at := w.store.task(t, task.ID).EventSeq
	ch, stop, err := w.svc.Watch(ctx, person(orgA, ada), task.ID, at)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("the stream closed before the final status")
		}
		t.Fatalf("an event before the final status: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err = w.store.AppendEvents(ctx, orgA, task.ID, []NewEvent{*statusEvent(StatusCancelled, "")}); err != nil {
		t.Fatal(err)
	}
	wake.wake(orgA)
	if e, ok := <-ch; !ok || !isFinalStatus(e) {
		t.Fatalf("got %+v, %v", e, ok)
	}
	if _, ok := <-ch; ok {
		t.Fatal("an event after the final status")
	}

	// Reconnecting after the final status closes at once.
	again, stopAgain, err := w.svc.Watch(ctx, person(orgA, ada), task.ID, at+1)
	if err != nil {
		t.Fatal(err)
	}
	defer stopAgain()
	select {
	case e, ok := <-again:
		if ok {
			t.Fatalf("an event after the end: %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a stream that had everything didn't close")
	}
}
