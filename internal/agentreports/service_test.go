package agentreports

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/grove/driver"
	"github.com/xraph/shield/scan"

	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// The rules every Store keeps are tested once, as cases on a harness, and
// run against the in-memory store here and against PgStore in
// pgstore_integration_test.go.

const (
	projA   = "proj_a"
	projB   = "proj_b"
	envProd = "env_prod"

	viewer    = "user_viewer"    // reads deployments across the workspace
	developer = "user_developer" // reads and creates them across the workspace
	member    = "user_member"    // reads deployments on projA only, through a team
	outsider  = "user_outsider"  // can't use the agent
)

var base = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeAuthz grants permissions by user: at the workspace ("") or on one
// project. A project grant counts only when the project is passed, as
// security.Authorizer counts a team's.
type fakeAuthz struct {
	grants map[string]map[string][]security.Permission
}

func newFakeAuthz() *fakeAuthz {
	ws := func(perms ...security.Permission) map[string][]security.Permission {
		return map[string][]security.Permission{"": perms}
	}
	return &fakeAuthz{grants: map[string]map[string][]security.Permission{
		viewer:    ws(security.AgentUse, security.DeploymentRead),
		developer: ws(security.AgentUse, security.DeploymentRead, security.DeploymentCreate),
		member: {
			"":    {security.AgentUse},
			projA: {security.DeploymentRead},
		},
	}}
}

func (a *fakeAuthz) has(p security.Principal, perm security.Permission, resource string) bool {
	g := a.grants[p.UserID]
	if slices.Contains(g[""], perm) {
		return true
	}
	return resource != "" && slices.Contains(g[resource], perm)
}

func (a *fakeAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, resource string) error {
	if a.has(p, perm, resource) {
		return nil
	}
	return fmt.Errorf("%w (%s)", security.ErrForbidden, perm.Name())
}

func (a *fakeAuthz) AllowedOn(_ context.Context, p security.Principal, perm security.Permission, projectID string) (bool, error) {
	return a.has(p, perm, projectID), nil
}

type fakeProjects struct{}

func (fakeProjects) List(context.Context, security.Principal) ([]*projects.Project, error) {
	return []*projects.Project{{ID: projA}, {ID: projB}}, nil
}

type fakeAudit struct {
	mu     sync.Mutex
	events []projects.AuditEvent
}

func (a *fakeAudit) Record(_ context.Context, e projects.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func (a *fakeAudit) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.events))
	for _, e := range a.events {
		out = append(out, e.Action)
	}
	return out
}

func (a *fakeAudit) last(action string) (projects.AuditEvent, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.events) - 1; i >= 0; i-- {
		if a.events[i].Action == action {
			return a.events[i], true
		}
	}
	return projects.AuditEvent{}, false
}

type fakeObserver struct {
	mu   sync.Mutex
	docs []KnowledgeDocument
	err  error
}

func (o *fakeObserver) ReportChanged(_ context.Context, doc KnowledgeDocument) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.docs = append(o.docs, doc)
	return o.err
}

func (o *fakeObserver) seen() []KnowledgeDocument {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.docs)
}

// inspector reaches the columns a Store keeps for delivery, which no
// Store method returns until Task 4's.
type inspector interface {
	hidden(t *testing.T, orgID, id string) record
	setIssueNumber(t *testing.T, orgID, id string, n int)
	// noticeSent is what delivery does once a notice went out.
	noticeSent(t *testing.T, orgID, id string, at time.Time)
}

type harness struct {
	org      string
	svc      *Service
	store    Store
	peek     inspector
	clock    *clock
	audit    *fakeAudit
	mu       sync.Mutex
	warnings []string
}

// tenantsOn answers every workspace's model tenant: the agent is on.
type tenantsOn struct{}

func (tenantsOn) NexusTenant(_ context.Context, orgID string) (string, error) {
	return "tenant_" + orgID, nil
}

// passEngine is a safety engine that lets every text through as it is.
type passEngine struct{}

func (passEngine) ScanInput(_ context.Context, in *scan.Input) (*scan.Result, error) {
	return &scan.Result{Decision: scan.DecisionAllow, Redacted: in.Text}, nil
}

func (passEngine) ScanOutput(context.Context, *scan.Input) (*scan.Result, error) {
	return &scan.Result{Decision: scan.DecisionAllow}, nil
}

func newHarness(t *testing.T, store Store, peek inspector, org string) *harness {
	t.Helper()
	h := &harness{org: org, store: store, peek: peek, clock: &clock{now: base}, audit: &fakeAudit{}}
	if m, ok := store.(*memStore); ok {
		m.now = h.clock.Now
	}
	h.svc = NewService(Deps{
		Store: store, Authz: newFakeAuthz(), Projects: fakeProjects{}, Audit: h.audit, Now: h.clock.Now,
		Binder: &agentscope.Binder{Tenants: tenantsOn{}}, Safety: &agentsafety.Scanner{Engine: passEngine{}},
		Warn: func(msg string, err error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.warnings = append(h.warnings, msg+": "+err.Error())
		},
	})
	return h
}

func newMemHarness(t *testing.T) *harness {
	t.Helper()
	m := newMemStore()
	return newHarness(t, m, m, "org_a")
}

func (h *harness) person(userID string) security.Principal {
	return security.Principal{UserID: userID, OrgID: h.org}
}

func (h *harness) signal(kind, component, occurrence string) Signal {
	return Signal{
		OrgID: h.org, ProjectID: projA, EnvironmentID: envProd, Environment: "production",
		Kind: kind, Component: component, Severity: SeverityHigh,
		Title:      "The " + component + " deployment to production failed",
		Facts:      []Fact{{Label: "Deployment", Value: occurrence, Path: "/deployments/" + occurrence}},
		Evidence:   Evidence{DeploymentID: strings.Split(occurrence, "#")[0]},
		Repository: "acme/api", Occurrence: occurrence,
	}
}

func (h *harness) observe(t *testing.T, sig Signal) *Observation {
	t.Helper()
	obs, err := h.svc.Observe(context.Background(), sig)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	return obs
}

func (h *harness) lookup(t *testing.T, id string) *Report {
	t.Helper()
	r, err := h.svc.Lookup(context.Background(), h.org, id)
	if err != nil {
		t.Fatalf("lookup %s: %v", id, err)
	}
	return r
}

func (h *harness) clear(t *testing.T, c Clearance) []*Report {
	t.Helper()
	if c.OrgID == "" {
		c.OrgID, c.ProjectID, c.EnvironmentID = h.org, projA, envProd
	}
	out, err := h.svc.Clear(context.Background(), c)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	return out
}

func (h *harness) warned() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.warnings)
}

type serviceCase struct {
	name string
	run  func(t *testing.T, h *harness)
}

