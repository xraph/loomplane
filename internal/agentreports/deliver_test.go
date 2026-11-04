package agentreports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/grove/driver"
	"github.com/xraph/shield/scan"

	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/repositories/githubtest"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
)

// Delivery is tested once, as cases on an environment, and run against
// the in-memory store here and against PgStore, with real notices, in
// pgstore_integration_test.go.

const (
	acmeInstallation = int64(42)
	owner1           = "worker_1"
	userOwner        = "user_owner"
	userAdmin        = "user_admin"
	userTeam         = "user_team"
	userStarter      = "user_starter"
	projectSecret    = "hunter2-Zq9"
)

var (
	issuesAccepted    = map[string]string{"contents": "read", "issues": "write", "metadata": "read"}
	issuesNotAccepted = map[string]string{"contents": "read", "metadata": "read"}
	admins            = []string{userAdmin, userOwner}
)

// sentNotice is one queued notice row, for one recipient.
type sentNotice struct {
	User, Subject, Title, Body, Path, Need string
	Metadata                               map[string]string
}

// noticeLog is the fake Execer the in-memory store queues notices
// through: it keeps one row per recipient and subject, as the outbox's
// conflict rule does.
type noticeLog struct {
	mu   sync.Mutex
	rows map[string]sentNotice
}

func (l *noticeLog) Exec(_ context.Context, query string, args ...any) (driver.Result, error) {
	if !strings.Contains(query, "loomplane_notification_outbox") || len(args) < 7 {
		return nil, errors.New("noticeLog: not a notice")
	}
	var p struct {
		Title, Body, Path, Need string
		Metadata                map[string]string
	}
	if err := json.Unmarshal([]byte(args[2].(string)), &p); err != nil {
		return nil, err
	}
	ids, users := args[5].([]string), args[6].([]string)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rows == nil {
		l.rows = map[string]sentNotice{}
	}
	for i, id := range ids {
		if _, ok := l.rows[id]; !ok {
			l.rows[id] = sentNotice{User: users[i], Subject: args[4].(string), Title: p.Title, Body: p.Body, Path: p.Path, Need: p.Need, Metadata: p.Metadata}
		}
	}
	return nil, nil
}

func (l *noticeLog) sent() []sentNotice {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := slices.Collect(maps.Values(l.rows))
	sortNotices(out)
	return out
}

func sortNotices(n []sentNotice) {
	slices.SortFunc(n, func(a, b sentNotice) int {
		if c := strings.Compare(a.Subject, b.Subject); c != 0 {
			return c
		}
		return strings.Compare(a.User, b.User)
	})
}

// adminsDB answers notifications.Admins with the workspace's admins.
type adminsDB struct{ users []string }

func (a adminsDB) Query(_ context.Context, query string, _ ...any) (driver.Rows, error) {
	if !strings.Contains(query, "authsome_members") {
		return nil, errors.New("adminsDB: unexpected query")
	}
	return &stringRows{vals: a.users}, nil
}

type stringRows struct {
	vals []string
	i    int
}

func (r *stringRows) Next() bool {
	r.i++
	return r.i <= len(r.vals)
}

func (r *stringRows) Scan(dest ...any) error {
	*dest[0].(*string) = r.vals[r.i-1]
	return nil
}

func (r *stringRows) Columns() ([]string, error) { return []string{"user_id"}, nil }
func (r *stringRows) Close() error               { return nil }
func (r *stringRows) Err() error                 { return nil }

// fakeTeams makes projA's responsible team userTeam alone.
type fakeTeams struct{}

func (fakeTeams) Resolve(_ context.Context, _ notifications.Querier, _, projectID, _, _ string) (teams.Route, error) {
	if projectID == projA {
		return teams.Route{TeamID: "team_api", Team: "API", Source: "project", Members: []string{userTeam, userAdmin}}, nil
	}
	return teams.Route{Team: teams.AdminsName, Source: "admins", Members: admins}, nil
}

// fakeStarters started dep_1 as a person, dep_agent as the agent and
// dep_auto as the automation.
type fakeStarters struct{ org string }

func (f fakeStarters) StartedBy(_ context.Context, _, deploymentID string) (string, error) {
	switch deploymentID {
	case "dep_agent":
		return security.AgentSubject(f.org), nil
	case "dep_auto":
		return "loomplane:" + f.org, nil
	case "dep_gone":
		return "", errors.New("deployment not found")
	}
	return userStarter, nil
}

type fakeProjectReader struct{}

func (fakeProjectReader) GetProject(_ context.Context, orgID, id string) (*projects.Project, error) {
	return &projects.Project{ID: id, OrgID: orgID, Name: "Shop"}, nil
}

type fakePeople struct{}

func (fakePeople) Name(_ context.Context, userID string) string {
	if userID == developer {
		return "Dana Developer"
	}
	return "a teammate"
}

type fakeSecrets struct {
	values []string
	err    error
}

func (f fakeSecrets) SecretValues(context.Context, string, string, string) ([]string, error) {
	return f.values, f.err
}

// blockEngine blocks every outbound text.
type blockEngine struct{}

func (blockEngine) ScanInput(context.Context, *scan.Input) (*scan.Result, error) {
	return &scan.Result{Blocked: true}, nil
}

func (blockEngine) ScanOutput(context.Context, *scan.Input) (*scan.Result, error) {
	return &scan.Result{Blocked: true}, nil
}

// tenantsOff answers as agentsettings does while the agent is off.
type tenantsOff struct{}

func (tenantsOff) NexusTenant(context.Context, string) (string, error) {
	return "", agentsettings.ErrDisabled
}

// memInstallations is the workspace's connected GitHub installations.
type memInstallations struct {
	mu   sync.Mutex
	recs []*repositories.InstallationRecord
}

func (s *memInstallations) SaveInstallation(_ context.Context, r *repositories.InstallationRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *r
	s.recs = append(s.recs, &c)
	return nil
}

func (s *memInstallations) ListInstallations(_ context.Context, orgID string) ([]*repositories.InstallationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*repositories.InstallationRecord
	for _, r := range s.recs {
		if r.OrgID == orgID {
			c := *r
			c.Permissions = maps.Clone(r.Permissions)
			out = append(out, &c)
		}
	}
	return out, nil
}

func (s *memInstallations) DeleteInstallation(context.Context, string, int64) error { return nil }

func (s *memInstallations) SavePermissions(_ context.Context, orgID string, id int64, perms map[string]string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.recs {
		if r.OrgID == orgID && r.ID == id {
			r.Permissions, r.PermissionsCheckedAt = maps.Clone(perms), &at
		}
	}
	return nil
}

// expire makes Loomplane ask GitHub for the permissions again, as it does
// once PermissionsTTL has passed.
func (s *memInstallations) expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.recs {
		r.PermissionsCheckedAt = nil
	}
}

// deliverEnv is a workspace with acme's GitHub App installation, a
// project named Shop and the people who hear about its reports.
type deliverEnv struct {
	*harness
	gh      *githubtest.Server
	conns   *repositories.Connections
	inst    *memInstallations
	notices func(t *testing.T) []sentNotice
}

type deliverOptions struct {
	perms   map[string]string
	engine  agentsafety.Engine // nil: real shield
	tenants agentscope.Tenants // nil: the agent is on
	secrets SecretValues
}

