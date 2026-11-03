package agentruns

import (
	"context"
	"encoding/json"
	"time"
)

// Record is what a task keeps that only its jobs read: the principal the
// codec encoded and the input the run starts from.
type Record struct {
	Principal []byte
	Input     string
}

// Transition moves a task to Status, but only from one of From.
type Transition struct {
	From   []string
	Status string
	// Error, when set, replaces the task's error.
	Error string
	// Tokens, when set, replaces the tokens the task used.
	Tokens *int
	// Event, when set, is appended to the run's events in the same
	// transaction, so a watcher that sees the new status has its event.
	Event *NewEvent
}

// NewEvent is an event before the store numbers it.
type NewEvent struct {
	Kind string
	Data json.RawMessage
}

// StoreFilter narrows a Store's List.
type StoreFilter struct {
	ProjectID   string
	Kind        Kind
	RequestedBy string
	Limit       int
}

// StaleTask is a task the sweep queues again: one queued too long
// (Queued), or one whose continuation was queued and never started.
type StaleTask struct {
	OrgID  string
	ID     string
	Queued bool
}

// Store keeps tasks and their events. Every method but Stale reads and
// writes one workspace's rows only.
type Store interface {
	Create(ctx context.Context, t *Task, rec Record) error
	Get(ctx context.Context, orgID, id string) (*Task, error)
	ByRun(ctx context.Context, orgID, runID string) (*Task, error)
	Record(ctx context.Context, orgID, id string) (*Record, error)
	// Session is the newest task of a conversation.
	Session(ctx context.Context, orgID, sessionID string) (*Task, error)
	List(ctx context.Context, orgID string, f StoreFilter) ([]*Task, error)
	// Open is every task of the workspace that hasn't finished.
	Open(ctx context.Context, orgID string) ([]*Task, error)
	// Transition reports whether the task moved. A terminal status sets
	// finished_at.
	Transition(ctx context.Context, orgID, id string, t Transition) (bool, error)
	// ClaimContinue sets continue_queued_at to now when it's unset or
	// older than stale, in one statement, and reports whether it did. A
	// finished task is never claimed.
	ClaimContinue(ctx context.Context, orgID, id string, now time.Time, stale time.Duration) (bool, error)
	ClearContinue(ctx context.Context, orgID, id string) error
	MarkEnqueued(ctx context.Context, orgID, id string, at time.Time) error
	// AppendEvents numbers events after the task's last and bumps its
	// event_seq, in one transaction, and returns the last number.
	AppendEvents(ctx context.Context, orgID, taskID string, events []NewEvent) (int64, error)
	Events(ctx context.Context, orgID, taskID string, after int64, limit int) ([]Event, error)
	// Stale reads across workspaces, for the sweep alone: tasks queued
	// since before queuedBefore, and open tasks whose continuation was
	// queued before continueBefore.
	Stale(ctx context.Context, queuedBefore, continueBefore time.Time, limit int) ([]StaleTask, error)
}
