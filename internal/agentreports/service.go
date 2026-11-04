package agentreports

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
)

const (
	maxTitleRunes      = 200
	maxSummaryRunes    = 2000
	maxCauseRunes      = 2000
	maxSuggestions     = 5
	maxSuggestionRunes = 500
	maxReasonRunes     = 500
	listLimit          = 50
	maxListLimit       = 100
	maxDocuments       = 200
)

// Signal is what a detector, or the agent's open_report tool, saw.
type Signal struct {
	OrgID, ProjectID, EnvironmentID, Environment string
	Kind                                         string // Signal* constant
	Component                                    string // action ID ("service:api"), binding name, source alias, or "deployment"
	Severity                                     string
	Title                                        string
	Facts                                        []Fact
	Evidence                                     Evidence
	Repository                                   string // "owner/name" or ""
	// Occurrence identifies one occurrence of an edge-triggered signal
	// ("dep_1#0": deployment and retry count). Empty for level-triggered
	// signals (drift, error_rate), which never count repeats while open.
	Occurrence             string
	At                     time.Time
	OpenedBy, OpenedByTask string // manual reports only
}

// Clearance is a detector saying a problem went away.
type Clearance struct {
	OrgID, ProjectID, EnvironmentID string
	Signals                         []string // which signal kinds clear
	Components                      []string // nil: every component of those kinds
	Reason                          string   // "A later deployment (dep_9) succeeded", for people
	At                              time.Time
}

// Change is what a signal did to the report that holds it.
type Change string

const (
	ChangeOpened    Change = "opened"
	ChangeReopened  Change = "reopened"
	ChangeRepeated  Change = "repeated"
	ChangeUnchanged Change = "unchanged"
)

// Observation is the report a signal landed on, and what it did there.
type Observation struct {
	Report *Report
	Change Change
}

// Investigation is where a report's investigation moves to.
type Investigation struct {
	Status string // Investigation* constant
	Note   string // why skipped or failed, for people
	TaskID string
	RunID  string
}

// FindingsInput is what an investigation writes.
type FindingsInput struct {
	Summary     string   `json:"summary"`      // 1..2000 characters
	LikelyCause string   `json:"likely_cause"` // 0..2000
	Suggestions []string `json:"suggestions"`  // 0..5, each 1..500
	Confidence  string   `json:"confidence"`   // low, medium or high
}

// Writer is the task writing a report's findings.
type Writer struct {
	TaskID string
	// Investigates is the report the task's own record says it
	// investigates, read from the task store and never from the model;
	// "" for every other task. The watcher starts an investigation before
	// it can store the task's ID on the report, so until it does, this is
	// how the investigation is told apart.
	Investigates string
}

// ListFilter narrows a person's list.
type ListFilter struct {
	Status    string // "open" (default), "resolved", "closed" or "all"
	ProjectID string
	Limit     int // 1..100, default 50
}

// ReportList is a page of reports.
type ReportList struct {
	Reports []*Report `json:"reports"`
}

// CloseInput is a person closing a report.
type CloseInput struct {
	Reason string `json:"reason,omitempty"` // 0..500
}

var (
	ErrNotFound = errors.New("report not found")
	ErrInvalid  = errors.New("invalid report")
	ErrNotOpen  = errors.New("this report is already closed")
	// ErrLeaseLost is a delivery step finding another worker holds the
	// report's delivery now: it stops, and leaves GitHub alone.
	ErrLeaseLost     = errors.New("another worker took this report's delivery over")
	ErrNotYourReport = errors.New("only the investigation of this report can write its findings")
)

