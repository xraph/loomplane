package agentknowledge_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/xraph/weave"
	"github.com/xraph/weave/document"

	"github.com/xraph/loomplane/internal/agentknowledge"
	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentsettings"
)

// fakeReports serves each workspace's report documents, oldest first, the
// way agentreports.Service.Documents pages them, and records each call.
type fakeReports struct {
	mu    sync.Mutex
	docs  map[string][]agentreports.KnowledgeDocument
	calls map[string][]time.Time // the after of each call, per workspace
}

func (f *fakeReports) Documents(_ context.Context, orgID string, after time.Time, limit int) ([]agentreports.KnowledgeDocument, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string][]time.Time{}
	}
	f.calls[orgID] = append(f.calls[orgID], after)
	out := []agentreports.KnowledgeDocument{}
	for _, d := range f.docs[orgID] {
		if d.UpdatedAt.After(after) && len(out) < limit {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeReports) afters(orgID string) []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.calls[orgID]...)
}

var reportsBase = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

// reportDoc is report id of orgID as 3c hands it over, updated n seconds
// after reportsBase.
func reportDoc(orgID, id string, n int, text string) agentreports.KnowledgeDocument {
	return agentreports.KnowledgeDocument{
		SourceKey: agentreports.SourcePrefix + id, OrgID: orgID, ProjectID: "proj_1",
		Title: "The api deployment to production failed", Text: text,
		UpdatedAt: reportsBase.Add(time.Duration(n) * time.Second),
		Metadata: map[string]string{
			"project_id": "proj_1", "environment": "production", "signal": "deployment_failed",
			"component": "service:api", "status": "resolved", "severity": "high",
		},
	}
}

// reports makes n documents for orgID, each a second after the last.
func reports(orgID string, n int) []agentreports.KnowledgeDocument {
	out := make([]agentreports.KnowledgeDocument, n)
	for i := range out {
		out[i] = reportDoc(orgID, fmt.Sprintf("arep_%d", i+1), i+1, fmt.Sprintf("Report %d: the image wasn't found.", i+1))
	}
	return out
}

func TestReportDocumentMapping(t *testing.T) {
	in := reportDoc("aorg_1", "arep_1", 1, "The deploy of api failed: the image wasn't found.")
	d, err := agentknowledge.ReportDocument(in)
	if err != nil {
		t.Fatal(err)
	}
	if d.SourceKey != "report/arep_1" || d.Kind != agentknowledge.KindReport || d.URL != "/agent/reports/arep_1" ||
		d.Title != in.Title || d.Content != in.Text || d.ProjectID != "proj_1" || !d.Untrusted {
		t.Fatalf("document %+v", d)
	}
	if d.Metadata["report_id"] != "arep_1" || d.Metadata["signal"] != "deployment_failed" || d.Metadata["status"] != "resolved" {
		t.Fatalf("metadata %v", d.Metadata)
	}
	if _, ok := in.Metadata["report_id"]; ok {
		t.Fatal("ReportDocument wrote into 3c's metadata")
	}

	// What's stored says it isn't to be trusted.
	f := newFixture(t, dims)
	if out, ierr := f.svc.Ingest(context.Background(), "aorg_1", d); ierr != nil || out != agentknowledge.OutcomeIndexed {
		t.Fatalf("Ingest = %q, %v", out, ierr)
	}
	docs := f.workspaceDocs(t, "aorg_1", "report/arep_1")
	if len(docs) != 1 {
		t.Fatalf("%d documents for report/arep_1", len(docs))
	}
	if md := docs[0].Metadata; md["trust"] != "untrusted" || md["report_id"] != "arep_1" || md["url"] != "/agent/reports/arep_1" || md["kind"] != "report" {
		t.Fatalf("stored metadata %v", md)
	}

	for _, key := range []string{"something:else", "agent-report:", "report/arep_1", ""} {
		bad := in
		bad.SourceKey = key
		if _, err = agentknowledge.ReportDocument(bad); !errors.Is(err, agentknowledge.ErrInvalid) {
			t.Errorf("ReportDocument(%q): %v, want ErrInvalid", key, err)
		}
	}
}

