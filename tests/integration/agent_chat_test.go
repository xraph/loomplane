//go:build integration

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/agentchat"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/agenttest"
)

// chatOrg is a workspace with the agent on and a budget, on a server
// whose agent runs on fake: Ada owns it, Bo is a developer.
type chatOrg struct {
	*agentOrg
	bo *client
}

func startChatOrg(t *testing.T, fake *agenttest.FakeAnthropic) *chatOrg {
	t.Helper()
	a := startAgentOrg(t, fake.AIConfig())
	t.Cleanup(a.r.stop)
	a.budget("10")
	a.enable(nil)
	return &chatOrg{agentOrg: a, bo: joinAs(t, a, "bo@example.com", "developer")}
}

// converse starts a conversation as c.
func converse(t *testing.T, c *client, message string) agentchat.Sent {
	t.Helper()
	var sent agentchat.Sent
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/agent/conversations", map[string]string{"message": message}, &sent)
	if sent.Conversation.ID == "" || sent.Task.ID == "" || sent.Conversation.ID != sent.Task.SessionID {
		t.Fatalf("sent %+v", sent)
	}
	return sent
}

// sseFrame is one message of a task stream.
type sseFrame struct {
	ID, Event, Data string
}

// event is an agent.event frame's event.
func (f sseFrame) event(t *testing.T) agentruns.Event {
	t.Helper()
	var e agentruns.Event
	if err := json.Unmarshal([]byte(f.Data), &e); err != nil {
		t.Fatalf("frame %+v: %v", f, err)
	}
	return e
}

// taskStream is an open GET /agent/tasks/:id/stream.
type taskStream struct {
	t      *testing.T
	res    *http.Response
	rd     *bufio.Reader
	cancel context.CancelFunc
	// keepalives counts the comments read.
	keepalives int
}

// openStream opens a task's stream as c, resuming after lastID when it's
// set, and fails the test unless it answers 200. The stream gives up
// after two minutes.
func openStream(t *testing.T, c *client, taskID, query, lastID string) *taskStream {
	t.Helper()
	s, status, body := tryStream(t, c, taskID, query, lastID)
	if status != http.StatusOK {
		t.Fatalf("stream %s%s: %d %s", taskID, query, status, body)
	}
	return s
}

// tryStream opens a task's stream and returns it on a 200, or the status
// and body otherwise.
func tryStream(t *testing.T, c *client, taskID, query, lastID string) (*taskStream, int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/agent/tasks/"+taskID+"/stream"+query, http.NoBody)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("X-Publishable-Key", c.key)
	req.Header.Set("Accept", "text/event-stream")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("stream %s: %v", taskID, err)
	}
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		cancel()
		return nil, res.StatusCode, string(raw)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("stream %s: content type %q", taskID, ct)
	}
	s := &taskStream{t: t, res: res, rd: bufio.NewReader(res.Body), cancel: cancel}
	t.Cleanup(s.close)
	return s, res.StatusCode, ""
}

// close drops the connection.
func (s *taskStream) close() {
	s.cancel()
	_ = s.res.Body.Close()
}

// next reads the next frame with an event or data, skipping comments
// and retry fields. It reports false when the stream ends.
func (s *taskStream) next() (sseFrame, bool) {
	s.t.Helper()
	var f sseFrame
	for {
		line, err := s.rd.ReadString('\n')
		if err != nil {
			if f.Event != "" || f.Data != "" {
				s.t.Fatalf("the stream ended inside a frame: %+v", f)
			}
			return sseFrame{}, false
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case line == "":
			if f.Event != "" || f.Data != "" {
				return f, true
			}
			f = sseFrame{}
		case strings.HasPrefix(line, ":"):
			s.keepalives++
		case strings.HasPrefix(line, "id: "):
			f.ID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			f.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			f.Data += strings.TrimPrefix(line, "data: ")
		case strings.HasPrefix(line, "retry: "):
		default:
			s.t.Fatalf("a line the SSE grammar doesn't have: %q", line)
		}
	}
}

