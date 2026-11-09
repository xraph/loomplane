package agentchat

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
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

// maxLockedSends bounds how many sends in this process hold the
// conversation lock at once. pglock holds a pooled connection for as long
// as fn runs, and fn needs more of them: the store's read, and Runs.Start's
// settings, budget, Warden and insert. Unbounded, a burst of sends across
// many conversations takes every connection and then waits forever for
// one more. Four leaves the rest of the pool free, as the authorizer's
// sync lock does (internal/security/teams.go).
const maxLockedSends = 4

// TryLockFunc runs fn while holding the lock called name and reports
// whether it ran. When someone else holds the lock it returns false at
// once instead of waiting. pglock.TryFor fits.
type TryLockFunc func(ctx context.Context, name string, fn func(context.Context) error) (bool, error)

// Deps are what a Service needs.
type Deps struct {
	Store Store
	Runs  Runs
	Authz *security.Authorizer
	// TryLock is the conversation's lock: pglock.TryFor over the app's
	// database, so replicas take turns too. A send that finds it held
	// gets ErrBusy at once, since the holder is already starting a turn.
	TryLock TryLockFunc
	// Safety quotes a page context's label for the model: the agent's
	// agentsafety.Scanner. Without it the label is withheld.
	Safety Quoter
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Service keeps people's conversations with the agent.
type Service struct {
	store Store
	runs  Runs
	authz *security.Authorizer
	lock  TryLockFunc
	quote Quoter
	now   func() time.Time
	// slots bounds the sends holding the lock (maxLockedSends).
	slots chan struct{}
}

// NewService returns a Service over d.
func NewService(d Deps) *Service {
	s := &Service{store: d.Store, runs: d.Runs, authz: d.Authz, lock: d.TryLock, now: d.Now,
		slots: make(chan struct{}, maxLockedSends)}
	if d.Safety != nil {
		s.quote = d.Safety
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
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
// read everyone's conversations. Someone who isn't a member doesn't, so
// they get the same ErrNotFound as anyone else and can't tell an ID
// exists.
func (s *Service) manages(ctx context.Context, p security.Principal) (bool, error) {
	if s.authz == nil {
		return false, errors.New("agentchat: no authorizer is configured")
	}
	ok, err := s.authz.Allowed(ctx, p, security.AIManage)
	if errors.Is(err, security.ErrNotMember) || errors.Is(err, security.ErrUnauthenticated) ||
		errors.Is(err, security.ErrForbidden) {
		return false, nil
	}
	return ok, err
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
	pc, err := pageContext(in.Context)
	if err != nil {
		return nil, err
	}
	title := titleOf(msg)
	who := p
	start := agentruns.StartInput{
		Kind: agentruns.KindChat, OrgID: p.OrgID, OnBehalfOf: &who, ProjectID: in.ProjectID, Input: msg,
		Metadata: map[string]string{agentruns.MetaTitle: title},
	}
	if cerr := s.withContext(ctx, p, &start, pc); cerr != nil {
		return nil, cerr
	}
	task, err := s.runs.Start(ctx, start)
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
// still on the last one, it's ErrBusy: a send holds the conversation's
// lock while it reads the latest turn and writes the next one's task, and
// a send that finds the lock held is busy at once, so two sends at once
// start one turn.
func (s *Service) Send(ctx context.Context, p security.Principal, id string, in SendInput) (*Sent, error) {
	if err := requirePerson(p); err != nil {
		return nil, err
	}
	msg, err := message(in.Message)
	if err != nil {
		return nil, err
	}
	pc, err := pageContext(in.Context)
	if err != nil {
		return nil, err
	}
	c, err := s.store.Conversation(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if merr := s.mine(ctx, p, c, ErrNotYours); merr != nil {
		return nil, merr
	}
	// The context is worked out before the lock, which holds a pooled
	// connection: a conversation's project never changes, so c's is the
	// next turn's too.
	where := agentruns.StartInput{ProjectID: c.ProjectID}
	if cerr := s.withContext(ctx, p, &where, pc); cerr != nil {
		return nil, cerr
	}
	var sent *Sent
	ran, err := s.locked(ctx, id, func(ctx context.Context) error {
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
			Preface: where.Preface, PageContext: where.PageContext,
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
	if !ran {
		return nil, ErrBusy
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
			CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt, Context: storedContext(r.Context)}
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

// locked runs fn while holding the conversation's lock, in one of the
// slots, and reports whether it ran: false when someone else holds the
// lock.
func (s *Service) locked(ctx context.Context, id string, fn func(context.Context) error) (bool, error) {
	if s.lock == nil {
		return false, errors.New("agentchat: no lock is configured")
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return false, ctx.Err()
	}
	return s.lock(ctx, lockPrefix+id, fn)
}

// mine checks p started c. Someone who manages the workspace's AI may read
// it, so they get notYours; anyone else ErrNotFound, so IDs don't leak.
func (s *Service) mine(ctx context.Context, p security.Principal, c *Conversation, notYours error) error {
	if c.StartedBy == p.UserID {
		return nil
	}
	admin, err := s.manages(ctx, p)
	if err != nil {
		return err
	}
	if admin {
		return notYours
	}
	return ErrNotFound
}

// changeable checks p may rename, pin or delete c: they started it, and
// may still use the agent where it runs, as stopping one of its tasks
// asks. Managing the workspace's AI doesn't let anyone change someone
// else's.
func (s *Service) changeable(ctx context.Context, p security.Principal, c *Conversation) error {
	if err := s.mine(ctx, p, c, ErrNotYoursToChange); err != nil {
		return err
	}
	if s.authz == nil {
		return errors.New("agentchat: no authorizer is configured")
	}
	err := s.authz.Require(ctx, p, security.AgentUse, c.ProjectID)
	if errors.Is(err, security.ErrNotMember) || errors.Is(err, security.ErrUnauthenticated) {
		return fmt.Errorf("%w: %w", security.ErrForbidden, err)
	}
	return err
}

// withContext puts pc on start, once it's scoped to start's project: on
// the turn as it is, and before the message as the preface the model
// reads. The message itself stays what the person typed.
func (s *Service) withContext(ctx context.Context, p security.Principal, start *agentruns.StartInput, pc *PageContext) error {
	pc, err := s.scoped(ctx, p, start.ProjectID, pc)
	if err != nil || pc == nil {
		return err
	}
	raw, err := contextJSON(pc)
	if err != nil {
		return err
	}
	start.PageContext, start.Preface = raw, s.preface(ctx, pc)
	return nil
}

// change checks in and returns what it writes.
func change(in ConversationUpdate) (Change, error) {
	if in.Title == nil && in.Pinned == nil {
		return Change{}, fmt.Errorf("%w: say what to change: a title, pinned, or both", ErrBadChange)
	}
	out := Change{Pinned: in.Pinned}
	if in.Title != nil {
		title := strings.Join(strings.Fields(*in.Title), " ")
		switch {
		case title == "":
			return Change{}, fmt.Errorf("%w: a conversation needs a title. Write one of up to %d characters", ErrBadChange, MaxTitle)
		case !utf8.ValidString(title) || strings.ContainsFunc(title, unicode.IsControl):
			return Change{}, fmt.Errorf("%w: a title is plain text on one line", ErrBadChange)
		case utf8.RuneCountInString(title) > MaxTitle:
			return Change{}, fmt.Errorf("%w: a title is up to %d characters", ErrBadChange, MaxTitle)
		}
		out.Title = &title
	}
	return out, nil
}

// Update renames, pins or unpins a conversation p started. The title they
// give is the conversation's from then on: nothing titles it again.
func (s *Service) Update(ctx context.Context, p security.Principal, id string, in ConversationUpdate) (*Conversation, error) {
	if err := requirePerson(p); err != nil {
		return nil, err
	}
	ch, err := change(in)
	if err != nil {
		return nil, err
	}
	c, err := s.store.Conversation(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if cerr := s.changeable(ctx, p, c); cerr != nil {
		return nil, cerr
	}
	ch.At = s.now().UTC()
	if uerr := s.store.Update(ctx, p.OrgID, id, ch); uerr != nil {
		return nil, uerr
	}
	return s.store.Conversation(ctx, p.OrgID, id)
}

// Delete deletes a conversation p started: it leaves their list, reads as
// not found, and takes no more messages. Its tasks, their events and
// requests stay. When the agent is still on its last message, that task is
// cancelled first, as stopping it would; a delete that finds a message on
// its way in is ErrSending.
func (s *Service) Delete(ctx context.Context, p security.Principal, id string) error {
	if err := requirePerson(p); err != nil {
		return err
	}
	c, err := s.store.Conversation(ctx, p.OrgID, id)
	if err != nil {
		return err
	}
	if cerr := s.changeable(ctx, p, c); cerr != nil {
		return cerr
	}
	ran, err := s.locked(ctx, id, func(ctx context.Context) error {
		// Read again under the lock: a send that held it before us has
		// written its task by now.
		latest, lerr := s.store.Conversation(ctx, p.OrgID, id)
		if lerr != nil {
			return lerr
		}
		if !agentruns.Terminal(latest.Status) {
			cerr := s.runs.Cancel(ctx, p, latest.LastTaskID)
			if cerr != nil && !errors.Is(cerr, agentruns.ErrNotCancelable) {
				return cerr
			}
		}
		return s.store.Delete(ctx, p.OrgID, id, s.now().UTC())
	})
	if err != nil {
		return err
	}
	if !ran {
		return ErrSending
	}
	return nil
}
