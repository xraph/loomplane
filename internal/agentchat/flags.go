package agentchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// FlagReason is why a person flags an answer of the agent.
type FlagReason string

// The reasons a person may give.
const (
	FlagOffensive  FlagReason = "offensive"
	FlagHarmful    FlagReason = "harmful"
	FlagInaccurate FlagReason = "inaccurate"
	FlagOther      FlagReason = "other"
)

// FlagReasons are the reasons a flag may give, in the order people pick
// from.
var FlagReasons = []FlagReason{FlagOffensive, FlagHarmful, FlagInaccurate, FlagOther}

const (
	// MaxFlagNote is the most characters of a flag's note.
	MaxFlagNote = 1000
	// MaxFlagAnswer is the most characters of the answer a flag keeps.
	MaxFlagAnswer = 8000
	// MaxFlags is the most flags Flags returns, the newest first.
	MaxFlags = 500
)

// The statuses Flags reads. "" is FlagsOpen.
const (
	FlagsOpen      = "open"
	FlagsDismissed = "dismissed"
	FlagsAll       = "all"
)

// The audit actions of a flag.
const (
	auditFlag    = "agent.answer.flag"
	auditDismiss = "agent.answer.flag.dismiss"
)

var (
	// ErrBadFlag wraps what's wrong with a flag, or with the status Flags
	// was asked for.
	ErrBadFlag = errors.New("invalid flag")
	// ErrUnfinished is a flag on an answer the agent is still writing or
	// is waiting on a person for.
	ErrUnfinished = errors.New("The agent hasn't finished this answer yet. Report it once it has.") //nolint:staticcheck,revive // ST1005: a sentence people read as is
	// ErrFlagNotFound is a flag ID that names no flag.
	ErrFlagNotFound = errors.New("flag not found")
)

// FlagInput is a person's report of one answer.
type FlagInput struct {
	Reason FlagReason `json:"reason"`
	// Note is what the person adds, up to MaxFlagNote characters.
	Note string `json:"note,omitempty"`
}

