package releaseprep

import (
	"errors"
	"time"
)

// Stored status: how far the preparation got.
const (
	StatusPlanning  = "planning"  // making or waiting for the plan
	StatusReviewing = "reviewing" // evidence stored; the agent run was started
	StatusDone      = "done"      // finished; State says how
	StatusFailed    = "failed"    // the job couldn't produce a report
	StatusCancelled = "cancelled"
)

// State is what people read. Settle stores it and a read recomputes it.
const (
	StatePlanning   = "planning"
	StateReviewing  = "reviewing"
	StatePromoting  = "promoting"          // the gate passed; the approval is in flight
	StatePromoted   = "promoted"           // the agent approved; the canary or watch takes it from here
	StateWaiting    = "waiting_for_person" // a gate failed; an approve_plan request is pending
	StateApproved   = "approved"           // a person approved it
	StateRejected   = "rejected"
	StateStale      = "stale"
	StateExpired    = "expired"
	StateSuperseded = "superseded"
	StateBlocked    = "blocked" // the plan has problems or failed to plan
	StateStopped    = "stopped" // the run ended without a promotion or a pending request; the plan waits for a person
	StateFailed     = "failed"
	StateCancelled  = "cancelled"
)

// Terminal reports whether a state is final: every state except planning,
// reviewing, promoting and waiting_for_person.
func Terminal(state string) bool {
	switch state {
	case StatePlanning, StateReviewing, StatePromoting, StateWaiting:
		return false
	}
	return true
}

// Decisions of the latest gate evaluation.
const (
	DecisionNone    = ""
	DecisionPromote = "promote" // the last evaluation allowed it
	DecisionAsk     = "ask"
)

// Where a person asked for the preparation.
const (
	ViaDashboard = "dashboard"
	ViaCLI       = "loomctl"
	ViaChat      = "chat"
)

// Kinds of concern and their bounds.
const (
	ConcernRisk      = "risk" // a note for reviewers
	ConcernAsk       = "ask"  // a reason to ask a person; always wins
	MaxConcerns      = 20
	MaxConcernLength = 2000 // characters
)

// ReleaseReport is one release preparation: what would change in production
// and whether the agent may promote it alone.
//
// Evidence and Concerns hold untrusted text (commit titles and authors,
// migration paths and patches, open report titles, what the model wrote).
// They are stored as data and never travel in a realtime event.
type ReleaseReport struct {
	ID                string             `json:"id" forge:"id"` // typeid "rrep"
	OrgID             string             `json:"org_id"`
	ProjectID         string             `json:"project_id"`
	FromEnvironmentID string             `json:"from_environment_id"`
	FromEnvironment   string             `json:"from_environment"`
	ToEnvironmentID   string             `json:"to_environment_id"`
	ToEnvironment     string             `json:"to_environment"`
	Scope             []string           `json:"scope"`
	Status            string             `json:"status"`
	State             string             `json:"state"`
	StateReason       string             `json:"state_reason"`
	RequestedBy       string             `json:"requested_by"` // a person's user ID, never empty
	Via               string             `json:"via"`
	PlanID            string             `json:"plan_id"`
	PlanRevision      string             `json:"plan_revision"`
	ReusedPlan        bool               `json:"reused_plan"`
	TaskID            string             `json:"task_id"`
	RunID             string             `json:"run_id"`
	Evidence          *ReadinessEvidence `json:"evidence"`
	Gates             []GateCheck        `json:"gates"`   // the latest evaluation, or the job's preview
	Preview           bool               `json:"preview"` // Gates came from the job, not from approve_plan
	Decision          string             `json:"decision"`
	EvaluatedAt       *time.Time         `json:"evaluated_at"`
	Concerns          []Concern          `json:"concerns"`
	RequestID         string             `json:"request_id"` // read time: the approve_plan request for the plan, if any
	DecidedBy         string             `json:"decided_by"` // read time: the plan's
	Error             string             `json:"error"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
	FinishedAt        *time.Time         `json:"finished_at"`
}

// ReleaseReportEvent is the realtime release_report event: exactly what the
// table's trigger projects. It is the report without its evidence, its
// concerns and the read-time fields, so no commit, diff or model text leaves
// in an event. Clients refetch the report for those.
type ReleaseReportEvent struct {
	ID                string      `json:"id" forge:"id"`
	OrgID             string      `json:"org_id"`
	ProjectID         string      `json:"project_id"`
	FromEnvironmentID string      `json:"from_environment_id"`
	FromEnvironment   string      `json:"from_environment"`
	ToEnvironmentID   string      `json:"to_environment_id"`
	ToEnvironment     string      `json:"to_environment"`
	Scope             []string    `json:"scope"`
	Status            string      `json:"status"`
	State             string      `json:"state"`
	StateReason       string      `json:"state_reason"`
	RequestedBy       string      `json:"requested_by"`
	Via               string      `json:"via"`
	PlanID            string      `json:"plan_id"`
	PlanRevision      string      `json:"plan_revision"`
	ReusedPlan        bool        `json:"reused_plan"`
	TaskID            string      `json:"task_id"`
	RunID             string      `json:"run_id"`
	Gates             []GateCheck `json:"gates"`
	Preview           bool        `json:"preview"`
	Decision          string      `json:"decision"`
	EvaluatedAt       *time.Time  `json:"evaluated_at"`
	Error             string      `json:"error"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
	FinishedAt        *time.Time  `json:"finished_at"`
}

// GateCheck is one of the seven gates as Loomplane computed it.
type GateCheck struct {
	Number int    `json:"number"` // 1..7
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	// Detail says why, for people; always set. It is Loomplane's own words
	// and travels in realtime events, so it never quotes commit, diff or
	// model text.
	Detail string `json:"detail"`
}

// Concern is a note the agent added to a report. It can only make a
// release more cautious.
type Concern struct {
	Kind  string    `json:"kind"`
	Text  string    `json:"text"` // model-written; outbound-scanned before people see it
	RunID string    `json:"run_id"`
	At    time.Time `json:"at"`
}

// Errors of the store.
var (
	ErrNotFound        = errors.New("release report not found")
	ErrInProgress      = errors.New("a release is already being prepared for this environment")
	ErrTooManyConcerns = errors.New("this report already has as many concerns as it can hold")
	// ErrMovedOn: the report is there, but no longer in the status or
	// state the change applies to, so nothing was written.
	ErrMovedOn = errors.New("the release report has moved on; nothing was changed")
)