// until reads frames until one satisfies stop, which it returns with the
// frames before it.
func (s *taskStream) until(stop func(sseFrame) bool) (before []sseFrame, at sseFrame) {
	s.t.Helper()
	for {
		f, ok := s.next()
		if !ok {
			s.t.Fatalf("the stream ended first; frames so far: %v", frameKinds(s.t, before))
		}
		if stop(f) {
			return before, f
		}
		before = append(before, f)
	}
}

// rest reads the stream to its end, which must be agent.end and nothing
// after it, and returns the frames before it and the end.
func (s *taskStream) rest() ([]sseFrame, struct{ Status, Error string }) {
	s.t.Helper()
	frames, end := s.until(func(f sseFrame) bool { return f.Event == "agent.end" })
	if f, ok := s.next(); ok {
		s.t.Fatalf("a frame after agent.end: %+v", f)
	}
	var out struct{ Status, Error string }
	if err := json.Unmarshal([]byte(end.Data), &out); err != nil {
		s.t.Fatalf("agent.end %q: %v", end.Data, err)
	}
	for _, f := range frames {
		if f.Event != "agent.event" {
			s.t.Fatalf("a %q frame before agent.end", f.Event)
		}
	}
	return frames, out
}

// kindIs stops at an agent.event of kind.
func kindIs(t *testing.T, kind string) func(sseFrame) bool {
	return func(f sseFrame) bool { return f.Event == "agent.event" && f.event(t).Kind == kind }
}

func frameKinds(t *testing.T, frames []sseFrame) []string {
	out := make([]string, 0, len(frames))
	for _, f := range frames {
		if f.Event != "agent.event" {
			out = append(out, f.Event)
			continue
		}
		out = append(out, f.event(t).Kind)
	}
	return out
}

// checkSeqs fails unless the frames are agent.events with ids first,
// first+1 and so on, each id its event's seq.
func checkSeqs(t *testing.T, frames []sseFrame, first int64) {
	t.Helper()
	for i, f := range frames {
		want := first + int64(i)
		e := f.event(t)
		if f.ID != strconv.FormatInt(want, 10) || e.Seq != want {
			t.Fatalf("frame %d: id %q seq %d, want %d; kinds %v", i, f.ID, e.Seq, want, frameKinds(t, frames))
		}
	}
}

// sameEvents fails unless the frames carry exactly evs.
func sameEvents(t *testing.T, frames []sseFrame, evs []agentruns.Event) {
	t.Helper()
	if len(frames) != len(evs) {
		t.Fatalf("%d frames, %d events: %v vs %v", len(frames), len(evs), frameKinds(t, frames), kindsOf(evs))
	}
	for i, f := range frames {
		got, want := f.event(t), evs[i]
		if got.Seq != want.Seq || got.Kind != want.Kind || got.TaskID != want.TaskID || string(got.Data) != string(want.Data) {
			t.Fatalf("frame %d: %+v, the log has %+v", i, got, want)
		}
	}
}

// taskOf reads a task as c.
func taskOf(t *testing.T, c *client, id string) agentruns.Task {
	t.Helper()
	var task agentruns.Task
	c.must(http.StatusOK, http.MethodGet, "/api/v1/agent/tasks/"+id, nil, &task)
	return task
}

// waitTask polls the task as c until done says so.
func waitTask(t *testing.T, c *client, id string, done func(agentruns.Task) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		task := taskOf(t, c, id)
		if done(task) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s stayed %s", id, task.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func history(t *testing.T, c *client, id string) agentchat.History {
	t.Helper()
	var h agentchat.History
	c.must(http.StatusOK, http.MethodGet, "/api/v1/agent/conversations/"+id, nil, &h)
	return h
}

