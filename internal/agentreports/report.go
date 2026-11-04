// Package agentreports holds the agent's reports: one record per incident the
// watcher or a chat found, with what is known about it, what the agent made of
// it, and where it was sent.
package agentreports

import "time"

// What raised a report.
const (
	SignalDeploymentFailed     = "deployment_failed"
	SignalCanaryRolledBack     = "canary_rolled_back"
	SignalCanaryRevertFailed   = "canary_revert_failed"
	SignalWatchRolledBack      = "watch_rolled_back"
	SignalWatchRollbackBlocked = "watch_rollback_blocked"
	SignalWatchRollbackFailed  = "watch_rollback_failed"
	SignalDrift                = "drift"
	SignalErrorRate            = "error_rate"
	SignalAnalysisFailing      = "analysis_failing"
	SignalManual               = "manual" // opened from a chat through open_report
)

// Where a report stands.
const (
	StatusOpen     = "open"
	StatusResolved = "resolved" // the signal cleared
	StatusClosed   = "closed"   // a person closed it
)

const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
	SeverityLow      = "low"
)

// Where an investigation stands.
const (
	InvestigationNone    = "none"
	InvestigationQueued  = "queued"
	InvestigationRunning = "running"
	InvestigationDone    = "done"
	InvestigationFailed  = "failed"
	InvestigationSkipped = "skipped"
)

// Where a report's GitHub issue stands.
const (
	IssueNone         = "none"
	IssueOpen         = "open"
	IssueClosed       = "closed"
	IssueWaiting      = "waiting_for_permission" // the installation hasn't accepted Issues
	IssueNoRepository = "no_repository"          // Loomplane couldn't tell which repository, or there's no installation on its owner
	IssueFailed       = "failed"                 // last attempt failed; Error says why; the delivery job retries
)

// Fact is one line of what is known.
type Fact struct {
	Label     string `json:"label"`
	Value     string `json:"value"`
	Untrusted bool   `json:"untrusted,omitempty"` // came from outside Loomplane (an error message, a verdict reason)
	Path      string `json:"path,omitempty"`      // dashboard path, such as "/deployments/dep_1"
}

// Evidence names the records a report is about.
type Evidence struct {
	DeploymentID string   `json:"deployment_id,omitempty"`
	PlanID       string   `json:"plan_id,omitempty"`
	BindingID    string   `json:"binding_id,omitempty"`
	WatchID      string   `json:"watch_id,omitempty"`
	AnalysisID   string   `json:"analysis_id,omitempty"`
	SourceAlias  string   `json:"source_alias,omitempty"`
	VerdictIDs   []string `json:"verdict_ids,omitempty"`
}

// Findings is what the agent's investigation wrote.
type Findings struct {
	Summary     string    `json:"summary"`
	LikelyCause string    `json:"likely_cause"`
	Suggestions []string  `json:"suggestions"`
	Confidence  string    `json:"confidence"` // low, medium, high
	WrittenAt   time.Time `json:"written_at"`
}

// Issue is the report's GitHub issue.
type Issue struct {
	Number int    `json:"number,omitempty"`
	URL    string `json:"url,omitempty"`
	State  string `json:"state"`
	Error  string `json:"error,omitempty"`
}

// Report is one incident. It is also the body of the agent_report realtime
// event, which the database trigger builds from the same fields.
type Report struct {
	ID                string     `json:"id" forge:"id"` // "arep_" + id
	OrgID             string     `json:"org_id"`
	ProjectID         string     `json:"project_id"`
	EnvironmentID     string     `json:"environment_id"`
	Environment       string     `json:"environment"`
	Signal            string     `json:"signal"`
	Component         string     `json:"component"`
	Fingerprint       string     `json:"fingerprint"`
	Status            string     `json:"status"`
	Severity          string     `json:"severity"`
	Title             string     `json:"title"`
	Facts             []Fact     `json:"facts"`
	Evidence          Evidence   `json:"evidence"`
	Findings          *Findings  `json:"findings"`
	Repository        string     `json:"repository"` // "owner/name"; "" when unknown
	Occurrences       int        `json:"occurrences"`
	FirstSeenAt       time.Time  `json:"first_seen_at"`
	LastSeenAt        time.Time  `json:"last_seen_at"`
	ResolvedAt        *time.Time `json:"resolved_at"`
	ClosedBy          string     `json:"closed_by"`
	Resolution        string     `json:"resolution"`
	TakenBy           string     `json:"taken_by"`
	TakenAt           *time.Time `json:"taken_at"`
	Investigation     string     `json:"investigation"`
	InvestigationNote string     `json:"investigation_note"`
	TaskID            string     `json:"task_id"`
	Issue             Issue      `json:"issue"`
	Revision          int        `json:"revision"`
	OpenedBy          string     `json:"opened_by"` // "" for detectors; the person for a manual report
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`

	// What the watcher and the agent's tools read, which people never see.
	RunID                  string     `json:"-"` // the investigation's run
	InvestigationStartedAt *time.Time `json:"-"` // when it was queued or started running
	OpenedByTask           string     `json:"-"` // the chat task that opened a manual report
}
