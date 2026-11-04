//go:build integration

package agentknowledge_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/knowledge"
	cortexweave "github.com/xraph/cortex/knowledge/weave"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/weave"
	"github.com/xraph/weave/chunker"
	"github.com/xraph/weave/document"
	"github.com/xraph/weave/embedder"
	weaveengine "github.com/xraph/weave/engine"
	weavepg "github.com/xraph/weave/store/postgres"
	"github.com/xraph/weave/vectorstore/pgvector"

	"github.com/xraph/loomplane/internal/agentknowledge"
	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/pglock"
)

// checkoutTopic is what topicEmbedder recognises.
const checkoutTopic = "checkout"

// topicEmbedder embeds a text about the checkout along the first axis and
// everything else along the second, a little of each, so a search about
// the checkout ranks what's about it first. Real embeddings do this by
// meaning; the test only needs the order.
type topicEmbedder struct{}

func (topicEmbedder) Embed(_ context.Context, texts []string) ([]embedder.EmbedResult, error) {
	out := make([]embedder.EmbedResult, len(texts))
	for i, text := range texts {
		v := make([]float32, dims)
		if strings.Contains(strings.ToLower(text), checkoutTopic) {
			v[0], v[1] = 1, 0.1
		} else {
			v[0], v[1] = 0.1, 1
		}
		out[i] = embedder.EmbedResult{Vector: v, TokenCount: 1}
	}
	return out, nil
}

func (topicEmbedder) Dimensions() int { return dims }