// A conversation starts with a message, its task streams every event in
// order, ending with agent.end, and its history has the same events.
func TestChat_startStreamAndHistory(t *testing.T) {
	fake := agenttest.Anthropic(t, agenttest.Turn{Text: "Two projects are deployed."})
	o := startChatOrg(t, fake)

	sent := converse(t, o.owner, "What's deployed?")
	if sent.Conversation.Title != "What's deployed?" || sent.Conversation.Turns != 1 || sent.Task.Kind != agentruns.KindChat {
		t.Fatalf("sent %+v", sent)
	}
	frames, end := openStream(t, o.owner, sent.Task.ID, "", "").rest()
	if end.Status != agentruns.StatusCompleted || end.Error != "" {
		t.Fatalf("agent.end %+v", end)
	}
	checkSeqs(t, frames, 1)
	kinds := frameKinds(t, frames)
	if !slices.Contains(kinds, "run_started") || !slices.Contains(kinds, "token") || kinds[len(kinds)-1] != agentruns.EventStatus {
		t.Fatalf("kinds %v", kinds)
	}

	h := history(t, o.owner, sent.Conversation.ID)
	if len(h.Turns) != 1 || h.More || h.Conversation.ID != sent.Conversation.ID || h.Conversation.Status != agentruns.StatusCompleted {
		t.Fatalf("history %+v", h)
	}
	turn := h.Turns[0]
	if turn.TaskID != sent.Task.ID || turn.Message != "What's deployed?" || turn.Status != agentruns.StatusCompleted ||
		turn.EventSeq != int64(len(frames)) || turn.FinishedAt == nil {
		t.Fatalf("turn %+v", turn)
	}
	sameEvents(t, frames, turn.Events)

	var list agentchat.ConversationList
	o.owner.must(http.StatusOK, http.MethodGet, "/api/v1/agent/conversations", nil, &list)
	if len(list.Conversations) != 1 || list.Conversations[0].ID != sent.Conversation.ID || list.Conversations[0].LastTaskID != sent.Task.ID {
		t.Fatalf("list %+v", list)
	}

	// The stream's frames are in the AsyncAPI spec the clients are made from.
	var async struct {
		Channels map[string]struct {
			Address  string                     `json:"address"`
			Messages map[string]json.RawMessage `json:"messages"`
		} `json:"channels"`
	}
	o.owner.must(http.StatusOK, http.MethodGet, "/asyncapi.json", nil, &async)
	found := false
	for _, ch := range async.Channels {
		if ch.Address == "/api/v1/agent/tasks/:id/stream" {
			found = ch.Messages["agent.event"] != nil && ch.Messages["agent.end"] != nil
		}
	}
	if !found {
		t.Fatal("the AsyncAPI spec has no task stream with agent.event and agent.end")
	}
}

// A stream that drops mid-run picks up with Last-Event-ID at the next
// event: between the two connections every event comes once, with no
// gap, and a reconnect after the end hears only agent.end.
func TestChat_streamResumesAfterADrop(t *testing.T) {
	gate := make(chan struct{})
	fake := agenttest.Anthropic(t,
		agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "list_projects", Input: map[string]any{}}}},
		agenttest.Turn{Deltas: []string{"Two ", "projects."}, DeltaGap: 50 * time.Millisecond, Wait: gate},
	)
	o := startChatOrg(t, fake)
	sent := converse(t, o.owner, "What's deployed?")

	first := openStream(t, o.owner, sent.Task.ID, "", "")
	before, call := first.until(kindIs(t, "tool_call"))
	if e := call.event(t); !strings.Contains(string(e.Data), `"tool":"list_projects"`) {
		t.Fatalf("tool_call %s", e.Data)
	}
	got := append(before, call)
	checkSeqs(t, got, 1)
	first.close()
	k := call.event(t).Seq

	close(gate)
	frames, end := openStream(t, o.owner, sent.Task.ID, "", strconv.FormatInt(k, 10)).rest()
	if end.Status != agentruns.StatusCompleted {
		t.Fatalf("agent.end %+v", end)
	}
	if len(frames) == 0 || frames[0].ID != strconv.FormatInt(k+1, 10) {
		t.Fatalf("the resumed stream starts at %v, want %d", frameKinds(t, frames), k+1)
	}
	checkSeqs(t, frames, k+1)
	got = append(got, frames...)
	text := ""
	for _, f := range got {
		if e := f.event(t); e.Kind == "token" {
			text += tokenText(t, e)
		}
	}
	if text != "Two projects." {
		t.Fatalf("the two streams' text %q", text)
	}
	sameEvents(t, got, history(t, o.owner, sent.Conversation.ID).Turns[0].Events)

	last := got[len(got)-1].ID
	again, end := openStream(t, o.owner, sent.Task.ID, "", last).rest()
	if len(again) != 0 || end.Status != agentruns.StatusCompleted {
		t.Fatalf("after the end: %v, %+v", frameKinds(t, again), end)
	}
}

