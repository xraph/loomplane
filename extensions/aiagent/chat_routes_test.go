package aiagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/agentchat"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/security"
)

// fakeChat answers every call with err, and records what it was given.
type fakeChat struct {
	mu     sync.Mutex
	err    error
	calls  []string
	filter agentchat.ListFilter
	query  agentchat.HistoryQuery
	update agentchat.ConversationUpdate
	start  agentchat.StartInput
}

func (f *fakeChat) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return f.err
}

func (f *fakeChat) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return f.calls[len(f.calls)-1]
}

func sentTo(id string) *agentchat.Sent {
	return &agentchat.Sent{Conversation: agentchat.Conversation{ID: id}, Task: agentruns.Task{ID: "atask_1", SessionID: id}}
}

func (f *fakeChat) Start(_ context.Context, _ security.Principal, in agentchat.StartInput) (*agentchat.Sent, error) {
	f.mu.Lock()
	f.start = in
	f.mu.Unlock()
	if err := f.record("start " + in.Message + " " + in.ProjectID); err != nil {
		return nil, err
	}
	return sentTo("ses_1"), nil
}

func (f *fakeChat) Send(_ context.Context, _ security.Principal, id string, in agentchat.SendInput) (*agentchat.Sent, error) {
	if err := f.record("send " + id + " " + in.Message); err != nil {
		return nil, err
	}
	return sentTo(id), nil
}

func (f *fakeChat) Update(_ context.Context, _ security.Principal, id string, in agentchat.ConversationUpdate) (*agentchat.Conversation, error) {
	f.mu.Lock()
	f.update = in
	f.mu.Unlock()
	if err := f.record("update " + id); err != nil {
		return nil, err
	}
	return &agentchat.Conversation{ID: id, Title: "Renamed"}, nil
}

func (f *fakeChat) Delete(_ context.Context, _ security.Principal, id string) error {
	return f.record("delete " + id)
}

func (f *fakeChat) List(_ context.Context, _ security.Principal, lf agentchat.ListFilter) (*agentchat.ConversationList, error) {
	f.mu.Lock()
	f.filter = lf
	f.mu.Unlock()
	if err := f.record("list"); err != nil {
		return nil, err
	}
	return &agentchat.ConversationList{Conversations: []agentchat.Conversation{}}, nil
}

func (f *fakeChat) History(_ context.Context, _ security.Principal, id string, q agentchat.HistoryQuery) (*agentchat.History, error) {
	f.mu.Lock()
	f.query = q
	f.mu.Unlock()
	if err := f.record("history " + id); err != nil {
		return nil, err
	}
	return &agentchat.History{Conversation: agentchat.Conversation{ID: id}, Turns: []agentchat.Turn{}}, nil
}

// fakeTasks is the run driver. Watch sends events, stays quiet for idle,
// then closes; Get answers with task, or getErr.
type fakeTasks struct {
	mu        sync.Mutex
	task      agentruns.Task
	getErr    error
	cancelErr error
	watchErr  error
	events    []agentruns.Event
	idle      time.Duration
	watched   []int64
	calls     []string
}

func (f *fakeTasks) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeTasks) Get(_ context.Context, _ security.Principal, id string) (*agentruns.Task, error) {
	f.record("get " + id)
	if f.getErr != nil {
		return nil, f.getErr
	}
	t := f.task
	t.ID = id
	return &t, nil
}

func (f *fakeTasks) Cancel(_ context.Context, _ security.Principal, id string) error {
	f.record("cancel " + id)
	return f.cancelErr
}

func (f *fakeTasks) Watch(ctx context.Context, _ security.Principal, id string, after int64) (<-chan agentruns.Event, func(), error) {
	f.mu.Lock()
	f.watched = append(f.watched, after)
	f.mu.Unlock()
	if f.watchErr != nil {
		return nil, nil, f.watchErr
	}
	wctx, cancel := context.WithCancel(ctx)
	out := make(chan agentruns.Event)
	go func() {
		defer close(out)
		for _, e := range f.events {
			e.TaskID = id
			select {
			case out <- e:
			case <-wctx.Done():
				return
			}
		}
		select {
		case <-time.After(f.idle):
		case <-wctx.Done():
		}
	}()
	return out, cancel, nil
}

func (f *fakeTasks) watchedAfter() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.watched...)
}