// newEnv builds the environment over h's store. db answers who the
// admins are; notices lists what was queued.
func newEnv(t *testing.T, h *harness, db notifications.Querier, notices func(*testing.T) []sentNotice, o deliverOptions) *deliverEnv {
	t.Helper()
	if o.perms == nil {
		o.perms = issuesAccepted
	}
	gh := githubtest.New(t)
	gh.AddInstallation(acmeInstallation, "acme", o.perms)
	app, err := repositories.NewApp(gh.AppConfig())
	if err != nil {
		t.Fatal(err)
	}
	inst := &memInstallations{}
	if err = inst.SaveInstallation(context.Background(), &repositories.InstallationRecord{
		OrgID: h.org, ID: acmeInstallation, Account: "acme", AccountType: "Organization", CreatedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	conns := repositories.NewConnections(inst, app, nil, nil, []byte("0123456789abcdef0123456789abcdef"))
	engine := o.engine
	if engine == nil {
		engine = agenttest.Shield(t, h.org)
	}
	tenants := o.tenants
	if tenants == nil {
		tenants = tenantsOn{}
	}
	secrets := o.secrets
	if secrets == nil {
		secrets = fakeSecrets{values: []string{projectSecret}}
	}
	h.svc = NewService(Deps{
		Store: h.store, DB: db, Authz: newFakeAuthz(), Projects: fakeProjects{}, ProjectReader: fakeProjectReader{},
		People: fakePeople{}, Teams: fakeTeams{}, Safety: &agentsafety.Scanner{Engine: engine},
		Binder: &agentscope.Binder{Tenants: tenants}, Issues: conns, Secrets: secrets, Starters: fakeStarters{org: h.org},
		Audit: h.audit, PublicURL: publicURL + "/", Now: h.clock.Now,
		Warn: func(msg string, err error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.warnings = append(h.warnings, msg+": "+err.Error())
		},
	})
	return &deliverEnv{harness: h, gh: gh, conns: conns, inst: inst, notices: notices}
}

func newMemEnv(t *testing.T, o deliverOptions) *deliverEnv {
	t.Helper()
	m := NewMemStore()
	log := &noticeLog{}
	m.notices = log
	h := newHarness(t, m, m, "org_a")
	return newEnv(t, h, adminsDB{users: admins}, func(*testing.T) []sentNotice { return log.sent() }, o)
}

func (e *deliverEnv) deliver(t *testing.T, id string) {
	t.Helper()
	if err := e.svc.Deliver(context.Background(), e.org, id, owner1); err != nil {
		t.Fatalf("deliver: %v", err)
	}
}

func (e *deliverEnv) issues() []githubtest.IssueRecord { return e.gh.Issues("acme/api") }

// creates counts the requests that open an issue.
func (e *deliverEnv) creates() int {
	n := 0
	for _, r := range e.gh.Requests() {
		if r == "POST /repos/acme/api/issues" {
			n++
		}
	}
	return n
}

func (e *deliverEnv) opened(t *testing.T) *Report {
	t.Helper()
	return e.observe(t, e.signal(SignalDeploymentFailed, "service:api", "dep_1#0")).Report
}

// everyone hears about projA's reports: the admins, the API team and the
// person who started dep_1.
var everyone = []string{userAdmin, userOwner, userStarter, userTeam}

func noticeUsers(notices []sentNotice, subject string) []string {
	var users []string
	for _, n := range notices {
		if n.Subject == subject {
			users = append(users, n.User)
		}
	}
	slices.Sort(users)
	return users
}

type deliverCase struct {
	name string
	run  func(t *testing.T, e *deliverEnv)
	opts deliverOptions
}

// deliverCases run against every Store.
var deliverCases = []deliverCase{
	{name: "FailedDeploymentDeliversOneNoticeAndOneIssue", run: failedDeploymentDeliversOneNoticeAndOneIssue},
	{name: "RepeatsNotifyAtMostHourly", run: repeatsNotifyAtMostHourly},
	{name: "ReopenReopensTheSameIssue", run: reopenReopensTheSameIssue},
	{name: "ResolveClosesTheIssueWithAComment", run: resolveClosesTheIssueWithAComment},
	{name: "CloseCommentsAndClosesAsNotPlanned", run: closeCommentsAndClosesAsNotPlanned},
	{name: "BodyUpdatesNeverReopenAnIssueAPersonClosed", run: bodyUpdatesNeverReopenAnIssueAPersonClosed},
	{name: "PermissionNotAcceptedSendsHeraldOnly", run: permissionNotAcceptedSendsHeraldOnly, opts: deliverOptions{perms: issuesNotAccepted}},
	{name: "NoRepositoryIsHeraldOnly", run: noRepositoryIsHeraldOnly},
	{name: "SecretsNeverReachTheIssueOrNotice", run: secretsNeverReachTheIssueOrNotice},
	{name: "OutboundBlockSendsTheFallback", run: outboundBlockSendsTheFallback, opts: deliverOptions{engine: blockEngine{}}},
	{name: "NoticeIsNotResentWhenGitHubFails", run: noticeIsNotResentWhenGitHubFails},
	{name: "IssueCreatedBeforeACrashIsFoundByItsMarker", run: issueCreatedBeforeACrashIsFoundByItsMarker},
	{name: "PlantedMarkerIssueIsNotAdopted", run: plantedMarkerIssueIsNotAdopted},
	{name: "DeliveryClaimKeepsTwoWorkersApart", run: deliveryClaimKeepsTwoWorkersApart},
	{name: "NothingIsDeliveredWhileTheAgentIsOff", run: nothingIsDeliveredWhileTheAgentIsOff, opts: deliverOptions{tenants: tenantsOff{}}},
	{name: "PendingAndQueueDelivery", run: pendingAndQueueDelivery},
	{name: "KnowledgeKeepsTheScannedIssueText", run: knowledgeKeepsTheScannedIssueText},
	{name: "StepsKeepWhatChangedMeanwhile", run: stepsKeepWhatChangedMeanwhile, opts: deliverOptions{engine: passEngine{}}},
	{name: "CloseIsRetriedWithoutASecondComment", run: closeIsRetriedWithoutASecondComment},
	{name: "LostLeaseOpensNoIssue", run: lostLeaseOpensNoIssue},
	{name: "UnsavedNoticeHoldsBackGitHub", run: unsavedNoticeHoldsBackGitHub},
	{name: "CommentsAreRedactedBeforeTheyAreEscaped", run: commentsAreRedactedBeforeTheyAreEscaped,
		opts: deliverOptions{secrets: fakeSecrets{values: []string{"s3<cr@et-Val"}}}},
	{name: "StalledWorkerOpensNoSecondIssue", run: stalledWorkerOpensNoSecondIssue},
	{name: "DeletedIssueIsOpenedAgain", run: deletedIssueIsOpenedAgain},
	{name: "UnreadableSecretsSendNothing", run: unreadableSecretsSendNothing,
		opts: deliverOptions{secrets: fakeSecrets{values: []string{projectSecret}, err: errors.New("vault is down")}}},
	{name: "SecretsOfAGoneProjectAreSkipped", run: secretsOfAGoneProjectAreSkipped,
		opts: deliverOptions{secrets: fakeSecrets{err: fmt.Errorf("read project: %w", projects.ErrNotFound)}, engine: blockEngine{}}},
}

func runMem(t *testing.T, name string) {
	t.Helper()
	for _, c := range deliverCases {
		if c.name == name {
			c.run(t, newMemEnv(t, c.opts))
			return
		}
	}
	t.Fatalf("no case %s", name)
}

func TestFailedDeploymentDeliversOneNoticeAndOneIssue(t *testing.T) {
	runMem(t, "FailedDeploymentDeliversOneNoticeAndOneIssue")
}
func TestRepeatsNotifyAtMostHourly(t *testing.T) { runMem(t, "RepeatsNotifyAtMostHourly") }
func TestReopenReopensTheSameIssue(t *testing.T) { runMem(t, "ReopenReopensTheSameIssue") }
func TestResolveClosesTheIssueWithAComment(t *testing.T) {
	runMem(t, "ResolveClosesTheIssueWithAComment")
}
func TestCloseCommentsAndClosesAsNotPlanned(t *testing.T) {
	runMem(t, "CloseCommentsAndClosesAsNotPlanned")
}
func TestBodyUpdatesNeverReopenAnIssueAPersonClosed(t *testing.T) {
	runMem(t, "BodyUpdatesNeverReopenAnIssueAPersonClosed")
}
func TestPermissionNotAcceptedSendsHeraldOnly(t *testing.T) {
	runMem(t, "PermissionNotAcceptedSendsHeraldOnly")
}
func TestNoRepositoryIsHeraldOnly(t *testing.T) { runMem(t, "NoRepositoryIsHeraldOnly") }
func TestSecretsNeverReachTheIssueOrNotice(t *testing.T) {
	runMem(t, "SecretsNeverReachTheIssueOrNotice")
}
func TestOutboundBlockSendsTheFallback(t *testing.T) { runMem(t, "OutboundBlockSendsTheFallback") }
func TestNoticeIsNotResentWhenGitHubFails(t *testing.T) {
	runMem(t, "NoticeIsNotResentWhenGitHubFails")
}
func TestIssueCreatedBeforeACrashIsFoundByItsMarker(t *testing.T) {
	runMem(t, "IssueCreatedBeforeACrashIsFoundByItsMarker")
}
func TestPlantedMarkerIssueIsNotAdopted(t *testing.T) { runMem(t, "PlantedMarkerIssueIsNotAdopted") }
func TestDeliveryClaimKeepsTwoWorkersApart(t *testing.T) {
	runMem(t, "DeliveryClaimKeepsTwoWorkersApart")
}
func TestNothingIsDeliveredWhileTheAgentIsOff(t *testing.T) {
	runMem(t, "NothingIsDeliveredWhileTheAgentIsOff")
}
func TestPendingAndQueueDelivery(t *testing.T) { runMem(t, "PendingAndQueueDelivery") }
func TestKnowledgeKeepsTheScannedIssueText(t *testing.T) {
	runMem(t, "KnowledgeKeepsTheScannedIssueText")
}

func failedDeploymentDeliversOneNoticeAndOneIssue(t *testing.T, e *deliverEnv) {
	r := e.opened(t)
	e.deliver(t, r.ID)

	notices := e.notices(t)
	if got := noticeUsers(notices, r.ID+":1"); !slices.Equal(got, everyone) || len(notices) != len(everyone) {
		t.Fatalf("notices to %v (%d rows)", got, len(notices))
	}
	n := notices[0]
	if n.Title != "Shop · production: "+r.Title || n.Path != "/agent/reports/"+r.ID || n.Need != "deployment:read" ||
		n.Metadata["report_id"] != r.ID || n.Metadata["signal"] != SignalDeploymentFailed || n.Metadata["severity"] != SeverityHigh ||
		n.Metadata["environment"] != "production" || !strings.Contains(n.Body, "Deployment: dep_1") {
		t.Fatalf("notice %+v", n)
	}
	issues := e.issues()
	if len(issues) != 1 {
		t.Fatalf("%d issues", len(issues))
	}
	is := issues[0]
	if is.Title != r.Title || !slices.Equal(is.Labels, []string{Label}) || is.State != "open" ||
		!strings.HasPrefix(is.Body, Marker(r.ID)) || !strings.Contains(is.Body, publicURL+"/agent/reports/"+r.ID) {
		t.Fatalf("issue %+v", is)
	}
	got := e.lookup(t, r.ID)
	if got.Issue.Number != is.Number || got.Issue.State != IssueOpen || !strings.HasSuffix(got.Issue.URL, "/acme/api/issues/1") || got.Issue.Error != "" {
		t.Fatalf("report issue %+v", got.Issue)
	}
	hid := e.peek.hidden(t, e.org, r.ID)
	if hid.NotifiedRevision != got.Revision || hid.IssueRevision != got.Revision || hid.PendingNotice != "" || hid.NoticeSeq != 1 {
		t.Fatalf("delivery columns %+v", hid)
	}
	if ev, ok := e.audit.last("agent.report.issue"); !ok || ev.Attributes["action"] != "created" || ev.Attributes["number"] != 1 ||
		ev.Attributes["repository"] != "acme/api" || ev.Attributes["report_id"] != r.ID || !ev.Principal.IsAgent() {
		t.Fatalf("audit %+v", ev)
	}

	requests := len(e.gh.Requests())
	e.deliver(t, r.ID)
	if len(e.gh.Requests()) != requests || len(e.notices(t)) != len(everyone) {
		t.Fatalf("a second delivery did something: %d requests, %d notices", len(e.gh.Requests())-requests, len(e.notices(t)))
	}
	if pending, err := e.svc.Pending(context.Background(), e.org); err != nil || len(pending) != 0 {
		t.Fatalf("pending after delivery: %v, %v", pending, err)
	}
}

func repeatsNotifyAtMostHourly(t *testing.T, e *deliverEnv) {
	r := e.opened(t)
	e.deliver(t, r.ID)
	e.clock.advance(2 * time.Hour)
	for _, occ := range []string{"dep_2#0", "dep_3#0", "dep_4#0"} {
		if obs := e.observe(t, e.signal(SignalDeploymentFailed, "service:api", occ)); obs.Change != ChangeRepeated {
			t.Fatalf("%s: %s", occ, obs.Change)
		}
		e.deliver(t, r.ID)
		e.clock.advance(5 * time.Minute)
	}
	notices := e.notices(t)
	if len(notices) != 2*len(everyone) || len(noticeUsers(notices, r.ID+":2")) != len(everyone) {
		t.Fatalf("%d notices", len(notices))
	}
	for _, n := range notices {
		if n.Subject == r.ID+":2" && (!strings.HasPrefix(n.Title, "Again: Shop · production: ") ||
			!strings.Contains(n.Body, "This has happened 2 times since Tue, 03 Nov 2026 10:00:00 UTC.")) {
			t.Fatalf("repeat notice %+v", n)
		}
	}
	is := e.issues()[0]
	if !strings.Contains(is.Body, "seen 4 times.") {
		t.Fatalf("issue body doesn't count 4:\n%s", is.Body)
	}
	if len(is.Comments) != 1 || !strings.HasPrefix(is.Comments[0], "This happened again (2 times now, last at ") {
		t.Fatalf("comments %q", is.Comments)
	}
	if hid := e.peek.hidden(t, e.org, r.ID); len(hid.IssueComments) != 0 || hid.IssueCommentAt == nil {
		t.Fatalf("queued comments %v, last at %v", hid.IssueComments, hid.IssueCommentAt)
	}
	// An hour on, a repeat is worth a comment again.
	e.clock.advance(time.Hour)
	e.observe(t, e.signal(SignalDeploymentFailed, "service:api", "dep_5#0"))
	e.deliver(t, r.ID)
	if is = e.issues()[0]; len(is.Comments) != 2 || !strings.HasPrefix(is.Comments[1], "This happened again (5 times now") {
		t.Fatalf("comments %q", is.Comments)
	}
}

func reopenReopensTheSameIssue(t *testing.T, e *deliverEnv) {
	r := e.opened(t)
	e.deliver(t, r.ID)
	e.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}, Reason: "A later deployment (dep_9) succeeded"})
	e.deliver(t, r.ID)
	if is := e.issues()[0]; is.State != "closed" || is.StateReason != "completed" {
		t.Fatalf("resolved issue %+v", is)
	}
	e.clock.advance(time.Hour)
	if back := e.observe(t, e.signal(SignalDeploymentFailed, "service:api", "dep_2#0")); back.Change != ChangeReopened || back.Report.ID != r.ID {
		t.Fatalf("change %s", back.Change)
	}
	e.deliver(t, r.ID)
	issues := e.issues()
	if len(issues) != 1 || e.creates() != 1 {
		t.Fatalf("%d issues, %d creates", len(issues), e.creates())
	}
	is := issues[0]
	if is.State != "open" || is.StateReason != "reopened" || len(is.Comments) != 2 ||
		is.Comments[1] != "This is back: "+r.Title+". Loomplane reopened the report." {
		t.Fatalf("reopened issue %+v", is)
	}
	if got := e.lookup(t, r.ID); got.Issue.Number != is.Number || got.Issue.State != IssueOpen {
		t.Fatalf("report issue %+v", got.Issue)
	}
	if users := noticeUsers(e.notices(t), r.ID+":2"); len(users) != len(everyone) {
		t.Fatalf("reopen notice to %v", users)
	}
	for _, n := range e.notices(t) {
		if n.Subject == r.ID+":2" && !strings.HasPrefix(n.Title, "Back again: ") {
			t.Fatalf("reopen title %q", n.Title)
		}
	}
	if ev, ok := e.audit.last("agent.report.issue"); !ok || ev.Attributes["action"] != "commented" {
		t.Fatalf("audit %+v", ev)
	}
}