// One stream follows a run through a pause: the request, then, once Ada
// answers, the resumed run's start, its text and the end.
func TestChat_streamFollowsARunAcrossAPause(t *testing.T) {
	fake := agenttest.Anthropic(t,
		agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "ask_person", Input: map[string]string{"question": "Which region?"}}}},
		agenttest.Turn{Text: "Using eu-west-1."},
	)
	o := startChatOrg(t, fake)
	sent := converse(t, o.owner, "Deploy to the right region.")

	s := openStream(t, o.owner, sent.Task.ID, "", "")
	before, asked := s.until(kindIs(t, agentruns.EventRequest))
	var req struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(asked.event(t).Data, &req); err != nil || req.RequestID == "" {
		t.Fatalf("request event %s: %v", asked.Data, err)
	}
	o.owner.must(http.StatusOK, http.MethodPost, "/api/v1/agent/requests/"+req.RequestID+"/answer", map[string]string{"text": "eu-west-1"}, nil)

	after, end := s.rest()
	if end.Status != agentruns.StatusCompleted {
		t.Fatalf("agent.end %+v", end)
	}
	all := append(append(before, asked), after...)
	checkSeqs(t, all, 1)
	resumed, text := false, ""
	for _, f := range after {
		e := f.event(t)
		switch e.Kind {
		case "run_started":
			resumed = resumed || strings.Contains(string(e.Data), `"resumed":true`)
		case "token":
			if !resumed {
				t.Fatalf("a token before the resumed run started: %v", frameKinds(t, after))
			}
			text += tokenText(t, e)
		}
	}
	if !resumed || text != "Using eu-west-1." {
		t.Fatalf("after the answer: resumed %v, text %q, kinds %v", resumed, text, frameKinds(t, after))
	}
}

// ?after= with no header replays from the seq after it.
func TestChat_streamAfterQuery(t *testing.T) {
	fake := agenttest.Anthropic(t, agenttest.Turn{Text: "hello"})
	o := startChatOrg(t, fake)
	sent := converse(t, o.owner, "Say hello.")
	waitTask(t, o.owner, sent.Task.ID, func(task agentruns.Task) bool { return agentruns.Terminal(task.Status) })

	frames, end := openStream(t, o.owner, sent.Task.ID, "?after=2", "").rest()
	if end.Status != agentruns.StatusCompleted {
		t.Fatalf("agent.end %+v", end)
	}
	checkSeqs(t, frames, 3)
	evs := history(t, o.owner, sent.Conversation.ID).Turns[0].Events
	sameEvents(t, frames, evs[2:])

	// The larger of the two wins.
	frames, _ = openStream(t, o.owner, sent.Task.ID, "?after=1", "3").rest()
	checkSeqs(t, frames, 4)
	if _, status, _ := tryStream(t, o.owner, sent.Task.ID, "?after=two", ""); status != http.StatusUnprocessableEntity {
		t.Fatalf("?after=two: %d", status)
	}
}

