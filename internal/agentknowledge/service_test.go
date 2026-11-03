package agentknowledge_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/nexus/pipeline"
	"github.com/xraph/weave"
	"github.com/xraph/weave/chunker"
	"github.com/xraph/weave/collection"
	"github.com/xraph/weave/document"
	"github.com/xraph/weave/embedder"
	weaveengine "github.com/xraph/weave/engine"
	"github.com/xraph/weave/id"
	memstore "github.com/xraph/weave/store/memory"
	vecmemory "github.com/xraph/weave/vectorstore/memory"

	"github.com/xraph/loomplane/internal/agentknowledge"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/config"
)

const systemTenant = "nx-loomplane-system"

// countingEmbedder returns vectors of size out and counts its calls. Like
// weave's nexus embedder, it refuses vectors of another size than dims
// with weave.ErrDimensionMismatch.
type countingEmbedder struct {
	dims, out int

	mu      sync.Mutex
	calls   int
	tenants []string // pipeline.TenantID of each call
}

func (e *countingEmbedder) Embed(ctx context.Context, texts []string) ([]embedder.EmbedResult, error) {
	e.mu.Lock()
	e.calls++
	e.tenants = append(e.tenants, pipeline.TenantID(ctx))
	e.mu.Unlock()
	if e.out != e.dims {
		return nil, fmt.Errorf("%w: model %q returned %d dimensions, want %d", weave.ErrDimensionMismatch, "fake", e.out, e.dims)
	}
	out := make([]embedder.EmbedResult, len(texts))
	for i, text := range texts {
		v := make([]float32, e.out)
		for j := range v {
			v[j] = float32((len(text)+j)%7) + 1
		}
		out[i] = embedder.EmbedResult{Vector: v, TokenCount: 1}
	}
	return out, nil
}

func (e *countingEmbedder) Dimensions() int { return e.dims }

func (e *countingEmbedder) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// spyEngine counts the calls that reach weave.
type spyEngine struct {
	agentknowledge.Engine
	mu      sync.Mutex
	calls   int
	failKey string // Ingest of this source key fails
}

func (s *spyEngine) touch() {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
}

func (s *spyEngine) CreateCollection(ctx context.Context, col *collection.Collection) error {
	s.touch()
	return s.Engine.CreateCollection(ctx, col)
}

func (s *spyEngine) GetCollectionByName(ctx context.Context, name string) (*collection.Collection, error) {
	s.touch()
	return s.Engine.GetCollectionByName(ctx, name)
}

func (s *spyEngine) Ingest(ctx context.Context, in *weaveengine.IngestInput) (*weaveengine.IngestResult, error) {
	s.touch()
	if in.SourceKey == s.failKey {
		return nil, errors.New("the provider refused it")
	}
	return s.Engine.Ingest(ctx, in)
}

func (s *spyEngine) DeleteBySource(ctx context.Context, colID id.CollectionID, sourceKey string) error {
	s.touch()
	return s.Engine.DeleteBySource(ctx, colID, sourceKey)
}

func (s *spyEngine) ListDocuments(ctx context.Context, f *document.ListFilter) ([]*document.Document, error) {
	s.touch()
	return s.Engine.ListDocuments(ctx, f)
}

func (s *spyEngine) RecoverStalled(ctx context.Context, after time.Duration) (weaveengine.RecoveryResult, error) {
	s.touch()
	return s.Engine.RecoverStalled(ctx, after)
}

func (s *spyEngine) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// fakeSettings answers Effective and RequireBudget as told and records the
// knowledge status it is given.
type fakeSettings struct {
	mu        sync.Mutex
	knowledge map[string]bool // per workspace; missing is on
	effErr    error
	budgetErr error
	statuses  []agentsettings.KnowledgeStatus
}

func (f *fakeSettings) Effective(_ context.Context, orgID string) (*agentsettings.Effective, error) {
	if f.effErr != nil {
		return nil, f.effErr
	}
	on, ok := f.knowledge[orgID]
	return &agentsettings.Effective{OrgID: orgID, Enabled: true, Knowledge: !ok || on}, nil
}

func (f *fakeSettings) RequireBudget(context.Context, string) error { return f.budgetErr }