// A report that resolves with findings reaches its workspace's knowledge
// through the observer, on weave's Postgres store and pgvector, with the
// docs beside it as the agent has them: a knowledge search in that
// workspace, the way cortex's knowledge_search makes it, returns the
// report with its link; another workspace's returns none of it.
func TestResolvedReportIsSearchable(t *testing.T) {
	const o1, o2 = "aorg_k1", "aorg_k2"
	db := openGrove(t, agenttest.FreshPgvectorDatabase(t))
	ctx := context.Background()
	if err := pgvector.Migrate(ctx, db, dims); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.Unwrap(db)
	eng, err := weaveengine.New(
		weaveengine.WithStore(weavepg.New(db)),
		weaveengine.WithVectorStore(pgvector.New(db, pgvector.WithDimensions(dims))),
		weaveengine.WithEmbedder(topicEmbedder{}),
		weaveengine.WithChunker(chunker.NewRecursiveChunker()),
	)
	if err != nil {
		t.Fatal(err)
	}
	binder := &agentscope.Binder{Tenants: tenants{}}
	know := agentknowledge.NewService(agentknowledge.Deps{
		Engine:   func() agentknowledge.Engine { return eng },
		Settings: &fakeSettings{},
		Binder:   binder,
		System:   func(context.Context) (string, error) { return systemTenant, nil },
		Config:   config.AIKnowledgeConfig{EmbeddingProvider: config.AIProviderOpenAI, EmbeddingModel: "text-embedding-3-small", Dimensions: dims},
		Lock:     pglock.For(pg),
		Docs:     func() (agentknowledge.Bundle, error) { return threePages(), nil },
		Schema:   []byte(`{"type": "object"}`),
	})
	know.SetStatus(agentsettings.KnowledgeStatus{Configured: true, Available: true})
	if _, err = know.IngestDocs(ctx); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var warnings []string
	reports := agentreports.NewService(agentreports.Deps{
		Store:     agentreports.NewPgStore(pg),
		Safety:    &agentsafety.Scanner{Engine: agenttest.Shield(t, o1)},
		Binder:    binder,
		PublicURL: "https://loomplane.example",
		Warn: func(msg string, err error) {
			mu.Lock()
			defer mu.Unlock()
			warnings = append(warnings, msg+": "+err.Error())
		},
	})
	reports.AddObserver(agentknowledge.ReportObserver{Knowledge: know})

	obs, err := reports.Observe(ctx, agentreports.Signal{
		OrgID: o1, ProjectID: "proj_1", EnvironmentID: "env_prod", Environment: "production",
		Kind: agentreports.SignalDeploymentFailed, Component: "service:checkout", Severity: agentreports.SeverityHigh,
		Title:      "The checkout deployment to production failed",
		Facts:      []agentreports.Fact{{Label: "Deployment", Value: "dep_1", Path: "/deployments/dep_1"}},
		Evidence:   agentreports.Evidence{DeploymentID: "dep_1"},
		Repository: "acme/checkout", Occurrence: "dep_1#0",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := obs.Report.ID
	if ok, ierr := reports.SetInvestigation(ctx, o1, id, []string{agentreports.InvestigationNone},
		agentreports.Investigation{Status: agentreports.InvestigationQueued, TaskID: "atask_1", RunID: "arun_1"}); !ok || ierr != nil {
		t.Fatalf("queue the investigation: %v %v", ok, ierr)
	}
	if _, err = reports.WriteFindings(ctx, o1, id, agentreports.Writer{TaskID: "atask_1"}, agentreports.FindingsInput{
		Summary: "The checkout image tag was never pushed.", LikelyCause: "The build step skipped the push.",
		Suggestions: []string{"Push the image before deploying."}, Confidence: "high",
	}); err != nil {
		t.Fatal(err)
	}
	sourceKey := agentknowledge.ReportSourcePrefix + id
	if docs := listed(t, eng, o1, sourceKey); len(docs) != 0 {
		t.Fatalf("an open report is in knowledge before it changed: %d documents", len(docs))
	}

	cleared, err := reports.Clear(ctx, agentreports.Clearance{
		OrgID: o1, ProjectID: "proj_1", EnvironmentID: "env_prod",
		Signals: []string{agentreports.SignalDeploymentFailed}, Reason: "A later deployment (dep_2) succeeded",
	})
	if err != nil || len(cleared) != 1 {
		t.Fatalf("Clear = %d reports, %v", len(cleared), err)
	}
	mu.Lock()
	if len(warnings) != 0 {
		t.Fatalf("warnings %v", warnings)
	}
	mu.Unlock()
	docs := listed(t, eng, o1, sourceKey)
	if len(docs) != 1 || docs[0].State != document.StateReady {
		t.Fatalf("%d documents for %s after the report resolved", len(docs), sourceKey)
	}
	if md := docs[0].Metadata; md["trust"] != "untrusted" || md["url"] != "/agent/reports/"+id || md["status"] != agentreports.StatusResolved || md["report_id"] != id {
		t.Fatalf("metadata %v", md)
	}

	search := cortexweave.New(eng, cortexweave.WithShared(cortexweave.SharedCollection{
		Tenant: agentknowledge.DocsTenant, Collection: agentknowledge.DocsCollection,
	}))
	retrieve := func(orgID string) []knowledge.ScoredChunk {
		t.Helper()
		sctx := cortex.WithScope(ctx, agentscope.Scope{OrgID: orgID}.Cortex())
		chunks, rerr := search.Retrieve(sctx, "Why did the checkout deployment fail?", &knowledge.RetrieveParams{TopK: 5})
		if rerr != nil {
			t.Fatalf("%s's search: %v", orgID, rerr)
		}
		return chunks
	}
	got := retrieve(o1)
	if len(got) == 0 || got[0].Metadata["source_key"] != sourceKey || got[0].Metadata["tenant_id"] != o1 {
		t.Fatalf("o1's search: %+v", got)
	}
	if text := got[0].Content; !strings.Contains(text, "never pushed") || !strings.Contains(text, "/agent/reports/"+id) {
		t.Fatalf("o1's top result %q has no findings or link", text)
	}
	docsSeen := false
	for _, c := range retrieve(o2) {
		if c.Metadata["tenant_id"] != agentknowledge.DocsTenant || strings.Contains(c.Content, "never pushed") {
			t.Fatalf("o2's search returned %+v", c)
		}
		docsSeen = true
	}
	if !docsSeen {
		t.Fatal("o2's search found not even the docs")
	}
}

// listed is orgID's documents of sourceKey in its workspace collection.
func listed(t *testing.T, eng *weaveengine.Engine, orgID, sourceKey string) []*document.Document {
	t.Helper()
	docs, err := eng.ListDocuments(weave.WithTenant(context.Background(), orgID), &document.ListFilter{SourceKey: sourceKey})
	if err != nil {
		t.Fatal(err)
	}
	return docs
}