func chatRig(t *testing.T, on bool) (forge.Router, *fakeChat, *fakeTasks) {
	t.Helper()
	r := forge.NewRouter()
	chat := &fakeChat{}
	runs := &fakeTasks{task: agentruns.Task{Status: agentruns.StatusCompleted}}
	if err := mountChatRoutes(r, nil, &agentscope.Binder{Tenants: tenants{on: on}}, chat, runs); err != nil {
		t.Fatal(err)
	}
	return r, chat, runs
}

func asAda() *security.Principal { return &security.Principal{OrgID: "org_1", UserID: "usr_ada"} }

// streamFrom opens the task stream with the query and Last-Event-ID
// given, and returns the response once the stream ends.
func streamFrom(t *testing.T, r forge.Router, query, lastID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/agent/tasks/atask_1/stream"+query, http.NoBody)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	req = req.WithContext(security.WithPrincipal(req.Context(), *asAda()))
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}

// frame is one SSE message of a stream: its id, event and data.
type frame struct{ id, event, data string }

// framesOf parses an SSE body into its messages, leaving out comments
// and the retry field.
func framesOf(body string) []frame {
	var out []frame
	for block := range strings.SplitSeq(body, "\n\n") {
		var f frame
		for line := range strings.SplitSeq(block, "\n") {
			switch {
			case strings.HasPrefix(line, "id: "):
				f.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				f.data += strings.TrimPrefix(line, "data: ")
			}
		}
		if f.event != "" || f.data != "" {
			out = append(out, f)
		}
	}
	return out
}

func TestStreamResumesFromLastEventID(t *testing.T) {
	r, _, runs := chatRig(t, true)
	for _, c := range []struct {
		query, lastID string
		want          int64
	}{
		{"?after=3", "7", 7},
		{"?after=9", "7", 9},
		{"", "", 0},
		{"?after=2", "", 2},
		{"?lastEventId=5", "", 5}, // a browser's EventSource
	} {
		before := len(runs.watchedAfter())
		out := streamFrom(t, r, c.query, c.lastID)
		watched := runs.watchedAfter()
		if out.Code != http.StatusOK || len(watched) != before+1 || watched[before] != c.want {
			t.Fatalf("%q, Last-Event-ID %q: %d, watched %v, want after %d", c.query, c.lastID, out.Code, watched, c.want)
		}
		if !strings.Contains(out.Body.String(), "retry: 2000\n") {
			t.Fatalf("no retry of 2000ms in %q", out.Body.String())
		}
	}
	for _, c := range []struct{ query, lastID string }{
		{"", "x"}, {"?after=x", ""}, {"?after=-1", ""}, {"", "-3"}, {"?after=3", "1.5"},
	} {
		before := len(runs.watchedAfter())
		out := streamFrom(t, r, c.query, c.lastID)
		if out.Code != http.StatusUnprocessableEntity || len(runs.watchedAfter()) != before {
			t.Fatalf("%q, Last-Event-ID %q: %d %s", c.query, c.lastID, out.Code, out.Body.String())
		}
		if strings.Contains(out.Header().Get("Content-Type"), "event-stream") {
			t.Fatalf("%q, Last-Event-ID %q opened a stream", c.query, c.lastID)
		}
	}
}

func TestStreamSendsKeepAlives(t *testing.T) {
	old := streamKeepAlive
	streamKeepAlive = 10 * time.Millisecond
	t.Cleanup(func() { streamKeepAlive = old })
	r, _, runs := chatRig(t, true)
	runs.idle = 50 * time.Millisecond

	body := streamFrom(t, r, "", "").Body.String()
	if n := strings.Count(body, ": keepalive\n\n"); n < 2 {
		t.Fatalf("%d keepalives in %q", n, body)
	}
	if last := strings.LastIndex(body, ": keepalive"); last > strings.Index(body, "event: agent.end") {
		t.Fatalf("a keepalive after the end: %q", body)
	}
	frames := framesOf(body)
	if len(frames) != 1 || frames[0].event != frameEnd {
		t.Fatalf("frames %+v", frames)
	}
}