func (f *fakeSettings) SetKnowledge(st agentsettings.KnowledgeStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, st)
}

func (f *fakeSettings) last() agentsettings.KnowledgeStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.statuses) == 0 {
		return agentsettings.KnowledgeStatus{}
	}
	return f.statuses[len(f.statuses)-1]
}

// tenants is each workspace's nexus tenant, "nx-" and its ID.
type tenants struct{}

func (tenants) NexusTenant(_ context.Context, orgID string) (string, error) {
	return "nx-" + orgID, nil
}

func page(path, title, text string) agentknowledge.DocPage {
	url := agentknowledge.DocsBaseURL
	if path != "" {
		url += "/" + path
	}
	return agentknowledge.DocPage{Path: path, Title: title, Description: title + " in short.", URL: url, Text: text}
}

// threePages is a bundle of three pages, the root among them.
func threePages() agentknowledge.Bundle {
	return agentknowledge.Bundle{Version: 1, Digest: "one", Pages: []agentknowledge.DocPage{
		page("", "Loomplane", "Loomplane deploys your projects."),
		page("agents/old", "An old page", "This page is going away."),
		page("ai/models", "Models", "Pick a model for the agent."),
	}}
}

type fixture struct {
	svc      *agentknowledge.Service
	eng      *spyEngine
	weave    *weaveengine.Engine
	emb      *countingEmbedder
	settings *fakeSettings
	bundle   agentknowledge.Bundle
	locks    []string
}

const dims = 8

func newFixture(t *testing.T, out int) *fixture {
	t.Helper()
	return newStallFixture(t, out, 0)
}

// newStallFixture is newFixture with weave and Recover counting an ingest
// stalled after stall (0: weave's default).
func newStallFixture(t *testing.T, out int, stall time.Duration) *fixture {
	t.Helper()
	f := &fixture{emb: &countingEmbedder{dims: dims, out: out}, settings: &fakeSettings{}, bundle: threePages()}
	eng, err := weaveengine.New(
		weaveengine.WithStore(memstore.New()),
		weaveengine.WithVectorStore(vecmemory.New()),
		weaveengine.WithEmbedder(f.emb),
		weaveengine.WithChunker(chunker.NewRecursiveChunker()),
		weaveengine.WithRecovery(weaveengine.RecoveryConfig{StalledAfter: stall}),
	)
	if err != nil {
		t.Fatal(err)
	}
	f.weave = eng
	f.eng = &spyEngine{Engine: eng}
	f.svc = agentknowledge.NewService(agentknowledge.Deps{
		Engine:   func() agentknowledge.Engine { return f.eng },
		Settings: f.settings,
		Binder:   &agentscope.Binder{Tenants: tenants{}},
		System:   func(context.Context) (string, error) { return systemTenant, nil },
		Config: config.AIKnowledgeConfig{
			EmbeddingProvider: config.AIProviderOpenAI, EmbeddingModel: "text-embedding-3-small", Dimensions: dims,
		},
		Lock: func(ctx context.Context, name string, fn func(context.Context) error) error {
			f.locks = append(f.locks, name)
			return fn(ctx)
		},
		Docs:         func() (agentknowledge.Bundle, error) { return f.bundle, nil },
		Schema:       []byte(`{"$id": "https://loomplane.dev/schemas/loomplane.json", "type": "object"}`),
		StalledAfter: stall,
		Warn:         func(string, error) {},
	})
	f.svc.SetStatus(agentsettings.KnowledgeStatus{Configured: true, Available: true})
	return f
}

