//go:build integration

package models

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	_ "github.com/xraph/grove/drivers/pgdriver/pgmigrate" // registers the Postgres migration executor
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/security"
)

// scratchStore is a PgStore over a database of its own, dropped when the
// test ends, with the ai_models migration applied twice (a rerun must be
// harmless). Its clock is fixed and moved by hand.
func scratchStore(t *testing.T) (*PgStore, *time.Time) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the model deployments store test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("models_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatal("LOOMPLANE_TEST_DATABASE_URL must be a PostgreSQL URL")
	}
	u.Path = "/" + name
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	exec, err := migrate.NewExecutorFor(pg)
	if err != nil {
		t.Fatal(err)
	}
	applied := false
	for _, m := range migrations.Group().Migrations() {
		if m.Version != "20261126000200" {
			continue
		}
		if m.Name != "ai_models" {
			t.Fatalf("migration %s is %q, want ai_models", m.Version, m.Name)
		}
		for range 2 {
			if err = m.Up(ctx, exec); err != nil {
				t.Fatal(err)
			}
		}
		applied = true
	}
	if !applied {
		t.Fatal("no ai_models migration")
	}
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	st := NewPgStore(pg)
	st.now = func() time.Time { return now }
	return st, &now
}

// llamaRow is a model waiting for a GPU server in env_1 of proj_1.
func llamaRow(org, env string) *Deployment {
	return &Deployment{
		OrgID: org, ProjectID: "proj_1", EnvironmentID: env, Resource: "llama",
		EntryID: "meta-llama/Llama-3.1-8B-Instruct", Quantization: "bf16", Engine: "vllm",
		Phase: PhaseWaitingForServer, ServerCloudID: "cloud_1", ServerOperationID: "op_1", ServerName: "gpu-box",
		Request:     &DeployInput{EntryID: "meta-llama/Llama-3.1-8B-Instruct", Quantization: "bf16", MaxModelLen: 16384, MonthlyBudgetUSD: "250"},
		RequestedBy: security.Principal{UserID: "usr_1", OrgID: org, KeyID: "key_1"},
	}
}