func resolveClosesTheIssueWithAComment(t *testing.T, e *deliverEnv) {
	r := e.opened(t)
	e.deliver(t, r.ID)
	e.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}, Reason: "A later deployment (dep_9) succeeded"})
	e.deliver(t, r.ID)
	is := e.issues()[0]
	if is.State != "closed" || is.StateReason != "completed" || len(is.Comments) != 1 ||
		is.Comments[0] != "Resolved: A later deployment (dep_9) succeeded. Loomplane closed the report." {
		t.Fatalf("issue %+v", is)
	}
	if got := e.lookup(t, r.ID); got.Issue.State != IssueClosed {
		t.Fatalf("report issue %+v", got.Issue)
	}
	if n := len(e.notices(t)); n != len(everyone) {
		t.Fatalf("resolving sent a notice: %d rows", n)
	}
}

func closeCommentsAndClosesAsNotPlanned(t *testing.T, e *deliverEnv) {
	r := e.opened(t)
	e.deliver(t, r.ID)
	if _, err := e.svc.Close(context.Background(), e.person(developer), r.ID, CloseInput{Reason: "Not a real problem."}); err != nil {
		t.Fatal(err)
	}
	e.deliver(t, r.ID)
	is := e.issues()[0]
	if is.State != "closed" || is.StateReason != "not_planned" || len(is.Comments) != 1 ||
		is.Comments[0] != "Dana Developer closed this in Loomplane: Not a real problem." {
		t.Fatalf("issue %+v", is)
	}
}