// serviceCases run against every Store.
var serviceCases = []serviceCase{
	{"FirstSignalOpensAReport", firstSignalOpensAReport},
	{"RepeatUpdatesTheSameReport", repeatUpdatesTheSameReport},
	{"RetriesOfOneDeploymentAreOneOccurrence", retriesOfOneDeploymentAreOneOccurrence},
	{"LevelSignalsDoNotCountRepeats", levelSignalsDoNotCountRepeats},
	{"RepeatsQueueANoticeAtMostHourly", repeatsQueueANoticeAtMostHourly},
	{"ClearResolvesMatchingComponentsOnly", clearResolvesMatchingComponentsOnly},
	{"ReopenWithinADayReusesTheReport", reopenWithinADayReusesTheReport},
	{"ManualReportsNeverAutoClear", manualReportsNeverAutoClear},
	{"CloseNeedsDeploymentCreate", closeNeedsDeploymentCreate},
	{"PeopleOnlySeeProjectsTheyCanRead", peopleOnlySeeProjectsTheyCanRead},
	{"FindingsOnlyFromTheInvestigation", findingsOnlyFromTheInvestigation},
	{"InvestigationMovesOnlyFromTheGivenStates", investigationMovesOnlyFromTheGivenStates},
	{"FindingsCommentOnlyWithAnIssue", findingsCommentOnlyWithAnIssue},
	{"InvestigatingFollowsReportsThatResolved", investigatingFollowsReportsThatResolved},
	{"OpenTreatsEmptyAsEvery", openTreatsEmptyAsEvery},
	{"ObserversHearResolvedReports", observersHearResolvedReports},
	{"DocumentsSkipOpenReportsWithoutFindings", documentsSkipOpenReportsWithoutFindings},
	{"ReportsStayInTheirWorkspace", reportsStayInTheirWorkspace},
}

func TestFingerprintIsStable(t *testing.T) {
	if got := Fingerprint("p", "e", "drift", "service:api"); got != "p/e/drift/service:api" {
		t.Fatalf("Fingerprint = %q", got)
	}
	if got := Fingerprint("p", "", "analysis_failing", "source:web"); got != "p/-/analysis_failing/source:web" {
		t.Fatalf("Fingerprint without an environment = %q", got)
	}
}

func TestFirstSignalOpensAReport(t *testing.T) { firstSignalOpensAReport(t, newMemHarness(t)) }
func TestRepeatUpdatesTheSameReport(t *testing.T) {
	repeatUpdatesTheSameReport(t, newMemHarness(t))
}
func TestRetriesOfOneDeploymentAreOneOccurrence(t *testing.T) {
	retriesOfOneDeploymentAreOneOccurrence(t, newMemHarness(t))
}
func TestLevelSignalsDoNotCountRepeats(t *testing.T) {
	levelSignalsDoNotCountRepeats(t, newMemHarness(t))
}
func TestRepeatsQueueANoticeAtMostHourly(t *testing.T) {
	repeatsQueueANoticeAtMostHourly(t, newMemHarness(t))
}
func TestClearResolvesMatchingComponentsOnly(t *testing.T) {
	clearResolvesMatchingComponentsOnly(t, newMemHarness(t))
}
func TestReopenWithinADayReusesTheReport(t *testing.T) {
	reopenWithinADayReusesTheReport(t, newMemHarness(t))
}
func TestManualReportsNeverAutoClear(t *testing.T) {
	manualReportsNeverAutoClear(t, newMemHarness(t))
}
func TestCloseNeedsDeploymentCreate(t *testing.T) {
	closeNeedsDeploymentCreate(t, newMemHarness(t))
}
func TestPeopleOnlySeeProjectsTheyCanRead(t *testing.T) {
	peopleOnlySeeProjectsTheyCanRead(t, newMemHarness(t))
}
func TestFindingsOnlyFromTheInvestigation(t *testing.T) {
	findingsOnlyFromTheInvestigation(t, newMemHarness(t))
}
func TestInvestigationMovesOnlyFromTheGivenStates(t *testing.T) {
	investigationMovesOnlyFromTheGivenStates(t, newMemHarness(t))
}
func TestFindingsCommentOnlyWithAnIssue(t *testing.T) {
	findingsCommentOnlyWithAnIssue(t, newMemHarness(t))
}
func TestInvestigatingFollowsReportsThatResolved(t *testing.T) {
	investigatingFollowsReportsThatResolved(t, newMemHarness(t))
}
func TestOpenTreatsEmptyAsEvery(t *testing.T) { openTreatsEmptyAsEvery(t, newMemHarness(t)) }
func TestObserversHearResolvedReports(t *testing.T) {
	observersHearResolvedReports(t, newMemHarness(t))
}
func TestDocumentsSkipOpenReportsWithoutFindings(t *testing.T) {
	documentsSkipOpenReportsWithoutFindings(t, newMemHarness(t))
}
func TestReportsStayInTheirWorkspace(t *testing.T) {
	reportsStayInTheirWorkspace(t, newMemHarness(t))
}

