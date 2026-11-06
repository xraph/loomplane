// Package agentchat keeps each person's conversations with the workspace's
// AI agent.
//
// A conversation is no table of its own. It's the chat tasks of one
// workspace that share a session ID (agentruns' loomplane_agent_tasks rows
// with kind 'chat'): the first gives its starter, project, title and start,
// the latest its status, last task and last update. Only the person who
// started it adds to it, one turn at a time; people who manage the
// workspace's AI may read it.
//
// What the starter changes about a conversation lives beside its tasks, in
// loomplane_agent_conversations: the title they gave it, when they pinned
// it, and when they deleted it. A conversation with no row there has none
// of those.
package agentchat

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/security"
)

const (
	// MaxMessage is the most characters one message may have.
	MaxMessage = 16000
	// TitleLen is the most runes of a conversation's title, cut at the
	// last space before it.
	TitleLen = 80
	// MaxTitle is the most characters of a title the person gives.
	MaxTitle = 120
)

// Conversation is one person's thread of chat tasks.
type Conversation struct {
	ID        string `json:"id" forge:"id"` // the cortex session ID
	OrgID     string `json:"org_id"`
	ProjectID string `json:"project_id"` // "" for the whole workspace
	// Title is the one the person gave it, or else the start of its first
	// message.
	Title      string     `json:"title"`
	StartedBy  string     `json:"started_by"`
	Status     string     `json:"status"` // the latest turn's task status
	LastTaskID string     `json:"last_task_id"`
	Turns      int        `json:"turns"`
	Pinned     bool       `json:"pinned"`
	PinnedAt   *time.Time `json:"pinned_at"` // nil when it isn't pinned
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Turn is one message and the agent's run on it.
type Turn struct {
	TaskID     string            `json:"task_id"`
	Message    string            `json:"message"`
	Status     string            `json:"status"`
	Error      string            `json:"error"`
	Events     []agentruns.Event `json:"events"`
	EventSeq   int64             `json:"event_seq"` // the last seq in Events; stream with after=EventSeq
	CreatedAt  time.Time         `json:"created_at"`
	FinishedAt *time.Time        `json:"finished_at"`
	// Context is where in the dashboard the person sent the message from,
	// when the dashboard said.
	Context *PageContext `json:"context,omitempty"`
}

// History is a page of a conversation's turns.
type History struct {
	Conversation Conversation `json:"conversation"`
	Turns        []Turn       `json:"turns"` // oldest first
	More         bool         `json:"more"`  // older turns exist: ask with before=<Turns[0].TaskID>
}

// StartInput is a conversation's first message.
type StartInput struct {
	Message   string       `json:"message"`
	ProjectID string       `json:"project_id,omitempty"`
	Context   *PageContext `json:"context,omitempty"`
}

// SendInput is a conversation's next message.
type SendInput struct {
	Message string       `json:"message"`
	Context *PageContext `json:"context,omitempty"`
}

// ConversationUpdate changes a conversation. A field left out stays as it is.
type ConversationUpdate struct {
	// Title replaces the conversation's title: 1 to MaxTitle characters on
	// one line. No automatic title replaces it afterwards.
	Title *string `json:"title,omitempty"`
	// Pinned pins the conversation (pinned_at is when) or unpins it.
	Pinned *bool `json:"pinned,omitempty"`
}

// Sent is the conversation a message went to and the task it started.
type Sent struct {
	Conversation Conversation   `json:"conversation"`
	Task         agentruns.Task `json:"task"`
}

// ConversationList is a page of conversations, the most recently updated
// first.
type ConversationList struct {
	Conversations []Conversation `json:"conversations"`
}

// ListFilter narrows List.
type ListFilter struct {
	ProjectID string
	Everyone  bool      // ai:manage only
	Before    time.Time // UpdatedAt cursor
	Limit     int       // 1..100, default 30
}

// HistoryQuery picks a page of History.
type HistoryQuery struct {
	Before string // a task ID
	Limit  int    // turns, 1..50, default 20
}

var (
	ErrNotFound = errors.New("conversation not found")
	ErrInvalid  = errors.New("invalid message")
	ErrBusy     = errors.New("The agent is still on your last message. Wait for it to finish, answer what it asked, or stop it.") //nolint:staticcheck,revive // ST1005: a sentence people read as is
	ErrNotYours = errors.New("only the person who started this conversation can add to it")
	// ErrNotYoursToChange is someone who may read a conversation but didn't
	// start it, renaming, pinning or deleting it.
	ErrNotYoursToChange = errors.New("only the person who started this conversation can rename, pin or delete it")
	// ErrBadChange wraps what's wrong with a ConversationUpdate.
	ErrBadChange = errors.New("invalid change")
	// ErrSending is a delete that found a message on its way into the
	// conversation.
	ErrSending = errors.New("A message is on its way into this conversation. Try again in a moment.") //nolint:staticcheck,revive // ST1005: a sentence people read as is
)

// TurnRow is one chat task as the store reads it.
type TurnRow struct {
	TaskID, Input, Status, Error string
	EventSeq                     int64
	CreatedAt                    time.Time
	FinishedAt                   *time.Time
	// Context is the turn's page context as stored, or nil.
	Context []byte
}

// Change is what Store.Update writes. A nil field stays as it is.
type Change struct {
	Title  *string
	Pinned *bool
	At     time.Time
}

// Store reads conversations from the agent's task table. Every method
// reads one workspace's chat tasks only.
type Store interface {
	// Conversation returns ErrNotFound when orgID has no chat task in the
	// session.
	Conversation(ctx context.Context, orgID, id string) (*Conversation, error)
	// List reads the conversations startedBy started, or everyone's when
	// startedBy is "", the most recently updated first.
	List(ctx context.Context, orgID, startedBy string, f ListFilter) ([]Conversation, error)
	// Turns reads up to limit turns, newest first, from before the task
	// before ("" for the newest), and reports whether older ones exist.
	Turns(ctx context.Context, orgID, id, before string, limit int) ([]TurnRow, bool, error)
	// Update writes the person's title or pin. A deleted conversation
	// stays as it is.
	Update(ctx context.Context, orgID, id string, c Change) error
	// Delete marks the conversation deleted at at. Its tasks stay.
	// Conversation, List and Turns never read a deleted one again.
	Delete(ctx context.Context, orgID, id string, at time.Time) error
}

// Runs is what the service asks of agentruns. *agentruns.Service is the
// real one.
type Runs interface {
	Start(ctx context.Context, in agentruns.StartInput) (*agentruns.Task, error)
	Events(ctx context.Context, p security.Principal, id string, after int64, limit int) ([]agentruns.Event, error)
	Cancel(ctx context.Context, p security.Principal, taskID string) error
}

// Quoter quotes text from outside Loomplane for the model.
// *agentsafety.Scanner is the real one.
type Quoter interface {
	Untrusted(ctx context.Context, source, text string) string
}

var _ Runs = (*agentruns.Service)(nil)

// titleOf is a conversation's title from its first message: the message's
// words on one line, cut at the last space before TitleLen runes, or at
// TitleLen runes when one word runs past it.
func titleOf(message string) string {
	runes := []rune(strings.Join(strings.Fields(message), " "))
	if len(runes) <= TitleLen {
		return string(runes)
	}
	cut := TitleLen
	for i := TitleLen; i > 0; i-- {
		if runes[i] == ' ' {
			cut = i
			break
		}
	}
	return strings.TrimRight(string(runes[:cut]), " ")
}
