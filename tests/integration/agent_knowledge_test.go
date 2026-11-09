//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/cortex"
	cortexengine "github.com/xraph/cortex/engine"
	"github.com/xraph/weave/vectorstore/pgvector"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/agentknowledge"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/ops"
)

// knowledgeAI is the fake agent's AI settings with knowledge configured:
// OpenAI's priced embedding model, at dimensions, on a fake embedding
// server. Boot itself embeds nothing; the docs job that follows it does.
func knowledgeAI(t *testing.T, fake *agenttest.FakeAnthropic, dimensions int) config.AIConfig {
	t.Helper()
	embeddings := newEmbeddingServer(t, dimensions)
	ai := fake.AIConfig()
	ai.Knowledge.EmbeddingProvider = config.AIProviderOpenAI
	ai.Knowledge.EmbeddingAPIKey = "test-embedding-key"
	ai.Knowledge.EmbeddingBaseURL = embeddings.URL
	ai.Knowledge.EmbeddingModel = "text-embedding-3-small"
	ai.Knowledge.Dimensions = dimensions
	return ai
}

// knowledgeStatus is the knowledge_status on the agent's settings view.
type knowledgeStatus struct {
	Configured bool   `json:"configured"`
	Available  bool   `json:"available"`
	Reason     string `json:"reason"`
}

// knowledgeOrg starts the app on dsn with ai. With orgID "" it makes Ada
// the owner of a new workspace, on a database no Loomplane has run on;
// otherwise, after a reboot, it signs her in again to orgID.
func knowledgeOrg(t *testing.T, dsn string, ai config.AIConfig, orgID string) *agentOrg {
	t.Helper()
	cfg := config.Defaults()
	cfg.AI = ai
	cfg.Database.URL = dsn
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Dashboard.Dir = t.TempDir()
	r := start(t, cfg)
	a := &agentOrg{r: r, cfg: cfg, owner: &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}, orgID: orgID}
	if orgID == "" {
		account(t, a.owner, "ada@example.com")
		var org struct {
			ID string `json:"id"`
		}
		a.owner.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Engines", "slug": "engines"}, &org)
		a.orgID = org.ID
	} else {
		var signin struct {
			SessionToken string `json:"session_token"`
		}
		a.owner.must(http.StatusOK, http.MethodPost, "/authsome/v1/signin", map[string]string{"email": "ada@example.com", "password": testPassword}, &signin)
		a.owner.token = signin.SessionToken
	}
	a.owner.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": a.orgID}, nil)
	return a
}

// knowledge is the server's knowledge status, as an admin reads it, and
// checks the agent's status answers beside it.
func (a *agentOrg) knowledge(t *testing.T) knowledgeStatus {
	t.Helper()
	var v struct {
		KnowledgeStatus knowledgeStatus `json:"knowledge_status"`
	}
	a.owner.must(http.StatusOK, http.MethodGet, "/api/v1/ai/settings/agent", nil, &v)
	a.owner.must(http.StatusOK, http.MethodGet, "/api/v1/agent/status", nil, nil)
	return v.KnowledgeStatus
}

// dbValue runs a query that returns one value on dsn.
func dbValue[T any](t *testing.T, dsn, query string) T {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var v T
	if err = conn.QueryRow(ctx, query).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v
}

const (
	pgvectorRows      = `SELECT count(*) FROM grove_migrations WHERE "group" = '` + pgvector.GroupName + `'`
	embeddingColumn   = `SELECT coalesce((SELECT format_type(atttypid, atttypmod) FROM pg_attribute WHERE attrelid = to_regclass('weave_vectors') AND attname = 'embedding' AND NOT attisdropped), '')`
	vectorAvailableOn = `SELECT count(*) FROM pg_available_extensions WHERE name = 'vector'`
)

