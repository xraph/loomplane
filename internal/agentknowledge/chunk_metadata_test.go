package agentknowledge_test

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/knowledge"
	cortexweave "github.com/xraph/cortex/knowledge/weave"
	"github.com/xraph/weave"
	"github.com/xraph/weave/chunker"
	"github.com/xraph/weave/collection"
	weaveengine "github.com/xraph/weave/engine"
	memstore "github.com/xraph/weave/store/memory"
	vecmemory "github.com/xraph/weave/vectorstore/memory"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/agentknowledge"
	"github.com/xraph/loomplane/internal/agentreports"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/security"
)

// searchAs runs a knowledge search in accessOrg the way cortex's
// knowledge_search does, as principal p (none when nil), and returns the
// chunks Readable lets through, with every chunk the search found.
func searchAs(t *testing.T, eng *weaveengine.Engine, authz agentknowledge.ProjectAuthorizer, p any) (readable, found []knowledge.ScoredChunk) {
	t.Helper()
	ctx := cortex.WithScope(context.Background(), agentscope.Scope{OrgID: accessOrg}.Cortex())
	if p != nil {
		ctx = cortex.WithPrincipal(ctx, p)
	}
	search := cortexweave.New(eng, cortexweave.WithShared(cortexweave.SharedCollection{
		Tenant: agentknowledge.DocsTenant, Collection: agentknowledge.DocsCollection,
	}))
	found, err := search.Retrieve(ctx, "Why did the deployment fail?", &knowledge.RetrieveParams{TopK: 50})
	if err != nil {
		t.Fatal(err)
	}
	readable, err = agentknowledge.Readable(ctx, authz, found)
	if err != nil {
		t.Fatal(err)
	}
	return readable, found
}

