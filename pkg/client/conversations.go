package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// --- Conversations with the built-in agent ---

// Conversation is one person's chat with the agent: the messages they sent
// and the answers it gave, each message a task.
type Conversation struct {
	ID string `json:"id"`
	// ProjectID is empty for a conversation about the whole workspace.
	ProjectID string `json:"project_id"`
	Title     string `json:"title"`
	StartedBy string `json:"started_by"`
	// Status is the latest message's task status: queued, running, waiting,
	// completed, failed or cancelled.
	Status     string    `json:"status"`
	LastTaskID string    `json:"last_task_id"`
	Turns      int       `json:"turns"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ConversationTurn is one message and the agent's answer to it, with the
// events its task wrote.
type ConversationTurn struct {
	TaskID  string       `json:"task_id"`
	Message string       `json:"message"`
	Status  string       `json:"status"`
	Error   string       `json:"error"`
	Events  []AgentEvent `json:"events"`
	// EventSeq is the last seq in Events. Stream a task that is still
	// running from there.
	EventSeq   int64      `json:"event_seq"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// ConversationHistory is a page of a conversation: its turns, oldest first.
type ConversationHistory struct {
	Conversation Conversation       `json:"conversation"`
	Turns        []ConversationTurn `json:"turns"`
	// More says older turns exist.
	More bool `json:"more"`
}

// AgentTask is one piece of work the agent does: the answer to a message,
// say.
type AgentTask struct {
	ID        string `json:"id"`
	OrgID     string `json:"org_id"`
	ProjectID string `json:"project_id"`
	Kind      string `json:"kind"`
	RunID     string `json:"run_id"`
	SessionID string `json:"session_id"`
	// Status is queued, running, waiting (on a person), completed, failed
	// or cancelled.
	Status      string            `json:"status"`
	RequestedBy string            `json:"requested_by"`
	MaxTokens   int               `json:"max_tokens"`
	Tokens      int               `json:"tokens"`
	EventSeq    int64             `json:"event_seq"`
	Error       string            `json:"error"`
	Metadata    map[string]string `json:"metadata"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	FinishedAt  *time.Time        `json:"finished_at"`
}

// Open reports whether the task can still change: it is queued, running or
// waiting on a person.
func (t AgentTask) Open() bool {
	switch t.Status {
	case "queued", "running", "waiting":
		return true
	}
	return false
}

// AgentEvent is one entry in a task's log. Kind is token (Data has text),
// step, tool_call and tool_result (Data has tool), request (Data has
// request_id and kind), safety_block, status, and a few more.
type AgentEvent struct {
	TaskID string          `json:"task_id"`
	Seq    int64           `json:"seq"`
	Kind   string          `json:"kind"`
	Data   json.RawMessage `json:"data"`
	At     time.Time       `json:"at"`
}

// StreamEnd is how a task ended: the status it finished with, and its error
// when it failed.
type StreamEnd struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// SentMessage is the conversation a message went to and the task that
// answers it.
type SentMessage struct {
	Conversation Conversation `json:"conversation"`
	Task         AgentTask    `json:"task"`
}

// StartConversation sends the first message of a new conversation. A
// projectID scopes it to that project; empty is the whole workspace.
func (c *Client) StartConversation(ctx context.Context, message, projectID string) (*SentMessage, error) {
	body := struct {
		Message   string `json:"message"`
		ProjectID string `json:"project_id,omitempty"`
	}{message, projectID}
	var out SentMessage
	if err := c.do(ctx, http.MethodPost, "/api/v1/agent/conversations", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendMessage adds a message to your conversation. The agent must have
// finished the last one, or it answers 409.
func (c *Client) SendMessage(ctx context.Context, conversationID, message string) (*SentMessage, error) {
	body := struct {
		Message string `json:"message"`
	}{message}
	var out SentMessage
	path := "/api/v1/agent/conversations/" + url.PathEscape(conversationID) + "/messages"
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Conversations lists your conversations with the agent, the most recently
// updated first. A projectID keeps those about that project.
func (c *Client) Conversations(ctx context.Context, projectID string) ([]Conversation, error) {
	path := "/api/v1/agent/conversations"
	if projectID != "" {
		path += "?" + url.Values{"project_id": {projectID}}.Encode()
	}
	var out struct {
		Conversations []Conversation `json:"conversations"`
	}
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out.Conversations, err
}

// Conversation returns the latest turns of one conversation.
func (c *Client) Conversation(ctx context.Context, id string) (*ConversationHistory, error) {
	var out ConversationHistory
	if err := c.do(ctx, http.MethodGet, "/api/v1/agent/conversations/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelAgentTask stops one of the agent's tasks.
func (c *Client) CancelAgentTask(ctx context.Context, id string) (*AgentTask, error) {
	var out AgentTask
	if err := c.do(ctx, http.MethodPost, "/api/v1/agent/tasks/"+url.PathEscape(id)+"/cancel", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// How StreamAgentTask reconnects. Tests shorten the backoff.
var (
	streamAttempts   = 6
	streamBackoff    = 500 * time.Millisecond
	streamBackoffMax = 8 * time.Second
)

// maxStreamLine bounds one line of a task stream.
const maxStreamLine = 1 << 20

// StreamAgentTask follows GET /agent/tasks/:id/stream until agent.end,
// calling fn per event in seq order, starting after the event numbered
// after (0 for all of them).
//
// It uses the client's transport without an overall timeout: a task can
// take as long as it takes. When the connection drops it reconnects with
// Last-Event-ID, at most 6 times in a row, waiting 500ms and doubling to
// 8s between tries, and skips the events it already has. An answer other
// than a 5xx, such as a 404 for a task you can't see, ends it at once.
func (c *Client) StreamAgentTask(ctx context.Context, taskID string, after int64, fn func(AgentEvent) error) (StreamEnd, error) {
	last := after
	failures := 0
	wait := streamBackoff
	for {
		end, got, err := c.streamOnce(ctx, taskID, &last, fn)
		if err == nil && end != nil {
			return *end, nil
		}
		var fnErr callbackError
		if errors.As(err, &fnErr) {
			return StreamEnd{}, fnErr.err
		}
		if ctx.Err() != nil {
			return StreamEnd{}, ctx.Err()
		}
		var apiErr *Error
		if errors.As(err, &apiErr) && apiErr.Status < 500 {
			return StreamEnd{}, err
		}
		if got {
			failures, wait = 0, streamBackoff
		}
		failures++
		if failures >= streamAttempts {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return StreamEnd{}, fmt.Errorf("lost the agent's stream after %d tries: %w", failures, err)
		}
		select {
		case <-ctx.Done():
			return StreamEnd{}, ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, streamBackoffMax)
	}
}

// callbackError marks an error fn returned, which ends the stream for good.
type callbackError struct{ err error }

func (e callbackError) Error() string { return e.err.Error() }

// streamOnce is one connection. It returns the end when agent.end came, and
// whether any frame arrived.
func (c *Client) streamOnce(ctx context.Context, taskID string, last *int64, fn func(AgentEvent) error) (end *StreamEnd, got bool, err error) {
	path := "/api/v1/agent/tasks/" + url.PathEscape(taskID) + "/stream"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, http.NoBody)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if *last > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(*last, 10))
	}
	hc := *c.http
	hc.Timeout = 0
	res, err := hc.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		e := &Error{Status: res.StatusCode}
		data, rerr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if rerr != nil {
			return nil, false, e
		}
		if json.Unmarshal(data, e) != nil {
			e = &Error{}
		}
		e.Status = res.StatusCode
		e.Message = message(data)
		return nil, false, e
	}
	var event string
	var data []string
	br := bufio.NewReader(res.Body)
	dispatch := func() (*StreamEnd, error) {
		name, payload := event, strings.Join(data, "\n")
		event, data = "", nil
		switch name {
		case "agent.event":
			var ev AgentEvent
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				return nil, fmt.Errorf("read an agent event: %w", err)
			}
			if ev.Seq <= *last {
				return nil, nil
			}
			got = true
			if err := fn(ev); err != nil {
				return nil, callbackError{err}
			}
			*last = ev.Seq
		case "agent.end":
			var e StreamEnd
			if err := json.Unmarshal([]byte(payload), &e); err != nil {
				return nil, fmt.Errorf("read the agent's last frame: %w", err)
			}
			return &e, nil
		}
		return nil, nil
	}
	for {
		line, rerr := readLine(br)
		if rerr != nil {
			return nil, got, rerr
		}
		switch {
		case line == "":
			if event == "" && len(data) == 0 {
				continue
			}
			e, derr := dispatch()
			if derr != nil || e != nil {
				return e, got, derr
			}
		case strings.HasPrefix(line, ":"):
			// A comment: the server's keepalive.
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				event = value
			case "data":
				data = append(data, value)
			}
		}
	}
}

// readLine reads one line of a stream without its line ending.
func readLine(br *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			return "", err
		}
		sb.Write(chunk)
		if sb.Len() > maxStreamLine {
			return "", errors.New("a line of the agent's stream was too long")
		}
		if !isPrefix {
			return sb.String(), nil
		}
	}
}
