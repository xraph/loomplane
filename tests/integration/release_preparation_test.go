//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/agentrequests"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/releaseprep"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

// The release scenarios run the whole app with the agent on and a
// scripted model. A project has two sources: api, which each release
// promotes, and web, which it holds. Production ran both once; staging
// then deployed a newer api. The evidence that needs a cloud's monitoring
// or GitHub (the soak, the watch, canaries, open reports and diffs) comes
// from fakes the test sets; the plans, the project, the budget and every
// permission are the app's own.

const (
	// releaseCommitA is what production runs of both sources.
	releaseCommitA = exampleSHA
	// releaseCommitB is api's newer commit, which staging runs.
	releaseCommitB = "b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1"
)

// releaseManifest runs api and web on Cloud Run. Production releases as a
// canary, so approving a release starts its canary.
const releaseManifest = `version: 1
services:
  api:
    repo: api
    source: .
    provider: cloud-run
    port: 8080
  web:
    repo: web
    source: .
    provider: cloud-run
    port: 8080
environments:
  production:
    release:
      strategy: canary
      steps: [50, 100]
      hold: 1m
`

// releaseManifestWithFiles is releaseManifest plus a bucket api needs.
const releaseManifestWithFiles = `version: 1
services:
  api:
    repo: api
    source: .
    provider: cloud-run
    port: 8080
    needs: [files]
  web:
    repo: web
    source: .
    provider: cloud-run
    port: 8080
resources:
  files:
    kind: object-storage
    provider: cloud-storage
environments:
  production:
    release:
      strategy: canary
      steps: [50, 100]
      hold: 1m
`

// apiFiles is a Go HTTP service in a repository of its own.
var apiFiles = map[string]string{
	"go.mod":  "module example.com/api\n\ngo 1.26\n",
	"main.go": "package main\n\nimport \"net/http\"\n\nfunc main() { _ = http.ListenAndServe(\":8080\", nil) }\n",
}

// webFiles is a Next.js app in a repository of its own.
var webFiles = map[string]string{
	"package.json": `{"name": "web", "scripts": {"build": "next build", "start": "next start"}, "dependencies": {"next": "15.0.0", "react": "19.0.0"}}`,
	"app/page.tsx": "export default function Page() { return null }\n",
}

// releaseGitHub serves acme/api, whose main moves when the test pushes,
// and acme/web, which stays at releaseCommitA.
type releaseGitHub struct {
	mu   sync.Mutex
	head string
	srv  *httptest.Server
}

