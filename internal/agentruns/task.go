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
	// UseSummaryModel runs the kind on the workspace's summary model.
	UseSummaryModel bool
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
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	FinishedAt    *time.Time        `json:"finished_at"`
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
	// "request" or "status".
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
	At   time.Time       `json:"at"`
}

// The event kinds Loomplane adds to cortex's.
const (
	EventRequest = "request"
	EventStatus  = "status"
)

// ListFilter narrows List.
type ListFilter struct {
	ProjectID   string
	Kind        Kind
	RequestedBy string
	Limit       int // 1..100, default 50
}

// Metadata keys the run driver reads. A StartInput can't set the ones
// Loomplane writes itself.
const (
	// MetaPlanID names the plan a person started the task for, which
	// counts as their request for it.
	MetaPlanID = "plan_id"
	// metaCreatedPlans lists the plans the agent created during the
	// task, space-separated. Only the run driver writes it.
	metaCreatedPlans = "created_plans"
)

var (
	ErrNotFound      = errors.New("agent task not found")
	ErrUnknownKind   = errors.New("unknown agent task kind")
	ErrInvalid       = errors.New("invalid agent task")
	ErrNotCancelable = errors.New("the agent task has already finished")
)