func TestCreateGetUpdate(t *testing.T) {
	st, now := scratchStore(t)
	ctx := t.Context()
	d := llamaRow("org_a", "env_1")
	d.MonthlyBudgetUSD = "250"
	if err := st.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d.ID, "aimodel_") || d.Revision != 1 || !d.CreatedAt.Equal(*now) || !d.UpdatedAt.Equal(*now) {
		t.Fatalf("created: id %q revision %d created %v updated %v", d.ID, d.Revision, d.CreatedAt, d.UpdatedAt)
	}
	// Amounts come back as Postgres writes them; none is stored as zero.
	if d.MonthlyBudgetUSD != "250.00" || d.HourlyUSDOverride != "" || d.MonthCostUSD != "0.0000" || d.Month != "" || !d.AccruedAt.IsZero() {
		t.Fatalf("amounts: budget %q override %q cost %q month %q accrued %v", d.MonthlyBudgetUSD, d.HourlyUSDOverride, d.MonthCostUSD, d.Month, d.AccruedAt)
	}
	got, err := st.Get(ctx, "org_a", "proj_1", "env_1", "llama")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, d) {
		t.Fatalf("read back:\n got %+v\nwant %+v", got, d)
	}
	if got.Request == nil || got.Request.MaxModelLen != 16384 || got.RequestedBy != (security.Principal{UserID: "usr_1", OrgID: "org_a", KeyID: "key_1"}) {
		t.Fatalf("private fields: request %+v principal %+v", got.Request, got.RequestedBy)
	}

	// The server is up: the request is spent and the model planned.
	*now = now.Add(time.Minute)
	read := got.Revision
	got.Phase, got.PlanID, got.Message = PhasePlanned, "plan_1", ""
	got.Request, got.RequestedBy = nil, security.Principal{}
	got.HourlyUSDOverride, got.MonthlyBudgetUSD = "1.2345", ""
	got.AlertedProjected = "2026-11"
	// What identifies the row never moves, whatever the caller sends.
	got.ProjectID, got.EnvironmentID, got.Resource = "proj_other", "env_other", "other"
	if err = st.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if got.Revision != read+1 || !got.UpdatedAt.Equal(*now) || !got.CreatedAt.Equal(d.CreatedAt) {
		t.Fatalf("updated: revision %d updated %v created %v", got.Revision, got.UpdatedAt, got.CreatedAt)
	}
	if got.ProjectID != "proj_1" || got.EnvironmentID != "env_1" || got.Resource != "llama" {
		t.Fatalf("identity moved: %s/%s/%s", got.ProjectID, got.EnvironmentID, got.Resource)
	}
	if got.Request != nil || got.RequestedBy != (security.Principal{}) || got.HourlyUSDOverride != "1.2345" || got.MonthlyBudgetUSD != "" || got.AlertedProjected != "2026-11" {
		t.Fatalf("updated fields: %+v", got)
	}
	again, err := st.Get(ctx, "org_a", "proj_1", "env_1", "llama")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, got) {
		t.Fatalf("after update:\n got %+v\nwant %+v", again, got)
	}

	// A writer holding the first read is stale.
	d.Message = "late"
	if err = st.Update(ctx, d); !errors.Is(err, ErrStale) {
		t.Fatalf("stale update: err = %v, want ErrStale", err)
	}
	// So is a bad one refused before it reaches the table.
	again.Phase = "running"
	if err = st.Update(ctx, again); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad phase: err = %v, want ErrInvalid", err)
	}

	if err = st.Delete(ctx, "org_a", "proj_1", "env_1", "llama"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Get(ctx, "org_a", "proj_1", "env_1", "llama"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: err = %v, want ErrNotFound", err)
	}
	if err = st.Delete(ctx, "org_a", "proj_1", "env_1", "llama"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: err = %v, want ErrNotFound", err)
	}
	if err = st.Update(ctx, got); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update after delete: err = %v, want ErrNotFound", err)
	}
}

func TestUniquePerEnvironmentAndResource(t *testing.T) {
	st, _ := scratchStore(t)
	ctx := t.Context()
	if err := st.Create(ctx, llamaRow("org_a", "env_1")); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, llamaRow("org_a", "env_1")); !errors.Is(err, ErrConflict) {
		t.Fatalf("same resource in the same environment: err = %v, want ErrConflict", err)
	}
	// A model runs only in its environment, so staging and production each
	// have their own row, and another org's names are its own.
	for _, d := range []*Deployment{llamaRow("org_a", "env_2"), llamaRow("org_b", "env_1")} {
		if err := st.Create(ctx, d); err != nil {
			t.Fatalf("%s/%s: %v", d.OrgID, d.EnvironmentID, err)
		}
	}
	other := llamaRow("org_a", "env_1")
	other.Resource = "bge"
	if err := st.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListProject(ctx, "org_a", "proj_1")
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(list))
	for _, d := range list {
		keys = append(keys, d.EnvironmentID+"/"+d.Resource)
	}
	if got := strings.Join(keys, " "); got != "env_1/bge env_1/llama env_2/llama" {
		t.Fatalf("project list: %s", got)
	}
	if list, err = st.ListProject(ctx, "org_a", "proj_none"); err != nil || len(list) != 0 {
		t.Fatalf("an empty project: %v, %v", list, err)
	}
	// Bad rows are refused with a reason, not a database error.
	bad := llamaRow("org_a", "env_3")
	bad.MonthlyBudgetUSD = "0"
	if err = st.Create(ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a zero budget: err = %v, want ErrInvalid", err)
	}
}

