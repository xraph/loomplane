package agentrequests

import (
	"context"
	"time"

	"github.com/xraph/grove/driver"
)

// Tx is one transaction: a request, its route and its notices commit in
// it together. A grove driver.Tx is one.
type Tx interface {
	Query(ctx context.Context, query string, args ...any) (driver.Rows, error)
	Exec(ctx context.Context, query string, args ...any) (driver.Result, error)
	Commit() error
	Rollback() error
}

// StoreFilter narrows Store.List.
type StoreFilter struct {
	// Status is one status, or "" for every one.
	Status    string
	ProjectID string
	TaskID    string
	// Mine, a user ID, keeps the requests asked of them or routed to a team
	// they're a current member of; with Admin, also those routed to the
	// workspace's admins.
	Mine  string
	Admin bool
	Limit int
}

// Settlement is how a request ends.
type Settlement struct {
	Status     string
	Outcome    string
	AnsweredBy string
	Answer     string
	At         time.Time
}

// Ref names a request in its workspace.
type Ref struct{ OrgID, ID string }

// Store keeps requests. Every method but Due reads and writes one
// workspace only.
type Store interface {
	// Begin starts the transaction a request is created in.
	Begin(ctx context.Context) (Tx, error)
	// Insert writes r in tx. It's false, and writes nothing, when the run
	// already has a request for r's tool call.
	Insert(ctx context.Context, tx Tx, r *Request) (bool, error)
	Get(ctx context.Context, orgID, id string) (*Request, error)
	// List returns the newest requests first.
	List(ctx context.Context, orgID string, f StoreFilter) ([]*Request, error)
	// Pause returns the requests made for one pause of a run.
	Pause(ctx context.Context, orgID, runID, suspensionID string) ([]*Request, error)
	// Settle moves a pending request to st. It's false when the request
	// wasn't pending: someone settled it first.
	Settle(ctx context.Context, orgID, id string, st Settlement) (bool, error)
	// Reopen moves a request st settled back to pending, when the run
	// couldn't be resumed with it. It's false when the request isn't
	// settled as st says.
	Reopen(ctx context.Context, orgID, id string, st Settlement) (bool, error)
	// CancelTask cancels the task's pending requests and returns them.
	CancelTask(ctx context.Context, orgID, taskID string, at time.Time) ([]*Request, error)
	// Due is the one read across workspaces, for the sweep: the pending
	// requests that settle on their own (plan approvals, GitHub and cloud
	// connections) and those past their expiry, oldest first.
	Due(ctx context.Context, now time.Time, limit int) ([]Ref, error)
}
