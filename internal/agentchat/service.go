package agentchat

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/security"
)

const (
	defaultListLimit    = 30
	maxListLimit        = 100
	defaultHistoryLimit = 20
	maxHistoryLimit     = 50
	// eventPage is the most events one agentruns.Events call returns.
	eventPage = 1000
)

// lockPrefix names the advisory lock a conversation's sends take turns
// on, followed by its ID.
const lockPrefix = "agent-conversation:"

// Deps are what a Service needs.
type Deps struct {
	Store Store
	Runs  Runs
	Authz *security.Authorizer
	// Lock runs fn holding the advisory lock called name: pglock.For over
	// the app's database, so replicas take turns too.
	Lock func(ctx context.Context, name string, fn func(context.Context) error) error
	// Now is the clock, nil for time.Now. The service reads no time of
	// its own yet: every time it returns is a task's.
	Now func() time.Time
}

// Service keeps people's conversations with the agent.
type Service struct {
	store Store
	runs  Runs
	authz *security.Authorizer
	lock  func(ctx context.Context, name string, fn func(context.Context) error) error
}

// NewService returns a Service over d.
func NewService(d Deps) *Service {
	return &Service{store: d.Store, runs: d.Runs, authz: d.Authz, lock: d.Lock}
}

// requirePerson refuses anyone but a person of a workspace: the agent never
// chats with itself, and Loomplane's automation doesn't chat.
func requirePerson(p security.Principal) error {
	if p.Kind != security.KindUser || p.OnBehalfOf != nil || p.UserID == "" || p.OrgID == "" {
		return fmt.Errorf("%w: only a person chats with the agent", security.ErrForbidden)
	}
	return nil
}

// message is raw trimmed, or ErrInvalid when it's empty or longer
// than MaxMessage characters.
func message(raw string) (string, error) {
	msg := strings.TrimSpace(raw)
	if msg == "" {
		return "", fmt.Errorf("%w: write a message", ErrInvalid)
	}
	if utf8.RuneCountInString(msg) > MaxMessage {
		return "", fmt.Errorf("%w: a message is up to %d characters", ErrInvalid, MaxMessage)
	}
	return msg, nil
}

// manages reports whether p manages the workspace's AI, which lets them
// read everyone's conversations.
func (s *Service) manages(ctx context.Context, p security.Principal) (bool, error) {
	if s.authz == nil {
		return false, errors.New("agentchat: no authorizer is configured")
	}
	return s.authz.Allowed(ctx, p, security.AIManage)
}

// Start opens a conversation with its first message. agentruns checks
// agent:use, the project, the workspace and its budget, and makes the
// conversation's session; its errors come back unchanged.
func (s *Service) Start(ctx context.Context, p security.Principal, in StartInput) (*Sent, error) {
	if err := requirePerson(p); err != nil {
		return nil, err
	}
	msg, err := message(in.Message)
	if err != nil {
		return nil, err
	}
	title := titleOf(msg)
	who := p
	task, err := s.runs.Start(ctx, agentruns.StartInput{
		Kind: agentruns.KindChat, OrgID: p.OrgID, OnBehalfOf: &who, ProjectID: in.ProjectID, Input: msg,
		Metadata: map[string]string{agentruns.MetaTitle: title},
	})
	if err != nil {
		return nil, err
	}
	return &Sent{
		Conversation: Conversation{
			ID: task.SessionID, OrgID: task.OrgID, ProjectID: task.ProjectID, Title: title, StartedBy: p.UserID,
			Status: task.Status, LastTaskID: task.ID, Turns: 1, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		},
		Task: *task,
	}, nil
}