func TestObserveRefusesAnIncompleteSignal(t *testing.T) {
	h := newMemHarness(t)
	for name, change := range map[string]func(*Signal){
		"no org":       func(s *Signal) { s.OrgID = "" },
		"no project":   func(s *Signal) { s.ProjectID = "" },
		"no component": func(s *Signal) { s.Component = "" },
		"no title":     func(s *Signal) { s.Title = " " },
		"long title":   func(s *Signal) { s.Title = strings.Repeat("x", 201) },
		"bad kind":     func(s *Signal) { s.Kind = "bored" },
		"bad severity": func(s *Signal) { s.Severity = "urgent" },
	} {
		sig := h.signal(SignalDeploymentFailed, "service:api", "dep_1#0")
		change(&sig)
		if _, err := h.svc.Observe(context.Background(), sig); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestListAndGetNeedAgentUse(t *testing.T) {
	h := newMemHarness(t)
	obs := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0"))
	ctx := context.Background()
	if _, err := h.svc.List(ctx, h.person(outsider), ListFilter{}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("List without agent:use: %v", err)
	}
	if _, err := h.svc.Get(ctx, h.person(outsider), obs.Report.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("Get without agent:use: %v", err)
	}
}

func firstSignalOpensAReport(t *testing.T, h *harness) {
	obs := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0"))
	r := obs.Report
	if obs.Change != ChangeOpened || r.Occurrences != 1 || r.Revision != 1 || r.Status != StatusOpen {
		t.Fatalf("opened: change %s, occurrences %d, revision %d, status %s", obs.Change, r.Occurrences, r.Revision, r.Status)
	}
	if !strings.HasPrefix(r.ID, "arep_") || r.Fingerprint != "proj_a/env_prod/deployment_failed/service:api" {
		t.Fatalf("id %q, fingerprint %q", r.ID, r.Fingerprint)
	}
	if r.Investigation != InvestigationNone || r.Issue.State != IssueNone || !r.FirstSeenAt.Equal(base) || !r.LastSeenAt.Equal(base) {
		t.Fatalf("new report: %+v", r)
	}
	if len(r.Facts) != 1 || r.Evidence.DeploymentID != "dep_1" || r.Repository != "acme/api" {
		t.Fatalf("facts %+v, evidence %+v, repository %q", r.Facts, r.Evidence, r.Repository)
	}
	got := h.lookup(t, r.ID)
	if got.ID != r.ID || got.Title != r.Title || got.Severity != SeverityHigh {
		t.Fatalf("read back %+v", got)
	}
	hidden := h.peek.hidden(t, h.org, r.ID)
	if hidden.PendingNotice != "opened" || !slices.Equal(hidden.OccurrenceKeys, []string{"dep_1#0"}) || len(hidden.IssueComments) != 0 {
		t.Fatalf("pending notice %q, keys %v, comments %v", hidden.PendingNotice, hidden.OccurrenceKeys, hidden.IssueComments)
	}
	e, ok := h.audit.last("agent.report.open")
	if !ok || !e.Principal.IsAgent() || e.Principal.OrgID != h.org || e.ProjectID != projA {
		t.Fatalf("audit: %+v", e)
	}
	if e.Attributes["report_id"] != r.ID || e.Attributes["signal"] != SignalDeploymentFailed ||
		e.Attributes["component"] != "service:api" || e.Attributes["fingerprint"] != r.Fingerprint {
		t.Fatalf("audit attributes: %v", e.Attributes)
	}
}

func repeatUpdatesTheSameReport(t *testing.T, h *harness) {
	first := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0"))
	h.clock.advance(time.Minute)
	second := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_2#0"))
	r := second.Report
	if second.Change != ChangeRepeated || r.ID != first.Report.ID || r.Occurrences != 2 || r.Revision != 2 {
		t.Fatalf("repeat: change %s, same id %v, occurrences %d, revision %d", second.Change, r.ID == first.Report.ID, r.Occurrences, r.Revision)
	}
	if r.Evidence.DeploymentID != "dep_2" || r.Facts[0].Value != "dep_2#0" || !r.LastSeenAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("facts and evidence come from the latest signal: %+v %+v, last seen %v", r.Facts, r.Evidence, r.LastSeenAt)
	}
	hidden := h.peek.hidden(t, h.org, r.ID)
	if !slices.Equal(hidden.IssueComments, []string{"repeated"}) {
		t.Fatalf("comments %v", hidden.IssueComments)
	}
	if hidden.PendingNotice != "opened" {
		t.Fatalf("an undelivered opened notice is kept, got %q", hidden.PendingNotice)
	}
	if got := h.audit.actions(); !slices.Equal(got, []string{"agent.report.open"}) {
		t.Fatalf("a repeat isn't audited: %v", got)
	}
}

func retriesOfOneDeploymentAreOneOccurrence(t *testing.T, h *harness) {
	first := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0"))
	h.clock.advance(time.Minute)
	again := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0"))
	if again.Change != ChangeUnchanged || again.Report.Occurrences != 1 || again.Report.Revision != 1 {
		t.Fatalf("same occurrence: change %s, occurrences %d, revision %d", again.Change, again.Report.Occurrences, again.Report.Revision)
	}
	if !h.lookup(t, first.Report.ID).LastSeenAt.Equal(base) {
		t.Fatal("an occurrence already counted writes nothing")
	}
	retry := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#1"))
	if retry.Change != ChangeRepeated || retry.Report.Occurrences != 2 || retry.Report.ID != first.Report.ID {
		t.Fatalf("a retry: change %s, occurrences %d", retry.Change, retry.Report.Occurrences)
	}
}

func levelSignalsDoNotCountRepeats(t *testing.T, h *harness) {
	var id string
	for i := range 3 {
		obs := h.observe(t, h.signal(SignalDrift, "service:api", ""))
		if i == 0 {
			id = obs.Report.ID
			if obs.Change != ChangeOpened {
				t.Fatalf("first: %s", obs.Change)
			}
		} else if obs.Change != ChangeUnchanged || obs.Report.ID != id {
			t.Fatalf("level signal %d: change %s", i, obs.Change)
		}
		h.clock.advance(10 * time.Minute)
	}
	r := h.lookup(t, id)
	if r.Occurrences != 1 || r.Revision != 1 || !r.LastSeenAt.Equal(base.Add(20*time.Minute)) {
		t.Fatalf("occurrences %d, revision %d, last seen %v", r.Occurrences, r.Revision, r.LastSeenAt)
	}
	open, err := h.svc.Open(context.Background(), h.org, projA, envProd)
	if err != nil || len(open) != 1 {
		t.Fatalf("open: %d, %v", len(open), err)
	}
	if keys := h.peek.hidden(t, h.org, id).OccurrenceKeys; len(keys) != 0 {
		t.Fatalf("keys %v", keys)
	}
}

func repeatsQueueANoticeAtMostHourly(t *testing.T, h *harness) {
	id := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0")).Report.ID
	h.peek.noticeSent(t, h.org, id, base)
	h.clock.advance(10 * time.Minute)
	h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_2#0"))
	if got := h.peek.hidden(t, h.org, id); got.PendingNotice != "" || !slices.Equal(got.IssueComments, []string{"repeated"}) {
		t.Fatalf("within the hour: notice %q, comments %v", got.PendingNotice, got.IssueComments)
	}
	h.clock.advance(time.Minute)
	h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_3#0"))
	if got := h.peek.hidden(t, h.org, id); !slices.Equal(got.IssueComments, []string{"repeated"}) {
		t.Fatalf("one pending repeat comment at a time: %v", got.IssueComments)
	}
	h.clock.advance(time.Hour)
	obs := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_4#0"))
	if got := h.peek.hidden(t, h.org, id); got.PendingNotice != "repeated" {
		t.Fatalf("after the hour: notice %q", got.PendingNotice)
	}
	if obs.Report.Occurrences != 4 {
		t.Fatalf("occurrences %d", obs.Report.Occurrences)
	}
}

func clearResolvesMatchingComponentsOnly(t *testing.T, h *harness) {
	api := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0")).Report
	web := h.observe(t, h.signal(SignalDeploymentFailed, "service:web", "dep_2#0")).Report
	h.clock.advance(time.Minute)
	reason := "A later deployment (dep_9) succeeded"
	cleared := h.clear(t, Clearance{OrgID: h.org, ProjectID: projA, EnvironmentID: envProd,
		Signals: []string{SignalDeploymentFailed}, Components: []string{"service:api"}, Reason: reason})
	if len(cleared) != 1 || cleared[0].ID != api.ID {
		t.Fatalf("cleared %d reports", len(cleared))
	}
	r := h.lookup(t, api.ID)
	if r.Status != StatusResolved || r.Resolution != reason || r.ResolvedAt == nil || !r.ResolvedAt.Equal(base.Add(time.Minute)) || r.Revision != 2 {
		t.Fatalf("resolved: %+v", r)
	}
	if got := h.peek.hidden(t, h.org, api.ID).IssueComments; !slices.Equal(got, []string{"resolved"}) {
		t.Fatalf("comments %v", got)
	}
	if h.lookup(t, web.ID).Status != StatusOpen {
		t.Fatal("web was cleared too")
	}
	e, ok := h.audit.last("agent.report.resolve")
	if !ok || !e.Principal.IsAgent() || e.Attributes["report_id"] != api.ID {
		t.Fatalf("audit %+v", e)
	}
	// Another environment, or another signal, clears nothing.
	if n := len(h.clear(t, Clearance{OrgID: h.org, ProjectID: projA, EnvironmentID: "env_stage", Signals: []string{SignalDeploymentFailed}})); n != 0 {
		t.Fatalf("another environment cleared %d", n)
	}
	if n := len(h.clear(t, Clearance{OrgID: h.org, ProjectID: projA, EnvironmentID: envProd, Signals: []string{SignalDrift}})); n != 0 {
		t.Fatalf("another signal cleared %d", n)
	}
	// Nil components clear every component of the kind.
	if n := len(h.clear(t, Clearance{OrgID: h.org, ProjectID: projA, EnvironmentID: envProd, Signals: []string{SignalDeploymentFailed}})); n != 1 {
		t.Fatalf("every component cleared %d", n)
	}
	if _, err := h.svc.Clear(context.Background(), Clearance{OrgID: h.org, ProjectID: projA}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no signals: %v", err)
	}
}

func reopenWithinADayReusesTheReport(t *testing.T, h *harness) {
	first := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0")).Report
	if ok, err := h.svc.SetInvestigation(context.Background(), h.org, first.ID, []string{InvestigationNone},
		Investigation{Status: InvestigationQueued, TaskID: "atask_1"}); !ok || err != nil {
		t.Fatalf("queue: %v %v", ok, err)
	}
	h.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}, Reason: "fixed"})
	h.clock.advance(23 * time.Hour)
	back := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_5#0"))
	r := back.Report
	if back.Change != ChangeReopened || r.ID != first.ID {
		t.Fatalf("23h later: change %s, same id %v", back.Change, r.ID == first.ID)
	}
	if r.Status != StatusOpen || r.ResolvedAt != nil || r.Resolution != "" || r.ClosedBy != "" || r.Occurrences != 2 ||
		r.Revision != 3 || r.Investigation != InvestigationNone || r.TakenBy != "" || r.TakenAt != nil {
		t.Fatalf("reopened: %+v", r)
	}
	if r.Evidence.DeploymentID != "dep_5" {
		t.Fatalf("evidence %+v", r.Evidence)
	}
	hidden := h.peek.hidden(t, h.org, r.ID)
	if hidden.PendingNotice != "reopened" || !slices.Equal(hidden.IssueComments, []string{"resolved", "reopened"}) {
		t.Fatalf("notice %q, comments %v", hidden.PendingNotice, hidden.IssueComments)
	}
	if _, ok := h.audit.last("agent.report.reopen"); !ok {
		t.Fatal("reopen not audited")
	}

	// A person's close reopens the same way.
	if _, err := h.svc.Close(context.Background(), h.person(developer), r.ID, CloseInput{Reason: "not now"}); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(time.Hour)
	again := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_6#0"))
	if again.Change != ChangeReopened || again.Report.ID != first.ID || again.Report.ClosedBy != "" {
		t.Fatalf("after a close: change %s, closed by %q", again.Change, again.Report.ClosedBy)
	}

	h.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}, Reason: "fixed"})
	h.clock.advance(25 * time.Hour)
	later := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_7#0"))
	if later.Change != ChangeOpened || later.Report.ID == first.ID || later.Report.Occurrences != 1 {
		t.Fatalf("25h later: change %s, new id %v", later.Change, later.Report.ID != first.ID)
	}
}