func newReleaseGitHub(t *testing.T) *releaseGitHub {
	t.Helper()
	g := &releaseGitHub{head: releaseCommitA}
	tarballs := map[string][]byte{"acme/api": tarball(t, "acme-api-fedcba9", apiFiles), "acme/web": tarball(t, "acme-web-fedcba9", webFiles)}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, tgz := range tarballs {
			switch {
			case r.URL.Path == "/repos/"+name:
				_, _ = w.Write([]byte(`{"default_branch":"main"}`))
				return
			case r.URL.Path == "/repos/"+name+"/commits/main":
				head := releaseCommitA
				if name == "acme/api" {
					g.mu.Lock()
					head = g.head
					g.mu.Unlock()
				}
				_, _ = w.Write([]byte(head))
				return
			case strings.HasPrefix(r.URL.Path, "/repos/"+name+"/tarball/"):
				_, _ = w.Write(tgz)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *releaseGitHub) push(commit string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.head = commit
}

// releaseWatches is releasewatch.Service as the scenario sets it: a soak,
// each environment's current release and production's coverage. It counts
// its calls, and can hold the next SoakForPlan until the test lets it go.
type releaseWatches struct {
	mu         sync.Mutex
	soak       releasewatch.Soak
	staging    releasewatch.ReleaseEvidence
	production releasewatch.ReleaseEvidence
	coverage   releasewatch.Coverage
	calls      int
	hold       chan struct{}
	held       chan struct{}
}

// greenWatches is a healthy staging soaked for two hours, verified
// releases in both environments, and a 15 minute watch that can judge
// every production service.
func greenWatches() *releaseWatches {
	return &releaseWatches{
		soak: releasewatch.Soak{
			Environment: "staging", Running: true, Since: time.Now().Add(-2 * time.Hour), Duration: 2 * time.Hour, Healthy: true,
			Services: []releasewatch.SoakService{{Name: "api", Provider: "cloud-run", Verdict: releasehealth.Pass}},
		},
		staging: releasewatch.ReleaseEvidence{Environment: "staging", DeploymentID: "dep_staging", Watched: true, Settled: true, Verdict: string(releasehealth.Pass)},
		production: releasewatch.ReleaseEvidence{
			Environment: "production", DeploymentID: "dep_production", Watched: true, Settled: true, Verdict: string(releasehealth.Pass),
		},
		coverage: releasewatch.Coverage{Environment: "production", Duration: 15 * time.Minute, Services: []releasewatch.ServiceCoverage{
			{Name: "api", Provider: "cloud-run", Watchable: true}, {Name: "web", Provider: "cloud-run", Watchable: true},
		}},
	}
}

func (w *releaseWatches) set(change func(w *releaseWatches)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	change(w)
}

func (w *releaseWatches) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

// holdNext makes the next SoakForPlan wait until release is called. held
// closes once a call is waiting.
func (w *releaseWatches) holdNext() (held <-chan struct{}, release func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.hold, w.held = make(chan struct{}), make(chan struct{})
	h := w.hold
	var once sync.Once
	return w.held, func() { once.Do(func() { close(h) }) }
}

func (w *releaseWatches) SoakForPlan(ctx context.Context, _ security.Principal, _ string) (*releasewatch.Soak, error) {
	w.mu.Lock()
	w.calls++
	hold, held := w.hold, w.held
	w.hold, w.held = nil, nil
	w.mu.Unlock()
	if hold != nil {
		close(held)
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.soak
	s.Services = slices.Clone(s.Services)
	return &s, nil
}

func (w *releaseWatches) Armable(_ context.Context, _ security.Principal, _, _ string) (*releasewatch.Coverage, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	c := w.coverage
	c.Services = slices.Clone(c.Services)
	return &c, nil
}

func (w *releaseWatches) Evidence(_ context.Context, _ security.Principal, _, environment string) (*releasewatch.ReleaseEvidence, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	e := w.production
	if environment != "production" {
		e = w.staging
	}
	return &e, nil
}

// noCanaries: no production canary ever ramped a changed service.
type noCanaries struct{}

func (noCanaries) LastCanary(context.Context, security.Principal, string, string, string) (*deployments.CanaryRun, error) {
	return nil, nil
}

// noOpenReports: nothing is open on staging.
type noOpenReports struct{}

func (noOpenReports) Open(context.Context, string, string, string) ([]releaseprep.OpenReport, error) {
	return []releaseprep.OpenReport{}, nil
}

// oneCommitAhead is GitHub's compare: staging is one commit ahead.
type oneCommitAhead struct{}

func (oneCommitAhead) Compare(_ context.Context, _, _, _, head string) (*repositories.Comparison, error) {
	return &repositories.Comparison{
		Status: "ahead", AheadBy: 1, TotalCommits: 1,
		Commits: []repositories.ComparedCommit{{SHA: head, Message: "Serve orders faster", Author: "Ada", Date: time.Now().Add(-3 * time.Hour)}},
		Files:   []repositories.ChangedFile{{Filename: "main.go", Status: "modified", Additions: 3, Deletions: 1}},
	}, nil
}

// releaseRig is a release scenario: the app, the project and the fakes.
type releaseRig struct {
	*scenario
	project  string
	base     string
	svc      *releaseprep.Service
	watches  *releaseWatches
	prodEnv  string
	stageEnv string
	// staging is staging's deployed plan.
	staging planBody
}

// releaseOptions shape a rig.
type releaseOptions struct {
	// production is the manifest production first deploys; staging's is
	// releaseManifest.
	production string
}

// newReleaseRig starts the app on fake and builds the project: Shop, with
// sources api (acme/api) and web (acme/web), a staging environment, and
// Ada as owner. Production deploys both at A; staging then deploys api at
// B and web at A. The release evidence that isn't the app's own is a
// green fake from then on.
func newReleaseRig(t *testing.T, fake *agenttest.FakeAnthropic, o releaseOptions) *releaseRig {
	t.Helper()
	google := newFakeGoogle(t)
	// Production releases as a canary, which needs Cloud Run's revisions.
	google.grant(append(slices.Clone(gcp.Permissions), gcp.CanaryPermissions...))
	cloud := newFakeCloud(google.srv.URL)
	google.deploy = cloud
	gh := newReleaseGitHub(t)
	s := startScenario(t, fake, "25", func(cfg *config.Config) {
		cfg.GitHub.APIURL = gh.srv.URL
		cfg.Clouds.GoogleEndpoint = google.srv.URL
	})
	t.Cleanup(s.r.stop)
	g := &releaseRig{scenario: s}
	var proj struct {
		ID string `json:"id"`
	}
	s.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Shop"}, &proj)
	g.project, g.base = proj.ID, "/api/v1/projects/"+proj.ID
	s.owner.must(http.StatusCreated, http.MethodPost, g.base+"/sources", map[string]string{"repository": "acme/api", "alias": "api"}, nil)
	s.owner.must(http.StatusCreated, http.MethodPost, g.base+"/sources", map[string]string{"repository": "acme/web", "alias": "web"}, nil)
	s.owner.must(http.StatusCreated, http.MethodPost, g.base+"/environments", map[string]string{"name": "staging"}, nil)
	analyzeAll(t, s.cfg, s.owner, proj.ID)
	s.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "gcp", "name": "Prod", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, nil)

	production := o.production
	if production == "" {
		production = releaseManifest
	}
	s.owner.must(http.StatusCreated, http.MethodPut, g.base+"/manifest", map[string]string{"yaml": production}, nil)
	first := planRelease(t, s.owner, g.base+"/plans", map[string]string{"environment": "production"})
	if first.Status != "ready" {
		t.Fatalf("production's first plan: %+v", first)
	}
	g.prodEnv = first.EnvironmentID
	if d := approveAndWait(t, s.owner, first.planBody); d.Status != "succeeded" {
		t.Fatalf("production's first deployment: %+v", d)
	}
	if production != releaseManifest {
		s.owner.must(http.StatusCreated, http.MethodPut, g.base+"/manifest", map[string]string{"yaml": releaseManifest}, nil)
	}

	gh.push(releaseCommitB)
	analyzeAll(t, s.cfg, s.owner, proj.ID)
	staging := planRelease(t, s.owner, g.base+"/plans", map[string]string{"environment": "staging"})
	if staging.Status != "ready" {
		t.Fatalf("staging's plan: %+v", staging)
	}
	g.stageEnv, g.staging = staging.EnvironmentID, staging.planBody
	if d := approveAndWait(t, s.owner, staging.planBody); d.Status != "succeeded" {
		t.Fatalf("staging's deployment: %+v", d)
	}

	g.svc = inject[*releaseprep.Service](t, s.container())
	g.watches = greenWatches()
	src := g.svc.Collector().Sources
	src.Watches, src.Canaries, src.Reports, src.Diffs = g.watches, noCanaries{}, noOpenReports{}, oneCommitAhead{}
	g.svc.SetSources(src)
	return g
}

// promotion is the plan c makes to promote api from staging to
// production, ready.
func (g *releaseRig) promotion(t *testing.T, c *client) releasePlan {
	t.Helper()
	p := planRelease(t, c, g.base+"/plans", map[string]any{"environment": "production", "from": "staging", "scope": []string{"api"}})
	if p.Status != "ready" || p.Kind != "promotion" {
		t.Fatalf("the promotion: %+v", p)
	}
	return p
}

// prepare asks for the release of api as c, from the dashboard.
func (g *releaseRig) prepare(t *testing.T, c *client) releaseprep.ReleaseReport {
	t.Helper()
	var r releaseprep.ReleaseReport
	c.must(http.StatusAccepted, http.MethodPost, g.base+"/release-reports", map[string]any{"scope": []string{"api"}}, &r)
	return r
}

// report is the report as Ada reads it.
func (g *releaseRig) report(t *testing.T, id string) releaseprep.ReleaseReport {
	t.Helper()
	var r releaseprep.ReleaseReport
	g.owner.must(http.StatusOK, http.MethodGet, "/api/v1/release-reports/"+id, nil, &r)
	return r
}

// task waits for the report's review to start and returns its task ID.
func (g *releaseRig) task(t *testing.T, id string) string {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		r := g.report(t, id)
		if r.TaskID != "" {
			return r.TaskID
		}
		if r.Status != releaseprep.StatusPlanning {
			t.Fatalf("the report left planning with no review: %+v", r)
		}
		if time.Now().After(deadline) {
			t.Fatalf("no review started for %s", id)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// finish waits for the review's run to end, as Ada reads it.
func (g *releaseRig) finish(t *testing.T, taskID string) *agentruns.Task {
	t.Helper()
	return waitFor(t, inject[*agentruns.Service](t, g.container()), g.ownerPrincipal(), taskID, finished)
}

// settle runs one settle sweep, as the cron does, and returns the report.
func (g *releaseRig) settle(t *testing.T, id string) releaseprep.ReleaseReport {
	t.Helper()
	if err := g.svc.Settle(context.Background()); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	return g.report(t, id)
}

// requestsOf are the agent's requests of a task, in any status.
func (g *releaseRig) requestsOf(t *testing.T, taskID string) []*agentrequests.Request {
	t.Helper()
	var list agentrequests.RequestList
	g.owner.must(http.StatusOK, http.MethodGet, "/api/v1/agent/requests?status=all", nil, &list)
	var out []*agentrequests.Request
	for _, r := range list.Requests {
		if r.TaskID == taskID {
			out = append(out, r)
		}
	}
	return out
}

// notified are who got the report's notice for state, sorted.
func (g *releaseRig) notified(t *testing.T, reportID, state string) []string {
	t.Helper()
	rows, err := g.db.Query(context.Background(), `SELECT user_id FROM loomplane_notification_outbox
		WHERE org_id = $1 AND kind = 'agent.release' AND subject_id = $2 ORDER BY user_id`, g.orgID, reportID+":"+state)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// releaseNotices counts every release notice of the workspace.
func (g *releaseRig) releaseNotices(t *testing.T) int {
	t.Helper()
	var n int
	if err := g.db.QueryRow(context.Background(), `SELECT count(*) FROM loomplane_notification_outbox WHERE org_id = $1 AND kind = 'agent.release'`, g.orgID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// deployment waits up to wait for the plan's deployment and returns it,
// or nil when there's none by then.
func (g *releaseRig) deployment(t *testing.T, planID string, wait time.Duration) *deployments.Deployment {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		var d deployments.Deployment
		if g.owner.do(http.MethodGet, "/api/v1/plans/"+planID+"/deployment", nil, &d) != http.StatusNotFound {
			return &d
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// gateAudit are the allowed flags of the gate's evaluations of a report.
func (g *releaseRig) gateAudit(t *testing.T, reportID string) []string {
	t.Helper()
	rows, err := g.db.Query(context.Background(), `SELECT coalesce(metadata->>'allowed','') FROM chronicle_events
		WHERE action = 'release.gate.evaluate' AND tenant_id = $1 AND metadata->>'report_id' = $2 ORDER BY sequence`, g.orgID, reportID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// recordRelease records that source now runs commit in an environment,
// as a deployment of it would: its release, and the commit and revision
// its service's binding runs.
func (g *releaseRig) recordRelease(t *testing.T, environmentID, alias, commit string) {
	t.Helper()
	var sourceID, analysisID string
	for _, s := range g.staging.Sources {
		if s.Alias == alias {
			sourceID, analysisID = s.SourceID, s.AnalysisID
		}
	}
	if sourceID == "" {
		t.Fatalf("staging's plan has no source %s", alias)
	}
	err := inject[*planning.PgStore](t, g.container()).RecordRelease(context.Background(), g.orgID, &planning.DeployedRelease{
		EnvironmentID: environmentID, PlanID: g.staging.ID, DeployedAt: time.Now().UTC(),
		Sources: []*planning.DeployedSource{{EnvironmentID: environmentID, SourceID: sourceID, AnalysisID: analysisID, CommitSHA: commit}},
	})
	if err != nil {
		t.Fatalf("RecordRelease: %v", err)
	}
	tag, err := g.db.Exec(context.Background(), `UPDATE loomplane_bindings SET commit_sha = $1, revision = $2
		WHERE org_id = $3 AND environment_id = $4 AND subject = 'service' AND name = $5`, commit, alias+"-"+commit[:7], g.orgID, environmentID, alias)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("%s's binding in %s: %d rows, %v", alias, environmentID, tag.RowsAffected(), err)
	}
}

// waitForSecondTurn waits until the review has asked the model for its
// second turn, which the script holds with Wait until the test lets it go.
func waitForSecondTurn(t *testing.T, fake *agenttest.FakeAnthropic) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for len(fake.Requests()) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the model had %d requests, want 2", len(fake.Requests()))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// reviewTurns script the review: read the report, approve the plan, and a
// summary. The inputs are filled in once the test knows them.
func reviewTurns(getReport, approve *lateInput) []agenttest.Turn {
	return []agenttest.Turn{
		{ToolUses: []agenttest.ToolUse{{Name: "get_release_report", Input: getReport}}},
		{ToolUses: []agenttest.ToolUse{{Name: "approve_plan", Input: approve}}},
		{Text: "Reviewed the release."},
	}
}

// failedGates are the numbers of the gates that didn't pass.
func failedGates(gates []releaseprep.GateCheck) []int {
	out := []int{}
	for _, g := range gates {
		if !g.Passed {
			out = append(out, g.Number)
		}
	}
	return out
}

// With autonomous promotion on and every gate green, a release Ada asks
// for from the dashboard is approved by the agent through the approval
// workflow, at the revision it read: the plan's canary starts as that one
// decision, and no person is asked. Ada and the workspace's admins hear
// that it was promoted, once.
func TestReleasePrep_allGreenPromotes(t *testing.T) {
	getReport, approve := &lateInput{}, &lateInput{}
	fake := agenttest.Anthropic(t, reviewTurns(getReport, approve)...)
	g := newReleaseRig(t, fake, releaseOptions{})
	benID := g.member(t, "ben@example.com", "admin", "Ben")
	g.member(t, "dev@example.com", "developer", "Dev")
	g.autonomy(t, g.project)
	plan := g.promotion(t, g.owner)
	approve.set(map[string]any{"plan_id": plan.ID, "revision": plan.Revision})

	r := g.prepare(t, g.owner)
	getReport.set(map[string]any{"report_id": r.ID})
	if !r.ReusedPlan || r.PlanID != plan.ID || r.Via != releaseprep.ViaDashboard {
		t.Fatalf("the report %+v", r)
	}
	taskID := g.task(t, r.ID)
	if final := g.finish(t, taskID); final.Status != agentruns.StatusCompleted {
		t.Fatalf("the review %+v", final)
	}

	p := g.planStatus(t, plan.ID)
	if p.Status != "approved" || p.DecidedBy != security.AgentSubject(g.orgID) {
		t.Fatalf("the plan %+v", p)
	}
	d := g.deployment(t, plan.ID, 30*time.Second)
	if d == nil || d.Canary == nil {
		t.Fatalf("the approval started no canary: %+v", d)
	}
	if reqs := g.requestsOf(t, taskID); len(reqs) != 0 {
		t.Fatalf("the agent asked a person: %+v", reqs[0])
	}

	got := g.settle(t, r.ID)
	if got.State != releaseprep.StatePromoted || got.Preview || got.Decision != releaseprep.DecisionPromote || len(got.Gates) != 7 {
		t.Fatalf("the settled report %+v", got)
	}
	if failed := failedGates(got.Gates); len(failed) != 0 {
		t.Fatalf("gates %v failed: %+v", failed, got.Gates)
	}
	// The budget is the workspace's own: $25 for the agent, with the
	// watch's $2.50 kept free.
	if b := got.Gates[releaseprep.GateBudget-1].Detail; !strings.Contains(b, "of $25.00 keeps the $2.50 reserve") {
		t.Fatalf("gate 7 says %q", b)
	}
	audit := g.gateAudit(t, r.ID)
	if len(audit) == 0 || slices.ContainsFunc(audit, func(a string) bool { return a != "true" }) {
		t.Fatalf("the gate's audit %v", audit)
	}
	want := []string{g.userID(), benID}
	slices.Sort(want)
	if got := g.notified(t, r.ID, releaseprep.StatePromoted); !slices.Equal(got, want) {
		t.Fatalf("the promoted notice reached %v, want Ada and the admins %v", got, want)
	}
	before := g.releaseNotices(t)
	g.settle(t, r.ID)
	if after := g.releaseNotices(t); after != before || before != len(want) {
		t.Fatalf("%d release notices, then %d after another settle", before, after)
	}
	if n := len(fake.Requests()); n != 3 {
		t.Fatalf("the model got %d requests", n)
	}
}

// join signs a new person in as role, named first, and returns their
// client and user ID.
func (g *releaseRig) join(t *testing.T, email, role, first string) (*client, string) {
	t.Helper()
	c := joinAs(t, g.agentOrg, email, role)
	var me struct {
		UserID string `json:"user_id"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/session", nil, &me)
	g.rename(me.UserID, first)
	return c, me.UserID
}

// waiting waits for the review to wait on a person and returns the report.
func (g *releaseRig) waiting(t *testing.T, reportID, taskID string) releaseprep.ReleaseReport {
	t.Helper()
	runs := inject[*agentruns.Service](t, g.container())
	task := waitFor(t, runs, g.ownerPrincipal(), taskID, func(tk *agentruns.Task) bool { return tk.Status == agentruns.StatusWaiting || finished(tk) })
	if task.Status != agentruns.StatusWaiting {
		t.Fatalf("the review %+v", task)
	}
	return g.report(t, reportID)
}

// With autonomous promotion off, the review asks a person through the
// approval card, in 3a's words, and the gate is never consulted: the
// report keeps the job's preview. Ada approves in the API with the
// revision; the run resumes and ends, and the report says Ada approved
// it.
func TestReleasePrep_autonomyOffAsks(t *testing.T) {
	getReport, approve := &lateInput{}, &lateInput{}
	fake := agenttest.Anthropic(t, reviewTurns(getReport, approve)...)
	g := newReleaseRig(t, fake, releaseOptions{})
	plan := g.promotion(t, g.owner)
	approve.set(map[string]any{"plan_id": plan.ID, "revision": plan.Revision})
	r := g.prepare(t, g.owner)
	getReport.set(map[string]any{"report_id": r.ID})
	taskID := g.task(t, r.ID)

	req := g.pendingRequest(t, taskID, agentrequests.KindApprovePlan)
	if req.Question != "Needs a person: autonomous promotion is off for this project" || req.Target.PlanID != plan.ID || req.Target.PlanRevision != plan.Revision {
		t.Fatalf("the request %+v", req)
	}
	got := g.waiting(t, r.ID, taskID)
	if got.State != releaseprep.StateWaiting || !got.Preview || got.RequestID != req.ID {
		t.Fatalf("the waiting report %+v", got)
	}
	if failed := failedGates(got.Gates); !slices.Equal(failed, []int{releaseprep.GateAutonomy}) {
		t.Fatalf("the preview fails gates %v: %+v", failed, got.Gates)
	}
	if p := g.planStatus(t, plan.ID); p.Status != "ready" {
		t.Fatalf("the plan is %s while it waits", p.Status)
	}
	if audit := g.gateAudit(t, r.ID); len(audit) != 0 {
		t.Fatalf("the gate was consulted: %v", audit)
	}

	g.owner.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
	if final := g.finish(t, taskID); final.Status != agentruns.StatusCompleted {
		t.Fatalf("the review %+v", final)
	}
	if msg := lastMessage(t, fake, 2); !strings.Contains(msg, "approved by Ada") {
		t.Fatalf("the model read %s", msg)
	}
	got = g.settle(t, r.ID)
	if got.State != releaseprep.StateApproved || got.DecidedBy != g.userID() || got.StateReason != "approved by "+g.userID() {
		t.Fatalf("the settled report %+v", got)
	}
	if d := g.deployment(t, plan.ID, 30*time.Second); d == nil {
		t.Fatal("Ada's approval started no deployment")
	}
}

// With autonomous promotion on and staging soaked for 18 minutes of the
// hour, the request names gate 4, and the report shows gate 4 failed and
// every other gate passed.
func TestReleasePrep_failedGateAsksAndNamesIt(t *testing.T) {
	getReport, approve := &lateInput{}, &lateInput{}
	fake := agenttest.Anthropic(t, reviewTurns(getReport, approve)...)
	g := newReleaseRig(t, fake, releaseOptions{})
	g.autonomy(t, g.project)
	g.watches.set(func(w *releaseWatches) {
		w.soak.Since, w.soak.Duration = time.Now().Add(-18*time.Minute), 18*time.Minute
	})
	plan := g.promotion(t, g.owner)
	approve.set(map[string]any{"plan_id": plan.ID, "revision": plan.Revision})
	r := g.prepare(t, g.owner)
	getReport.set(map[string]any{"report_id": r.ID})
	taskID := g.task(t, r.ID)

	req := g.pendingRequest(t, taskID, agentrequests.KindApprovePlan)
	if !strings.HasPrefix(req.Question, "Needs a person: ") || !strings.Contains(req.Question, "gate 4 (staging soak)") ||
		!strings.Contains(req.Question, "staging has soaked 18m of the 1h minimum") {
		t.Fatalf("the request %+v", req)
	}
	got := g.waiting(t, r.ID, taskID)
	if got.State != releaseprep.StateWaiting || got.Preview || got.Decision != releaseprep.DecisionAsk {
		t.Fatalf("the report %+v", got)
	}
	if failed := failedGates(got.Gates); !slices.Equal(failed, []int{releaseprep.GateSoak}) {
		t.Fatalf("gates %v failed, want only 4: %+v", failed, got.Gates)
	}
	if p := g.planStatus(t, plan.ID); p.Status != "ready" || p.DecidedBy != "" {
		t.Fatalf("the plan %+v", p)
	}
}

// Every gate passes, but the agent notes a concern that asks: the
// release waits for a person, the request says why in the agent's words,
// and the agent approves nothing.
func TestReleasePrep_modelConcernAsks(t *testing.T) {
	getReport, note, approve := &lateInput{}, &lateInput{}, &lateInput{}
	fake := agenttest.Anthropic(t,
		agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "get_release_report", Input: getReport}}},
		agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "note_release_concern", Input: note}}},
		agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "approve_plan", Input: approve}}},
		agenttest.Turn{Text: "A person should look at the migration."},
	)
	g := newReleaseRig(t, fake, releaseOptions{})
	g.autonomy(t, g.project)
	plan := g.promotion(t, g.owner)
	approve.set(map[string]any{"plan_id": plan.ID, "revision": plan.Revision})
	r := g.prepare(t, g.owner)
	getReport.set(map[string]any{"report_id": r.ID})
	note.set(map[string]any{"report_id": r.ID, "kind": "ask", "text": "the migration drops users.legacy_id"})
	taskID := g.task(t, r.ID)

	req := g.pendingRequest(t, taskID, agentrequests.KindApprovePlan)
	if !strings.Contains(req.Question, "the agent asked for a person: the migration drops users.legacy_id") {
		t.Fatalf("the request %+v", req)
	}
	got := g.waiting(t, r.ID, taskID)
	if failed := failedGates(got.Gates); len(failed) != 0 || got.Decision != releaseprep.DecisionAsk {
		t.Fatalf("the report fails gates %v, decision %q: %+v", failed, got.Decision, got.Gates)
	}
	if len(got.Concerns) != 1 || got.Concerns[0].Kind != releaseprep.ConcernAsk {
		t.Fatalf("concerns %+v", got.Concerns)
	}
	if p := g.planStatus(t, plan.ID); p.Status != "ready" || p.DecidedBy != "" {
		t.Fatalf("the plan %+v", p)
	}
}

// Production last ran a bucket staging's manifest dropped, so the
// promotion deletes it: with autonomy on and every source green, the
// release still waits for a person, and the gate is never consulted. The
// report's preview names the delete under gate 3.
func TestReleasePrep_destroyPausesWithAutonomyOn(t *testing.T) {
	getReport, approve := &lateInput{}, &lateInput{}
	served := make(chan struct{})
	fake := agenttest.Anthropic(t,
		agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "get_release_report", Input: getReport}}},
		agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "approve_plan", Input: approve}}, Wait: served},
		agenttest.Turn{Text: "A person decides the bucket."},
	)
	g := newReleaseRig(t, fake, releaseOptions{production: releaseManifestWithFiles})
	g.autonomy(t, g.project)
	plan := g.promotion(t, g.owner)
	if !slices.ContainsFunc(plan.Actions, func(a struct {
		ID       string `json:"id"`
		Op       string `json:"op"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
		Step     int    `json:"step"`
	}) bool {
		return a.Name == "files" && a.Op == "delete"
	}) {
		t.Fatalf("the promotion doesn't delete the bucket: %+v", plan.Actions)
	}
	approve.set(map[string]any{"plan_id": plan.ID, "revision": plan.Revision})
	r := g.prepare(t, g.owner)
	getReport.set(map[string]any{"report_id": r.ID})
	taskID := g.task(t, r.ID)

	// Whatever the evidence read before approve_plan is the job's.
	waitForSecondTurn(t, fake)
	before := g.watches.count()
	close(served)
	req := g.pendingRequest(t, taskID, agentrequests.KindApprovePlan)
	if req.Question != "Removes or replaces production data or resources, which always needs a person" {
		t.Fatalf("the request %+v", req)
	}
	if after := g.watches.count(); after != before {
		t.Fatalf("approve_plan read the watch %d times: the gate was consulted", after-before)
	}
	if audit := g.gateAudit(t, r.ID); len(audit) != 0 {
		t.Fatalf("the gate was consulted: %v", audit)
	}
	got := g.waiting(t, r.ID, taskID)
	if !got.Preview || len(got.Gates) != 7 || got.Gates[2].Passed ||
		!strings.Contains(got.Gates[2].Detail, "removes or replaces production data or resources (") || !strings.Contains(got.Gates[2].Detail, "files") {
		t.Fatalf("the preview's gate 3 %+v", got.Gates)
	}
	if p := g.planStatus(t, plan.ID); p.Status != "ready" || p.DecidedBy != "" {
		t.Fatalf("the plan %+v", p)
	}
}

// Production's last release is unverified: gate 5 fails and the release
// asks. The request says so in Loomplane's words; the watch's own reason
// is never quoted.
func TestReleasePrep_unverifiedWatchAsks(t *testing.T) {
	getReport, approve := &lateInput{}, &lateInput{}
	fake := agenttest.Anthropic(t, reviewTurns(getReport, approve)...)
	g := newReleaseRig(t, fake, releaseOptions{})
	g.autonomy(t, g.project)
	g.watches.set(func(w *releaseWatches) {
		w.production.Verdict, w.production.Unverified, w.production.Reason = string(releasehealth.Inconclusive), true, "too little traffic to judge"
	})
	plan := g.promotion(t, g.owner)
	approve.set(map[string]any{"plan_id": plan.ID, "revision": plan.Revision})
	r := g.prepare(t, g.owner)
	getReport.set(map[string]any{"report_id": r.ID})
	taskID := g.task(t, r.ID)

	req := g.pendingRequest(t, taskID, agentrequests.KindApprovePlan)
	if !strings.Contains(req.Question, "gate 5 (safety net): the last production release is unverified: its post-deploy watch couldn't judge it") ||
		strings.Contains(req.Question, "too little traffic") {
		t.Fatalf("the request %+v", req)
	}
	got := g.waiting(t, r.ID, taskID)
	if failed := failedGates(got.Gates); !slices.Equal(failed, []int{releaseprep.GateSafetyNet}) {
		t.Fatalf("gates %v failed: %+v", failed, got.Gates)
	}
	if p := g.planStatus(t, plan.ID); p.Status != "ready" || p.DecidedBy != "" {
		t.Fatalf("the plan %+v", p)
	}
}

// Production runs api on Kubernetes, and its coverage claims a watch can
// judge it: gate 5 still names Kubernetes and the release asks. Coverage
// lists what production's bindings run, so the fake's row is production's
// target for api. (A manifest names one provider for every environment,
// and a real Kubernetes target needs a cluster.)
func TestReleasePrep_kubernetesAlwaysAsks(t *testing.T) {
	getReport, approve := &lateInput{}, &lateInput{}
	fake := agenttest.Anthropic(t, reviewTurns(getReport, approve)...)
	g := newReleaseRig(t, fake, releaseOptions{})
	g.autonomy(t, g.project)
	g.watches.set(func(w *releaseWatches) {
		w.coverage.Services[0].Provider = "kubernetes"
	})
	plan := g.promotion(t, g.owner)
	approve.set(map[string]any{"plan_id": plan.ID, "revision": plan.Revision})
	r := g.prepare(t, g.owner)
	getReport.set(map[string]any{"report_id": r.ID})
	taskID := g.task(t, r.ID)

	req := g.pendingRequest(t, taskID, agentrequests.KindApprovePlan)
	if !strings.Contains(req.Question, "gate 5 (safety net): api runs on Kubernetes, which has no canary or watch Loomplane can judge, so a person releases it") {
		t.Fatalf("the request %+v", req)
	}
	got := g.waiting(t, r.ID, taskID)
	if failed := failedGates(got.Gates); !slices.Equal(failed, []int{releaseprep.GateSafetyNet}) {
		t.Fatalf("gates %v failed: %+v", failed, got.Gates)
	}
	if p := g.planStatus(t, plan.ID); p.Status != "ready" || p.DecidedBy != "" {
		t.Fatalf("the plan %+v", p)
	}
}

// Production records a new release of web, which the promotion holds,
// while the gate reads its evidence. The gate allows, the agent's approval
// goes through at the revision it read, and the approval workflow settles
// the plan stale: nothing deploys, and the report ends stale with a notice
// to Ada.
func TestReleasePrep_stalePlanBetweenReadAndApprove(t *testing.T) {
	getReport, approve := &lateInput{}, &lateInput{}
	served := make(chan struct{})
	fake := agenttest.Anthropic(t,
		agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "get_release_report", Input: getReport}}},
		agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "approve_plan", Input: approve}}, Wait: served},
		agenttest.Turn{Text: "The plan went stale."},
	)
	g := newReleaseRig(t, fake, releaseOptions{})
	g.autonomy(t, g.project)
	plan := g.promotion(t, g.owner)
	approve.set(map[string]any{"plan_id": plan.ID, "revision": plan.Revision})
	r := g.prepare(t, g.owner)
	getReport.set(map[string]any{"report_id": r.ID})
	taskID := g.task(t, r.ID)

	waitForSecondTurn(t, fake)
	held, release := g.watches.holdNext()
	defer release()
	close(served)
	select {
	case <-held:
	case <-time.After(time.Minute):
		t.Fatal("the gate never read the soak")
	}
	g.recordRelease(t, g.prodEnv, "web", "c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2")
	release()

	if final := g.finish(t, taskID); final.Status != agentruns.StatusCompleted {
		t.Fatalf("the review %+v", final)
	}
	deadline := time.Now().Add(30 * time.Second)
	p := g.planStatus(t, plan.ID)
	for p.Status == "ready" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		p = g.planStatus(t, plan.ID)
	}
	if p.Status != "stale" {
		t.Fatalf("the plan %+v", p)
	}
	if audit := g.gateAudit(t, r.ID); len(audit) == 0 || slices.ContainsFunc(audit, func(a string) bool { return a != "true" }) {
		t.Fatalf("the gate's audit %v", audit)
	}
	if d := g.deployment(t, plan.ID, 3*time.Second); d != nil {
		t.Fatalf("a stale plan deployed: %+v", d)
	}
	got := g.settle(t, r.ID)
	if got.State != releaseprep.StateStale {
		t.Fatalf("the settled report %+v", got)
	}
	if to := g.notified(t, r.ID, releaseprep.StateStale); !slices.Contains(to, g.userID()) {
		t.Fatalf("the stale notice reached %v, not Ada", to)
	}
	if reqs := g.requestsOf(t, taskID); len(reqs) != 0 {
		t.Fatalf("the agent asked a person: %+v", reqs[0])
	}
}

// The model approves with a revision the report never gave: every gate
// passes, the approval is refused as stale, the plan stays ready, nobody
// is asked, and the report says the agent's approval didn't go through.
func TestReleasePrep_oldRevisionFromTheModelIsRefused(t *testing.T) {
	getReport, approve := &lateInput{}, &lateInput{}
	fake := agenttest.Anthropic(t, reviewTurns(getReport, approve)...)
	g := newReleaseRig(t, fake, releaseOptions{})
	g.autonomy(t, g.project)
	plan := g.promotion(t, g.owner)
	approve.set(map[string]any{"plan_id": plan.ID, "revision": "rev_old"})
	r := g.prepare(t, g.owner)
	getReport.set(map[string]any{"report_id": r.ID})
	taskID := g.task(t, r.ID)

	if final := g.finish(t, taskID); final.Status != agentruns.StatusCompleted {
		t.Fatalf("the review %+v", final)
	}
	if msg := lastMessage(t, fake, 2); !strings.Contains(msg, planning.ErrStale.Error()) {
		t.Fatalf("the model read %s", msg)
	}
	if p := g.planStatus(t, plan.ID); p.Status != "ready" || p.DecidedBy != "" {
		t.Fatalf("the plan %+v", p)
	}
	got := g.settle(t, r.ID)
	if got.State != releaseprep.StateStopped || !strings.Contains(got.StateReason, "the agent's approval didn't go through") {
		t.Fatalf("the settled report %+v", got)
	}
	if reqs := g.requestsOf(t, taskID); len(reqs) != 0 {
		t.Fatalf("the agent asked a person: %+v", reqs[0])
	}
}

// With AI off there's nothing to prepare a release with: the route is
// 404, no report is stored, and no settle is scheduled.
func TestReleasePrep_offWithoutAI(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Dashboard.Dir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Shop"}, &proj)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+proj.ID+"/environments", map[string]string{"name": "staging"}, nil)
	c.must(http.StatusNotFound, http.MethodPost, "/api/v1/projects/"+proj.ID+"/release-reports", map[string]any{"scope": []string{"api"}}, nil)

	db, err := pgx.Connect(context.Background(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(context.Background()) }()
	var n int
	if err = db.QueryRow(context.Background(), `SELECT count(*) FROM loomplane_release_reports`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d release reports with AI off, %v", n, err)
	}
	if sc := settleCron(t, dispatchOf(t, r.app.(forge.App))); sc != nil {
		t.Fatalf("AI off, and the settle is scheduled: %+v", sc)
	}
}

// The same release asked for in a chat pauses for a person, even with
// autonomy on and every gate green: the model chose to start it, so it is
// never the person's own request, and the agent approves nothing.
func TestReleasePrep_chatReleaseAsksAPerson(t *testing.T) {
	getReport, approve := &lateInput{}, &lateInput{}
	fake := agenttest.Anthropic(t, append([]agenttest.Turn{
		{ToolUses: []agenttest.ToolUse{{Name: "prepare_release", Input: map[string]any{"project": "shop", "scope": []string{"api"}}}}},
		{Text: "Loomplane is preparing the release."},
	}, reviewTurns(getReport, approve)...)...)
	g := newReleaseRig(t, fake, releaseOptions{})
	g.autonomy(t, g.project)
	plan := g.promotion(t, g.owner)
	approve.set(map[string]any{"plan_id": plan.ID, "revision": plan.Revision})
	// The job waits on the soak until the chat has had its last turn, so
	// the model's turns come in the script's order.
	held, release := g.watches.holdNext()
	defer release()

	ada := g.ownerPrincipal()
	runs, chat := g.chat(t, ada, "Prepare the release of api.")
	if final := waitFor(t, runs, ada, chat.ID, finished); final.Status != agentruns.StatusCompleted {
		t.Fatalf("the chat %+v", final)
	}
	<-held
	var list struct {
		Reports []releaseprep.ReleaseReport `json:"reports"`
	}
	g.owner.must(http.StatusOK, http.MethodGet, g.base+"/release-reports", nil, &list)
	if len(list.Reports) != 1 || list.Reports[0].Via != releaseprep.ViaChat || list.Reports[0].RequestedBy != ada.UserID || list.Reports[0].PlanID != plan.ID {
		t.Fatalf("reports %+v", list.Reports)
	}
	r := list.Reports[0]
	getReport.set(map[string]any{"report_id": r.ID})
	release()
	taskID := g.task(t, r.ID)

	req := g.pendingRequest(t, taskID, agentrequests.KindApprovePlan)
	if !strings.HasPrefix(req.Question, "Needs a person: ") || !strings.Contains(req.Question, "asked for in chat") {
		t.Fatalf("the request %+v", req)
	}
	got := g.waiting(t, r.ID, taskID)
	if got.State != releaseprep.StateWaiting || got.Decision != releaseprep.DecisionAsk {
		t.Fatalf("the report %+v", got)
	}
	if p := g.planStatus(t, plan.ID); p.Status != "ready" || p.DecidedBy != "" {
		t.Fatalf("the plan %+v", p)
	}
}

// A developer, who can't approve production, prepares a release: the
// agent reviews it but can't approve it, the plan waits for an approver,
// and the developer and the project's responsible team hear so. The
// workspace's admins, who aren't on the team, don't.
func TestReleasePrep_developerAsksAPerson(t *testing.T) {
	getReport := &lateInput{}
	fake := agenttest.Anthropic(t,
		agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "get_release_report", Input: getReport}}},
		agenttest.Turn{Text: "Reviewed; it needs someone who can approve production."},
	)
	g := newReleaseRig(t, fake, releaseOptions{})
	g.autonomy(t, g.project)
	dev, devID := g.join(t, "dev@example.com", "developer", "Dev")
	_, graceID := g.join(t, "grace@example.com", "developer", "Grace")
	g.member(t, "ben@example.com", "admin", "Ben")
	var team struct {
		ID string `json:"id"`
	}
	g.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/teams", map[string]string{"name": "Payments"}, &team)
	g.owner.must(http.StatusOK, http.MethodPut, "/api/v1/teams/"+team.ID+"/members/"+graceID, nil, nil)
	g.owner.must(http.StatusOK, http.MethodPut, g.base+"/team", map[string]string{"team_id": team.ID}, nil)

	plan := g.promotion(t, dev)
	r := g.prepare(t, dev)
	getReport.set(map[string]any{"report_id": r.ID})
	if !r.ReusedPlan || r.PlanID != plan.ID || r.RequestedBy != devID {
		t.Fatalf("the report %+v", r)
	}
	taskID := g.task(t, r.ID)
	if final := g.finish(t, taskID); final.Status != agentruns.StatusCompleted {
		t.Fatalf("the review %+v", final)
	}
	if tools := fake.Requests()[0].Tools; slices.Contains(tools, "approve_plan") || !slices.Contains(tools, "get_release_report") {
		t.Fatalf("the review was shown %v", tools)
	}
	got := g.settle(t, r.ID)
	if got.State != releaseprep.StateStopped || got.StateReason != "the agent reviewed it; it waits for someone who can approve production in Needs approval" {
		t.Fatalf("the settled report %+v", got)
	}
	if p := g.planStatus(t, plan.ID); p.Status != "ready" || p.DecidedBy != "" {
		t.Fatalf("the plan %+v", p)
	}
	if reqs := g.requestsOf(t, taskID); len(reqs) != 0 {
		t.Fatalf("the agent asked a person: %+v", reqs[0])
	}
	want := []string{devID, graceID}
	slices.Sort(want)
	if to := g.notified(t, r.ID, releaseprep.StateStopped); !slices.Equal(to, want) {
		t.Fatalf("the notice reached %v, want the developer and the team %v", to, want)
	}
}

