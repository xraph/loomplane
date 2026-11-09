package aiagent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/xraph/cortex/safety"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/sqlitedriver"
	"github.com/xraph/nexus/guard"
	"github.com/xraph/nexus/provider"
	"github.com/xraph/shield"
	shieldengine "github.com/xraph/shield/engine"
	"github.com/xraph/shield/presets"
	shieldsqlite "github.com/xraph/shield/store/sqlite"

	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/config"
)

// guardOrg is the workspace the libraries' tests seed shield for.
const guardOrg = "org_guard"

// A fake AWS access key: shield's aws_access_key detector finds it.
const awsKey = "AKIAIOSFODNN7EXAMPLE"

// libraries starts an app with Loomplane's database (in-memory SQLite,
// shield's tables migrated) and the extension's libraries registered as
// the platform registers them, with the InfraAgent preset seeded for
// guardOrg the way turning the agent on seeds it.
func libraries(t *testing.T, ai config.AIConfig) *Extension {
	t.Helper()
	ctx := context.Background()
	drv := sqlitedriver.New()
	if err := drv.Open(ctx, ":memory:"); err != nil {
		t.Fatal(err)
	}
	db, err := grove.Open(drv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = shieldsqlite.New(db).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ext := New(config.Config{AI: ai}, nil)
	app := forge.New(forge.WithAppName("libraries-guard-test"), forge.WithEnvPrefix("LOOMPLANE_TEST_FORGE_"))
	if err = app.RegisterExtension(&groveProvider{BaseExtension: forge.NewBaseExtension("grove", "0", "test database"), db: db}); err != nil {
		t.Fatal(err)
	}
	for _, lib := range ext.LibraryExtensions() {
		if err = app.RegisterExtension(lib); err != nil {
			t.Fatal(err)
		}
	}
	if err = app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if err = (shieldSeeder{eng: ext.shieldEngine}).Seed(ctx, guardOrg); err != nil {
		t.Fatal(err)
	}
	return ext
}

// shieldCtx is a call's context as agentscope.Binder leaves it for
// guardOrg: shield's tenant on it.
func shieldCtx() context.Context {
	return shield.WithTenant(context.Background(), guardOrg)
}

// chunk is one thing a stream gave, and when.
type chunk struct {
	text string
	at   time.Time
}

// readStream reads s to its end: the text chunks and the error it ended
// with.
func readStream(t *testing.T, s provider.Stream) ([]chunk, error) {
	t.Helper()
	defer func() { _ = s.Close() }()
	var out []chunk
	for {
		c, err := s.Next(shieldCtx())
		if err != nil {
			return out, err
		}
		if c != nil && c.Delta.Content != "" {
			out = append(out, chunk{text: c.Delta.Content, at: time.Now()})
		}
	}
}

// streamed asks the gateway the libraries built for a streamed reply.
func streamed(t *testing.T, ext *Extension, model string) (chunks []chunk, endedAt time.Time, end error) {
	t.Helper()
	gw := ext.nexus.Gateway()
	s, err := gw.Engine().CompleteStream(shieldCtx(), &provider.CompletionRequest{
		Model: model, Stream: true, MaxTokens: 100,
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	chunks, end = readStream(t, s)
	return chunks, time.Now(), end
}

func joined(chunks []chunk) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(c.text)
	}
	return b.String()
}

// The gateway LibraryExtensions builds hands each chunk on as the
// provider sends it, and checks the whole reply when it ends: a reply
// with a credential ends with *guard.BlockedError, not io.EOF; one with
// an email address ends cleanly (cortex's gate redacts what people see).
func TestGatewayGuardsStreamsWithPassthrough(t *testing.T) {
	const gap = 300 * time.Millisecond
	cases := []struct {
		name    string
		deltas  []string
		blocked bool
	}{
		{"clean", []string{"Your projects: ", "api and web."}, false},
		{"credential", []string{"Here it is: ", "the key is " + awsKey + "."}, true},
		{"email", []string{"Ask the owner: ", "rex@example.com."}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := agenttest.Anthropic(t, agenttest.Turn{Deltas: tc.deltas, DeltaGap: gap})
			ai := fake.AIConfig()
			ext := libraries(t, ai)
			chunks, endedAt, end := streamed(t, ext, ai.Model)
			if len(chunks) != 2 || joined(chunks) != strings.Join(tc.deltas, "") {
				t.Fatalf("chunks %+v", chunks)
			}
			// Passthrough: the first chunk came while the provider was
			// still answering, not with the verdict.
			if endedAt.Sub(chunks[0].at) < gap/2 {
				t.Fatalf("the first chunk came %v before the end: buffered", endedAt.Sub(chunks[0].at))
			}
			var be *guard.BlockedError
			switch {
			case tc.blocked && (!errors.As(end, &be) || be.Phase != guard.PhaseOutput || strings.Contains(be.Reason, awsKey)):
				t.Fatalf("the stream ended with %v, want an output block that doesn't quote the key", end)
			case !tc.blocked && !errors.Is(end, io.EOF):
				t.Fatalf("the stream ended with %v, want io.EOF", end)
			}
		})
	}
}

