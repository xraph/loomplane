// Package reportops gives the agent its report operations: reading the
// workspace's reports, opening one a person asked for, and writing up the
// one it investigates. They are the agent's only; loomctl mcp doesn't
// serve them.
package reportops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

const (
	maxTitleRunes   = 120
	maxSummaryRunes = 2000
)

// ListInput is list_reports'.
type ListInput struct {
	Project string `json:"project,omitempty" jsonschema:"project slug, name or ID; empty for every project"`
	Status  string `json:"status,omitempty" jsonschema:"open (default), resolved, closed or all"`
}

// GetInput is get_report's.
type GetInput struct {
	ReportID string `json:"report_id"`
}

// OpenInput is open_report's.
type OpenInput struct {
	Project     string `json:"project"`
	Environment string `json:"environment,omitempty"`
	Title       string `json:"title" jsonschema:"one line, at most 120 characters"`
	Summary     string `json:"summary" jsonschema:"what happened and why it matters, at most 2000 characters"`
	Component   string `json:"component,omitempty" jsonschema:"service or resource name; defaults to the title"`
}

// UpdateInput is update_report's.
type UpdateInput struct {
	ReportID    string   `json:"report_id"`
	Summary     string   `json:"summary"`
	LikelyCause string   `json:"likely_cause,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
	Confidence  string   `json:"confidence" jsonschema:"low, medium or high"`
}

// The report operations.
var (
	ListReports  = defs.Define[ListInput]("list_reports", "List the workspace's agent reports, newest first.", defs.RiskRead, defs.ReadOnly())
	GetReport    = defs.Define[GetInput]("get_report", "Read one agent report with its facts and findings.", defs.RiskRead, defs.ReadOnly())
	OpenReport   = defs.Define[OpenInput]("open_report", "Open a report about a problem a person asked you to report. It is sent through Herald and opens a GitHub issue on the project's repository.", defs.RiskChange)
	UpdateReport = defs.Define[UpdateInput]("update_report", "Write your findings into the report you are investigating. Call it once, at the end.", defs.RiskChange)
)

// Evidence is the records a report is about, as the model reads them.
type Evidence struct {
	DeploymentID string `json:"deployment_id,omitempty"`
	PlanID       string `json:"plan_id,omitempty"`
	BindingID    string `json:"binding_id,omitempty"`
	WatchID      string `json:"watch_id,omitempty"`
	AnalysisID   string `json:"analysis_id,omitempty"`
	SourceAlias  string `json:"source_alias,omitempty"`
}

// ReportView is a report as the model reads it. Text from outside
// Loomplane (an untrusted fact, GitHub's error) and text people or an
// earlier run wrote (the findings, a closing reason) reach it quoted
// through Safety.Untrusted.
type ReportView struct {
	ID                string                 `json:"id"`
	ProjectID         string                 `json:"project_id"`
	EnvironmentID     string                 `json:"environment_id,omitempty"`
	Environment       string                 `json:"environment,omitempty"`
	Signal            string                 `json:"signal"`
	Component         string                 `json:"component"`
	Status            string                 `json:"status"`
	Severity          string                 `json:"severity"`
	Title             string                 `json:"title"`
	Facts             []agentreports.Fact    `json:"facts"`
	Evidence          Evidence               `json:"evidence"`
	Findings          *agentreports.Findings `json:"findings"`
	Repository        string                 `json:"repository,omitempty"`
	Occurrences       int                    `json:"occurrences"`
	FirstSeenAt       time.Time              `json:"first_seen_at"`
	LastSeenAt        time.Time              `json:"last_seen_at"`
	ResolvedAt        *time.Time             `json:"resolved_at,omitempty"`
	ClosedBy          string                 `json:"closed_by,omitempty"`
	Resolution        string                 `json:"resolution,omitempty"`
	Investigation     string                 `json:"investigation"`
	InvestigationNote string                 `json:"investigation_note,omitempty"`
	Issue             agentreports.Issue     `json:"issue"`
	OpenedBy          string                 `json:"opened_by,omitempty"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
}