// Staging deploys api again between the report and approve_plan: the gate
// reads the pins again, gate 2 fails saying what moved, and the release
// waits for a person.
func TestReleasePrep_stagingMovesBeforeApproveAsks(t *testing.T) {
	const commitC = "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"
	getReport, approve := &lateInput{}, &lateInput{}
	served := make(chan struct{})
	fake := agenttest.Anthropic(t,
		agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "get_release_report", Input: getReport}}},
		agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "approve_plan", Input: approve}}, Wait: served},
		agenttest.Turn{Text: "Staging moved."},
	)
	g := newReleaseRig(t, fake, releaseOptions{})
	g.autonomy(t, g.project)
	plan := g.promotion(t, g.owner)
	approve.set(map[string]any{"plan_id": plan.ID, "revision": plan.Revision})
	r := g.prepare(t, g.owner)
	getReport.set(map[string]any{"report_id": r.ID})
	taskID := g.task(t, r.ID)

	waitForSecondTurn(t, fake)
	if got := g.report(t, r.ID); len(failedGates(got.Gates)) != 0 {
		t.Fatalf("the preview before staging moved %+v", got.Gates)
	}
	g.recordRelease(t, g.stageEnv, "api", commitC)
	close(served)

	req := g.pendingRequest(t, taskID, agentrequests.KindApprovePlan)
	moved := fmt.Sprintf("gate 2 (promotes what staging runs): staging no longer runs what the plan pins: api: staging runs %s, the plan pins %s", commitC, releaseCommitB)
	if !strings.Contains(req.Question, moved) {
		t.Fatalf("the request %+v", req)
	}
	got := g.waiting(t, r.ID, taskID)
	if failed := failedGates(got.Gates); !slices.Equal(failed, []int{releaseprep.GateFromStaging}) || got.Preview {
		t.Fatalf("gates %v failed: %+v", failed, got.Gates)
	}
	if p := g.planStatus(t, plan.ID); p.Status != "ready" || p.DecidedBy != "" {
		t.Fatalf("the plan %+v", p)
	}
}

