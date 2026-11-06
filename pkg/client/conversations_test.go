package client

import (
	"context"
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
)

func TestConversationRoutes(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, fmt.Sprintf("%s %s %s auth=%s", r.Method, r.URL.RequestURI(), b, r.Header.Get("Authorization")))
		mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agent/conversations":
			_, _ = w.Write([]byte(`{"conversations":[{"id":"c1","project_id":"p1","title":"Hi","status":"completed","turns":2}]}`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"conversation":{"id":"c1"},"turns":[{"task_id":"t1","message":"hello","status":"completed","event_seq":2,"events":[{"task_id":"t1","seq":1,"kind":"token","data":{"text":"Hi"}}]}],"more":true}`))
		case r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(`{"id":"c1","title":"Checkout","pinned":true,"pinned_at":"2026-11-05T10:00:00Z"}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			_, _ = w.Write([]byte(`{"id":"t1","status":"cancelled"}`))
		default:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"conversation":{"id":"c1"},"task":{"id":"t1","status":"queued"}}`))
		}
	}))
	defer srv.Close()
	c, ctx := New(srv.URL, "lp_key"), context.Background()

	sent, err := c.StartConversation(ctx, "hello", "p1")
	if err != nil || sent.Conversation.ID != "c1" || sent.Task.ID != "t1" || !sent.Task.Open() {
		t.Fatalf("StartConversation = %+v, %v", sent, err)
	}
	if _, err = c.SendMessage(ctx, "c1", "more"); err != nil {
		t.Fatal(err)
	}
	list, err := c.Conversations(ctx, "p1")
	if err != nil || len(list) != 1 || list[0].Title != "Hi" || list[0].Turns != 2 {
		t.Fatalf("Conversations = %+v, %v", list, err)
	}
	if _, err = c.Conversations(ctx, ""); err != nil {
		t.Fatal(err)
	}
	h, err := c.Conversation(ctx, "c1")
	if err != nil || !h.More || len(h.Turns) != 1 || h.Turns[0].EventSeq != 2 || len(h.Turns[0].Events) != 1 || h.Turns[0].Events[0].Kind != "token" {
		t.Fatalf("Conversation = %+v, %v", h, err)
	}
	task, err := c.CancelAgentTask(ctx, "t1")
	if err != nil || task.Status != "cancelled" {
		t.Fatalf("CancelAgentTask = %+v, %v", task, err)
	}
	title, pin := "Checkout", true
	cv, err := c.UpdateConversation(ctx, "c1", ConversationUpdate{Title: &title, Pinned: &pin})
	if err != nil || cv.Title != "Checkout" || !cv.Pinned || cv.PinnedAt == nil {
		t.Fatalf("UpdateConversation = %+v, %v", cv, err)
	}
	if err = c.DeleteConversation(ctx, "c1"); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	want := []string{
		`POST /api/v1/agent/conversations {"message":"hello","project_id":"p1"} auth=Bearer lp_key`,
		`POST /api/v1/agent/conversations/c1/messages {"message":"more"} auth=Bearer lp_key`,
		`GET /api/v1/agent/conversations?project_id=p1  auth=Bearer lp_key`,
		`GET /api/v1/agent/conversations  auth=Bearer lp_key`,
		`GET /api/v1/agent/conversations/c1  auth=Bearer lp_key`,
		`POST /api/v1/agent/tasks/t1/cancel  auth=Bearer lp_key`,
		`PATCH /api/v1/agent/conversations/c1 {"title":"Checkout","pinned":true} auth=Bearer lp_key`,
		`DELETE /api/v1/agent/conversations/c1  auth=Bearer lp_key`,
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
}

func frame(seq int) string {
	data, _ := json.Marshal(AgentEvent{TaskID: "t1", Seq: int64(seq), Kind: "token", Data: json.RawMessage(`{"text":"x"}`)})
	return fmt.Sprintf("id: %d\nevent: agent.event\ndata: %s\n\n", seq, data)
}

func TestStreamAgentTaskReconnects(t *testing.T) {
	var mu sync.Mutex
	var ids []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ids = append(ids, r.Header.Get("Last-Event-ID"))
		n := len(ids)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer lp_key" || r.URL.Path != "/api/v1/agent/tasks/t1/stream" {
			t.Errorf("request: %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			// A replay of seq 1 would be skipped; here the server sends 1, 2 and drops.
			_, _ = io.WriteString(w, ": keepalive\n\n"+frame(1)+frame(2))
			return
		}
		_, _ = io.WriteString(w, frame(2)+frame(3)+frame(4)+"event: agent.end\ndata: {\"status\":\"completed\"}\n\n")
	}))
	defer srv.Close()
	old := streamBackoff
	streamBackoff = time.Millisecond
	defer func() { streamBackoff = old }()

	var seqs []int64
	end, err := New(srv.URL, "lp_key").StreamAgentTask(context.Background(), "t1", 0, func(e AgentEvent) error {
		seqs = append(seqs, e.Seq)
		return nil
	})
	if err != nil || end.Status != "completed" {
		t.Fatalf("end = %+v, %v", end, err)
	}
	if fmt.Sprint(seqs) != "[1 2 3 4]" {
		t.Fatalf("seqs = %v", seqs)
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprintf("%q", ids) != `["" "2"]` {
		t.Fatalf("Last-Event-ID headers = %q", ids)
	}
}

func TestStreamAgentTaskHasNoOverallTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for range 4 {
			_, _ = io.WriteString(w, ": keepalive\n\n")
			fl.Flush()
			time.Sleep(50 * time.Millisecond)
		}
		_, _ = io.WriteString(w, frame(1)+"event: agent.end\ndata: {\"status\":\"failed\",\"error\":\"boom\"}\n\n")
	}))
	defer srv.Close()
	c := New(srv.URL, "k").WithHTTPClient(&http.Client{Timeout: 50 * time.Millisecond})
	var n int
	end, err := c.StreamAgentTask(context.Background(), "t1", 0, func(AgentEvent) error { n++; return nil })
	if err != nil || n != 1 || end.Status != "failed" || end.Error != "boom" {
		t.Fatalf("end = %+v, n = %d, err = %v", end, n, err)
	}
	// The client's own timeout is still there for its other calls.
	if c.http.Timeout != 50*time.Millisecond {
		t.Fatalf("the stream changed the client's timeout to %v", c.http.Timeout)
	}
}

func TestStreamAgentTaskGivesUp(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	old := streamBackoff
	streamBackoff = time.Millisecond
	defer func() { streamBackoff = old }()
	_, err := New(srv.URL, "k").StreamAgentTask(context.Background(), "t1", 0, func(AgentEvent) error { return nil })
	if err == nil {
		t.Fatal("want an error")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 6 {
		t.Fatalf("tried %d times, want 6", calls)
	}
}

func TestStreamAgentTaskRefusedConnectionsGiveUp(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	old := streamBackoff
	streamBackoff = time.Millisecond
	defer func() { streamBackoff = old }()
	start := time.Now()
	_, err := New(url, "k").StreamAgentTask(context.Background(), "t1", 0, func(AgentEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "6 tries") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("backoff was not bounded: %v", time.Since(start))
	}
}

func TestStreamAgentTaskStopsOnRefusal(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"agent task not found"}`)
	}))
	defer srv.Close()
	_, err := New(srv.URL, "k").StreamAgentTask(context.Background(), "t1", 0, func(AgentEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "agent task not found") {
		t.Fatalf("err = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("a 404 was tried %d times", calls)
	}
}

func TestStreamAgentTaskCallbackErrorStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, frame(1)+frame(2))
	}))
	defer srv.Close()
	stop := errors.New("enough")
	_, err := New(srv.URL, "k").StreamAgentTask(context.Background(), "t1", 0, func(AgentEvent) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v", err)
	}
}