// Store keeps reports. Every method is scoped to one workspace, and
// Observe applies its rules so that two writers can't race.
type Store interface {
	Get(ctx context.Context, orgID, id string) (*Report, error)
	// List returns the reports in projectIDs, newest first. The filter is
	// already checked.
	List(ctx context.Context, orgID string, projectIDs []string, f ListFilter) ([]*Report, error)
	// Open returns the open reports; projectID "" means every project and
	// environmentID "" every environment.
	Open(ctx context.Context, orgID, projectID, environmentID string) ([]*Report, error)
	// Investigating returns the reports, in any status, whose
	// investigation is queued or running.
	Investigating(ctx context.Context, orgID string) ([]*Report, error)
	// Observe opens, repeats or reopens the report for sig's fingerprint.
	// A report that resolved or closed at or after reopenAfter reopens.
	Observe(ctx context.Context, sig Signal, reopenAfter time.Time, now time.Time) (*Observation, error)
	// Clear resolves the open reports c names. Manual reports never clear.
	Clear(ctx context.Context, c Clearance) ([]*Report, error)
	// Close closes an open report, or returns ErrNotOpen.
	Close(ctx context.Context, orgID, id, by, reason string, at time.Time) (*Report, error)
	// SetInvestigation moves the investigation only when it is in from,
	// and reports whether it did.
	SetInvestigation(ctx context.Context, orgID, id string, from []string, inv Investigation, at time.Time) (bool, error)
	// WriteFindings writes only for w's task when it is the report's
	// investigation or the chat that opened it, or returns
	// ErrNotYourReport. An investigation the watcher has queued but not
	// yet stored the task of (no task ID, queued or running) also takes
	// the findings of a task whose own record names the report.
	WriteFindings(ctx context.Context, orgID, id string, w Writer, f Findings) (*Report, error)
	// Documents returns the reports with findings or no longer open,
	// updated after after, oldest first. A full page goes on through every
	// report updated at the same moment as its last, so it can hold more
	// than limit.
	Documents(ctx context.Context, orgID string, after time.Time, limit int) ([]*Report, error)

	// Pending returns the reports whose notice or issue is behind their
	// revision, and the open ones waiting for an installation to accept
	// Issues, oldest first.
	Pending(ctx context.Context, orgID string) ([]*Report, error)
	// QueueDelivery marks a report's delivery queued at now, when it
	// isn't, or was queued over DeliveryRequeue before now, and reports
	// whether it did.
	QueueDelivery(ctx context.Context, orgID, id string, now time.Time) (bool, error)
	// ClaimDelivery holds the report's delivery for owner for lease, when
	// nobody else holds it, and returns where it stands. It's nil, with
	// no error, when another owner holds it.
	ClaimDelivery(ctx context.Context, orgID, id, owner string, lease time.Duration) (*Delivery, error)
	// SaveNotice records the notice step in one transaction, only while
	// owner holds the delivery and the report is still at revision with
	// pendingNotice queued: when pendingNotice is set, send runs inside
	// the transaction with the notice's sequence number, and the notice
	// counts as sent. notified_revision becomes revision either way. It
	// reports false, having saved nothing, when the report moved on.
	SaveNotice(ctx context.Context, orgID, id, owner string, revision int, pendingNotice string,
		send func(ctx context.Context, q notifications.Execer, seq int) error, at time.Time) (bool, error)
	// RenewDelivery extends owner's hold on the delivery by lease, only
	// while owner still holds it and the lease hasn't run out, and returns
	// where it stands now. Otherwise it's ErrLeaseLost: unlike
	// ClaimDelivery, it never takes up a delivery that was let go.
	RenewDelivery(ctx context.Context, orgID, id, owner string, lease time.Duration) (*Delivery, error)
	// SaveIssue records the issue step while owner holds the delivery,
	// and is ErrLeaseLost when owner no longer does.
	SaveIssue(ctx context.Context, orgID, id, owner string, revision int, in IssueSave) error
	// ReleaseDelivery lets the delivery go; done also clears its queued
	// mark.
	ReleaseDelivery(ctx context.Context, orgID, id, owner string, done bool) error
}

// Delivery is a report with where its delivery stands, which people
// never see.
type Delivery struct {
	Report           *Report
	NotifiedRevision int
	IssueRevision    int
	PendingNotice    string // "", "opened", "reopened" or "repeated"
	NoticeSeq        int
	LastNoticeAt     *time.Time
	IssueComments    []string // queued comment kinds, oldest first
	IssueCommentAt   *time.Time
}

// IssueSave is what the issue step writes back.
type IssueSave struct {
	Number int
	URL    string
	State  string // Issue* constant
	Error  string
	// Handled is how many of the queued comments, from the front, were
	// posted or dropped. Those go; ones queued since stay.
	Handled int
	// Requeue goes back on the front of the queue: what's left of a
	// handled comment, such as the close after it.
	Requeue []string
	// CommentAt is when the last repeat comment went out; nil keeps it.
	CommentAt *time.Time
	// Done moves issue_revision to the revision delivered.
	Done bool
}

// Authorizer is the part of *security.Authorizer reports use.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
	AllowedOn(ctx context.Context, p security.Principal, perm security.Permission, projectID string) (bool, error)
}

var _ Authorizer = (*security.Authorizer)(nil)

// Projects lists the projects a person sees. *projects.Service is the real
// one.
type Projects interface {
	List(ctx context.Context, p security.Principal) ([]*projects.Project, error)
}

