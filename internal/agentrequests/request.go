// Package agentrequests holds what the workspace's AI agent asks of people.
package agentrequests

// Request is one thing a run asks of a person, one row of
// loomplane_agent_requests, and the payload of the agent_request realtime
// events. The rest of its fields come with the requests service.
type Request struct {
	ID string `json:"id" forge:"id"`
}
