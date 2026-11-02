package aiagent

import (
	"context"
	"fmt"

	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/nexus"
	"github.com/xraph/weave/embedder"
	weavenexus "github.com/xraph/weave/embedder/nexus"
	weaveengine "github.com/xraph/weave/engine"
	weaveext "github.com/xraph/weave/extension"
	"github.com/xraph/weave/vectorstore/pgvector"

	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/config"
)

// knowledgeUnavailable is why knowledge isn't there yet.
const knowledgeUnavailable = "Knowledge isn't set up yet on this server"

// bootKnowledge makes knowledge ready on this server and says whether it
// is. Start calls it after the library extensions have started and before
// any agent job runs. 3b replaces it with the documentation ingestion; until
// then knowledge is configured or not, and never available.
var bootKnowledge = func(_ context.Context, cfg config.AIConfig, _ *grove.DB) agentsettings.KnowledgeStatus {
	return agentsettings.KnowledgeStatus{Configured: cfg.Knowledge.Configured(), Available: false, Reason: knowledgeUnavailable}
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

// newKnowledgeExtension builds weave without routes or migrations (3b runs
// pgvector's group when knowledge boots) and without its recovery loop,
// which Loomplane schedules itself.
func newKnowledgeExtension(ai config.AIConfig, gateway func() *nexus.Gateway, tenants func(context.Context, string) (string, error)) *knowledgeExtension {
	return &knowledgeExtension{
		Extension: weaveext.New(weaveext.WithConfig(weaveext.Config{RecoveryInterval: -1}), weaveext.WithDisableRoutes(), weaveext.WithDisableMigrate()),
		ai:        ai, gateway: gateway, tenants: tenants,
	}
}

// Register gives weave pgvector over Loomplane's database and the nexus
// embedder, then registers it.
func (k *knowledgeExtension) Register(app forge.App) error {
	db, err := forge.InjectType[*grove.DB](app.Container())
	if err != nil {
		return fmt.Errorf("knowledge needs the grove extension: %w", err)
	}
	weaveext.WithVectorStore(pgvector.New(db, pgvector.WithDimensions(k.ai.Knowledge.Dimensions)))(k.Extension)
	weaveext.WithEngineOption(weaveengine.WithEmbedder(newEmbedder(k.ai, k.gateway, k.tenants)))(k.Extension)
	return k.Extension.Register(app)
}

// Stop marks weave stopped without stopping its engine, whose Stop closes
// its store: the store wraps Loomplane's own database, which grove closes
// last. The engine has nothing else to stop: its recovery loop is off and
// it has no plugins.
func (k *knowledgeExtension) Stop(context.Context) error {
	k.MarkStopped()
	return nil
}

// newEmbedder embeds through nexus with the server's embedding model,
// always on the embeddings provider so no embedding is routed to a chat
// provider, and bills the workspace's tenant.
func newEmbedder(ai config.AIConfig, gateway func() *nexus.Gateway, tenants func(context.Context, string) (string, error)) embedder.Embedder {
	return weavenexus.New(gateway, ai.Knowledge.EmbeddingModel, ai.Knowledge.Dimensions,
		weavenexus.WithTenantMapper(tenants), weavenexus.WithProvider(agentsettings.EmbeddingsProvider))
}