// The workspace turns the agent off while it reviews: the run is
// cancelled, the report settles stopped with that reason, read with no
// principal of the agent's, and the plan stays ready for a person.
func TestReleasePrep_agentTurnedOffMidReviewStops(t *testing.T) {
	getReport := &lateInput{}
	served := make(chan struct{})
	fake := agenttest.Anthropic(t,
		agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "get_release_report", Input: getReport}}},
		agenttest.Turn{Text: "Still reading.", Wait: served},
	)
	g := newReleaseRig(t, fake, releaseOptions{})
	defer close(served)
	g.autonomy(t, g.project)
	plan := g.promotion(t, g.owner)
	r := g.prepare(t, g.owner)
	getReport.set(map[string]any{"report_id": r.ID})
	taskID := g.task(t, r.ID)

	waitForSecondTurn(t, fake)
	g.owner.must(http.StatusOK, http.MethodPatch, "/api/v1/ai/settings/agent", map[string]any{"enabled": false, "revision": g.agent().Revision}, nil)
	if final := g.finish(t, taskID); final.Status != agentruns.StatusCancelled {
		t.Fatalf("the review %+v", final)
	}
	// The routes are there only while the agent is on, so the report is
	// read as the settle stored it.
	if err := g.svc.Settle(context.Background()); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	got, err := g.svc.Lookup(context.Background(), g.orgID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != releaseprep.StateStopped || !strings.Contains(got.StateReason, agentsettings.ErrDisabled.Error()) ||
		!strings.Contains(got.StateReason, "the plan waits for a person in Needs approval") {
		t.Fatalf("the settled report %+v", got)
	}
	if p := g.planStatus(t, plan.ID); p.Status != "ready" || p.DecidedBy != "" {
		t.Fatalf("the plan %+v", p)
	}
	if to := g.notified(t, r.ID, releaseprep.StateStopped); !slices.Contains(to, g.userID()) {
		t.Fatalf("the stopped notice reached %v, not Ada", to)
	}
}