// On the bundled Postgres, which has no pgvector, a server with knowledge
// configured boots as it always has: knowledge says why it's off, nothing
// of weave's pgvector group is applied, and the agent answers.
func TestKnowledgeOffOnPlainPostgres(t *testing.T) {
	dsn := freshDatabase(t)
	if dbValue[int64](t, dsn, vectorAvailableOn) != 0 {
		t.Skip("LOOMPLANE_TEST_DATABASE_URL has pgvector; this test needs a Postgres without it")
	}
	a := knowledgeOrg(t, dsn, knowledgeAI(t, agenttest.Anthropic(t), 1536), "")
	defer a.r.stop()

	st := a.knowledge(t)
	if !st.Configured || st.Available || !strings.Contains(st.Reason, "pgvector") {
		t.Fatalf("knowledge_status %+v", st)
	}
	if n := dbValue[int64](t, dsn, pgvectorRows); n != 0 {
		t.Fatalf("%d weave_pgvector rows on a Postgres without pgvector", n)
	}
}

// With pgvector, knowledge comes on and makes its vector table at the
// configured size, once: a reboot finds it, records nothing more, and the
// schema guard still knows the database.
func TestKnowledgeOnWithPgvector(t *testing.T) {
	dsn := freshPgvectorDatabase(t)
	ai := knowledgeAI(t, agenttest.Anthropic(t), 1536)
	a := knowledgeOrg(t, dsn, ai, "")
	if st := a.knowledge(t); !st.Configured || !st.Available || st.Reason != "" {
		t.Fatalf("first boot: knowledge_status %+v", st)
	}
	if got := dbValue[string](t, dsn, embeddingColumn); got != "vector(1536)" {
		t.Fatalf("weave_vectors.embedding is %q, want vector(1536)", got)
	}
	a.r.stop()

	a = knowledgeOrg(t, dsn, ai, a.orgID)
	defer a.r.stop()
	if st := a.knowledge(t); !st.Configured || !st.Available {
		t.Fatalf("after a reboot: knowledge_status %+v", st)
	}
	if n := dbValue[int64](t, dsn, pgvectorRows); n != 1 {
		t.Fatalf("%d weave_pgvector rows, want 1", n)
	}
	if err := ops.CheckCompatibleWith(context.Background(), dsn, migrations.All(), migrations.OptionalVersions()); err != nil {
		t.Fatalf("the schema guard refuses a database with the pgvector group: %v", err)
	}
}

// Changing ai.knowledge.dimensions once the vector table exists turns
// knowledge off with a reason naming the key and both sizes. The server
// still boots, and a workspace that had knowledge on still chats: its
// model just isn't offered knowledge_search.
func TestKnowledgeDimensionMismatchAfterReboot(t *testing.T) {
	dsn := freshPgvectorDatabase(t)
	fake := agenttest.Anthropic(t, agenttest.Turn{Text: "Your projects are fine."})
	a := knowledgeOrg(t, dsn, knowledgeAI(t, fake, 1536), "")
	if st := a.knowledge(t); !st.Available {
		t.Fatalf("first boot: knowledge_status %+v", st)
	}
	knowledgeWorkspace(a, true)
	a.r.stop()

	a = knowledgeOrg(t, dsn, knowledgeAI(t, fake, 768), a.orgID)
	defer a.r.stop()
	st := a.knowledge(t)
	if !st.Configured || st.Available || !strings.Contains(st.Reason, "its vectors have 1536 dimensions, and ai.knowledge.dimensions is 768") ||
		!strings.Contains(st.Reason, "set ai.knowledge.dimensions to 1536") {
		t.Fatalf("after changing the size: knowledge_status %+v", st)
	}
	if got := dbValue[string](t, dsn, embeddingColumn); got != "vector(1536)" {
		t.Fatalf("the reboot changed weave_vectors.embedding to %q", got)
	}

	sent := converse(t, a.owner, "How are my projects?")
	waitTask(t, a.owner, sent.Task.ID, func(task agentruns.Task) bool { return agentruns.Terminal(task.Status) })
	if task := taskOf(t, a.owner, sent.Task.ID); task.Status != agentruns.StatusCompleted {
		t.Fatalf("the chat after a size change ended %s: %s", task.Status, task.Error)
	}
	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d model requests, want 1", len(reqs))
	}
	if tools := reqs[0].Tools; slices.Contains(tools, "knowledge_search") || !slices.Contains(tools, "list_projects") {
		t.Errorf("with knowledge off for a size change the model is offered %v", tools)
	}
}