// sourceKeys are the distinct source keys of chunks in accessOrg's own
// collection, sorted.
func sourceKeys(chunks []knowledge.ScoredChunk) []string {
	var out []string
	for _, c := range chunks {
		if c.Metadata["tenant_id"] != accessOrg {
			continue
		}
		if k := c.Metadata["source_key"]; !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// Every chunk a search finds in a workspace's knowledge carries its
// document's kind and project, which is all Readable decides on: a
// report through the observer and a runbook through Ingest alike. A
// person who reads deployments on p1 alone gets p1's report and runbook,
// never p2's.
func TestIngestedChunksCarryKindAndProject(t *testing.T) {
	f := newFixture(t, dims)
	ctx := context.Background()
	p1, p2 := typeid.MustGenerate("proj").String(), typeid.MustGenerate("proj").String()
	o := agentknowledge.ReportObserver{Knowledge: f.svc}
	for _, doc := range []agentreports.KnowledgeDocument{
		projectReport("arep_p1", p1, "Project one's deployment failed: the image wasn't found."),
		projectReport("arep_p2", p2, "Project two's deployment failed: its database password expired."),
	} {
		if err := o.ReportChanged(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	for _, rb := range []struct{ key, project, text string }{
		{"runbook/p1", p1, "When project one's deployment fails, check the image tag first."},
		{"runbook/p2", p2, "When project two's deployment fails, rotate the database password."},
	} {
		out, err := f.svc.Ingest(ctx, accessOrg, agentknowledge.Document{
			SourceKey: rb.key, Kind: agentknowledge.KindRunbook, Title: rb.key, Content: rb.text, ProjectID: rb.project,
		})
		if err != nil || out != agentknowledge.OutcomeIndexed {
			t.Fatalf("Ingest %s: %s, %v", rb.key, out, err)
		}
	}
	want := map[string][2]string{
		"report/arep_p1": {agentknowledge.KindReport, p1}, "report/arep_p2": {agentknowledge.KindReport, p2},
		"runbook/p1": {agentknowledge.KindRunbook, p1}, "runbook/p2": {agentknowledge.KindRunbook, p2},
	}

	readable, found := searchAs(t, f.weave, projectGrantAuthz(t, p1),
		security.AgentPrincipal(accessOrg, &security.Principal{Kind: security.KindUser, UserID: onP1, OrgID: accessOrg}))
	if got := sourceKeys(found); !slices.Equal(got, slices.Sorted(maps.Keys(want))) {
		t.Fatalf("the search found %v, want every document", got)
	}
	for _, c := range found {
		w, ok := want[c.Metadata["source_key"]]
		if !ok {
			continue
		}
		if c.Metadata["kind"] != w[0] || c.Metadata["project_id"] != w[1] {
			t.Fatalf("chunk of %s has kind %q and project %q, want %q and %q",
				c.Metadata["source_key"], c.Metadata["kind"], c.Metadata["project_id"], w[0], w[1])
		}
	}
	if got := sourceKeys(readable); !slices.Equal(got, []string{"report/arep_p1", "runbook/p1"}) {
		t.Fatalf("p1's member read %v, want p1's report and runbook alone", got)
	}
}

// legacyChunker is the chunker Loomplane ran before weave merged a
// document's metadata into its chunks: it wrote the document's kind and
// project on every chunk it made.
type legacyChunker struct {
	inner chunker.Chunker
	md    map[string]string
}

func (c *legacyChunker) Chunk(ctx context.Context, text string, opts *chunker.Options) ([]chunker.ChunkResult, error) {
	out, err := c.inner.Chunk(ctx, text, opts)
	if err != nil {
		return nil, err
	}
	for i := range out {
		md := maps.Clone(out[i].Metadata)
		if md == nil {
			md = map[string]string{}
		}
		maps.Copy(md, c.md)
		out[i].Metadata = md
	}
	return out, nil
}

// Chunks indexed before the upgrade carry kind and project only because
// the old chunker stamped them. Searched with today's engine over the
// same stores, they still keep to their project: p1's member reads p1's
// alone, the agent reads both, and a run with no principal neither.
func TestChunksStampedBeforeTheUpgradeStillFilter(t *testing.T) {
	ctx := weave.WithTenant(context.Background(), accessOrg)
	p1, p2 := typeid.MustGenerate("proj").String(), typeid.MustGenerate("proj").String()
	store, vectors := memstore.New(), vecmemory.New()
	emb := &countingEmbedder{dims: dims, out: dims}
	stamp := &legacyChunker{inner: chunker.NewRecursiveChunker()}
	legacy, err := weaveengine.New(weaveengine.WithStore(store), weaveengine.WithVectorStore(vectors),
		weaveengine.WithEmbedder(emb), weaveengine.WithChunker(stamp))
	if err != nil {
		t.Fatal(err)
	}
	col := &collection.Collection{
		Name: agentknowledge.WorkspaceCollection, TenantID: accessOrg, AppID: agentknowledge.App,
		EmbeddingModel: "text-embedding-3-small", EmbeddingDims: dims,
	}
	if err = legacy.CreateCollection(ctx, col); err != nil {
		t.Fatal(err)
	}
	// The source keys aren't a report's, and the documents' own metadata
	// names no kind or project, so only the stamped chunk metadata can
	// keep p2's chunk from p1's member.
	for _, d := range []struct{ key, project, text string }{
		{"legacy/p1", p1, "Project one's deployment failed: the image wasn't found."},
		{"legacy/p2", p2, "Project two's deployment failed: its database password expired."},
	} {
		stamp.md = map[string]string{"kind": agentknowledge.KindReport, "project_id": d.project}
		if _, err = legacy.Ingest(ctx, &weaveengine.IngestInput{
			CollectionID: col.ID, SourceKey: d.key, Title: d.key, Content: d.text,
			Metadata: map[string]string{"title": d.key},
		}); err != nil {
			t.Fatal(err)
		}
	}

	current, err := weaveengine.New(weaveengine.WithStore(store), weaveengine.WithVectorStore(vectors),
		weaveengine.WithEmbedder(emb), weaveengine.WithChunker(chunker.NewRecursiveChunker()))
	if err != nil {
		t.Fatal(err)
	}
	authz := projectGrantAuthz(t, p1)
	member := security.Principal{Kind: security.KindUser, UserID: onP1, OrgID: accessOrg}
	readable, found := searchAs(t, current, authz, security.AgentPrincipal(accessOrg, &member))
	if got := sourceKeys(found); !slices.Equal(got, []string{"legacy/p1", "legacy/p2"}) {
		t.Fatalf("the search found %v, want both legacy documents", got)
	}
	if got := sourceKeys(readable); !slices.Equal(got, []string{"legacy/p1"}) {
		t.Fatalf("p1's member read %v, want p1's legacy chunk alone", got)
	}
	if readable, _ = searchAs(t, current, authz, security.AgentPrincipal(accessOrg, nil)); !slices.Equal(sourceKeys(readable), []string{"legacy/p1", "legacy/p2"}) {
		t.Fatalf("the agent alone read %v, want both", sourceKeys(readable))
	}
	if readable, _ = searchAs(t, current, authz, nil); len(sourceKeys(readable)) != 0 {
		t.Fatalf("a run with no principal read %v", sourceKeys(readable))
	}
}