func TestAccrueResetsOnANewMonth(t *testing.T) {
	st, _ := scratchStore(t)
	ctx := t.Context()
	d := llamaRow("org_a", "env_1")
	d.Phase = PhaseActive
	if err := st.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	created := d.Revision
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	if err := st.Accrue(ctx, d, "2026-11", "3.20", at); err != nil {
		t.Fatal(err)
	}
	if err := st.Accrue(ctx, d, "2026-11", "1.57", at.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if d.Month != "2026-11" || d.MonthCostUSD != "4.7700" || !d.AccruedAt.Equal(at.Add(5*time.Minute)) {
		t.Fatalf("after two accruals: %s %s %v", d.Month, d.MonthCostUSD, d.AccruedAt)
	}

	// Accrual never makes an edit stale, and an edit never undoes it.
	edit, err := st.Get(ctx, "org_a", "proj_1", "env_1", "llama")
	if err != nil {
		t.Fatal(err)
	}
	if edit.Revision != created || edit.MonthCostUSD != "4.7700" {
		t.Fatalf("stored after accrual: revision %d cost %s", edit.Revision, edit.MonthCostUSD)
	}
	if err = st.Accrue(ctx, d, "2026-11", "0.0001", at.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	edit.MonthlyBudgetUSD, edit.MonthCostUSD, edit.Month = "100", "0", "1999-01"
	if err = st.Update(ctx, edit); err != nil {
		t.Fatalf("an edit read before the last accrual: %v", err)
	}
	if edit.MonthCostUSD != "4.7701" || edit.Month != "2026-11" {
		t.Fatalf("an edit changed the month's cost: %s %s", edit.Month, edit.MonthCostUSD)
	}

	// A new month starts from nothing.
	if err = st.Accrue(ctx, d, "2026-12", "1.57", at.AddDate(0, 1, 0)); err != nil {
		t.Fatal(err)
	}
	if d.Month != "2026-12" || d.MonthCostUSD != "1.5700" {
		t.Fatalf("a new month: %s %s", d.Month, d.MonthCostUSD)
	}
	// A late accrual for a closed month changes nothing.
	if err = st.Accrue(ctx, d, "2026-11", "9.99", at); !errors.Is(err, ErrStale) {
		t.Fatalf("an earlier month: err = %v, want ErrStale", err)
	}
	for _, bad := range []struct{ month, usd string }{{"2026-13", "1"}, {"Nov 2026", "1"}, {"2026-12", "-1"}, {"2026-12", "1.00001"}} {
		if err = st.Accrue(ctx, d, bad.month, bad.usd, at); !errors.Is(err, ErrInvalid) {
			t.Errorf("Accrue(%q, %q): err = %v, want ErrInvalid", bad.month, bad.usd, err)
		}
	}
	got, err := st.Get(ctx, "org_a", "proj_1", "env_1", "llama")
	if err != nil {
		t.Fatal(err)
	}
	if got.Month != "2026-12" || got.MonthCostUSD != "1.5700" {
		t.Fatalf("stored: %s %s", got.Month, got.MonthCostUSD)
	}
}

// TestAnotherOrgsRowIsInvisible: every read, update, delete and accrual
// names the org, and a row of another org reads as not found.
func TestAnotherOrgsRowIsInvisible(t *testing.T) {
	st, _ := scratchStore(t)
	ctx := t.Context()
	mine := llamaRow("org_a", "env_1")
	if err := st.Create(ctx, mine); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(ctx, "org_b", "proj_1", "env_1", "llama"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get as org_b: err = %v, want ErrNotFound", err)
	}
	if list, err := st.List(ctx, "org_b"); err != nil || len(list) != 0 {
		t.Errorf("List as org_b: %v, %v", list, err)
	}
	if list, err := st.ListProject(ctx, "org_b", "proj_1"); err != nil || len(list) != 0 {
		t.Errorf("ListProject as org_b: %v, %v", list, err)
	}
	theirs := *mine
	theirs.OrgID, theirs.Message = "org_b", "taken over"
	if err := st.Update(ctx, &theirs); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update as org_b: err = %v, want ErrNotFound", err)
	}
	if err := st.Accrue(ctx, &theirs, "2026-11", "5", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("Accrue as org_b: err = %v, want ErrNotFound", err)
	}
	if err := st.Delete(ctx, "org_b", "proj_1", "env_1", "llama"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete as org_b: err = %v, want ErrNotFound", err)
	}
	got, err := st.Get(ctx, "org_a", "proj_1", "env_1", "llama")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, mine) {
		t.Fatalf("org_a's row changed:\n got %+v\nwant %+v", got, mine)
	}
	if list, err := st.List(ctx, "org_a"); err != nil || len(list) != 1 {
		t.Fatalf("List as org_a: %v, %v", list, err)
	}
}

