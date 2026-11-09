package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// --- Release preparation ---

// ReleaseGate is one of the seven gates, as Loomplane computed it. Detail
// is Loomplane's own words.
type ReleaseGate struct {
	Number int    `json:"number"`
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// ReleaseConcern is a note the agent added. Kind is "risk" or "ask". Text is
// the model's: untrusted.
type ReleaseConcern struct {
	Kind  string    `json:"kind"`
	Text  string    `json:"text"`
	RunID string    `json:"run_id"`
	At    time.Time `json:"at"`
}

// ReleaseReport is one release preparation. Evidence is kept as it came, so
// --json loses nothing; ReadinessEvidence reads the parts loomctl prints.
type ReleaseReport struct {
	ID                string           `json:"id"`
	OrgID             string           `json:"org_id"`
	ProjectID         string           `json:"project_id"`
	FromEnvironmentID string           `json:"from_environment_id"`
	FromEnvironment   string           `json:"from_environment"`
	ToEnvironmentID   string           `json:"to_environment_id"`
	ToEnvironment     string           `json:"to_environment"`
	Scope             []string         `json:"scope"`
	Status            string           `json:"status"`
	State             string           `json:"state"`
	StateReason       string           `json:"state_reason"`
	RequestedBy       string           `json:"requested_by"`
	Via               string           `json:"via"`
	PlanID            string           `json:"plan_id"`
	PlanRevision      string           `json:"plan_revision"`
	ReusedPlan        bool             `json:"reused_plan"`
	TaskID            string           `json:"task_id"`
	RunID             string           `json:"run_id"`
	Evidence          json.RawMessage  `json:"evidence"`
	Gates             []ReleaseGate    `json:"gates"`
	Preview           bool             `json:"preview"`
	Decision          string           `json:"decision"`
	EvaluatedAt       *time.Time       `json:"evaluated_at"`
	Concerns          []ReleaseConcern `json:"concerns"`
	RequestID         string           `json:"request_id"`
	DecidedBy         string           `json:"decided_by"`
	Error             string           `json:"error"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
	FinishedAt        *time.Time       `json:"finished_at"`
}

// ReleaseCommit is one commit between production and staging. Title and
// Author are untrusted.
type ReleaseCommit struct {
	SHA    string    `json:"sha"`
	Title  string    `json:"title"`
	Author string    `json:"author"`
	At     time.Time `json:"at"`
}

// ReleaseMigration is a changed migration or schema file. Path is untrusted.
type ReleaseMigration struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// ReleaseSource is what changes in one source.
type ReleaseSource struct {
	Alias            string             `json:"alias"`
	Repository       string             `json:"repository"`
	ProductionCommit string             `json:"production_commit"`
	StagingCommit    string             `json:"staging_commit"`
	Direction        string             `json:"direction"`
	AheadBy          int                `json:"ahead_by"`
	BehindBy         int                `json:"behind_by"`
	TotalCommits     int                `json:"total_commits"`
	Commits          []ReleaseCommit    `json:"commits"`
	FilesChanged     int                `json:"files_changed"`
	Migrations       []ReleaseMigration `json:"migrations"`
	Truncated        bool               `json:"truncated"`
	Error            string             `json:"error"`
}

// ReleaseSoak is how long staging has run what would be promoted.
type ReleaseSoak struct {
	Environment string        `json:"environment"`
	Running     bool          `json:"running"`
	Duration    time.Duration `json:"duration"`
	Healthy     bool          `json:"healthy"`
	Unverified  bool          `json:"unverified"`
}

// ReleaseCoverage is what a release in production would be watched for.
type ReleaseCoverage struct {
	Environment string        `json:"environment"`
	Duration    time.Duration `json:"duration"`
	Canary      []string      `json:"canary"`
	Unverified  bool          `json:"unverified"`
}

// ReleaseCanary is a production canary run. Reason is the provider's: untrusted.
type ReleaseCanary struct {
	DeploymentID string   `json:"deployment_id"`
	Status       string   `json:"status"`
	Services     []string `json:"services"`
}

// ReleaseArtifact says whether a service's image is reused or rebuilt.
type ReleaseArtifact struct {
	Service string `json:"service"`
	Reused  bool   `json:"reused"`
	Reason  string `json:"reason"`
}

// ReleaseOpenReport is an agent report open on staging. Title is untrusted.
type ReleaseOpenReport struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
}

// ReleaseBudget is the agent's allowance.
type ReleaseBudget struct {
	LimitUSD  string `json:"limit_usd"`
	SpentUSD  string `json:"spent_usd"`
	Exhausted bool   `json:"exhausted"`
}

// ReleaseEvidence is the part of a report's evidence loomctl prints.
type ReleaseEvidence struct {
	PlanStatus  string              `json:"plan_status"`
	Sources     []ReleaseSource     `json:"sources"`
	Artifacts   []ReleaseArtifact   `json:"artifacts"`
	Soak        *ReleaseSoak        `json:"soak"`
	Coverage    *ReleaseCoverage    `json:"coverage"`
	Canaries    []ReleaseCanary     `json:"canaries"`
	OpenReports []ReleaseOpenReport `json:"open_reports"`
	Budget      *ReleaseBudget      `json:"budget"`
	Unavailable map[string]string   `json:"unavailable"`
}

// ReadinessEvidence reads the evidence, nil when the report has none yet.
func (r *ReleaseReport) ReadinessEvidence() (*ReleaseEvidence, error) {
	if len(r.Evidence) == 0 || string(r.Evidence) == "null" {
		return nil, nil
	}
	var ev ReleaseEvidence
	if err := json.Unmarshal(r.Evidence, &ev); err != nil {
		return nil, err
	}
	return &ev, nil
}

// PrepareRelease is a release to prepare. Empty fields take the project's
// defaults: its only staging and production environments, every source.
type PrepareRelease struct {
	From    string   `json:"from,omitempty"`
	To      string   `json:"to,omitempty"`
	Scope   []string `json:"scope,omitempty"`
	Replace bool     `json:"replace,omitempty"`
}

// PrepareRelease starts preparing a release and returns its report at once;
// the plan, the evidence and the review follow. It acts as the person whose
// key this is.
func (c *Client) PrepareRelease(ctx context.Context, projectID string, in PrepareRelease) (*ReleaseReport, error) {
	var r ReleaseReport
	return &r, c.do(ctx, http.MethodPost, "/api/v1/projects/"+esc(projectID)+"/release-reports", in, &r)
}

// ReleaseReport reads one release report.
func (c *Client) ReleaseReport(ctx context.Context, id string) (*ReleaseReport, error) {
	var r ReleaseReport
	return &r, c.do(ctx, http.MethodGet, "/api/v1/release-reports/"+esc(id), nil, &r)
}

// ReleaseReports lists a project's release reports, newest first. A limit of
// 0 takes the server's default.
func (c *Client) ReleaseReports(ctx context.Context, projectID string, limit int) ([]ReleaseReport, error) {
	path := "/api/v1/projects/" + esc(projectID) + "/release-reports"
	if limit > 0 {
		path += "?" + url.Values{"limit": {strconv.Itoa(limit)}}.Encode()
	}
	var out struct {
		Reports []ReleaseReport `json:"reports"`
	}
	return out.Reports, c.do(ctx, http.MethodGet, path, nil, &out)
}

// CancelReleaseReport stops a release preparation that's still going.
func (c *Client) CancelReleaseReport(ctx context.Context, id string) (*ReleaseReport, error) {
	var r ReleaseReport
	return &r, c.do(ctx, http.MethodPost, "/api/v1/release-reports/"+esc(id)+"/cancel", nil, &r)
}