func bodyUpdatesNeverReopenAnIssueAPersonClosed(t *testing.T, e *deliverEnv) {
	ctx := context.Background()
	r := e.opened(t)
	e.deliver(t, r.ID)
	iss, err := e.conns.Issues(ctx, e.org, "acme/api")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = iss.Edit(ctx, 1, repositories.IssueEdit{State: "closed", StateReason: "not_planned"}); err != nil {
		t.Fatal(err)
	}
	e.clock.advance(2 * time.Hour)
	e.observe(t, e.signal(SignalDeploymentFailed, "service:api", "dep_2#0"))
	e.deliver(t, r.ID)
	is := e.issues()[0]
	if is.State != "closed" || !strings.Contains(is.Body, "seen 2 times.") || len(is.Comments) != 1 {
		t.Fatalf("issue %+v", is)
	}
	if got := e.lookup(t, r.ID); got.Issue.State != IssueClosed {
		t.Fatalf("report issue %+v", got.Issue)
	}
}

func permissionNotAcceptedSendsHeraldOnly(t *testing.T, e *deliverEnv) {
	r := e.opened(t)
	e.deliver(t, r.ID)
	if n := len(e.notices(t)); n != len(everyone) {
		t.Fatalf("%d notices", n)
	}
	if e.creates() != 0 || len(e.issues()) != 0 {
		t.Fatal("an issue was opened without the permission")
	}
	got := e.lookup(t, r.ID)
	if got.Issue.State != IssueWaiting || !strings.HasPrefix(got.Issue.Error, "acme hasn't accepted the Issues permission yet") ||
		strings.Contains(got.Issue.Error, repositories.ErrIssuesNotAccepted.Error()) {
		t.Fatalf("report issue %+v", got.Issue)
	}
	if hid := e.peek.hidden(t, e.org, r.ID); hid.IssueRevision != got.Revision {
		t.Fatalf("issue revision %d of %d", hid.IssueRevision, got.Revision)
	}
	if pending, err := e.svc.Pending(context.Background(), e.org); err != nil || len(pending) != 1 {
		t.Fatalf("a waiting report isn't pending: %v, %v", pending, err)
	}

	e.gh.SetPermissions(acmeInstallation, issuesAccepted)
	e.inst.expire()
	e.deliver(t, r.ID)
	if e.creates() != 1 || len(e.issues()) != 1 || len(e.notices(t)) != len(everyone) {
		t.Fatalf("%d creates, %d notices", e.creates(), len(e.notices(t)))
	}
	if got = e.lookup(t, r.ID); got.Issue.State != IssueOpen || got.Issue.Number != 1 || got.Issue.Error != "" {
		t.Fatalf("report issue %+v", got.Issue)
	}
}

func noRepositoryIsHeraldOnly(t *testing.T, e *deliverEnv) {
	sig := e.signal(SignalDeploymentFailed, "service:api", "dep_1#0")
	sig.Repository = ""
	r := e.observe(t, sig).Report
	e.deliver(t, r.ID)
	if n := len(e.notices(t)); n != len(everyone) {
		t.Fatalf("%d notices", n)
	}
	if reqs := e.gh.Requests(); len(reqs) != 0 {
		t.Fatalf("GitHub was called: %v", reqs)
	}
	if got := e.lookup(t, r.ID); got.Issue.State != IssueNoRepository || got.Issue.Error != "Loomplane couldn't tell which repository this belongs to" {
		t.Fatalf("report issue %+v", got.Issue)
	}
}

