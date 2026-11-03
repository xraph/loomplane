package client

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// --- The built-in agent's requests ---

// AgentRequest is one thing the agent asks of a person: a plan to approve,
// a secret to provide, a question, or an account to connect.
//
// A secret's value is never in one. ProvideAgentSecret sends it, and no
// answer carries it back.
type AgentRequest struct {
	ID            string `json:"id"`
	OrgID         string `json:"org_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	TaskID        string `json:"task_id"`
	RunID         string `json:"run_id"`
	// Kind is approve_plan, provide_secret, answer, connect_github or
	// create_connection.
	Kind string `json:"kind"`
	// Status is pending, answered, declined, expired or cancelled.
	Status   string             `json:"status"`
	Question string             `json:"question"`
	Context  []AgentRequestLink `json:"context"`
	Options  []string           `json:"options"`
	Target   AgentRequestTarget `json:"target"`
	// AskedFor is the person the agent was working for; empty for
	// background work.
	AskedFor   string `json:"asked_for"`
	AnsweredBy string `json:"answered_by"`
	Answer     string `json:"answer"`
	Outcome    string `json:"outcome"`
	// The route the request took when it was made. TeamName is the team
	// it went to, empty when the admins or the asker got it. RouteSource
	// is named (the agent picked the team, and TeamReason says why),
	// project (the project's responsible team), admins or asker. RouteNote
	// says why a team was passed over.
	TeamID      string     `json:"team_id"`
	TeamName    string     `json:"team_name"`
	RouteSource string     `json:"route_source"`
	TeamReason  string     `json:"team_reason"`
	RouteNote   string     `json:"route_note"`
	ExpiresAt   time.Time  `json:"expires_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	AnsweredAt  *time.Time `json:"answered_at"`
}

// AgentRequestLink points at a dashboard page for context.
type AgentRequestLink struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// AgentRequestTarget is what a request is about. Which fields are set
// depends on its kind.
type AgentRequestTarget struct {
	PlanID       string `json:"plan_id,omitempty"`
	PlanRevision string `json:"plan_revision,omitempty"`
	Key          string `json:"key,omitempty"`
	Environment  string `json:"environment,omitempty"`
	Service      string `json:"service,omitempty"`
	Cloud        string `json:"cloud,omitempty"`
	Repository   string `json:"repository,omitempty"`
}

// AgentRequestsOption narrows AgentRequests.
type AgentRequestsOption func(url.Values)

// ForMe keeps the requests asked of you: the ones you were the asker of,
// that went to a team you're in, or that went to the workspace admins when
// you're one.
func ForMe() AgentRequestsOption {
	return func(q url.Values) { q.Set("for", "me") }
}

// AgentRequests lists what the agent is asking people for, newest first.
// Only the pending ones unless all is true.
func (c *Client) AgentRequests(ctx context.Context, all bool, opts ...AgentRequestsOption) ([]AgentRequest, error) {
	q := url.Values{"status": {"pending"}}
	if all {
		q.Set("status", "all")
	}
	for _, o := range opts {
		o(q)
	}
	var out struct {
		Requests []AgentRequest `json:"requests"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/agent/requests?"+q.Encode(), nil, &out)
	return out.Requests, err
}

func agentRequestPath(id, action string) string {
	p := "/api/v1/agent/requests/" + url.PathEscape(id)
	if action != "" {
		p += "/" + action
	}
	return p
}

// AgentRequest returns one request.
func (c *Client) AgentRequest(ctx context.Context, id string) (*AgentRequest, error) {
	var out AgentRequest
	if err := c.do(ctx, http.MethodGet, agentRequestPath(id, ""), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AnswerAgentRequest answers a question the agent asked.
func (c *Client) AnswerAgentRequest(ctx context.Context, id, text string) (*AgentRequest, error) {
	var out AgentRequest
	body := struct {
		Text string `json:"text"`
	}{text}
	if err := c.do(ctx, http.MethodPost, agentRequestPath(id, "answer"), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ProvideAgentSecret sets the secret the agent asked for. Loomplane keeps
// it as a secret variable and never shows it to the agent or anyone else.
func (c *Client) ProvideAgentSecret(ctx context.Context, id, value string) (*AgentRequest, error) {
	var out AgentRequest
	body := struct {
		Value string `json:"value"`
	}{value}
	if err := c.do(ctx, http.MethodPost, agentRequestPath(id, "secret"), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CompleteAgentRequest says you've done what the agent asked: connected
// GitHub, or a cloud account. Loomplane checks before it believes you.
func (c *Client) CompleteAgentRequest(ctx context.Context, id string) (*AgentRequest, error) {
	var out AgentRequest
	if err := c.do(ctx, http.MethodPost, agentRequestPath(id, "complete"), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeclineAgentRequest turns a request down. The agent sees the reason. On a
// plan to approve, it rejects the plan.
func (c *Client) DeclineAgentRequest(ctx context.Context, id, reason string) (*AgentRequest, error) {
	var out AgentRequest
	body := struct {
		Reason string `json:"reason,omitempty"`
	}{reason}
	if err := c.do(ctx, http.MethodPost, agentRequestPath(id, "decline"), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