func manualReportsNeverAutoClear(t *testing.T, h *harness) {
	sig := h.signal(SignalManual, "chat", "")
	sig.OpenedBy, sig.OpenedByTask = developer, "atask_chat"
	r := h.observe(t, sig).Report
	if r.OpenedBy != developer || r.OpenedByTask != "atask_chat" {
		t.Fatalf("opened by %q, task %q", r.OpenedBy, r.OpenedByTask)
	}
	if n := len(h.clear(t, Clearance{Signals: []string{SignalManual, SignalDeploymentFailed}, Reason: "all good"})); n != 0 {
		t.Fatalf("cleared %d manual reports", n)
	}
	if h.lookup(t, r.ID).Status != StatusOpen {
		t.Fatal("a manual report cleared")
	}
}

func closeNeedsDeploymentCreate(t *testing.T, h *harness) {
	ctx := context.Background()
	r := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0")).Report
	if _, err := h.svc.Close(ctx, h.person(viewer), r.ID, CloseInput{}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("viewer: %v", err)
	}
	if _, err := h.svc.Close(ctx, h.person(developer), r.ID, CloseInput{Reason: strings.Repeat("x", 501)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long reason: %v", err)
	}
	h.clock.advance(time.Minute)
	closed, err := h.svc.Close(ctx, h.person(developer), r.ID, CloseInput{Reason: "Fixed by hand"})
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != StatusClosed || closed.ClosedBy != developer || closed.Resolution != "Fixed by hand" ||
		closed.ResolvedAt == nil || !closed.ResolvedAt.Equal(base.Add(time.Minute)) || closed.Revision != 2 {
		t.Fatalf("closed: %+v", closed)
	}
	if got := h.peek.hidden(t, h.org, r.ID).IssueComments; !slices.Equal(got, []string{"closed"}) {
		t.Fatalf("comments %v", got)
	}
	if _, err := h.svc.Close(ctx, h.person(developer), r.ID, CloseInput{}); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("closing twice: %v", err)
	}
	e, ok := h.audit.last("agent.report.close")
	if !ok || e.Principal.UserID != developer || e.Principal.IsAgent() || e.Attributes["report_id"] != r.ID {
		t.Fatalf("audit %+v", e)
	}
	if _, err := h.svc.Close(ctx, h.person(developer), "arep_missing", CloseInput{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func peopleOnlySeeProjectsTheyCanRead(t *testing.T, h *harness) {
	ctx := context.Background()
	inA := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0")).Report
	sig := h.signal(SignalDeploymentFailed, "service:api", "dep_2#0")
	sig.ProjectID = projB
	inB := h.observe(t, sig).Report

	list, err := h.svc.List(ctx, h.person(member), ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Reports) != 1 || list.Reports[0].ID != inA.ID {
		t.Fatalf("member sees %d reports", len(list.Reports))
	}
	if _, err = h.svc.Get(ctx, h.person(member), inB.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member reads projB's report: %v", err)
	}
	if got, gerr := h.svc.Get(ctx, h.person(member), inA.ID); gerr != nil || got.ID != inA.ID {
		t.Fatalf("member reads projA's report: %v", gerr)
	}
	if list, err = h.svc.List(ctx, h.person(member), ListFilter{ProjectID: projB}); err != nil || len(list.Reports) != 0 {
		t.Fatalf("member lists projB: %d %v", len(list.Reports), err)
	}
	if list, err = h.svc.List(ctx, h.person(viewer), ListFilter{}); err != nil || len(list.Reports) != 2 {
		t.Fatalf("viewer lists: %d %v", len(list.Reports), err)
	}
	if list.Reports[0].ID != inB.ID {
		t.Fatal("newest first")
	}
	if list, err = h.svc.List(ctx, h.person(viewer), ListFilter{Limit: 1}); err != nil || len(list.Reports) != 1 {
		t.Fatalf("limit: %d %v", len(list.Reports), err)
	}
	h.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}})
	if list, err = h.svc.List(ctx, h.person(viewer), ListFilter{}); err != nil || len(list.Reports) != 1 || list.Reports[0].ID != inB.ID {
		t.Fatalf("open by default: %d %v", len(list.Reports), err)
	}
	if list, err = h.svc.List(ctx, h.person(viewer), ListFilter{Status: StatusResolved}); err != nil || len(list.Reports) != 1 || list.Reports[0].ID != inA.ID {
		t.Fatalf("resolved: %d %v", len(list.Reports), err)
	}
	if list, err = h.svc.List(ctx, h.person(viewer), ListFilter{Status: "all"}); err != nil || len(list.Reports) != 2 {
		t.Fatalf("all: %d %v", len(list.Reports), err)
	}
	for _, f := range []ListFilter{{Status: "everything"}, {Limit: 101}, {Limit: -1}} {
		if _, err := h.svc.List(ctx, h.person(viewer), f); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", f, err)
		}
	}
}

