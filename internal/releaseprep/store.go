package releaseprep

import (
	"context"
	"time"
)

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
	// planning or reviewing or whose state isn't Terminal, least recently
	// updated first.
	Unsettled(ctx context.Context, limit int) ([]*ReleaseReport, error)
	// SetPlan records the plan once, while the report is planning and has
	// none. It reports whether it did.
	SetPlan(ctx context.Context, orgID, id, planID string, reused bool) (bool, error)
	// SetEvidence stores the plan revision, the evidence and the job's
	// preview of the gates (Preview true).
	SetEvidence(ctx context.Context, orgID, id, planRevision string, ev *ReadinessEvidence, gates []GateCheck) error
	// SetTask records the review's task and run once, while the report is
	// planning and has none, and moves it to reviewing. It reports whether
	// it did.
	SetTask(ctx context.Context, orgID, id, taskID, runID string) (bool, error)
	// RecordEvaluation stores a gate evaluation (Preview false). A nil ev
	// keeps the stored evidence.
	RecordEvaluation(ctx context.Context, orgID, id string, gates []GateCheck, decision string, ev *ReadinessEvidence, at time.Time) error
	// AddConcern appends a concern in one statement; ErrTooManyConcerns at
	// MaxConcerns.
	AddConcern(ctx context.Context, orgID, id string, c Concern) error
	Settle(ctx context.Context, orgID, id, status, state, reason, errText string, at time.Time) error
	// MarkNotified reports true once per (report, state): the first call
	// for a state that differs from the last one marked.
	MarkNotified(ctx context.Context, orgID, id, state string) (bool, error)
}
