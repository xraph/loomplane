package releaseprep

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/dispatch/cron"
	dispatchengine "github.com/xraph/dispatch/engine"
	"github.com/xraph/forge"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentrequests"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releaseprep"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
)

const (
	orgA = "org_a"
	ada  = "usr_ada"
)

// adaP is Ada, a person of org_a, as a fresh pointer each time.
func adaP() *security.Principal {
	return &security.Principal{Kind: security.KindUser, OrgID: orgA, UserID: ada}
}

// tenants is the agent's switch, as the binder sees it.
type tenants struct{ on bool }

func (t tenants) NexusTenant(context.Context, string) (string, error) {
	if !t.on {
		return "", agentsettings.ErrDisabled
	}
	return "tenant_1", nil
}

// call is one call the routes made of the service.
type call struct {
	method    string
	principal security.Principal
	projectID string
	id        string
	in        releaseprep.PrepareInput
	via       string
	limit     int
}

// fakeReports answers every call with err, or a report, and records what
// it was given.
type fakeReports struct {
	mu    sync.Mutex
	err   error
	calls []call
}

func (f *fakeReports) record(c call) (*releaseprep.ReleaseReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	if f.err != nil {
		return nil, f.err
	}
	id := c.id
	if id == "" {
		id = "rrep_1"
	}
	return &releaseprep.ReleaseReport{ID: id, OrgID: c.principal.OrgID, ProjectID: c.projectID, Via: c.via, State: releaseprep.StatePlanning}, nil
}

func (f *fakeReports) seen() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeReports) Prepare(_ context.Context, p security.Principal, projectID string, in releaseprep.PrepareInput, via string) (*releaseprep.ReleaseReport, error) {
	return f.record(call{method: "prepare", principal: p, projectID: projectID, in: in, via: via})
}

func (f *fakeReports) List(_ context.Context, p security.Principal, projectID string, limit int) ([]*releaseprep.ReleaseReport, error) {
	r, err := f.record(call{method: "list", principal: p, projectID: projectID, limit: limit})
	if err != nil {
		return nil, err
	}
	return []*releaseprep.ReleaseReport{r}, nil
}

func (f *fakeReports) Get(_ context.Context, p security.Principal, id string) (*releaseprep.ReleaseReport, error) {
	return f.record(call{method: "get", principal: p, id: id})
}

func (f *fakeReports) Cancel(_ context.Context, p security.Principal, id string) (*releaseprep.ReleaseReport, error) {
	return f.record(call{method: "cancel", principal: p, id: id})
}

func newRouter(t *testing.T, on bool, api reportsAPI) forge.Router {
	t.Helper()
	r := forge.NewRouter()
	if err := mountRoutes(r, nil, &agentscope.Binder{Tenants: tenants{on: on}}, api); err != nil {
		t.Fatal(err)
	}
	return r
}

func serve(t *testing.T, r forge.Router, method, path, body string, p *security.Principal, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if p != nil {
		req = req.WithContext(security.WithPrincipal(req.Context(), *p))
	}
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}

