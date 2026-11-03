package aiagent

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/dispatch/cron"
	dispatchengine "github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/job"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/nexus"
	"github.com/xraph/weave/chunker"
	"github.com/xraph/weave/embedder"
	weavenexus "github.com/xraph/weave/embedder/nexus"
	weaveengine "github.com/xraph/weave/engine"
	weaveext "github.com/xraph/weave/extension"
	"github.com/xraph/weave/vectorstore"
	"github.com/xraph/weave/vectorstore/pgvector"

	"github.com/xraph/loomplane/internal/agentknowledge"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/pglock"
	"github.com/xraph/loomplane/pkg/manifest"
)

// newVectorStore is knowledge's vector store: pgvector over Loomplane's
// database. Tests swap it, since pgvector needs Postgres.
var newVectorStore = func(db *grove.DB, dimensions int) vectorstore.VectorStore {
	return pgvector.New(db, pgvector.WithDimensions(dimensions))
}

// knowledgeExtension is weave's extension, given its vector store and
// embedder in Register, once the database is in the container. Everything
// else is weave's own.
type knowledgeExtension struct {
	*weaveext.Extension
	ai      config.AIConfig
	gateway func() *nexus.Gateway
	tenants func(ctx context.Context, orgID string) (string, error)
}

// newKnowledgeExtension builds weave without routes or migrations
// (agentknowledge.Boot runs pgvector's group when knowledge boots) and
// without its recovery loop, which Loomplane schedules itself.
func newKnowledgeExtension(ai config.AIConfig, gateway func() *nexus.Gateway, tenants func(context.Context, string) (string, error)) *knowledgeExtension {
	return &knowledgeExtension{
		Extension: weaveext.New(weaveext.WithConfig(weaveext.Config{RecoveryInterval: -1}), weaveext.WithDisableRoutes(), weaveext.WithDisableMigrate()),
		ai:        ai, gateway: gateway, tenants: tenants,
	}
}

// Register gives weave pgvector over Loomplane's database, the nexus
// embedder and the recursive chunker, then registers it.
func (k *knowledgeExtension) Register(app forge.App) error {
	db, err := forge.InjectType[*grove.DB](app.Container())
	if err != nil {
		return fmt.Errorf("knowledge needs the grove extension: %w", err)
	}
	weaveext.WithVectorStore(newVectorStore(db, k.ai.Knowledge.Dimensions))(k.Extension)
	weaveext.WithEngineOption(weaveengine.WithEmbedder(newEmbedder(k.ai, k.gateway, k.tenants)))(k.Extension)
	// Weave's extension sets no chunker, and ingest refuses to run
	// without one.
	weaveext.WithEngineOption(weaveengine.WithChunker(chunker.NewRecursiveChunker()))(k.Extension)
	return k.Extension.Register(app)
}

// newEmbedder embeds through nexus with the server's embedding model,
// always on the embeddings provider so no embedding is routed to a chat
// provider, and bills the workspace's tenant.
func newEmbedder(ai config.AIConfig, gateway func() *nexus.Gateway, tenants func(context.Context, string) (string, error)) embedder.Embedder {
	return weavenexus.New(gateway, ai.Knowledge.EmbeddingModel, ai.Knowledge.Dimensions,
		weavenexus.WithTenantMapper(tenants), weavenexus.WithProvider(agentsettings.EmbeddingsProvider))
}

// knowledgeEngine is weave's engine, or nil when knowledge isn't
// configured or weave hasn't registered.
func (e *Extension) knowledgeEngine() agentknowledge.Engine {
	if e.weave == nil {
		return nil
	}
	eng := e.weave.Engine()
	if eng == nil {
		// Returned as is, a nil *Engine would make a non-nil interface.
		return nil
	}
	return eng
}

// registerKnowledge builds knowledge over weave and provides it whenever
// AI is on: with knowledge not configured its engine is nil, and every
// call is skipped. It registers the docs and recovery jobs.
func (e *Extension) registerKnowledge(c forge.Container, settings *agentsettings.Service) error {
	eng := e.jobs()
	if eng == nil {
		return errors.New("the agent's knowledge needs the dispatch extension to have registered")
	}
	log := e.Logger()
	e.system = &SystemTenant{gateway: gatewayAdapter{gw: e.nexus.Gateway}, budgetUSD: e.cfg.AI.Knowledge.DocsBudgetUSD}
	e.knowledge = agentknowledge.NewService(agentknowledge.Deps{
		Engine:   e.knowledgeEngine,
		Settings: settings,
		Binder:   e.binder,
		System:   e.system.ID,
		Config:   e.cfg.AI.Knowledge,
		Lock:     pglock.For(pgdriver.Unwrap(e.db)),
		Docs:     agentknowledge.EmbeddedDocs,
		Schema:   manifest.Schema,
		Warn:     func(msg string, err error) { log.Warn(msg, forge.F("error", err.Error())) },
	})
	dispatchengine.Register(eng, agentknowledge.IngestDocsJob(e.knowledge))
	dispatchengine.Register(eng, agentknowledge.RecoverJob(e.knowledge))
	if err := forge.ProvideValue(c, e.knowledge); err != nil {
		return fmt.Errorf("provide the agent's knowledge: %w", err)
	}
	return nil
}

// recoverCron schedules knowledge's recovery. Like the sweep it runs on
// the default queue, which every server works, so a server later run with
// AI off answers its ticks with the AI-off handler.
func recoverCron() *cron.Definition[struct{}] {
	return &cron.Definition[struct{}]{
		Name: agentknowledge.CronRecover, JobName: agentknowledge.JobRecover,
		Schedule: agentknowledge.RecoverEvery, Queue: agentruns.SweepQueue,
	}
}

// startKnowledge boots knowledge and hands the service its status. When
// knowledge is available it queues the docs job on the agent's queue and
// schedules recovery. Knowledge never stops the agent starting: a job
// that can't be queued is logged, and the next start tries again.
func (e *Extension) startKnowledge(ctx context.Context) {
	if e.knowledge == nil {
		return
	}
	st := agentknowledge.Boot(ctx, e.cfg.AI, e.embeddingsPriced, agentknowledge.PgVectors(e.db))
	e.knowledge.SetStatus(st)
	eng := e.jobs()
	if !st.Available || eng == nil {
		return
	}
	warn := func(msg string, err error) {
		if l := e.Logger(); l != nil {
			l.Warn(msg, forge.F("error", err.Error()))
		}
	}
	opts := append(agentknowledge.DocsJobOptions(), job.WithQueue(Queue))
	if _, err := dispatchengine.Enqueue(ctx, eng, agentknowledge.JobIngestDocs, struct{}{}, opts...); err != nil {
		warn("aiagent: couldn't queue indexing the docs", err)
	}
	if err := dispatchengine.RegisterCron(ctx, eng, recoverCron()); err != nil {
		warn("aiagent: couldn't schedule knowledge's recovery", err)
	}
}
