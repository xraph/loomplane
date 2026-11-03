package aiagent

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xraph/cortex"
	cortexengine "github.com/xraph/cortex/engine"
	"github.com/xraph/cortex/llm"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/sqlitedriver"
	"github.com/xraph/nexus"
	"github.com/xraph/nexus/provider"
	"github.com/xraph/nexus/providers/openai"
	"github.com/xraph/nexus/tenant"
	"github.com/xraph/weave"
	weaveext "github.com/xraph/weave/extension"
	"github.com/xraph/weave/vectorstore"
	vectormemory "github.com/xraph/weave/vectorstore/memory"

	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/security"
)

// fakeProvider is a nexus provider that records what reaches it.
type fakeProvider struct {
	name   string
	models []provider.Model

	mu       sync.Mutex
	complete []*provider.CompletionRequest
	embed    []*provider.EmbeddingRequest
}

func (p *fakeProvider) Name() string { return p.name }
func (p *fakeProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{Chat: true, Streaming: true, Embeddings: true}
}
func (p *fakeProvider) Models(context.Context) ([]provider.Model, error) { return p.models, nil }
func (p *fakeProvider) Complete(_ context.Context, req *provider.CompletionRequest) (*provider.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.complete = append(p.complete, req)
	return &provider.CompletionResponse{Provider: p.name, Model: req.Model, Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}}}, nil
}
func (p *fakeProvider) CompleteStream(context.Context, *provider.CompletionRequest) (provider.Stream, error) {
	return nil, errors.New("no streams here")
}
func (p *fakeProvider) Embed(_ context.Context, req *provider.EmbeddingRequest) (*provider.EmbeddingResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.embed = append(p.embed, req)
	out := &provider.EmbeddingResponse{Provider: p.name, Model: req.Model, Usage: provider.Usage{PromptTokens: 3, TotalTokens: 3}}
	for range req.Input {
		out.Embeddings = append(out.Embeddings, make([]float64, req.Dimensions))
	}
	return out, nil
}
func (p *fakeProvider) Healthy(context.Context) bool   { return true }
func (p *fakeProvider) SupportsDimensions(string) bool { return true }

func (p *fakeProvider) calls() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.complete), len(p.embed)
}

// gateway is a started nexus gateway with this extension's options and
// the providers given.
func gateway(t *testing.T, ai config.AIConfig, providers ...provider.Provider) *nexus.Gateway {
	t.Helper()
	opts := gatewayOptions(ai, nil, nil)
	for _, p := range providers {
		opts = append(opts, nexus.WithProvider(p))
	}
	gw := nexus.New(opts...)
	if err := gw.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gw.Shutdown(context.Background()) })
	return gw
}

func serverAI() config.AIConfig {
	ai := config.Defaults().AI
	ai.Enabled = true
	return ai
}