// Ada has a production plan open: preparing a release is refused with
// ErrOpenPlan, naming her plan and her, and touches nothing. With replace
// she agrees to supersede it: the new release supersedes that plan and no
// other.
func TestReleasePrep_openPlanIsRefusedUnlessReplaced(t *testing.T) {
	fake := agenttest.Anthropic(t, agenttest.Turn{Text: "Reviewed the release."})
	g := newReleaseRig(t, fake, releaseOptions{})
	open := g.plan(t, g.project, "production")
	again := g.plan(t, g.project, "staging")
	prepare := g.base + "/release-reports"

	code, body := send(g.owner, http.MethodPost, prepare, map[string]any{"scope": []string{"api"}})
	if code != http.StatusConflict || !strings.Contains(body, releaseprep.ErrOpenPlan.Error()) || !strings.Contains(body, open.ID) || !strings.Contains(body, g.userID()) {
		t.Fatalf("prepare with Ada's plan open: %d %s", code, body)
	}
	if p := g.planStatus(t, open.ID); p.Status != "ready" {
		t.Fatalf("a refused prepare touched Ada's plan: %+v", p)
	}
	var n int
	if err := g.db.QueryRow(context.Background(), `SELECT count(*) FROM loomplane_release_reports`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d reports after a refused prepare, %v", n, err)
	}

	var r releaseprep.ReleaseReport
	g.owner.must(http.StatusAccepted, http.MethodPost, prepare, map[string]any{"scope": []string{"api"}, "replace": true}, &r)
	taskID := g.task(t, r.ID)
	got := g.report(t, r.ID)
	if got.PlanID == "" || got.PlanID == open.ID || got.ReusedPlan {
		t.Fatalf("the report %+v", got)
	}
	if p := g.planStatus(t, open.ID); p.Status != "superseded" {
		t.Fatalf("Ada's plan %+v", p)
	}
	if p := g.planStatus(t, again.ID); p.Status != "ready" {
		t.Fatalf("staging's plan %+v", p)
	}
	g.finish(t, taskID)
}

