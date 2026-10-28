package models

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/catalogstate"
	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/recipes"
)

// fakeBudget records the budget notices queued, and fails while err is set.
type fakeBudget struct {
	mu   sync.Mutex
	sent []notifications.ModelBudgetNotice
	err  error
}

func (f *fakeBudget) EnqueueModelBudgetNoticeIn(_ context.Context, _ notifications.Execer, n notifications.ModelBudgetNotice) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, n)
	return nil
}

func (f *fakeBudget) notices() []notifications.ModelBudgetNotice {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

// pricesOf reads the fake GPU targets as the cron does, with no principal.
type pricesOf struct{ targets *fakeTargets }

func (p pricesOf) ConnectedTargets(_ context.Context, orgID string) ([]gpus.GPUTarget, error) {
	p.targets.mu.Lock()
	defer p.targets.mu.Unlock()
	if orgID != "org" {
		return nil, nil
	}
	return slices.Clone(p.targets.targets), p.targets.err
}

type budgetSetup struct {
	*deploySetup
	budget *fakeBudget
}

// newBudget deploys Llama 8B onto gpu-box in staging, on its one $1.57 an
// hour L40S, and says it's running there as kind on held GPUs.
func newBudget(t *testing.T, kind string, held int) *budgetSetup {
	t.Helper()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), deployer, []gpus.GPUTarget{l40sServer()}, boxes()...)
	d.grantLlama()
	res, err := d.svc.Deploy(context.Background(), member, projectID, stagingID, DeployInput{EntryID: llama8BID, Target: onGPU("nvidia-l40s")})
	if err != nil {
		t.Fatal(err)
	}
	r := current(t, d).Resources[res.Deployment.Resource]
	d.facts.running(stagingID, llamaResource, kind, r.Settings, held)
	b := &budgetSetup{deploySetup: d, budget: &fakeBudget{}}
	d.svc.d.Waiting, d.svc.d.Prices, d.svc.d.Budget = d.rows, pricesOf{d.targets}, b.budget
	return b
}