func findingsOnlyFromTheInvestigation(t *testing.T, h *harness) {
	ctx := context.Background()
	r := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0")).Report
	if ok, err := h.svc.SetInvestigation(ctx, h.org, r.ID, []string{InvestigationNone},
		Investigation{Status: InvestigationQueued, TaskID: "atask_1", RunID: "arun_1"}); !ok || err != nil {
		t.Fatalf("queue: %v %v", ok, err)
	}
	in := FindingsInput{Summary: "The API ran out of memory on start.", LikelyCause: "A new cache.",
		Suggestions: []string{"Raise the memory limit."}, Confidence: "medium"}
	if _, err := h.svc.WriteFindings(ctx, h.org, r.ID, "atask_2", in); !errors.Is(err, ErrNotYourReport) {
		t.Fatalf("another task: %v", err)
	}
	if _, err := h.svc.WriteFindings(ctx, h.org, r.ID, "", in); !errors.Is(err, ErrNotYourReport) {
		t.Fatalf("no task: %v", err)
	}
	for name, bad := range map[string]FindingsInput{
		"long summary":     {Summary: strings.Repeat("x", 2001), Confidence: "low"},
		"empty summary":    {Summary: " ", Confidence: "low"},
		"long cause":       {Summary: "s", LikelyCause: strings.Repeat("x", 2001), Confidence: "low"},
		"six suggestions":  {Summary: "s", Suggestions: []string{"a", "b", "c", "d", "e", "f"}, Confidence: "low"},
		"empty suggestion": {Summary: "s", Suggestions: []string{""}, Confidence: "low"},
		"long suggestion":  {Summary: "s", Suggestions: []string{strings.Repeat("x", 501)}, Confidence: "low"},
		"confidence":       {Summary: "s", Confidence: "certain"},
	} {
		if _, err := h.svc.WriteFindings(ctx, h.org, r.ID, "atask_1", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	h.clock.advance(time.Minute)
	got, err := h.svc.WriteFindings(ctx, h.org, r.ID, "atask_1", in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Findings == nil || got.Findings.Summary != in.Summary || got.Findings.Confidence != "medium" ||
		!slices.Equal(got.Findings.Suggestions, in.Suggestions) || !got.Findings.WrittenAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("findings %+v", got.Findings)
	}
	if read := h.lookup(t, r.ID); read.Findings == nil || read.Findings.LikelyCause != "A new cache." || read.RunID != "arun_1" {
		t.Fatalf("read back %+v", read.Findings)
	}
	if _, err := h.svc.WriteFindings(ctx, h.org, "arep_missing", "atask_1", in); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}

	// A manual report takes findings from the chat that opened it.
	sig := h.signal(SignalManual, "chat", "")
	sig.OpenedBy, sig.OpenedByTask = developer, "atask_chat"
	manual := h.observe(t, sig).Report
	if _, err := h.svc.WriteFindings(ctx, h.org, manual.ID, "atask_chat", in); err != nil {
		t.Fatalf("manual: %v", err)
	}
}

func investigationMovesOnlyFromTheGivenStates(t *testing.T, h *harness) {
	ctx := context.Background()
	obs := h.svc.AddObserver
	o := &fakeObserver{}
	obs(o)
	r := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0")).Report
	move := func(from []string, inv Investigation) bool {
		t.Helper()
		ok, err := h.svc.SetInvestigation(ctx, h.org, r.ID, from, inv)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !move([]string{InvestigationNone}, Investigation{Status: InvestigationQueued}) {
		t.Fatal("none to queued")
	}
	if move([]string{InvestigationNone}, Investigation{Status: InvestigationQueued, TaskID: "atask_2"}) {
		t.Fatal("queued twice")
	}
	h.clock.advance(time.Second)
	if !move([]string{InvestigationQueued}, Investigation{Status: InvestigationQueued, TaskID: "atask_1"}) {
		t.Fatal("record the task")
	}
	got := h.lookup(t, r.ID)
	if got.TaskID != "atask_1" || got.Investigation != InvestigationQueued || got.InvestigationStartedAt == nil ||
		!got.InvestigationStartedAt.Equal(base.Add(time.Second)) || got.Revision != 1 {
		t.Fatalf("queued: %+v", got)
	}
	if !move([]string{InvestigationQueued}, Investigation{Status: InvestigationRunning, RunID: "arun_1"}) {
		t.Fatal("queued to running")
	}
	if got = h.lookup(t, r.ID); got.TaskID != "atask_1" || got.RunID != "arun_1" {
		t.Fatalf("running keeps the task: %+v", got)
	}
	if len(o.seen()) != 0 {
		t.Fatal("observers hear only finished investigations")
	}
	if !move([]string{InvestigationRunning}, Investigation{Status: InvestigationFailed, Note: "The run hit its token limit."}) {
		t.Fatal("running to failed")
	}
	got = h.lookup(t, r.ID)
	if got.Investigation != InvestigationFailed || got.InvestigationNote != "The run hit its token limit." || got.Revision != 2 {
		t.Fatalf("failed: %+v", got)
	}
	if len(o.seen()) != 1 {
		t.Fatalf("observers after failed: %d", len(o.seen()))
	}
	if _, err := h.svc.SetInvestigation(ctx, h.org, r.ID, []string{InvestigationFailed}, Investigation{Status: "thinking"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad status: %v", err)
	}
	if _, err := h.svc.SetInvestigation(ctx, h.org, r.ID, nil, Investigation{Status: InvestigationQueued}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no from: %v", err)
	}
	if ok, err := h.svc.SetInvestigation(ctx, h.org, "arep_missing", []string{InvestigationNone}, Investigation{Status: InvestigationQueued}); ok || err != nil {
		t.Fatalf("missing: %v %v", ok, err)
	}
}

func findingsCommentOnlyWithAnIssue(t *testing.T, h *harness) {
	ctx := context.Background()
	o := &fakeObserver{}
	h.svc.AddObserver(o)
	without := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0")).Report
	with := h.observe(t, h.signal(SignalDeploymentFailed, "service:web", "dep_2#0")).Report
	h.peek.setIssueNumber(t, h.org, with.ID, 7)
	for _, id := range []string{without.ID, with.ID} {
		if ok, err := h.svc.SetInvestigation(ctx, h.org, id, []string{InvestigationNone}, Investigation{Status: InvestigationDone}); !ok || err != nil {
			t.Fatalf("done: %v %v", ok, err)
		}
	}
	if got := h.peek.hidden(t, h.org, without.ID).IssueComments; len(got) != 0 {
		t.Fatalf("no issue yet, comments %v", got)
	}
	if got := h.peek.hidden(t, h.org, with.ID).IssueComments; !slices.Equal(got, []string{"findings"}) {
		t.Fatalf("with an issue, comments %v", got)
	}
	if r := h.lookup(t, with.ID); r.Revision != 2 || r.Investigation != InvestigationDone {
		t.Fatalf("done: %+v", r)
	}
	if n := len(o.seen()); n != 2 {
		t.Fatalf("observers after done: %d", n)
	}
	third := h.observe(t, h.signal(SignalDeploymentFailed, "service:db", "dep_3#0")).Report
	h.peek.setIssueNumber(t, h.org, third.ID, 8)
	if ok, err := h.svc.SetInvestigation(ctx, h.org, third.ID, []string{InvestigationNone}, Investigation{Status: InvestigationFailed}); !ok || err != nil {
		t.Fatalf("failed: %v %v", ok, err)
	}
	if got := h.peek.hidden(t, h.org, third.ID).IssueComments; len(got) != 0 {
		t.Fatalf("a failed investigation comments %v", got)
	}
}

func investigatingFollowsReportsThatResolved(t *testing.T, h *harness) {
	ctx := context.Background()
	r := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0")).Report
	idle := h.observe(t, h.signal(SignalDeploymentFailed, "service:web", "dep_2#0")).Report
	if ok, err := h.svc.SetInvestigation(ctx, h.org, r.ID, []string{InvestigationNone}, Investigation{Status: InvestigationRunning, TaskID: "atask_1"}); !ok || err != nil {
		t.Fatalf("running: %v %v", ok, err)
	}
	h.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}, Components: []string{"service:api"}})
	got, err := h.svc.Investigating(ctx, h.org)
	if err != nil || len(got) != 1 || got[0].ID != r.ID || got[0].Status != StatusResolved {
		t.Fatalf("investigating: %d %v", len(got), err)
	}
	open, err := h.svc.Open(ctx, h.org, "", "")
	if err != nil || len(open) != 1 || open[0].ID != idle.ID {
		t.Fatalf("open: %d %v", len(open), err)
	}
}