// docs lists the documents of sourceKey in the docs collection.
func (f *fixture) docs(t *testing.T, sourceKey string) []*document.Document {
	t.Helper()
	ctx := weave.WithTenant(context.Background(), agentknowledge.DocsTenant)
	col, err := f.weave.GetCollectionByName(ctx, agentknowledge.DocsCollection)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := f.weave.ListDocuments(ctx, &document.ListFilter{CollectionID: col.ID, SourceKey: sourceKey})
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

// workspaceDocs lists the documents of sourceKey in orgID's collection.
func (f *fixture) workspaceDocs(t *testing.T, orgID, sourceKey string) []*document.Document {
	t.Helper()
	ctx := weave.WithTenant(context.Background(), orgID)
	col, err := f.weave.GetCollectionByName(ctx, agentknowledge.WorkspaceCollection)
	if errors.Is(err, weave.ErrCollectionNotFound) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	docs, err := f.weave.ListDocuments(ctx, &document.ListFilter{CollectionID: col.ID, SourceKey: sourceKey})
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

func TestIngestDocsTwiceEmbedsOnce(t *testing.T) {
	f := newFixture(t, dims)
	ctx := context.Background()
	got, err := f.svc.IngestDocs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (agentknowledge.DocsResult{Indexed: 4}); got != want {
		t.Fatalf("first ingest %+v, want %+v", got, want)
	}
	embeds := f.emb.count()
	if embeds == 0 {
		t.Fatal("the first ingest embedded nothing")
	}
	if len(f.locks) != 1 || f.locks[0] != "agent-knowledge-docs" {
		t.Fatalf("locks %v", f.locks)
	}

	got, err = f.svc.IngestDocs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (agentknowledge.DocsResult{Unchanged: 4}); got != want {
		t.Fatalf("second ingest %+v, want %+v", got, want)
	}
	if n := f.emb.count(); n != embeds {
		t.Fatalf("the second ingest embedded again: %d calls, then %d", embeds, n)
	}
	for _, key := range []string{"docs/index", "docs/agents/old", "docs/ai/models", "schema/loomplane.json"} {
		if docs := f.docs(t, key); len(docs) != 1 {
			t.Errorf("%d documents for %s, want 1", len(docs), key)
		}
	}
	root := f.docs(t, "docs/index")[0]
	if root.Title != "Loomplane" || root.Source != agentknowledge.DocsBaseURL ||
		root.Metadata["kind"] != agentknowledge.KindDocs || root.Metadata["url"] != agentknowledge.DocsBaseURL || root.Metadata["title"] != "Loomplane" {
		t.Errorf("root page %+v", root)
	}
}

func TestIngestDocsRemovesPagesThatAreGone(t *testing.T) {
	f := newFixture(t, dims)
	ctx := context.Background()
	if _, err := f.svc.IngestDocs(ctx); err != nil {
		t.Fatal(err)
	}
	f.bundle = agentknowledge.Bundle{Version: 1, Digest: "two", Pages: []agentknowledge.DocPage{f.bundle.Pages[0], f.bundle.Pages[2]}}
	got, err := f.svc.IngestDocs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (agentknowledge.DocsResult{Unchanged: 3, Removed: 1}); got != want {
		t.Fatalf("ingest %+v, want %+v", got, want)
	}
	if docs := f.docs(t, "docs/agents/old"); len(docs) != 0 {
		t.Fatalf("the removed page is still there: %+v", docs)
	}
	for _, key := range []string{"docs/index", "docs/ai/models", "schema/loomplane.json"} {
		if docs := f.docs(t, key); len(docs) != 1 {
			t.Errorf("%d documents for %s, want 1", len(docs), key)
		}
	}
}

func TestIngestDocsChargesTheSystemTenant(t *testing.T) {
	f := newFixture(t, dims)
	if _, err := f.svc.IngestDocs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.emb.tenants) == 0 {
		t.Fatal("nothing was embedded")
	}
	for i, tenant := range f.emb.tenants {
		if tenant != systemTenant {
			t.Fatalf("embed %d billed %q, want %q", i, tenant, systemTenant)
		}
	}
}

func TestIngestDocsUnderTheDocsTenant(t *testing.T) {
	f := newFixture(t, dims)
	if _, err := f.svc.IngestDocs(context.Background()); err != nil {
		t.Fatal(err)
	}
	col, err := f.weave.GetCollectionByName(weave.WithTenant(context.Background(), agentknowledge.DocsTenant), agentknowledge.DocsCollection)
	if err != nil {
		t.Fatal(err)
	}
	if col.TenantID != "loomplane-docs" || col.AppID != "loomplane" || col.EmbeddingModel != "text-embedding-3-small" || col.EmbeddingDims != dims {
		t.Fatalf("collection %+v", col)
	}
	if _, err = f.weave.GetCollectionByName(weave.WithTenant(context.Background(), "aorg_1"), agentknowledge.DocsCollection); !errors.Is(err, weave.ErrCollectionNotFound) {
		t.Fatalf("a workspace found the docs collection: %v", err)
	}
}

func TestIngestDocsSkipsWhenUnavailable(t *testing.T) {
	f := newFixture(t, dims)
	f.svc.SetStatus(agentsettings.KnowledgeStatus{Configured: true, Reason: "no pgvector"})
	got, err := f.svc.IngestDocs(context.Background())
	if err != nil || got != (agentknowledge.DocsResult{}) {
		t.Fatalf("IngestDocs = %+v, %v", got, err)
	}
	if n := f.eng.count(); n != 0 || len(f.locks) != 0 {
		t.Fatalf("%d engine calls, locks %v", n, f.locks)
	}
}

// Review focus 3: vectors of another size than ai.knowledge.dimensions turn
// knowledge off with the reason, never a crash.
func TestEmbeddingSizeMismatchTurnsKnowledgeOff(t *testing.T) {
	f := newFixture(t, 768)
	f.emb.dims = 1536
	f.svc = agentknowledge.NewService(agentknowledge.Deps{
		Engine:   func() agentknowledge.Engine { return f.eng },
		Settings: f.settings,
		Binder:   &agentscope.Binder{Tenants: tenants{}},
		System:   func(context.Context) (string, error) { return systemTenant, nil },
		Config: config.AIKnowledgeConfig{
			EmbeddingProvider: config.AIProviderOpenAI, EmbeddingModel: "text-embedding-3-small", Dimensions: 1536,
		},
		Lock: func(ctx context.Context, _ string, fn func(context.Context) error) error { return fn(ctx) },
		Docs: func() (agentknowledge.Bundle, error) { return f.bundle, nil },
	})
	f.svc.SetStatus(agentsettings.KnowledgeStatus{Configured: true, Available: true})

	_, err := f.svc.IngestDocs(context.Background())
	if !errors.Is(err, weave.ErrDimensionMismatch) {
		t.Fatalf("IngestDocs: %v, want weave.ErrDimensionMismatch", err)
	}
	st := f.svc.Status()
	if !st.Configured || st.Available {
		t.Fatalf("status %+v", st)
	}
	for _, want := range []string{"ai.knowledge.embedding_model text-embedding-3-small", "(1536)"} {
		if !strings.Contains(st.Reason, want) {
			t.Errorf("reason %q doesn't name %q", st.Reason, want)
		}
	}
	if got := f.settings.last(); got != st {
		t.Fatalf("settings got %+v, want %+v", got, st)
	}
	// Knowledge is off now: a workspace's ingest is skipped, not failed.
	out, err := f.svc.Ingest(context.Background(), "aorg_1", agentknowledge.Document{SourceKey: "report/r1", Kind: agentknowledge.KindReport, Title: "t", Content: "c"})
	if err != nil || out != agentknowledge.OutcomeSkipped {
		t.Fatalf("Ingest after the mismatch = %q, %v", out, err)
	}
}

func report(content string) agentknowledge.Document {
	return agentknowledge.Document{
		SourceKey: "report/r1", Kind: agentknowledge.KindReport, Title: "Deploy failed",
		Content: content, URL: "/agent/reports/r1", ProjectID: "proj_1",
		Metadata: map[string]string{"kind": "spoofed", "severity": "high"},
	}
}

func TestIngestReportIntoTheWorkspace(t *testing.T) {
	f := newFixture(t, dims)
	ctx := context.Background()
	out, err := f.svc.Ingest(ctx, "aorg_1", report("The deploy of api failed: the image wasn't found."))
	if err != nil || out != agentknowledge.OutcomeIndexed {
		t.Fatalf("first Ingest = %q, %v", out, err)
	}
	col, err := f.weave.GetCollectionByName(weave.WithTenant(ctx, "aorg_1"), agentknowledge.WorkspaceCollection)
	if err != nil {
		t.Fatal(err)
	}
	if col.TenantID != "aorg_1" || col.AppID != agentknowledge.App {
		t.Fatalf("collection %+v", col)
	}
	for i, tenant := range f.emb.tenants {
		if tenant != "nx-aorg_1" {
			t.Fatalf("embed %d billed %q, want the workspace's tenant", i, tenant)
		}
	}
	embeds := f.emb.count()

	out, err = f.svc.Ingest(ctx, "aorg_1", report("The deploy of api failed: the image wasn't found."))
	if err != nil || out != agentknowledge.OutcomeUnchanged {
		t.Fatalf("same again = %q, %v", out, err)
	}
	if f.emb.count() != embeds {
		t.Fatal("the same content was embedded again")
	}

	out, err = f.svc.Ingest(ctx, "aorg_1", report("The deploy of api failed again: out of memory."))
	if err != nil || out != agentknowledge.OutcomeIndexed {
		t.Fatalf("new content = %q, %v", out, err)
	}
	docs := f.workspaceDocs(t, "aorg_1", "report/r1")
	if len(docs) != 1 {
		t.Fatalf("%d documents for report/r1, want 1", len(docs))
	}
	md := docs[0].Metadata
	if md["kind"] != "report" || md["title"] != "Deploy failed" || md["url"] != "/agent/reports/r1" || md["project_id"] != "proj_1" || md["severity"] != "high" {
		t.Fatalf("metadata %v", md)
	}
}

func TestIngestSkipsWhenTheWorkspaceHasKnowledgeOff(t *testing.T) {
	f := newFixture(t, dims)
	f.settings.knowledge = map[string]bool{"aorg_1": false}
	out, err := f.svc.Ingest(context.Background(), "aorg_1", report("x"))
	if err != nil || out != agentknowledge.OutcomeSkipped {
		t.Fatalf("knowledge off: %q, %v", out, err)
	}
	f.settings.effErr = fmt.Errorf("read: %w", agentsettings.ErrDisabled)
	out, err = f.svc.Ingest(context.Background(), "aorg_1", report("x"))
	if err != nil || out != agentknowledge.OutcomeSkipped {
		t.Fatalf("agent off: %q, %v", out, err)
	}
	if n := f.eng.count(); n != 0 {
		t.Fatalf("%d engine calls", n)
	}
}

func TestIngestRefusesAtBudget(t *testing.T) {
	f := newFixture(t, dims)
	f.settings.budgetErr = agentsettings.ErrBudgetExhausted
	out, err := f.svc.Ingest(context.Background(), "aorg_1", report("x"))
	if !errors.Is(err, agentsettings.ErrBudgetExhausted) || out == agentknowledge.OutcomeIndexed {
		t.Fatalf("Ingest = %q, %v", out, err)
	}
	if f.emb.count() != 0 {
		t.Fatal("embedded over budget")
	}
}

func TestIngestValidates(t *testing.T) {
	f := newFixture(t, dims)
	for _, d := range []agentknowledge.Document{
		{SourceKey: "", Kind: agentknowledge.KindReport, Content: "x"},
		{SourceKey: "  ", Kind: agentknowledge.KindRunbook, Content: "x"},
		{SourceKey: "docs/x", Kind: agentknowledge.KindDocs, Content: "x"},
	} {
		if _, err := f.svc.Ingest(context.Background(), "aorg_1", d); !errors.Is(err, agentknowledge.ErrInvalid) {
			t.Errorf("Ingest(%+v): %v, want ErrInvalid", d, err)
		}
	}
}

func TestRemoveDeletesTheSource(t *testing.T) {
	f := newFixture(t, dims)
	ctx := context.Background()
	if _, err := f.svc.Ingest(ctx, "aorg_1", report("Something broke.")); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Remove(ctx, "aorg_1", "report/r1"); err != nil {
		t.Fatal(err)
	}
	if docs := f.workspaceDocs(t, "aorg_1", "report/r1"); len(docs) != 0 {
		t.Fatalf("report/r1 is still there: %+v", docs)
	}
	if err := f.svc.Remove(ctx, "aorg_2", "report/r1"); err != nil {
		t.Fatalf("Remove with no collection: %v", err)
	}
}

func TestWorkspacesNeverSeeEachOther(t *testing.T) {
	f := newFixture(t, dims)
	ctx := context.Background()
	if _, err := f.svc.Ingest(ctx, "aorg_1", report("Only aorg_1 may read this.")); err != nil {
		t.Fatal(err)
	}
	docs, err := f.weave.ListDocuments(weave.WithTenant(ctx, "aorg_2"), &document.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 0 {
		t.Fatalf("aorg_2 sees %d documents", len(docs))
	}
	if _, err = f.weave.GetCollectionByName(weave.WithTenant(ctx, "aorg_2"), agentknowledge.WorkspaceCollection); !errors.Is(err, weave.ErrCollectionNotFound) {
		t.Fatalf("aorg_2 found a workspace collection: %v", err)
	}
	// Removing from another workspace never touches aorg_1's report.
	if err = f.svc.Remove(ctx, "aorg_2", "report/r1"); err != nil {
		t.Fatal(err)
	}
	if docs := f.workspaceDocs(t, "aorg_1", "report/r1"); len(docs) != 1 {
		t.Fatalf("aorg_1 has %d documents for report/r1, want 1", len(docs))
	}
}

// Without an engine (knowledge not configured), a workspace's ingest is
// skipped.
func TestIngestWithoutAnEngineSkips(t *testing.T) {
	svc := agentknowledge.NewService(agentknowledge.Deps{Settings: &fakeSettings{}})
	svc.SetStatus(agentsettings.KnowledgeStatus{Configured: false, Reason: agentknowledge.ReasonNotConfigured})
	out, err := svc.Ingest(context.Background(), "aorg_1", report("x"))
	if err != nil || out != agentknowledge.OutcomeSkipped {
		t.Fatalf("Ingest = %q, %v", out, err)
	}
	if err = svc.Remove(context.Background(), "aorg_1", "report/r1"); err != nil {
		t.Fatal(err)
	}
	if res, derr := svc.IngestDocs(context.Background()); derr != nil || res != (agentknowledge.DocsResult{}) {
		t.Fatalf("IngestDocs = %+v, %v", res, derr)
	}
	if err = svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Recovery reaches weave only while knowledge is available.
func TestRecoverOnlyWhenAvailable(t *testing.T) {
	f := newFixture(t, dims)
	if err := f.svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.eng.count() != 1 {
		t.Fatalf("%d engine calls, want RecoverStalled", f.eng.count())
	}
	f.svc.SetStatus(agentsettings.KnowledgeStatus{Configured: true, Reason: "off"})
	if err := f.svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.eng.count() != 1 {
		t.Fatal("recovered while knowledge was off")
	}
}

// The jobs run the service and are named as the platform schedules them.
func TestJobs(t *testing.T) {
	f := newFixture(t, dims)
	docs := agentknowledge.IngestDocsJob(f.svc)
	if docs.Name != agentknowledge.JobIngestDocs {
		t.Fatalf("docs job %q", docs.Name)
	}
	if err := docs.Handler(context.Background(), struct{}{}); err != nil {
		t.Fatal(err)
	}
	if len(f.docs(t, "docs/index")) != 1 {
		t.Fatal("the docs job indexed nothing")
	}
	recoverJob := agentknowledge.RecoverJob(f.svc)
	if recoverJob.Name != agentknowledge.JobRecover {
		t.Fatalf("recover job %q", recoverJob.Name)
	}
	if err := recoverJob.Handler(context.Background(), struct{}{}); err != nil {
		t.Fatal(err)
	}
}

// A run that dies mid-page leaves the page processing. A run started
// within the stall window finds it so and leaves it, without counting it
// indexed; once the page has gone quiet, recovery fails it and indexes the
// docs again, embedding that page and nothing else.
func TestCrashedPageIsIndexedAfterRecovery(t *testing.T) {
	const stall = 500 * time.Millisecond
	f := newStallFixture(t, dims, stall)
	ctx := context.Background()
	full := f.bundle
	f.bundle = agentknowledge.Bundle{Version: 1, Pages: full.Pages[:1]}
	if _, err := f.svc.IngestDocs(ctx); err != nil {
		t.Fatal(err)
	}

	// The crashed run's page: processing, with a fresh heartbeat.
	models := full.Pages[2]
	content := "# " + models.Title + "\n\n" + models.Description + "\n\n" + models.Text
	dctx := weave.WithTenant(ctx, agentknowledge.DocsTenant)
	col, err := f.weave.GetCollectionByName(dctx, agentknowledge.DocsCollection)
	if err != nil {
		t.Fatal(err)
	}
	crashed := &document.Document{
		Entity: weave.NewEntity(), ID: id.NewDocumentID(), CollectionID: col.ID, TenantID: agentknowledge.DocsTenant,
		Title: models.Title, Source: models.URL, ContentHash: fmt.Sprintf("%x", sha256.Sum256([]byte(content))),
		ContentLength: len(content), SourceKey: "docs/ai/models", State: document.StateProcessing,
	}
	if err = f.weave.Store().CreateDocument(dctx, crashed); err != nil {
		t.Fatal(err)
	}

	f.bundle = full
	got, err := f.svc.IngestDocs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (agentknowledge.DocsResult{Indexed: 1, Unchanged: 2}); got != want {
		t.Fatalf("with the page still processing %+v, want %+v (the page counted nowhere)", got, want)
	}
	state := func() document.State {
		t.Helper()
		docs := f.docs(t, "docs/ai/models")
		if len(docs) != 1 {
			t.Fatalf("%d documents for docs/ai/models", len(docs))
		}
		return docs[0].State
	}
	if st := state(); st != document.StateProcessing {
		t.Fatalf("the crashed page is %s", st)
	}
	// Not quiet long enough yet: recovery leaves it.
	if err = f.svc.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if st := state(); st != document.StateProcessing {
		t.Fatalf("recovery took a page that hadn't stalled: %s", st)
	}

	embeds := f.emb.count()
	time.Sleep(stall + 200*time.Millisecond)
	if err = f.svc.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if st := state(); st != document.StateReady {
		t.Fatalf("after recovery the page is %s, want ready", st)
	}
	if n := f.emb.count() - embeds; n != 1 {
		t.Fatalf("recovery made %d embed calls, want 1 (the crashed page alone)", n)
	}
}

// No workspace path writes the docs: the docs' tenant, or no org at all,
// isn't a workspace.
func TestIngestRefusesTheDocsTenant(t *testing.T) {
	f := newFixture(t, dims)
	ctx := context.Background()
	for _, org := range []string{agentknowledge.DocsTenant, "", " "} {
		if _, err := f.svc.Ingest(ctx, org, report("x")); !errors.Is(err, agentknowledge.ErrInvalid) {
			t.Errorf("Ingest(%q): %v, want ErrInvalid", org, err)
		}
		if err := f.svc.Remove(ctx, org, "docs/index"); !errors.Is(err, agentknowledge.ErrInvalid) {
			t.Errorf("Remove(%q): %v, want ErrInvalid", org, err)
		}
	}
	if n := f.eng.count(); n != 0 {
		t.Fatalf("%d engine calls", n)
	}
}

// One page that fails doesn't stop the rest: the other pages are indexed,
// pages the bundle dropped are still removed, and the error names the
// page, so the job retries.
func TestIngestDocsKeepsGoingPastAPageError(t *testing.T) {
	f := newFixture(t, dims)
	ctx := context.Background()
	if _, err := f.svc.IngestDocs(ctx); err != nil {
		t.Fatal(err)
	}
	f.bundle = agentknowledge.Bundle{Version: 1, Pages: []agentknowledge.DocPage{
		f.bundle.Pages[0], page("ai/models", "Models", "Models are picked per workspace now."), page("ai/new", "New", "A new page."),
	}}
	f.eng.failKey = "docs/ai/models"
	got, err := f.svc.IngestDocs(ctx)
	if err == nil || !strings.Contains(err.Error(), "docs/ai/models") {
		t.Fatalf("IngestDocs error %v, want one naming docs/ai/models", err)
	}
	if want := (agentknowledge.DocsResult{Indexed: 1, Unchanged: 2, Removed: 1}); got != want {
		t.Fatalf("result %+v, want %+v", got, want)
	}
	if len(f.docs(t, "docs/ai/new")) != 1 || len(f.docs(t, "docs/agents/old")) != 0 {
		t.Fatal("the pages after the failing one weren't indexed, or the dropped page wasn't removed")
	}
	if len(f.docs(t, "docs/ai/models")) != 1 {
		t.Fatal("the failing page's old version is gone")
	}
}
