package agentruns

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

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

// wake wakes orgA's subscribers, as a write there would.
func (f *fakeWake) wake() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.subs[orgA] {
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
	wake.wake()
	if e := <-ch; e.Seq != 2 || e.Kind != "token" {
		t.Fatalf("second %+v", e)
	}
	if e := <-ch; e.Seq != 3 || e.Kind != "done" {
		t.Fatalf("third %+v", e)
	}
	if _, err = w.svc.finish(runCtx(task), task, StatusCompleted, 0, ""); err != nil {
		t.Fatal(err)
	}
	wake.wake()
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
	wake.wake()
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
	wake.wake()
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

func TestSafeArguments(t *testing.T) {
	for in, want := range map[string]string{
		`{"key":"DB_URL","value":"postgres://u:p@h/db","secret":true}`:                              `{"key":"DB_URL","secret":true,"value":"[hidden]"}`,
		`{"nested":{"api_key":"k","password":"p"}}`:                                                 `{"nested":{"api_key":"[hidden]","password":"[hidden]"}}`,
		`{"key":"PORT","value":"8080"}`:                                                             `{"key":"PORT","value":"8080"}`,
		`{"items":[{"Token":"t","private_key":"pk","secret_value":"sv","n":12345678901234567890}]}`: `{"items":[{"Token":"[hidden]","n":12345678901234567890,"private_key":"[hidden]","secret_value":"[hidden]"}]}`,
		`{"value":"v","secret":"true"}`:                                                             `{"secret":"true","value":"v"}`,
		``:                                                                                          `{}`,
		`not json`:                                                                                  `{}`,
		`{"note":"` + strings.Repeat("x", 5<<10) + `"}`:                                             `{"truncated":true}`,
	} {
		if got := string(SafeArguments(json.RawMessage(in))); got != want {
			t.Errorf("SafeArguments(%.60s) = %.80s, want %s", in, got, want)
		}
	}
}

// toolCall is a cortex tool_call event.
func toolCall(name, callID, args string) engine.StreamEvent {
	return engine.StreamEvent{Type: engine.EventToolCall, Data: map[string]any{"tool_name": name, "tool_id": callID, "arguments": args}}
}

func TestToolCallEventCarriesSafeArguments(t *testing.T) {
	w := newWorld(t)
	task := w.adaChat(t)
	events := make(chan engine.StreamEvent, 8)
	events <- toolCall("set_variable", "call_1", `{"project":"mono","key":"DB_URL","value":"postgres://u:p@h/db"}`)
	events <- toolCall("get_plan", "call_2", `{"plan_id":"plan_1","nested":{"api_key":"k","password":"p"}}`)
	events <- toolCall("set_variable", "call_3", `{"project":"mono","key":"PORT","value":"8080"}`)
	events <- toolCall("get_plan", "call_4", `{"plan_id":"`+strings.Repeat("x", 5<<10)+`"}`)
	// A secret_or_external call keeps no arguments, whatever they are.
	events <- toolCall("request_secret", "call_5", `{"project":"mono","key":"API_TOKEN","reason":"hunter3"}`)
	events <- toolCall("set_variable", "call_6", `{"project":"mono","key":"DB_PASSWORD","value":"hunter2","secret":true}`)
	close(events)
	w.svc.pump(context.Background(), task, events)
	evs := w.store.eventsOf(task.ID)
	want := map[string]string{
		"call_1": `{"key":"DB_URL","project":"mono","value":"postgres://u:p@h/db"}`,
		"call_2": `{"nested":{"api_key":"[hidden]","password":"[hidden]"},"plan_id":"plan_1"}`,
		"call_3": `{"key":"PORT","project":"mono","value":"8080"}`,
		"call_4": `{"truncated":true}`,
		"call_5": "",
		"call_6": "",
	}
	seen := 0
	for _, e := range evs {
		if e.Kind != string(engine.EventToolCall) {
			continue
		}
		seen++
		var data struct {
			Tool      string          `json:"tool"`
			CallID    string          `json:"call_id"`
			Risk      string          `json:"risk"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(e.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Tool == "" || data.Risk == "" {
			t.Errorf("tool_call %s", e.Data)
		}
		if got := string(data.Arguments); got != want[data.CallID] {
			t.Errorf("%s arguments %s, want %q", data.CallID, got, want[data.CallID])
		}
		if want[data.CallID] == "" && strings.Contains(string(e.Data), "arguments") {
			t.Errorf("%s keeps arguments: %s", data.CallID, e.Data)
		}
		for _, leak := range []string{"hunter2", "hunter3"} {
			if strings.Contains(string(e.Data), leak) {
				t.Errorf("%s carries %q: %s", data.CallID, leak, e.Data)
			}
		}
	}
	if seen != len(want) {
		t.Fatalf("%d tool calls, want %d: %+v", seen, len(want), evs)
	}
}

// toolResult is a tool_result row as the log keeps it.
type toolResult struct {
	Seq     int64
	Tool    string `json:"tool"`
	CallID  string `json:"call_id"`
	Outcome string `json:"outcome"`
	Message string `json:"message"`
}

func toolResults(t *testing.T, evs []Event) (results []toolResult, callSeq map[string]int64) {
	t.Helper()
	callSeq = map[string]int64{}
	for _, e := range evs {
		switch e.Kind {
		case string(engine.EventToolCall):
			var c struct {
				CallID string `json:"call_id"`
			}
			if err := json.Unmarshal(e.Data, &c); err != nil {
				t.Fatal(err)
			}
			callSeq[c.CallID] = e.Seq
		case EventToolResult:
			var r toolResult
			if err := json.Unmarshal(e.Data, &r); err != nil {
				t.Fatal(err)
			}
			r.Seq = e.Seq
			results = append(results, r)
		}
	}
	return results, callSeq
}

// Cortex fires a tool's hook on the run's goroutine while the pump may
// still have the tool's call in the channel: the outcome waits for its
// call and lands right after it.
func TestToolOutcomesFollowTheirCalls(t *testing.T) {
	w := newWorld(t)
	h := &Hooks{Service: w.svc}
	task := w.adaChat(t)
	runID := mustRunID(t, task.RunID)
	ctx := runCtx(task)
	err := w.svc.drive(context.Background(), task, func(_ context.Context, events chan<- engine.StreamEvent) error {
		events <- toolCall("list_projects", "call_1", `{}`)
		events <- toolCall("list_projects", "call_2", `{}`)
		events <- toolCall("approve_plan", "call_3", `{"plan_id":"plan_1","revision":"r1"}`)
		// The pump hasn't read any of them yet.
		for _, err := range []error{
			h.OnToolCompleted(ctx, runID, "list_projects", `[{"id":"prj_mono"}]`, time.Millisecond),
			h.OnToolCompleted(ctx, runID, "list_projects", `[{"id":"prj_mono"}]`, time.Millisecond),
			h.OnToolDenied(ctx, runID, "approve_plan", "approving production waits for a person"),
		} {
			if err != nil {
				t.Error(err)
			}
		}
		close(events)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	results, calls := toolResults(t, w.store.eventsOf(task.ID))
	want := []toolResult{
		{Tool: "list_projects", CallID: "call_1", Outcome: "ok"},
		{Tool: "list_projects", CallID: "call_2", Outcome: "ok"},
		{Tool: "approve_plan", CallID: "call_3", Outcome: "denied", Message: "approving production waits for a person"},
	}
	if len(results) != len(want) {
		t.Fatalf("results %+v", results)
	}
	for i, r := range results {
		seq := r.Seq
		r.Seq = 0
		if r != want[i] {
			t.Errorf("result %d %+v, want %+v", i, r, want[i])
		}
		if seq <= calls[r.CallID] {
			t.Errorf("%s's result at %d, its call at %d", r.CallID, seq, calls[r.CallID])
		}
	}

	// A hook after the call is written writes at once; a failure's message
	// is the error, cut to 300 bytes.
	err = w.svc.drive(context.Background(), task, func(_ context.Context, events chan<- engine.StreamEvent) error {
		go func() {
			defer close(events)
			events <- toolCall("get_plan", "call_4", `{"plan_id":"plan_1"}`)
			for {
				if _, c := toolResults(t, w.store.eventsOf(task.ID)); c["call_4"] > 0 {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if herr := h.OnToolFailed(ctx, runID, "get_plan", errors.New(strings.Repeat("é", 400))); herr != nil {
				t.Error(herr)
			}
		}()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	results, _ = toolResults(t, w.store.eventsOf(task.ID))
	last := results[len(results)-1]
	if last.CallID != "call_4" || last.Outcome != "failed" || len(last.Message) > 300 || len(last.Message) < 298 || !utf8.ValidString(last.Message) {
		t.Fatalf("failed result %+v (%d bytes)", last, len(last.Message))
	}
}

// A resumed run's outcomes are for the calls it paused on, written before
// this job: they match the pending calls of the last pause.
func TestResumedOutcomesMatchThePausedCalls(t *testing.T) {
	w := newWorld(t)
	h := &Hooks{Service: w.svc}
	task := w.adaChat(t)
	runID := mustRunID(t, task.RunID)
	events := make(chan engine.StreamEvent, 4)
	events <- toolCall("ask_person", "call_1", `{"question":"Which region?"}`)
	events <- engine.StreamEvent{Type: engine.EventSuspended, Data: map[string]any{
		"run_id": task.RunID, "reason": "external_tool",
		"pending": []suspension.PendingCall{{ID: "call_1", Name: "ask_person"}},
	}}
	close(events)
	w.svc.pump(context.Background(), task, events)

	err := w.svc.drive(context.Background(), task, func(_ context.Context, events chan<- engine.StreamEvent) error {
		if herr := h.OnToolCompleted(runCtx(task), runID, "ask_person", "eu-west-1", time.Minute); herr != nil {
			t.Error(herr)
		}
		close(events)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	results, calls := toolResults(t, w.store.eventsOf(task.ID))
	if len(results) != 1 || results[0].CallID != "call_1" || results[0].Outcome != "ok" || results[0].Seq <= calls["call_1"] {
		t.Fatalf("results %+v", results)
	}
}

func TestToolOutcomeWithoutAWriterIsDropped(t *testing.T) {
	w := newWorld(t)
	h := &Hooks{Service: w.svc}
	task := w.adaChat(t)
	before := len(w.store.eventsOf(task.ID))
	runID := mustRunID(t, task.RunID)
	for _, err := range []error{
		h.OnToolCompleted(runCtx(task), runID, "list_projects", "[]", time.Millisecond),
		h.OnToolFailed(runCtx(task), runID, "list_projects", errors.New("boom")),
		h.OnToolDenied(runCtx(task), runID, "list_projects", "no"),
		h.OnToolCompleted(runCtx(task), id.NewAgentRunID(), "list_projects", "[]", time.Millisecond),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if after := len(w.store.eventsOf(task.ID)); after != before {
		t.Fatalf("%d events written", after-before)
	}
}

func TestSafetyBlockWithholdsTheStepsText(t *testing.T) {
	w := newWorld(t)
	task := w.adaChat(t)
	events := make(chan engine.StreamEvent, 16)
	events <- engine.StreamEvent{Type: engine.EventStep, Data: map[string]any{"index": 1, "type": "generation"}}
	events <- engine.StreamEvent{Type: engine.EventToken, Data: map[string]any{"content": "Hello"}}
	events <- toolCall("list_projects", "call_1", `{}`)
	events <- engine.StreamEvent{Type: engine.EventStep, Data: map[string]any{"index": 2, "type": "generation"}}
	events <- engine.StreamEvent{Type: engine.EventToken, Data: map[string]any{"content": "AKIA"}}
	events <- engine.StreamEvent{Type: engine.EventToken, Data: map[string]any{"content": strings.Repeat(".", flushBytes)}}
	events <- engine.StreamEvent{Type: engine.EventToken, Data: map[string]any{"content": "..."}}
	events <- engine.StreamEvent{Type: engine.EventSafetyBlock, Data: map[string]any{"message": "cortex: the answer was blocked"}}
	close(events)
	w.svc.pump(context.Background(), task, events)

	got, err := w.svc.Events(context.Background(), person(orgA, ada), task.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, 0, len(got))
	for _, e := range got {
		kinds = append(kinds, e.Kind)
	}
	if want := "step,token,tool_call,step,token,token,safety_block"; strings.Join(kinds, ",") != want {
		t.Fatalf("kinds %s, want %s", strings.Join(kinds, ","), want)
	}
	if string(got[1].Data) != `{"text":"Hello"}` {
		t.Fatalf("step 1's text %s", got[1].Data)
	}
	for _, e := range got[4:6] {
		if string(e.Data) != `{"text":"","withheld":true}` {
			t.Fatalf("step 2's text %d: %s", e.Seq, e.Data)
		}
	}
	if !strings.Contains(string(got[6].Data), "blocked") {
		t.Fatalf("safety_block %s", got[6].Data)
	}
}