func openTreatsEmptyAsEvery(t *testing.T, h *harness) {
	ctx := context.Background()
	h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0"))
	stage := h.signal(SignalDeploymentFailed, "service:api", "dep_2#0")
	stage.EnvironmentID, stage.Environment = "env_stage", "staging"
	h.observe(t, stage)
	other := h.signal(SignalDeploymentFailed, "service:api", "dep_3#0")
	other.ProjectID = projB
	h.observe(t, other)
	for _, c := range []struct {
		project, env string
		want         int
	}{{"", "", 3}, {projA, "", 2}, {projA, envProd, 1}, {"", "env_stage", 1}, {projB, envProd, 1}} {
		got, err := h.svc.Open(ctx, h.org, c.project, c.env)
		if err != nil || len(got) != c.want {
			t.Errorf("Open(%q, %q) = %d, %v; want %d", c.project, c.env, len(got), err, c.want)
		}
	}
	if _, err := h.svc.Open(ctx, "", "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no org: %v", err)
	}
}

func observersHearResolvedReports(t *testing.T, h *harness) {
	ctx := context.Background()
	o := &fakeObserver{}
	h.svc.AddObserver(o)
	r := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0")).Report
	h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_2#0"))
	if n := len(o.seen()); n != 0 {
		t.Fatalf("observers heard an open report %d times", n)
	}
	h.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}, Reason: "A later deployment (dep_9) succeeded"})
	docs := o.seen()
	if len(docs) != 1 {
		t.Fatalf("observers heard %d", len(docs))
	}
	doc := docs[0]
	if doc.SourceKey != "agent-report:"+r.ID || doc.OrgID != h.org || doc.ProjectID != projA || doc.Title != r.Title ||
		doc.Metadata["status"] != StatusResolved || doc.Metadata["signal"] != SignalDeploymentFailed ||
		doc.Metadata["component"] != "service:api" || doc.Metadata["environment"] != "production" ||
		doc.Metadata["severity"] != SeverityHigh || doc.Metadata["project_id"] != projA {
		t.Fatalf("document %+v", doc)
	}
	if !strings.Contains(doc.Text, r.Title) || doc.UpdatedAt.IsZero() {
		t.Fatalf("text %q", doc.Text)
	}

	// A reopen tells them too, and an observer's error is only logged.
	o.err = errors.New("knowledge is down")
	h.clock.advance(time.Hour)
	back := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_3#0"))
	if back.Change != ChangeReopened {
		t.Fatalf("change %s", back.Change)
	}
	if docs = o.seen(); len(docs) != 2 || docs[1].Metadata["status"] != StatusOpen {
		t.Fatalf("reopen: %d documents", len(docs))
	}
	if len(h.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}})) != 1 {
		t.Fatal("clear failed with a failing observer")
	}
	if _, err := h.svc.Close(ctx, h.person(developer), h.observe(t, h.signal(SignalDeploymentFailed, "service:web", "dep_4#0")).Report.ID, CloseInput{}); err != nil {
		t.Fatal(err)
	}
	if n := len(o.seen()); n != 4 {
		t.Fatalf("after close: %d documents", n)
	}
	if w := h.warned(); len(w) != 3 || !strings.Contains(w[0], "knowledge is down") {
		t.Fatalf("warnings %v", w)
	}
}

