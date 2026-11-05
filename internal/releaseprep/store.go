package releaseprep

import (
	"context"
	"time"

	"github.com/xraph/loomplane/internal/notifications"
)

// NoticeTx is the transaction Finish hands its send: enough to enqueue a
// notice and to resolve its recipients in the same transaction.
type NoticeTx interface {
	notifications.Execer
	notifications.Querier
}

// Store keeps release reports. Every method but Unsettled is scoped to one
// organization: a report of another organization reads as ErrNotFound and
// is never changed.
type Store interface {
	// Create stores a new report and fills its ID and times when they are
	// empty. It returns ErrInProgress when another report of the target
	// environment is planning or reviewing.
	Create(ctx context.Context, r *ReleaseReport) error
	Get(ctx context.Context, orgID, id string) (*ReleaseReport, error)
	// List returns a project's reports, newest first.
	List(ctx context.Context, orgID, projectID string, limit int) ([]*ReleaseReport, error)
	// Active returns the environment's report that is planning or
	// reviewing, or ErrNotFound when there is none.
	Active(ctx context.Context, orgID, toEnvironmentID string) (*ReleaseReport, error)
	// ForPlan returns the newest report of a plan.
	ForPlan(ctx context.Context, orgID, planID string) (*ReleaseReport, error)
	// Unsettled returns reports of any organization whose status is
	// planning or reviewing or whose state isn't Terminal, the least
	// recently swept first, and records that this sweep read them.
	Unsettled(ctx context.Context, limit int) ([]*ReleaseReport, error)
	// SetPlan records the plan once, while the report is planning and has
	// none. It reports whether it did.
	SetPlan(ctx context.Context, orgID, id, planID string, reused bool) (bool, error)
	// SetEvidence stores the plan revision, the evidence and the job's
	// preview of the gates (Preview true), while the report is planning;
	// otherwise ErrMovedOn.
	SetEvidence(ctx context.Context, orgID, id, planRevision string, ev *ReadinessEvidence, gates []GateCheck) error
	// SetTask records the review's task and run once, while the report is
	// planning and has none, and moves it to reviewing. It reports whether
	// it did.
	SetTask(ctx context.Context, orgID, id, taskID, runID string) (bool, error)
	// RecordEvaluation stores a gate evaluation (Preview false) while the
	// report is reviewing; otherwise ErrMovedOn. A nil ev keeps the stored
	// evidence.
	RecordEvaluation(ctx context.Context, orgID, id string, gates []GateCheck, decision string, ev *ReadinessEvidence, at time.Time) error
	// AddConcern appends a concern in one statement; ErrTooManyConcerns at
	// MaxConcerns.
	AddConcern(ctx context.Context, orgID, id string, c Concern) error
	// Settle moves a report from state from to a final status (done,
	// failed or cancelled) and a state. It writes only while the report is
	// in from and not yet settled (status planning or reviewing, or a state
	// that isn't Terminal); otherwise ErrMovedOn. A settled report never
	// moves again and never takes back its environment. It never cancels a
	// report whose decision is promote: the agent's approval may be landing.
	Settle(ctx context.Context, orgID, id, from, status, state, reason, errText string, at time.Time) error
	// Finish is Settle with its notice: in one transaction it moves the
	// report from state from to the final status and state, records the
	// state as notified when send is set, and calls send. When the report
	// has moved on it returns ErrMovedOn and sends nothing. When send
	// fails, nothing is kept and its error is returned, so the next sweep
	// tries again. The report stays locked through send, so a cancel that
	// arrives meanwhile waits and then finds it settled.
	Finish(ctx context.Context, orgID, id, from, status, state, reason, errText string, at time.Time, send func(ctx context.Context, tx NoticeTx) error) error
}