// SecretValues returns the plaintext of every secret that applies to a
// project in an environment ("" for every environment), so delivery can
// redact them from what it sends.
type SecretValues interface {
	SecretValues(ctx context.Context, orgID, projectID, environment string) ([]string, error)
}

// Starters names who started a deployment, so its report reaches them.
type Starters interface {
	StartedBy(ctx context.Context, orgID, deploymentID string) (string, error)
}

// Teams names a project's responsible team. *teams.Service is the real
// one.
type Teams interface {
	Resolve(ctx context.Context, q notifications.Querier, orgID, projectID, named, reason string) (teams.Route, error)
}

// ProjectReader reads one project without a person, for the name a
// report shows. *projects.PgStore is the real one.
type ProjectReader interface {
	GetProject(ctx context.Context, orgID, id string) (*projects.Project, error)
}

// People names a person in what reports send. agentrequests.PgPeople is
// the real one: it never returns an email.
type People interface {
	Name(ctx context.Context, userID string) string
}

// Deps are what a Service needs.
type Deps struct {
	Store Store
	// DB reads who hears about a report: the owners and admins, and,
	// through Teams, the project's responsible team. *pgdriver.PgDB is
	// the real one.
	DB            notifications.Querier
	Authz         Authorizer
	Projects      Projects
	ProjectReader ProjectReader // nil: reports name the project by its ID
	People        People        // nil: a person closing a report is "Someone"
	Teams         Teams         // nil: no responsible team hears about reports
	Safety        *agentsafety.Scanner
	Binder        *agentscope.Binder
	Issues        repositories.IssueTracker // nil: Herald only
	Secrets       SecretValues              // nil: only the outbound scan stands between a report and GitHub
	Starters      Starters                  // nil: the deployment's starter isn't told
	Audit         projects.Auditor
	PublicURL     string
	Now           func() time.Time
	Warn          func(msg string, err error)
}

// Service keeps one report per incident: detectors and the agent open and
// clear them, people read and close them.
type Service struct {
	d         Deps
	mu        sync.RWMutex
	observers []Observer
}

// NewService returns a Service over d.
func NewService(d Deps) *Service { return &Service{d: d} }

