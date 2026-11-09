// Package agenttest is the AI agent's test kit: a fake Anthropic Messages
// API that serves scripted turns, and a fresh, migrated database. Nothing
// in it reaches a real model or a network outside the test.
package agenttest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

// Default token counts a turn reports when it sets none.
const (
	defaultInputTokens  = 100
	defaultOutputTokens = 50
)

// APIKey is the server key AIConfig sets.
const APIKey = "test"

// ToolUse is one tool call a scripted turn makes.
type ToolUse struct {
	Name string
	// Input is the call's arguments. One that is a Pending holds the turn
	// until it is ready.
	Input any
}

// Pending is a tool input a test fills in once it knows it, such as an ID
// the run creates. The fake serves a turn only once each of its Pending
// inputs is ready, so a run that asks early waits for the input instead of
// getting a null one.
type Pending interface {
	Ready() <-chan struct{}
}

// Turn is one scripted answer: text, tool calls or both, and the usage it
// reports.
type Turn struct {
	Text         string
	ToolUses     []ToolUse
	InputTokens  int // default 100
	OutputTokens int // default 50
	// Deltas, when set, is the text in pieces: a stream sends one
	// content_block_delta per piece, and Text is ignored.
	Deltas []string
	// DeltaGap is the pause between two deltas of a stream.
	DeltaGap time.Duration
	// Wait, when set, holds the turn's response until the channel closes
	// (or the request ends).
	Wait <-chan struct{}
}

// Captured is what one request to the fake asked for.
type Captured struct {
	Model    string
	System   string
	Messages []json.RawMessage
	Tools    []string
}

// FakeAnthropic is an httptest server speaking POST /v1/messages, plain
// and SSE, answering with its turns in order.
type FakeAnthropic struct {
	t     testing.TB
	srv   *httptest.Server
	turns []Turn

	mu       sync.Mutex
	requests []Captured
}

// Anthropic starts a fake serving turns in order. A request after the last
// turn fails the test and is answered with an API error. The server stops
// when the test ends.
func Anthropic(t testing.TB, turns ...Turn) *FakeAnthropic {
	t.Helper()
	f := &FakeAnthropic{t: t, turns: turns}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// URL is the fake's base URL, for ai.base_url.
func (f *FakeAnthropic) URL() string { return f.srv.URL }

// Requests are the requests served so far, in order.
func (f *FakeAnthropic) Requests() []Captured {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Captured, len(f.requests))
	copy(out, f.requests)
	return out
}

// AIConfig is the ai: block of a server whose agent runs on the fake: the
// defaults, on, with the fake as the anthropic base URL and key "test".
func (f *FakeAnthropic) AIConfig() config.AIConfig {
	ai := config.Defaults().AI
	ai.Enabled, ai.Provider, ai.Model, ai.APIKey, ai.BaseURL = true, config.AIProviderAnthropic, config.DefaultAIModel, APIKey, f.URL()
	return ai
}

// request is the part of a Messages API request the fake reads.
type request struct {
	Model    string            `json:"model"`
	System   json.RawMessage   `json:"system"`
	Messages []json.RawMessage `json:"messages"`
	Tools    []struct {
		Name string `json:"name"`
	} `json:"tools"`
	Stream bool `json:"stream"`
}

// systemText is the system prompt as one string: Anthropic takes a string
// or a list of text blocks.
func systemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n\n")
}