func TestObserverIngestsAndUpdates(t *testing.T) {
	f := newFixture(t, dims)
	ctx := context.Background()
	o := agentknowledge.ReportObserver{Knowledge: f.svc}
	doc := reportDoc("aorg_1", "arep_1", 1, "The deploy of api failed: the image wasn't found.")
	if err := o.ReportChanged(ctx, doc); err != nil {
		t.Fatal(err)
	}
	embeds := f.emb.count()
	if embeds == 0 {
		t.Fatal("the report wasn't embedded")
	}
	if err := o.ReportChanged(ctx, doc); err != nil {
		t.Fatal(err)
	}
	if n := f.emb.count(); n != embeds {
		t.Fatalf("the same text was embedded again: %d calls, then %d", embeds, n)
	}
	if docs := f.workspaceDocs(t, "aorg_1", "report/arep_1"); len(docs) != 1 {
		t.Fatalf("%d documents after the same text twice", len(docs))
	}

	doc.Text = "The deploy of api failed: out of memory. The agent found a new cache."
	if err := o.ReportChanged(ctx, doc); err != nil {
		t.Fatal(err)
	}
	if f.emb.count() == embeds {
		t.Fatal("the new text wasn't embedded")
	}
	docs := f.workspaceDocs(t, "aorg_1", "report/arep_1")
	if len(docs) != 1 {
		t.Fatalf("%d documents for report/arep_1 after new text, want 1", len(docs))
	}
	if docs[0].ContentLength != len(doc.Text) {
		t.Fatalf("the stored document is %d bytes, want the new text's %d", docs[0].ContentLength, len(doc.Text))
	}
}

func TestObserverRemovesAnEmptiedReport(t *testing.T) {
	f := newFixture(t, dims)
	ctx := context.Background()
	o := agentknowledge.ReportObserver{Knowledge: f.svc}
	doc := reportDoc("aorg_1", "arep_1", 1, "Something broke.")
	if err := o.ReportChanged(ctx, doc); err != nil {
		t.Fatal(err)
	}
	doc.Text = ""
	if err := o.ReportChanged(ctx, doc); err != nil {
		t.Fatal(err)
	}
	if docs := f.workspaceDocs(t, "aorg_1", "report/arep_1"); len(docs) != 0 {
		t.Fatalf("report/arep_1 is still there: %+v", docs)
	}
	// A report with nothing to keep in a workspace with no documents yet.
	if err := o.ReportChanged(ctx, reportDoc("aorg_2", "arep_2", 1, "")); err != nil {
		t.Fatal(err)
	}
}

func TestObserverSkipsWhenKnowledgeIsOff(t *testing.T) {
	f := newFixture(t, dims)
	f.settings.knowledge = map[string]bool{"aorg_1": false}
	o := agentknowledge.ReportObserver{Knowledge: f.svc}
	for _, text := range []string{"Something broke.", ""} {
		if err := o.ReportChanged(context.Background(), reportDoc("aorg_1", "arep_1", 1, text)); err != nil {
			t.Fatalf("ReportChanged(%q): %v", text, err)
		}
	}
	if n := f.eng.count(); n != 0 {
		t.Fatalf("%d engine calls with knowledge off", n)
	}
	// A source key that isn't a report's is refused, not ingested.
	bad := reportDoc("aorg_1", "arep_1", 1, "x")
	bad.SourceKey = "something:else"
	if err := o.ReportChanged(context.Background(), bad); !errors.Is(err, agentknowledge.ErrInvalid) {
		t.Fatalf("a bad source key: %v", err)
	}
}

func TestBackfillPagesByUpdatedAt(t *testing.T) {
	f := newFixture(t, dims)
	ctx := context.Background()
	all := reports("aorg_1", 450)
	r := &fakeReports{docs: map[string][]agentreports.KnowledgeDocument{"aorg_1": all}}
	n, err := f.svc.BackfillReports(ctx, r, "aorg_1")
	if err != nil || n != 450 {
		t.Fatalf("BackfillReports = %d, %v", n, err)
	}
	want := []time.Time{{}, all[199].UpdatedAt, all[399].UpdatedAt}
	got := r.afters("aorg_1")
	if len(got) != len(want) {
		t.Fatalf("Documents called with after %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("call %d: after %v, want %v", i, got[i], want[i])
		}
	}
	for _, key := range []string{"report/arep_1", "report/arep_200", "report/arep_201", "report/arep_450"} {
		if docs := f.workspaceDocs(t, "aorg_1", key); len(docs) != 1 {
			t.Errorf("%d documents for %s", len(docs), key)
		}
	}
	embeds := f.emb.count()
	if n, err = f.svc.BackfillReports(ctx, r, "aorg_1"); err != nil || n != 450 {
		t.Fatalf("second BackfillReports = %d, %v", n, err)
	}
	if f.emb.count() != embeds {
		t.Fatalf("a second backfill embedded %d times", f.emb.count()-embeds)
	}
}

