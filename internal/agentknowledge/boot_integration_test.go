//go:build integration

package agentknowledge_test

import (
	"context"
	"strings"
	"testing"

	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/weave/vectorstore/pgvector"

	"github.com/xraph/loomplane/internal/agentknowledge"
	"github.com/xraph/loomplane/internal/agenttest"
)

// openGrove opens dsn as the grove database Loomplane hands the agent.
func openGrove(t *testing.T, dsn string) *grove.DB {
	t.Helper()
	pg := pgdriver.New()
	if err := pg.Open(context.Background(), dsn); err != nil {
		t.Fatal(err)
	}
	db, err := grove.Open(pg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// one runs a query that returns a single value.
func one[T any](t *testing.T, db *grove.DB, query string) T {
	t.Helper()
	var v T
	rows, err := pgdriver.Unwrap(db).Query(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		t.Fatalf("%s: no row (%v)", query, rows.Err())
	}
	if err = rows.Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

const (
	vectorRows    = `SELECT count(*) FROM grove_migrations WHERE "group" = '` + pgvector.GroupName + `'`
	embeddingType = `SELECT coalesce((SELECT format_type(atttypid, atttypmod) FROM pg_attribute
		WHERE attrelid = to_regclass('weave_vectors') AND attname = 'embedding' AND NOT attisdropped), '')`
)

// On a Postgres without pgvector, Boot keeps knowledge off with the reason,
// and leaves the database as it found it: no extension, no table, no
// weave_pgvector row.
func TestPgVectorsOnPlainPostgres(t *testing.T) {
	db := openGrove(t, agenttest.FreshDatabase(t))
	if one[int64](t, db, `SELECT count(*) FROM pg_available_extensions WHERE name = 'vector'`) != 0 {
		t.Skip(agenttest.DatabaseEnv + " has pgvector; this test needs a Postgres without it")
	}
	st := agentknowledge.Boot(context.Background(), knowledgeOn("text-embedding-3-small", 1536), priced, agentknowledge.PgVectors(db))
	if !st.Configured || st.Available || !strings.Contains(st.Reason, "pgvector") {
		t.Fatalf("status %+v", st)
	}
	if n := one[int64](t, db, vectorRows); n != 0 {
		t.Fatalf("%d weave_pgvector rows without pgvector", n)
	}
	if one[bool](t, db, `SELECT to_regclass('weave_vectors') IS NOT NULL OR EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')`) {
		t.Fatal("Boot left a vector table or extension behind")
	}
}

// With pgvector, Boot makes the vector table at the configured size, once;
// a second boot changes nothing, and a boot with another size keeps
// knowledge off with the reason instead of failing.
func TestPgVectorsOnPgvector(t *testing.T) {
	db := openGrove(t, agenttest.FreshPgvectorDatabase(t))
	ctx := context.Background()
	v := agentknowledge.PgVectors(db)
	for boot := range 2 {
		st := agentknowledge.Boot(ctx, knowledgeOn("text-embedding-3-small", 1536), priced, v)
		if !st.Configured || !st.Available || st.Reason != "" {
			t.Fatalf("boot %d: status %+v", boot+1, st)
		}
	}
	if got := one[string](t, db, embeddingType); got != "vector(1536)" {
		t.Fatalf("weave_vectors.embedding is %q, want vector(1536)", got)
	}
	if n := one[int64](t, db, vectorRows); n != 1 {
		t.Fatalf("%d weave_pgvector rows, want 1", n)
	}

	st := agentknowledge.Boot(ctx, knowledgeOn("text-embedding-3-small", 768), priced, v)
	if !st.Configured || st.Available || !strings.Contains(st.Reason, "its vectors have 1536 dimensions, and ai.knowledge.dimensions is 768") {
		t.Fatalf("another size: status %+v", st)
	}
	if got := one[string](t, db, embeddingType); got != "vector(1536)" {
		t.Fatalf("another size changed the table to %q", got)
	}
}