// knowledgeReset is the manual reset the Knowledge docs give for changing
// ai.knowledge.dimensions, statement for statement.
var knowledgeReset = []string{
	`DROP TABLE weave_vectors;`,
	`DELETE FROM grove_migrations WHERE "group" = 'weave_pgvector';`,
	`DELETE FROM weave_collections WHERE name IN ('loomplane-docs', 'workspace');`,
}

// The reset the Knowledge docs give for a new size works: after a size
// change has turned knowledge off, the three statements and a restart
// bring knowledge back on at the new size, with the docs indexed again
// and the workspace's own documents gone.
func TestKnowledgeResetForANewSize(t *testing.T) {
	page := knowledgePage(t)
	for _, stmt := range knowledgeReset {
		if !strings.Contains(page, stmt) {
			t.Fatalf("the Knowledge docs no longer give %q; keep this test and the page in step", stmt)
		}
	}
	bundle, err := agentknowledge.EmbeddedDocs()
	if err != nil {
		t.Fatal(err)
	}
	documents := int64(len(bundle.Pages) + 1) // every page and the schema
	fake := agenttest.Anthropic(t)

	dsn := freshPgvectorDatabase(t)
	a := knowledgeOrg(t, dsn, docsAI(fake, newEmbeddingServer(t, 8), 8), "")
	waitForDocs(t, dsn, readyDocs, documents)
	knowledgeWorkspace(a, true)
	knowledge := inject[*agentknowledge.Service](t, a.container())
	if outcome, ierr := knowledge.Ingest(context.Background(), a.orgID, agentknowledge.Document{
		SourceKey: "report/arep_canary", Kind: agentknowledge.KindReport, Title: "Checkout canary",
		Content: canaryReport, URL: "/agent/reports/arep_canary",
	}); ierr != nil || outcome != agentknowledge.OutcomeIndexed {
		t.Fatalf("Ingest: %s, %v", outcome, ierr)
	}
	a.r.stop()

	// The new size alone turns knowledge off.
	a = knowledgeOrg(t, dsn, docsAI(fake, newEmbeddingServer(t, 4), 4), a.orgID)
	if st := a.knowledge(t); st.Available {
		t.Fatalf("after changing the size: knowledge_status %+v", st)
	}
	a.r.stop()

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, "BEGIN;\n"+strings.Join(knowledgeReset, "\n")+"\nCOMMIT;"); err != nil {
		t.Fatalf("the documented reset: %v", err)
	}
	_ = conn.Close(ctx)
	if n := dbValue[int64](t, dsn, `SELECT count(*) FROM weave_documents`); n != 0 {
		t.Fatalf("%d knowledge documents left after the reset", n)
	}

	emb := newEmbeddingServer(t, 4)
	a = knowledgeOrg(t, dsn, docsAI(fake, emb, 4), a.orgID)
	defer a.r.stop()
	if st := a.knowledge(t); !st.Configured || !st.Available || st.Reason != "" {
		t.Fatalf("after the reset: knowledge_status %+v", st)
	}
	if got := dbValue[string](t, dsn, embeddingColumn); got != "vector(4)" {
		t.Fatalf("after the reset weave_vectors.embedding is %q, want vector(4)", got)
	}
	if n := dbValue[int64](t, dsn, pgvectorRows); n != 1 {
		t.Fatalf("%d weave_pgvector rows after the reset, want 1", n)
	}
	waitForDocs(t, dsn, readyDocs, documents)
	if n := emb.requests.Load(); n < documents {
		t.Fatalf("the docs were indexed again with %d embedding requests for %d documents", n, documents)
	}
	if n := dbValue[int64](t, dsn, `SELECT count(*) FROM weave_vectors`); n == 0 {
		t.Fatal("no vectors at the new size")
	}
	if n := dbValue[int64](t, dsn, `SELECT count(*) FROM weave_documents WHERE tenant_id = '`+a.orgID+`'`); n != 0 {
		t.Fatalf("%d of the workspace's documents survived the reset", n)
	}
}

// knowledgePage is the Knowledge page's text, as the docs bundle has it.
func knowledgePage(t *testing.T) string {
	t.Helper()
	bundle, err := agentknowledge.EmbeddedDocs()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range bundle.Pages {
		if p.Path == "agents/knowledge" {
			return p.Text
		}
	}
	t.Fatal("no Knowledge page in the docs bundle")
	return ""
}