// ReportSummary is one line of list_reports: what a report is about and
// where it stands, without its facts or findings, which get_report reads.
type ReportSummary struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	Environment   string    `json:"environment,omitempty"`
	Signal        string    `json:"signal"`
	Component     string    `json:"component"`
	Status        string    `json:"status"`
	Severity      string    `json:"severity"`
	Title         string    `json:"title"`
	Occurrences   int       `json:"occurrences"`
	LastSeenAt    time.Time `json:"last_seen_at"`
	Investigation string    `json:"investigation"`
	HasFindings   bool      `json:"has_findings"`
	IssueURL      string    `json:"issue_url,omitempty"`
}

type listOutput struct {
	Reports []ReportSummary `json:"reports"`
}

// errNoReports is a report operation called before aiagent built the
// reports.
var errNoReports = errors.New("reports aren't available on this server")

// errInvalid wraps agentreports.ErrInvalid, so the audit counts a bad
// request as one.
func errInvalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", agentreports.ErrInvalid, fmt.Sprintf(format, args...))
}

// Register adds the report operations to r over d. They read d.Reports and
// d's ports at call time.
func Register(r *agentops.Registry, d *agentops.Deps) {
	agentops.Register(r, ListReports, security.DeploymentRead, func(ctx context.Context, call agentops.Call, in ListInput) (listOutput, error) {
		return list(ctx, d, call, in)
	})
	agentops.Register(r, GetReport, security.DeploymentRead, func(ctx context.Context, call agentops.Call, in GetInput) (*ReportView, error) {
		rep, err := readable(ctx, d, call, in.ReportID)
		if err != nil {
			return nil, err
		}
		return view(ctx, d.Safety, rep), nil
	})
	agentops.Register(r, OpenReport, security.DeploymentCreate, func(ctx context.Context, call agentops.Call, in OpenInput) (*ReportView, error) {
		return open(ctx, d, call, in)
	})
	agentops.Register(r, UpdateReport, security.DeploymentCreate, func(ctx context.Context, call agentops.Call, in UpdateInput) (*ReportView, error) {
		return update(ctx, d, call, in)
	})
}

// list reads the reports the agent and the person may both read: in one
// project when the call names it or the run is about one, else in every
// project.
func list(ctx context.Context, d *agentops.Deps, call agentops.Call, in ListInput) (listOutput, error) {
	if d.Reports == nil {
		return listOutput{}, errNoReports
	}
	projectID := call.Scope.ProjectID
	if strings.TrimSpace(in.Project) != "" {
		p, err := d.FindProject(ctx, call, in.Project)
		if err != nil {
			return listOutput{}, err
		}
		projectID = p.ID
	}
	// A run about one environment sees only that environment's reports
	// and the project-wide ones, chosen before the page is cut.
	got, err := d.Reports.List(ctx, call.Principal, agentreports.ListFilter{
		Status: strings.TrimSpace(in.Status), ProjectID: projectID, EnvironmentID: call.Scope.EnvironmentID,
	})
	if err != nil {
		return listOutput{}, err
	}
	out := listOutput{Reports: make([]ReportSummary, 0, len(got.Reports))}
	for _, rep := range got.Reports {
		out.Reports = append(out.Reports, ReportSummary{
			ID: rep.ID, ProjectID: rep.ProjectID, Environment: rep.Environment, Signal: rep.Signal, Component: rep.Component,
			Status: rep.Status, Severity: rep.Severity, Title: rep.Title, Occurrences: rep.Occurrences, LastSeenAt: rep.LastSeenAt,
			Investigation: rep.Investigation, HasFindings: rep.Findings != nil, IssueURL: rep.Issue.URL,
		})
	}
	return out, nil
}

