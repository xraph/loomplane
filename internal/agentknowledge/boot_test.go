package agentknowledge_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xraph/weave"

	"github.com/xraph/loomplane/internal/agentknowledge"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/config"
)

// fakeVectors answers Available and Migrate as told and records the calls.
type fakeVectors struct {
	ok         bool
	reason     string
	probeErr   error
	migrateErr error

	probes     int
	migrations []int
}

func (f *fakeVectors) Available(context.Context) (ok bool, reason string, err error) {
	f.probes++
	return f.ok, f.reason, f.probeErr
}

func (f *fakeVectors) Migrate(_ context.Context, dimensions int) error {
	f.migrations = append(f.migrations, dimensions)
	return f.migrateErr
}

func (f *fakeVectors) untouched(t *testing.T) {
	t.Helper()
	if f.probes != 0 || len(f.migrations) != 0 {
		t.Fatalf("the database was touched: %d probes, migrations %v", f.probes, f.migrations)
	}
}

func priced(context.Context) bool { return true }

func unpriced(context.Context) bool { return false }

// knowledgeOn is AI with knowledge configured at dimensions.
func knowledgeOn(model string, dimensions int) config.AIConfig {
	return config.AIConfig{Enabled: true, Knowledge: config.AIKnowledgeConfig{
		EmbeddingProvider: config.AIProviderOpenAI, EmbeddingModel: model, Dimensions: dimensions,
	}}
}

func TestBootWithoutConfigNeverTouchesTheDatabase(t *testing.T) {
	v := &fakeVectors{ok: true}
	asked := false
	st := agentknowledge.Boot(context.Background(), config.AIConfig{Enabled: true}, func(context.Context) bool { asked = true; return true }, v)
	want := agentsettings.KnowledgeStatus{Configured: false, Available: false, Reason: agentknowledge.ReasonNotConfigured}
	if st != want {
		t.Fatalf("status %+v, want %+v", st, want)
	}
	if asked {
		t.Error("asked for a price with knowledge not configured")
	}
	v.untouched(t)
}

func TestBootWithoutAPriceStaysOff(t *testing.T) {
	v := &fakeVectors{ok: true}
	st := agentknowledge.Boot(context.Background(), knowledgeOn("nomic-embed-text", 768), unpriced, v)
	if !st.Configured || st.Available {
		t.Fatalf("status %+v", st)
	}
	for _, want := range []string{"ai.knowledge.embedding_model (nomic-embed-text)", "ai.prices"} {
		if !strings.Contains(st.Reason, want) {
			t.Errorf("reason %q doesn't name %q", st.Reason, want)
		}
	}
	v.untouched(t)
}

func TestBootWithoutPgvector(t *testing.T) {
	v := &fakeVectors{ok: false, reason: `extension "vector" is not available`}
	st := agentknowledge.Boot(context.Background(), knowledgeOn("text-embedding-3-small", 1536), priced, v)
	want := "Knowledge needs the pgvector extension in Loomplane's Postgres: extension \"vector\" is not available"
	if !st.Configured || st.Available || st.Reason != want {
		t.Fatalf("status %+v, want reason %q", st, want)
	}
	if len(v.migrations) != 0 {
		t.Fatalf("migrated without pgvector: %v", v.migrations)
	}
}

func TestBootProbeErrorKeepsKnowledgeOff(t *testing.T) {
	v := &fakeVectors{probeErr: errors.New("timeout")}
	st := agentknowledge.Boot(context.Background(), knowledgeOn("text-embedding-3-small", 1536), priced, v)
	if !st.Configured || st.Available || !strings.HasPrefix(st.Reason, "Loomplane couldn't check its database for pgvector") {
		t.Fatalf("status %+v", st)
	}
	if !strings.Contains(st.Reason, "timeout") {
		t.Errorf("reason %q doesn't give the cause", st.Reason)
	}
	if len(v.migrations) != 0 {
		t.Fatalf("migrated after a failed probe: %v", v.migrations)
	}
}

func TestBootDimensionMismatch(t *testing.T) {
	v := &fakeVectors{ok: true, migrateErr: fmt.Errorf("%w: weave_vectors.embedding has 1536 dimensions, want 768", weave.ErrDimensionMismatch)}
	st := agentknowledge.Boot(context.Background(), knowledgeOn("text-embedding-3-small", 768), priced, v)
	if !st.Configured || st.Available || !strings.Contains(st.Reason, "ai.knowledge.dimensions (768)") {
		t.Fatalf("status %+v", st)
	}
	if !strings.Contains(st.Reason, "weave_vectors") {
		t.Errorf("reason %q doesn't say which table to drop", st.Reason)
	}
}

// A migration that fails for any other reason keeps knowledge off with
// the cause.
func TestBootMigrateErrorKeepsKnowledgeOff(t *testing.T) {
	v := &fakeVectors{ok: true, migrateErr: errors.New("disk full")}
	st := agentknowledge.Boot(context.Background(), knowledgeOn("text-embedding-3-small", 1536), priced, v)
	want := "Setting up the knowledge table failed: disk full"
	if !st.Configured || st.Available || st.Reason != want {
		t.Fatalf("status %+v, want reason %q", st, want)
	}
}

func TestBootMigratesWithConfiguredDimensions(t *testing.T) {
	v := &fakeVectors{ok: true}
	st := agentknowledge.Boot(context.Background(), knowledgeOn("text-embedding-3-small", 1536), priced, v)
	want := agentsettings.KnowledgeStatus{Configured: true, Available: true}
	if st != want {
		t.Fatalf("status %+v, want %+v", st, want)
	}
	if v.probes != 1 || len(v.migrations) != 1 || v.migrations[0] != 1536 {
		t.Fatalf("%d probes, migrations %v; want one probe and Migrate(1536)", v.probes, v.migrations)
	}
}
