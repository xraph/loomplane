package aiagent

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/agentchat"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/security"
)

// chatAPI is what the conversation routes ask of agentchat.
// *agentchat.Service is the real one.
type chatAPI interface {
	Start(ctx context.Context, p security.Principal, in agentchat.StartInput) (*agentchat.Sent, error)
	Send(ctx context.Context, p security.Principal, id string, in agentchat.SendInput) (*agentchat.Sent, error)
	List(ctx context.Context, p security.Principal, f agentchat.ListFilter) (*agentchat.ConversationList, error)
	History(ctx context.Context, p security.Principal, id string, q agentchat.HistoryQuery) (*agentchat.History, error)
}

// tasksAPI is what the task routes ask of the run driver.
// *agentruns.Service is the real one.
type tasksAPI interface {
	Get(ctx context.Context, p security.Principal, taskID string) (*agentruns.Task, error)
	Cancel(ctx context.Context, p security.Principal, taskID string) error
	Watch(ctx context.Context, p security.Principal, taskID string, after int64) (<-chan agentruns.Event, func(), error)
}

var (
	_ chatAPI  = (*agentchat.Service)(nil)
	_ tasksAPI = (*agentruns.Service)(nil)
)

// StreamEnd is a task stream's last frame: the task's final status, and
// its error when it failed.
type StreamEnd struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// streamKeepAlive is how long a task stream stays quiet before it sends a
// comment, so proxies keep an idle stream open. Tests shorten it.
var streamKeepAlive = 15 * time.Second

// streamRetry is how long, in milliseconds, a client waits before it
// reconnects to a dropped stream.
const streamRetry = 2000

// The task stream's frames.
const (
	frameEvent = "agent.event"
	frameEnd   = "agent.end"
)

// ConversationQuery narrows the conversations listed.
type ConversationQuery struct {
	ProjectID string `json:"project_id,omitempty" query:"project_id,omitempty"`
	// Everyone lists everyone's conversations in the workspace, which needs
	// ai:manage.
	Everyone bool `json:"everyone,omitempty" query:"everyone,omitempty"`
	// Before is the updated_at of the last conversation of the page before,
	// RFC 3339.
	Before string `json:"before,omitempty" query:"before,omitempty"`
	Limit  int    `json:"limit,omitempty" query:"limit,omitempty"`
}

// HistoryQuery picks a page of a conversation's turns.
type HistoryQuery struct {
	// Before is the task ID of the oldest turn of the page before.
	Before string `json:"before,omitempty" query:"before,omitempty"`
	Limit  int    `json:"limit,omitempty" query:"limit,omitempty"`
}

// StreamQuery says where a task stream starts.
type StreamQuery struct {
	// After is the last event seq the client has. The Last-Event-ID
	// header says the same, and the larger of the two wins.
	After int64 `json:"after,omitempty" query:"after,omitempty"`
}