// readable is the report id as the agent and the person may read it,
// within the run's scope. A report they can't read, or outside the run's
// project, is not found.
func readable(ctx context.Context, d *agentops.Deps, call agentops.Call, id string) (*agentreports.Report, error) {
	if d.Reports == nil {
		return nil, errNoReports
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errInvalid("name the report: its report_id")
	}
	rep, err := d.Reports.Get(ctx, call.Principal, id)
	if err != nil {
		return nil, err
	}
	if err := call.InScope(ctx, rep.ProjectID, rep.EnvironmentID); err != nil {
		return nil, err
	}
	return rep, nil
}

// open opens a manual report in a project the agent and the person may
// both deploy to, and queues its delivery.
func open(ctx context.Context, d *agentops.Deps, call agentops.Call, in OpenInput) (*ReportView, error) {
	if d.Reports == nil {
		return nil, errNoReports
	}
	title, summary := strings.TrimSpace(in.Title), strings.TrimSpace(in.Summary)
	switch {
	case title == "" || strings.ContainsFunc(title, lineBreaking) || utf8.RuneCountInString(title) > maxTitleRunes:
		return nil, errInvalid("the title is one line of 1 to %d characters", maxTitleRunes)
	case summary == "" || utf8.RuneCountInString(summary) > maxSummaryRunes:
		return nil, errInvalid("the summary is 1 to %d characters", maxSummaryRunes)
	}
	p, err := d.FindProject(ctx, call, in.Project)
	if err != nil {
		return nil, err
	}
	var envID, envName string
	if name := strings.TrimSpace(in.Environment); name != "" {
		env, envErr := d.FindEnvironment(ctx, call, p.ID, name)
		if envErr != nil {
			return nil, envErr
		}
		envID, envName = env.ID, env.Name
	} else if call.Scope.EnvironmentID != "" {
		return nil, errInvalid("this conversation is about one environment: name it")
	}
	// Observe takes no principal: the agent and the person must both be
	// allowed to deploy here.
	if authErr := d.Authz.Require(ctx, call.Principal, security.DeploymentCreate, p.ID); authErr != nil {
		return nil, authErr
	}
	component := strings.TrimSpace(in.Component)
	if component == "" {
		component = strings.ReplaceAll(strings.ToLower(title), " ", "-")
	}
	repository := ""
	if len(p.Repositories) == 1 {
		repository = p.Repositories[0]
	}
	openedBy := call.Principal.UserID
	if person, ok := call.Principal.Person(); ok {
		openedBy = person.UserID
	}
	org := call.Principal.OrgID
	obs, err := d.Reports.Observe(ctx, agentreports.Signal{
		OrgID: org, ProjectID: p.ID, EnvironmentID: envID, Environment: envName,
		Kind: agentreports.SignalManual, Component: component, Severity: agentreports.SeverityMedium, Title: title,
		Facts:      []agentreports.Fact{{Label: "Summary", Value: summary}},
		Repository: repository, OpenedBy: openedBy, OpenedByTask: call.TaskID,
		Occurrence: call.TaskID + ":" + call.CallID,
	})
	if err != nil {
		return nil, err
	}
	// Only with a delivery job to queue: a delivery marked queued with no
	// job would wait out DeliveryRequeue before the watcher queued it.
	if d.DeliverReport != nil {
		queued, err := d.Reports.QueueDelivery(ctx, org, obs.Report.ID, time.Now())
		if err != nil {
			return nil, err
		}
		// The report is open either way: the watcher queues a delivery
		// that couldn't be queued here once its requeue time passes.
		if queued {
			if derr := d.DeliverReport(ctx, org, obs.Report.ID); derr != nil && d.Warn != nil {
				d.Warn("reportops: queue the delivery of report "+obs.Report.ID, derr)
			}
		}
	}
	return view(ctx, d.Safety, obs.Report), nil
}

// lineBreaking is a rune that has no place in a one-line title: a control
// character (C0, DEL or C1, tabs included) or a line or paragraph
// separator.
func lineBreaking(r rune) bool {
	return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
}

