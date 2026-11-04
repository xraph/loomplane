//go:build integration

package releaseprep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/agenttest"
)

func openPg(t *testing.T) (*PgStore, *pgdriver.PgDB) {
	t.Helper()
	pg := pgdriver.New()
	if err := pg.Open(context.Background(), agenttest.FreshDatabase(t)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return NewPgStore(pg), pg
}

var t0 = time.Date(2026, 11, 4, 9, 0, 0, 123456000, time.UTC)

// newReport is a report that has just been asked for.
func newReport(org, project, to string) *ReleaseReport {
	return &ReleaseReport{
		OrgID:             org,
		ProjectID:         project,
		FromEnvironmentID: "env_staging_" + project,
		FromEnvironment:   "staging",
		ToEnvironmentID:   to,
		ToEnvironment:     "production",
		Scope:             []string{"api"},
		Status:            StatusPlanning,
		State:             StatePlanning,
		RequestedBy:       "usr_ada",
		Via:               ViaDashboard,
		CreatedAt:         t0,
		UpdatedAt:         t0,
	}
}

func mustCreate(t *testing.T, s *PgStore, r *ReleaseReport) *ReleaseReport {
	t.Helper()
	if err := s.Create(context.Background(), r); err != nil {
		t.Fatalf("create: %v", err)
	}
	return r
}

// mustGet reads one of org_a's reports.
func mustGet(t *testing.T, s *PgStore, id string) *ReleaseReport {
	t.Helper()
	r, err := s.Get(context.Background(), "org_a", id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return r
}

func TestReportRoundTrip(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()

	// A bare report reads back with empty arrays, never nil.
	bare := newReport("org_a", "proj_a", "env_prod_bare")
	bare.Scope = nil
	mustCreate(t, s, bare)
	if !strings.HasPrefix(bare.ID, "rrep_") {
		t.Fatalf("id %q", bare.ID)
	}
	got := mustGet(t, s, bare.ID)
	if got.Scope == nil || got.Gates == nil || got.Concerns == nil || got.Evidence != nil {
		t.Fatalf("bare report: scope %v gates %v concerns %v evidence %v", got.Scope, got.Gates, got.Concerns, got.Evidence)
	}

	// Every stored field comes back as written.
	evaluated, finished := t0.Add(time.Minute), t0.Add(2*time.Minute)
	full := newReport("org_a", "proj_a", "env_prod_full")
	full.Scope = []string{"api", "web"}
	full.Status, full.State, full.StateReason = StatusDone, StatePromoted, "every gate passed"
	full.Via = ViaCLI
	full.PlanID, full.PlanRevision, full.ReusedPlan = "plan_1", "rev_1", true
	full.TaskID, full.RunID = "atask_1", "run_1"
	full.Evidence = &ReadinessEvidence{
		Diffs: []SourceDiff{{
			SourceID: "src_1", Alias: "api", Repository: "acme/api", ProductionCommit: "a", StagingCommit: "b",
			Direction: DirectionAhead, AheadBy: 1, TotalCommits: 1,
			Commits:    []DiffCommit{{SHA: "b", Title: "Add users", Author: "ada", At: t0}},
			Migrations: []MigrationFile{{Path: "db/migrate/1_users.rb", Status: "added", Additions: 3, Patch: "+create_table"}},
		}},
		Canaries:       []CanarySummary{{Status: "promoted", Services: []string{"api"}, ActionIDs: []string{"act_1"}}},
		OpenReports:    []OpenReport{{ID: "arep_1", Title: "Errors up", Severity: "high", FirstSeenAt: t0}},
		Unavailable:    map[string]string{"canaries": "no canary has run"},
		BackupSchedule: true,
	}
	full.Gates = []GateCheck{{Number: 1, Name: "autonomy", Passed: true, Detail: "the project allows it"}}
	full.Preview = false
	full.Decision = DecisionPromote
	full.EvaluatedAt = &evaluated
	full.Concerns = []Concern{{Kind: ConcernRisk, Text: "watch the users table", RunID: "run_1", At: t0}}
	full.Error = ""
	full.FinishedAt = &finished
	mustCreate(t, s, full)

	got = mustGet(t, s, full.ID)
	if !reflect.DeepEqual(got, full) {
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(full)
		t.Fatalf("round trip:\n got %s\nwant %s", a, b)
	}

	// A report that no person asked for is refused.
	for _, by := range []string{"", "agent:org_a", "loomplane:org_a"} {
		noPerson := newReport("org_a", "proj_a", "env_prod_x")
		noPerson.RequestedBy = by
		if err := s.Create(ctx, noPerson); err == nil {
			t.Fatalf("a report requested by %q was stored", by)
		}
	}
	if _, err := s.Get(ctx, "org_a", "rrep_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing report: %v", err)
	}
}

func TestOneActivePreparationPerEnvironment(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()
	first := mustCreate(t, s, newReport("org_a", "proj_a", "env_prod"))
	if err := s.Create(ctx, newReport("org_a", "proj_a", "env_prod")); !errors.Is(err, ErrInProgress) {
		t.Fatalf("second create: %v", err)
	}
	active, err := s.Active(ctx, "org_a", "env_prod")
	if err != nil || active.ID != first.ID {
		t.Fatalf("active: %+v %v", active, err)
	}
	// Reviewing is still in progress.
	if ok, err := s.SetTask(ctx, "org_a", first.ID, "atask_1", "run_1"); !ok || err != nil {
		t.Fatalf("set task: %v %v", ok, err)
	}
	if err := s.Create(ctx, newReport("org_a", "proj_a", "env_prod")); !errors.Is(err, ErrInProgress) {
		t.Fatalf("create while reviewing: %v", err)
	}
	// Another environment isn't blocked.
	mustCreate(t, s, newReport("org_a", "proj_a", "env_other"))

	if err := s.Settle(ctx, "org_a", first.ID, StateReviewing, StatusDone, StatePromoted, "", "", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Active(ctx, "org_a", "env_prod"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("active after done: %v", err)
	}
	third := mustCreate(t, s, newReport("org_a", "proj_a", "env_prod"))
	if third.ID == first.ID {
		t.Fatal("same id")
	}
	settled := mustGet(t, s, first.ID)
	if settled.Status != StatusDone || settled.State != StatePromoted || settled.FinishedAt == nil || !settled.FinishedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("settled: %+v", settled)
	}
}

func TestSetPlanAndSetTaskOnlyOnce(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()
	r := mustCreate(t, s, newReport("org_a", "proj_a", "env_prod"))

	if ok, err := s.SetPlan(ctx, "org_a", r.ID, "plan_1", true); !ok || err != nil {
		t.Fatalf("first set plan: %v %v", ok, err)
	}
	if ok, err := s.SetPlan(ctx, "org_a", r.ID, "plan_2", false); ok || err != nil {
		t.Fatalf("second set plan: %v %v", ok, err)
	}
	got := mustGet(t, s, r.ID)
	if got.PlanID != "plan_1" || !got.ReusedPlan {
		t.Fatalf("plan changed: %s %v", got.PlanID, got.ReusedPlan)
	}

	gates := []GateCheck{{Number: 3, Name: "changes", Passed: false, Detail: "a migration changes a table"}}
	if err := s.SetEvidence(ctx, "org_a", r.ID, "rev_7", &ReadinessEvidence{BackupSchedule: true}, gates); err != nil {
		t.Fatal(err)
	}
	got = mustGet(t, s, r.ID)
	if got.PlanRevision != "rev_7" || !got.Preview || got.Evidence == nil || !got.Evidence.BackupSchedule || !reflect.DeepEqual(got.Gates, gates) {
		t.Fatalf("evidence: %+v", got)
	}

	if ok, err := s.SetTask(ctx, "org_a", r.ID, "atask_1", "run_1"); !ok || err != nil {
		t.Fatalf("first set task: %v %v", ok, err)
	}
	if ok, err := s.SetTask(ctx, "org_a", r.ID, "atask_2", "run_2"); ok || err != nil {
		t.Fatalf("second set task: %v %v", ok, err)
	}
	got = mustGet(t, s, r.ID)
	if got.TaskID != "atask_1" || got.RunID != "run_1" || got.Status != StatusReviewing || got.State != StateReviewing {
		t.Fatalf("task changed: %+v", got)
	}

	// A cancelled preparation never gains a task or a plan.
	c := mustCreate(t, s, newReport("org_a", "proj_a", "env_other"))
	if err := s.Settle(ctx, "org_a", c.ID, StatePlanning, StatusCancelled, StateCancelled, "cancelled by usr_ada", "", t0); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SetPlan(ctx, "org_a", c.ID, "plan_3", false); ok || err != nil {
		t.Fatalf("set plan on a cancelled report: %v %v", ok, err)
	}
	if ok, err := s.SetTask(ctx, "org_a", c.ID, "atask_3", "run_3"); ok || err != nil {
		t.Fatalf("set task on a cancelled report: %v %v", ok, err)
	}
	if got := mustGet(t, s, c.ID); got.Status != StatusCancelled || got.TaskID != "" || got.PlanID != "" {
		t.Fatalf("cancelled report changed: %+v", got)
	}
}

func TestRecordEvaluation(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()
	r := mustCreate(t, s, newReport("org_a", "proj_a", "env_prod"))
	if err := s.SetEvidence(ctx, "org_a", r.ID, "rev_1", &ReadinessEvidence{BackupSchedule: true}, []GateCheck{{Number: 1, Name: "a", Detail: "d"}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SetTask(ctx, "org_a", r.ID, "atask_1", "run_1"); !ok || err != nil {
		t.Fatalf("set task: %v %v", ok, err)
	}
	at := t0.Add(5 * time.Minute)
	gates := []GateCheck{{Number: 1, Name: "a", Passed: true, Detail: "ok"}, {Number: 2, Name: "b", Passed: true, Detail: "ok"}}
	if err := s.RecordEvaluation(ctx, "org_a", r.ID, gates, DecisionPromote, &ReadinessEvidence{}, at); err != nil {
		t.Fatal(err)
	}
	got := mustGet(t, s, r.ID)
	if got.Preview || got.Decision != DecisionPromote || got.EvaluatedAt == nil || !got.EvaluatedAt.Equal(at) ||
		!reflect.DeepEqual(got.Gates, gates) || got.Evidence == nil || got.Evidence.BackupSchedule {
		t.Fatalf("evaluation: %+v", got)
	}
	// Without evidence the stored evidence stays.
	if err := s.RecordEvaluation(ctx, "org_a", r.ID, gates, DecisionAsk, nil, at); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, s, r.ID); got.Decision != DecisionAsk || got.Evidence == nil {
		t.Fatalf("evaluation without evidence: %+v", got)
	}
	if err := s.RecordEvaluation(ctx, "org_a", r.ID, gates, "maybe", nil, at); err == nil {
		t.Fatal("an unknown decision was stored")
	}
}

func TestAddConcernIsCappedAndAppendOnly(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()
	r := mustCreate(t, s, newReport("org_a", "proj_a", "env_prod"))
	for i := range MaxConcerns {
		kind := ConcernRisk
		if i%2 == 1 {
			kind = ConcernAsk
		}
		if err := s.AddConcern(ctx, "org_a", r.ID, Concern{Kind: kind, Text: fmt.Sprintf("concern %d", i), RunID: "run_1", At: t0.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatalf("concern %d: %v", i, err)
		}
	}
	if err := s.AddConcern(ctx, "org_a", r.ID, Concern{Kind: ConcernAsk, Text: "one more", At: t0}); !errors.Is(err, ErrTooManyConcerns) {
		t.Fatalf("concern %d: %v", MaxConcerns+1, err)
	}
	got := mustGet(t, s, r.ID)
	if len(got.Concerns) != MaxConcerns {
		t.Fatalf("%d concerns", len(got.Concerns))
	}
	for i, c := range got.Concerns {
		if c.Text != fmt.Sprintf("concern %d", i) || !c.At.Equal(t0.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("concern %d out of order: %+v", i, c)
		}
	}

	other := mustCreate(t, s, newReport("org_a", "proj_a", "env_other"))
	for _, bad := range []Concern{
		{Kind: "opinion", Text: "x"},
		{Kind: ConcernRisk, Text: ""},
		{Kind: ConcernRisk, Text: strings.Repeat("é", MaxConcernLength+1)},
	} {
		if err := s.AddConcern(ctx, "org_a", other.ID, bad); err == nil {
			t.Fatalf("stored concern %q (%d runes)", bad.Kind, len([]rune(bad.Text)))
		}
	}
	if err := s.AddConcern(ctx, "org_a", other.ID, Concern{Kind: ConcernRisk, Text: strings.Repeat("é", MaxConcernLength)}); err != nil {
		t.Fatalf("a concern of exactly %d characters: %v", MaxConcernLength, err)
	}
	if err := s.AddConcern(ctx, "org_a", "rrep_missing", Concern{Kind: ConcernRisk, Text: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("concern on a missing report: %v", err)
	}
}

func TestForPlanReturnsTheNewest(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()
	older := mustCreate(t, s, newReport("org_a", "proj_a", "env_prod"))
	if _, err := s.SetPlan(ctx, "org_a", older.ID, "plan_1", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(ctx, "org_a", older.ID, StatePlanning, StatusDone, StateStopped, "", "", t0); err != nil {
		t.Fatal(err)
	}
	newer := newReport("org_a", "proj_a", "env_prod")
	newer.CreatedAt, newer.UpdatedAt = t0.Add(time.Hour), t0.Add(time.Hour)
	mustCreate(t, s, newer)
	if _, err := s.SetPlan(ctx, "org_a", newer.ID, "plan_1", true); err != nil {
		t.Fatal(err)
	}
	got, err := s.ForPlan(ctx, "org_a", "plan_1")
	if err != nil || got.ID != newer.ID {
		t.Fatalf("for plan: %+v %v", got, err)
	}
	// A report with no plan yet is never "the report for" the empty plan.
	mustCreate(t, s, newReport("org_a", "proj_a", "env_other"))
	if _, err := s.ForPlan(ctx, "org_a", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("for the empty plan: %v", err)
	}
	if _, err := s.ForPlan(ctx, "org_a", "plan_none"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("for an unknown plan: %v", err)
	}
}

func TestListIsNewestFirstAndLimited(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()
	ids := make([]string, 0, 3)
	for i := range 3 {
		r := newReport("org_a", "proj_a", fmt.Sprintf("env_prod_%d", i))
		r.CreatedAt = t0.Add(time.Duration(i) * time.Minute)
		ids = append(ids, mustCreate(t, s, r).ID)
	}
	mustCreate(t, s, newReport("org_a", "proj_b", "env_prod_b"))
	got, err := s.List(ctx, "org_a", "proj_a", 2)
	if err != nil || len(got) != 2 || got[0].ID != ids[2] || got[1].ID != ids[1] {
		t.Fatalf("list: %v %v", got, err)
	}
	all, err := s.List(ctx, "org_a", "proj_a", 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("list with no limit: %d %v", len(all), err)
	}
}

func TestUnsettledSpansOrganizations(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()
	a := mustCreate(t, s, newReport("org_a", "proj_a", "env_prod_a"))
	b := mustCreate(t, s, newReport("org_b", "proj_b", "env_prod_b"))
	for _, r := range []*ReleaseReport{a, b} {
		if ok, err := s.SetTask(ctx, r.OrgID, r.ID, "atask_"+r.OrgID, "run_"+r.OrgID); !ok || err != nil {
			t.Fatalf("set task: %v %v", ok, err)
		}
	}
	// Done, but a person still has to decide: unsettled.
	waiting := mustCreate(t, s, newReport("org_a", "proj_a", "env_prod_w"))
	if err := s.Settle(ctx, "org_a", waiting.ID, StatePlanning, StatusDone, StateWaiting, "gate 4 failed", "", t0); err != nil {
		t.Fatal(err)
	}
	// Done and terminal: settled.
	done := mustCreate(t, s, newReport("org_a", "proj_a", "env_prod_d"))
	if err := s.Settle(ctx, "org_a", done.ID, StatePlanning, StatusDone, StateApproved, "", "", t0); err != nil {
		t.Fatal(err)
	}
	got, err := s.Unsettled(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(got))
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	slices.Sort(ids)
	want := []string{a.ID, b.ID, waiting.ID}
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("unsettled %v, want %v", ids, want)
	}
	if got, err := s.Unsettled(ctx, 1); err != nil || len(got) != 1 {
		t.Fatalf("limited: %d %v", len(got), err)
	}
}

func TestMarkNotifiedOncePerState(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()
	r := mustCreate(t, s, newReport("org_a", "proj_a", "env_prod"))
	for i, step := range []struct {
		state string
		want  bool
	}{{StatePromoted, true}, {StatePromoted, false}, {StateStale, true}, {StateStale, false}} {
		got, err := s.MarkNotified(ctx, "org_a", r.ID, step.state, nil)
		if err != nil || got != step.want {
			t.Fatalf("step %d (%s): %v %v", i, step.state, got, err)
		}
	}
	if _, err := s.MarkNotified(ctx, "org_a", "rrep_missing", StatePromoted, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing report: %v", err)
	}
}

// Org B never reads, lists or changes org A's reports, even with their IDs.
func TestReportsStayInTheirOrganization(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()
	a := mustCreate(t, s, newReport("org_a", "proj_a", "env_prod_a"))
	if _, err := s.SetPlan(ctx, "org_a", a.ID, "plan_a", false); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Get(ctx, "org_b", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org b read org a's report: %v", err)
	}
	if got, err := s.List(ctx, "org_b", "proj_a", 10); err != nil || len(got) != 0 {
		t.Fatalf("org b listed org a's project: %v %v", got, err)
	}
	if _, err := s.Active(ctx, "org_b", "env_prod_a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org b saw org a's active preparation: %v", err)
	}
	if _, err := s.ForPlan(ctx, "org_b", "plan_a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org b found org a's report by plan: %v", err)
	}
	// Org B can prepare its own environment of the same ID; the active
	// preparation belongs to the organization.
	mustCreate(t, s, newReport("org_b", "proj_a", "env_prod_a"))

	if ok, err := s.SetPlan(ctx, "org_b", a.ID, "plan_b", false); ok || !errors.Is(err, ErrNotFound) {
		t.Fatalf("org b set a plan: %v %v", ok, err)
	}
	if ok, err := s.SetTask(ctx, "org_b", a.ID, "atask_b", "run_b"); ok || !errors.Is(err, ErrNotFound) {
		t.Fatalf("org b set a task: %v %v", ok, err)
	}
	if err := s.SetEvidence(ctx, "org_b", a.ID, "rev_b", &ReadinessEvidence{}, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org b set evidence: %v", err)
	}
	if err := s.RecordEvaluation(ctx, "org_b", a.ID, nil, DecisionPromote, nil, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org b recorded an evaluation: %v", err)
	}
	if err := s.AddConcern(ctx, "org_b", a.ID, Concern{Kind: ConcernRisk, Text: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org b added a concern: %v", err)
	}
	if err := s.Settle(ctx, "org_b", a.ID, StatePlanning, StatusCancelled, StateCancelled, "", "", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org b settled: %v", err)
	}
	if ok, err := s.MarkNotified(ctx, "org_b", a.ID, StatePromoted, nil); ok || !errors.Is(err, ErrNotFound) {
		t.Fatalf("org b marked notified: %v %v", ok, err)
	}

	got := mustGet(t, s, a.ID)
	if got.PlanID != "plan_a" || got.TaskID != "" || got.Evidence != nil || got.Status != StatusPlanning || len(got.Concerns) != 0 || got.Decision != "" {
		t.Fatalf("org b changed org a's report: %+v", got)
	}
	if ok, err := s.MarkNotified(ctx, "org_a", a.ID, StatePromoted, nil); !ok || err != nil {
		t.Fatalf("org a's own notice was spent by org b: %v %v", ok, err)
	}
}

// eventKeys are the JSON keys of ReleaseReportEvent, which the trigger's
// projection must equal.
func eventKeys(t *testing.T) []string {
	t.Helper()
	raw, err := json.Marshal(ReleaseReportEvent{})
	if err != nil {
		t.Fatal(err)
	}
	return jsonKeys(t, raw)
}

func jsonKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func TestReleaseReportEventsAreEmitted(t *testing.T) {
	s, pg := openPg(t)
	ctx := context.Background()
	type event struct {
		Type, EntityID, ProjectID string
		Data                      []byte
	}
	events := func() []event {
		rows, err := pg.Query(ctx, `SELECT type, entity_id, project_id, data::text FROM loomplane_events WHERE type LIKE 'release_report.%' ORDER BY sequence`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []event
		for rows.Next() {
			var e event
			var data string
			if err := rows.Scan(&e.Type, &e.EntityID, &e.ProjectID, &data); err != nil {
				t.Fatal(err)
			}
			e.Data = []byte(data)
			out = append(out, e)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	r := newReport("org_a", "proj_a", "env_prod")
	r.Evidence = &ReadinessEvidence{Diffs: []SourceDiff{{Commits: []DiffCommit{{Title: "secret commit message"}}}}}
	r.Concerns = []Concern{{Kind: ConcernAsk, Text: "secret concern"}}
	mustCreate(t, s, r)

	got := events()
	if len(got) != 1 || got[0].Type != "release_report.updated" || got[0].EntityID != r.ID || got[0].ProjectID != "proj_a" {
		t.Fatalf("insert events: %+v", got)
	}
	want := eventKeys(t)
	if keys := jsonKeys(t, got[0].Data); !slices.Equal(keys, want) {
		t.Fatalf("event keys %v, want %v", keys, want)
	}
	for _, private := range []string{"evidence", "notified_state", "concerns", "request_id", "decided_by"} {
		if slices.Contains(want, private) {
			t.Fatalf("event carries %s", private)
		}
	}
	for _, secret := range []string{"secret commit message", "secret concern"} {
		if strings.Contains(string(got[0].Data), secret) {
			t.Fatalf("event leaks %q: %s", secret, got[0].Data)
		}
	}
	// The event decodes as a ReleaseReportEvent with the report's values.
	var ev ReleaseReportEvent
	if err := json.Unmarshal(got[0].Data, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ID != r.ID || ev.OrgID != "org_a" || ev.ToEnvironmentID != "env_prod" || ev.RequestedBy != "usr_ada" ||
		!slices.Equal(ev.Scope, []string{"api"}) || ev.Status != StatusPlanning || !ev.CreatedAt.Equal(t0) {
		t.Fatalf("event values: %+v", ev)
	}

	// Notice bookkeeping alone emits nothing.
	if ok, err := s.MarkNotified(ctx, "org_a", r.ID, StatePromoted, nil); !ok || err != nil {
		t.Fatalf("mark notified: %v %v", ok, err)
	}
	if n := len(events()); n != 1 {
		t.Fatalf("notice bookkeeping emitted: %d events", n)
	}
	// Changing only the evidence or the concerns emits nothing either, at
	// the table; the store also moves updated_at, which is what clients see.
	if _, err := pg.Exec(ctx, `UPDATE loomplane_release_reports SET evidence = '{"backup_schedule":true}', concerns = '[]' WHERE id = $1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(events()); n != 1 {
		t.Fatalf("evidence or concerns alone emitted: %d events", n)
	}
	if err := s.AddConcern(ctx, "org_a", r.ID, Concern{Kind: ConcernRisk, Text: "another secret concern"}); err != nil {
		t.Fatal(err)
	}
	got = events()
	if len(got) != 2 || strings.Contains(string(got[1].Data), "secret") {
		t.Fatalf("a concern's event: %+v", got)
	}
	if err := s.Settle(ctx, "org_a", r.ID, StatePlanning, StatusFailed, StateFailed, "", "could not plan", t0); err != nil {
		t.Fatal(err)
	}
	got = events()
	if len(got) != 3 {
		t.Fatalf("settle events: %d", len(got))
	}
	if err := json.Unmarshal(got[2].Data, &ev); err != nil || ev.State != StateFailed || ev.Error != "could not plan" || ev.FinishedAt == nil {
		t.Fatalf("settle event: %+v %v", ev, err)
	}

	if _, err := pg.Exec(ctx, `DELETE FROM loomplane_release_reports WHERE id = $1`, r.ID); err != nil {
		t.Fatal(err)
	}
	got = events()
	last := got[len(got)-1]
	if len(got) != 4 || last.Type != "release_report.deleted" || last.EntityID != r.ID {
		t.Fatalf("delete events: %+v", got)
	}
	if keys := jsonKeys(t, last.Data); !slices.Equal(keys, want) {
		t.Fatalf("delete event keys %v", keys)
	}
}

// Racing writers never pass the concern cap or start two preparations of
// one environment.
func TestConcurrentWritersKeepTheLimits(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()
	r := mustCreate(t, s, newReport("org_a", "proj_a", "env_prod"))

	const writers = 30
	errs := make(chan error, writers)
	for i := range writers {
		go func() {
			errs <- s.AddConcern(ctx, "org_a", r.ID, Concern{Kind: ConcernRisk, Text: fmt.Sprintf("c%d", i)})
		}()
	}
	var ok, full int
	for range writers {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, ErrTooManyConcerns):
			full++
		default:
			t.Fatal(err)
		}
	}
	if ok != MaxConcerns || full != writers-MaxConcerns || len(mustGet(t, s, r.ID).Concerns) != MaxConcerns {
		t.Fatalf("%d stored, %d refused", ok, full)
	}

	for i := range writers {
		go func() {
			errs <- s.Create(ctx, newReport("org_a", "proj_a", fmt.Sprintf("env_race_%d", i%2)))
		}()
	}
	ok, full = 0, 0
	for range writers {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, ErrInProgress):
			full++
		default:
			t.Fatal(err)
		}
	}
	if ok != 2 || full != writers-2 {
		t.Fatalf("%d preparations started, %d refused", ok, full)
	}
}

// Fix round 1, I1: a state change applies only to the state it was read
// in, and a settled report never moves again.
func TestSettleIsCompareAndSet(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()

	// A cancel lands, then the job's late write against the state it read.
	r := mustCreate(t, s, newReport("org_a", "proj_a", "env_prod"))
	if err := s.Settle(ctx, "org_a", r.ID, StatePlanning, StatusCancelled, StateCancelled, "cancelled", "", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(ctx, "org_a", r.ID, StatePlanning, StatusFailed, StateFailed, "", "the job failed", t0); !errors.Is(err, ErrMovedOn) {
		t.Fatalf("late job settle: %v", err)
	}
	if err := s.SetEvidence(ctx, "org_a", r.ID, "rev_1", &ReadinessEvidence{}, nil); !errors.Is(err, ErrMovedOn) {
		t.Fatalf("late evidence: %v", err)
	}
	if err := s.RecordEvaluation(ctx, "org_a", r.ID, nil, DecisionPromote, nil, t0); !errors.Is(err, ErrMovedOn) {
		t.Fatalf("late evaluation: %v", err)
	}
	if got := mustGet(t, s, r.ID); got.Status != StatusCancelled || got.State != StateCancelled || got.Error != "" || got.Evidence != nil || got.Decision != "" {
		t.Fatalf("the cancel was overwritten: %+v", got)
	}

	// A cancel and a settle race on a reviewing report: exactly one wins.
	for i := range 10 {
		rr := mustCreate(t, s, newReport("org_a", "proj_a", fmt.Sprintf("env_race_%d", i)))
		if ok, err := s.SetTask(ctx, "org_a", rr.ID, "atask", "run"); !ok || err != nil {
			t.Fatalf("set task: %v %v", ok, err)
		}
		errs := make(chan error, 2)
		go func() {
			errs <- s.Settle(ctx, "org_a", rr.ID, StateReviewing, StatusCancelled, StateCancelled, "", "", t0)
		}()
		go func() { errs <- s.Settle(ctx, "org_a", rr.ID, StateReviewing, StatusDone, StatePromoted, "", "", t0) }()
		var won, lost int
		for range 2 {
			switch err := <-errs; {
			case err == nil:
				won++
			case errors.Is(err, ErrMovedOn):
				lost++
			default:
				t.Fatal(err)
			}
		}
		if won != 1 || lost != 1 {
			t.Fatalf("race %d: %d won, %d lost", i, won, lost)
		}
	}

	// A finished report can't be reopened or take back its environment.
	f := mustCreate(t, s, newReport("org_a", "proj_a", "env_done"))
	if err := s.Settle(ctx, "org_a", f.ID, StatePlanning, StatusDone, StatePromoted, "", "", t0); err != nil {
		t.Fatal(err)
	}
	next := mustCreate(t, s, newReport("org_a", "proj_a", "env_done"))
	for _, status := range []string{StatusPlanning, StatusReviewing} {
		if err := s.Settle(ctx, "org_a", f.ID, StatePromoted, status, StatePlanning, "", "", t0); err == nil {
			t.Fatalf("settled a report back to %s", status)
		}
	}
	if err := s.Settle(ctx, "org_a", f.ID, StatePromoted, StatusCancelled, StateCancelled, "", "", t0); !errors.Is(err, ErrMovedOn) {
		t.Fatalf("a finished report moved again: %v", err)
	}
	if ok, err := s.SetTask(ctx, "org_a", f.ID, "atask_2", "run_2"); ok || err != nil {
		t.Fatalf("a finished report took a task: %v %v", ok, err)
	}
	if active, err := s.Active(ctx, "org_a", "env_done"); err != nil || active.ID != next.ID {
		t.Fatalf("active slot: %+v %v", active, err)
	}
	if got := mustGet(t, s, f.ID); got.Status != StatusDone || got.State != StatePromoted {
		t.Fatalf("finished report changed: %+v", got)
	}

	// Waiting for a person is not final: it settles once, from that state.
	w := mustCreate(t, s, newReport("org_a", "proj_a", "env_wait"))
	if err := s.Settle(ctx, "org_a", w.ID, StatePlanning, StatusDone, StateWaiting, "", "", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(ctx, "org_a", w.ID, StateWaiting, StatusDone, StateApproved, "", "", t0); err != nil {
		t.Fatalf("waiting to approved: %v", err)
	}
	if err := s.Settle(ctx, "org_a", w.ID, StateWaiting, StatusDone, StateRejected, "", "", t0); !errors.Is(err, ErrMovedOn) {
		t.Fatalf("approved, then rejected: %v", err)
	}

	// An evaluation is recorded only while the review runs.
	e := mustCreate(t, s, newReport("org_a", "proj_a", "env_eval"))
	if err := s.RecordEvaluation(ctx, "org_a", e.ID, nil, DecisionPromote, nil, t0); !errors.Is(err, ErrMovedOn) {
		t.Fatalf("evaluation while planning: %v", err)
	}
	if err := s.Settle(ctx, "org_a", "rrep_missing", StatePlanning, StatusFailed, StateFailed, "", "", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("settle a missing report: %v", err)
	}
}

// Fix round 1, I2: the notice is enqueued in the mark's transaction, so a
// failed enqueue leaves the report unmarked and the next sweep retries.
func TestMarkNotifiedRollsBackWhenTheNoticeFails(t *testing.T) {
	s, pg := openPg(t)
	ctx := context.Background()
	if _, err := pg.Exec(ctx, `CREATE TABLE notice_probe (report_id TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	probes := func() int {
		var n int
		if err := pg.QueryRow(ctx, `SELECT count(*) FROM notice_probe`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	r := mustCreate(t, s, newReport("org_a", "proj_a", "env_prod"))
	refuse := errors.New("enqueue failed")
	failing := func(ctx context.Context, tx NoticeTx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO notice_probe VALUES ($1)`, r.ID); err != nil {
			return err
		}
		return refuse
	}
	if ok, err := s.MarkNotified(ctx, "org_a", r.ID, StatePromoted, failing); ok || !errors.Is(err, refuse) {
		t.Fatalf("failing send: %v %v", ok, err)
	}
	if n := probes(); n != 0 {
		t.Fatalf("the failed notice was kept: %d", n)
	}
	sends := 0
	working := func(ctx context.Context, tx NoticeTx) error {
		sends++
		_, err := tx.Exec(ctx, `INSERT INTO notice_probe VALUES ($1)`, r.ID)
		return err
	}
	if ok, err := s.MarkNotified(ctx, "org_a", r.ID, StatePromoted, working); !ok || err != nil {
		t.Fatalf("retry: %v %v", ok, err)
	}
	if ok, err := s.MarkNotified(ctx, "org_a", r.ID, StatePromoted, working); ok || err != nil {
		t.Fatalf("second notice: %v %v", ok, err)
	}
	if sends != 1 || probes() != 1 {
		t.Fatalf("%d sends, %d notices", sends, probes())
	}
}

// Fix round 1, I3: reports that stay unsettled don't starve a newer one.
func TestUnsettledSweepsFairly(t *testing.T) {
	s, pg := openPg(t)
	ctx := context.Background()
	for i := range 150 {
		w := newReport("org_a", "proj_a", fmt.Sprintf("env_wait_%d", i))
		w.Status, w.State = StatusDone, StateWaiting
		mustCreate(t, s, w)
	}
	newer := newReport("org_b", "proj_b", "env_new")
	newer.CreatedAt, newer.UpdatedAt = t0.Add(time.Hour), t0.Add(time.Hour)
	mustCreate(t, s, newer)
	var events int
	count := func() int {
		if err := pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_events WHERE type LIKE 'release_report.%'`).Scan(&events); err != nil {
			t.Fatal(err)
		}
		return events
	}
	before := count()

	reached := 0
	for sweep := 1; sweep <= 2 && reached == 0; sweep++ {
		got, err := s.Unsettled(ctx, 100)
		if err != nil || len(got) != 100 {
			t.Fatalf("sweep %d: %d %v", sweep, len(got), err)
		}
		for _, r := range got {
			if r.ID == newer.ID {
				reached = sweep
			}
		}
	}
	if reached == 0 {
		t.Fatal("two sweeps of 100 never reached the newer report")
	}
	if n := count(); n != before {
		t.Fatalf("sweeping emitted %d events", n-before)
	}
}

// Fix round 1, M6: Unsettled and Terminal agree on every state.
func TestUnsettledMatchesTerminal(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()
	states := []string{StatePlanning, StateReviewing, StatePromoting, StatePromoted, StateWaiting, StateApproved,
		StateRejected, StateStale, StateExpired, StateSuperseded, StateBlocked, StateStopped, StateFailed, StateCancelled}
	byID := map[string]string{}
	for _, state := range states {
		r := newReport("org_a", "proj_a", "env_"+state)
		r.Status, r.State = StatusDone, state
		byID[mustCreate(t, s, r).ID] = state
	}
	got, err := s.Unsettled(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range got {
		seen[byID[r.ID]] = true
	}
	for _, state := range states {
		if seen[state] != !Terminal(state) {
			t.Errorf("state %s: unsettled %v, Terminal %v", state, seen[state], Terminal(state))
		}
	}
}

// Fix round 1, M3 and M7.
func TestStoreKeepsTimesAndIDsHonest(t *testing.T) {
	s, _ := openPg(t)
	ctx := context.Background()
	first := mustCreate(t, s, newReport("org_a", "proj_a", "env_prod"))
	again := newReport("org_a", "proj_a", "env_prod")
	if err := s.Create(ctx, again); !errors.Is(err, ErrInProgress) || again.ID != "" {
		t.Fatalf("refused create kept id %q: %v", again.ID, err)
	}
	if ok, err := s.SetTask(ctx, "org_a", first.ID, "atask", "run"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	before := mustGet(t, s, first.ID).UpdatedAt
	if err := s.RecordEvaluation(ctx, "org_a", first.ID, nil, DecisionAsk, nil, t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := mustGet(t, s, first.ID)
	if got.UpdatedAt.Before(before) || !got.EvaluatedAt.Equal(t0.Add(-time.Hour)) {
		t.Fatalf("updated_at %v went back from %v (evaluated %v)", got.UpdatedAt, before, got.EvaluatedAt)
	}
}