func (b *budgetSetup) row(t *testing.T) *Deployment {
	t.Helper()
	d, err := b.rows.Get(context.Background(), "org", projectID, stagingID, llamaResource)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (b *budgetSetup) setBudget(t *testing.T, usd string) {
	t.Helper()
	d := b.row(t)
	d.MonthlyBudgetUSD = usd
	if err := b.rows.Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
}

// spent says the model cost usd this month as of at.
func (b *budgetSetup) spent(t *testing.T, usd string, at time.Time) {
	t.Helper()
	d := b.row(t)
	if err := b.rows.Accrue(context.Background(), d, monthOf(at), usd, at); err != nil {
		t.Fatal(err)
	}
}

func (b *budgetSetup) accrue(t *testing.T, at time.Time) *Deployment {
	t.Helper()
	if err := b.svc.Accrue(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	return b.row(t)
}

var nov3 = time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

// 1.57 an hour, last accrued 30 minutes ago: 0.785 more. The first run
// only starts the clock.
func TestAccrueAddsElapsedCost(t *testing.T) {
	b := newBudget(t, recipes.ModelVLLMKind, 1)
	if d := b.accrue(t, nov3); d.MonthCostUSD != "0.0000" || !d.AccruedAt.Equal(nov3) || d.Month != "2026-11" {
		t.Fatalf("first sight: %s %s %v", d.Month, d.MonthCostUSD, d.AccruedAt)
	}
	before := b.row(t).Revision
	d := b.accrue(t, nov3.Add(30*time.Minute))
	if d.MonthCostUSD != "0.7850" || !d.AccruedAt.Equal(nov3.Add(30*time.Minute)) {
		t.Fatalf("after 30 minutes: %s %v", d.MonthCostUSD, d.AccruedAt)
	}
	// Accrual never makes an edit stale.
	if d.Revision != before {
		t.Errorf("revision %d, was %d", d.Revision, before)
	}
}

// A new month starts from nothing, counting only its own hours, and a
// row already in a later month is left alone.
func TestAccrueStartsFreshEachMonth(t *testing.T) {
	b := newBudget(t, recipes.ModelVLLMKind, 1)
	b.accrue(t, time.Date(2026, 11, 30, 23, 0, 0, 0, time.UTC))
	d := b.accrue(t, time.Date(2026, 12, 1, 1, 0, 0, 0, time.UTC))
	if d.Month != "2026-12" || d.MonthCostUSD != "1.5700" {
		t.Fatalf("new month: %s %s", d.Month, d.MonthCostUSD)
	}
	// A run whose clock is behind the row's counts nothing.
	if d = b.accrue(t, time.Date(2026, 11, 30, 23, 59, 0, 0, time.UTC)); d.Month != "2026-12" || d.MonthCostUSD != "1.5700" {
		t.Errorf("earlier run: %s %s", d.Month, d.MonthCostUSD)
	}
}

// Budget 500, 300 spent on Nov 21 at 00:00 UTC, at 1.57 an hour for the
// 240 hours left: on track for 676.80, so one notice. The next run sends
// none, and December can send one again.
func TestProjectedNoticeOncePerMonth(t *testing.T) {
	ctx := context.Background()
	b := newBudget(t, recipes.ModelVLLMKind, 1)
	b.setBudget(t, "500.00")
	// 1.57 an hour for the last five minutes is 0.1308.
	b.spent(t, "299.8692", time.Date(2026, 11, 20, 23, 55, 0, 0, time.UTC))
	edit := b.row(t) // a user's read before the notice
	nov21 := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	d := b.accrue(t, nov21)
	if d.MonthCostUSD != "300.0000" {
		t.Fatalf("spent %s", d.MonthCostUSD)
	}
	sent := b.budget.notices()
	want := notifications.ModelBudgetNotice{
		OrgID: "org", ProjectID: projectID, EnvironmentID: stagingID, DeploymentID: d.ID, Resource: llamaResource, Month: "2026-11",
		Kind: notifications.ModelBudgetProjected, ProjectedUSD: "676.80", BudgetUSD: "500.00",
	}
	if len(sent) != 1 || sent[0] != want {
		t.Fatalf("notices %+v", sent)
	}
	if d.AlertedProjected != "2026-11" || d.AlertedCrossed != "" || d.Revision != edit.Revision {
		t.Errorf("row after the notice: projected %q crossed %q revision %d, was %d", d.AlertedProjected, d.AlertedCrossed, d.Revision, edit.Revision)
	}
	// The user's edit, read before the notice, still saves and doesn't
	// undo it.
	edit.Message = "an edit"
	if err := b.rows.Update(ctx, edit); err != nil {
		t.Fatalf("an edit read before the notice: %v", err)
	}
	if d = b.accrue(t, nov21.Add(5*time.Minute)); len(b.budget.notices()) != 1 || d.AlertedProjected != "2026-11" {
		t.Errorf("second run: %+v, alerted %q", b.budget.notices(), d.AlertedProjected)
	}
	// December: 1.57 an hour for 744 hours is on track for more than 500.
	b.accrue(t, time.Date(2026, 12, 1, 0, 5, 0, 0, time.UTC))
	if sent = b.budget.notices(); len(sent) != 2 || sent[1].Month != "2026-12" || sent[1].Kind != notifications.ModelBudgetProjected {
		t.Errorf("December: %+v", sent)
	}
}

// Once what it cost passes the budget, admins hear it passed, once a
// month, and not that it's on track as well.
func TestCrossedNotice(t *testing.T) {
	b := newBudget(t, recipes.ModelVLLMKind, 1)
	b.setBudget(t, "10.00")
	b.spent(t, "9", nov3)
	d := b.accrue(t, nov3.Add(time.Hour))
	sent := b.budget.notices()
	if len(sent) != 1 || sent[0].Kind != notifications.ModelBudgetCrossed || sent[0].BudgetUSD != "10.00" || sent[0].MonthToDateUSD != "10.57" ||
		sent[0].ProjectedUSD != "" {
		t.Fatalf("notices %+v (spent %s)", sent, d.MonthCostUSD)
	}
	if d.AlertedCrossed != "2026-11" {
		t.Errorf("crossed month %q", d.AlertedCrossed)
	}
	b.accrue(t, nov3.Add(2*time.Hour))
	if len(b.budget.notices()) != 1 {
		t.Errorf("second run sent %+v", b.budget.notices())
	}
}

// A notice that couldn't be queued isn't recorded, so the next run sends
// it. One that was is never sent again.
func TestFailedNoticeIsSentNextRun(t *testing.T) {
	b := newBudget(t, recipes.ModelVLLMKind, 1)
	b.setBudget(t, "10.00")
	b.spent(t, "20", nov3)
	b.budget.err = errors.New("the outbox is down")
	if err := b.svc.Accrue(context.Background(), nov3.Add(time.Hour)); err == nil {
		t.Fatal("a failed notice wasn't reported")
	}
	if d := b.row(t); d.AlertedCrossed != "" || d.MonthCostUSD != "21.5700" {
		t.Fatalf("after a failed notice: crossed %q spent %s", d.AlertedCrossed, d.MonthCostUSD)
	}
	b.budget.err = nil
	if d := b.accrue(t, nov3.Add(2*time.Hour)); d.AlertedCrossed != "2026-11" || len(b.budget.notices()) != 1 {
		t.Errorf("retry: crossed %q, notices %+v", d.AlertedCrossed, b.budget.notices())
	}
}

// Ruling M25: a price nobody knows adds nothing and sends nothing, never
// $0, and the clock moves on so a price learned later isn't charged for
// the time before. A GPU model that nothing deployed yet, or that isn't
// running, is the same.
func TestNoAlertWithoutAPrice(t *testing.T) {
	b := newBudget(t, recipes.ModelVLLMKind, 1)
	b.setBudget(t, "1.00")
	b.targets.targets[0].HourlyPriceKnown, b.targets.targets[0].HourlyUSD = false, 0
	b.accrue(t, nov3)
	d := b.accrue(t, nov3.Add(10*time.Hour))
	if d.MonthCostUSD != "0.0000" || !d.AccruedAt.Equal(nov3.Add(10*time.Hour)) || len(b.budget.notices()) != 0 {
		t.Fatalf("unknown price: spent %s at %v, notices %+v", d.MonthCostUSD, d.AccruedAt, b.budget.notices())
	}
	// The price is known from now: only the hour since counts.
	b.targets.targets[0].HourlyPriceKnown, b.targets.targets[0].HourlyUSD = true, 1.57
	if d = b.accrue(t, nov3.Add(11*time.Hour)); d.MonthCostUSD != "1.5700" {
		t.Errorf("known again: %s", d.MonthCostUSD)
	}

	// Deploying, and nothing deployed it yet: its GPUs aren't known.
	u := newBudget(t, recipes.ModelVLLMKind, 1)
	u.setBudget(t, "1.00")
	u.facts.bindings[stagingID] = nil
	u.facts.set(stagingID, llamaResource, catalogstate.State{Value: catalogstate.Deploying})
	u.accrue(t, nov3)
	if d = u.accrue(t, nov3.Add(10*time.Hour)); d.MonthCostUSD != "0.0000" || len(u.budget.notices()) != 0 {
		t.Errorf("unscheduled: spent %s, notices %+v", d.MonthCostUSD, u.budget.notices())
	}
	// Failed, so not running: nothing counts.
	u.facts.set(stagingID, llamaResource, catalogstate.State{Value: catalogstate.Failed})
	if d = u.accrue(t, nov3.Add(20*time.Hour)); d.MonthCostUSD != "0.0000" || len(u.budget.notices()) != 0 {
		t.Errorf("failed: spent %s, notices %+v", d.MonthCostUSD, u.budget.notices())
	}
}

// Ruling M25: a model on CPU adds 0, its cost being its server's, and no
// budget, however small, treats that as free or as spent.
func TestCPUModelAccruesNothingAndSendsNoNotice(t *testing.T) {
	b := newBudget(t, recipes.ModelLlamaCPPKind, 0)
	b.setBudget(t, "0.01")
	b.accrue(t, nov3)
	d := b.accrue(t, nov3.Add(100*time.Hour))
	if d.MonthCostUSD != "0.0000" || len(b.budget.notices()) != 0 {
		t.Errorf("CPU: spent %s, notices %+v", d.MonthCostUSD, b.budget.notices())
	}
}

// Overlapping runs count each interval once: the run that read the row
// before the other accrued it is refused, and skips it.
func TestOverlappingAccrualsCountOnce(t *testing.T) {
	b := newBudget(t, recipes.ModelVLLMKind, 1)
	b.accrue(t, nov3)
	at := nov3.Add(time.Hour)
	hooked := &accrueHook{memDeployments: b.rows}
	b.svc.d.Store = hooked
	hooked.before = func() {
		// Another replica's run, over the same rows, lands first.
		if err := b.svc.Accrue(context.Background(), at); err != nil {
			t.Error(err)
		}
	}
	if err := b.svc.Accrue(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	if d := b.row(t); d.MonthCostUSD != "1.5700" || !d.AccruedAt.Equal(at) {
		t.Errorf("after two runs over one hour: %s %v", d.MonthCostUSD, d.AccruedAt)
	}
}

// accrueHook runs before once, the first time Accrue is called.
type accrueHook struct {
	*memDeployments
	before func()
}

func (h *accrueHook) Accrue(ctx context.Context, d *Deployment, month, addUSD string, at time.Time) error {
	if hook := h.before; hook != nil {
		h.before = nil
		hook()
	}
	return h.memDeployments.Accrue(ctx, d, month, addUSD, at)
}

// The budget is an alert, never a cutoff: a run past it reads no
// manifest, makes no plan and changes nothing that runs, and budget.go
// can't, since it imports none of them.
func TestBudgetNeverStopsAModel(t *testing.T) {
	b := newBudget(t, recipes.ModelVLLMKind, 1)
	b.setBudget(t, "1.00")
	b.spent(t, "50", nov3)
	revisions, plans, row := b.revisions.count(), len(b.plans.asked), b.row(t)
	b.accrue(t, nov3.Add(time.Hour))
	if len(b.budget.notices()) != 1 {
		t.Fatalf("notices %+v", b.budget.notices())
	}
	after := b.row(t)
	if b.revisions.count() != revisions || len(b.plans.asked) != plans || after.Phase != row.Phase || after.Revision != row.Revision || after.PlanID != row.PlanID {
		t.Errorf("the run changed the model: %d revisions, %d plans, row %+v", b.revisions.count(), len(b.plans.asked), after)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "budget.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		for _, banned := range []string{"/internal/manifests", "/internal/planning", "/internal/deployments", "/internal/orchestration", "/internal/runtime", "/pkg/manifest"} {
			if strings.HasSuffix(path, banned) {
				t.Errorf("budget.go imports %s", path)
			}
		}
	}
}