// update writes the findings of the report the call's task investigates,
// or of the manual report its chat opened.
func update(ctx context.Context, d *agentops.Deps, call agentops.Call, in UpdateInput) (*ReportView, error) {
	rep, err := readable(ctx, d, call, in.ReportID)
	if err != nil {
		return nil, err
	}
	// WriteFindings takes no principal. The agent alone holds
	// deployment:create, so an investigation passes; a chat passes only
	// when its person may deploy to the project too.
	if authErr := d.Authz.Require(ctx, call.Principal, security.DeploymentCreate, rep.ProjectID); authErr != nil {
		return nil, authErr
	}
	w := agentreports.Writer{TaskID: call.TaskID}
	if d.InvestigationOf != nil && call.TaskID != "" && call.RunID != "" {
		taskID, reportID, runErr := d.InvestigationOf(ctx, call.Principal.OrgID, call.RunID)
		if runErr != nil {
			return nil, runErr
		}
		// The run's own task record, never the model, says which report
		// it investigates.
		if taskID == call.TaskID {
			w.Investigates = reportID
		}
	}
	written, err := d.Reports.WriteFindings(ctx, call.Principal.OrgID, rep.ID, w, agentreports.FindingsInput{
		Summary: in.Summary, LikelyCause: in.LikelyCause, Suggestions: in.Suggestions, Confidence: in.Confidence,
	})
	if err != nil {
		return nil, err
	}
	return view(ctx, d.Safety, written), nil
}

// view is rep as the model reads it.
func view(ctx context.Context, safety *agentsafety.Scanner, rep *agentreports.Report) *ReportView {
	out := &ReportView{
		ID: rep.ID, ProjectID: rep.ProjectID, EnvironmentID: rep.EnvironmentID, Environment: rep.Environment,
		Signal: rep.Signal, Component: rep.Component, Status: rep.Status, Severity: rep.Severity, Title: rep.Title,
		Facts: make([]agentreports.Fact, 0, len(rep.Facts)),
		Evidence: Evidence{
			DeploymentID: rep.Evidence.DeploymentID, PlanID: rep.Evidence.PlanID, BindingID: rep.Evidence.BindingID,
			WatchID: rep.Evidence.WatchID, AnalysisID: rep.Evidence.AnalysisID, SourceAlias: rep.Evidence.SourceAlias,
		},
		Repository: rep.Repository, Occurrences: rep.Occurrences, FirstSeenAt: rep.FirstSeenAt, LastSeenAt: rep.LastSeenAt,
		ResolvedAt: rep.ResolvedAt, ClosedBy: rep.ClosedBy, Resolution: safety.Untrusted(ctx, agentsafety.SourceToolResult, rep.Resolution),
		Investigation: rep.Investigation, InvestigationNote: rep.InvestigationNote, Issue: rep.Issue,
		OpenedBy: rep.OpenedBy, CreatedAt: rep.CreatedAt, UpdatedAt: rep.UpdatedAt,
	}
	for _, f := range rep.Facts {
		if f.Untrusted {
			f.Value = safety.Untrusted(ctx, agentsafety.SourceErrorOutput, f.Value)
		}
		out.Facts = append(out.Facts, f)
	}
	out.Issue.Error = safety.Untrusted(ctx, agentsafety.SourceErrorOutput, rep.Issue.Error)
	if rep.Findings != nil {
		f := *rep.Findings
		f.Summary = safety.Untrusted(ctx, agentsafety.SourceToolResult, f.Summary)
		f.LikelyCause = safety.Untrusted(ctx, agentsafety.SourceToolResult, f.LikelyCause)
		f.Suggestions = make([]string, 0, len(rep.Findings.Suggestions))
		for _, s := range rep.Findings.Suggestions {
			f.Suggestions = append(f.Suggestions, safety.Untrusted(ctx, agentsafety.SourceToolResult, s))
		}
		out.Findings = &f
	}
	return out
}