// TestWaitingListsEveryOrg: the job that carries waiting models on reads
// every org's, and only those still waiting.
func TestWaitingListsEveryOrg(t *testing.T) {
	st, now := scratchStore(t)
	ctx := t.Context()
	b := llamaRow("org_b", "env_1")
	if err := st.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	a := llamaRow("org_a", "env_1")
	if err := st.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	planned := llamaRow("org_a", "env_2")
	planned.Phase = PhasePlanned
	if err := st.Create(ctx, planned); err != nil {
		t.Fatal(err)
	}
	var lister WaitingLister = st
	waiting, err := lister.Waiting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(waiting))
	for _, d := range waiting {
		got = append(got, d.OrgID+"/"+d.EnvironmentID)
		if d.Request == nil || d.RequestedBy.UserID != "usr_1" {
			t.Errorf("%s: a waiting row needs its request and requester: %+v %+v", d.OrgID, d.Request, d.RequestedBy)
		}
	}
	if strings.Join(got, " ") != "org_b/env_1 org_a/env_1" {
		t.Fatalf("waiting: %v", got)
	}
}

// Stranded lists, across organizations, the planned rows a deploy stopped
// writing before since: no plan, no message, a requester.
func TestStrandedListsEveryOrg(t *testing.T) {
	st, now := scratchStore(t)
	ctx := t.Context()
	add := func(org, env string, change func(*Deployment)) {
		t.Helper()
		d := llamaRow(org, env)
		d.Phase = PhasePlanned
		change(d)
		if err := st.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	keep := func(*Deployment) {}
	add("org_b", "env_1", keep)
	add("org_a", "env_1", keep)
	add("org_a", "env_2", func(d *Deployment) { d.PlanID = "plan_1" })
	add("org_a", "env_3", func(d *Deployment) { d.Message = "Plan staging from the project to deploy it." })
	add("org_a", "env_4", func(d *Deployment) { d.RequestedBy = security.Principal{} })
	add("org_a", "env_5", func(d *Deployment) { d.Phase = PhaseWaitingForServer })
	cutoff := now.Add(time.Minute)
	*now = now.Add(2 * time.Minute)
	add("org_a", "env_6", keep) // too young

	var lister WaitingLister = st
	rows, err := lister.Stranded(ctx, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(rows))
	for _, d := range rows {
		got = append(got, d.OrgID+"/"+d.EnvironmentID)
	}
	slices.Sort(got)
	if strings.Join(got, " ") != "org_a/env_1 org_b/env_1" {
		t.Errorf("stranded: %v", got)
	}
}

// TestAccrueCountsAnIntervalOnce: two runs that worked out the same interval
// from the same read can't both add it.
func TestAccrueCountsAnIntervalOnce(t *testing.T) {
	st, _ := scratchStore(t)
	ctx := t.Context()
	d := llamaRow("org_a", "env_1")
	d.Phase = PhaseActive
	if err := st.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	first, second := *d, *d // both read the never-accrued row
	if err := st.Accrue(ctx, &first, "2026-11", "3.2000", at); err != nil {
		t.Fatal(err)
	}
	if err := st.Accrue(ctx, &second, "2026-11", "3.2000", at.Add(time.Second)); !errors.Is(err, ErrStale) {
		t.Fatalf("a second accrual from the same read: err = %v, want ErrStale", err)
	}
	if second.MonthCostUSD != "0.0000" || !second.AccruedAt.IsZero() {
		t.Fatalf("a refused accrual changed its row: %s %v", second.MonthCostUSD, second.AccruedAt)
	}
	got, err := st.Get(ctx, "org_a", "proj_1", "env_1", "llama")
	if err != nil {
		t.Fatal(err)
	}
	if got.MonthCostUSD != "3.2000" || !got.AccruedAt.Equal(at) {
		t.Fatalf("stored: %s %v, want 3.2000 at %v", got.MonthCostUSD, got.AccruedAt, at)
	}
	// Reading again lets the next interval in.
	if err = st.Accrue(ctx, got, "2026-11", "1.0000", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got.MonthCostUSD != "4.2000" {
		t.Fatalf("after a fresh read: %s", got.MonthCostUSD)
	}
}

// TestAccrueTakesTheRowLock: accruals that race each add their share.
func TestAccrueTakesTheRowLock(t *testing.T) {
	st, _ := scratchStore(t)
	ctx := t.Context()
	d := llamaRow("org_a", "env_1")
	d.Phase = PhaseActive
	if err := st.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	const writers = 20
	errs := make(chan error, writers)
	for range writers {
		go func() {
			// Each run reads the row again when another got there first.
			for {
				own, err := st.Get(ctx, "org_a", "proj_1", "env_1", "llama")
				if err == nil {
					err = st.Accrue(ctx, own, "2026-11", "0.0125", time.Now())
				}
				if !errors.Is(err, ErrStale) {
					errs <- err
					return
				}
			}
		}()
	}
	for range writers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.Get(ctx, "org_a", "proj_1", "env_1", "llama")
	if err != nil {
		t.Fatal(err)
	}
	if got.MonthCostUSD != "0.2500" {
		t.Fatalf("%d accruals of 0.0125: %s", writers, got.MonthCostUSD)
	}
}

// Ruling M24: the Reaper reads a deleted environment's rows before the
// deletion and deletes them after it, leaving every other environment's,
// and the stored Request carries its target, and a move's starting point,
// through Postgres.
func TestReaperDeletesAnEnvironmentsRows(t *testing.T) {
	st, _ := scratchStore(t)
	ctx := t.Context()
	waiting := llamaRow("org_a", "env_1")
	waiting.Request.Target = TargetChoice{Source: TargetServerSize, ConnectionID: "conn_do", SizeID: "gpu-l40sx1-48gb", Region: "tor1"}
	waiting.Request.Moving = &MovingFrom{Kind: "model-vllm", Phase: PhaseActive, ServerName: "gpu-box", PlanID: "plan_1"}
	kept := llamaRow("org_a", "env_2")
	other := llamaRow("org_b", "env_1")
	for _, d := range []*Deployment{waiting, kept, other} {
		if err := st.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.Get(ctx, "org_a", "proj_1", "env_1", "llama")
	if err != nil || got.Request == nil || got.Request.Target != waiting.Request.Target || got.Request.Moving == nil || *got.Request.Moving != *waiting.Request.Moving {
		t.Fatalf("stored request %+v, %v", got.Request, err)
	}

	var warned []string
	r := &Reaper{Store: st, Warn: func(msg string, err error) { warned = append(warned, msg+": "+err.Error()) }}
	after := r.Reap(ctx, "org_a", "proj_1", "env_1")
	if after == nil || len(warned) != 1 || !strings.Contains(warned[0], "gpu-box") {
		t.Fatalf("warnings %v", warned)
	}
	after(ctx)
	if _, err = st.Get(ctx, "org_a", "proj_1", "env_1", "llama"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the deleted environment's row: %v", err)
	}
	for _, d := range []*Deployment{kept, other} {
		if _, err = st.Get(ctx, d.OrgID, d.ProjectID, d.EnvironmentID, d.Resource); err != nil {
			t.Errorf("%s/%s: %v", d.OrgID, d.EnvironmentID, err)
		}
	}
}
