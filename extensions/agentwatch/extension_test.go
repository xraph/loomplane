package agentwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	watch "github.com/xraph/loomplane/internal/agentwatch"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

// The people the route tests act as: a viewer reads reports, a developer
// also closes them.
const (
	viewer    = "usr_viewer"
	developer = "usr_dev"
)

func as(user string) *security.Principal { return &security.Principal{OrgID: "org_1", UserID: user} }

// authz lets everyone in org_1 use the agent and read proj_1's
// deployments, and only the developer start them.
type authz struct{}

func (authz) Require(_ context.Context, p security.Principal, perm security.Permission, _ string) error {
	switch {
	case p.UserID == "":
		return security.ErrUnauthenticated
	case perm == security.DeploymentCreate && p.UserID != developer:
		return security.ErrForbidden
	}
	return nil
}

func (authz) AllowedOn(_ context.Context, p security.Principal, perm security.Permission, projectID string) (bool, error) {
	return p.UserID != "" && perm == security.DeploymentRead && projectID == "proj_1", nil
}

type projectList struct{}

func (projectList) List(context.Context, security.Principal) ([]*projects.Project, error) {
	return []*projects.Project{{ID: "proj_1"}}, nil
}

// tenants is the agent's switch, as the binder sees it.
type tenants struct{ on bool }

func (t tenants) NexusTenant(context.Context, string) (string, error) {
	if !t.on {
		return "", agentsettings.ErrDisabled
	}
	return "tenant_1", nil
}

// fakeWatcher answers the watcher's status.
type fakeWatcher struct{ err error }

func (f fakeWatcher) Status(context.Context, security.Principal) (*watch.Status, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &watch.Status{Issues: repositories.IssuesStatus{Configured: true, Installations: []repositories.InstallationIssues{}}}, nil
}

type rig struct {
	r       forge.Router
	reports *agentreports.Service
	report  *agentreports.Report
}

