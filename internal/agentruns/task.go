// Package agentruns runs the workspace's AI agent: its tasks and the events
// of their runs.
//
// A task is one run of the agent, a chat turn or a piece of background
// work. Start writes it and queues a Dispatch job; the job runs the
// cortex run with a run ID assigned up front, so a retried job can't run
// the same work twice. Cortex's hooks queue a continuation when a paused
// run is ready again or a lapsed run can be reclaimed, and the run's
// events are kept in loomplane_agent_run_events for whoever follows it.
package agentruns

import (
	"encoding/json"
	"errors"
	"time"
)

// Kind is what a task is for.
type Kind string

// The kinds of task. 3c registers the investigation's spec and 3d the
// release preparation's.
const (
	KindChat               Kind = "chat"
	KindInvestigation      Kind = "investigation"
	KindReleasePreparation Kind = "release_preparation"
)

// KindSpec is how the agent runs one kind of task.
type KindSpec struct {
	Kind Kind
	// Agent is the cortex agent's name, such as "loomplane-chat".
	Agent string
	// Prompt is the system prompt. Start's job appends the names of the
	// task's workspace, project and environment.
	Prompt string
	// Tools allowlists registry operations by name. Nil is every
	// operation in the registry.
	Tools            []string
	MaxSteps         int
	DefaultMaxTokens int
	// MaxTokens is the most a StartInput may ask for.
	MaxTokens int
	// MaxOutputTokens is the most one model call may write, sent as the
	// call's max_tokens. It is not the task's budget, which is far larger
	// than any provider lets one call write. 0 means
	// defaultMaxOutputTokens.
	MaxOutputTokens int
	// UseSummaryModel runs the kind on the workspace's summary model.
	UseSummaryModel bool
}

// defaultMaxOutputTokens is a kind's per-call output cap when it sets
// none: room for a long reply or a large tool call, and under every
// supported provider's limit.
const defaultMaxOutputTokens = 16_000

// outputTokens is spec's per-call output cap.
func (spec KindSpec) outputTokens() int {
	if spec.MaxOutputTokens > 0 {
		return spec.MaxOutputTokens
	}
	return defaultMaxOutputTokens
}

// A task's statuses.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusWaiting   = "waiting" // paused on a person
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Terminal reports whether status is one a task never leaves.
func Terminal(status string) bool {
	return status == StatusCompleted || status == StatusFailed || status == StatusCancelled
}

// open are the statuses of a task that hasn't finished.
var open = []string{StatusQueued, StatusRunning, StatusWaiting}

// Task is one run of the agent.
type Task struct {
	ID            string            `json:"id" forge:"id"`
	OrgID         string            `json:"org_id"`
	ProjectID     string            `json:"project_id"`
	EnvironmentID string            `json:"environment_id"`
	Kind          Kind              `json:"kind"`
	RunID         string            `json:"run_id"`
	SessionID     string            `json:"session_id"`
	Status        string            `json:"status"`
	RequestedBy   string            `json:"requested_by"` // the person; "" for background work
	MaxTokens     int               `json:"max_tokens"`
	Tokens        int               `json:"tokens"`
	EventSeq      int64             `json:"event_seq"`
	Error         string            `json:"error"`
	Metadata      map[string]string `json:"metadata"`
	// ReleasePlanID is the plan the person started the task to release,
	// at ReleaseRevision. Only Start sets it, from the person's own call,
	// and it's the one record that makes a plan the agent didn't get
	// from the person count as their request.
	ReleasePlanID   string     `json:"release_plan_id,omitempty"`
	ReleaseRevision string     `json:"release_revision,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	FinishedAt      *time.Time `json:"finished_at"`
}

// TaskEvent is a task as the agent_task realtime events carry it. The
// columns are the allowlist of loomplane_agent_task_event_trigger and
// nothing else: never the input (a chat message is the person's), the
// principal, the session or the metadata.
type TaskEvent struct {
	ID            string     `json:"id" forge:"id"`
	OrgID         string     `json:"org_id"`
	ProjectID     string     `json:"project_id"`
	EnvironmentID string     `json:"environment_id"`
	Kind          string     `json:"kind"`
	RunID         string     `json:"run_id"`
	Status        string     `json:"status"`
	RequestedBy   string     `json:"requested_by"` // the person; "" for background work
	EventSeq      int64      `json:"event_seq"`
	Tokens        int        `json:"tokens"`
	MaxTokens     int        `json:"max_tokens"`
	Error         string     `json:"error"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	FinishedAt    *time.Time `json:"finished_at"`
}

// Event is one entry of a run's event log.
type Event struct {
	TaskID string `json:"task_id"`
	Seq    int64  `json:"seq"`
	// Kind is a cortex stream type (run_started, step, tool_call, token,
	// checkpoint, suspended, safety_block, incomplete, done, error), or
	// "tool_result", "request" or "status".
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
	At   time.Time       `json:"at"`
}

// The event kinds Loomplane adds to cortex's.
const (
	EventRequest = "request"
	EventStatus  = "status"
	// EventToolResult is how a tool call ended: {tool, call_id, outcome,
	// message}, outcome "ok", "failed" or "denied". It follows its
	// tool_call in the log.
	EventToolResult = "tool_result"
)

// The outcomes of a tool_result event.
const (
	OutcomeOK     = "ok"
	OutcomeFailed = "failed"
	OutcomeDenied = "denied"
)

// MetaTitle is the StartInput.Metadata key of a conversation's title,
// which its cortex session takes when the start job makes it.
const MetaTitle = "title"

// ListFilter narrows List.
type ListFilter struct {
	ProjectID   string
	Kind        Kind
	RequestedBy string
	Limit       int // 1..100, default 50
}

// The metadata keys the store keeps a task's release plan under. Start
// refuses them in a StartInput's Metadata: only its typed fields set them.
const (
	metaReleasePlan     = "release_plan_id"
	metaReleaseRevision = "release_revision"
)

// The metadata keys that name the report an investigation is about. Only
// an investigation the agent runs alone, which only the watcher starts,
// may carry them: Start refuses them on any other task, so a chat can
// never name a report as its own.
const (
	MetaReportID    = "report_id"
	MetaFingerprint = "fingerprint"
)

var (
	ErrNotFound      = errors.New("agent task not found")
	ErrUnknownKind   = errors.New("unknown agent task kind")
	ErrInvalid       = errors.New("invalid agent task")
	ErrNotCancelable = errors.New("the agent task has already finished")
)