func TestStreamEndsWithTheTaskStatus(t *testing.T) {
	r, _, runs := chatRig(t, true)
	runs.events = []agentruns.Event{
		{Seq: 4, Kind: "token", Data: json.RawMessage(`{"text":"Hel"}`)},
		{Seq: 5, Kind: "tool_call", Data: json.RawMessage(`{"tool":"list_projects","call_id":"c1"}`)},
		{Seq: 6, Kind: "safety_block", Data: json.RawMessage(`{"step_seq":2,"message":"stopped"}`)},
	}
	runs.task = agentruns.Task{Status: agentruns.StatusFailed, Error: "boom"}

	out := streamFrom(t, r, "", "3")
	frames := framesOf(out.Body.String())
	if len(frames) != 4 {
		t.Fatalf("frames %+v", frames)
	}
	for i, f := range frames[:3] {
		var ev agentruns.Event
		if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
			t.Fatal(err)
		}
		want := runs.events[i]
		if f.event != frameEvent || f.id != fmt.Sprint(want.Seq) || ev.Seq != want.Seq || ev.Kind != want.Kind ||
			string(ev.Data) != string(want.Data) || ev.TaskID != "atask_1" {
			t.Fatalf("frame %d: %+v", i, f)
		}
	}
	end := frames[3]
	if end.event != frameEnd || end.id != "" || end.data != `{"status":"failed","error":"boom"}` {
		t.Fatalf("the last frame: %+v", end)
	}

	// A task that ended without an error leaves it out.
	runs.events, runs.task = nil, agentruns.Task{Status: agentruns.StatusCompleted}
	frames = framesOf(streamFrom(t, r, "", "6").Body.String())
	if len(frames) != 1 || frames[0].data != `{"status":"completed"}` {
		t.Fatalf("frames %+v", frames)
	}
}

// A watch that ends with the task still open (the caller lost sight of it,
// or the client went) closes without agent.end, so the client reconnects
// and hears why.
func TestStreamWithoutAnEndClosesQuietly(t *testing.T) {
	r, _, runs := chatRig(t, true)
	runs.task = agentruns.Task{Status: agentruns.StatusRunning}
	if frames := framesOf(streamFrom(t, r, "", "").Body.String()); len(frames) != 0 {
		t.Fatalf("frames %+v", frames)
	}
}

func TestChatRoutesCallTheServices(t *testing.T) {
	r, chat, runs := chatRig(t, true)
	for _, c := range []struct {
		method, path, body string
		status             int
		call               string
	}{
		{http.MethodPost, "/api/v1/agent/conversations", `{"message":"hi","project_id":"proj_1"}`, http.StatusCreated, "start hi proj_1"},
		{http.MethodPost, "/api/v1/agent/conversations/ses_1/messages", `{"message":"more"}`, http.StatusCreated, "send ses_1 more"},
		{http.MethodGet, "/api/v1/agent/conversations/ses_1?before=atask_9&limit=5", "", http.StatusOK, "history ses_1"},
		{http.MethodPatch, "/api/v1/agent/conversations/ses_1", `{"title":"Renamed","pinned":true}`, http.StatusOK, "update ses_1"},
		{http.MethodDelete, "/api/v1/agent/conversations/ses_1", "", http.StatusNoContent, "delete ses_1"},
	} {
		out := serve(t, r, c.method, c.path, c.body, asAda())
		if out.Code != c.status || chat.last() != c.call {
			t.Fatalf("%s %s: %d %s, called %q", c.method, c.path, out.Code, out.Body.String(), chat.last())
		}
	}
	if chat.query != (agentchat.HistoryQuery{Before: "atask_9", Limit: 5}) {
		t.Fatalf("history query %+v", chat.query)
	}
	if u := chat.update; u.Title == nil || *u.Title != "Renamed" || u.Pinned == nil || !*u.Pinned {
		t.Fatalf("update %+v", u)
	}
	if out := serve(t, r, http.MethodPatch, "/api/v1/agent/conversations/ses_1", `{"pinned":false}`, asAda()); out.Code != http.StatusOK ||
		chat.update.Title != nil || chat.update.Pinned == nil || *chat.update.Pinned {
		t.Fatalf("unpin: %d %+v", out.Code, chat.update)
	}
	if out := serve(t, r, http.MethodPatch, "/api/v1/agent/conversations/ses_1", `{"title":`, asAda()); out.Code != http.StatusBadRequest {
		t.Fatalf("bad update body: %d", out.Code)
	}
	ctxBody := `{"message":"hi","context":{"path":"/projects/proj_1/services/web?environment=production",` +
		`"label":"Service web in production","project_id":"proj_1","service":"web","environment":"production","plan_id":"plan_42","kind":"service"}}`
	if out := serve(t, r, http.MethodPost, "/api/v1/agent/conversations", ctxBody, asAda()); out.Code != http.StatusCreated {
		t.Fatalf("start with a context: %d", out.Code)
	}
	want := agentchat.PageContext{Path: "/projects/proj_1/services/web?environment=production", Label: "Service web in production",
		ProjectID: "proj_1", Service: "web", Environment: "production", PlanID: "plan_42", Kind: "service"}
	if chat.start.Context == nil || *chat.start.Context != want {
		t.Fatalf("start context %+v", chat.start.Context)
	}
	var sent agentchat.Sent
	out := serve(t, r, http.MethodPost, "/api/v1/agent/conversations", `{"message":"hi"}`, asAda())
	if err := json.Unmarshal(out.Body.Bytes(), &sent); err != nil || sent.Conversation.ID != "ses_1" || sent.Task.ID != "atask_1" {
		t.Fatalf("sent %s, %v", out.Body.String(), err)
	}

	out = serve(t, r, http.MethodGet, "/api/v1/agent/conversations?project_id=proj_1&everyone=true&before=2026-11-01T10:00:00.5Z&limit=10", "", asAda())
	before := time.Date(2026, 11, 1, 10, 0, 0, 5e8, time.UTC)
	if out.Code != http.StatusOK || chat.filter.ProjectID != "proj_1" || !chat.filter.Everyone || !chat.filter.Before.Equal(before) || chat.filter.Limit != 10 {
		t.Fatalf("list: %d %+v", out.Code, chat.filter)
	}
	for _, q := range []string{"?everyone=maybe", "?before=yesterday", "?limit=ten"} {
		if out = serve(t, r, http.MethodGet, "/api/v1/agent/conversations"+q, "", asAda()); out.Code != http.StatusUnprocessableEntity {
			t.Fatalf("list%s: %d", q, out.Code)
		}
	}
	if out = serve(t, r, http.MethodPost, "/api/v1/agent/conversations", `{"message":`, asAda()); out.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", out.Code)
	}

	runs.task = agentruns.Task{Status: agentruns.StatusCancelled}
	var task agentruns.Task
	out = serve(t, r, http.MethodPost, "/api/v1/agent/tasks/atask_2/cancel", "", asAda())
	if err := json.Unmarshal(out.Body.Bytes(), &task); out.Code != http.StatusOK || err != nil || task.ID != "atask_2" || task.Status != agentruns.StatusCancelled {
		t.Fatalf("cancel: %d %s", out.Code, out.Body.String())
	}
	if got := runs.calls[len(runs.calls)-2:]; got[0] != "cancel atask_2" || got[1] != "get atask_2" {
		t.Fatalf("cancel called %q", got)
	}
	out = serve(t, r, http.MethodGet, "/api/v1/agent/tasks/atask_3", "", asAda())
	if out.Code != http.StatusOK || runs.calls[len(runs.calls)-1] != "get atask_3" {
		t.Fatalf("get: %d %q", out.Code, runs.calls)
	}
}

