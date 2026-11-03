//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/weave/vectorstore/pgvector"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/ops"
)

// knowledgeAI is the fake agent's AI settings with knowledge configured:
// OpenAI's priced embedding model, at dimensions, on a server that fails
// the test if anything embeds. Booting never should.
func knowledgeAI(t *testing.T, fake *agenttest.FakeAnthropic, dimensions int) config.AIConfig {
	t.Helper()
	embeddings := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("booting called the embedding provider: %s %s", r.Method, r.URL.Path)
		http.Error(w, "no", http.StatusTeapot)
	}))
	t.Cleanup(embeddings.Close)
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
