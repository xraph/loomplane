//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/agentwatch"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/repositories/githubtest"
)

// The watcher's scenarios run the whole app with the agent on: a project
// from acme/api deploying to Cloud Run, githubtest as the GitHub App with
// installation 42 on acme, and a scripted model. The watcher is driven by
// hand, one pass at a time, with the delivery jobs it queues run inline;
// the app's own cron and workers may run some of the same work, which the
// leases make harmless, so every wait is for an outcome.

// installation is the App's installation on acme.
const installation = 42

// Installation permissions: what reading acme/api needs, and that with
// Issues accepted.
var (
	readOnly   = map[string]string{"contents": "read", "metadata": "read"}
	withIssues = map[string]string{"contents": "read", "metadata": "read", "issues": "write"}
)

// watchWorld is Engines, on a server with the agent on: Ada owns it, Ben
// is an admin and Dev a developer. Its project API is on acme/api, with a
// production environment on the fake cloud, whose builds fail until the
// test says otherwise.
type watchWorld struct {
	*agentOrg
	fake    *agenttest.FakeAnthropic
	gh      *githubtest.Server
	cloud   *fakeCloud
	db      *pgx.Conn
	adaID   string
	benID   string
	project string
	// replicas are the apps watching, the first one a.r.
	replicas []*running
}

// startWatch starts the world on fake with the agent's budget agentUSD
// and the installation's permissions perms, and runs a first pass, so
// the watcher reads every event after it.
func startWatch(t *testing.T, fake *agenttest.FakeAnthropic, agentUSD string, perms map[string]string) *watchWorld {
	t.Helper()
	google := newFakeGoogle(t)
	cloud := newFakeCloud(google.srv.URL)
	cloud.failBuilds = true
	google.deploy = cloud
	gh := githubtest.New(t)
	gh.AddInstallation(installation, "acme", perms)
	front := githubFront(t, gh)
	app := gh.AppConfig()

	cfg := config.Defaults()
	cfg.AI = fake.AIConfig()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Dashboard.Dir = t.TempDir()
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.GoogleEndpoint = google.srv.URL
	cfg.GitHub = config.GitHubConfig{
		APIURL: front.URL, WebURL: front.URL, AppID: app.ID, AppSlug: app.Slug, PrivateKey: app.PrivateKey,
		ClientID: app.ClientID, ClientSecret: app.ClientSecret, WebhookSecret: "github-push-integration-secret",
	}
	r := start(t, cfg)
	t.Cleanup(r.stop)
	a := &agentOrg{r: r, cfg: cfg, owner: &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}}
	account(t, a.owner, "ada@example.com")
	var org struct {
		ID string `json:"id"`
	}
	a.owner.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Engines", "slug": "engines"}, &org)
	a.owner.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	a.orgID = org.ID

	db, err := pgx.Connect(context.Background(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(context.Background()) })
	// Engines connected the App's installation on acme.
	if _, err = db.Exec(context.Background(), `INSERT INTO loomplane_github_installations (org_id, installation_id, account, account_type, created_by, created_at)
		VALUES ($1, $2, 'acme', 'Organization', 'test', now())`, a.orgID, installation); err != nil {
		t.Fatal(err)
	}
	w := &watchWorld{agentOrg: a, fake: fake, gh: gh, cloud: cloud, db: db, adaID: a.userID(), replicas: []*running{r}}
	a.budget(agentUSD)
	a.enable(nil)
	w.benID = w.join(t, "ben@example.com", "admin")
	w.join(t, "dev@example.com", "developer")

	var proj struct {
		ID string `json:"id"`
	}
	a.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "API", "repository_url": "https://github.com/acme/api"}, &proj)
	w.project = proj.ID
	analyze(t, cfg, a.owner, proj.ID)
	a.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "gcp", "name": "Prod", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, nil)
	a.owner.must(http.StatusCreated, http.MethodPut, "/api/v1/projects/"+proj.ID+"/manifest", map[string]string{"yaml": apiOnCloudRun}, nil)
	w.pass(t)
	return w
}