func secretsNeverReachTheIssueOrNotice(t *testing.T, e *deliverEnv) {
	ctx := context.Background()
	const awsKey, dbURL = "AKIAIOSFODNN7EXAMPLE", "postgres://app:s3cretpw@db:5432/app"
	leaked := []string{projectSecret, awsKey, "s3cretpw"}
	sig := e.signal(SignalDeploymentFailed, "service:api", "dep_1#0")
	sig.Facts = append(sig.Facts,
		Fact{Label: "Error", Value: "login failed for " + dbURL + " with password " + projectSecret, Untrusted: true},
		Fact{Label: "Key", Value: "the build used " + awsKey, Untrusted: true})
	r := e.observe(t, sig).Report
	e.deliver(t, r.ID)

	// Findings that quote them, a repeat and a resolution do too.
	if ok, err := e.svc.SetInvestigation(ctx, e.org, r.ID, []string{InvestigationNone}, Investigation{Status: InvestigationRunning, TaskID: "atask_1"}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, err := e.svc.WriteFindings(ctx, e.org, r.ID, Writer{TaskID: "atask_1"}, FindingsInput{
		Summary: "The database password " + projectSecret + " was rejected.", LikelyCause: "It connects with " + dbURL + ".",
		Suggestions: []string{"Rotate " + awsKey}, Confidence: "high",
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.svc.SetInvestigation(ctx, e.org, r.ID, []string{InvestigationRunning}, Investigation{Status: InvestigationDone}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	e.clock.advance(2 * time.Hour)
	sig.Occurrence = "dep_2#0"
	e.observe(t, sig)
	e.deliver(t, r.ID)
	e.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}, Reason: "Fixed by rotating " + projectSecret})
	e.deliver(t, r.ID)

	notices := e.notices(t)
	sent := make([]string, 0, 2*len(notices)+8)
	for _, n := range notices {
		sent = append(sent, n.Title, n.Body)
	}
	issues := e.issues()
	if len(issues) != 1 || len(issues[0].Comments) < 2 {
		t.Fatalf("issues %+v", issues)
	}
	sent = append(sent, issues[0].Title, issues[0].Body)
	sent = append(sent, issues[0].Comments...)
	for _, text := range sent {
		for _, secret := range leaked {
			if strings.Contains(text, secret) {
				t.Fatalf("%q went out in %q", secret, text)
			}
		}
	}
	if !strings.Contains(issues[0].Body, "postgres://app:[redacted]@db:5432/app") {
		t.Fatalf("the URL lost more than its password:\n%s", issues[0].Body)
	}
}

func outboundBlockSendsTheFallback(t *testing.T, e *deliverEnv) {
	r := e.opened(t)
	e.deliver(t, r.ID)
	for _, n := range e.notices(t) {
		if n.Title != "Loomplane opened a report for Shop" || n.Body != "Open it in Loomplane to read it." {
			t.Fatalf("notice %+v", n)
		}
	}
	is := e.issues()[0]
	want := Marker(r.ID) + "\n\nLoomplane opened a report for this repository. Read it in Loomplane: " + publicURL + "/agent/reports/" + r.ID
	if is.Body != want || is.Title != "Loomplane opened a report for Shop" {
		t.Fatalf("issue %q\n%s", is.Title, is.Body)
	}
}

func noticeIsNotResentWhenGitHubFails(t *testing.T, e *deliverEnv) {
	r := e.opened(t)
	e.gh.FailNext("/repos/acme/api/issues", http.StatusBadGateway)
	if err := e.svc.Deliver(context.Background(), e.org, r.ID, owner1); err == nil {
		t.Fatal("a GitHub failure didn't fail the delivery")
	}
	if n := len(e.notices(t)); n != len(everyone) {
		t.Fatalf("%d notices", n)
	}
	if got := e.lookup(t, r.ID); got.Issue.State != IssueFailed || !strings.Contains(got.Issue.Error, "502") {
		t.Fatalf("report issue %+v", got.Issue)
	}
	e.deliver(t, r.ID)
	if e.creates() != 1 || len(e.notices(t)) != len(everyone) {
		t.Fatalf("%d creates, %d notices", e.creates(), len(e.notices(t)))
	}
	if got := e.lookup(t, r.ID); got.Issue.State != IssueOpen || got.Issue.Error != "" {
		t.Fatalf("report issue %+v", got.Issue)
	}
}

func issueCreatedBeforeACrashIsFoundByItsMarker(t *testing.T, e *deliverEnv) {
	ctx := context.Background()
	r := e.opened(t)
	iss, err := e.conns.Issues(ctx, e.org, "acme/api")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = iss.Create(ctx, repositories.IssueInput{Title: "old", Body: Marker(r.ID) + "\n\nold body", Labels: []string{Label}}); err != nil {
		t.Fatal(err)
	}
	e.deliver(t, r.ID)
	if e.creates() != 1 {
		t.Fatalf("%d creates", e.creates())
	}
	issues := e.issues()
	if len(issues) != 1 || issues[0].Title != r.Title || strings.Contains(issues[0].Body, "old body") {
		t.Fatalf("issues %+v", issues)
	}
	if got := e.lookup(t, r.ID); got.Issue.Number != 1 || got.Issue.State != IssueOpen {
		t.Fatalf("report issue %+v", got.Issue)
	}
}

func plantedMarkerIssueIsNotAdopted(t *testing.T, e *deliverEnv) {
	r := e.opened(t)
	planted := e.gh.AddIssue("acme/api", githubtest.IssueRecord{Title: "planted", Body: Marker(r.ID), Labels: []string{Label}})
	e.gh.AddIssue("acme/api", githubtest.IssueRecord{Title: "from a workflow", Body: Marker(r.ID), Labels: []string{Label},
		Author: "github-actions[bot]", AuthorType: "Bot"})
	e.deliver(t, r.ID)
	issues := e.issues()
	if len(issues) != 3 || issues[0].Body != Marker(r.ID) || issues[0].Title != "planted" || issues[1].Title != "from a workflow" {
		t.Fatalf("a planted issue changed: %+v", issues)
	}
	if got := e.lookup(t, r.ID); got.Issue.Number == planted || got.Issue.Number != 3 {
		t.Fatalf("report issue %+v", got.Issue)
	}
}

func deliveryClaimKeepsTwoWorkersApart(t *testing.T, e *deliverEnv) {
	ctx := context.Background()
	r := e.opened(t)
	// A worker holding the delivery keeps another out.
	if d, err := e.store.ClaimDelivery(ctx, e.org, r.ID, "worker_x", DeliveryLease); err != nil || d == nil {
		t.Fatalf("claim: %v, %v", d, err)
	}
	if err := e.svc.Deliver(ctx, e.org, r.ID, "worker_y"); err != nil {
		t.Fatal(err)
	}
	if len(e.notices(t)) != 0 || len(e.gh.Requests()) != 0 {
		t.Fatal("a second worker delivered a held report")
	}
	if err := e.store.ReleaseDelivery(ctx, e.org, r.ID, "worker_x", false); err != nil {
		t.Fatal(err)
	}
	// Two at once: one notice set, one issue.
	var wg sync.WaitGroup
	for _, w := range []string{"worker_a", "worker_b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.svc.Deliver(ctx, e.org, r.ID, w); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := len(e.notices(t)); n != len(everyone) {
		t.Fatalf("%d notices", n)
	}
	if e.creates() != 1 || len(e.issues()) != 1 {
		t.Fatalf("%d creates", e.creates())
	}
}

func nothingIsDeliveredWhileTheAgentIsOff(t *testing.T, e *deliverEnv) {
	ctx := context.Background()
	r := e.opened(t)
	e.deliver(t, r.ID)
	if len(e.notices(t)) != 0 || len(e.gh.Requests()) != 0 {
		t.Fatal("delivered with the agent off")
	}
	if hid := e.peek.hidden(t, e.org, r.ID); hid.NotifiedRevision != 0 || hid.IssueRevision != 0 || hid.PendingNotice != noticeOpened {
		t.Fatalf("delivery columns %+v", hid)
	}
	if text, err := e.svc.Text(ctx, r); err != nil || text != (Rendered{}) {
		t.Fatalf("text %+v, %v", text, err)
	}
	e.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}})
	if docs, err := e.svc.Documents(ctx, e.org, time.Time{}, 10); err != nil || len(docs) != 0 {
		t.Fatalf("documents %d, %v", len(docs), err)
	}
	// The claim is let go, so the next tick with the agent on delivers.
	if d, err := e.store.ClaimDelivery(ctx, e.org, r.ID, "worker_z", DeliveryLease); err != nil || d == nil {
		t.Fatalf("claim after: %v, %v", d, err)
	}
}

func pendingAndQueueDelivery(t *testing.T, e *deliverEnv) {
	ctx := context.Background()
	r := e.opened(t)
	pending, err := e.svc.Pending(ctx, e.org)
	if err != nil || len(pending) != 1 || pending[0].ID != r.ID {
		t.Fatalf("pending %v, %v", pending, err)
	}
	now := e.clock.Now()
	for i, want := range []bool{true, false} {
		if queued, qerr := e.svc.QueueDelivery(ctx, e.org, r.ID, now.Add(time.Duration(i)*time.Minute)); qerr != nil || queued != want {
			t.Fatalf("queue %d: %v, %v", i, queued, qerr)
		}
	}
	if queued, qerr := e.svc.QueueDelivery(ctx, e.org, r.ID, now.Add(DeliveryRequeue+time.Second)); qerr != nil || !queued {
		t.Fatalf("requeue after the wait: %v, %v", queued, qerr)
	}
	e.deliver(t, r.ID)
	if queued, qerr := e.svc.QueueDelivery(ctx, e.org, r.ID, now.Add(DeliveryRequeue+2*time.Second)); qerr != nil || !queued {
		t.Fatalf("queue after delivery: %v, %v", queued, qerr)
	}
	if pending, err = e.svc.Pending(ctx, e.org); err != nil || len(pending) != 0 {
		t.Fatalf("pending after delivery %v, %v", pending, err)
	}
}

