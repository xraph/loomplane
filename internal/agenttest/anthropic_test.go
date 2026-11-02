package agenttest_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/nexus/provider"
	"github.com/xraph/nexus/providers/anthropic"

	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/config"
)

// client is nexus's own Anthropic provider pointed at the fake, so the
// answers are read the way the agent reads them.
func client(f *agenttest.FakeAnthropic) *anthropic.Provider {
	return anthropic.New(agenttest.APIKey, anthropic.WithBaseURL(f.URL()),
		anthropic.WithPromptCaching(anthropic.CacheConfig{TTL: anthropic.CacheTTL5m, Messages: true}))
}

func request() *provider.CompletionRequest {
	return &provider.CompletionRequest{
		Model: config.DefaultAIModel, System: "You are Loomplane's agent.", MaxTokens: 1024,
		Messages: []provider.Message{{Role: "user", Content: "Why did the deploy fail?"}},
		Tools: []provider.Tool{
			{Type: "function", Function: provider.ToolFunction{Name: "get_deployment", Parameters: map[string]any{"type": "object"}}},
			{Type: "function", Function: provider.ToolFunction{Name: "list_projects", Parameters: map[string]any{"type": "object"}}},
		},
	}
}

func TestFakeAnthropicServesTurnsInOrder(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			f := agenttest.Anthropic(t,
				agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "get_deployment", Input: map[string]string{"deployment_id": "dep_1"}}}, InputTokens: 120, OutputTokens: 30},
				agenttest.Turn{Text: "The build ran out of memory."},
			)
			p := client(f)
			ctx := context.Background()

			first := complete(ctx, t, p, request(), stream)
			if len(first.calls) != 1 || first.calls[0].Function.Name != "get_deployment" ||
				!strings.Contains(first.calls[0].Function.Arguments, `"deployment_id":"dep_1"`) || first.calls[0].ID == "" {
				t.Fatalf("first turn's tool calls %+v", first.calls)
			}
			if first.finish != "tool_calls" || first.usage.PromptTokens != 120 || first.usage.CompletionTokens != 30 {
				t.Fatalf("first turn: finish %q, usage %+v", first.finish, first.usage)
			}
			second := complete(ctx, t, p, request(), stream)
			if second.text != "The build ran out of memory." || len(second.calls) != 0 || second.finish != "stop" {
				t.Fatalf("second turn: %+v", second)
			}
			if second.usage.PromptTokens != 100 || second.usage.CompletionTokens != 50 {
				t.Fatalf("default usage %+v", second.usage)
			}

			reqs := f.Requests()
			if len(reqs) != 2 {
				t.Fatalf("%d requests", len(reqs))
			}
			for _, r := range reqs {
				if r.Model != config.DefaultAIModel || r.System != "You are Loomplane's agent." || len(r.Messages) != 1 ||
					strings.Join(r.Tools, ",") != "get_deployment,list_projects" {
					t.Fatalf("captured %+v", r)
				}
			}
		})
	}
}

type answer struct {
	text   string
	calls  []provider.ToolCall
	finish string
	usage  provider.Usage
}

func complete(ctx context.Context, t *testing.T, p *anthropic.Provider, req *provider.CompletionRequest, stream bool) answer {
	t.Helper()
	if !stream {
		resp, err := p.Complete(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		msg := resp.Choices[0].Message
		text, _ := msg.Content.(string)
		return answer{text: text, calls: msg.ToolCalls, finish: resp.Choices[0].FinishReason, usage: resp.Usage}
	}
	req.Stream = true
	s, err := p.CompleteStream(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var a answer
	calls := map[string]*provider.ToolCall{}
	var order []string
	for {
		chunk, nerr := s.Next(ctx)
		if errors.Is(nerr, io.EOF) {
			break
		}
		if nerr != nil {
			t.Fatal(nerr)
		}
		a.text += chunk.Delta.Content
		for _, c := range chunk.Delta.ToolCalls {
			got, ok := calls[c.ID]
			if !ok {
				got = &provider.ToolCall{ID: c.ID, Type: c.Type, Function: provider.ToolCallFunc{Name: c.Function.Name}}
				calls[c.ID] = got
				order = append(order, c.ID)
			}
			got.Function.Arguments += c.Function.Arguments
		}
		if chunk.FinishReason != "" {
			a.finish = chunk.FinishReason
		}
	}
	for _, id := range order {
		a.calls = append(a.calls, *calls[id])
	}
	if u := s.Usage(); u != nil {
		a.usage = *u
	}
	return a
}

// recordingTB is a testing.TB that keeps what would fail the test.
type recordingTB struct {
	testing.TB
	mu     sync.Mutex
	errors []string
}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recordingTB) failures() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.errors...)
}

func TestFakeAnthropicFailsPastTheScript(t *testing.T) {
	tb := &recordingTB{TB: t}
	f := agenttest.Anthropic(tb, agenttest.Turn{Text: "one"})
	p := client(f)
	ctx := context.Background()
	if _, err := p.Complete(ctx, request()); err != nil {
		t.Fatal(err)
	}
	if len(tb.failures()) != 0 {
		t.Fatalf("the scripted turn failed the test: %v", tb.failures())
	}
	if _, err := p.Complete(ctx, request()); err == nil {
		t.Fatal("a request past the script was answered")
	}
	got := tb.failures()
	if len(got) != 1 || !strings.Contains(got[0], "after the script") {
		t.Fatalf("failures %v", got)
	}
}