// embeddingServer is an OpenAI-compatible /v1/embeddings that counts its
// requests and answers each input with a vector of dimensions numbers.
type embeddingServer struct {
	*httptest.Server
	requests atomic.Int64
}

func newEmbeddingServer(t *testing.T, dimensions int) *embeddingServer {
	t.Helper()
	return newEmbeddingServerWith(t, func(text string) []float64 {
		v := make([]float64, dimensions)
		for j := range v {
			v[j] = float64((len(text)+j)%11+1) / 11
		}
		return v
	})
}

// newEmbeddingServerWith is an embedding server that answers each input
// with vector(input).
func newEmbeddingServerWith(t *testing.T, vector func(text string) []float64) *embeddingServer {
	t.Helper()
	s := &embeddingServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/embeddings") {
			t.Errorf("embedding server: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		s.requests.Add(1)
		var in struct {
			Model string          `json:"model"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var texts []string
		if err := json.Unmarshal(in.Input, &texts); err != nil {
			var one string
			if err = json.Unmarshal(in.Input, &one); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			texts = []string{one}
		}
		type item struct {
			Object    string    `json:"object"`
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		data := make([]item, len(texts))
		for i, text := range texts {
			data[i] = item{Object: "embedding", Index: i, Embedding: vector(text)}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list", "data": data, "model": in.Model,
			"usage": map[string]int{"prompt_tokens": len(texts), "total_tokens": len(texts)},
		})
	}))
	t.Cleanup(s.Close)
	return s
}

// docsAI is AI with knowledge on an OpenAI-compatible embedding model at
// dimensions, served by emb and priced in ai.prices.
func docsAI(fake *agenttest.FakeAnthropic, emb *embeddingServer, dimensions int) config.AIConfig {
	ai := fake.AIConfig()
	ai.Knowledge.EmbeddingProvider = config.AIProviderOpenAICompatible
	ai.Knowledge.EmbeddingBaseURL = emb.URL + "/v1"
	ai.Knowledge.EmbeddingModel = "test-embed"
	ai.Knowledge.Dimensions = dimensions
	ai.Prices = append(ai.Prices, config.AIPrice{Provider: config.AIProviderOpenAICompatible, Model: "test-embed", InputPerMillion: "0.02", OutputPerMillion: "0"})
	return ai
}

// startDocs starts the app with ai on dsn.
func startDocs(t *testing.T, dsn string, ai config.AIConfig) *running {
	t.Helper()
	cfg := config.Defaults()
	cfg.AI = ai
	cfg.Database.URL = dsn
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Dashboard.Dir = t.TempDir()
	return start(t, cfg)
}

const (
	readyDocs     = `SELECT count(*) FROM weave_documents WHERE tenant_id = 'loomplane-docs' AND state = 'ready'`
	docsJobsDone  = `SELECT count(*) FROM dispatch_jobs WHERE name = 'loomplane.agent.knowledge.docs' AND state = 'completed'`
	docsJobsStuck = `SELECT count(*) FROM dispatch_jobs WHERE name = 'loomplane.agent.knowledge.docs' AND state = 'failed'`
)

// waitForDocs polls query on dsn until it answers want.
func waitForDocs(t *testing.T, dsn, query string, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		got := dbValue[int64](t, dsn, query)
		if got == want {
			return
		}
		if stuck := dbValue[int64](t, dsn, docsJobsStuck); stuck != 0 {
			var lastErr string
			_ = dbValueInto(t, dsn, `SELECT coalesce(last_error, '') FROM dispatch_jobs WHERE name = 'loomplane.agent.knowledge.docs' ORDER BY updated_at DESC LIMIT 1`, &lastErr)
			t.Fatalf("the docs job failed: %s", lastErr)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d, want %d", query, got, want)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// checkEmbedded fails the test unless requests embedded each of documents
// once: one request per document, every chunk of a page in one batch. A page
// embedded twice means a docs job ran again after its swap failed.
func checkEmbedded(t *testing.T, dsn string, requests, documents int64) {
	t.Helper()
	if requests != documents {
		t.Fatalf("%d embedding requests for %d documents: a page was embedded twice; docs jobs: %s", requests, documents, docsJobs(t, dsn))
	}
}

// docsJobs describes the docs jobs on dsn, for a failure message.
func docsJobs(t *testing.T, dsn string) string {
	t.Helper()
	var v string
	if err := dbValueInto(t, dsn, `SELECT coalesce(string_agg(state || ' after ' || retry_count || ' retries ' || coalesce(last_error, ''), '; '), '') FROM dispatch_jobs WHERE name = 'loomplane.agent.knowledge.docs'`, &v); err != nil {
		return err.Error()
	}
	return v
}

// dbValueInto is dbValue for a value that may be absent.
func dbValueInto(t *testing.T, dsn, query string, v any) error {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	return conn.QueryRow(ctx, query).Scan(v)
}

// The docs are indexed once per release: the first boot embeds every page
// and the schema, a reboot of the same release embeds nothing, and two
// servers started together on one database embed each page once.
func TestDocsIngestionAcrossReboots(t *testing.T) {
	bundle, err := agentknowledge.EmbeddedDocs()
	if err != nil {
		t.Fatal(err)
	}
	documents := int64(len(bundle.Pages) + 1) // every page and the schema
	const dimensions = 8
	fake := agenttest.Anthropic(t)

	dsn := freshPgvectorDatabase(t)
	emb := newEmbeddingServer(t, dimensions)
	ai := docsAI(fake, emb, dimensions)
	r := startDocs(t, dsn, ai)
	waitForDocs(t, dsn, docsJobsDone, 1)
	if got := dbValue[int64](t, dsn, readyDocs); got != documents {
		t.Fatalf("first boot indexed %d documents, want %d", got, documents)
	}
	firstBoot := emb.requests.Load()
	t.Logf("first boot: %d documents in %d embedding requests", documents, firstBoot)
	checkEmbedded(t, dsn, firstBoot, documents)
	r.stop()

	r = startDocs(t, dsn, ai)
	waitForDocs(t, dsn, docsJobsDone, 2)
	r.stop()
	if n := emb.requests.Load() - firstBoot; n != 0 {
		t.Fatalf("a reboot of the same release made %d embedding requests", n)
	}
	if got := dbValue[int64](t, dsn, readyDocs); got != documents {
		t.Fatalf("after a reboot %d documents, want %d", got, documents)
	}

	// Two servers on one fresh database, started together.
	dsn = freshPgvectorDatabase(t)
	emb = newEmbeddingServer(t, dimensions)
	ai = docsAI(fake, emb, dimensions)
	a := startDocs(t, dsn, ai)
	b := startDocs(t, dsn, ai)
	defer a.stop()
	defer b.stop()
	waitForDocs(t, dsn, docsJobsDone, 2)
	if got := dbValue[int64](t, dsn, readyDocs); got != documents {
		t.Fatalf("two servers indexed %d documents, want %d", got, documents)
	}
	checkEmbedded(t, dsn, emb.requests.Load(), documents)
}

// topicDimensions is the size of topicVector's vectors.
const topicDimensions = 4

// canaryTopic is what topicVector recognises.
const canaryTopic = "canary thresholds"

// topicVector embeds a text about canary thresholds along the first axis
// and everything else along the second, a little of each, so a search for
// canary thresholds ranks what's about them first and still finds the
// rest. Real embeddings do this by meaning; the test only needs the order.
func topicVector(text string) []float64 {
	v := make([]float64, topicDimensions)
	if strings.Contains(strings.ToLower(text), canaryTopic) {
		v[0], v[1] = 1, 0.1
	} else {
		v[0], v[1] = 0.1, 1
	}
	return v
}

// knowledgeWorkspace is a workspace on a's server with a budget and the
// agent on, and knowledge on when knowledge says so.
func knowledgeWorkspace(a *agentOrg, knowledge bool) {
	a.budget("10")
	a.enable(map[string]any{"knowledge": knowledge})
}

// Cortex's weave adapter, which the extension wires from the weave engine
// it finds, also searches the docs: listing a workspace's collections
// names the shared docs collection, under its own tenant.
func TestCortexKnowledgeSharesDocs(t *testing.T) {
	dsn := freshPgvectorDatabase(t)
	fake := agenttest.Anthropic(t)
	a := knowledgeOrg(t, dsn, docsAI(fake, newEmbeddingServerWith(t, topicVector), topicDimensions), "")
	defer a.r.stop()
	bundle, err := agentknowledge.EmbeddedDocs()
	if err != nil {
		t.Fatal(err)
	}
	waitForDocs(t, dsn, readyDocs, int64(len(bundle.Pages)+1)) // every page and the schema

	eng := inject[*cortexengine.Engine](t, a.container())
	kp := eng.Knowledge()
	if kp == nil {
		t.Fatal("cortex has no knowledge provider with knowledge configured")
	}
	ctx := cortex.WithScope(context.Background(), agentscope.Scope{OrgID: a.orgID}.Cortex())
	cols, err := kp.ListCollections(ctx)
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(cols) != 1 || cols[0].Name != agentknowledge.DocsCollection || cols[0].DocumentCount == 0 {
		t.Fatalf("a workspace's collections: %+v, want only %s, with the docs in it", cols, agentknowledge.DocsCollection)
	}
}

// searchResult is one result of a knowledge_search, as the model reads it.
type searchResult struct {
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata"`
}

// searchResults are the knowledge_search results in the last message of
// a request: the tool result the model receives after its call.
func searchResults(t *testing.T, req agenttest.Captured) []searchResult {
	t.Helper()
	text := toolResultText(t, req)
	var out struct {
		Status  string         `json:"status"`
		Results []searchResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil || out.Status != "success" {
		t.Fatalf("knowledge_search answered %s (%v)", text, err)
	}
	return out.Results
}

// toolResultText is the tool result in the last message of a request,
// out of the untrusted-content quote the run puts it in.
func toolResultText(t *testing.T, req agenttest.Captured) string {
	t.Helper()
	if len(req.Messages) == 0 {
		t.Fatal("a request with no messages")
	}
	var msg struct {
		Role    string `json:"role"`
		Content []struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		} `json:"content"`
	}
	last := req.Messages[len(req.Messages)-1]
	if err := json.Unmarshal(last, &msg); err != nil {
		t.Fatalf("the last message %s: %v", last, err)
	}
	for _, b := range msg.Content {
		if b.Type != "tool_result" {
			continue
		}
		var text string
		if json.Unmarshal(b.Content, &text) != nil {
			var parts []struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(b.Content, &parts); err != nil {
				t.Fatalf("tool result %s: %v", b.Content, err)
			}
			for _, p := range parts {
				text += p.Text
			}
		}
		// The run quotes a tool's result as untrusted content.
		if body, ok := strings.CutPrefix(text, `<untrusted_content source="tool_result">`); ok {
			text, _ = strings.CutSuffix(strings.TrimSpace(body), "</untrusted_content>")
		} else {
			t.Errorf("a tool result that isn't quoted as untrusted: %.200s", text)
		}
		return strings.TrimSpace(text)
	}
	t.Fatalf("no tool result in %s", last)
	return ""
}

// canaryReport is the o1 report the test indexes.
const canaryReport = "Canary thresholds tripped on checkout: error rate 4% against a 1% threshold."

// split counts a search's results by the tenant weave read each from:
// the docs', o1's report, and any other.
func split(results []searchResult, o1 string) (docs, reports, other int) {
	for _, r := range results {
		switch tenant := r.Metadata["tenant_id"]; {
		case tenant == agentknowledge.DocsTenant:
			docs++
		case tenant == o1 && strings.Contains(r.Content, "tripped on checkout"):
			reports++
		default:
			other++
		}
	}
	return docs, reports, other
}

// A run's knowledge_search reads the docs and its own workspace's
// documents, never another workspace's; a workspace with knowledge off
// isn't offered the tool.
func TestChatSearchesDocsAndOnlyItsOwnReports(t *testing.T) {
	search := agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "knowledge_search", Input: map[string]string{"query": "canary thresholds"}}}}
	// o2's second search names o1's collection by its ID, filled in once
	// it exists; the fake encodes a turn's input when it serves it.
	crossInput := map[string]string{"query": "canary thresholds"}
	cross := agenttest.Turn{ToolUses: []agenttest.ToolUse{{Name: "knowledge_search", Input: crossInput}}}
	fake := agenttest.Anthropic(t,
		search, agenttest.Turn{Text: "The docs explain canary thresholds."}, // o2
		cross, agenttest.Turn{Text: "I can't read that."}, // o2, naming o1's collection
		search, agenttest.Turn{Text: "Your checkout canary tripped."}, // o1
		agenttest.Turn{Text: "I can't search here."}, // o3
	)
	dsn := freshPgvectorDatabase(t)
	o1 := knowledgeOrg(t, dsn, docsAI(fake, newEmbeddingServerWith(t, topicVector), topicDimensions), "")
	defer o1.r.stop()
	o2 := secondAgentOrg(t, o1, "bo@example.com", "Other", "other")
	o3 := secondAgentOrg(t, o1, "cy@example.com", "Plain", "plain")
	knowledgeWorkspace(o1, true)
	knowledgeWorkspace(o2, true)
	knowledgeWorkspace(o3, false)
	waitForDocs(t, dsn, docsJobsDone, 1)

	knowledge := inject[*agentknowledge.Service](t, o1.container())
	outcome, err := knowledge.Ingest(context.Background(), o1.orgID, agentknowledge.Document{
		SourceKey: "report/arep_canary", Kind: agentknowledge.KindReport, Title: "Checkout canary",
		Content: canaryReport, URL: "/agent/reports/arep_canary",
	})
	if err != nil || outcome != agentknowledge.OutcomeIndexed {
		t.Fatalf("Ingest: %s, %v", outcome, err)
	}

	chat := func(a *agentOrg, message string) string {
		t.Helper()
		sent := converse(t, a.owner, message)
		waitTask(t, a.owner, sent.Task.ID, func(task agentruns.Task) bool { return agentruns.Terminal(task.Status) })
		if task := taskOf(t, a.owner, sent.Task.ID); task.Status != agentruns.StatusCompleted {
			t.Fatalf("the chat ended %s: %s", task.Status, task.Error)
		}
		return sent.Conversation.ID
	}

	crossInput["collection"] = dbValue[string](t, dsn, `SELECT id FROM weave_collections WHERE tenant_id = '`+o1.orgID+`' AND name = '`+agentknowledge.WorkspaceCollection+`'`)

	conversations := map[*agentOrg]string{}
	conversations[o2] = chat(o2, "What are canary thresholds?")
	chat(o2, "Read the other workspace's reports.")
	conversations[o1] = chat(o1, "Why did the checkout canary stop?")
	chat(o3, "What are canary thresholds?")

	reqs := fake.Requests()
	if len(reqs) != 7 {
		t.Fatalf("%d model requests, want 7", len(reqs))
	}
	for i, name := range map[int]string{0: "o2", 4: "o1"} {
		if !slices.Contains(reqs[i].Tools, "knowledge_search") {
			t.Errorf("%s's model isn't offered knowledge_search: %v", name, reqs[i].Tools)
		}
	}
	if docs, reports, other := split(searchResults(t, reqs[1]), o1.orgID); docs == 0 || reports != 0 || other != 0 {
		t.Errorf("o2's search: %d from the docs, %d of o1's reports, %d other", docs, reports, other)
	}
	if text := toolResultText(t, reqs[3]); !strings.Contains(text, `"error"`) || strings.Contains(text, "tripped") {
		t.Errorf("o2's search of o1's collection answered %s", text)
	}
	if docs, reports, other := split(searchResults(t, reqs[5]), o1.orgID); docs == 0 || reports != 1 || other != 0 {
		t.Errorf("o1's search: %d from the docs, %d of its reports, %d other", docs, reports, other)
	}
	if tools := reqs[6].Tools; slices.Contains(tools, "knowledge_search") || !slices.Contains(tools, "list_projects") {
		t.Errorf("with knowledge off the model is offered %v", tools)
	}

	// The step view shows the search as a read, with its query.
	for a, id := range conversations {
		h := history(t, a.owner, id)
		var call map[string]any
		for _, turn := range h.Turns {
			for _, e := range turn.Events {
				if e.Kind == "tool_call" {
					if err := json.Unmarshal(e.Data, &call); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		args, _ := call["arguments"].(map[string]any)
		if call["tool"] != "knowledge_search" || call["risk"] != "read" || args["query"] != "canary thresholds" {
			t.Errorf("the search's step: %v", call)
		}
	}
}