// A model nobody priced is refused before nexus calls its provider, so an
// agent never spends what no budget counts.
func TestGatewayRefusesUnpricedModels(t *testing.T) {
	fake := &fakeProvider{name: config.AIProviderOpenAICompatible}
	gw := gateway(t, serverAI(), fake)
	_, err := gw.Engine().Complete(context.Background(), &provider.CompletionRequest{
		Provider: config.AIProviderOpenAICompatible, Model: "llama",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	if !errors.Is(err, nexus.ErrModelUnpriced) {
		t.Fatalf("got %v, want ErrModelUnpriced", err)
	}
	if c, _ := fake.calls(); c != 0 {
		t.Fatalf("the provider was called %d times", c)
	}
}

func TestGatewayPricesConfiguredModels(t *testing.T) {
	ai := serverAI()
	ai.Prices = []config.AIPrice{{Provider: config.AIProviderOpenAICompatible, Model: "llama", InputPerMillion: "0.10", OutputPerMillion: "0.20"}}
	fake := &fakeProvider{name: config.AIProviderOpenAICompatible}
	gw := gateway(t, ai, fake)
	ctx := context.Background()
	if !gw.PriceBook().Priced(ctx, config.AIProviderOpenAICompatible, "llama") {
		t.Fatal("ai.prices didn't price the model")
	}
	if _, err := gw.Engine().Complete(ctx, &provider.CompletionRequest{
		Provider: config.AIProviderOpenAICompatible, Model: "llama",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	if c, _ := fake.calls(); c != 1 {
		t.Fatalf("the provider was called %d times", c)
	}
}

// The server key (ai.api_key) goes only to the server's provider; the
// other chat providers get none, so a workspace on them brings its own.
func TestServerKeyGoesOnlyToItsProvider(t *testing.T) {
	ai := serverAI()
	ai.Provider, ai.APIKey = config.AIProviderOpenAI, "k"
	got := serverKeys(ai)
	want := map[string]string{config.AIProviderAnthropic: "", config.AIProviderOpenAI: "k", config.AIProviderOpenAICompatible: ""}
	if len(got) != len(want) {
		t.Fatalf("keys %v", got)
	}
	for name, key := range want {
		if got[name] != key {
			t.Errorf("%s has key %q, want %q", name, got[name], key)
		}
	}
	providers := chatProviders(ai)
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		names = append(names, p.Name())
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{config.AIProviderAnthropic, config.AIProviderOpenAI, config.AIProviderOpenAICompatible}) {
		t.Fatalf("providers %v", names)
	}
}

// Cortex's extension leaves run_concurrency at 0, no limit. The agent's
// engine caps runs per process.
func TestCortexRunConcurrencyIsCapped(t *testing.T) {
	// Cortex needs shield's segment checker to publish, so it starts with
	// the other libraries.
	ai := libraries(t, serverAI())
	if got := ai.cortex.Engine().RunConcurrency(); got != RunsPerProcess {
		t.Fatalf("run concurrency %d, want %d", got, RunsPerProcess)
	}
}

// Knowledge embeds through the provider that's only for embeddings, never
// a chat provider that lists the same model, under the workspace's
// tenant.
func TestEmbeddingsNameTheirProvider(t *testing.T) {
	ai := serverAI()
	ai.Knowledge = config.AIKnowledgeConfig{EmbeddingProvider: config.AIProviderOpenAI, EmbeddingAPIKey: "ek", EmbeddingModel: "text-embedding-3-small", Dimensions: 8}
	priced := []provider.Model{{ID: "text-embedding-3-small", Pricing: provider.Pricing{Free: true}}}
	chat := &fakeProvider{name: config.AIProviderOpenAI, models: priced}
	emb := &fakeProvider{name: agentsettings.EmbeddingsProvider, models: priced}
	gw := gateway(t, ai, chat, emb)
	ctx := context.Background()
	tn, err := gw.Tenants().Create(ctx, &tenant.CreateInput{Name: "Engines", Slug: tenantSlug("org_1")})
	if err != nil {
		t.Fatal(err)
	}
	embedder := newEmbedder(ai, func() *nexus.Gateway { return gw }, func(_ context.Context, org string) (string, error) {
		if org != "org_1" {
			return "", agentsettings.ErrDisabled
		}
		return tn.ID.String(), nil
	})
	if _, err = embedder.Embed(weave.WithTenant(ctx, "org_1"), []string{"hello"}); err != nil {
		t.Fatal(err)
	}
	if c, e := chat.calls(); c+e != 0 {
		t.Fatalf("the chat provider got %d completions and %d embeddings", c, e)
	}
	if _, e := emb.calls(); e != 1 || emb.embed[0].Provider != agentsettings.EmbeddingsProvider || emb.embed[0].Dimensions != 8 {
		t.Fatalf("embeddings: %d, %+v", e, emb.embed)
	}
}

// embeddingsPricedOn is what Extension.embeddingsPriced reports, for a
// gateway built without the extension.
func embeddingsPricedOn(gw *nexus.Gateway, k config.AIKnowledgeConfig) bool {
	return gatewayAdapter{gw: func() *nexus.Gateway { return gw }}.Priced(context.Background(), agentsettings.EmbeddingsProvider, k.EmbeddingModel)
}

// The catalog prices OpenAI's embedding models, but under the provider
// name "openai". The embeddings provider has its own name, so the
// catalog row is copied over as an override.
func TestOpenAIEmbeddingsArePricedFromTheCatalog(t *testing.T) {
	ai := serverAI()
	ai.Knowledge = config.AIKnowledgeConfig{EmbeddingProvider: config.AIProviderOpenAI, EmbeddingAPIKey: "ek", EmbeddingModel: "text-embedding-3-small", Dimensions: 8}
	emb := &fakeProvider{name: agentsettings.EmbeddingsProvider}
	gw := gateway(t, ai, emb)
	ctx := context.Background()
	if !gw.PriceBook().Priced(ctx, agentsettings.EmbeddingsProvider, "text-embedding-3-small") || !embeddingsPricedOn(gw, ai.Knowledge) {
		t.Fatal("the openai embedding model isn't priced")
	}
	var want provider.Pricing
	models, _ := openai.New("").Models(ctx)
	for _, m := range models {
		if m.ID == "text-embedding-3-small" {
			want = m.Pricing
		}
	}
	got, ok := embeddingPrice(ctx, ai.Knowledge, nil)
	if !ok || want.EmbeddingPerMillion.IsZero() || !got.Pricing.EmbeddingPerMillion.Equal(want.EmbeddingPerMillion) {
		t.Fatalf("override %+v (ok %v), catalog %+v", got, ok, want)
	}
	if got.Provider != agentsettings.EmbeddingsProvider || got.Model != "text-embedding-3-small" {
		t.Fatalf("override is for %s/%s", got.Provider, got.Model)
	}
	tn, err := gw.Tenants().Create(ctx, &tenant.CreateInput{Name: "Engines", Slug: tenantSlug("org_1")})
	if err != nil {
		t.Fatal(err)
	}
	embedder := newEmbedder(ai, func() *nexus.Gateway { return gw }, func(context.Context, string) (string, error) { return tn.ID.String(), nil })
	if _, err = embedder.Embed(weave.WithTenant(ctx, "org_1"), []string{"hello"}); err != nil {
		t.Fatalf("an embedding was refused: %v", err)
	}
	if _, e := emb.calls(); e != 1 {
		t.Fatalf("the embeddings provider got %d calls", e)
	}
}

func TestCompatibleEmbeddingsArePricedFromAIPrices(t *testing.T) {
	ai := serverAI()
	ai.Knowledge = config.AIKnowledgeConfig{EmbeddingProvider: config.AIProviderOpenAICompatible, EmbeddingBaseURL: "http://localhost:11434/v1", EmbeddingModel: "nomic-embed-text", Dimensions: 8}
	ai.Prices = []config.AIPrice{{Provider: config.AIProviderOpenAICompatible, Model: "nomic-embed-text", InputPerMillion: "0", OutputPerMillion: "0"}}
	gw := gateway(t, ai, &fakeProvider{name: agentsettings.EmbeddingsProvider})
	if !embeddingsPricedOn(gw, ai.Knowledge) {
		t.Fatal("a zero ai.prices entry left the embedding model unpriced")
	}
	ai.Prices[0].InputPerMillion = "0.05"
	o, ok := embeddingPrice(context.Background(), ai.Knowledge, ai.Prices)
	if !ok || o.Pricing.EmbeddingPerMillion.String() != "0.05" || o.Pricing.InputPerMillion.String() != "0.05" {
		t.Fatalf("override %+v (ok %v)", o, ok)
	}
}

func TestUnpricedEmbeddingsAreReported(t *testing.T) {
	for _, k := range []config.AIKnowledgeConfig{
		{EmbeddingProvider: config.AIProviderOpenAICompatible, EmbeddingBaseURL: "http://localhost:11434/v1", EmbeddingModel: "nomic-embed-text", Dimensions: 8},
		{EmbeddingProvider: config.AIProviderOpenAI, EmbeddingModel: "text-embedding-unknown", Dimensions: 8},
	} {
		ai := serverAI()
		ai.Knowledge = k
		gw := gateway(t, ai, &fakeProvider{name: agentsettings.EmbeddingsProvider})
		if embeddingsPricedOn(gw, k) {
			t.Errorf("%s/%s reported as priced", k.EmbeddingProvider, k.EmbeddingModel)
		}
		if _, ok := embeddingPrice(context.Background(), k, nil); ok {
			t.Errorf("%s/%s has an override", k.EmbeddingProvider, k.EmbeddingModel)
		}
	}
}

func TestTenantSlug(t *testing.T) {
	if got := tenantSlug("aorg_01HX_Z"); got != "loomplane-aorg-01hx-z" {
		t.Fatalf("slug %q", got)
	}
}

// groveProvider stands in for the grove extension: it provides one
// database to the container, which every library finds there.
type groveProvider struct {
	*forge.BaseExtension
	db *grove.DB
}

func (g *groveProvider) Register(app forge.App) error {
	if err := g.BaseExtension.Register(app); err != nil {
		return err
	}
	return forge.ProvideValue(app.Container(), g.db)
}

func (g *groveProvider) Start(context.Context) error { g.MarkStarted(); return nil }

// shutdownRecorder is a weave plugin that notes the engine's shutdown.
type shutdownRecorder struct{ stopped atomic.Bool }

func (*shutdownRecorder) Name() string { return "shutdown-recorder" }
func (r *shutdownRecorder) OnShutdown(context.Context) error {
	r.stopped.Store(true)
	return nil
}

// The libraries borrow Loomplane's database from the container. Stopping
// them must leave it open: the rest of the app still stops after them,
// and grove closes it last. Weave's engine stops for real, recovery loop
// and plugins included.
func TestLibrariesLeaveTheDatabaseOpen(t *testing.T) {
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
	// pgvector needs Postgres; weave keeps its vectors in memory here.
	restore := newVectorStore
	newVectorStore = func(*grove.DB, int) vectorstore.VectorStore { return vectormemory.New() }
	t.Cleanup(func() { newVectorStore = restore })

	ai := serverAI()
	ai.Knowledge = config.AIKnowledgeConfig{EmbeddingProvider: config.AIProviderOpenAI, EmbeddingModel: "text-embedding-3-small", Dimensions: 8}
	ext := New(config.Config{AI: ai}, nil)
	shutdown := &shutdownRecorder{}
	weaveext.WithExtension(shutdown)(ext.weave.Extension)
	app := forge.New(forge.WithAppName("libraries-test"), forge.WithEnvPrefix("LOOMPLANE_TEST_FORGE_"))
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
	if err = app.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.Ping(ctx); err != nil {
		t.Fatalf("a library closed the shared database: %v", err)
	}
	if !shutdown.stopped.Load() {
		t.Fatal("weave's engine didn't stop: its plugins never heard")
	}
	if ext.weave.IsStarted() {
		t.Fatal("weave still reads as started")
	}
}

// Cortex refuses a tool authorizer without a principal codec, so the two
// go in together: the engine the extension builds starts, and one without
// the codec doesn't.
func TestEngineBuildsWithAuthorizerAndCodec(t *testing.T) {
	// The extension's engine starts (libraries fails the test if not).
	ai := libraries(t, serverAI())

	opts := ai.engineOptions()
	if _, err := cortexengine.New(opts...); err != nil {
		t.Fatalf("engine.New with the extension's options: %v", err)
	}
	withoutCodec := make([]cortexengine.Option, 0, len(ai.toolOptions())+1)
	withoutCodec = append(withoutCodec, ai.toolOptions()...)
	withoutCodec = append(withoutCodec, cortexengine.WithToolAuthorizer(ai.toolAuthorizer))
	if _, err := cortexengine.New(withoutCodec...); !errors.Is(err, cortex.ErrPrincipalCodecRequired) {
		t.Fatalf("engine.New without the codec: %v", err)
	}
	// Until Register binds it, the authorizer shows nothing and allows
	// nothing.
	s := cortex.Subject{Principal: security.AgentPrincipal("o", nil)}
	if got := ai.toolAuthorizer.Visible(context.Background(), s, []llm.Tool{{Name: "list_projects"}}); len(got) != 0 {
		t.Fatalf("an unbound authorizer shows %v", got)
	}
	if err := ai.toolAuthorizer.Authorize(context.Background(), s, llm.ToolCall{Name: "list_projects"}); err == nil {
		t.Fatal("an unbound authorizer allowed a call")
	}
}

// The sweep is scheduled on the default queue, which every server works:
// a server that runs with AI off later hands its ticks to the AI-off
// handler instead of piling them on a queue nobody works.
func TestTheSweepRunsOnTheDefaultQueue(t *testing.T) {
	if c := sweepCron(); c.Queue != "default" || c.JobName != agentruns.JobSweep || c.Schedule != "@every 15s" {
		t.Fatalf("cron %+v", c)
	}
}