// With no shield engine there's nothing to check a reply with, so the
// guard stops it.
func TestOutputGuardFailsClosedWithoutShield(t *testing.T) {
	g := outputGuard(func() *shieldengine.Engine { return nil })
	if g.Phase() != guard.PhaseOutput {
		t.Fatalf("phase %s", g.Phase())
	}
	r, err := g.Check(shieldCtx(), &guard.CheckInput{Messages: []provider.Message{{Role: "assistant", Content: "hello"}}})
	if err != nil || r == nil || !r.Blocked {
		t.Fatalf("Check: %+v, %v", r, err)
	}
	r, err = outputGuard(nil).Check(shieldCtx(), &guard.CheckInput{})
	if err != nil || r == nil || !r.Blocked {
		t.Fatalf("nil resolver: %+v, %v", r, err)
	}
}

// seededShield is a shield engine over an in-memory store, the
// InfraAgent preset seeded for guardOrg.
func seededShield(t *testing.T) *shieldengine.Engine {
	t.Helper()
	ctx := context.Background()
	drv := sqlitedriver.New()
	if err := drv.Open(ctx, ":memory:"); err != nil {
		t.Fatal(err)
	}
	db, err := grove.Open(drv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := shieldsqlite.New(db)
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = presets.InfraAgent().Seed(ctx, st, guardOrg, agentscope.App); err != nil {
		t.Fatal(err)
	}
	eng, err := shieldengine.New(shieldengine.WithStore(st))
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// The guard maps shield's verdict for a net that can't change delivered
// text: a credential stops the reply, other redactions and flags let it
// through untouched, and it never returns a rewrite.
func TestOutputGuardMapsShieldsDecision(t *testing.T) {
	eng := seededShield(t)
	g := outputGuard(func() *shieldengine.Engine { return eng })
	cases := []struct {
		name, text string
		blocked    bool
	}{
		{"clean", "Your projects: api and web.", false},
		{"aws key", "The key is " + awsKey + ".", true},
		{"github token", "Use ghp_abcdefghijklmnopqrstuvwxyz0123456789 to push.", true},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----", true},
		{"email", "Ask rex@example.com.", false},
		{"destructive command", "Run rm -rf / on the box.", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for range 2 { // the second answers from nexusguard's verdict cache
				r, err := g.Check(shieldCtx(), &guard.CheckInput{Messages: []provider.Message{{Role: "assistant", Content: tc.text}}})
				if err != nil {
					t.Fatal(err)
				}
				if r.Blocked != tc.blocked || r.Modified || r.Action == guard.ActionRedact {
					t.Fatalf("verdict %+v, want blocked %v and no rewrite", r, tc.blocked)
				}
				if r.Blocked && strings.Contains(r.Reason, "AKIA") {
					t.Fatalf("the reason quotes the reply: %q", r.Reason)
				}
			}
		})
	}
	// The tenant can also come from nexus, when ctx carries no shield
	// tenant.
	r, err := g.Check(context.Background(), &guard.CheckInput{TenantID: guardOrg, Messages: []provider.Message{{Role: "assistant", Content: "The key is " + awsKey + "."}}})
	if err != nil || !r.Blocked {
		t.Fatalf("nexus tenant: %+v, %v", r, err)
	}
}

// Cortex's engine, built the way the extension builds it (its config and
// automatic wiring, then the host's options), publishes checked segments
// and refuses to run without shield's segment checker: the host's policy
// won over cortex's default.
func TestCortexPublishesCheckedSegments(t *testing.T) {
	ext := libraries(t, serverAI())
	c := ext.cortex.Engine().RuntimeCapabilities()
	if c.PublicationMode != safety.ModeCheckedSegments || !c.PublicationRequired || !c.IncrementalGateInstalled || !c.PublicationGateInstalled {
		t.Fatalf("capabilities %+v", c)
	}
	// Without shield there's no checker, and the engine doesn't start.
	alone := New(config.Config{AI: serverAI()}, nil)
	app := forge.New(forge.WithAppName("cortex-alone-test"), forge.WithEnvPrefix("LOOMPLANE_TEST_FORGE_"))
	alone.cortex.SetDependencies(nil)
	if err := app.RegisterExtension(alone.cortex); err != nil {
		t.Fatal(err)
	}
	err := app.Start(context.Background())
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if !errors.Is(err, safety.ErrUnavailable) {
		t.Fatalf("cortex without shield started: %v", err)
	}
}