// The four routes, and nothing else under release-reports: Lookup, which
// reads with no principal, is never served.
func TestRoutesRegistered(t *testing.T) {
	r := newRouter(t, true, &fakeReports{})
	want := map[string]string{
		"releaseReports.prepare": "POST /api/v1/projects/:id/release-reports",
		"releaseReports.list":    "GET /api/v1/projects/:id/release-reports",
		"releaseReports.get":     "GET /api/v1/release-reports/:id",
		"releaseReports.cancel":  "POST /api/v1/release-reports/:id/cancel",
	}
	got := map[string]string{}
	for _, info := range r.Routes() {
		if !strings.Contains(info.Path, "release-report") {
			continue
		}
		got[info.Name] = info.Method + " " + info.Path
		if !slices.Contains(info.Tags, "Agent") {
			t.Errorf("%s has tags %v", info.Name, info.Tags)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routes %v, want %v", got, want)
	}
	api := reflect.TypeFor[reportsAPI]()
	if _, ok := api.MethodByName("Lookup"); ok || api.NumMethod() != 4 {
		t.Fatalf("the routes reach %d methods of the service, Lookup among them: %v", api.NumMethod(), ok)
	}
}

func TestRoutesMapErrors(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/projects/prj_1/release-reports", `{}`},
		{http.MethodGet, "/api/v1/projects/prj_1/release-reports", ""},
		{http.MethodGet, "/api/v1/release-reports/rrep_1", ""},
		{http.MethodPost, "/api/v1/release-reports/rrep_1/cancel", ""},
	}
	cases := []struct {
		err  error
		want int
		body string // the response must say this
	}{
		{fmt.Errorf("%w: only a person can ask", security.ErrForbidden), http.StatusForbidden, "only a person can ask"},
		{releaseprep.ErrNotYourTask, http.StatusForbidden, ""},
		{security.ErrUnauthenticated, http.StatusUnauthorized, ""},
		{releaseprep.ErrNotFound, http.StatusNotFound, releaseprep.ErrNotFound.Error()},
		{planning.ErrNotFound, http.StatusNotFound, ""},
		{projects.ErrNotFound, http.StatusNotFound, ""},
		{fmt.Errorf("%w: plan pln_9, made by Loomplane agent; decide it, or prepare again with replace to supersede it", releaseprep.ErrOpenPlan), http.StatusConflict, "plan pln_9, made by Loomplane agent"},
		{fmt.Errorf("%w: rrep_7", releaseprep.ErrInProgress), http.StatusConflict, "rrep_7"},
		{fmt.Errorf("%w: it ended promoted", releaseprep.ErrNotCancelable), http.StatusConflict, "it ended promoted"},
		{agentsettings.ErrDisabled, http.StatusConflict, agentsettings.ErrDisabled.Error()},
		{agentsettings.ErrBudgetExhausted, http.StatusConflict, ""},
		// The key's error may wrap what a store said: only its own words go out.
		{fmt.Errorf("%w: provider key %q: dial tcp 10.0.0.1: refused", agentsettings.ErrKeyUnavailable, "anthropic"), http.StatusConflict, agentsettings.ErrKeyUnavailable.Error()},
		{releaseprep.ErrNoStaging, http.StatusUnprocessableEntity, releaseprep.ErrNoStaging.Error()},
		{releaseprep.ErrNoProduction, http.StatusUnprocessableEntity, ""},
		{fmt.Errorf("%w: staging, qa", releaseprep.ErrAmbiguous), http.StatusUnprocessableEntity, "staging, qa"},
		{fmt.Errorf("%w: list 1 to 50 reports", releaseprep.ErrInvalid), http.StatusUnprocessableEntity, "list 1 to 50 reports"},
	}
	for _, rt := range routes {
		for _, tc := range cases {
			r := newRouter(t, true, &fakeReports{err: tc.err})
			out := serve(t, r, rt.method, rt.path, rt.body, adaP(), nil)
			if out.Code != tc.want {
				t.Errorf("%s %s with %v: %d %s, want %d", rt.method, rt.path, tc.err, out.Code, out.Body.String(), tc.want)
				continue
			}
			if tc.body != "" && !strings.Contains(out.Body.String(), tc.body) {
				t.Errorf("%s %s with %v: body %s doesn't say %q", rt.method, rt.path, tc.err, out.Body.String(), tc.body)
			}
			if strings.Contains(out.Body.String(), "10.0.0.1") {
				t.Errorf("%s %s: the body quotes what the key store said: %s", rt.method, rt.path, out.Body.String())
			}
		}
	}

	r := newRouter(t, true, &fakeReports{})
	for _, q := range []string{"?limit=many", "?limit=2.5"} {
		if out := serve(t, r, http.MethodGet, "/api/v1/projects/prj_1/release-reports"+q, "", adaP(), nil); out.Code != http.StatusUnprocessableEntity {
			t.Errorf("list %s: %d %s", q, out.Code, out.Body.String())
		}
	}
	if out := serve(t, r, http.MethodPost, "/api/v1/projects/prj_1/release-reports", `{"scope":`, adaP(), nil); out.Code != http.StatusBadRequest {
		t.Errorf("a broken body: %d %s", out.Code, out.Body.String())
	}
	for _, rt := range routes {
		if out := serve(t, r, rt.method, rt.path, rt.body, nil, nil); out.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no principal: %d", rt.method, rt.path, out.Code)
		}
	}
}