// mountChatRoutes serves the conversations and the agent's tasks under
// /api/v1, behind guard and the scope binder, as the request routes are:
// they exist only with AI on, and answer only while the agent is on in
// the caller's workspace.
func mountChatRoutes(r forge.Router, guard identity.Guard, binder *agentscope.Binder, chat chatAPI, runs tasksAPI) error {
	if chat == nil || runs == nil || binder == nil {
		return errors.New("the agent's conversation routes need the conversations, the runs and the scope binder")
	}
	h := chatHandlers{chat: chat, runs: runs}
	mw := append(append([]forge.Middleware{}, guard...), binder.Middleware())
	g := r.Group("/api/v1", forge.WithGroupMiddleware(mw...), forge.WithGroupTags("Agent"))
	sent := func(desc string) forge.RouteOption {
		return forge.WithResponseSchema(http.StatusCreated, desc, agentchat.Sent{})
	}
	task := func(desc string) forge.RouteOption {
		return forge.WithResponseSchema(http.StatusOK, desc, agentruns.Task{})
	}
	errs := []error{
		g.GET("/agent/conversations", h.list,
			forge.WithName("agentConversations.list"),
			forge.WithSummary("Your conversations with the agent, the most recently updated first"),
			forge.WithDescription("With everyone=true, everyone's in the workspace, which needs ai:manage. "+
				"For the next page, pass the last conversation's updated_at as before."),
			forge.WithQuerySchema(ConversationQuery{}),
			forge.WithResponseSchema(http.StatusOK, "The conversations", agentchat.ConversationList{}),
		),
		g.POST("/agent/conversations", h.start,
			forge.WithName("agentConversations.start"),
			forge.WithSummary("Start a conversation with the agent"),
			forge.WithDescription("The message starts the conversation's first task. Follow it on GET /agent/tasks/{id}/stream."),
			forge.WithRequestSchema(agentchat.StartInput{}),
			sent("The conversation and its first task"),
		),
		g.GET("/agent/conversations/:id", h.history,
			forge.WithName("agentConversations.get"),
			forge.WithSummary("A page of a conversation's turns, each with its run's events"),
			forge.WithDescription("The person who started it and those who manage the workspace's AI may read it. "+
				"Stream a turn that's still running with after set to its event_seq."),
			forge.WithQuerySchema(HistoryQuery{}),
			forge.WithResponseSchema(http.StatusOK, "The conversation's turns, oldest first", agentchat.History{}),
		),
		g.POST("/agent/conversations/:id/messages", h.send,
			forge.WithName("agentConversations.send"),
			forge.WithSummary("Add a message to your conversation"),
			forge.WithDescription("Only the person who started the conversation adds to it, once the agent has finished the last message."),
			forge.WithRequestSchema(agentchat.SendInput{}),
			sent("The conversation and the task the message started"),
		),
		g.GET("/agent/tasks/:id", h.getTask,
			forge.WithName("agentTasks.get"),
			forge.WithSummary("One of the agent's tasks"),
			task("The task"),
		),
		g.POST("/agent/tasks/:id/cancel", h.cancel,
			forge.WithName("agentTasks.cancel"),
			forge.WithSummary("Stop one of the agent's tasks"),
			forge.WithDescription("The person who asked for it, or someone who manages the workspace's AI, may stop it."),
			task("The cancelled task"),
		),
		g.EventStream("/agent/tasks/:id/stream", h.stream,
			forge.WithName("agentTasks.stream"),
			forge.WithSummary("Follow a task's events as they happen"),
			forge.WithDescription("Each agent.event carries one event, with its seq as the SSE id. "+
				"Reconnect with Last-Event-ID (or after) set to the last seq you have, and the stream picks up at the next one. "+
				"agent.end is the last frame: the task's final status."),
			forge.WithQuerySchema(StreamQuery{}),
			forge.WithMiddleware(h.streamGate),
			forge.WithSSEMessages(map[string]any{frameEvent: agentruns.Event{}, frameEnd: StreamEnd{}}),
		),
	}
	return errors.Join(errs...)
}

type chatHandlers struct {
	chat chatAPI
	runs tasksAPI
}

func (h chatHandlers) list(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	f := agentchat.ListFilter{ProjectID: c.Query("project_id")}
	if raw := c.Query("everyone"); raw != "" {
		if f.Everyone, err = strconv.ParseBool(raw); err != nil {
			return forge.NewHTTPError(http.StatusUnprocessableEntity, "everyone is true or false")
		}
	}
	if raw := c.Query("before"); raw != "" {
		if f.Before, err = time.Parse(time.RFC3339Nano, raw); err != nil {
			return forge.NewHTTPError(http.StatusUnprocessableEntity, "before is a time, such as a conversation's updated_at")
		}
	}
	if f.Limit, err = limitOf(c, "limit is a number from 1 to 100"); err != nil {
		return err
	}
	list, err := h.chat.List(c.Request().Context(), p, f)
	if err != nil {
		return chatFail(err)
	}
	return c.JSON(http.StatusOK, list)
}

func (h chatHandlers) start(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in agentchat.StartInput
	if berr := c.BindJSON(&in); berr != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"message": "what you want to ask"}`)
	}
	sent, err := h.chat.Start(c.Request().Context(), p, in)
	if err != nil {
		return chatFail(err)
	}
	return c.JSON(http.StatusCreated, sent)
}

func (h chatHandlers) history(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	q := agentchat.HistoryQuery{Before: c.Query("before")}
	if q.Limit, err = limitOf(c, "limit is a number from 1 to 50"); err != nil {
		return err
	}
	hist, err := h.chat.History(c.Request().Context(), p, c.Param("id"), q)
	if err != nil {
		return chatFail(err)
	}
	return c.JSON(http.StatusOK, hist)
}

func (h chatHandlers) send(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in agentchat.SendInput
	if berr := c.BindJSON(&in); berr != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"message": "what you want to ask"}`)
	}
	sent, err := h.chat.Send(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return chatFail(err)
	}
	return c.JSON(http.StatusCreated, sent)
}

func (h chatHandlers) getTask(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	t, err := h.runs.Get(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return chatFail(err)
	}
	return c.JSON(http.StatusOK, t)
}

func (h chatHandlers) cancel(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err = h.runs.Cancel(ctx, p, c.Param("id")); err != nil {
		return chatFail(err)
	}
	t, err := h.runs.Get(ctx, p, c.Param("id"))
	if err != nil {
		return chatFail(err)
	}
	return c.JSON(http.StatusOK, t)
}