// Flag is a person's report of one of the agent's answers, for whoever
// runs the server. It keeps the question and the answer as they were
// when it was made, and nothing else of the conversation.
type Flag struct {
	ID             string `json:"id" forge:"id"`
	OrgID          string `json:"org_id"`
	ProjectID      string `json:"project_id"` // "" for the whole workspace
	ConversationID string `json:"conversation_id"`
	TaskID         string `json:"task_id"` // the turn's task
	ReportedBy     string `json:"reported_by"`
	Reason         string `json:"reason"`
	Note           string `json:"note"`
	// Question is the turn's message.
	Question string `json:"question"`
	// Answer is the turn's answer text, without what a safety stop
	// withheld, up to MaxFlagAnswer characters.
	Answer    string    `json:"answer"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// DismissedBy is who dismissed it, "" while it's open.
	DismissedBy string     `json:"dismissed_by"`
	DismissedAt *time.Time `json:"dismissed_at"`
}

// Flag reports one finished answer of a conversation p may read: its
// starter's, or anyone's for those who manage the workspace's AI. Anyone
// else gets the error History gives them. A task that isn't one of the
// conversation's turns is ErrNotFound, and an answer the agent hasn't
// finished is ErrUnfinished. Flagging the same answer again changes the
// reason and note of p's one flag on it, and opens it again if it was
// dismissed.
func (s *Service) Flag(ctx context.Context, p security.Principal, conversationID, taskID string, in FlagInput) (*Flag, error) {
	if err := requirePerson(p); err != nil {
		return nil, err
	}
	reason, note, err := flagInput(in)
	if err != nil {
		return nil, err
	}
	c, err := s.store.Conversation(ctx, p.OrgID, conversationID)
	if err != nil {
		return nil, err
	}
	if rerr := s.readable(ctx, p, c); rerr != nil {
		return nil, rerr
	}
	turn, err := s.store.Turn(ctx, p.OrgID, c.ID, taskID)
	if err != nil {
		return nil, err
	}
	if !agentruns.Terminal(turn.Status) {
		return nil, ErrUnfinished
	}
	events, err := s.events(ctx, p, turn.TaskID)
	if err != nil {
		return nil, err
	}
	id, err := typeid.Generate("aflag")
	if err != nil {
		return nil, fmt.Errorf("generate flag id: %w", err)
	}
	now := s.now().UTC()
	f, err := s.store.SaveFlag(ctx, Flag{
		ID: id.String(), OrgID: c.OrgID, ProjectID: c.ProjectID, ConversationID: c.ID, TaskID: turn.TaskID,
		ReportedBy: p.UserID, Reason: string(reason), Note: note, Question: turn.Input, Answer: answerOf(events),
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return nil, err
	}
	s.auditFlag(ctx, p, auditFlag, f)
	return f, nil
}

// Flags returns the newest flags of every workspace, up to MaxFlags:
// status is FlagsOpen ("" too), FlagsDismissed or FlagsAll. It checks no
// one: only the server's owner may read them, and the route sees to that.
func (s *Service) Flags(ctx context.Context, status string) ([]Flag, error) {
	if status == "" {
		status = FlagsOpen
	}
	if status != FlagsOpen && status != FlagsDismissed && status != FlagsAll {
		return nil, fmt.Errorf("%w: status is %s, %s or %s", ErrBadFlag, FlagsOpen, FlagsDismissed, FlagsAll)
	}
	flags, err := s.store.Flags(ctx, status, MaxFlags)
	if err != nil {
		return nil, err
	}
	if flags == nil {
		flags = []Flag{}
	}
	return flags, nil
}

// DismissFlag dismisses a flag on behalf of by, the server's owner, whom
// the route has checked. Dismissing it again keeps the first dismissal,
// and records nothing.
func (s *Service) DismissFlag(ctx context.Context, id, by string) (*Flag, error) {
	if by == "" {
		return nil, fmt.Errorf("%w: say who dismisses it", ErrBadFlag)
	}
	// Postgres keeps microseconds: at is what it keeps, so it can tell
	// this dismissal from an earlier one.
	at := s.now().UTC().Truncate(time.Microsecond)
	f, err := s.store.DismissFlag(ctx, id, by, at)
	if err != nil {
		return nil, err
	}
	if f.DismissedBy == by && f.DismissedAt != nil && f.DismissedAt.Equal(at) {
		// The flag's workspace keeps the record, beside the flag's own.
		owner := security.Principal{Kind: security.KindUser, UserID: by, OrgID: f.OrgID}
		s.auditFlag(ctx, owner, auditDismiss, f)
	}
	return f, nil
}

// flagInput checks in and returns its reason and its note, trimmed.
func flagInput(in FlagInput) (FlagReason, string, error) {
	if !slices.Contains(FlagReasons, in.Reason) {
		return "", "", fmt.Errorf("%w: pick a reason: offensive, harmful, inaccurate or other", ErrBadFlag)
	}
	note := strings.TrimSpace(in.Note)
	switch {
	case !utf8.ValidString(note) || strings.ContainsFunc(note, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
	}):
		return "", "", fmt.Errorf("%w: a note is plain text", ErrBadFlag)
	case utf8.RuneCountInString(note) > MaxFlagNote:
		return "", "", fmt.Errorf("%w: a note is up to %d characters", ErrBadFlag, MaxFlagNote)
	}
	return in.Reason, note, nil
}

// answerOf is the answer text of a turn's events: its token events'
// text joined, without the tokens a safety stop withheld, cut at
// MaxFlagAnswer characters.
func answerOf(events []agentruns.Event) string {
	var b strings.Builder
	n := 0
	for _, e := range events {
		if e.Kind != "token" {
			continue
		}
		var tok struct {
			Text     string `json:"text"`
			Withheld bool   `json:"withheld"`
		}
		if json.Unmarshal(e.Data, &tok) != nil || tok.Withheld {
			continue
		}
		for _, r := range tok.Text {
			if n == MaxFlagAnswer {
				return b.String()
			}
			b.WriteRune(r)
			n++
		}
	}
	return b.String()
}

// auditFlag records a flag or its dismissal: who, which answer, and the
// reason. Never the note, the question or the answer.
func (s *Service) auditFlag(ctx context.Context, p security.Principal, action string, f *Flag) {
	if s.audit == nil {
		return
	}
	s.audit.Record(context.WithoutCancel(ctx), projects.AuditEvent{
		Action: action, Severity: "info", ProjectID: f.ProjectID, Principal: p,
		Attributes: map[string]any{"flag_id": f.ID, "conversation_id": f.ConversationID, "task_id": f.TaskID, "reason": f.Reason},
	})
}