func documentsSkipOpenReportsWithoutFindings(t *testing.T, h *harness) {
	ctx := context.Background()
	h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0"))
	h.clock.advance(time.Minute)
	withFindings := h.observe(t, h.signal(SignalDeploymentFailed, "service:web", "dep_2#0")).Report
	resolved := h.observe(t, h.signal(SignalDeploymentFailed, "service:db", "dep_3#0")).Report
	if ok, err := h.svc.SetInvestigation(ctx, h.org, withFindings.ID, []string{InvestigationNone}, Investigation{Status: InvestigationRunning, TaskID: "atask_1"}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	h.clock.advance(time.Minute)
	h.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}, Components: []string{"service:db"}, Reason: "fixed"})
	h.clock.advance(time.Minute)
	if _, err := h.svc.WriteFindings(ctx, h.org, withFindings.ID, "atask_1", FindingsInput{Summary: "Out of memory.", Confidence: "high"}); err != nil {
		t.Fatal(err)
	}
	docs, err := h.svc.Documents(ctx, h.org, time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0].SourceKey != "agent-report:"+resolved.ID || docs[1].SourceKey != "agent-report:"+withFindings.ID {
		t.Fatalf("documents %+v", docs)
	}
	if !docs[0].UpdatedAt.Before(docs[1].UpdatedAt) {
		t.Fatal("oldest first")
	}
	if docs, err = h.svc.Documents(ctx, h.org, docs[0].UpdatedAt, 10); err != nil || len(docs) != 1 {
		t.Fatalf("after: %d %v", len(docs), err)
	}
	if docs, err = h.svc.Documents(ctx, h.org, time.Time{}, 1); err != nil || len(docs) != 1 {
		t.Fatalf("limit: %d %v", len(docs), err)
	}
	for _, limit := range []int{0, 201} {
		if _, err := h.svc.Documents(ctx, h.org, time.Time{}, limit); !errors.Is(err, ErrInvalid) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
}

func reportsStayInTheirWorkspace(t *testing.T, h *harness) {
	ctx := context.Background()
	r := h.observe(t, h.signal(SignalDeploymentFailed, "service:api", "dep_1#0")).Report
	otherOrg := h.org + "_other"
	other := h.signal(SignalDeploymentFailed, "service:api", "dep_1#0")
	other.OrgID = otherOrg
	if obs := h.observe(t, other); obs.Change != ChangeOpened || obs.Report.ID == r.ID {
		t.Fatalf("another workspace's signal: %s", obs.Change)
	}
	if _, err := h.svc.Lookup(ctx, otherOrg, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("lookup across workspaces: %v", err)
	}
	stranger := security.Principal{UserID: viewer, OrgID: otherOrg}
	if _, err := h.svc.Get(ctx, stranger, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get across workspaces: %v", err)
	}
	if n := len(h.clear(t, Clearance{OrgID: otherOrg, ProjectID: projA, EnvironmentID: envProd, Signals: []string{SignalDeploymentFailed}})); n != 1 {
		t.Fatalf("clear in the other workspace resolved %d", n)
	}
	if h.lookup(t, r.ID).Status != StatusOpen {
		t.Fatal("clear crossed workspaces")
	}
	if _, err := h.svc.WriteFindings(ctx, otherOrg, r.ID, "atask_1", FindingsInput{Summary: "s", Confidence: "low"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("findings across workspaces: %v", err)
	}
}

// memStore is the in-memory Store: the same rules as PgStore, under one
// lock.
type memStore struct {
	mu      sync.Mutex
	records map[string]*record
	now     func() time.Time
	// notices is where SaveNotice queues notices, as the database
	// transaction would; nil drops them.
	notices notifications.Execer
	// queued records when each delivery was queued, and claims who
	// holds it until when.
	queued map[string]time.Time
	claims map[string]memClaim
}

type memClaim struct {
	owner string
	until time.Time
}

func newMemStore() *memStore {
	return &memStore{records: map[string]*record{}, now: time.Now, queued: map[string]time.Time{}, claims: map[string]memClaim{}}
}

var _ Store = (*memStore)(nil)

func (m *memStore) find(orgID, id string) (*record, error) {
	r, ok := m.records[id]
	if !ok || r.OrgID != orgID {
		return nil, ErrNotFound
	}
	return r, nil
}

func (m *memStore) Get(_ context.Context, orgID, id string) (*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if err != nil {
		return nil, err
	}
	return r.report(), nil
}

// sorted returns the workspace's records that match keep, by less.
func (m *memStore) sorted(orgID string, keep func(*record) bool, less func(a, b *record) int) []*Report {
	var rs []*record
	for _, r := range m.records {
		if r.OrgID == orgID && keep(r) {
			rs = append(rs, r)
		}
	}
	slices.SortFunc(rs, less)
	out := make([]*Report, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.report())
	}
	return out
}

func newestFirst(a, b *record) int {
	if c := b.LastSeenAt.Compare(a.LastSeenAt); c != 0 {
		return c
	}
	return strings.Compare(b.ID, a.ID)
}

