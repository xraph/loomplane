package ctl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/pkg/client"
)

// fakeAgent is a Loomplane that serves the agent's conversation routes.
type fakeAgent struct {
	mu sync.Mutex
	// calls is every request, "METHOD path body".
	calls []string
	// streams is the SSE body each task's stream sends.
	streams map[string]string
	// hold keeps a task's stream open after its frames until the client
	// goes.
	hold    map[string]bool
	holding chan string
	// nextTask is the ID the next message's task gets.
	nextTask []string
	// conflict makes the message routes answer 409 with this message.
	conflict string
	// startStatus overrides the answer to POST /agent/conversations.
	startStatus int
	startBody   string
	requests    map[string]string
	history     string
}

func sse(seq int, kind, data string) string {
	return fmt.Sprintf("id: %d\nevent: agent.event\ndata: {\"task_id\":\"t\",\"seq\":%d,\"kind\":%q,\"data\":%s,\"at\":\"2026-11-03T10:00:00Z\"}\n\n", seq, seq, kind, data)
}

func end(status, errText string) string {
	b, _ := json.Marshal(client.StreamEnd{Status: status, Error: errText})
	return "event: agent.end\ndata: " + string(b) + "\n\n"
}

func (f *fakeAgent) called(sub string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI()+" "+string(b))
	f.mu.Unlock()
	path := r.URL.Path
	switch {
	case path == "/api/v1/projects":
		_, _ = w.Write([]byte(`{"projects":[{"id":"proj_api","name":"API","slug":"api"}]}`))
	case path == "/api/v1/agent/conversations" && r.Method == http.MethodPost, strings.HasSuffix(path, "/messages"):
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.startStatus != 0 && !strings.HasSuffix(path, "/messages") {
			w.WriteHeader(f.startStatus)
			_, _ = w.Write([]byte(f.startBody))
			return
		}
		if f.conflict != "" {
			w.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprintf(w, `{"error":%q}`, f.conflict)
			return
		}
		id := "t1"
		if len(f.nextTask) > 0 {
			id, f.nextTask = f.nextTask[0], f.nextTask[1:]
		}
		conv := "c1"
		if strings.HasSuffix(path, "/messages") {
			conv = strings.Split(path, "/")[5]
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"conversation":{"id":%q},"task":{"id":%q,"status":"queued"}}`, conv, id)
	case path == "/api/v1/agent/conversations":
		_, _ = w.Write([]byte(`{"conversations":[{"id":"c1","project_id":"proj_api","title":"Why did checkout fail?","status":"completed","turns":2,"updated_at":"` +
			time.Now().Add(-3*time.Hour).UTC().Format(time.RFC3339) + `"},{"id":"c2","project_id":"","title":"","status":"waiting","turns":1}]}`))
	case strings.HasPrefix(path, "/api/v1/agent/conversations/"):
		if f.history == "" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"conversation not found"}`))
			return
		}
		_, _ = w.Write([]byte(f.history))
	case strings.HasSuffix(path, "/stream"):
		id := strings.Split(path, "/")[5]
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, f.streams[id])
		w.(http.Flusher).Flush()
		if f.hold[id] {
			f.holding <- id
			<-r.Context().Done()
		}
	case strings.HasSuffix(path, "/cancel"):
		_, _ = w.Write([]byte(`{"id":"` + strings.Split(path, "/")[5] + `","status":"cancelled"}`))
	case strings.HasPrefix(path, "/api/v1/agent/requests/") && strings.HasSuffix(path, "/answer"):
		_, _ = w.Write([]byte(`{"id":"areq_1","status":"answered"}`))
	case strings.HasPrefix(path, "/api/v1/agent/requests/"):
		id := strings.TrimPrefix(path, "/api/v1/agent/requests/")
		body, ok := f.requests[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"agent request not found"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type chatRun struct {
	out, errOut *bytes.Buffer
	intr        chan struct{}
	err         error
}

// run runs loomctl against f with stdin.
func (f *fakeAgent) run(t *testing.T, stdin string, args ...string) *chatRun {
	t.Helper()
	r := f.start(t, stdin, args...)
	r.err = <-r.done
	return r.chatRun
}

type startedRun struct {
	*chatRun
	done chan error
}

func (f *fakeAgent) start(t *testing.T, stdin string, args ...string) startedRun {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	r := &chatRun{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, intr: make(chan struct{})}
	app := New(Options{
		Out: r.out, Err: r.errOut, In: strings.NewReader(stdin), Interrupt: r.intr,
		Getenv: func(k string) string {
			switch k {
			case EnvURL:
				return srv.URL
			case EnvKey:
				return "lp_test"
			}
			return ""
		},
		ConfigPath: t.TempDir() + "/none.json",
	})
	done := make(chan error, 1)
	go func() { done <- app.Run(args) }()
	return startedRun{r, done}
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	var e *ExitError
	if err == nil {
		return 0
	}
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an ExitError", err)
	}
	return e.Code
}

func TestAgentAskStreamsText(t *testing.T) {
	f := &fakeAgent{streams: map[string]string{"t1": sse(1, "step", `{}`) + sse(2, "token", `{"text":"Hel"}`) + sse(3, "token", `{"text":"lo"}`) +
		sse(4, "tool_call", `{"tool":"list_projects","call_id":"x","risk":"read"}`) +
		sse(5, "tool_result", `{"tool":"list_projects","call_id":"x","outcome":"ok","message":""}`) +
		sse(6, "tool_call", `{"tool":"set_variable","call_id":"y","risk":"mutate"}`) +
		sse(7, "tool_result", `{"tool":"set_variable","call_id":"y","outcome":"denied","message":"you may not"}`) +
		sse(8, "tool_call", `{"tool":"deploy","call_id":"z","risk":"mutate"}`) +
		sse(9, "tool_result", `{"tool":"deploy","call_id":"z","outcome":"failed","message":"no plan"}`) +
		end("completed", "")}}
	r := f.run(t, "", "agent", "ask", "what", "projects", "exist?")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.out.String() != "Hello\n" {
		t.Fatalf("stdout = %q", r.out.String())
	}
	for _, want := range []string{"· list_projects (read)\n  ok\n", "· set_variable (mutate)\n  refused: you may not\n", "· deploy (mutate)\n  failed: no plan\n"} {
		if !strings.Contains(r.errOut.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.errOut.String())
		}
	}
	if got := f.called("POST /api/v1/agent/conversations "); len(got) != 1 || !strings.Contains(got[0], `{"message":"what projects exist?"}`) {
		t.Fatalf("start = %q", got)
	}
}

func TestAgentAskStripsControlCharacters(t *testing.T) {
	f := &fakeAgent{streams: map[string]string{"t1": sse(1, "token", `{"text":"hi\u001b]8;;http://evil\u0007 there"}`) + end("completed", "")}}
	r := f.run(t, "", "agent", "ask", "x")
	if strings.ContainsAny(r.out.String(), "\x1b\x07") {
		t.Fatalf("stdout = %q", r.out.String())
	}
}

func TestAgentAskContinuesAConversation(t *testing.T) {
	f := &fakeAgent{streams: map[string]string{"t1": end("completed", "")}}
	r := f.run(t, "", "agent", "ask", "and now?", "--conversation", "c1")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.called("POST /api/v1/agent/conversations/c1/messages"); len(got) != 1 || !strings.Contains(got[0], `{"message":"and now?"}`) {
		t.Fatalf("calls = %q", f.calls)
	}
}

func TestAgentAskScopesToAProject(t *testing.T) {
	f := &fakeAgent{streams: map[string]string{"t1": end("completed", "")}}
	r := f.run(t, "", "agent", "ask", "status?", "--project", "api")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.called("POST /api/v1/agent/conversations "); len(got) != 1 || !strings.Contains(got[0], `"project_id":"proj_api"`) {
		t.Fatalf("calls = %q", f.calls)
	}
}

const answerRequest = `{"id":"areq_1","kind":"answer","status":"pending","question":"Which region should api use?","options":["eu-west-1","us-east-1"]}`

func TestAgentAskNoWaitExitsWhenWaiting(t *testing.T) {
	f := &fakeAgent{
		requests: map[string]string{"areq_1": answerRequest},
		streams:  map[string]string{"t1": sse(1, "token", `{"text":"Let me check."}`) + sse(2, "request", `{"request_id":"areq_1","kind":"answer"}`)},
		hold:     map[string]bool{"t1": true}, holding: make(chan string, 1),
	}
	r := f.run(t, "", "agent", "ask", "set up a cache", "--no-wait")
	if exitCode(t, r.err) != 3 {
		t.Fatalf("err = %v", r.err)
	}
	for _, want := range []string{"agent asks: Which region should api use?", "1) eu-west-1", "loomctl agent requests answer areq_1",
		"The agent is waiting for a person. Continue with: loomctl agent --conversation c1"} {
		if !strings.Contains(r.errOut.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.errOut.String())
		}
	}
	if r.out.String() != "Let me check.\n" {
		t.Fatalf("stdout = %q", r.out.String())
	}
}

func TestAgentAskRequestKinds(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"secret":  {`{"id":"areq_1","kind":"provide_secret","status":"pending","question":"The Stripe key?"}`, "loomctl agent requests provide areq_1"},
		"github":  {`{"id":"areq_1","kind":"connect_github","status":"pending","question":"Connect GitHub"}`, "/integrations"},
		"cloud":   {`{"id":"areq_1","kind":"create_connection","status":"pending","question":"Add AWS"}`, "/clouds"},
		"settled": {`{"id":"areq_1","kind":"answer","status":"answered","question":"Old question"}`, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeAgent{
				requests: map[string]string{"areq_1": tc.body},
				streams:  map[string]string{"t1": sse(1, "request", `{"request_id":"areq_1"}`) + end("completed", "")},
			}
			r := f.run(t, "", "agent", "ask", "go")
			if r.err != nil {
				t.Fatal(r.err)
			}
			if tc.want == "" {
				if r.errOut.Len() != 0 {
					t.Fatalf("stderr = %q", r.errOut.String())
				}
				return
			}
			if !strings.Contains(r.errOut.String(), tc.want) {
				t.Fatalf("stderr lacks %q:\n%s", tc.want, r.errOut.String())
			}
		})
	}
}

func TestAgentAskApprovePlanRequest(t *testing.T) {
	f := &fakeAgent{
		requests: map[string]string{"areq_1": `{"id":"areq_1","kind":"approve_plan","status":"pending","question":"Approve?","target":{"plan_id":"plan_9","plan_revision":"rev_4"}}`},
		streams:  map[string]string{"t1": sse(1, "request", `{"request_id":"areq_1"}`) + end("completed", "")},
	}
	r := f.run(t, "", "agent", "ask", "go")
	if r.err != nil || !strings.Contains(r.errOut.String(), "loomctl approve plan_9 --revision rev_4") {
		t.Fatalf("err = %v, stderr:\n%s", r.err, r.errOut.String())
	}
}

func TestAgentAskFailures(t *testing.T) {
	t.Run("failed", func(t *testing.T) {
		f := &fakeAgent{streams: map[string]string{"t1": sse(1, "token", `{"text":"Partial"}`) + end("failed", "boom")}}
		r := f.run(t, "", "agent", "ask", "x")
		if exitCode(t, r.err) != 1 || !strings.Contains(r.errOut.String(), "boom") {
			t.Fatalf("err = %v, stderr = %q", r.err, r.errOut.String())
		}
	})
	t.Run("safety stop", func(t *testing.T) {
		if safetyStopped != agentruns.SafetyStopped {
			t.Fatalf("safetyStopped = %q, the server says %q", safetyStopped, agentruns.SafetyStopped)
		}
		f := &fakeAgent{streams: map[string]string{"t1": sse(1, "step", `{}`) + sse(2, "token", `{"text":"unsafe"}`) +
			sse(3, "safety_block", `{"message":"`+agentruns.SafetyStopped+`","step_seq":1}`) + end("failed", agentruns.SafetyStopped)}}
		r := f.run(t, "", "agent", "ask", "x")
		if exitCode(t, r.err) != 1 {
			t.Fatalf("err = %v", r.err)
		}
		if n := strings.Count(r.errOut.String(), agentruns.SafetyStopped); n != 1 {
			t.Fatalf("the safety text was printed %d times:\n%s", n, r.errOut.String())
		}
	})
	t.Run("safety stop without the frame", func(t *testing.T) {
		f := &fakeAgent{streams: map[string]string{"t1": end("failed", agentruns.SafetyStopped)}}
		r := f.run(t, "", "agent", "ask", "x")
		if exitCode(t, r.err) != 1 || !strings.Contains(r.errOut.String(), agentruns.SafetyStopped) {
			t.Fatalf("err = %v, stderr = %q", r.err, r.errOut.String())
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		f := &fakeAgent{streams: map[string]string{"t1": end("cancelled", "")}}
		r := f.run(t, "", "agent", "ask", "x")
		if exitCode(t, r.err) != 1 || !strings.Contains(r.errOut.String(), "cancelled") {
			t.Fatalf("err = %v, stderr = %q", r.err, r.errOut.String())
		}
	})
	t.Run("busy", func(t *testing.T) {
		f := &fakeAgent{conflict: "The agent is still on your last message. Wait for it to finish, answer what it asked, or stop it."}
		r := f.run(t, "", "agent", "ask", "x", "--conversation", "c1")
		if exitCode(t, r.err) != 1 || !strings.Contains(r.errOut.String(), "still on your last message") {
			t.Fatalf("err = %v, stderr = %q", r.err, r.errOut.String())
		}
	})
}

func TestAgentAskJSON(t *testing.T) {
	f := &fakeAgent{streams: map[string]string{"t1": sse(1, "step", `{}`) + sse(2, "token", `{"text":"Hi"}`) +
		sse(3, "tool_call", `{"tool":"list_projects","risk":"read"}`) + end("completed", "")}}
	r := f.run(t, "", "agent", "ask", "x", "--json")
	if r.err != nil {
		t.Fatal(r.err)
	}
	lines := strings.Split(strings.TrimSpace(r.out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout:\n%s", r.out.String())
	}
	for i, l := range lines {
		var ev client.AgentEvent
		if err := json.Unmarshal([]byte(l), &ev); err != nil || ev.Seq != int64(i+1) {
			t.Fatalf("line %d = %q (%v)", i, l, err)
		}
	}
	if r.errOut.Len() != 0 {
		t.Fatalf("stderr = %q", r.errOut.String())
	}
}

func TestAgentAskInterruptCancels(t *testing.T) {
	f := &fakeAgent{
		streams: map[string]string{"t1": sse(1, "token", `{"text":"Thinking"}`)},
		hold:    map[string]bool{"t1": true}, holding: make(chan string, 1),
	}
	r := f.start(t, "", "agent", "ask", "x")
	select {
	case <-f.holding:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream never opened")
	}
	close(r.intr)
	var err error
	select {
	case err = <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("ask didn't return")
	}
	if exitCode(t, err) != 130 {
		t.Fatalf("err = %v", err)
	}
	if got := f.called("POST /api/v1/agent/tasks/t1/cancel"); len(got) != 1 {
		t.Fatalf("calls = %q", f.calls)
	}
}

func TestAgentAskNeedsAPrompt(t *testing.T) {
	r := (&fakeAgent{}).run(t, "", "agent", "ask")
	if r.err == nil || !strings.Contains(r.err.Error(), "say what to ask") {
		t.Fatalf("err = %v", r.err)
	}
}

func TestAgentInteractiveAnswersInline(t *testing.T) {
	f := &fakeAgent{
		requests: map[string]string{"areq_1": answerRequest},
		streams: map[string]string{"t1": sse(1, "token", `{"text":"One moment."}`) + sse(2, "request", `{"request_id":"areq_1","kind":"answer"}`) +
			sse(3, "token", `{"text":"Done."}`) + end("completed", "")},
	}
	r := f.run(t, "Which region should api use?\neu-west-1\n/quit\n", "agent")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.called("POST /api/v1/agent/conversations "); len(got) != 1 || !strings.Contains(got[0], "Which region should api use?") {
		t.Fatalf("start = %q", got)
	}
	if got := f.called("POST /api/v1/agent/requests/areq_1/answer"); len(got) != 1 || !strings.Contains(got[0], `{"text":"eu-west-1"}`) {
		t.Fatalf("answer = %q", f.calls)
	}
	if r.out.String() != "One moment.\nDone.\n" {
		t.Fatalf("stdout = %q", r.out.String())
	}
	if !strings.Contains(r.errOut.String(), "agent asks: Which region should api use?") {
		t.Fatalf("stderr = %q", r.errOut.String())
	}
}

func TestAgentInteractiveAnswersByNumber(t *testing.T) {
	f := &fakeAgent{
		requests: map[string]string{"areq_1": answerRequest},
		streams:  map[string]string{"t1": sse(1, "request", `{"request_id":"areq_1"}`) + end("completed", "")},
	}
	r := f.run(t, "go\n2\n/quit\n", "agent")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.called("/answer"); len(got) != 1 || !strings.Contains(got[0], `{"text":"us-east-1"}`) {
		t.Fatalf("answer = %q", f.calls)
	}
}

func TestAgentInteractiveNewAndStop(t *testing.T) {
	f := &fakeAgent{
		nextTask: []string{"t1", "t2", "t3"},
		requests: map[string]string{"areq_1": answerRequest},
		streams: map[string]string{
			"t1": sse(1, "token", `{"text":"First."}`) + end("completed", ""),
			// An empty line leaves the question pending and the task open.
			"t2": sse(1, "request", `{"request_id":"areq_1"}`),
			"t3": sse(1, "token", `{"text":"Third."}`) + end("completed", ""),
		},
	}
	r := f.run(t, "one\ntwo\n\n/stop\n/new\nthree\n/quit\n", "agent")
	if r.err != nil {
		t.Fatal(r.err)
	}
	starts := f.called("POST /api/v1/agent/conversations ")
	sends := f.called("/messages")
	if len(starts) != 2 || len(sends) != 1 || !strings.Contains(sends[0], `"two"`) || !strings.Contains(starts[1], `"three"`) {
		t.Fatalf("calls = %q", f.calls)
	}
	if got := f.called("POST /api/v1/agent/tasks/t2/cancel"); len(got) != 1 {
		t.Fatalf("calls = %q", f.calls)
	}
	if r.out.String() != "First.\nThird.\n" {
		t.Fatalf("stdout = %q", r.out.String())
	}
}

func TestAgentInteractiveKeepsGoingAfterARefusal(t *testing.T) {
	f := &fakeAgent{conflict: "The agent is still on your last message.", streams: map[string]string{}}
	r := f.run(t, "hello\n/quit\n", "agent")
	if r.err != nil || !strings.Contains(r.errOut.String(), "still on your last message") {
		t.Fatalf("err = %v, stderr = %q", r.err, r.errOut.String())
	}
}

func TestAgentInteractiveResumesAWaitingConversation(t *testing.T) {
	f := &fakeAgent{
		history:  `{"conversation":{"id":"c9"},"turns":[{"task_id":"t7","message":"set up a cache","status":"waiting","event_seq":2}],"more":false}`,
		requests: map[string]string{"areq_1": answerRequest},
		streams:  map[string]string{"t7": sse(1, "request", `{"request_id":"areq_1"}`) + sse(2, "token", `{"text":"Back."}`) + end("completed", "")},
	}
	r := f.run(t, "eu-west-1\n/quit\n", "agent", "--conversation", "c9")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.called("/answer"); len(got) != 1 {
		t.Fatalf("calls = %q", f.calls)
	}
	if r.out.String() != "Back.\n" {
		t.Fatalf("stdout = %q", r.out.String())
	}
}

func TestAgentInteractiveInterruptAtThePromptExits(t *testing.T) {
	f := &fakeAgent{}
	r := f.start(t, "", "agent")
	close(r.intr)
	select {
	case err := <-r.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the chat didn't exit")
	}
}

func TestAgentConversationsList(t *testing.T) {
	f := &fakeAgent{}
	r := f.run(t, "", "agent", "conversations")
	if r.err != nil {
		t.Fatal(r.err)
	}
	out := r.out.String()
	for _, want := range []string{"ID", "PROJECT", "TITLE", "STATUS", "UPDATED", "c1", "api", "Why did checkout fail?", "completed", "3h", "c2", "waiting"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	r = f.run(t, "", "agent", "conversations", "--json", "--project", "api")
	if r.err != nil {
		t.Fatal(r.err)
	}
	var list []client.Conversation
	if err := json.Unmarshal(r.out.Bytes(), &list); err != nil || len(list) != 2 {
		t.Fatalf("json = %q (%v)", r.out.String(), err)
	}
	if got := f.called("GET /api/v1/agent/conversations?project_id=proj_api"); len(got) != 1 {
		t.Fatalf("calls = %q", f.calls)
	}
}

func TestAgentChatOffMessage(t *testing.T) {
	f := &fakeAgent{startStatus: http.StatusNotFound, startBody: `{"error":"not found"}`}
	r := f.run(t, "", "agent", "ask", "hello")
	if r.err == nil || r.err.Error() != agentOff {
		t.Fatalf("err = %v", r.err)
	}
	r = f.run(t, "", "agent", "conversations")
	if r.err != nil {
		// The fake lists conversations; the off answer is for the start route.
		t.Fatal(r.err)
	}
}

func TestAgentErrorNamesWhatIsMissing(t *testing.T) {
	for msg, want := range map[string]string{
		"conversation not found":    "conversation not found",
		"agent task not found":      "agent task not found",
		"agent request not found":   "There's no agent request with that ID.",
		"not found":                 agentOff,
		"no such route: GET /agent": agentOff,
	} {
		got := agentError(&client.Error{Status: 404, Message: msg})
		if got.Error() != want {
			t.Errorf("agentError(404 %q) = %q, want %q", msg, got, want)
		}
	}
	if got := agentError(&client.Error{Status: 500, Message: "conversation not found"}); got.Error() != "conversation not found" {
		t.Errorf("a 500 changed: %v", got)
	}
}

func TestAgentContinueUnknownConversation(t *testing.T) {
	f := &fakeAgent{}
	f.startStatus = 0
	r := f.run(t, "", "agent", "--conversation", "nope")
	if r.err == nil || r.err.Error() != "conversation not found" {
		t.Fatalf("err = %v", r.err)
	}
}
