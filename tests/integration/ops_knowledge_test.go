//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/weave/vectorstore/pgvector"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/ops"
)

// knowledgeKeys are AI settings with an ai.knowledge block, AI on or off.
func knowledgeKeys(enabled bool, dimensions int) config.AIConfig {
	return config.AIConfig{Enabled: enabled, Knowledge: config.AIKnowledgeConfig{
		EmbeddingProvider: config.AIProviderOpenAI, EmbeddingModel: "text-embedding-3-small", Dimensions: dimensions,
	}}
}

// knowledgeRestore is how loomplane restore runs on a server with
// knowledge at dimensions: weave's pgvector group is on demand.
func knowledgeRestore(t *testing.T, dimensions int) ops.RestoreOptions {
	t.Helper()
	onDemand, err := migrations.OnDemand(knowledgeKeys(true, dimensions))
	if err != nil || len(onDemand) != 1 {
		t.Fatalf("on-demand groups %v, %v", onDemand, err)
	}
	return ops.RestoreOptions{OnDemand: onDemand, Optional: migrations.OptionalVersions(), VectorDimensions: dimensions}
}

// A backup taken where knowledge was on restores with its vector table:
// weave's pgvector group runs before the rows load, the vectors come back,
// and the schema guard knows the restored database.
func TestKnowledgeBackupRestoresItsVectors(t *testing.T) {
	ctx := context.Background()
	src := freshPgvectorDatabase(t)
	vectors, err := pgvector.Migrations(8)
	if err != nil {
		t.Fatal(err)
	}
	if err = ops.Migrate(ctx, src, append(migrations.All(), vectors)); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `INSERT INTO weave_vectors (id, embedding, content) VALUES ('v1', '[1,2,3,4,5,6,7,8]', 'kept')`)
	_ = conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var backup bytes.Buffer
	m, err := ops.Backup(ctx, src, "test", &backup)
	if err != nil {
		t.Fatal(err)
	}
	if m.VectorDimensions != 8 {
		t.Fatalf("the manifest says the vectors have %d dimensions, want 8", m.VectorDimensions)
	}

	dst := freshPgvectorDatabase(t)
	if _, err = ops.Restore(ctx, dst, migrations.All(), bytes.NewReader(backup.Bytes()), knowledgeRestore(t, 8)); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := dbValue[string](t, dst, `SELECT content FROM weave_vectors WHERE id = 'v1'`); got != "kept" {
		t.Fatalf("restored vector content %q", got)
	}
	if got := dbValue[string](t, dst, embeddingColumn); got != "vector(8)" {
		t.Fatalf("restored weave_vectors.embedding is %q, want vector(8)", got)
	}
	if n := dbValue[int64](t, dst, pgvectorRows); n != 1 {
		t.Fatalf("%d weave_pgvector rows after the restore, want 1", n)
	}
	if err = ops.CheckCompatibleWith(ctx, dst, migrations.All(), migrations.OptionalVersions()); err != nil {
		t.Fatalf("the schema guard refuses the restored database: %v", err)
	}
}

// Restoring a backup that has weave's pgvector group, on a server where
// knowledge isn't configured, is refused with what to set, not as a
// newer Loomplane's schema. A backup without it restores as before.
func TestKnowledgeBackupNeedsKnowledgeToRestore(t *testing.T) {
	ctx := context.Background()
	src := freshDatabase(t)
	if err := ops.Migrate(ctx, src, migrations.All()); err != nil {
		t.Fatal(err)
	}
	var plain bytes.Buffer
	if _, err := ops.Backup(ctx, src, "test", &plain); err != nil {
		t.Fatal(err)
	}
	// The bookkeeping row is enough for the manifest; this Postgres may
	// have no pgvector to make the table with.
	vectors, err := pgvector.Migrations(8)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	mg := vectors.Migrations()[0]
	_, err = conn.Exec(ctx, `INSERT INTO grove_migrations (version, name, "group") VALUES ($1, $2, $3)`, mg.Version, mg.Name, vectors.Name())
	_ = conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var withVectors bytes.Buffer
	if _, err = ops.Backup(ctx, src, "test", &withVectors); err != nil {
		t.Fatal(err)
	}

	off := ops.RestoreOptions{Optional: migrations.OptionalVersions()}
	_, err = ops.Restore(ctx, freshDatabase(t), migrations.All(), bytes.NewReader(withVectors.Bytes()), off)
	if err == nil || !strings.Contains(err.Error(), "set ai.enabled and ai.knowledge") || strings.Contains(err.Error(), "or later") {
		t.Fatalf("restoring a knowledge backup without knowledge: %v", err)
	}
	if _, err = ops.Restore(ctx, freshDatabase(t), migrations.All(), bytes.NewReader(plain.Bytes()), off); err != nil {
		t.Fatalf("restoring a backup without the pgvector group: %v", err)
	}
	if empty, _ := migrations.OnDemand(config.AIConfig{Enabled: true, Knowledge: config.AIKnowledgeConfig{Dimensions: 8}}); len(empty) != 0 {
		t.Fatalf("knowledge not configured, and on-demand groups %v", empty)
	}
}

