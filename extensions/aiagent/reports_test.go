package aiagent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/xraph/dispatch"
	dispatchengine "github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/job"
	dispatchmemory "github.com/xraph/dispatch/store/memory"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentwatch"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

func TestRegisterOpsAddsTheReportOperations(t *testing.T) {
	r := agentops.NewRegistry()
	registerOps(r, &agentops.Deps{})
	for name, risk := range map[string]defs.Risk{
		"list_reports": defs.RiskRead, "get_report": defs.RiskRead,
		"open_report": defs.RiskChange, "update_report": defs.RiskChange,
		"prepare_release": defs.RiskChange, "get_release_report": defs.RiskRead, "note_release_concern": defs.RiskChange,
	} {
		op, ok := r.Lookup(name)
		if !ok || op.Def.Risk != risk {
			t.Errorf("%s: registered %v, risk %q", name, ok, op.Def.Risk)
		}
	}
}

// reportsExtension is the extension as Register leaves it before the
// reports: the scanner, binder and audit log made, the database open.
func reportsExtension(t *testing.T) (*Extension, forge.Container) {
	t.Helper()
	db, err := grove.Open(pgdriver.New())
	if err != nil {
		t.Fatal(err)
	}
	e := &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0", "test"),
		cfg:           config.Config{PublicURL: "https://loomplane.example"},
		db:            db, scanner: &agentsafety.Scanner{}, binder: &agentscope.Binder{},
		deps: &agentops.Deps{Audit: noAudit{}},
	}
	c := forge.NewContainer()
	for _, provide := range []func() error{
		func() error { return forge.ProvideValue(c, &security.Authorizer{}) },
		func() error { return forge.ProvideValue(c, &environments.Service{}) },
		func() error { return forge.ProvideValue(c, &deployments.Service{}) },
		func() error { return forge.ProvideValue(c, &repositories.Connections{}) },
		func() error { return forge.ProvideValue(c, &teams.Service{}) },
	} {
		if err := provide(); err != nil {
			t.Fatal(err)
		}
	}
	return e, c
}

// With AI on every port of the reports is filled: a nil Secrets would
// drop the known-secret redaction from every notice and issue, a nil
// Issues every issue, and so on. Only Now may be left to the real clock.
func TestReportsFillEveryPort(t *testing.T) {
	e, c := reportsExtension(t)
	src, err := e.reportSources(c, &projects.Service{})
	if err != nil {
		t.Fatal(err)
	}
	d := reflect.ValueOf(e.reportDeps(src))
	for i := range d.NumField() {
		f, name := d.Field(i), d.Type().Field(i).Name
		if name == "Now" {
			continue
		}
		if !filled(f) {
			t.Errorf("Deps.%s isn't filled", name)
		}
	}
	if err = e.registerReports(c, &projects.Service{}); err != nil {
		t.Fatal(err)
	}
	provided, err := forge.InjectType[*agentreports.Service](c)
	if err != nil || provided == nil || provided != e.deps.Reports || e.deps.InvestigationOf == nil {
		t.Fatalf("provided %p, ops have %p, investigations %v: %v", provided, e.deps.Reports, e.deps.InvestigationOf != nil, err)
	}
}

// filled reports whether v holds something: a non-nil pointer, func or
// interface, a non-empty string, and an adapter whose own pointers are all
// set, so a port wrapping a nil service doesn't count.
func filled(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Interface:
		return !v.IsNil() && filled(v.Elem())
	case reflect.Pointer, reflect.Func, reflect.Map, reflect.Slice:
		return !v.IsNil()
	case reflect.String:
		return v.String() != ""
	case reflect.Struct:
		for i := range v.NumField() {
			if f := v.Field(i); (f.Kind() == reflect.Pointer || f.Kind() == reflect.Interface) && !filled(f) {
				return false
			}
		}
		return true
	}
	return false
}

