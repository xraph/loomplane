package agentruns

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/cortex/engine"
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