// With AI off, an ai.knowledge block left in config.yaml turns on nothing
// for a restore either: a knowledge backup is refused with what to set,
// before the restore touches the target, even on a Postgres that has
// pgvector. No vector extension, no table, not even grove's bookkeeping.
func TestKnowledgeBackupWithAIOffLeavesPgvectorAlone(t *testing.T) {
	ctx := context.Background()
	src := freshPgvectorDatabase(t)
	vectors, err := pgvector.Migrations(8)
	if err != nil {
		t.Fatal(err)
	}
	if err = ops.Migrate(ctx, src, append(migrations.All(), vectors)); err != nil {
		t.Fatal(err)
	}
	var backup bytes.Buffer
	if _, err = ops.Backup(ctx, src, "test", &backup); err != nil {
		t.Fatal(err)
	}

	onDemand, err := migrations.OnDemand(knowledgeKeys(false, 8))
	if err != nil || len(onDemand) != 0 {
		t.Fatalf("AI off: on-demand groups %v, %v", onDemand, err)
	}
	dst := freshPgvectorDatabase(t)
	_, err = ops.Restore(ctx, dst, migrations.All(), bytes.NewReader(backup.Bytes()),
		ops.RestoreOptions{OnDemand: onDemand, Optional: migrations.OptionalVersions()})
	if err == nil || !strings.Contains(err.Error(), "set ai.enabled and ai.knowledge") {
		t.Fatalf("restoring a knowledge backup with AI off: %v", err)
	}
	if dbValue[bool](t, dst, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector') OR to_regclass('weave_vectors') IS NOT NULL OR to_regclass('grove_migrations') IS NOT NULL`) {
		t.Fatal("a refused restore touched the target database")
	}
}

// knowledgeBackup is a backup of a database with knowledge's vector table
// at 8 dimensions, holding one vector.
func knowledgeBackup(t *testing.T) []byte {
	t.Helper()
	ctx := context.Background()
	src := freshPgvectorDatabase(t)
	vectors, err := pgvector.Migrations(8)
	if err != nil {
		t.Fatal(err)
	}
	if err = ops.Migrate(ctx, src, append(migrations.All(), vectors)); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `INSERT INTO weave_vectors (id, embedding, content) VALUES ('v1', '[1,2,3,4,5,6,7,8]', 'kept')`)
	_ = conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var backup bytes.Buffer
	if _, err = ops.Backup(ctx, src, "test", &backup); err != nil {
		t.Fatal(err)
	}
	return backup.Bytes()
}

// untouched fails the test if a refused restore left anything on dsn: the
// vector extension, the vector table, or grove's bookkeeping.
func untouched(t *testing.T, dsn string) {
	t.Helper()
	if dbValue[bool](t, dsn, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector') OR to_regclass('weave_vectors') IS NOT NULL OR to_regclass('grove_migrations') IS NOT NULL OR to_regclass('weave_collections') IS NOT NULL`) {
		t.Fatal("a refused restore touched the target database")
	}
}

// A knowledge backup restored where ai.knowledge.dimensions is another
// size is refused, naming both sizes, before the restore touches the
// target. With the size put back, the same target takes it, without
// --force.
func TestKnowledgeRestoreRefusesAnotherSize(t *testing.T) {
	ctx := context.Background()
	backup := knowledgeBackup(t)
	dst := freshPgvectorDatabase(t)
	_, err := ops.Restore(ctx, dst, migrations.All(), bytes.NewReader(backup), knowledgeRestore(t, 4))
	if err == nil || !strings.Contains(err.Error(), "have 8 dimensions, and ai.knowledge.dimensions is 4") || !strings.Contains(err.Error(), "set ai.knowledge.dimensions to 8") {
		t.Fatalf("restoring 8-dimension vectors at 4: %v", err)
	}
	untouched(t, dst)

	if _, err = ops.Restore(ctx, dst, migrations.All(), bytes.NewReader(backup), knowledgeRestore(t, 8)); err != nil {
		t.Fatalf("restoring again at the backup's size: %v", err)
	}
	if got := dbValue[string](t, dst, `SELECT content FROM weave_vectors WHERE id = 'v1'`); got != "kept" {
		t.Fatalf("restored vector content %q", got)
	}
}

// A knowledge backup restored into a Postgres without pgvector is refused
// with the reason, before the restore touches the target, so running it
// again (into a Postgres that has it) needs no --force.
func TestKnowledgeRestoreRefusesWithoutPgvector(t *testing.T) {
	ctx := context.Background()
	backup := knowledgeBackup(t)
	dst := freshDatabase(t)
	if dbValue[int64](t, dst, vectorAvailableOn) != 0 {
		t.Skip("LOOMPLANE_TEST_DATABASE_URL has pgvector; this test needs a Postgres without it")
	}
	for attempt := range 2 {
		_, err := ops.Restore(ctx, dst, migrations.All(), bytes.NewReader(backup), knowledgeRestore(t, 8))
		if err == nil || !strings.Contains(err.Error(), "can't have pgvector") || errors.Is(err, ops.ErrNotEmpty) {
			t.Fatalf("attempt %d: restoring a knowledge backup without pgvector: %v", attempt+1, err)
		}
		untouched(t, dst)
	}
}