// streamGate answers a stream that can't start with a status, before the
// stream opens: once it has, the response is a 200 whatever happens. A
// resume position that isn't a seq is 422, and a task the caller may not
// see is 404.
func (h chatHandlers) streamGate(next forge.Handler) forge.Handler {
	return func(c forge.Context) error {
		p, err := principal(c)
		if err != nil {
			return err
		}
		if _, err = resumeAfter(c.Query("after"), lastEventID(c.Request())); err != nil {
			return err
		}
		if _, err = h.runs.Get(c.Request().Context(), p, c.Param("id")); err != nil {
			return chatFail(err)
		}
		return next(c)
	}
}

// stream sends the task's events from the client's position on, each with
// its seq as the SSE id, a keepalive comment whenever it's been quiet for
// streamKeepAlive, and agent.end with the final status once the task has
// finished and every event is out. A reconnect after the end gets only
// agent.end, so a client that missed it still closes.
func (h chatHandlers) stream(c forge.Context, s forge.Stream) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	after, err := resumeAfter(c.Query("after"), s.LastEventID())
	if err != nil {
		return err
	}
	if rerr := s.SetRetry(streamRetry); rerr != nil {
		return rerr
	}
	ctx, id, quiet := s.Context(), c.Param("id"), streamKeepAlive
	events, stop, err := h.runs.Watch(ctx, p, id, after)
	if err != nil {
		return err
	}
	defer stop()
	idle := time.NewTimer(quiet)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-idle.C:
			if serr := s.SendComment("keepalive"); serr != nil {
				return serr
			}
			idle.Reset(quiet)
		case ev, open := <-events:
			if !open {
				return h.end(ctx, s, p, id)
			}
			if serr := s.SendJSONWithID(strconv.FormatInt(ev.Seq, 10), frameEvent, ev); serr != nil {
				return serr
			}
			idle.Reset(quiet)
		}
	}
}

// end sends agent.end once the watch is over. A watch also ends when the
// client goes or the caller may no longer see the task; then the stream
// just closes, and a reconnect gets the status it's owed (a 404 for a task
// it can't see).
func (h chatHandlers) end(ctx context.Context, s forge.Stream, p security.Principal, id string) error {
	if ctx.Err() != nil {
		return nil
	}
	t, err := h.runs.Get(ctx, p, id)
	if err != nil || !agentruns.Terminal(t.Status) {
		return nil
	}
	return s.SendJSON(frameEnd, StreamEnd{Status: t.Status, Error: t.Error})
}

// lastEventID is the client's resume position as Forge reads it: the
// Last-Event-ID header, or the lastEventId query a browser's EventSource
// sends instead.
func lastEventID(r *http.Request) string {
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		return id
	}
	return r.URL.Query().Get("lastEventId")
}

// resumeAfter is where a stream picks up: the larger of the after query
// and the last event ID, each a seq (0 or more) when given.
func resumeAfter(query, lastID string) (int64, error) {
	var after int64
	for _, raw := range []string{query, lastID} {
		if raw == "" {
			continue
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			return 0, forge.NewHTTPError(http.StatusUnprocessableEntity, "after and Last-Event-ID are event seqs: whole numbers, 0 or more")
		}
		after = max(after, n)
	}
	return after, nil
}

// limitOf reads the limit query, 0 when it's absent.
func limitOf(c forge.Context, msg string) (int, error) {
	raw := c.Query("limit")
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, forge.NewHTTPError(http.StatusUnprocessableEntity, msg)
	}
	return n, nil
}

// chatFail maps the conversations' and the tasks' errors to a status: not
// found 404, invalid 422, someone else's conversation 403, and busy, the
// agent off, out of budget or a task already finished 409, each with its
// own message. The agent's other errors and the permission errors go
// through fail.
func chatFail(err error) error {
	for _, m := range []struct {
		target error
		status int
	}{
		{agentchat.ErrNotFound, http.StatusNotFound},
		{agentruns.ErrNotFound, http.StatusNotFound},
		{agentchat.ErrNotYours, http.StatusForbidden},
		{agentchat.ErrBusy, http.StatusConflict},
		{agentsettings.ErrDisabled, http.StatusConflict},
		{agentsettings.ErrBudgetExhausted, http.StatusConflict},
		{agentruns.ErrNotCancelable, http.StatusConflict},
	} {
		if errors.Is(err, m.target) {
			return forge.NewHTTPError(m.status, m.target.Error())
		}
	}
	if errors.Is(err, agentchat.ErrInvalid) || errors.Is(err, agentruns.ErrInvalid) {
		// The wrapped text says what's wrong with it.
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}
	return fail(err)
}