// The routes answer what the service says, as the generated client reads it.
func TestRoutesAnswer(t *testing.T) {
	api := &fakeReports{}
	r := newRouter(t, true, api)
	var got releaseprep.ReleaseReport
	out := serve(t, r, http.MethodPost, "/api/v1/projects/prj_1/release-reports", `{"from":"qa","to":"prod","scope":["api"],"replace":true}`, adaP(), nil)
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || out.Code != http.StatusAccepted || got.ID != "rrep_1" {
		t.Fatalf("prepare: %d %s", out.Code, out.Body.String())
	}
	out = serve(t, r, http.MethodPost, "/api/v1/projects/prj_1/release-reports", "", adaP(), nil)
	if out.Code != http.StatusAccepted {
		t.Fatalf("prepare with no body: %d %s", out.Code, out.Body.String())
	}
	var list ReleaseReportList
	out = serve(t, r, http.MethodGet, "/api/v1/projects/prj_1/release-reports?limit=5", "", adaP(), nil)
	if err := json.Unmarshal(out.Body.Bytes(), &list); err != nil || out.Code != http.StatusOK || len(list.Reports) != 1 {
		t.Fatalf("list: %d %s", out.Code, out.Body.String())
	}
	out = serve(t, r, http.MethodGet, "/api/v1/release-reports/rrep_2", "", adaP(), nil)
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || out.Code != http.StatusOK || got.ID != "rrep_2" {
		t.Fatalf("get: %d %s", out.Code, out.Body.String())
	}
	out = serve(t, r, http.MethodPost, "/api/v1/release-reports/rrep_3/cancel", "", adaP(), nil)
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || out.Code != http.StatusOK || got.ID != "rrep_3" {
		t.Fatalf("cancel: %d %s", out.Code, out.Body.String())
	}
	calls := api.seen()
	want := []call{
		{method: "prepare", principal: *adaP(), projectID: "prj_1", in: releaseprep.PrepareInput{From: "qa", To: "prod", Scope: []string{"api"}, Replace: true}, via: releaseprep.ViaDashboard},
		{method: "prepare", principal: *adaP(), projectID: "prj_1", via: releaseprep.ViaDashboard},
		{method: "list", principal: *adaP(), projectID: "prj_1", limit: 5},
		{method: "get", principal: *adaP(), id: "rrep_2"},
		{method: "cancel", principal: *adaP(), id: "rrep_3"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls\n%+v\nwant\n%+v", calls, want)
	}

	// An empty list is [], never null.
	empty := newRouter(t, true, &emptyReports{})
	out = serve(t, empty, http.MethodGet, "/api/v1/projects/prj_1/release-reports", "", adaP(), nil)
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"reports":[]`) {
		t.Fatalf("an empty list: %d %s", out.Code, out.Body.String())
	}
}

// emptyReports lists nothing.
type emptyReports struct{ fakeReports }

func (*emptyReports) List(context.Context, security.Principal, string, int) ([]*releaseprep.ReleaseReport, error) {
	return nil, nil
}

// How a release was asked for is the route's own constant. Nothing the
// caller sends sets it: not a body field, a query parameter or a header
// named via, so no request can pass as chat, or as anything else.
func TestPrepareRouteSetsViaItself(t *testing.T) {
	api := &fakeReports{}
	r := newRouter(t, true, api)
	for _, body := range []string{`{"via":"chat"}`, `{"via":"loomctl","Via":"chat"}`, `{"from":"staging","via":"bogus"}`} {
		if out := serve(t, r, http.MethodPost, "/api/v1/projects/prj_1/release-reports", body, adaP(), nil); out.Code != http.StatusAccepted {
			t.Fatalf("body %s: %d %s", body, out.Code, out.Body.String())
		}
	}
	header := http.Header{"Via": {"chat"}, "X-Loomplane-Via": {"chat"}}
	if out := serve(t, r, http.MethodPost, "/api/v1/projects/prj_1/release-reports?via=chat", `{}`, adaP(), header); out.Code != http.StatusAccepted {
		t.Fatalf("query and header: %d %s", out.Code, out.Body.String())
	}
	calls := api.seen()
	if len(calls) != 4 {
		t.Fatalf("calls %+v", calls)
	}
	for _, c := range calls {
		if c.via != releaseprep.ViaDashboard {
			t.Errorf("a prepare was asked for via %q", c.via)
		}
	}
	if calls[2].in.From != "staging" {
		t.Errorf("the body's own fields were lost: %+v", calls[2].in)
	}
}

// Only a person acting for themselves prepares, cancels or reads release
// reports over HTTP: the agent, the agent acting for someone, Loomplane's
// automation and a principal naming no person get 403, and the service is
// never asked.
func TestPrepareRouteNeedsAPersonSession(t *testing.T) {
	person := adaP()
	agent := security.AgentPrincipal(orgA, nil)
	forAda := security.AgentPrincipal(orgA, person)
	system := security.SystemPrincipal(orgA)
	systemAsUser := security.Principal{Kind: security.KindUser, OrgID: orgA, UserID: security.SystemSubject(orgA)}
	agentAsUser := security.Principal{Kind: security.KindUser, OrgID: orgA, UserID: security.AgentSubject(orgA)}
	nobody := security.Principal{Kind: security.KindUser, OrgID: orgA}
	routes := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/projects/prj_1/release-reports"},
		{http.MethodGet, "/api/v1/projects/prj_1/release-reports"},
		{http.MethodGet, "/api/v1/release-reports/rrep_1"},
		{http.MethodPost, "/api/v1/release-reports/rrep_1/cancel"},
	}
	for _, p := range []security.Principal{agent, forAda, system, systemAsUser, agentAsUser, nobody} {
		api := &fakeReports{}
		r := newRouter(t, true, api)
		for _, rt := range routes {
			out := serve(t, r, rt.method, rt.path, "", &p, nil)
			if out.Code != http.StatusForbidden && out.Code != http.StatusUnauthorized {
				t.Errorf("%s %s as %+v: %d %s", rt.method, rt.path, p, out.Code, out.Body.String())
			}
		}
		if calls := api.seen(); len(calls) != 0 {
			t.Errorf("as %+v the service was asked %+v", p, calls)
		}
	}
	// An API key is a person's.
	key := adaP()
	key.KeyID = "key_1"
	api := &fakeReports{}
	if out := serve(t, newRouter(t, true, api), http.MethodPost, "/api/v1/projects/prj_1/release-reports", "", key, nil); out.Code != http.StatusAccepted {
		t.Fatalf("an API key: %d %s", out.Code, out.Body.String())
	}
}

// The routes sit behind the binder: with the agent off in the workspace
// they answer 409 and never reach the service.
func TestRoutesNeedTheAgentOn(t *testing.T) {
	api := &fakeReports{}
	r := newRouter(t, false, api)
	for _, rt := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/projects/prj_1/release-reports"},
		{http.MethodGet, "/api/v1/projects/prj_1/release-reports"},
		{http.MethodGet, "/api/v1/release-reports/rrep_1"},
		{http.MethodPost, "/api/v1/release-reports/rrep_1/cancel"},
	} {
		if out := serve(t, r, rt.method, rt.path, "", adaP(), nil); out.Code != http.StatusConflict {
			t.Errorf("%s %s with the agent off: %d %s", rt.method, rt.path, out.Code, out.Body.String())
		}
	}
	if calls := api.seen(); len(calls) != 0 {
		t.Fatalf("the service was asked %+v", calls)
	}
}

// install registers the release_preparation kind, puts the service's gate
// in 3a's authorizer and makes the service reachable from the agent's
// operations. A kind already registered fails it before the gate moves.
func TestExtensionInstallsTheGateAndKind(t *testing.T) {
	t.Cleanup(func() { releaseprep.Install(nil) })
	releaseprep.Install(nil)
	runs := agentruns.NewService(agentruns.Deps{})
	authz := &agentauthz.Authorizer{}
	svc := releaseprep.NewService(releaseprep.Deps{})
	if err := install(svc, runs, authz); err != nil {
		t.Fatal(err)
	}
	gate, ok := authz.Gate().(*releaseprep.Gate)
	if !ok || gate != svc.Gate() {
		t.Fatalf("the authorizer's gate is %T", authz.Gate())
	}
	if err := runs.RegisterKind(releaseprep.ReleaseSpec()); err == nil {
		t.Fatal("the release_preparation kind registered twice")
	}
	if releaseprep.Installed() != svc {
		t.Fatal("the service isn't installed for the agent's operations")
	}

	// Installing again over the same runs fails, and leaves a fresh
	// authorizer asking first.
	other := &agentauthz.Authorizer{}
	if err := install(releaseprep.NewService(releaseprep.Deps{}), runs, other); err == nil {
		t.Fatal("a second install over the same runs succeeded")
	}
	if _, ask := other.Gate().(agentauthz.AskFirst); !ask {
		t.Fatalf("a failed install moved the gate: %T", other.Gate())
	}
	if releaseprep.Installed() != svc {
		t.Fatal("a failed install replaced the installed service")
	}
}

// fullSources is every dependency, as the container gives them.
func fullSources() sources {
	return sources{
		pg:          &pgdriver.PgDB{},
		authz:       &security.Authorizer{},
		settings:    &agentsettings.Service{},
		reports:     &agentreportsStub{},
		runs:        agentruns.NewService(agentruns.Deps{}),
		requests:    &agentrequests.Service{},
		gate:        &agentauthz.Authorizer{},
		binder:      &agentscope.Binder{},
		scanner:     &agentsafety.Scanner{},
		connections: &repositories.Connections{},
		deployments: &deployments.Service{},
		plans:       &planning.Service{},
		planStore:   &planning.PgStore{},
		watches:     &releasewatch.Service{},
		projects:    &projects.Service{},
		teams:       &teams.Service{},
		audit:       nopAudit{},
	}
}

// agentreportsStub stands in for *agentreports.Service.
type agentreportsStub struct{}

func (agentreportsStub) Open(context.Context, string, string, string) ([]*agentreports.Report, error) {
	return nil, nil
}

type nopAudit struct{}

func (nopAudit) Record(context.Context, projects.AuditEvent) {}

// The wired service gets every dependency it needs. Teams, Places,
// PlanStore and Binder are the ones whose absence would only show later,
// as notices to nobody or closed reports left open: each is refused at
// start instead. Notify stays nil, so a notice is queued inside the
// transaction that settles its report.
func TestTheServiceIsWiredWithEverything(t *testing.T) {
	eng := func() *dispatchengine.Engine { return nil }
	d := deps(config.Defaults(), fullSources(), eng, nil)
	if err := complete(d); err != nil {
		t.Fatalf("a full container: %v", err)
	}
	if d.Notify != nil {
		t.Fatal("Notify is set: a notice would leave the settle's transaction")
	}
	if d.Push != nil {
		t.Fatal("Push is set, though no push mapper exists yet")
	}
	diffs, ok := d.Sources.Diffs.(releaseprep.GitHubDiffs)
	if !ok || diffs.GitHub == nil || diffs.Tokens == nil {
		t.Fatalf("diffs %+v", d.Sources.Diffs)
	}
	if _, ok = d.Sources.Reports.(releaseprep.WatcherReports); !ok {
		t.Fatalf("open reports are %T, not the watcher's", d.Sources.Reports)
	}
	if _, ok = d.Runs.(*agentruns.Service); !ok {
		t.Fatalf("runs are %T", d.Runs)
	}
	if _, ok = d.Places.(*projects.PgStore); !ok {
		t.Fatalf("places are %T", d.Places)
	}
	if _, ok = d.PlanStore.(*planning.PgStore); !ok {
		t.Fatalf("the plan store is %T", d.PlanStore)
	}
	if _, ok = d.Teams.(*teams.Service); !ok {
		t.Fatalf("teams are %T", d.Teams)
	}

	// Each one missing is refused, by name.
	for name, drop := range map[string]func(*releaseprep.Deps){
		"store": func(d *releaseprep.Deps) { d.Store = nil }, "runs": func(d *releaseprep.Deps) { d.Runs = nil },
		"requests": func(d *releaseprep.Deps) { d.Requests = nil }, "jobs": func(d *releaseprep.Deps) { d.Jobs = nil },
		"safety": func(d *releaseprep.Deps) { d.Safety = nil }, "binder": func(d *releaseprep.Deps) { d.Binder = nil },
		"teams": func(d *releaseprep.Deps) { d.Teams = nil }, "plan store": func(d *releaseprep.Deps) { d.PlanStore = nil },
		"places": func(d *releaseprep.Deps) { d.Places = nil }, "audit": func(d *releaseprep.Deps) { d.Audit = nil },
		"plans": func(d *releaseprep.Deps) { d.Sources.Plans = nil }, "watches": func(d *releaseprep.Deps) { d.Sources.Watches = nil },
		"canaries": func(d *releaseprep.Deps) { d.Sources.Canaries = nil }, "projects": func(d *releaseprep.Deps) { d.Sources.Projects = nil },
		"budgets": func(d *releaseprep.Deps) { d.Sources.Budgets = nil }, "authorizer": func(d *releaseprep.Deps) { d.Sources.Authz = nil },
		"open reports": func(d *releaseprep.Deps) { d.Sources.Reports = nil }, "diffs": func(d *releaseprep.Deps) { d.Sources.Diffs = nil },
		"notify": func(d *releaseprep.Deps) {
			d.Notify = func(context.Context, notifications.Execer, notifications.Notice) error { return nil }
		},
	} {
		broken := deps(config.Defaults(), fullSources(), eng, nil)
		drop(&broken)
		if err := complete(broken); err == nil {
			t.Errorf("without %s the service was built", name)
		}
	}
	// A typed nil is as missing as a nil.
	src := fullSources()
	src.teams = nil
	if err := complete(deps(config.Defaults(), src, eng, nil)); err == nil || !strings.Contains(err.Error(), "teams") {
		t.Errorf("a nil *teams.Service: %v", err)
	}
}

// The settle cron runs every 30 seconds on the default queue, as the
// agent's sweep does, so one left behind when a server later runs with AI
// off meets the AI-off handler. The jobs run there too.
func TestTheSettleCronRunsOnTheSweepQueue(t *testing.T) {
	c := settleCron()
	want := &cron.Definition[struct{}]{Name: releaseprep.CronSettle, JobName: releaseprep.JobSettle, Schedule: "@every 30s", Queue: agentruns.SweepQueue}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("cron %+v", c)
	}
	svc := releaseprep.NewService(releaseprep.Deps{})
	prepare, settle := releaseprep.PrepareJob(svc), releaseprep.SettleJob(svc)
	if prepare.Opts.Queue != agentruns.SweepQueue || settle.Opts.Queue != agentruns.SweepQueue || settle.Opts.MaxRetries != 0 {
		t.Fatalf("prepare %+v, settle %+v", prepare.Opts, settle.Opts)
	}
}
