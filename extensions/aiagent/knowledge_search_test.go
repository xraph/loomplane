package aiagent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/cortex"
	cortexengine "github.com/xraph/cortex/engine"
	"github.com/xraph/cortex/knowledge"
	cortexweave "github.com/xraph/cortex/knowledge/weave"
	"github.com/xraph/weave/chunker"
	"github.com/xraph/weave/embedder"
	weaveengine "github.com/xraph/weave/engine"
	weavememory "github.com/xraph/weave/store/memory"
	vectormemory "github.com/xraph/weave/vectorstore/memory"

	"github.com/xraph/loomplane/internal/agentknowledge"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/security"
)

// failingKnowledge fails every call the way a weave adapter does when
// its database can't be reached.
type failingKnowledge struct{ fail bool }

var errUnreachable = errors.New(`weave: retrieve: dial tcp secret-host:5432: connection refused`)

func (f failingKnowledge) Retrieve(context.Context, string, *knowledge.RetrieveParams) ([]knowledge.ScoredChunk, error) {
	if f.fail {
		return nil, errUnreachable
	}
	return []knowledge.ScoredChunk{{Content: "docs"}}, nil
}

func (f failingKnowledge) ListCollections(context.Context) ([]knowledge.CollectionInfo, error) {
	if f.fail {
		return nil, errUnreachable
	}
	return []knowledge.CollectionInfo{{Name: "loomplane-docs"}}, nil
}