// A conversation is its starter's: anyone else gets 404 for all of it,
// and an admin reads it but can't add to it. Bo's list is his own, and
// only an admin lists everyone's.
func TestChat_privacy(t *testing.T) {
	fake := agenttest.Anthropic(t, agenttest.Turn{Text: "Ada's answer."}, agenttest.Turn{Text: "Bo's answer."})
	o := startChatOrg(t, fake)
	adas := converse(t, o.owner, "Ada's question")
	waitTask(t, o.owner, adas.Task.ID, func(task agentruns.Task) bool { return agentruns.Terminal(task.Status) })

	conv, task := "/api/v1/agent/conversations/"+adas.Conversation.ID, "/api/v1/agent/tasks/"+adas.Task.ID
	o.bo.must(http.StatusNotFound, http.MethodGet, conv, nil, nil)
	o.bo.must(http.StatusNotFound, http.MethodGet, task, nil, nil)
	if _, status, _ := tryStream(t, o.bo, adas.Task.ID, "", ""); status != http.StatusNotFound {
		t.Fatalf("Bo streams Ada's task: %d", status)
	}
	o.bo.must(http.StatusNotFound, http.MethodPost, conv+"/messages", map[string]string{"message": "me too"}, nil)
	o.bo.must(http.StatusNotFound, http.MethodPost, task+"/cancel", nil, nil)

	bos := converse(t, o.bo, "Bo's question")
	waitTask(t, o.bo, bos.Task.ID, func(task agentruns.Task) bool { return agentruns.Terminal(task.Status) })
	if h := history(t, o.owner, bos.Conversation.ID); len(h.Turns) != 1 || h.Turns[0].Message != "Bo's question" {
		t.Fatalf("Ada reads Bo's conversation: %+v", h)
	}
	status, body := send(o.owner, http.MethodPost, "/api/v1/agent/conversations/"+bos.Conversation.ID+"/messages", map[string]string{"message": "an admin's word"})
	if status != http.StatusForbidden || !strings.Contains(body, agentchat.ErrNotYours.Error()) {
		t.Fatalf("Ada adds to Bo's conversation: %d %s", status, body)
	}

	var list agentchat.ConversationList
	o.bo.must(http.StatusOK, http.MethodGet, "/api/v1/agent/conversations", nil, &list)
	if len(list.Conversations) != 1 || list.Conversations[0].ID != bos.Conversation.ID {
		t.Fatalf("Bo's list %+v", list)
	}
	o.bo.must(http.StatusForbidden, http.MethodGet, "/api/v1/agent/conversations?everyone=true", nil, nil)
	o.owner.must(http.StatusOK, http.MethodGet, "/api/v1/agent/conversations?everyone=true", nil, &list)
	if len(list.Conversations) != 2 {
		t.Fatalf("everyone's %+v", list)
	}
}

// While the agent is still on a turn, the next message is refused with
// ErrBusy's words.
func TestChat_busy(t *testing.T) {
	fake := agenttest.Anthropic(t, agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "ask_person", Input: map[string]string{"question": "Which region?"}}}})
	o := startChatOrg(t, fake)
	sent := converse(t, o.owner, "Deploy to the right region.")
	waitTask(t, o.owner, sent.Task.ID, func(task agentruns.Task) bool { return task.Status == agentruns.StatusWaiting })

	status, body := send(o.owner, http.MethodPost, "/api/v1/agent/conversations/"+sent.Conversation.ID+"/messages", map[string]string{"message": "Any news?"})
	if status != http.StatusConflict || !strings.Contains(body, agentchat.ErrBusy.Error()) {
		t.Fatalf("a message while busy: %d %s", status, body)
	}
}