func knowledgeKeepsTheScannedIssueText(t *testing.T, e *deliverEnv) {
	ctx := context.Background()
	o := &fakeObserver{}
	e.svc.AddObserver(o)
	sig := e.signal(SignalDeploymentFailed, "service:api", "dep_1#0")
	sig.Facts = append(sig.Facts, Fact{Label: "Error", Value: "password " + projectSecret + " rejected", Untrusted: true})
	r := e.observe(t, sig).Report
	e.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}, Reason: "fixed"})
	docs := o.seen()
	if len(docs) != 1 {
		t.Fatalf("observers heard %d", len(docs))
	}
	backfill, err := e.svc.Documents(ctx, e.org, time.Time{}, 10)
	if err != nil || len(backfill) != 1 {
		t.Fatalf("documents %d, %v", len(backfill), err)
	}
	text, err := e.svc.Text(ctx, e.lookup(t, r.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range []KnowledgeDocument{docs[0], backfill[0]} {
		if !strings.HasPrefix(doc.Text, r.Title+"\n\n**High** in Shop (production)") || !strings.Contains(doc.Text, "### What happened") ||
			strings.Contains(doc.Text, "<!--") || strings.Contains(doc.Text, projectSecret) {
			t.Fatalf("knowledge text:\n%s", doc.Text)
		}
		if doc.Text != text.IssueTitle+"\n\n"+strings.TrimPrefix(text.IssueBody, Marker(r.ID)+"\n\n") {
			t.Fatal("knowledge keeps other text than the issue")
		}
	}
}

func TestStepsKeepWhatChangedMeanwhile(t *testing.T) { runMem(t, "StepsKeepWhatChangedMeanwhile") }

// A report that moves on while a worker delivers it keeps what moved: a
// notice for the old revision isn't recorded, and a comment queued after
// the read waits for the next delivery.
func stepsKeepWhatChangedMeanwhile(t *testing.T, e *deliverEnv) {
	ctx := context.Background()
	r := e.opened(t)
	d, err := e.store.ClaimDelivery(ctx, e.org, r.ID, owner1, DeliveryLease)
	if err != nil || d == nil || d.PendingNotice != noticeOpened || d.NoticeSeq != 0 {
		t.Fatalf("claim %+v, %v", d, err)
	}
	ran := false
	send := func(context.Context, notifications.Execer, int) error { ran = true; return nil }
	if saved, serr := e.store.SaveNotice(ctx, e.org, r.ID, "worker_other", d.Report.Revision, d.PendingNotice, send, base); serr != nil || saved || ran {
		t.Fatalf("another owner saved a notice: %v, %v, ran %v", saved, serr, ran)
	}
	e.observe(t, e.signal(SignalDeploymentFailed, "service:api", "dep_2#0"))
	if saved, serr := e.store.SaveNotice(ctx, e.org, r.ID, owner1, d.Report.Revision, d.PendingNotice, send, base); serr != nil || saved || ran {
		t.Fatalf("a notice for an old revision was saved: %v, %v, ran %v", saved, serr, ran)
	}
	if d, err = e.store.ClaimDelivery(ctx, e.org, r.ID, owner1, DeliveryLease); err != nil || d.Report.Revision != 2 {
		t.Fatalf("reclaim %+v, %v", d, err)
	}
	var seqs []int
	send = func(_ context.Context, _ notifications.Execer, seq int) error { seqs = append(seqs, seq); return nil }
	if saved, serr := e.store.SaveNotice(ctx, e.org, r.ID, owner1, d.Report.Revision, d.PendingNotice, send, base); serr != nil || !saved || !slices.Equal(seqs, []int{1}) {
		t.Fatalf("notice: %v, %v, seqs %v", saved, serr, seqs)
	}
	if hid := e.peek.hidden(t, e.org, r.ID); hid.NoticeSeq != 1 || hid.PendingNotice != "" || hid.NotifiedRevision != 2 || hid.LastNoticeAt == nil {
		t.Fatalf("after the notice %+v", hid)
	}

	// The issue step handled the comments it read; one queued since stays.
	e.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}})
	if d, err = e.store.ClaimDelivery(ctx, e.org, r.ID, owner1, DeliveryLease); err != nil || !slices.Equal(d.IssueComments, []string{commentRepeated, commentResolved}) {
		t.Fatalf("claim %+v, %v", d, err)
	}
	e.clock.advance(time.Minute)
	e.observe(t, e.signal(SignalDeploymentFailed, "service:api", "dep_3#0"))
	at := base.Add(time.Hour)
	if err = e.store.SaveIssue(ctx, e.org, r.ID, owner1, d.Report.Revision, IssueSave{
		Number: 7, URL: "https://github.com/acme/api/issues/7", State: IssueClosed, Handled: len(d.IssueComments), CommentAt: &at, Done: true,
	}); err != nil {
		t.Fatal(err)
	}
	hid := e.peek.hidden(t, e.org, r.ID)
	if !slices.Equal(hid.IssueComments, []string{commentReopened}) || hid.IssueRevision != d.Report.Revision ||
		hid.IssueCommentAt == nil || !hid.IssueCommentAt.Equal(at) || hid.Issue.Number != 7 || hid.Issue.State != IssueClosed {
		t.Fatalf("after the issue %+v", hid)
	}
	if err = e.store.ReleaseDelivery(ctx, e.org, r.ID, owner1, true); err != nil {
		t.Fatal(err)
	}
	if pending, err := e.svc.Pending(ctx, e.org); err != nil || len(pending) != 1 {
		t.Fatalf("the reopen isn't pending: %v, %v", pending, err)
	}
}

func TestCloseIsRetriedWithoutASecondComment(t *testing.T) {
	runMem(t, "CloseIsRetriedWithoutASecondComment")
}
func TestLostLeaseOpensNoIssue(t *testing.T)           { runMem(t, "LostLeaseOpensNoIssue") }
func TestUnsavedNoticeHoldsBackGitHub(t *testing.T)    { runMem(t, "UnsavedNoticeHoldsBackGitHub") }
func TestStalledWorkerOpensNoSecondIssue(t *testing.T) { runMem(t, "StalledWorkerOpensNoSecondIssue") }
func TestDeletedIssueIsOpenedAgain(t *testing.T)       { runMem(t, "DeletedIssueIsOpenedAgain") }

// An issue someone deleted on GitHub is opened again, once, on the next
// change, and the report records the new one.
func deletedIssueIsOpenedAgain(t *testing.T, e *deliverEnv) {
	r := e.opened(t)
	e.deliver(t, r.ID)
	e.gh.DeleteIssue("acme/api", 1)
	e.clock.advance(2 * time.Hour)
	e.observe(t, e.signal(SignalDeploymentFailed, "service:api", "dep_2#0"))
	e.deliver(t, r.ID)
	if e.creates() != 2 {
		t.Fatalf("%d creates", e.creates())
	}
	issues := e.issues()
	if len(issues) != 2 || !issues[0].Deleted || issues[1].Deleted || !strings.Contains(issues[1].Body, "seen 2 times.") {
		t.Fatalf("issues %+v", issues)
	}
	got := e.lookup(t, r.ID)
	if got.Issue.Number != 2 || got.Issue.State != IssueOpen || got.Issue.Error != "" {
		t.Fatalf("report issue %+v", got.Issue)
	}
	if hid := e.peek.hidden(t, e.org, r.ID); hid.IssueRevision != got.Revision || len(hid.IssueComments) != 0 {
		t.Fatalf("delivery columns %+v", hid)
	}
	e.deliver(t, r.ID)
	if e.creates() != 2 {
		t.Fatalf("a delivery with nothing new opened another: %d creates", e.creates())
	}
}
func TestUnreadableSecretsSendNothing(t *testing.T) { runMem(t, "UnreadableSecretsSendNothing") }
func TestSecretsOfAGoneProjectAreSkipped(t *testing.T) {
	runMem(t, "SecretsOfAGoneProjectAreSkipped")
}

