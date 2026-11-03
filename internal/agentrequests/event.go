// Package agentrequests holds what the workspace's AI agent asks of people.
package agentrequests

import "time"

// RequestEvent is a request as the agent_request realtime events carry it.
// The fields are the allowlist of loomplane_agent_event_trigger for
// loomplane_agent_requests and nothing else: never the question, its
// context, options or target, the answer or who gave it, or the
// suspension, checkpoint and tool call ids. A subscriber reads the rest
// through the requests API, which checks who may see it.
type RequestEvent struct {
	ID          string    `json:"id" forge:"id"`
	OrgID       string    `json:"org_id"`
	ProjectID   string    `json:"project_id"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	TeamID      string    `json:"team_id"`
	TeamName    string    `json:"team_name"`
	RouteSource string    `json:"route_source"`
	AskedFor    string    `json:"asked_for"` // the person asked; "" when nobody in particular
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