func TestBackfillStopsAtTheBudget(t *testing.T) {
	f := newFixture(t, dims)
	ctx := context.Background()
	f.settings.budgets = map[string]error{"aorg_1": fmt.Errorf("spent: %w", agentsettings.ErrBudgetExhausted)}
	f.settings.knowledge = map[string]bool{"aorg_3": false}
	r := &fakeReports{docs: map[string][]agentreports.KnowledgeDocument{
		"aorg_1": reports("aorg_1", 250), "aorg_2": reports("aorg_2", 3), "aorg_3": reports("aorg_3", 3),
	}}

	n, err := f.svc.BackfillReports(ctx, r, "aorg_1")
	if !errors.Is(err, agentsettings.ErrBudgetExhausted) || n != 0 {
		t.Fatalf("BackfillReports at the budget = %d, %v", n, err)
	}
	if calls := r.afters("aorg_1"); len(calls) != 1 {
		t.Fatalf("aorg_1 read %d pages at the budget, want 1", len(calls))
	}

	r.calls = nil
	job := agentknowledge.BackfillReportsJob(f.svc, r, func(context.Context) ([]string, error) {
		return []string{"aorg_1", "aorg_2", "aorg_3"}, nil
	})
	if job.Name != agentknowledge.JobBackfillReports {
		t.Fatalf("job %q", job.Name)
	}
	if err = job.Handler(ctx, struct{}{}); err != nil {
		t.Fatalf("the job failed on a workspace at its budget: %v", err)
	}
	if calls := r.afters("aorg_1"); len(calls) != 1 {
		t.Fatalf("aorg_1 read %d pages at the budget, want 1", len(calls))
	}
	if docs := f.workspaceDocs(t, "aorg_1", ""); len(docs) != 0 {
		t.Fatalf("aorg_1 has %d documents over its budget", len(docs))
	}
	for _, key := range []string{"report/arep_1", "report/arep_2", "report/arep_3"} {
		if docs := f.workspaceDocs(t, "aorg_2", key); len(docs) != 1 {
			t.Errorf("aorg_2: %d documents for %s", len(docs), key)
		}
	}
	// A workspace with knowledge off isn't read at all.
	if calls := r.afters("aorg_3"); len(calls) != 0 {
		t.Fatalf("aorg_3, with knowledge off, was read %d times", len(calls))
	}
	if len(f.locks) != 1 || f.locks[0] != "agent-knowledge-reports" {
		t.Fatalf("locks %v", f.locks)
	}
}

// The job does nothing while knowledge is unavailable, so a schedule left
// from a boot that had it costs nothing; an error reading the workspaces
// fails it.
func TestBackfillJobWithoutKnowledge(t *testing.T) {
	f := newFixture(t, dims)
	f.svc.SetStatus(agentsettings.KnowledgeStatus{Configured: true, Reason: "no pgvector"})
	listed := false
	job := agentknowledge.BackfillReportsJob(f.svc, &fakeReports{}, func(context.Context) ([]string, error) {
		listed = true
		return []string{"aorg_1"}, nil
	})
	if err := job.Handler(context.Background(), struct{}{}); err != nil {
		t.Fatal(err)
	}
	if listed || len(f.locks) != 0 || f.eng.count() != 0 {
		t.Fatalf("listed %v, locks %v, %d engine calls", listed, f.locks, f.eng.count())
	}

	f.svc.SetStatus(agentsettings.KnowledgeStatus{Configured: true, Available: true})
	boom := errors.New("the settings table is gone")
	job = agentknowledge.BackfillReportsJob(f.svc, &fakeReports{}, func(context.Context) ([]string, error) { return nil, boom })
	if err := job.Handler(context.Background(), struct{}{}); !errors.Is(err, boom) {
		t.Fatalf("job: %v", err)
	}
}

func TestReportsStayInTheirWorkspace(t *testing.T) {
	f := newFixture(t, dims)
	ctx := context.Background()
	o := agentknowledge.ReportObserver{Knowledge: f.svc}
	if err := o.ReportChanged(ctx, reportDoc("aorg_1", "arep_1", 1, "Only aorg_1 may read this.")); err != nil {
		t.Fatal(err)
	}
	docs, err := f.weave.ListDocuments(weave.WithTenant(ctx, "aorg_2"), &document.ListFilter{})
	if err != nil || len(docs) != 0 {
		t.Fatalf("aorg_2 lists %d documents, %v", len(docs), err)
	}
	if docs := f.workspaceDocs(t, "aorg_1", "report/arep_1"); len(docs) != 1 {
		t.Fatalf("aorg_1 has %d documents for report/arep_1", len(docs))
	}
	// A backfill of aorg_2 handed aorg_1's reports keeps none of them, in
	// either workspace.
	r := &fakeReports{docs: map[string][]agentreports.KnowledgeDocument{"aorg_2": reports("aorg_1", 2)}}
	if n, err := f.svc.BackfillReports(ctx, r, "aorg_2"); !errors.Is(err, agentknowledge.ErrInvalid) || n != 0 {
		t.Fatalf("BackfillReports(aorg_2) with aorg_1's reports = %d, %v", n, err)
	}
	if docs, err := f.weave.ListDocuments(weave.WithTenant(ctx, "aorg_2"), &document.ListFilter{}); err != nil || len(docs) != 0 {
		t.Fatalf("after its backfill aorg_2 lists %d documents, %v", len(docs), err)
	}
	if docs := f.workspaceDocs(t, "aorg_1", "report/arep_2"); len(docs) != 0 {
		t.Fatalf("aorg_2's backfill wrote into aorg_1: %d documents", len(docs))
	}
}