// open_report's hook queues the watcher's delivery job for the report on
// the sweep queue, and says so when it can't.
func TestDeliverReportQueuesTheDeliveryJob(t *testing.T) {
	store := dispatchmemory.New()
	d, err := dispatch.New(dispatch.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := dispatchengine.Build(d)
	if err != nil {
		t.Fatal(err)
	}
	e := &Extension{BaseExtension: forge.NewBaseExtension(Name, "0", "test"), jobs: func() *dispatchengine.Engine { return eng }}
	deliver := e.deliverReport()
	if deliver == nil {
		t.Fatal("no hook with a job engine")
	}
	if err = deliver(t.Context(), "aorg_a", "arep_1"); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.ListJobsByState(t.Context(), job.StatePending, job.ListOpts{})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("pending jobs %d, %v", len(jobs), err)
	}
	var payload agentwatch.DeliverPayload
	if err = json.Unmarshal(jobs[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if jobs[0].Name != agentwatch.JobDeliver || jobs[0].Queue != agentruns.SweepQueue || payload.OrgID != "aorg_a" || payload.ReportID != "arep_1" {
		t.Fatalf("queued %s on %q with %+v", jobs[0].Name, jobs[0].Queue, payload)
	}

	notReady := &Extension{BaseExtension: forge.NewBaseExtension(Name, "0", "test"), jobs: func() *dispatchengine.Engine { return nil }}
	if err = notReady.deliverReport()(t.Context(), "aorg_a", "arep_1"); err == nil {
		t.Fatal("a delivery with no engine ready passed")
	}
	if (&Extension{}).deliverReport() != nil {
		t.Fatal("a hook without a job engine")
	}
}

func TestReportsNeedEveryService(t *testing.T) {
	e, _ := reportsExtension(t)
	if _, err := e.reportSources(forge.NewContainer(), &projects.Service{}); err == nil {
		t.Fatal("reports were built without their services")
	}
	e, c := reportsExtension(t)
	e.deps.Audit = nil
	if _, err := e.reportSources(c, &projects.Service{}); err == nil {
		t.Fatal("reports were built without the audit log")
	}
}

func TestInvestigationOfReadsTheTaskRecord(t *testing.T) {
	ctx := context.Background()
	runs := fakeRuns{
		"arun_inv":   {ID: "atask_1", OrgID: "aorg_a", RunID: "arun_inv", Kind: agentruns.KindInvestigation, Status: agentruns.StatusRunning, Metadata: map[string]string{"report_id": "arep_1"}},
		"arun_done":  {ID: "atask_2", OrgID: "aorg_a", RunID: "arun_done", Kind: agentruns.KindInvestigation, Status: agentruns.StatusCompleted, Metadata: map[string]string{"report_id": "arep_1"}},
		"arun_chat":  {ID: "atask_3", OrgID: "aorg_a", RunID: "arun_chat", Kind: agentruns.KindChat, Status: agentruns.StatusRunning, Metadata: map[string]string{"report_id": "arep_1"}},
		"arun_queue": {ID: "atask_4", OrgID: "aorg_a", RunID: "arun_queue", Kind: agentruns.KindInvestigation, Status: agentruns.StatusQueued, Metadata: map[string]string{"report_id": "arep_2"}},
	}
	of := investigationOf(func() runsByRun { return runs })
	for run, want := range map[string][2]string{
		"arun_inv": {"atask_1", "arep_1"}, "arun_queue": {"atask_4", "arep_2"},
		"arun_done": {}, "arun_chat": {}, "arun_missing": {},
	} {
		task, report, err := of(ctx, "aorg_a", run)
		if err != nil || task != want[0] || report != want[1] {
			t.Errorf("%s: %q %q %v", run, task, report, err)
		}
	}
	if task, _, err := of(ctx, "aorg_b", "arun_inv"); err != nil || task != "" {
		t.Fatalf("another workspace: %q %v", task, err)
	}
	if task, _, err := investigationOf(func() runsByRun { return nil })(ctx, "aorg_a", "arun_inv"); err != nil || task != "" {
		t.Fatalf("no runs: %q %v", task, err)
	}
	broken := investigationOf(func() runsByRun { return failingRuns{} })
	if _, _, err := broken(ctx, "aorg_a", "arun_inv"); err == nil {
		t.Fatal("a failed read was swallowed")
	}
}

type fakeRuns map[string]*agentruns.Task

func (f fakeRuns) ByRun(_ context.Context, orgID, runID string) (*agentruns.Task, error) {
	if t, ok := f[runID]; ok && t.OrgID == orgID {
		return t, nil
	}
	return nil, agentruns.ErrNotFound
}

type failingRuns struct{}

func (failingRuns) ByRun(context.Context, string, string) (*agentruns.Task, error) {
	return nil, errors.New("the database is down")
}

type noAudit struct{}

func (noAudit) Record(context.Context, projects.AuditEvent) {}