// Ada stops a running turn: the task is cancelled, and the stream that
// follows it ends with agent.end cancelled.
func TestChat_cancel(t *testing.T) {
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	fake := agenttest.Anthropic(t, agenttest.Turn{Text: "Never sent.", Wait: gate})
	o := startChatOrg(t, fake)
	sent := converse(t, o.owner, "Take your time.")

	s := openStream(t, o.owner, sent.Task.ID, "", "")
	s.until(kindIs(t, "run_started"))
	var task agentruns.Task
	o.owner.must(http.StatusOK, http.MethodPost, "/api/v1/agent/tasks/"+sent.Task.ID+"/cancel", nil, &task)
	if task.Status != agentruns.StatusCancelled || task.ID != sent.Task.ID {
		t.Fatalf("cancelled %+v", task)
	}
	_, end := s.rest()
	if end.Status != agentruns.StatusCancelled {
		t.Fatalf("agent.end %+v", end)
	}
	status, body := send(o.owner, http.MethodPost, "/api/v1/agent/tasks/"+sent.Task.ID+"/cancel", nil)
	if status != http.StatusConflict || !strings.Contains(body, agentruns.ErrNotCancelable.Error()) {
		t.Fatalf("cancel again: %d %s", status, body)
	}
}

// loomctl's path: an API key as a bearer token starts, streams and reads
// a conversation as its maker.
func TestChat_apiKeyStream(t *testing.T) {
	fake := agenttest.Anthropic(t, agenttest.Turn{Text: "hello from the key"})
	o := startChatOrg(t, fake)
	var key keyBody
	o.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "loomctl", "access": "write"}, &key)
	ctl := &client{t: t, base: o.r.srv.URL, key: o.cfg.Identity.PublishableKey, token: key.Secret}

	sent := converse(t, ctl, "Say hello.")
	frames, end := openStream(t, ctl, sent.Task.ID, "", "").rest()
	if end.Status != agentruns.StatusCompleted {
		t.Fatalf("agent.end %+v", end)
	}
	checkSeqs(t, frames, 1)
	h := history(t, ctl, sent.Conversation.ID)
	if len(h.Turns) != 1 || h.Conversation.StartedBy != o.userID() {
		t.Fatalf("history %+v", h)
	}
	sameEvents(t, frames, h.Turns[0].Events)
	// It's Ada's conversation, whichever way she came in.
	if mine := history(t, o.owner, sent.Conversation.ID); len(mine.Turns) != 1 {
		t.Fatalf("Ada's history %+v", mine)
	}
}

// An admin turning the agent off ends an open stream with agent.end
// cancelled, and the next message is refused with ErrDisabled's words.
func TestChat_agentTurnedOffMidStream(t *testing.T) {
	fake := agenttest.Anthropic(t, agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "ask_person", Input: map[string]string{"question": "Which region?"}}}})
	o := startChatOrg(t, fake)
	sent := converse(t, o.owner, "Deploy to the right region.")

	s := openStream(t, o.owner, sent.Task.ID, "", "")
	s.until(kindIs(t, agentruns.EventRequest))
	o.owner.must(http.StatusOK, http.MethodPatch, "/api/v1/ai/settings/agent", map[string]any{"enabled": false, "revision": o.agent().Revision}, nil)
	_, end := s.rest()
	if end.Status != agentruns.StatusCancelled {
		t.Fatalf("agent.end %+v", end)
	}
	status, body := send(o.owner, http.MethodPost, "/api/v1/agent/conversations/"+sent.Conversation.ID+"/messages", map[string]string{"message": "Still there?"})
	if status != http.StatusConflict || !strings.Contains(body, agentsettings.ErrDisabled.Error()) {
		t.Fatalf("a message with the agent off: %d %s", status, body)
	}
	status, body = send(o.owner, http.MethodPost, "/api/v1/agent/conversations", map[string]string{"message": "Hello?"})
	if status != http.StatusConflict || !strings.Contains(body, agentsettings.ErrDisabled.Error()) {
		t.Fatalf("a new conversation with the agent off: %d %s", status, body)
	}
}