// The chat routes sit behind the binder: with the agent off they answer
// 409 and never reach the services.
func TestChatRoutesNeedTheAgentOn(t *testing.T) {
	r, chat, runs := chatRig(t, false)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/agent/conversations", ""},
		{http.MethodPost, "/api/v1/agent/conversations", `{"message":"hi"}`},
		{http.MethodPatch, "/api/v1/agent/conversations/ses_1", `{"pinned":true}`},
		{http.MethodDelete, "/api/v1/agent/conversations/ses_1", ""},
		{http.MethodGet, "/api/v1/agent/tasks/atask_1/stream", ""},
		{http.MethodPost, "/api/v1/agent/tasks/atask_1/cancel", ""},
	} {
		out := serve(t, r, c.method, c.path, c.body, asAda())
		if out.Code != http.StatusConflict || !strings.Contains(out.Body.String(), agentsettings.ErrDisabled.Error()) {
			t.Fatalf("%s %s with the agent off: %d %s", c.method, c.path, out.Code, out.Body.String())
		}
	}
	if len(chat.calls) != 0 || len(runs.calls) != 0 || len(runs.watchedAfter()) != 0 {
		t.Fatalf("calls %q %q", chat.calls, runs.calls)
	}
	if out := serve(t, r, http.MethodGet, "/api/v1/agent/tasks/atask_1/stream", "", nil); out.Code != http.StatusUnauthorized {
		t.Fatalf("no principal: %d", out.Code)
	}
}

