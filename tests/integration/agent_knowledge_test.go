//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/weave/vectorstore/pgvector"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/agentknowledge"
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
// knowledge off with a reason naming the key and the size. The server
// still boots and the agent still answers.
func TestKnowledgeDimensionMismatchAfterReboot(t *testing.T) {
	dsn := freshPgvectorDatabase(t)
	fake := agenttest.Anthropic(t)
	a := knowledgeOrg(t, dsn, knowledgeAI(t, fake, 1536), "")
	if st := a.knowledge(t); !st.Available {
		t.Fatalf("first boot: knowledge_status %+v", st)
	}
	a.r.stop()

	a = knowledgeOrg(t, dsn, knowledgeAI(t, fake, 768), a.orgID)
	defer a.r.stop()
	st := a.knowledge(t)
	if !st.Configured || st.Available || !strings.Contains(st.Reason, "ai.knowledge.dimensions (768)") {
		t.Fatalf("after changing the size: knowledge_status %+v", st)
	}
	if got := dbValue[string](t, dsn, embeddingColumn); got != "vector(1536)" {
		t.Fatalf("the reboot changed weave_vectors.embedding to %q", got)
	}
}

// embeddingServer is an OpenAI-compatible /v1/embeddings that counts its
// requests and answers each input with a vector of dimensions numbers.
type embeddingServer struct {
	*httptest.Server
	requests atomic.Int64
}

func newEmbeddingServer(t *testing.T, dimensions int) *embeddingServer {
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
			v := make([]float64, dimensions)
			for j := range v {
				v[j] = float64((len(text)+j)%11+1) / 11
			}
			data[i] = item{Object: "embedding", Index: i, Embedding: v}
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
// once: one request per document, every chunk of a page in one batch.
//
// A retried docs job may embed one page again: grove's pgdriver names a
// transaction's prepared statements after the transaction's address and
// never deallocates them, so a later transaction at the same address on
// the same connection can fail with 42P05 at the page's swap, after its
// embed. The job's retry embeds that page again. Each retry may account
// for one request more, never two.
func checkEmbedded(t *testing.T, dsn string, requests, documents int64) {
	t.Helper()
	retries := dbValue[int64](t, dsn, `SELECT coalesce(sum(retry_count), 0) FROM dispatch_jobs WHERE name = 'loomplane.agent.knowledge.docs'`)
	if requests < documents || requests > documents+retries {
		t.Fatalf("%d embedding requests for %d documents (%d job retries): a page was embedded twice; docs jobs: %s", requests, documents, retries, docsJobs(t, dsn))
	}
	if retries > 0 {
		t.Logf("docs jobs retried %d times: %s", retries, docsJobs(t, dsn))
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