func TestCommentsAreRedactedBeforeTheyAreEscaped(t *testing.T) {
	runMem(t, "CommentsAreRedactedBeforeTheyAreEscaped")
}

// A close that fails after its comment went out is retried on its own.
func closeIsRetriedWithoutASecondComment(t *testing.T, e *deliverEnv) {
	r := e.opened(t)
	e.deliver(t, r.ID)
	e.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}, Reason: "fixed"})
	// The body edit goes through; the close after the comment doesn't.
	e.gh.FailNext("/repos/acme/api/issues/1", 0)
	e.gh.FailNext("/repos/acme/api/issues/1", http.StatusBadGateway)
	if err := e.svc.Deliver(context.Background(), e.org, r.ID, owner1); err == nil {
		t.Fatal("a failed close didn't fail the delivery")
	}
	if is := e.issues()[0]; is.State != "open" || len(is.Comments) != 1 {
		t.Fatalf("after the failed close %+v", is)
	}
	if hid := e.peek.hidden(t, e.org, r.ID); !slices.Equal(hid.IssueComments, []string{closePrefix + "completed"}) {
		t.Fatalf("queued %v", hid.IssueComments)
	}
	e.deliver(t, r.ID)
	if is := e.issues()[0]; is.State != "closed" || is.StateReason != "completed" || len(is.Comments) != 1 {
		t.Fatalf("after the retry %+v", is)
	}
	if hid := e.peek.hidden(t, e.org, r.ID); len(hid.IssueComments) != 0 || hid.Issue.State != IssueClosed {
		t.Fatalf("after the retry %+v", hid)
	}
}

// claimThief is a Store that hands the delivery to another worker after
// the first claim, as a lapsed lease would.
type claimThief struct {
	Store
	mu     sync.Mutex
	claims int
}

func (c *claimThief) RenewDelivery(context.Context, string, string, string, time.Duration) (*Delivery, error) {
	return nil, ErrLeaseLost
}

func (c *claimThief) ClaimDelivery(ctx context.Context, orgID, id, owner string, lease time.Duration) (*Delivery, error) {
	c.mu.Lock()
	c.claims++
	n := c.claims
	c.mu.Unlock()
	if n > 1 {
		return nil, nil
	}
	return c.Store.ClaimDelivery(ctx, orgID, id, owner, lease)
}

// A worker that lost its lease opens no issue and writes nothing more,
// and a write after the lease went elsewhere is refused.
func lostLeaseOpensNoIssue(t *testing.T, e *deliverEnv) {
	ctx := context.Background()
	r := e.opened(t)
	deps := e.svc.d
	deps.Store = &claimThief{Store: e.store}
	// Losing the delivery is a handoff, not a failure to retry.
	if err := NewService(deps).Deliver(ctx, e.org, r.ID, owner1); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if e.creates() != 0 || len(e.notices(t)) != len(everyone) {
		t.Fatalf("%d creates, %d notices", e.creates(), len(e.notices(t)))
	}
	if got := e.lookup(t, r.ID); got.Issue.State == IssueFailed {
		t.Fatalf("a worker without the lease recorded %+v", got.Issue)
	}

	if d, err := e.store.ClaimDelivery(ctx, e.org, r.ID, "worker_x", time.Millisecond); err != nil || d == nil {
		t.Fatalf("claim: %v, %v", d, err)
	}
	time.Sleep(20 * time.Millisecond)
	e.clock.advance(time.Second)
	if d, err := e.store.ClaimDelivery(ctx, e.org, r.ID, "worker_y", DeliveryLease); err != nil || d == nil {
		t.Fatalf("claim after the lease lapsed: %v, %v", d, err)
	}
	if err := e.store.SaveIssue(ctx, e.org, r.ID, "worker_x", r.Revision, IssueSave{Number: 9, State: IssueOpen}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("save without the lease: %v", err)
	}
	if got := e.lookup(t, r.ID); got.Issue.Number == 9 {
		t.Fatal("a worker without the lease wrote the issue")
	}
}

// staleFind answers Find as it did before another worker opened the
// issue: nothing yet.
type staleFind struct{ repositories.Issues }

func (staleFind) Find(context.Context, string, string) (*repositories.Issue, error) { return nil, nil }

type staleTracker struct{ repositories.IssueTracker }

func (s staleTracker) Issues(ctx context.Context, orgID, repo string) (repositories.Issues, error) {
	iss, err := s.IssueTracker.Issues(ctx, orgID, repo)
	if err != nil {
		return nil, err
	}
	return staleFind{iss}, nil
}