func (s *Service) now() time.Time {
	if s.d.Now != nil {
		return s.d.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) warn(msg string, err error) {
	if s.d.Warn != nil {
		s.d.Warn(msg, err)
	}
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// forbidden folds a caller who isn't a member into ErrForbidden.
func forbidden(err error) error {
	if errors.Is(err, security.ErrNotMember) || errors.Is(err, security.ErrUnauthenticated) {
		return fmt.Errorf("%w: %w", security.ErrForbidden, err)
	}
	return err
}

// List returns the reports p may read: agent:use in the workspace, and
// only the projects p has deployment:read on.
func (s *Service) List(ctx context.Context, p security.Principal, f ListFilter) (*ReportList, error) {
	if err := s.d.Authz.Require(ctx, p, security.AgentUse, ""); err != nil {
		return nil, forbidden(err)
	}
	switch f.Status {
	case "":
		f.Status = StatusOpen
	case StatusOpen, StatusResolved, StatusClosed, "all":
	default:
		return nil, invalid("status is open, resolved, closed or all")
	}
	switch {
	case f.Limit == 0:
		f.Limit = listLimit
	case f.Limit < 0 || f.Limit > maxListLimit:
		return nil, invalid("limit is 1 to %d", maxListLimit)
	}
	ids, err := s.readable(ctx, p, f.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return &ReportList{Reports: []*Report{}}, nil
	}
	reports, err := s.d.Store.List(ctx, p.OrgID, ids, f)
	if err != nil {
		return nil, err
	}
	return &ReportList{Reports: reports}, nil
}

// readable is the projects whose reports p may read: projectID alone when
// set, else every project p sees. A team's grant counts because each
// check names the project.
func (s *Service) readable(ctx context.Context, p security.Principal, projectID string) ([]string, error) {
	candidates := []string{projectID}
	if projectID == "" {
		if s.d.Projects == nil {
			return nil, errors.New("agentreports: no project list is configured")
		}
		list, err := s.d.Projects.List(ctx, p)
		if err != nil {
			return nil, forbidden(err)
		}
		candidates = candidates[:0]
		for _, proj := range list {
			candidates = append(candidates, proj.ID)
		}
	}
	var ids []string
	for _, id := range candidates {
		ok, err := s.d.Authz.AllowedOn(ctx, p, security.DeploymentRead, id)
		if err != nil {
			return nil, err
		}
		if ok {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// Get returns a report p may read: agent:use in the workspace and
// deployment:read on its project. Otherwise it's ErrNotFound, so a report
// in a project p can't see doesn't show it exists.
func (s *Service) Get(ctx context.Context, p security.Principal, id string) (*Report, error) {
	if err := s.d.Authz.Require(ctx, p, security.AgentUse, ""); err != nil {
		return nil, forbidden(err)
	}
	r, err := s.d.Store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if err = s.d.Authz.Require(ctx, p, security.DeploymentRead, r.ProjectID); err != nil {
		if errors.Is(forbidden(err), security.ErrForbidden) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return r, nil
}

// Close is a person closing a report, which needs deployment:create on its
// project.
func (s *Service) Close(ctx context.Context, p security.Principal, id string, in CloseInput) (*Report, error) {
	r, err := s.Get(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if err = s.d.Authz.Require(ctx, p, security.DeploymentCreate, r.ProjectID); err != nil {
		return nil, forbidden(err)
	}
	reason := strings.TrimSpace(in.Reason)
	if utf8.RuneCountInString(reason) > maxReasonRunes {
		return nil, invalid("the reason is at most %d characters", maxReasonRunes)
	}
	if r.Status != StatusOpen {
		return nil, ErrNotOpen
	}
	closed, err := s.d.Store.Close(ctx, p.OrgID, id, p.UserID, reason, s.now())
	if err != nil {
		return nil, err
	}
	s.audit(ctx, p, "agent.report.close", closed)
	s.notify(ctx, closed)
	return closed, nil
}

// Observe records a signal: it opens a report, counts a repeat on the open
// one, or reopens one that resolved within ReopenWindow.
func (s *Service) Observe(ctx context.Context, sig Signal) (*Observation, error) {
	if err := validSignal(&sig); err != nil {
		return nil, err
	}
	now := s.now()
	if sig.At.IsZero() {
		sig.At = now
	}
	sig.At = sig.At.UTC()
	obs, err := s.d.Store.Observe(ctx, sig, sig.At.Add(-ReopenWindow), now)
	if err != nil {
		return nil, err
	}
	agent := security.AgentPrincipal(sig.OrgID, nil)
	switch obs.Change {
	case ChangeOpened:
		s.audit(ctx, agent, "agent.report.open", obs.Report)
	case ChangeReopened:
		s.audit(ctx, agent, "agent.report.reopen", obs.Report)
		s.notify(ctx, obs.Report)
	}
	return obs, nil
}

func validSignal(sig *Signal) error {
	sig.Title = strings.TrimSpace(sig.Title)
	switch {
	case sig.OrgID == "" || sig.ProjectID == "":
		return invalid("a signal names its workspace and project")
	case sig.Component == "":
		return invalid("a signal names its component")
	case !slices.Contains(signals, sig.Kind):
		return invalid("unknown signal %q", sig.Kind)
	case !slices.Contains(severities, sig.Severity):
		return invalid("unknown severity %q", sig.Severity)
	case sig.Title == "" || utf8.RuneCountInString(sig.Title) > maxTitleRunes:
		return invalid("the title is 1 to %d characters", maxTitleRunes)
	}
	return nil
}

var (
	signals = []string{
		SignalDeploymentFailed, SignalCanaryRolledBack, SignalCanaryRevertFailed, SignalWatchRolledBack,
		SignalWatchRollbackBlocked, SignalWatchRollbackFailed, SignalDrift, SignalErrorRate, SignalAnalysisFailing, SignalManual,
	}
	severities     = []string{SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow}
	investigations = []string{
		InvestigationNone, InvestigationQueued, InvestigationRunning, InvestigationDone, InvestigationFailed, InvestigationSkipped,
	}
	confidences = []string{"low", "medium", "high"}
)

// Clear resolves the open reports a detector says went away.
func (s *Service) Clear(ctx context.Context, c Clearance) ([]*Report, error) {
	if c.OrgID == "" || c.ProjectID == "" || len(c.Signals) == 0 {
		return nil, invalid("a clearance names its workspace, project and signals")
	}
	if c.At.IsZero() {
		c.At = s.now()
	}
	c.At = c.At.UTC()
	c.Reason = strings.TrimSpace(c.Reason)
	cleared, err := s.d.Store.Clear(ctx, c)
	if err != nil {
		return nil, err
	}
	agent := security.AgentPrincipal(c.OrgID, nil)
	for _, r := range cleared {
		s.audit(ctx, agent, "agent.report.resolve", r)
		s.notify(ctx, r)
	}
	return cleared, nil
}

// Open returns the workspace's open reports; projectID "" means every
// project and environmentID "" every environment.
func (s *Service) Open(ctx context.Context, orgID, projectID, environmentID string) ([]*Report, error) {
	if orgID == "" {
		return nil, invalid("no workspace")
	}
	return s.d.Store.Open(ctx, orgID, projectID, environmentID)
}

// Investigating returns the reports whose investigation is queued or
// running, open or not, so the watcher follows one that resolved mid-run.
func (s *Service) Investigating(ctx context.Context, orgID string) ([]*Report, error) {
	if orgID == "" {
		return nil, invalid("no workspace")
	}
	return s.d.Store.Investigating(ctx, orgID)
}

// Lookup reads a report without a person, for detectors and the agent.
func (s *Service) Lookup(ctx context.Context, orgID, id string) (*Report, error) {
	return s.d.Store.Get(ctx, orgID, id)
}

// SetInvestigation moves a report's investigation when it's in one of
// from, and reports whether it did. A finished investigation tells the
// observers.
func (s *Service) SetInvestigation(ctx context.Context, orgID, id string, from []string, inv Investigation) (bool, error) {
	if len(from) == 0 || !slices.Contains(investigations, inv.Status) {
		return false, invalid("an investigation moves from a known status to one")
	}
	for _, f := range from {
		if !slices.Contains(investigations, f) {
			return false, invalid("unknown investigation status %q", f)
		}
	}
	inv.Note = strings.TrimSpace(inv.Note)
	moved, err := s.d.Store.SetInvestigation(ctx, orgID, id, from, inv, s.now())
	if err != nil || !moved {
		return moved, err
	}
	if inv.Status == InvestigationDone || inv.Status == InvestigationFailed {
		r, err := s.d.Store.Get(ctx, orgID, id)
		if err != nil {
			s.warn("agentreports: read report "+id+" for its observers", err)
			return true, nil
		}
		s.notify(ctx, r)
	}
	return true, nil
}

// WriteFindings stores what an investigation found. Only the task that
// investigates the report, or the chat that opened a manual one, may.
func (s *Service) WriteFindings(ctx context.Context, orgID, id string, w Writer, in FindingsInput) (*Report, error) {
	f, err := validFindings(in)
	if err != nil {
		return nil, err
	}
	f.WrittenAt = s.now()
	// The store checks the task in the same write, so an investigation
	// replaced meanwhile can't write.
	return s.d.Store.WriteFindings(ctx, orgID, id, w, f)
}

func validFindings(in FindingsInput) (Findings, error) {
	f := Findings{
		Summary: strings.TrimSpace(in.Summary), LikelyCause: strings.TrimSpace(in.LikelyCause),
		Suggestions: []string{}, Confidence: strings.TrimSpace(in.Confidence),
	}
	if f.Summary == "" || utf8.RuneCountInString(f.Summary) > maxSummaryRunes {
		return f, invalid("the summary is 1 to %d characters", maxSummaryRunes)
	}
	if utf8.RuneCountInString(f.LikelyCause) > maxCauseRunes {
		return f, invalid("the likely cause is at most %d characters", maxCauseRunes)
	}
	if len(in.Suggestions) > maxSuggestions {
		return f, invalid("at most %d suggestions", maxSuggestions)
	}
	for _, sug := range in.Suggestions {
		sug = strings.TrimSpace(sug)
		if sug == "" || utf8.RuneCountInString(sug) > maxSuggestionRunes {
			return f, invalid("each suggestion is 1 to %d characters", maxSuggestionRunes)
		}
		f.Suggestions = append(f.Suggestions, sug)
	}
	if !slices.Contains(confidences, f.Confidence) {
		return f, invalid("confidence is low, medium or high")
	}
	return f, nil
}

// audit records a change to a report: who, which report, and what raised
// it. Never its facts.
func (s *Service) audit(ctx context.Context, p security.Principal, action string, r *Report) {
	if s.d.Audit == nil {
		return
	}
	s.d.Audit.Record(context.WithoutCancel(ctx), projects.AuditEvent{
		Action: action, Severity: "info", ProjectID: r.ProjectID, Principal: p,
		Attributes: map[string]any{"report_id": r.ID, "signal": r.Signal, "component": r.Component, "fingerprint": r.Fingerprint},
	})
}
