//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/dispatch/cron"
	dispatchengine "github.com/xraph/dispatch/engine"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/releaseprep"
)

// releaseReportPaths are the routes release preparation serves, as the
// published spec names them.
var releaseReportPaths = []string{
	"/api/v1/projects/{id}/release-reports",
	"/api/v1/release-reports/{id}",
	"/api/v1/release-reports/{id}/cancel",
}

// dispatchOf is the app's Dispatch engine.
func dispatchOf(t *testing.T, app forge.App) *dispatchengine.Engine {
	t.Helper()
	ext, err := app.GetExtension("dispatch")
	if err != nil {
		t.Fatal(err)
	}
	owner, ok := ext.(interface{ Engine() *dispatchengine.Engine })
	if !ok || owner.Engine() == nil {
		t.Fatal("the dispatch extension has no engine")
	}
	return owner.Engine()
}

// settleCron is release preparation's settle entry in Dispatch, or nil.
func settleCron(t *testing.T, eng *dispatchengine.Engine) *cron.Entry {
	t.Helper()
	crons, err := eng.CronStore().ListCrons(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range crons {
		if c.Name == releaseprep.CronSettle {
			return c
		}
	}
	return nil
}

// With AI off there's no release preparation: no route, nothing in the
// spec, no service in the container and no settle schedule. Its jobs have
// the AI-off handlers, which do nothing, and its table still migrates.
func TestReleasePrep_offHasNothing(t *testing.T) {
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
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono"}, &proj)
	c.must(http.StatusNotFound, http.MethodPost, "/api/v1/projects/"+proj.ID+"/release-reports", map[string]any{}, nil)
	c.must(http.StatusNotFound, http.MethodGet, "/api/v1/projects/"+proj.ID+"/release-reports", nil, nil)
	c.must(http.StatusNotFound, http.MethodGet, "/api/v1/release-reports/rrep_x", nil, nil)
	c.must(http.StatusNotFound, http.MethodPost, "/api/v1/release-reports/rrep_x/cancel", nil, nil)

	var spec struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	c.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &spec)
	for path := range spec.Paths {
		if strings.Contains(path, "release-report") {
			t.Errorf("AI off, and the spec has %s", path)
		}
	}
	app := r.app.(forge.App)
	if forge.HasType[*releaseprep.Service](app.Container()) {
		t.Error("AI off, and the container has release preparation")
	}
	eng := dispatchOf(t, app)
	if c := settleCron(t, eng); c != nil {
		t.Errorf("AI off, and the settle is scheduled: %+v", c)
	}
	for _, name := range []string{releaseprep.JobPrepare, releaseprep.JobSettle} {
		run, ok := eng.Registry().Get(name)
		if !ok {
			t.Fatalf("AI off, and nothing answers %s", name)
		}
		if err := run(context.Background(), []byte(`{"org_id":"org_x","report_id":"rrep_x"}`)); err != nil {
			t.Errorf("%s with AI off: %v", name, err)
		}
	}
	db, err := pgx.Connect(context.Background(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(context.Background()) }()
	var table *string
	if err = db.QueryRow(context.Background(), `SELECT to_regclass('loomplane_release_reports')::text`).Scan(&table); err != nil || table == nil {
		t.Fatalf("AI off, and the release reports table didn't migrate: %v", err)
	}
}

// With AI on, the extension is wired whole: the gate is in the agent's
// authorizer, the kind is registered, the operations reach the service,
// the jobs have their handlers, the settle runs every 30 seconds on the
// default queue, and the committed spec carries the routes as served.
//
// A developer, who can't approve production, prepares a release; how it
// was asked for is the route's, whatever the body says. A viewer can't
// prepare. A person of another workspace can't read, prepare, replace or
// cancel anything of this one.
func TestReleasePrep_onIsWiredAndKeepsToItsWorkspace(t *testing.T) {
	fake := agenttest.Anthropic(t)
	s := startScenario(t, fake, "10", nil)
	defer s.r.stop()
	app := s.r.app.(forge.App)
	c := app.Container()

	svc := inject[*releaseprep.Service](t, c)
	authz := inject[*agentauthz.Authorizer](t, c)
	if gate, ok := authz.Gate().(*releaseprep.Gate); !ok || gate != svc.Gate() {
		t.Fatalf("the authorizer's gate is %T", authz.Gate())
	}
	if releaseprep.Installed() != svc {
		t.Fatal("the agent's operations don't reach release preparation")
	}
	if err := inject[*agentruns.Service](t, c).RegisterKind(releaseprep.ReleaseSpec()); err == nil {
		t.Fatal("the release_preparation kind wasn't registered")
	}
	eng := dispatchOf(t, app)
	for _, name := range []string{releaseprep.JobPrepare, releaseprep.JobSettle} {
		if _, ok := eng.Registry().Get(name); !ok {
			t.Errorf("nothing answers %s", name)
		}
	}
	if sc := settleCron(t, eng); sc == nil || sc.JobName != releaseprep.JobSettle || sc.Schedule != "@every 30s" || sc.Queue != agentruns.SweepQueue {
		t.Fatalf("the settle cron is %+v", sc)
	}

	// The committed spec, which the dashboard's client is generated from,
	// carries the routes and every schema release preparation adds as
	// served. Schemas it only reaches, such as planning's Finding, belong
	// to their own features and are only required to be there.
	var raw json.RawMessage
	s.owner.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &raw)
	committedRaw, err := os.ReadFile("../../apps/dashboard/specs/loomplane.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	served, committed := openAPIDoc(t, raw), openAPIDoc(t, committedRaw)
	todo := map[string]bool{}
	for _, path := range releaseReportPaths {
		item, ok := served.Paths[path]
		if !ok {
			t.Errorf("the app doesn't serve %s", path)
			continue
		}
		if !jsonEqual(item, committed.Paths[path]) {
			t.Errorf("the committed spec's %s differs from the served one", path)
		}
		schemaRefs(item, todo)
	}
	owned := map[string]bool{}
	for _, name := range []string{
		"ReleaseReport", "ReleaseReportList", "ReadinessEvidence", "PrepareInputBody", "GateCheck", "Concern",
		"SourceDiff", "DiffCommit", "MigrationFile", "ChangedService", "CanarySummary", "OpenReport",
	} {
		owned[name], todo[name] = true, true
	}
	seen := map[string]bool{}
	for len(todo) > 0 {
		for name := range todo {
			delete(todo, name)
			if seen[name] {
				continue
			}
			seen[name] = true
			if _, there := committed.Components.Schemas[name]; !there {
				t.Errorf("the committed spec has no schema %s", name)
			}
			if owned[name] && !jsonEqual(served.Components.Schemas[name], committed.Components.Schemas[name]) {
				t.Errorf("the committed spec's schema %s differs from the served one", name)
			}
			if owned[name] {
				schemaRefs(served.Components.Schemas[name], todo)
			}
		}
	}

	var proj struct {
		ID string `json:"id"`
	}
	s.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono"}, &proj)
	s.owner.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+proj.ID+"/environments", map[string]string{"name": "staging"}, nil)
	prepare := "/api/v1/projects/" + proj.ID + "/release-reports"

	viewer := joinAs(t, s.agentOrg, "vi@example.com", "viewer")
	viewer.must(http.StatusForbidden, http.MethodPost, prepare, map[string]any{}, nil)

	// A developer may prepare, though not approve: the agent reviews it and
	// it waits for an approver. Chat in the body changes nothing.
	dev := joinAs(t, s.agentOrg, "dev@example.com", "developer")
	var me struct {
		UserID string `json:"user_id"`
	}
	dev.must(http.StatusOK, http.MethodGet, "/api/v1/session", nil, &me)
	var report releaseprep.ReleaseReport
	dev.must(http.StatusAccepted, http.MethodPost, prepare+"?via=chat", map[string]any{"via": "chat"}, &report)
	if report.ID == "" || report.Via != releaseprep.ViaDashboard || report.RequestedBy != me.UserID || report.OrgID != s.orgID {
		t.Fatalf("the developer's report %+v", report)
	}
	var stored string
	if err = s.db.QueryRow(context.Background(), `SELECT via FROM loomplane_release_reports WHERE id = $1`, report.ID).Scan(&stored); err != nil || stored != releaseprep.ViaDashboard {
		t.Fatalf("stored via %q, %v", stored, err)
	}
	var list struct {
		Reports []releaseprep.ReleaseReport `json:"reports"`
	}
	viewer.must(http.StatusOK, http.MethodGet, prepare, nil, &list)
	if len(list.Reports) != 1 || list.Reports[0].ID != report.ID {
		t.Fatalf("the viewer's list %+v", list)
	}
	viewer.must(http.StatusOK, http.MethodGet, "/api/v1/release-reports/"+report.ID, nil, nil)
	// The viewer neither asked for it nor may approve: no cancel.
	viewer.must(http.StatusForbidden, http.MethodPost, "/api/v1/release-reports/"+report.ID+"/cancel", nil, nil)

	// Bob owns Ledger, another workspace with the agent on.
	ledger := &agentOrg{r: s.r, cfg: s.cfg, owner: &client{t: t, base: s.r.srv.URL, key: s.cfg.Identity.PublishableKey}}
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

	before := s.releaseRow(t, report.ID)
	for _, call := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/release-reports/" + report.ID, nil},
		{http.MethodPost, "/api/v1/release-reports/" + report.ID + "/cancel", nil},
		{http.MethodGet, prepare, nil},
		{http.MethodPost, prepare, map[string]any{}},
		{http.MethodPost, prepare, map[string]any{"replace": true}},
	} {
		// Another workspace's report and project are not found: nothing
		// says they exist.
		if code := bob.do(call.method, call.path, call.body, nil); code != http.StatusNotFound {
			t.Errorf("Bob %s %s: %d, want 404", call.method, call.path, code)
		}
		if call.method != http.MethodGet {
			continue
		}
		if body := bob.raw(call.path); strings.Contains(body, report.ID) || strings.Contains(body, me.UserID) {
			t.Errorf("Bob read %s: %s", call.path, body)
		}
	}
	after := s.releaseRow(t, report.ID)
	if after.status == releaseprep.StatusCancelled || strings.Contains(after.reason, "cancelled by") {
		t.Fatalf("Bob cancelled Engines' release: %+v (was %+v)", after, before)
	}
	var n int
	if err = s.db.QueryRow(context.Background(), `SELECT count(*) FROM loomplane_release_reports`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d release reports after Bob's tries, %v", n, err)
	}
	if got := fake.Requests(); len(got) != 0 {
		t.Fatalf("the model was asked %d times", len(got))
	}
}

// releaseRow is a stored report's status and reason.
type releaseRow struct{ status, state, reason string }

func (s *scenario) releaseRow(t *testing.T, id string) releaseRow {
	t.Helper()
	var r releaseRow
	if err := s.db.QueryRow(context.Background(), `SELECT status, state, state_reason FROM loomplane_release_reports WHERE id = $1`, id).Scan(&r.status, &r.state, &r.reason); err != nil {
		t.Fatal(err)
	}
	return r
}