// Send adds a message to a conversation p started. While the agent is
// still on the last one, it's ErrBusy: sends take turns on the
// conversation's lock, and the task each one starts is written before
// the lock goes, so two sends at once start one turn.
func (s *Service) Send(ctx context.Context, p security.Principal, id string, in SendInput) (*Sent, error) {
	if err := requirePerson(p); err != nil {
		return nil, err
	}
	msg, err := message(in.Message)
	if err != nil {
		return nil, err
	}
	c, err := s.store.Conversation(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if c.StartedBy != p.UserID {
		admin, aerr := s.manages(ctx, p)
		if aerr != nil {
			return nil, aerr
		}
		if admin {
			return nil, ErrNotYours
		}
		return nil, ErrNotFound
	}
	if s.lock == nil {
		return nil, errors.New("agentchat: no lock is configured")
	}
	var sent *Sent
	err = s.lock(ctx, lockPrefix+id, func(ctx context.Context) error {
		// Read again under the lock: a send that held it before us has
		// written its task by now.
		latest, lerr := s.store.Conversation(ctx, p.OrgID, id)
		if lerr != nil {
			return lerr
		}
		if !agentruns.Terminal(latest.Status) {
			return ErrBusy
		}
		who := p
		task, serr := s.runs.Start(ctx, agentruns.StartInput{
			Kind: agentruns.KindChat, OrgID: p.OrgID, OnBehalfOf: &who, ProjectID: latest.ProjectID, Input: msg,
			SessionID: latest.ID, Metadata: map[string]string{agentruns.MetaTitle: latest.Title},
		})
		if serr != nil {
			return serr
		}
		next := *latest
		next.Status, next.LastTaskID, next.Turns, next.UpdatedAt = task.Status, task.ID, latest.Turns+1, task.UpdatedAt
		sent = &Sent{Conversation: next, Task: *task}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sent, nil
}

// List returns p's own conversations, or with Everyone, everyone's in the
// workspace, which needs ai:manage.
func (s *Service) List(ctx context.Context, p security.Principal, f ListFilter) (*ConversationList, error) {
	if err := requirePerson(p); err != nil {
		return nil, err
	}
	startedBy := p.UserID
	if f.Everyone {
		admin, err := s.manages(ctx, p)
		if err != nil {
			return nil, err
		}
		if !admin {
			return nil, fmt.Errorf("%w (%s): only those who manage the workspace's AI see everyone's conversations",
				security.ErrForbidden, security.AIManage.Name())
		}
		startedBy = ""
	}
	if f.Limit <= 0 {
		f.Limit = defaultListLimit
	}
	f.Limit = min(f.Limit, maxListLimit)
	list, err := s.store.List(ctx, p.OrgID, startedBy, f)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []Conversation{}
	}
	return &ConversationList{Conversations: list}, nil
}

// History returns a page of a conversation's turns, each with its run's
// events. Its starter and those who manage the workspace's AI may read
// it; anyone else gets ErrNotFound, so IDs don't leak.
func (s *Service) History(ctx context.Context, p security.Principal, id string, q HistoryQuery) (*History, error) {
	if err := requirePerson(p); err != nil {
		return nil, err
	}
	c, err := s.store.Conversation(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if c.StartedBy != p.UserID {
		admin, aerr := s.manages(ctx, p)
		if aerr != nil {
			return nil, aerr
		}
		if !admin {
			return nil, ErrNotFound
		}
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	limit = min(limit, maxHistoryLimit)
	rows, more, err := s.store.Turns(ctx, p.OrgID, id, q.Before, limit)
	if err != nil {
		return nil, err
	}
	turns := make([]Turn, 0, len(rows))
	for _, r := range slices.Backward(rows) {
		events, eerr := s.events(ctx, p, r.TaskID)
		if eerr != nil {
			return nil, eerr
		}
		turn := Turn{TaskID: r.TaskID, Message: r.Input, Status: r.Status, Error: r.Error, Events: events,
			CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt}
		if n := len(events); n > 0 {
			turn.EventSeq = events[n-1].Seq
		}
		turns = append(turns, turn)
	}
	return &History{Conversation: *c, Turns: turns, More: more}, nil
}

// events reads every event of a task, a page at a time. agentruns checks
// p may see the task again.
func (s *Service) events(ctx context.Context, p security.Principal, taskID string) ([]agentruns.Event, error) {
	out := []agentruns.Event{}
	var after int64
	for {
		page, err := s.runs.Events(ctx, p, taskID, after, eventPage)
		if errors.Is(err, agentruns.ErrNotFound) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < eventPage {
			return out, nil
		}
		after = page[len(page)-1].Seq
	}
}
