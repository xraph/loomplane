// Package agentruns runs the workspace's AI agent: its tasks and the events
// of their runs.
package agentruns

import "time"

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
