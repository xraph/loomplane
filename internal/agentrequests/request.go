// Package agentrequests holds what the workspace's AI agent asks of people.
//
// When a run needs a person (to approve a plan, connect GitHub, set a
// secret, connect a cloud or answer a question) cortex pauses it, and
// OnSuspended turns each pending call into a request. The request is
// routed to a team or to the workspace's admins, who are told through the
// notifications outbox in the same transaction. The first person allowed
// to settle it does, and the run resumes with what they did. Plan
// approvals are settled through the plan itself: Settle, which the run
// driver's sweep calls, sees the plan leave "ready" and decides the
// checkpoint.
package agentrequests

import (
	"errors"
	"time"
)

// Kind is what the agent asks for.
type Kind string

// The kinds of request.
const (
	KindApprovePlan      Kind = "approve_plan"
	KindConnectGitHub    Kind = "connect_github"
	KindProvideSecret    Kind = "provide_secret"
	KindCreateConnection Kind = "create_connection"
	KindAnswer           Kind = "answer"
)

// A request's statuses.
const (
	StatusPending   = "pending"
	StatusAnswered  = "answered"
	StatusDeclined  = "declined"
	StatusExpired   = "expired"
	StatusCancelled = "cancelled"
)

// How a request ended.
const (
	OutcomeApproved  = "approved"
	OutcomeRejected  = "rejected"
	OutcomeStale     = "stale"
	OutcomeExpired   = "expired"
	OutcomeProvided  = "provided"
	OutcomeConnected = "connected"
	OutcomeAnswered  = "answered"
	OutcomeDeclined  = "declined"
	OutcomeCancelled = "cancelled"
)

// RouteAsker is the route of a question or secret in a chat when the agent
// named no team that has members: it goes to the person who asked. The
// other route sources are teams.Route's: named, project and admins.
const RouteAsker = "asker"

// Link is a page of the dashboard that helps answer a request.
type Link struct {
	Label string `json:"label"`
	Path  string `json:"path"` // dashboard path
}

// Target is what a request is about. Which fields are set depends on its
// kind.
type Target struct {
	PlanID       string `json:"plan_id,omitempty"`
	PlanRevision string `json:"plan_revision,omitempty"`
	Key          string `json:"key,omitempty"`         // provide_secret
	Environment  string `json:"environment,omitempty"` // provide_secret
	Service      string `json:"service,omitempty"`     // provide_secret
	Cloud        string `json:"cloud,omitempty"`       // create_connection
	Repository   string `json:"repository,omitempty"`  // connect_github
}

// Request is one thing the agent asks of a person.
type Request struct {
	ID            string `json:"id" forge:"id"`
	OrgID         string `json:"org_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	TaskID        string `json:"task_id"`
	RunID         string `json:"run_id"`
	Kind          Kind   `json:"kind"`
	Status        string `json:"status"`
	// Question is the model's words, outbound-scanned.
	Question string   `json:"question"`
	Context  []Link   `json:"context"`
	Options  []string `json:"options"`
	Target   Target   `json:"target"`
	// AskedFor is the person the run acts for; "" for background work.
	AskedFor   string `json:"asked_for"`
	AnsweredBy string `json:"answered_by"`
	// Answer is what the person said, never a secret value.
	Answer string `json:"answer"`
	// Outcome is approved, rejected, stale, expired, provided, connected,
	// answered, declined or cancelled.
	Outcome string `json:"outcome"`
	// The route, as it was when the request was made: a later rename of
	// the team doesn't change it. TeamID is "" when the admins or the
	// asker got it; RouteSource is named, project, admins or asker.
	TeamID      string `json:"team_id"`
	TeamName    string `json:"team_name"`
	RouteSource string `json:"route_source"`
	// TeamReason is the agent's reason for naming the team, outbound-
	// scanned; RouteNote says why a team was passed over.
	TeamReason string     `json:"team_reason"`
	RouteNote  string     `json:"route_note"`
	ExpiresAt  time.Time  `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	AnsweredAt *time.Time `json:"answered_at"`

	// What the request resumes in cortex. Only the service reads them.
	SuspensionID string `json:"-"`
	CheckpointID string `json:"-"`
	ToolCallID   string `json:"-"`
}

// RequestList is a page of requests.
type RequestList struct {
	Requests []*Request `json:"requests"`
}

// ListFilter narrows List.
type ListFilter struct {
	// Status is "pending" (the default) or "all".
	Status    string
	ProjectID string
	// For is "me" for the requests asked of the caller, routed to a team
	// they're in, or routed to the workspace's admins when they manage
	// its AI. "" is every request.
	For   string
	Limit int // 1..100, default 50
}

// AnswerInput answers a question: 1 to 4000 characters.
type AnswerInput struct {
	Text string `json:"text"`
}

// SecretInput is a secret's value: 1 to 65536 bytes. It's never logged,
// stored outside the variable it sets, or returned.
type SecretInput struct {
	Value string `json:"value" sensitive:"true"`
}

// DeclineInput says why a request is declined.
type DeclineInput struct {
	Reason string `json:"reason,omitempty"`
}

var (
	ErrNotFound   = errors.New("agent request not found")
	ErrNotPending = errors.New("this request has already been settled")
	ErrInvalid    = errors.New("invalid answer")
	ErrNotDone    = errors.New("Loomplane can't see that it's done yet") //nolint:staticcheck // ST1005: the dashboard shows this sentence, and Loomplane is a name
)