func TestRouteErrorStatuses(t *testing.T) {
	r, chat, runs := chatRig(t, true)
	wrapped := func(err error) error { return fmt.Errorf("start the turn: %w", err) }
	for _, c := range []struct {
		err    error
		status int
		says   string
	}{
		{agentchat.ErrBusy, http.StatusConflict, agentchat.ErrBusy.Error()},
		{agentchat.ErrNotYours, http.StatusForbidden, agentchat.ErrNotYours.Error()},
		{agentchat.ErrNotYoursToChange, http.StatusForbidden, agentchat.ErrNotYoursToChange.Error()},
		{agentchat.ErrSending, http.StatusConflict, agentchat.ErrSending.Error()},
		{fmt.Errorf("%w: a conversation needs a title", agentchat.ErrBadChange), http.StatusBadRequest, "a conversation needs a title"},
		{agentchat.ErrNotFound, http.StatusNotFound, agentchat.ErrNotFound.Error()},
		{fmt.Errorf("%w: write a message", agentchat.ErrInvalid), http.StatusUnprocessableEntity, "invalid message: write a message"},
		{wrapped(agentsettings.ErrBudgetExhausted), http.StatusConflict, agentsettings.ErrBudgetExhausted.Error()},
		{wrapped(&metering.BlockedError{Reason: "Turn on the AI add-on in Settings > Billing, or add your own model key."}), http.StatusPaymentRequired, "Turn on the AI add-on in Settings"},
		{wrapped(metering.ErrNotReady), http.StatusServiceUnavailable, ""},
		{wrapped(agentsettings.ErrDisabled), http.StatusConflict, agentsettings.ErrDisabled.Error()},
		{agentruns.ErrNotFound, http.StatusNotFound, agentruns.ErrNotFound.Error()},
		{fmt.Errorf("%w: there's no such conversation", agentruns.ErrInvalid), http.StatusUnprocessableEntity, "no such conversation"},
		{fmt.Errorf("%w: only a person chats", security.ErrForbidden), http.StatusForbidden, ""},
	} {
		chat.err = c.err
		out := serve(t, r, http.MethodPost, "/api/v1/agent/conversations/ses_1/messages", `{"message":"more"}`, asAda())
		if out.Code != c.status || !strings.Contains(out.Body.String(), c.says) {
			t.Errorf("%v: %d %s, want %d", c.err, out.Code, out.Body.String(), c.status)
		}
	}
	chat.err = nil

	for _, c := range []struct {
		get, cancel error
		path        string
		status      int
	}{
		{agentruns.ErrNotFound, nil, "/api/v1/agent/tasks/atask_1", http.StatusNotFound},
		{nil, agentruns.ErrNotCancelable, "/api/v1/agent/tasks/atask_1/cancel", http.StatusConflict},
		{nil, agentruns.ErrNotFound, "/api/v1/agent/tasks/atask_1/cancel", http.StatusNotFound},
		{nil, fmt.Errorf("%w (ai:manage)", security.ErrForbidden), "/api/v1/agent/tasks/atask_1/cancel", http.StatusForbidden},
		{agentruns.ErrNotFound, nil, "/api/v1/agent/tasks/atask_1/stream", http.StatusNotFound},
	} {
		runs.getErr, runs.cancelErr = c.get, c.cancel
		method := http.MethodGet
		if strings.HasSuffix(c.path, "/cancel") {
			method = http.MethodPost
		}
		out := serve(t, r, method, c.path, "", asAda())
		if out.Code != c.status {
			t.Errorf("%s %s with %v/%v: %d %s", method, c.path, c.get, c.cancel, out.Code, out.Body.String())
		}
		if errors.Is(c.cancel, agentruns.ErrNotCancelable) && !strings.Contains(out.Body.String(), agentruns.ErrNotCancelable.Error()) {
			t.Errorf("not cancelable says %s", out.Body.String())
		}
	}
	if len(runs.watchedAfter()) != 0 {
		t.Fatal("a stream of a task the caller can't see was watched")
	}
	if errors.Is(chatFail(errors.New("boom")), agentruns.ErrNotFound) {
		t.Fatal("an unknown error mapped to not found")
	}
}

func TestABlockedSendAnswersTheReasonAlone(t *testing.T) {
	r, chat, _ := chatRig(t, true)
	const why = "Turn on the AI add-on in Settings > Billing, or add your own model key."
	chat.err = fmt.Errorf("start the turn: %w", &metering.BlockedError{Reason: why})
	out := serve(t, r, http.MethodPost, "/api/v1/agent/conversations/ses_1/messages", `{"message":"more"}`, asAda())
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil || out.Code != http.StatusPaymentRequired || body.Error != why {
		t.Fatalf("%d %s, want 402 with only the reason", out.Code, out.Body.String())
	}
}