// newRig mounts the routes over real reports in memory, with one open
// report on proj_1.
func newRig(t *testing.T, on bool, w watcherAPI) *rig {
	t.Helper()
	reports := agentreports.NewService(agentreports.Deps{Store: agentreports.NewMemStore(), Authz: authz{}, Projects: projectList{}})
	obs, err := reports.Observe(t.Context(), agentreports.Signal{
		OrgID: "org_1", ProjectID: "proj_1", Kind: agentreports.SignalDeploymentFailed, Component: "service:api",
		Severity: agentreports.SeverityHigh, Title: "The api deployment failed", Occurrence: "dep_1#0", At: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	r := forge.NewRouter()
	if err = mountRoutes(r, nil, &agentscope.Binder{Tenants: tenants{on: on}}, reports, w); err != nil {
		t.Fatal(err)
	}
	return &rig{r: r, reports: reports, report: obs.Report}
}

func serve(t *testing.T, r forge.Router, method, path, body string, p *security.Principal) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if p != nil {
		req = req.WithContext(security.WithPrincipal(req.Context(), *p))
	}
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}

func TestReportRoutesRegistered(t *testing.T) {
	rig := newRig(t, true, fakeWatcher{})
	want := map[string]string{
		"agentReports.list":  "GET /api/v1/agent/reports",
		"agentReports.get":   "GET /api/v1/agent/reports/:id",
		"agentReports.close": "POST /api/v1/agent/reports/:id/close",
		"agentWatcher.get":   "GET /api/v1/agent/watcher",
	}
	got := map[string]string{}
	for _, info := range rig.r.Routes() {
		if _, ours := want[info.Name]; !ours {
			continue
		}
		got[info.Name] = info.Method + " " + info.Path
		if !slices.Contains(info.Tags, "Agent") {
			t.Errorf("%s has tags %v", info.Name, info.Tags)
		}
	}
	for name, route := range want {
		if got[name] != route {
			t.Errorf("%s is %q, want %q", name, got[name], route)
		}
	}
}

func TestReportRoutesMapErrors(t *testing.T) {
	rig := newRig(t, true, fakeWatcher{})
	path := "/api/v1/agent/reports/" + rig.report.ID

	if out := serve(t, rig.r, http.MethodGet, "/api/v1/agent/reports/arep_nope", "", as(viewer)); out.Code != http.StatusNotFound {
		t.Errorf("an unknown report: %d %s", out.Code, out.Body.String())
	}
	if out := serve(t, rig.r, http.MethodPost, path+"/close", `{"reason":"not mine"}`, as(viewer)); out.Code != http.StatusForbidden {
		t.Errorf("a viewer closing: %d %s", out.Code, out.Body.String())
	}
	if out := serve(t, rig.r, http.MethodPost, path+"/close", `{"reason":"`+strings.Repeat("x", 501)+`"}`, as(developer)); out.Code != http.StatusUnprocessableEntity {
		t.Errorf("a reason too long: %d %s", out.Code, out.Body.String())
	}
	out := serve(t, rig.r, http.MethodPost, path+"/close", `{"reason":"Fixed by hand"}`, as(developer))
	var closed agentreports.Report
	if err := json.Unmarshal(out.Body.Bytes(), &closed); err != nil || out.Code != http.StatusOK || closed.Status != agentreports.StatusClosed {
		t.Fatalf("a developer closing: %d %s", out.Code, out.Body.String())
	}
	if out = serve(t, rig.r, http.MethodPost, path+"/close", "", as(developer)); out.Code != http.StatusConflict {
		t.Errorf("closing a closed report: %d %s", out.Code, out.Body.String())
	}
	if out = serve(t, rig.r, http.MethodPost, path+"/close", `{"reason":`, as(developer)); out.Code != http.StatusBadRequest {
		t.Errorf("a broken body: %d", out.Code)
	}
	for _, q := range []string{"?status=bogus", "?limit=many", "?limit=500"} {
		if out = serve(t, rig.r, http.MethodGet, "/api/v1/agent/reports"+q, "", as(viewer)); out.Code != http.StatusUnprocessableEntity {
			t.Errorf("list %s: %d %s", q, out.Code, out.Body.String())
		}
	}
	if out = serve(t, rig.r, http.MethodGet, path, "", nil); out.Code != http.StatusUnauthorized {
		t.Errorf("no principal: %d", out.Code)
	}

	bad := newRig(t, true, fakeWatcher{err: fmt.Errorf("%w: not a member", security.ErrForbidden)})
	if out = serve(t, bad.r, http.MethodGet, "/api/v1/agent/watcher", "", as(viewer)); out.Code != http.StatusForbidden {
		t.Errorf("the watcher refused: %d", out.Code)
	}
}

func TestReportRoutesList(t *testing.T) {
	rig := newRig(t, true, fakeWatcher{})
	var list agentreports.ReportList
	out := serve(t, rig.r, http.MethodGet, "/api/v1/agent/reports?status=all&project_id=proj_1&limit=10", "", as(viewer))
	if err := json.Unmarshal(out.Body.Bytes(), &list); err != nil || out.Code != http.StatusOK || len(list.Reports) != 1 || list.Reports[0].ID != rig.report.ID {
		t.Fatalf("list: %d %s", out.Code, out.Body.String())
	}
	out = serve(t, rig.r, http.MethodGet, "/api/v1/agent/reports?project_id=proj_2", "", as(viewer))
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"reports":[]`) {
		t.Fatalf("another project: %d %s", out.Code, out.Body.String())
	}
	var r agentreports.Report
	out = serve(t, rig.r, http.MethodGet, "/api/v1/agent/reports/"+rig.report.ID, "", as(viewer))
	if err := json.Unmarshal(out.Body.Bytes(), &r); err != nil || out.Code != http.StatusOK || r.ID != rig.report.ID {
		t.Fatalf("get: %d %s", out.Code, out.Body.String())
	}
	var st watch.Status
	out = serve(t, rig.r, http.MethodGet, "/api/v1/agent/watcher", "", as(viewer))
	if err := json.Unmarshal(out.Body.Bytes(), &st); err != nil || out.Code != http.StatusOK || st.Paused || !st.Issues.Configured {
		t.Fatalf("watcher: %d %s", out.Code, out.Body.String())
	}
}

// The routes sit behind the binder: with the agent off in the workspace
// they answer 409 and never read a report.
func TestReportRoutesNeedTheAgentOn(t *testing.T) {
	rig := newRig(t, false, fakeWatcher{err: errors.New("the watcher was asked")})
	for _, path := range []string{"/api/v1/agent/reports", "/api/v1/agent/reports/" + rig.report.ID, "/api/v1/agent/watcher"} {
		if out := serve(t, rig.r, http.MethodGet, path, "", as(viewer)); out.Code != http.StatusConflict {
			t.Errorf("%s with the agent off: %d %s", path, out.Code, out.Body.String())
		}
	}
	if out := serve(t, rig.r, http.MethodPost, "/api/v1/agent/reports/"+rig.report.ID+"/close", "", as(developer)); out.Code != http.StatusConflict {
		t.Errorf("close with the agent off: %d", out.Code)
	}
	if r, err := rig.reports.Get(t.Context(), *as(viewer), rig.report.ID); err != nil || r.Status != agentreports.StatusOpen {
		t.Fatalf("the report after: %+v %v", r, err)
	}
}

// The tick is scheduled on the default queue, as the agent's sweep is, so
// a server later run with AI off answers it with the AI-off handler
// instead of piling ticks on the agent's queue, which nothing works then.
func TestTheTickRunsOnTheSweepQueue(t *testing.T) {
	c := tickCron()
	if c.Name != watch.CronTick || c.JobName != watch.JobTick || c.Schedule != "@every 30s" || c.Queue != agentruns.SweepQueue {
		t.Fatalf("cron %+v", c)
	}
}

// Every watcher job runs on the default queue, as the tick does, so one
// left behind when a server later runs with AI off meets the AI-off
// handler. The org pass isn't retried, since the next tick queues
// another; a delivery is retried 8 times with Dispatch's backoff.
func TestTheJobsQueues(t *testing.T) {
	tick, org, deliver := watch.TickJob(nil), watch.OrgJob(nil), watch.DeliverJob(nil, "worker_1")
	if tick.Opts.Queue != agentruns.SweepQueue || tick.Opts.MaxRetries != 0 {
		t.Errorf("tick %+v", tick.Opts)
	}
	if org.Opts.Queue != agentruns.SweepQueue || org.Opts.MaxRetries != 0 {
		t.Errorf("org %+v", org.Opts)
	}
	if deliver.Opts.Queue != agentruns.SweepQueue || deliver.Opts.MaxRetries != 8 {
		t.Errorf("deliver %+v", deliver.Opts)
	}
}