// githubFront is GitHub as the app sees it: githubtest for the App, its
// installations and issues, and acme/api's code served to anyone.
func githubFront(t *testing.T, gh *githubtest.Server) *httptest.Server {
	t.Helper()
	tgz := exampleTarball(t)
	target, err := url.Parse(gh.URL())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/api", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"default_branch":"main"}`))
	})
	mux.HandleFunc("GET /repos/acme/api/commits/main", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(exampleSHA))
	})
	mux.HandleFunc("GET /repos/acme/api/tarball/{sha}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("sha") != exampleSHA {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(tgz)
	})
	mux.Handle("/", httputil.NewSingleHostReverseProxy(target))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// join signs email in as role and returns their user ID.
func (w *watchWorld) join(t *testing.T, email, role string) string {
	t.Helper()
	c := joinAs(t, w.agentOrg, email, role)
	var me struct {
		UserID string `json:"user_id"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/session", nil, &me)
	return me.UserID
}

// environment adds a staging-kind environment.
func (w *watchWorld) environment(t *testing.T, name string) {
	t.Helper()
	w.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+w.project+"/environments", map[string]string{"name": name, "kind": "staging"}, nil)
}

// fail plans env, approves the plan and waits for its deployment to
// fail, as the cloud's builds do until the test says otherwise.
func (w *watchWorld) fail(t *testing.T, env string) deploymentBody {
	t.Helper()
	var plan planBody
	w.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+w.project+"/plans", map[string]string{"environment": env}, &plan)
	waitPlanned(t, w.owner, &plan)
	if plan.Status != "ready" {
		t.Fatalf("the %s plan is %s: %s %+v", env, plan.Status, plan.Error, plan.Problems)
	}
	d := approveAndWait(t, w.owner, plan)
	if d.Status != "failed" {
		t.Fatalf("the %s deployment %s, want failed: %+v", env, d.Status, d)
	}
	return d
}

// failBuilds sets whether the cloud's builds fail.
func (w *watchWorld) failBuilds(fail bool) {
	w.cloud.mu.Lock()
	defer w.cloud.mu.Unlock()
	w.cloud.failBuilds = fail
}

// retry retries a deployment and waits for it to end as want.
func (w *watchWorld) retry(t *testing.T, id, want string) {
	t.Helper()
	var d deploymentBody
	w.owner.must(http.StatusOK, http.MethodPost, "/api/v1/deployments/"+id+"/retry", nil, &d)
	deadline := time.Now().Add(readyTimeout)
	for d.Status == "running" {
		if time.Now().After(deadline) {
			t.Fatalf("retry still running: %+v", d)
		}
		time.Sleep(100 * time.Millisecond)
		w.owner.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+id, nil, &d)
	}
	if d.Status != want {
		t.Fatalf("the retried deployment %s, want %s: %+v", d.Status, want, d)
	}
}

// watcherOf is a replica's watcher.
func watcherOf(t *testing.T, r *running) *agentwatch.Watcher {
	t.Helper()
	return inject[*agentwatch.Watcher](t, r.app.(forge.App).Container())
}

// pass runs one watcher pass on every replica at once, then the
// deliveries the passes queued, inline, as the delivery job does.
func (w *watchWorld) pass(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var wg sync.WaitGroup
	errs := make([]error, len(w.replicas))
	for i, r := range w.replicas {
		wt := watcherOf(t, r)
		wg.Go(func() { errs[i] = wt.RunOrg(ctx, w.orgID) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("replica %d's pass: %v", i+1, err)
		}
	}
	rows, err := w.db.Query(ctx, `SELECT id FROM loomplane_agent_reports WHERE org_id = $1 AND delivery_queued_at IS NOT NULL ORDER BY id`, w.orgID)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	// Every replica delivers each one at once, as two workers would.
	for _, id := range queued {
		for i, r := range w.replicas {
			reports := inject[*agentreports.Service](t, r.app.(forge.App).Container())
			owner := "it-replica-" + strconv.Itoa(i+1) + ":" + randomSuffix(t)
			wg.Go(func() { errs[i] = reports.Deliver(ctx, w.orgID, id, owner) })
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("deliver report %s on replica %d: %v", id, i+1, err)
			}
		}
	}
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// until runs passes until ok, for at most a minute.
func (w *watchWorld) until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		w.pass(t)
		if ok() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("still waiting for %s after a minute", what)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// reportView is a report as Ada reads it in the API.
type reportView struct {
	ID          string `json:"id"`
	Signal      string `json:"signal"`
	Status      string `json:"status"`
	Severity    string `json:"severity"`
	Title       string `json:"title"`
	Environment string `json:"environment"`
	Repository  string `json:"repository"`
	Occurrences int    `json:"occurrences"`
	Findings    *struct {
		Summary     string   `json:"summary"`
		LikelyCause string   `json:"likely_cause"`
		Suggestions []string `json:"suggestions"`
		Confidence  string   `json:"confidence"`
	} `json:"findings"`
	Investigation     string `json:"investigation"`
	InvestigationNote string `json:"investigation_note"`
	TaskID            string `json:"task_id"`
	Issue             struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
		State  string `json:"state"`
		Error  string `json:"error"`
	} `json:"issue"`
}

// reports are every report of Engines, newest first.
func (w *watchWorld) reports(t *testing.T) []reportView {
	t.Helper()
	var list struct {
		Reports []reportView `json:"reports"`
	}
	w.owner.must(http.StatusOK, http.MethodGet, "/api/v1/agent/reports?status=all", nil, &list)
	return list.Reports
}

// report is the one report for environment env, or nil.
func (w *watchWorld) report(t *testing.T, env string) *reportView {
	t.Helper()
	var found *reportView
	for _, r := range w.reports(t) {
		if r.Environment != env {
			continue
		}
		if found != nil {
			t.Fatalf("two reports for %s: %s and %s", env, found.ID, r.ID)
		}
		found = &r
	}
	return found
}

// inboxItem is one Herald inbox item.
type inboxItem struct{ User, Title, Body string }

// inbox is every inbox item of the notices of kind whose subject starts
// with subject, oldest first.
func (w *watchWorld) inbox(t *testing.T, kind, subject string) []inboxItem {
	t.Helper()
	rows, err := w.db.Query(context.Background(), `SELECT i.user_id, i.title, i.body FROM herald_inbox i
		JOIN loomplane_notification_outbox o ON o.id = i.metadata->>'outbox_id'
		WHERE o.org_id = $1 AND o.kind = $2 AND o.subject_id LIKE $3 ORDER BY i.created_at, i.user_id`, w.orgID, kind, subject+"%")
	if err != nil {
		t.Fatal(err)
	}
	var out []inboxItem
	for rows.Next() {
		var it inboxItem
		if err := rows.Scan(&it.User, &it.Title, &it.Body); err != nil {
			t.Fatal(err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// waitInbox waits until the inbox has an item of kind and subject for
// each admin, or a while has passed.
func (w *watchWorld) waitInbox(t *testing.T, kind, subject string) []inboxItem {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		items := w.inbox(t, kind, subject)
		if len(items) >= len(w.admins()) || time.Now().After(deadline) {
			return items
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// users are the items' recipients, sorted.
func users(items []inboxItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.User)
	}
	slices.Sort(out)
	return out
}

// admins are Ada and Ben, sorted.
func (w *watchWorld) admins() []string {
	out := []string{w.adaID, w.benID}
	slices.Sort(out)
	return out
}

// investigations are the investigation tasks: requested_by|max_tokens|status.
func (w *watchWorld) investigations(t *testing.T) []string {
	t.Helper()
	rows, err := w.db.Query(context.Background(), `SELECT requested_by || '|' || max_tokens || '|' || status FROM loomplane_agent_tasks
		WHERE org_id = $1 AND kind = 'investigation' ORDER BY created_at`, w.orgID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// waitTask waits for a task to complete, and fails the test when it
// ends otherwise.
func (w *watchWorld) waitTask(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		var status, why string
		if err := w.db.QueryRow(context.Background(), `SELECT status, error FROM loomplane_agent_tasks WHERE org_id = $1 AND id = $2`, w.orgID, id).Scan(&status, &why); err != nil {
			t.Fatal(err)
		}
		switch status {
		case "completed":
			return
		case "failed", "cancelled":
			t.Fatalf("task %s %s: %s", id, status, why)
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s still %s", id, status)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// watcherStatus is GET /agent/watcher as Ada reads it.
type watcherStatus struct {
	Paused       bool   `json:"paused"`
	PausedReason string `json:"paused_reason"`
	Issues       struct {
		Configured    bool `json:"configured"`
		Installations []struct {
			Account  string `json:"account"`
			Accepted bool   `json:"accepted"`
		} `json:"installations"`
	} `json:"issues"`
}

func (w *watchWorld) watcher(t *testing.T) watcherStatus {
	t.Helper()
	var st watcherStatus
	w.owner.must(http.StatusOK, http.MethodGet, "/api/v1/agent/watcher", nil, &st)
	return st
}

// investigation scripts the model's investigation of one report: it reads
// the report, the deployment and its logs, writes its findings, and is
// done. The first turn waits for gate, so the test can fill in the report
// it opened.
type investigation struct {
	gate                    chan struct{}
	report, deployment, out *lateInput
}

func newInvestigation() *investigation {
	return &investigation{gate: make(chan struct{}), report: &lateInput{}, deployment: &lateInput{}, out: &lateInput{}}
}

const (
	findingsSummary = "The build of api fails at its first step."
	findingsCause   = "The Go module needs a version the builder lacks."
)

func (i *investigation) turns() []agenttest.Turn {
	return []agenttest.Turn{
		{ToolUses: []agenttest.ToolUse{{Name: "get_report", Input: i.report}}, Wait: i.gate},
		{ToolUses: []agenttest.ToolUse{{Name: "get_deployment", Input: i.deployment}}},
		{ToolUses: []agenttest.ToolUse{{Name: "deployment_logs", Input: i.deployment}}},
		{ToolUses: []agenttest.ToolUse{{Name: "update_report", Input: i.out}}},
		{Text: "Done."},
	}
}

// open fills in the report and the deployment, and lets the model go.
func (i *investigation) open(reportID, deploymentID string) {
	i.report.set(map[string]any{"report_id": reportID})
	i.deployment.set(map[string]any{"deployment_id": deploymentID})
	i.out.set(map[string]any{
		"report_id": reportID, "summary": findingsSummary, "likely_cause": findingsCause,
		"suggestions": []string{"Pin the Go version in the Dockerfile."}, "confidence": "medium",
	})
	close(i.gate)
}

// A production deployment fails: the watcher opens one report, the agent
// investigates it alone, and the admins and the person who started the
// deployment hear about it once each, in Herald and in one GitHub issue
// on acme/api. It fails again on retry: the same report counts two, the
// issue says so with one comment, and nobody gets a second notice within
// the hour. A later deployment succeeds: the report resolves and the
// issue closes as completed.
func TestScenario_failedDeploymentReportsOnceAndRepeatsUpdate(t *testing.T) {
	inv := newInvestigation()
	fake := agenttest.Anthropic(t, inv.turns()...)
	w := startWatch(t, fake, "10", withIssues)

	d := w.fail(t, "production")
	w.pass(t)
	r := w.report(t, "production")
	if r == nil {
		t.Fatalf("no report for the failed deployment: %+v", w.reports(t))
	}
	inv.open(r.ID, d.ID)
	if r.TaskID == "" {
		t.Fatalf("the report wasn't investigated: %+v", r)
	}
	w.waitTask(t, r.TaskID)
	w.until(t, "the report's issue", func() bool { return w.report(t, "production").Issue.State == agentreports.IssueOpen })

	r = w.report(t, "production")
	if r.Signal != agentreports.SignalDeploymentFailed || r.Status != agentreports.StatusOpen || r.Severity != agentreports.SeverityHigh ||
		r.Investigation != agentreports.InvestigationDone || r.Occurrences != 1 || r.Repository != "acme/api" {
		t.Fatalf("report %+v", r)
	}
	if r.Findings == nil || r.Findings.Summary != findingsSummary || r.Findings.LikelyCause != findingsCause || r.Findings.Confidence != "medium" {
		t.Fatalf("findings %+v", r.Findings)
	}
	if got := len(w.reports(t)); got != 1 {
		t.Fatalf("%d reports", got)
	}
	if got := w.investigations(t); !slices.Equal(got, []string{"|150000|completed"}) {
		t.Fatalf("investigations %v", got)
	}

	items := w.waitInbox(t, "agent.report", r.ID+":")
	if !slices.Equal(users(items), w.admins()) {
		t.Fatalf("the report reached %v, want %v", users(items), w.admins())
	}
	for _, it := range items {
		if !strings.HasPrefix(it.Title, "API · production: Deployment to production failed at ") {
			t.Errorf("inbox title %q", it.Title)
		}
		if !strings.Contains(it.Body, findingsSummary) {
			t.Errorf("inbox body %q", it.Body)
		}
	}

	issues := w.gh.Issues("acme/api")
	if len(issues) != 1 {
		t.Fatalf("%d issues on acme/api", len(issues))
	}
	is := issues[0]
	if is.Number != r.Issue.Number || is.State != "open" || !slices.Equal(is.Labels, []string{agentreports.Label}) ||
		!strings.HasPrefix(is.Body, agentreports.Marker(r.ID)+"\n") || !strings.Contains(is.Body, findingsSummary) ||
		!strings.Contains(is.Body, findingsCause) || !strings.Contains(is.Body, "/agent/reports/"+r.ID) ||
		!strings.Contains(is.Body, "seen once") || len(is.Comments) != 0 {
		t.Fatalf("issue %+v", is)
	}

	// It fails again.
	w.retry(t, d.ID, "failed")
	w.until(t, "the second occurrence on the issue", func() bool {
		got := w.gh.Issues("acme/api")[0]
		return strings.Contains(got.Body, "seen 2 times") && len(got.Comments) == 1
	})
	r = w.report(t, "production")
	if r.Occurrences != 2 || r.Status != agentreports.StatusOpen || len(w.reports(t)) != 1 {
		t.Fatalf("after the retry: %+v", w.reports(t))
	}
	issues = w.gh.Issues("acme/api")
	if len(issues) != 1 || !strings.HasPrefix(issues[0].Comments[0], "This happened again (2 times now") {
		t.Fatalf("issues %+v", issues)
	}
	// The notice for it was delivered, and said nothing new within the
	// hour.
	w.until(t, "the repeat delivered", func() bool {
		var behind bool
		if err := w.db.QueryRow(context.Background(), `SELECT revision > notified_revision OR revision > issue_revision FROM loomplane_agent_reports WHERE id = $1`, r.ID).Scan(&behind); err != nil {
			t.Fatal(err)
		}
		return !behind
	})
	time.Sleep(2 * time.Second) // what the outbox would still drain
	if items := w.inbox(t, "agent.report", r.ID+":"); len(items) != 2 {
		t.Fatalf("%d inbox items after the repeat", len(items))
	}
	if got := len(w.investigations(t)); got != 1 {
		t.Fatalf("%d investigations after the repeat", got)
	}

	// A later deployment succeeds.
	w.failBuilds(false)
	w.retry(t, d.ID, "succeeded")
	w.until(t, "the issue closed", func() bool { return w.gh.Issues("acme/api")[0].State == "closed" })
	r = w.report(t, "production")
	if r.Status != agentreports.StatusResolved {
		t.Fatalf("report %+v", r)
	}
	is = w.gh.Issues("acme/api")[0]
	if is.StateReason != "completed" || len(is.Comments) != 2 || !strings.HasPrefix(is.Comments[1], "Resolved") {
		t.Fatalf("issue %+v", is)
	}
	if n := len(w.gh.Issues("acme/api")); n != 1 {
		t.Fatalf("%d issues", n)
	}
}

// doneTurn ends an investigation at once, reporting in input tokens.
func doneTurn(in int) agenttest.Turn {
	return agenttest.Turn{Text: "Done.", InputTokens: in, OutputTokens: 10}
}

// The workspace spends its agent budget on one investigation. The next
// failure pauses the watcher: the admins hear so once, and that report is
// delivered without an investigation, saying why. Raising the budget lets
// the next pass go on, with one notice saying so, and the next report is
// investigated.
func TestScenario_budgetExhaustedPausesTheWatcherAndNotifiesAdmins(t *testing.T) {
	fake := agenttest.Anthropic(t,
		// Well over 0.01 USD at the default model's catalog price, and
		// under the investigation's 150000 tokens.
		agenttest.Turn{Text: "That was expensive.", InputTokens: 20_000, OutputTokens: 1_000},
		doneTurn(100),
	)
	w := startWatch(t, fake, "0.01", withIssues)
	w.environment(t, "staging")
	w.environment(t, "qa")

	w.fail(t, "production")
	w.pass(t)
	first := w.report(t, "production")
	if first == nil || first.TaskID == "" {
		t.Fatalf("the first report %+v", first)
	}
	w.waitTask(t, first.TaskID)
	w.until(t, "the first report delivered", func() bool { return w.report(t, "production").Investigation == agentreports.InvestigationDone })
	if st := w.watcher(t); st.Paused {
		t.Fatalf("paused before anything was refused: %+v", st)
	}

	w.fail(t, "staging")
	w.until(t, "the second report delivered", func() bool {
		r := w.report(t, "staging")
		return r != nil && r.Issue.State == agentreports.IssueOpen
	})
	second := w.report(t, "staging")
	if second.Investigation != agentreports.InvestigationSkipped || second.InvestigationNote != agentreports.NoteBudget || second.TaskID != "" {
		t.Fatalf("the second report %+v", second)
	}
	if st := w.watcher(t); !st.Paused || st.PausedReason != agentwatch.PauseBudget {
		t.Fatalf("watcher %+v", st)
	}
	paused := w.waitInbox(t, "agent.budget", "watcher-paused:")
	if !slices.Equal(users(paused), w.admins()) {
		t.Fatalf("the pause reached %v, want %v", users(paused), w.admins())
	}
	items := w.waitInbox(t, "agent.report", second.ID+":")
	if !slices.Equal(users(items), w.admins()) {
		t.Fatalf("the second report reached %v", users(items))
	}
	for _, it := range items {
		if !strings.Contains(it.Body, "The agent didn't investigate this") {
			t.Errorf("inbox body %q", it.Body)
		}
	}
	if got := len(w.investigations(t)); got != 1 {
		t.Fatalf("%d investigations while paused", got)
	}

	// An admin raises the budget.
	w.budget("10")
	w.until(t, "the watcher going on", func() bool { return !w.watcher(t).Paused })
	resumed := w.waitInbox(t, "agent.budget", "watcher-resumed:")
	if !slices.Equal(users(resumed), w.admins()) {
		t.Fatalf("the resume reached %v, want %v", users(resumed), w.admins())
	}
	if n := len(w.inbox(t, "agent.budget", "watcher-paused:")); n != 2 {
		t.Fatalf("%d pause items", n)
	}

	w.fail(t, "qa")
	w.until(t, "the third report investigated", func() bool {
		r := w.report(t, "qa")
		return r != nil && r.TaskID != ""
	})
	third := w.report(t, "qa")
	w.waitTask(t, third.TaskID)
	if got := len(w.investigations(t)); got != 2 {
		t.Fatalf("%d investigations", got)
	}
}

// The installation hasn't accepted Issues: a failure reaches Herald, opens
// no issue, and the watcher and the report say why. Once acme accepts the
// permission and Loomplane reads it again, the open report gets its issue.
func TestScenario_issuesWaitForThePermission(t *testing.T) {
	fake := agenttest.Anthropic(t, doneTurn(100))
	w := startWatch(t, fake, "10", readOnly)

	w.fail(t, "production")
	w.pass(t)
	r := w.report(t, "production")
	if r == nil || r.TaskID == "" {
		t.Fatalf("report %+v", r)
	}
	w.waitTask(t, r.TaskID)
	w.until(t, "the report delivered", func() bool {
		return w.report(t, "production").Issue.State == agentreports.IssueWaiting
	})
	if items := w.waitInbox(t, "agent.report", r.ID+":"); !slices.Equal(users(items), w.admins()) {
		t.Fatalf("the report reached %v", users(items))
	}
	if n := len(w.gh.Issues("acme/api")); n != 0 {
		t.Fatalf("%d issues before Issues was accepted", n)
	}
	r = w.report(t, "production")
	if r.Issue.Number != 0 || !strings.Contains(r.Issue.Error, "acme hasn't accepted the Issues permission") {
		t.Fatalf("issue %+v", r.Issue)
	}
	st := w.watcher(t)
	if !st.Issues.Configured || len(st.Issues.Installations) != 1 || st.Issues.Installations[0].Account != "acme" || st.Issues.Installations[0].Accepted {
		t.Fatalf("watcher %+v", st)
	}

	// An owner of acme accepts it on GitHub; Loomplane reads it again once
	// what it read last is older than PermissionsTTL.
	w.gh.SetPermissions(installation, withIssues)
	w.pass(t)
	if n := len(w.gh.Issues("acme/api")); n != 0 {
		t.Fatalf("%d issues within PermissionsTTL", n)
	}
	if _, err := w.db.Exec(context.Background(), `UPDATE loomplane_github_installations SET permissions_checked_at = permissions_checked_at - make_interval(secs => $2)
		WHERE org_id = $1`, w.orgID, (repositories.PermissionsTTL + time.Minute).Seconds()); err != nil {
		t.Fatal(err)
	}
	w.until(t, "the issue", func() bool { return len(w.gh.Issues("acme/api")) == 1 })
	r = w.report(t, "production")
	is := w.gh.Issues("acme/api")[0]
	if r.Issue.State != agentreports.IssueOpen || r.Issue.Number != is.Number || !strings.HasPrefix(is.Body, agentreports.Marker(r.ID)) {
		t.Fatalf("report %+v, issue %+v", r.Issue, is)
	}
	if st := w.watcher(t); !st.Issues.Installations[0].Accepted {
		t.Fatalf("watcher %+v", st)
	}
	// The notice went once.
	if n := len(w.inbox(t, "agent.report", r.ID+":")); n != 2 {
		t.Fatalf("%d inbox items", n)
	}
}

// The secrets of the failure: a project secret, an AWS key and a database
// URL's password, all in the error the cloud gives.
const (
	leakedDBPassword  = "hunter2-Zq9"
	leakedAWSKey      = "AKIAIOSFODNN7EXAMPLE"
	leakedURLPassword = "s3cretPass77"
	errorMarker       = "migrate: connect failed"
)

var failDetail = errorMarker + " with DB_PASSWORD=" + leakedDBPassword + " AWS_ACCESS_KEY_ID=" + leakedAWSKey +
	" url=postgres://app:" + leakedURLPassword + "@db.internal:5432/app"

// A deployment fails with an error that holds a project secret, an AWS key
// and a database password: none of them reaches the issue, a comment,
// the inbox or the webhook payload, and the model reads the error only as
// untrusted content.
func TestScenario_secretsStayOutOfIssues(t *testing.T) {
	inv := newInvestigation()
	fake := agenttest.Anthropic(t, inv.turns()...)
	w := startWatch(t, fake, "10", withIssues)
	w.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+w.project+"/variables",
		map[string]any{"key": "DB_PASSWORD", "value": leakedDBPassword, "secret": true, "environment": "production"}, nil)
	w.cloud.mu.Lock()
	w.cloud.failDetail = failDetail
	w.cloud.mu.Unlock()

	d := w.fail(t, "production")
	w.pass(t)
	r := w.report(t, "production")
	if r == nil || r.TaskID == "" {
		t.Fatalf("report %+v", r)
	}
	inv.open(r.ID, d.ID)
	w.waitTask(t, r.TaskID)
	w.until(t, "the issue", func() bool { return w.report(t, "production").Issue.State == agentreports.IssueOpen })
	// A second failure adds a comment.
	w.retry(t, d.ID, "failed")
	w.until(t, "the comment", func() bool {
		is := w.gh.Issues("acme/api")
		return len(is) == 1 && len(is[0].Comments) == 1
	})
	items := w.waitInbox(t, "agent.report", r.ID+":")
	if len(items) != 2 {
		t.Fatalf("%d inbox items", len(items))
	}

	var sent []string
	for _, is := range w.gh.Issues("acme/api") {
		sent = append(sent, is.Title, is.Body)
		sent = append(sent, is.Comments...)
	}
	for _, it := range items {
		sent = append(sent, it.Title, it.Body)
	}
	rows, err := w.db.Query(context.Background(), `SELECT coalesce(wire_payload::text, '') || ' ' || payload::text FROM loomplane_notification_outbox
		WHERE org_id = $1 AND kind = 'agent.report'`, w.orgID)
	if err != nil {
		t.Fatal(err)
	}
	payloads, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) == 0 {
		t.Fatal("no webhook payload")
	}
	sent = append(sent, payloads...)
	for _, text := range sent {
		for _, secret := range []string{leakedDBPassword, leakedAWSKey, leakedURLPassword} {
			if strings.Contains(text, secret) {
				t.Errorf("%q went out in %q", secret, text)
			}
		}
	}

	// The model read the error, and only inside untrusted content.
	reqs := fake.Requests()
	if len(reqs) == 0 {
		t.Fatal("the model got no request")
	}
	seen := false
	for i, req := range reqs {
		for _, msg := range req.Messages {
			for _, s := range jsonStrings(t, msg) {
				if strings.Contains(s, errorMarker) {
					seen = true
				}
				if outside := untrusted.ReplaceAllString(s, ""); strings.Contains(outside, errorMarker) {
					t.Errorf("request %d carries the error outside untrusted content: %s", i+1, s)
				}
			}
		}
	}
	if !seen {
		t.Error("the model never read the error")
	}
}

// untrusted is a block of untrusted content.
var untrusted = regexp.MustCompile(`(?s)<untrusted_content[^>]*>.*?</untrusted_content>`)

// jsonStrings are every string in a JSON message, however deep.
func jsonStrings(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			out = append(out, x)
			// A tool result is often JSON in a string.
			var inner any
			if json.Unmarshal([]byte(x), &inner) == nil {
				if _, isString := inner.(string); !isString {
					walk(inner)
				}
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

// Two replicas watch one database: both tick and both run the workspace's
// pass and its deliveries, and still one report, one investigation, one
// notice per admin and one issue come of one failure.
func TestScenario_twoReplicasOneReport(t *testing.T) {
	fake := agenttest.Anthropic(t, doneTurn(100))
	w := startWatch(t, fake, "10", withIssues)
	second := start(t, w.cfg)
	t.Cleanup(second.stop)
	w.replicas = append(w.replicas, second)

	w.fail(t, "production")
	ctx := context.Background()
	for i, r := range w.replicas {
		if err := watcherOf(t, r).Tick(ctx); err != nil {
			t.Fatalf("replica %d's tick: %v", i+1, err)
		}
	}
	w.until(t, "the report investigated", func() bool {
		r := w.report(t, "production")
		return r != nil && r.TaskID != ""
	})
	r := w.report(t, "production")
	w.waitTask(t, r.TaskID)
	w.until(t, "the issue", func() bool { return w.report(t, "production").Issue.State == agentreports.IssueOpen })
	// What the replicas' own workers still run comes to nothing more.
	for range 3 {
		w.pass(t)
	}
	if n := len(w.reports(t)); n != 1 {
		t.Fatalf("%d reports", n)
	}
	if got := w.investigations(t); len(got) != 1 {
		t.Fatalf("investigations %v", got)
	}
	w.waitInbox(t, "agent.report", "")
	time.Sleep(2 * time.Second) // what the outbox would still drain
	if items := w.inbox(t, "agent.report", ""); !slices.Equal(users(items), w.admins()) {
		t.Fatalf("the report reached %v, want %v", users(items), w.admins())
	}
	if n := len(w.gh.Issues("acme/api")); n != 1 {
		t.Fatalf("%d issues", n)
	}
	if n := len(fake.Requests()); n != 1 {
		t.Fatalf("the model got %d requests", n)
	}
}