func (f *FakeAnthropic) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
		f.t.Errorf("agenttest: unexpected %s %s", r.Method, r.URL.Path)
		apiError(w, http.StatusNotFound, "not_found_error", "no such route")
		return
	}
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("agenttest: decode a Messages request: %v", err)
		apiError(w, http.StatusBadRequest, "invalid_request_error", "not a Messages request")
		return
	}
	c := Captured{Model: req.Model, System: systemText(req.System), Messages: req.Messages}
	for _, tool := range req.Tools {
		c.Tools = append(c.Tools, tool.Name)
	}
	f.mu.Lock()
	n := len(f.requests)
	f.requests = append(f.requests, c)
	f.mu.Unlock()
	if n >= len(f.turns) {
		f.t.Errorf("agenttest: request %d came after the script's %d turns", n+1, len(f.turns))
		apiError(w, http.StatusInternalServerError, "api_error", "the script has no more turns")
		return
	}
	turn := f.turns[n]
	if turn.Wait != nil {
		select {
		case <-turn.Wait:
		case <-r.Context().Done():
			return
		}
	}
	for _, use := range turn.ToolUses {
		if p, ok := use.Input.(Pending); ok {
			select {
			case <-p.Ready():
			case <-r.Context().Done():
				return
			}
		}
	}
	blocks, err := turn.blocks(n)
	if err != nil {
		f.t.Errorf("agenttest: turn %d: %v", n+1, err)
		apiError(w, http.StatusInternalServerError, "api_error", "the turn can't be served")
		return
	}
	msgID := fmt.Sprintf("msg_agenttest_%d", n+1)
	if req.Stream {
		f.stream(w, msgID, req.Model, turn, blocks)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // the client sees a short body
		"id": msgID, "type": "message", "role": "assistant", "model": req.Model,
		"content": blocks, "stop_reason": turn.stopReason(), "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": turn.input(), "output_tokens": turn.output()},
	})
}

// block is one content block of an answer.
type block struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// text is the turn's whole text.
func (t Turn) text() string {
	if t.Deltas != nil {
		return strings.Join(t.Deltas, "")
	}
	return t.Text
}

func (t Turn) blocks(n int) ([]block, error) {
	var out []block
	if text := t.text(); text != "" || len(t.ToolUses) == 0 {
		out = append(out, block{Type: "text", Text: text})
	}
	for i, u := range t.ToolUses {
		input := json.RawMessage(`{}`)
		if u.Input != nil {
			raw, err := json.Marshal(u.Input)
			if err != nil {
				return nil, fmt.Errorf("tool %s input: %w", u.Name, err)
			}
			input = raw
		}
		out = append(out, block{Type: "tool_use", ID: fmt.Sprintf("toolu_agenttest_%d_%d", n+1, i+1), Name: u.Name, Input: input})
	}
	return out, nil
}

func (t Turn) stopReason() string {
	if len(t.ToolUses) > 0 {
		return "tool_use"
	}
	return "end_turn"
}

func (t Turn) input() int {
	if t.InputTokens > 0 {
		return t.InputTokens
	}
	return defaultInputTokens
}

func (t Turn) output() int {
	if t.OutputTokens > 0 {
		return t.OutputTokens
	}
	return defaultOutputTokens
}

// stream answers as the Messages API streams: message_start, each block
// started, sent as one delta (or the turn's Deltas, each flushed) and
// stopped, message_delta with the stop reason and usage, then
// message_stop.
func (f *FakeAnthropic) stream(w http.ResponseWriter, msgID, model string, turn Turn, blocks []block) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	send := func(event string, data any) {
		raw, err := json.Marshal(data)
		if err != nil {
			f.t.Errorf("agenttest: encode %s: %v", event, err)
			return
		}
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
	}
	flush := func() {
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}
	send("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": msgID, "type": "message", "role": "assistant", "model": model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": turn.input(), "output_tokens": 1},
	}})
	for i, b := range blocks {
		switch b.Type {
		case "text":
			send("content_block_start", map[string]any{"type": "content_block_start", "index": i,
				"content_block": map[string]any{"type": "text", "text": ""}})
			pieces := []string{b.Text}
			if turn.Deltas != nil {
				pieces = turn.Deltas
			}
			for k, piece := range pieces {
				if k > 0 && turn.DeltaGap > 0 {
					time.Sleep(turn.DeltaGap)
				}
				send("content_block_delta", map[string]any{"type": "content_block_delta", "index": i,
					"delta": map[string]any{"type": "text_delta", "text": piece}})
				flush()
			}
		case "tool_use":
			send("content_block_start", map[string]any{"type": "content_block_start", "index": i,
				"content_block": map[string]any{"type": "tool_use", "id": b.ID, "name": b.Name, "input": map[string]any{}}})
			send("content_block_delta", map[string]any{"type": "content_block_delta", "index": i,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": string(b.Input)}})
		}
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	send("message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": turn.stopReason(), "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": turn.output()}})
	send("message_stop", map[string]any{"type": "message_stop"})
	flush()
}

// apiError answers as the Messages API does when it refuses.
func apiError(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // the client sees a short body
		"type": "error", "error": map[string]string{"type": kind, "message": message},
	})
}