func oldestFirst(a, b *record) int {
	if c := a.FirstSeenAt.Compare(b.FirstSeenAt); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

func (m *memStore) List(_ context.Context, orgID string, projectIDs []string, f ListFilter) ([]*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.sorted(orgID, func(r *record) bool {
		return slices.Contains(projectIDs, r.ProjectID) && (f.Status == "all" || r.Status == f.Status)
	}, newestFirst)
	if len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (m *memStore) Open(_ context.Context, orgID, projectID, environmentID string) ([]*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sorted(orgID, func(r *record) bool {
		return r.Status == StatusOpen && (projectID == "" || r.ProjectID == projectID) &&
			(environmentID == "" || r.EnvironmentID == environmentID)
	}, oldestFirst), nil
}

func (m *memStore) Investigating(_ context.Context, orgID string) ([]*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sorted(orgID, func(r *record) bool {
		return r.Investigation == InvestigationQueued || r.Investigation == InvestigationRunning
	}, oldestFirst), nil
}

func (m *memStore) Observe(_ context.Context, sig Signal, reopenAfter, now time.Time) (*Observation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fp := Fingerprint(sig.ProjectID, sig.EnvironmentID, sig.Kind, sig.Component)
	var latest *record
	for _, r := range m.records {
		if r.OrgID != sig.OrgID || r.Fingerprint != fp {
			continue
		}
		if r.Status == StatusOpen {
			change, _ := r.repeat(sig, now)
			return &Observation{Report: r.report(), Change: change}, nil
		}
		if r.ResolvedAt != nil && !r.ResolvedAt.Before(reopenAfter) && (latest == nil || r.ResolvedAt.After(*latest.ResolvedAt)) {
			latest = r
		}
	}
	if latest != nil {
		latest.reopen(sig, now)
		return &Observation{Report: latest.report(), Change: ChangeReopened}, nil
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	r := newRecord(id, sig, now)
	m.records[id] = r
	return &Observation{Report: r.report(), Change: ChangeOpened}, nil
}

func (m *memStore) Clear(_ context.Context, c Clearance) ([]*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var cleared []*record
	for _, r := range m.records {
		if r.OrgID == c.OrgID && r.ProjectID == c.ProjectID && r.EnvironmentID == c.EnvironmentID && r.Status == StatusOpen &&
			slices.Contains(c.Signals, r.Signal) && (c.Components == nil || slices.Contains(c.Components, r.Component)) && !r.manual() {
			r.Status, r.Resolution = StatusResolved, c.Reason
			r.settle(c.At, "resolved")
			cleared = append(cleared, r)
		}
	}
	slices.SortFunc(cleared, oldestFirst)
	out := make([]*Report, 0, len(cleared))
	for _, r := range cleared {
		out = append(out, r.report())
	}
	return out, nil
}

func (m *memStore) Close(_ context.Context, orgID, id, by, reason string, at time.Time) (*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if err != nil {
		return nil, err
	}
	if r.Status != StatusOpen {
		return nil, ErrNotOpen
	}
	r.Status, r.ClosedBy, r.Resolution = StatusClosed, by, reason
	r.settle(at, "closed")
	return r.report(), nil
}

func (m *memStore) SetInvestigation(_ context.Context, orgID, id string, from []string, inv Investigation, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if errors.Is(err, ErrNotFound) || (err == nil && !slices.Contains(from, r.Investigation)) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	r.investigate(inv, at)
	return true, nil
}

func (m *memStore) WriteFindings(_ context.Context, orgID, id string, f Findings) (*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if err != nil {
		return nil, err
	}
	written := f
	written.Suggestions = slices.Clone(f.Suggestions)
	r.Findings = &written
	r.Revision++
	r.UpdatedAt = f.WrittenAt
	return r.report(), nil
}

func (m *memStore) Documents(_ context.Context, orgID string, after time.Time, limit int) ([]*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.sorted(orgID, func(r *record) bool {
		return (r.Findings != nil || r.Status != StatusOpen) && r.UpdatedAt.After(after)
	}, func(a, b *record) int {
		if c := a.UpdatedAt.Compare(b.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) hidden(t *testing.T, orgID, id string) record {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if err != nil {
		t.Fatal(err)
	}
	return *r.clone()
}

func (m *memStore) setIssueNumber(t *testing.T, orgID, id string, n int) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if err != nil {
		t.Fatal(err)
	}
	r.Issue.Number, r.Issue.State = n, IssueOpen
}

func (m *memStore) noticeSent(t *testing.T, orgID, id string, at time.Time) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if err != nil {
		t.Fatal(err)
	}
	r.PendingNotice, r.LastNoticeAt = "", &at
}

func (m *memStore) Pending(_ context.Context, orgID string) ([]*Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sorted(orgID, func(r *record) bool {
		return r.Revision > r.NotifiedRevision || r.Revision > r.IssueRevision ||
			(r.Issue.State == IssueWaiting && r.Status == StatusOpen)
	}, oldestFirst), nil
}

func (m *memStore) QueueDelivery(_ context.Context, orgID, id string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.find(orgID, id); err != nil {
		return false, nil
	}
	if at, ok := m.queued[id]; ok && !at.Before(now.Add(-DeliveryRequeue)) {
		return false, nil
	}
	m.queued[id] = now
	return true, nil
}

func (m *memStore) ClaimDelivery(_ context.Context, orgID, id, owner string, lease time.Duration) (*Delivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if err != nil {
		return nil, err
	}
	now := m.now()
	if c, ok := m.claims[id]; ok && c.owner != owner && !c.until.Before(now) {
		return nil, nil
	}
	m.claims[id] = memClaim{owner: owner, until: now.Add(lease)}
	return r.delivery(), nil
}

func (m *memStore) holds(id, owner string) bool { return m.claims[id].owner == owner }

func (m *memStore) SaveNotice(ctx context.Context, orgID, id, owner string, revision int, pendingNotice string,
	send func(ctx context.Context, q notifications.Execer, seq int) error, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if err != nil {
		return false, err
	}
	if r.Revision != revision || r.PendingNotice != pendingNotice || !m.holds(id, owner) {
		return false, nil
	}
	if pendingNotice != "" {
		q := m.notices
		if q == nil {
			q = dropExecer{}
		}
		if err := send(ctx, q, r.NoticeSeq+1); err != nil {
			return false, err
		}
		r.NoticeSeq++
		r.LastNoticeAt, r.PendingNotice = &at, ""
	}
	r.NotifiedRevision = max(r.NotifiedRevision, revision)
	return true, nil
}

func (m *memStore) SaveIssue(_ context.Context, orgID, id, owner string, revision int, in IssueSave) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.find(orgID, id)
	if err != nil {
		return err
	}
	if !m.holds(id, owner) {
		return nil
	}
	r.Issue = Issue{Number: in.Number, URL: in.URL, State: in.State, Error: in.Error}
	r.IssueComments = slices.Clone(r.IssueComments[min(in.Handled, len(r.IssueComments)):])
	if in.CommentAt != nil {
		r.IssueCommentAt = cloneTime(in.CommentAt)
	}
	if in.Done {
		r.IssueRevision = max(r.IssueRevision, revision)
	}
	return nil
}

func (m *memStore) ReleaseDelivery(_ context.Context, _, id, owner string, done bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.holds(id, owner) {
		return nil
	}
	delete(m.claims, id)
	if done {
		delete(m.queued, id)
	}
	return nil
}

// dropExecer is an Execer that runs nothing.
type dropExecer struct{}

func (dropExecer) Exec(context.Context, string, ...any) (driver.Result, error) { return nil, nil }