// Bob, in another workspace with the agent on, can't read, list, prepare,
// replace or cancel anything of Engines': every call is not found, and
// the report waiting for a person and its plan are as they were.
func TestReleasePrep_anotherWorkspaceCantTouchIt(t *testing.T) {
	getReport, approve := &lateInput{}, &lateInput{}
	fake := agenttest.Anthropic(t, reviewTurns(getReport, approve)...)
	g := newReleaseRig(t, fake, releaseOptions{})
	plan := g.promotion(t, g.owner)
	approve.set(map[string]any{"plan_id": plan.ID, "revision": plan.Revision})
	r := g.prepare(t, g.owner)
	getReport.set(map[string]any{"report_id": r.ID})
	taskID := g.task(t, r.ID)
	g.pendingRequest(t, taskID, agentrequests.KindApprovePlan)
	before := g.waiting(t, r.ID, taskID)

	ledger := &agentOrg{r: g.r, cfg: g.cfg, owner: &client{t: t, base: g.r.srv.URL, key: g.cfg.Identity.PublishableKey}}
	account(t, ledger.owner, "bob@example.com")
	var org struct {
		ID string `json:"id"`
	}
	ledger.owner.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Ledger", "slug": "ledger"}, &org)
	ledger.owner.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	ledger.orgID = org.ID
	ledger.budget("10")
	ledger.enable(nil)
	bob := ledger.owner
	for _, call := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/release-reports/" + r.ID, nil},
		{http.MethodPost, "/api/v1/release-reports/" + r.ID + "/cancel", nil},
		{http.MethodGet, g.base + "/release-reports", nil},
		{http.MethodPost, g.base + "/release-reports", map[string]any{"scope": []string{"api"}}},
		{http.MethodPost, g.base + "/release-reports", map[string]any{"scope": []string{"api"}, "replace": true}},
	} {
		code, body := send(bob, call.method, call.path, call.body)
		if code != http.StatusNotFound || strings.Contains(body, r.ID) || strings.Contains(body, plan.ID) {
			t.Errorf("Bob %s %s: %d %s", call.method, call.path, code, body)
		}
	}
	after := g.report(t, r.ID)
	if after.Status != before.Status || after.State != before.State || after.StateReason != before.StateReason || after.TaskID != taskID {
		t.Fatalf("the report after Bob %+v, before %+v", after, before)
	}
	if p := g.planStatus(t, plan.ID); p.Status != "ready" || p.DecidedBy != "" {
		t.Fatalf("the plan after Bob %+v", p)
	}
	var n int
	if err := g.db.QueryRow(context.Background(), `SELECT count(*) FROM loomplane_release_reports`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d release reports after Bob, %v", n, err)
	}
}