// A failed knowledge search reaches the model as a short fixed message,
// never the library's error, which the log keeps.
func TestKnowledgeSearchErrorsAreQuiet(t *testing.T) {
	var logged []error
	warn := func(_ string, err error) { logged = append(logged, err) }
	eng, err := cortexengine.New(cortexengine.WithKnowledge(failingKnowledge{fail: true}), quietKnowledgeOption(warn, nil))
	if err != nil {
		t.Fatal(err)
	}
	_, err = eng.Knowledge().Retrieve(t.Context(), "canary thresholds", nil)
	if !errors.Is(err, errSearchFailed) || strings.Contains(err.Error(), "secret-host") || strings.Contains(err.Error(), "weave") {
		t.Fatalf("Retrieve: %v", err)
	}
	if _, err = eng.Knowledge().ListCollections(t.Context()); !errors.Is(err, errSearchFailed) {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(logged) != 2 || !errors.Is(logged[0], errUnreachable) {
		t.Fatalf("logged %v", logged)
	}

	// A search that works is passed through.
	eng, err = cortexengine.New(cortexengine.WithKnowledge(failingKnowledge{}), quietKnowledgeOption(warn, nil))
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := eng.Knowledge().Retrieve(t.Context(), "q", nil)
	if err != nil || len(chunks) != 1 || chunks[0].Content != "docs" {
		t.Fatalf("Retrieve: %v %v", chunks, err)
	}
	// No knowledge, nothing to wrap.
	if eng, err = cortexengine.New(quietKnowledgeOption(warn, nil)); err != nil || eng.Knowledge() != nil {
		t.Fatalf("without knowledge: %v, %v", eng.Knowledge(), err)
	}
}

// zeroEmbedder embeds every text as the same small vector.
type zeroEmbedder struct{}

func (zeroEmbedder) Embed(_ context.Context, texts []string) ([]embedder.EmbedResult, error) {
	out := make([]embedder.EmbedResult, len(texts))
	for i := range texts {
		out[i] = embedder.EmbedResult{Vector: []float32{1, 0, 0, 0}, TokenCount: 1}
	}
	return out, nil
}

func (zeroEmbedder) Dimensions() int { return 4 }

// A search before the docs job has made the docs collection, or before
// the workspace has a collection of its own, finds nothing and fails
// nothing: cortex's weave adapter, as the agent configures it, returns no
// results, so the model reads an empty search and the log stays quiet.
// Naming the docs collection, or listing collections, is the same.
func TestKnowledgeSearchBeforeAnyDocsFindsNothing(t *testing.T) {
	var logged []error
	warn := func(_ string, err error) { logged = append(logged, err) }
	w, err := weaveengine.New(weaveengine.WithStore(weavememory.New()), weaveengine.WithVectorStore(vectormemory.New()),
		weaveengine.WithEmbedder(zeroEmbedder{}), weaveengine.WithChunker(chunker.NewRecursiveChunker()))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := cortexengine.New(cortexengine.WithKnowledge(cortexweave.New(w, knowledgeOptions()...)), quietKnowledgeOption(warn, nil))
	if err != nil {
		t.Fatal(err)
	}
	ctx := cortex.WithScope(t.Context(), agentscope.Scope{OrgID: "aorg_1"}.Cortex())
	for _, params := range []*knowledge.RetrieveParams{nil, {Collection: agentknowledge.DocsCollection}, {Collection: agentknowledge.WorkspaceCollection}} {
		chunks, rerr := eng.Knowledge().Retrieve(ctx, "canary thresholds", params)
		if rerr != nil || len(chunks) != 0 {
			t.Fatalf("Retrieve(%+v) = %v, %v; want no results and no error", params, chunks, rerr)
		}
	}
	cols, err := eng.Knowledge().ListCollections(ctx)
	if err != nil || len(cols) != 0 {
		t.Fatalf("ListCollections = %v, %v", cols, err)
	}
	if len(logged) != 0 {
		t.Fatalf("logged %v", logged)
	}
}

// pastReports finds a report of each of two projects beside a docs page,
// as weave hands them back.
type pastReports struct{}

func (pastReports) Retrieve(context.Context, string, *knowledge.RetrieveParams) ([]knowledge.ScoredChunk, error) {
	return []knowledge.ScoredChunk{
		{Content: "p1's report", Metadata: map[string]string{"kind": "report", "project_id": "proj_1", "source_key": "report/arep_1"}},
		{Content: "p2's report", Metadata: map[string]string{"kind": "report", "project_id": "proj_2", "source_key": "report/arep_2"}},
		{Content: "docs", Metadata: map[string]string{"source_key": "docs/index"}},
	}, nil
}

func (pastReports) ListCollections(context.Context) ([]knowledge.CollectionInfo, error) {
	return nil, nil
}

// readsOn allows deployment:read on one project.
type readsOn string

func (r readsOn) AllowedOn(_ context.Context, _ security.Principal, perm security.Permission, projectID string) (bool, error) {
	return perm == security.DeploymentRead && projectID == string(r), nil
}

// A knowledge search through cortex finds a past report only for a run
// whose principal reads deployments on its project, and none at all
// before the authorizer is bound.
func TestKnowledgeSearchFindsOnlyReadableReports(t *testing.T) {
	warn := func(string, error) {}
	person := security.Principal{Kind: security.KindUser, UserID: "usr_1", OrgID: "aorg_1"}
	ctx := cortex.WithPrincipal(cortex.WithScope(t.Context(), agentscope.Scope{OrgID: "aorg_1"}.Cortex()),
		security.AgentPrincipal("aorg_1", &person))
	found := func(authz func() agentknowledge.ProjectAuthorizer) []string {
		t.Helper()
		eng, err := cortexengine.New(cortexengine.WithKnowledge(pastReports{}), quietKnowledgeOption(warn, authz))
		if err != nil {
			t.Fatal(err)
		}
		chunks, err := eng.Knowledge().Retrieve(ctx, "why did it fail", nil)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(chunks))
		for _, c := range chunks {
			out = append(out, c.Content)
		}
		return out
	}
	if got := found(func() agentknowledge.ProjectAuthorizer { return readsOn("proj_1") }); !slices.Equal(got, []string{"p1's report", "docs"}) {
		t.Fatalf("a reader of proj_1 found %v", got)
	}
	if got := found(func() agentknowledge.ProjectAuthorizer { return nil }); !slices.Equal(got, []string{"docs"}) {
		t.Fatalf("before the authorizer is bound the search found %v", got)
	}
	if got := (&Extension{}).knowledgeAuthz(); got != nil {
		t.Fatalf("an extension with no authorizer gives %v", got)
	}
}
