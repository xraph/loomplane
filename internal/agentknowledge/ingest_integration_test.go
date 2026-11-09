//go:build integration

package agentknowledge_test

import (
	"context"
	"sync"
	"testing"

	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/weave"
	"github.com/xraph/weave/chunker"
	"github.com/xraph/weave/document"
	weaveengine "github.com/xraph/weave/engine"
	weavepg "github.com/xraph/weave/store/postgres"
	"github.com/xraph/weave/vectorstore/pgvector"

	"github.com/xraph/loomplane/internal/agentknowledge"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/pglock"
)

// replica is one Loomplane's knowledge over a shared database: its own
// weave engine on weave's Postgres store and pgvector, the advisory lock,
// and emb.
func replica(t *testing.T, db *grove.DB, emb *countingEmbedder, bundle func() (agentknowledge.Bundle, error)) (*agentknowledge.Service, *weaveengine.Engine) {
	t.Helper()
	eng, err := weaveengine.New(
		weaveengine.WithStore(weavepg.New(db)),
		weaveengine.WithVectorStore(pgvector.New(db, pgvector.WithDimensions(dims))),
		weaveengine.WithEmbedder(emb),
		weaveengine.WithChunker(chunker.NewRecursiveChunker()),
	)
	if err != nil {
		t.Fatal(err)
	}
	svc := agentknowledge.NewService(agentknowledge.Deps{
		Engine:   func() agentknowledge.Engine { return eng },
		Settings: &fakeSettings{},
		Binder:   &agentscope.Binder{Tenants: tenants{}},
		System:   func(context.Context) (string, error) { return systemTenant, nil },
		Config:   config.AIKnowledgeConfig{EmbeddingProvider: config.AIProviderOpenAI, EmbeddingModel: "text-embedding-3-small", Dimensions: dims},
		Lock:     pglock.For(pgdriver.Unwrap(db)),
		Docs:     bundle,
		Schema:   []byte(`{"type": "object"}`),
	})
	svc.SetStatus(agentsettings.KnowledgeStatus{Configured: true, Available: true})
	return svc, eng
}

// Two replicas that index the docs together on one database embed each
// page once; a later run embeds nothing, and a page the bundle dropped is
// gone from the docs collection.
func TestIngestDocsOnPgvector(t *testing.T) {
	db := openGrove(t, agenttest.FreshPgvectorDatabase(t))
	ctx := context.Background()
	if err := pgvector.Migrate(ctx, db, dims); err != nil {
		t.Fatal(err)
	}
	emb := &countingEmbedder{dims: dims, out: dims}
	var mu sync.Mutex
	bundle := threePages()
	docs := func() (agentknowledge.Bundle, error) {
		mu.Lock()
		defer mu.Unlock()
		return bundle, nil
	}
	a, eng := replica(t, db, emb, docs)
	b, _ := replica(t, db, emb, docs)

	results := make([]agentknowledge.DocsResult, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, svc := range []*agentknowledge.Service{a, b} {
		wg.Go(func() { results[i], errs[i] = svc.IngestDocs(ctx) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("replica %d: %v", i, err)
		}
	}
	if indexed := results[0].Indexed + results[1].Indexed; indexed != 4 {
		t.Fatalf("results %+v: %d indexed, want 4 between them", results, indexed)
	}
	// One Embed call per document: every chunk of a page goes in one call.
	if n := emb.count(); n != 4 {
		t.Fatalf("%d embed calls for 4 documents", n)
	}

	mu.Lock()
	bundle = agentknowledge.Bundle{Version: 1, Digest: "two", Pages: []agentknowledge.DocPage{bundle.Pages[0], bundle.Pages[2]}}
	mu.Unlock()
	got, err := a.IngestDocs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (agentknowledge.DocsResult{Unchanged: 3, Removed: 1}); got != want {
		t.Fatalf("after dropping a page %+v, want %+v", got, want)
	}
	if n := emb.count(); n != 4 {
		t.Fatalf("a run with nothing new embedded %d more times", n-4)
	}
	dctx := weave.WithTenant(ctx, agentknowledge.DocsTenant)
	col, err := eng.GetCollectionByName(dctx, agentknowledge.DocsCollection)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := eng.ListDocuments(dctx, &document.ListFilter{CollectionID: col.ID, SourceKey: "docs/agents/old"})
	if err != nil || len(gone) != 0 {
		t.Fatalf("the dropped page: %d documents, %v", len(gone), err)
	}
}

// A workspace's report is in its own tenant on Postgres too: another
// workspace lists nothing and removes nothing of it.
func TestWorkspaceKnowledgeOnPgvector(t *testing.T) {
	db := openGrove(t, agenttest.FreshPgvectorDatabase(t))
	ctx := context.Background()
	if err := pgvector.Migrate(ctx, db, dims); err != nil {
		t.Fatal(err)
	}
	svc, eng := replica(t, db, &countingEmbedder{dims: dims, out: dims}, func() (agentknowledge.Bundle, error) { return threePages(), nil })
	if out, err := svc.Ingest(ctx, "aorg_1", report("Only aorg_1 may read this.")); err != nil || out != agentknowledge.OutcomeIndexed {
		t.Fatalf("Ingest = %q, %v", out, err)
	}
	if err := svc.Remove(ctx, "aorg_2", "report/r1"); err != nil {
		t.Fatal(err)
	}
	if docs, err := eng.ListDocuments(weave.WithTenant(ctx, "aorg_2"), &document.ListFilter{}); err != nil || len(docs) != 0 {
		t.Fatalf("aorg_2 lists %d documents, %v", len(docs), err)
	}
	if docs, err := eng.ListDocuments(weave.WithTenant(ctx, "aorg_1"), &document.ListFilter{SourceKey: "report/r1"}); err != nil || len(docs) != 1 {
		t.Fatalf("aorg_1 lists %d documents for report/r1, %v", len(docs), err)
	}
	if err := svc.Remove(ctx, "aorg_1", "report/r1"); err != nil {
		t.Fatal(err)
	}
	if docs, err := eng.ListDocuments(weave.WithTenant(ctx, "aorg_1"), &document.ListFilter{SourceKey: "report/r1"}); err != nil || len(docs) != 0 {
		t.Fatalf("after Remove aorg_1 lists %d documents, %v", len(docs), err)
	}
}
