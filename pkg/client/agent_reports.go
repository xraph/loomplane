package client

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// --- The built-in agent's reports and watcher ---

// AgentReportFact is one line of what is known about a report. Untrusted
// marks text that came from outside Loomplane, such as an error message.
type AgentReportFact struct {
	Label     string `json:"label"`
	Value     string `json:"value"`
	Untrusted bool   `json:"untrusted,omitempty"`
	Path      string `json:"path,omitempty"`
}

// AgentReportEvidence names the records a report is about.
type AgentReportEvidence struct {
	DeploymentID string   `json:"deployment_id,omitempty"`
	PlanID       string   `json:"plan_id,omitempty"`
	BindingID    string   `json:"binding_id,omitempty"`
	WatchID      string   `json:"watch_id,omitempty"`
	AnalysisID   string   `json:"analysis_id,omitempty"`
	SourceAlias  string   `json:"source_alias,omitempty"`
	VerdictIDs   []string `json:"verdict_ids,omitempty"`
}

// AgentReportFindings is what the agent's investigation wrote.
type AgentReportFindings struct {
	Summary     string    `json:"summary"`
	LikelyCause string    `json:"likely_cause"`
	Suggestions []string  `json:"suggestions"`
	Confidence  string    `json:"confidence"`
	WrittenAt   time.Time `json:"written_at"`
}

// AgentReportIssue is a report's GitHub issue. State is none, open, closed,
// waiting_for_permission, no_repository or failed.
type AgentReportIssue struct {
	Number int    `json:"number,omitempty"`
	URL    string `json:"url,omitempty"`
	State  string `json:"state"`
	Error  string `json:"error,omitempty"`
}

// AgentReport is one incident the watcher or a chat found.
type AgentReport struct {
	ID            string `json:"id"`
	OrgID         string `json:"org_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	Environment   string `json:"environment"`
	Signal        string `json:"signal"`
	Component     string `json:"component"`
	Fingerprint   string `json:"fingerprint"`
	// Status is open, resolved or closed. Severity is critical, high,
	// medium or low.
	Status      string               `json:"status"`
	Severity    string               `json:"severity"`
	Title       string               `json:"title"`
	Facts       []AgentReportFact    `json:"facts"`
	Evidence    AgentReportEvidence  `json:"evidence"`
	Findings    *AgentReportFindings `json:"findings"`
	Repository  string               `json:"repository"`
	Occurrences int                  `json:"occurrences"`
	FirstSeenAt time.Time            `json:"first_seen_at"`
	LastSeenAt  time.Time            `json:"last_seen_at"`
	ResolvedAt  *time.Time           `json:"resolved_at"`
	ClosedBy    string               `json:"closed_by"`
	Resolution  string               `json:"resolution"`
	TakenBy     string               `json:"taken_by"`
	TakenAt     *time.Time           `json:"taken_at"`
	// Investigation is none, queued, running, done, failed or skipped.
	Investigation     string           `json:"investigation"`
	InvestigationNote string           `json:"investigation_note"`
	TaskID            string           `json:"task_id"`
	Issue             AgentReportIssue `json:"issue"`
	Revision          int              `json:"revision"`
	OpenedBy          string           `json:"opened_by"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

// AgentWatcherInstallation is one GitHub installation the watcher opens
// issues through.
type AgentWatcherInstallation struct {
	Account     string    `json:"account"`
	AccountType string    `json:"account_type"`
	Accepted    bool      `json:"accepted"`
	ManageURL   string    `json:"manage_url"`
	CheckedAt   time.Time `json:"checked_at"`
}

// AgentWatcherIssues says whether the watcher can open GitHub issues.
type AgentWatcherIssues struct {
	Configured    bool                       `json:"configured"`
	Installations []AgentWatcherInstallation `json:"installations"`
}

// AgentWatcher is the watcher's state: watching or paused, and why.
type AgentWatcher struct {
	Paused       bool               `json:"paused"`
	PausedReason string             `json:"paused_reason"`
	PausedAt     *time.Time         `json:"paused_at"`
	TickedAt     *time.Time         `json:"ticked_at"`
	Issues       AgentWatcherIssues `json:"issues"`
}

func agentReportPath(id, action string) string {
	p := "/api/v1/agent/reports/" + url.PathEscape(id)
	if action != "" {
		p += "/" + action
	}
	return p
}

// AgentReports lists the agent's reports, newest first. status is open,
// resolved, closed or all; empty means the server's default, the open ones.
// projectID, when set, keeps one project's.
func (c *Client) AgentReports(ctx context.Context, status, projectID string) ([]AgentReport, error) {
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	if projectID != "" {
		q.Set("project_id", projectID)
	}
	path := "/api/v1/agent/reports"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Reports []AgentReport `json:"reports"`
	}
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out.Reports, err
}

// AgentReport returns one report.
func (c *Client) AgentReport(ctx context.Context, id string) (*AgentReport, error) {
	var out AgentReport
	if err := c.do(ctx, http.MethodGet, agentReportPath(id, ""), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CloseAgentReport closes a report. The reason is kept with it.
func (c *Client) CloseAgentReport(ctx context.Context, id, reason string) (*AgentReport, error) {
	var out AgentReport
	body := struct {
		Reason string `json:"reason"`
	}{reason}
	if err := c.do(ctx, http.MethodPost, agentReportPath(id, "close"), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AgentWatcher returns the watcher's state.
func (c *Client) AgentWatcher(ctx context.Context) (*AgentWatcher, error) {
	var out AgentWatcher
	if err := c.do(ctx, http.MethodGet, "/api/v1/agent/watcher", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