// Worker A claims, stalls past its lease, and has already looked for the
// issue; worker B claims, opens and records the issue and lets go. When A
// carries on, it can't take the delivery back up, and opens no second
// issue over B's.
func stalledWorkerOpensNoSecondIssue(t *testing.T, e *deliverEnv) {
	ctx := context.Background()
	r := e.opened(t)
	stale, err := e.store.ClaimDelivery(ctx, e.org, r.ID, "worker_a", time.Millisecond)
	if err != nil || stale == nil {
		t.Fatalf("claim: %v, %v", stale, err)
	}
	time.Sleep(20 * time.Millisecond)
	e.clock.advance(time.Second)
	if err = e.svc.Deliver(ctx, e.org, r.ID, "worker_b"); err != nil {
		t.Fatal(err)
	}
	if e.creates() != 1 {
		t.Fatalf("worker B: %d creates", e.creates())
	}

	deps := e.svc.d
	deps.Issues = staleTracker{deps.Issues}
	a := NewService(deps)
	bound, _, err := a.bind(ctx, e.org)
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.sender(bound, stale.Report)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.deliverIssue(bound, out, stale, "worker_a"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("worker A: %v", err)
	}
	if e.creates() != 1 || len(e.issues()) != 1 {
		t.Fatalf("%d creates", e.creates())
	}
	if got := e.lookup(t, r.ID); got.Issue.Number != 1 || got.Issue.State != IssueOpen {
		t.Fatalf("report issue %+v", got.Issue)
	}
	// Nor can A renew a lease it let lapse while nobody else took it.
	if _, err = e.store.ClaimDelivery(ctx, e.org, r.ID, "worker_c", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	e.clock.advance(time.Second)
	if _, err = e.store.RenewDelivery(ctx, e.org, r.ID, "worker_c", DeliveryLease); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("renew a lapsed lease: %v", err)
	}
	if _, err = e.store.RenewDelivery(ctx, e.org, "arep_gone", "worker_c", DeliveryLease); !errors.Is(err, ErrNotFound) {
		t.Fatalf("renew a missing report: %v", err)
	}
	if err = e.store.SaveIssue(ctx, e.org, "arep_gone", "worker_c", 1, IssueSave{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("save a missing report: %v", err)
	}
}

// A failed read of the secrets sends nothing, and the job retries.
func unreadableSecretsSendNothing(t *testing.T, e *deliverEnv) {
	r := e.opened(t)
	if err := e.svc.Deliver(context.Background(), e.org, r.ID, owner1); err == nil || !strings.Contains(err.Error(), "vault is down") {
		t.Fatalf("deliver: %v", err)
	}
	if len(e.notices(t)) != 0 || len(e.gh.Requests()) != 0 {
		t.Fatal("delivered without the report's secrets")
	}
	if hid := e.peek.hidden(t, e.org, r.ID); hid.NotifiedRevision != 0 || hid.IssueRevision != 0 {
		t.Fatalf("delivery columns %+v", hid)
	}
}

// A project that no longer exists has no secrets left to leak: delivery
// logs it and goes on, with the scan still in the way.
func secretsOfAGoneProjectAreSkipped(t *testing.T, e *deliverEnv) {
	r := e.opened(t)
	e.deliver(t, r.ID)
	if is := e.issues()[0]; !strings.HasPrefix(is.Body, Marker(r.ID)+"\n\nLoomplane opened a report for this repository.") {
		t.Fatalf("the scan didn't run:\n%s", is.Body)
	}
	if n := len(e.notices(t)); n != len(everyone) {
		t.Fatalf("%d notices", n)
	}
	if w := e.warned(); len(w) == 0 || !strings.Contains(w[0], "no longer exist") {
		t.Fatalf("warnings %v", w)
	}
}

// noticeRefuser is a Store whose report always moves on before the
// notice is saved.
type noticeRefuser struct{ Store }

func (noticeRefuser) SaveNotice(context.Context, string, string, string, int, string,
	func(context.Context, notifications.Execer, int) error, time.Time) (bool, error) {
	return false, nil
}

// A revision whose notice isn't recorded doesn't reach GitHub.
func unsavedNoticeHoldsBackGitHub(t *testing.T, e *deliverEnv) {
	r := e.opened(t)
	deps := e.svc.d
	deps.Store = noticeRefuser{Store: e.store}
	if err := NewService(deps).Deliver(context.Background(), e.org, r.ID, owner1); err != nil {
		t.Fatal(err)
	}
	if reqs := e.gh.Requests(); len(reqs) != 0 {
		t.Fatalf("GitHub was called before the notice: %v", reqs)
	}
	if pending, err := e.svc.Pending(context.Background(), e.org); err != nil || len(pending) != 1 {
		t.Fatalf("pending %v, %v", pending, err)
	}
}

// A comment is rendered from the redacted report, so escaping can't hide
// a secret from the redactor.
func commentsAreRedactedBeforeTheyAreEscaped(t *testing.T, e *deliverEnv) {
	r := e.opened(t)
	e.deliver(t, r.ID)
	e.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}, Reason: "Fixed by rotating s3<cr@et-Val"})
	e.deliver(t, r.ID)
	is := e.issues()[0]
	if len(is.Comments) != 1 || strings.Contains(is.Comments[0], "s3") || strings.Contains(is.Comments[0], "et-Val") ||
		!strings.Contains(is.Comments[0], "[redacted]") {
		t.Fatalf("comments %q", is.Comments)
	}
}

// countingEngine lets everything through and counts outbound scans.
type countingEngine struct {
	passEngine
	mu  sync.Mutex
	out int
}

func (c *countingEngine) ScanOutput(ctx context.Context, in *scan.Input) (*scan.Result, error) {
	c.mu.Lock()
	c.out++
	c.mu.Unlock()
	return c.passEngine.ScanOutput(ctx, in)
}

// An observer costs the issue's two scans, never the notice's.
func TestObserversScanOnlyTheIssueText(t *testing.T) {
	engine := &countingEngine{}
	e := newMemEnv(t, deliverOptions{engine: engine})
	e.svc.AddObserver(&fakeObserver{})
	e.opened(t)
	e.clear(t, Clearance{Signals: []string{SignalDeploymentFailed}})
	if engine.out != 2 {
		t.Fatalf("%d outbound scans", engine.out)
	}
}

func TestRecipientsAreAdminsTheTeamAndTheStarter(t *testing.T) {
	e := newMemEnv(t, deliverOptions{engine: passEngine{}})
	ctx := context.Background()
	for dep, want := range map[string][]string{
		"dep_1":     {userAdmin, userOwner, userTeam, userStarter},
		"dep_agent": {userAdmin, userOwner, userTeam},
		"dep_auto":  {userAdmin, userOwner, userTeam},
		"dep_gone":  {userAdmin, userOwner, userTeam},
		"":          {userAdmin, userOwner, userTeam},
	} {
		got, err := e.svc.recipients(ctx, &Report{OrgID: e.org, ProjectID: projA, Evidence: Evidence{DeploymentID: dep}})
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("%q: %v, %v", dep, got, err)
		}
	}
	// A project without a team of its own: the admins alone.
	got, err := e.svc.recipients(ctx, &Report{OrgID: e.org, ProjectID: projB})
	if err != nil || !slices.Equal(got, admins) {
		t.Fatalf("projB: %v, %v", got, err)
	}
}

// Only parts that no longer exist are passed over: a joined error with
// anything else in it fails closed.
func TestGoneSecretsAreOnlyNotFound(t *testing.T) {
	missing := fmt.Errorf("reveal TOKEN: %w", environments.ErrNotFound)
	for err, want := range map[error]bool{
		projects.ErrNotFound: true, missing: true, errors.Join(missing, missing): true,
		errors.Join(missing, errors.New("vault is down")): false, errors.New("vault is down"): false,
	} {
		if gone(err) != want {
			t.Fatalf("gone(%v) = %v", err, !want)
		}
	}
}

// A public URL with a port, as a server on its default address has, and
// IDs as the stores make them: the issue's links to Loomplane survive the
// outbound scan, which reads such a link as a secret, while an untrusted
// fact shaped like one is still scanned.
func TestLinksToLoomplaneSurviveTheScan(t *testing.T) {
	ctx, err := (&agentscope.Binder{Tenants: tenantsOn{}}).Bind(context.Background(), security.AgentPrincipal("org_a", nil), agentscope.Scope{OrgID: "org_a"})
	if err != nil {
		t.Fatal(err)
	}
	out := &sender{scanner: &agentsafety.Scanner{Engine: agenttest.Shield(t, "org_a")}, redact: realtime.NewRedactor(nil)}
	const (
		base    = "http://localhost:8780"
		id      = "arep_01m6jas8yyee2tbh724bczwdx1"
		plan    = "/deployments/plan_01m6jas4d0ehb8z6y2gp02yn8k?tab=changes"
		planned = base + plan
		smuggle = base + "/agent/reports/arep_01m6jas8yyee2tbh724bczwdy9"
	)
	r := &Report{ID: id, Title: "Deployment to production failed", Severity: SeverityHigh, Environment: "production", Occurrences: 1,
		Facts: []Fact{{Label: "Plan", Value: "plan_01m6jas4d0ehb8z6y2gp02yn8k", Path: plan}, {Label: "Error", Value: "see " + smuggle, Untrusted: true}}}
	n := names{Project: "Shop", Link: base + "/agent/reports/" + id}
	_, body := (&Service{}).issueTexts(ctx, out, r, n)
	if !strings.HasPrefix(body, Marker(id)+"\n") || !strings.Contains(body, "[Open this report in Loomplane]("+n.Link+")") ||
		!strings.Contains(body, "([open in Loomplane]("+planned+"))") {
		t.Fatalf("body %s", body)
	}
	if strings.Contains(body, smuggle) || strings.Contains(body, linkHold) {
		t.Fatalf("the untrusted link wasn't scanned, or a placeholder was left: %s", body)
	}

	// Text that already holds a placeholder is scanned whole.
	text := "Read " + linkHold + "0-held at " + n.Link
	if got, ok := out.textWith(ctx, text, []string{n.Link}); !ok || strings.Contains(got, n.Link) {
		t.Fatalf("textWith(%q) = %q, %v", text, got, ok)
	}
}
