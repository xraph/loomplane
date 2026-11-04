package aiagent

import (
	"context"
	"time"

	"github.com/xraph/cortex"
	cortexengine "github.com/xraph/cortex/engine"
	cortexext "github.com/xraph/cortex/extension"
	cortexweave "github.com/xraph/cortex/knowledge/weave"
	nexusllm "github.com/xraph/cortex/llm/nexus"
	"github.com/xraph/cortex/safety"
	shieldsafety "github.com/xraph/cortex/safety/shield"
	"github.com/xraph/forge"
	groveext "github.com/xraph/grove/extension"
	"github.com/xraph/nexus"
	nexusext "github.com/xraph/nexus/extension"
	nexusstore "github.com/xraph/nexus/store"
	shieldext "github.com/xraph/shield/extension"
	weaveext "github.com/xraph/weave/extension"

	"github.com/xraph/loomplane/internal/agentknowledge"
	"github.com/xraph/loomplane/internal/agentscope"
)

// suspensionTTL is how long a run waits on a person before cortex gives up
// on it.
const suspensionTTL = 24 * time.Hour

// buildLibraries builds nexus, shield, weave (when knowledge is
// configured) and cortex. None of them migrates (Loomplane runs their
// migration groups in its own pass) or serves routes. Each waits for the
// database it finds in the container, and cortex for the nexus, shield
// and weave it builds its adapters from.
func (e *Extension) buildLibraries() {
	e.nexus = &nexusExtension{Extension: nexusext.New(e.nexusOptions()...)}
	e.nexus.SetDependencies([]string{groveext.ExtensionName})

	e.shield = shieldext.New(shieldext.WithDisableRoutes(), shieldext.WithDisableMigrate())
	e.shield.SetDependencies([]string{groveext.ExtensionName})

	cortexDeps := []string{groveext.ExtensionName, nexusext.ExtensionName, shieldext.ExtensionName}
	if e.cfg.AI.Knowledge.Configured() {
		e.weave = newKnowledgeExtension(e.cfg.AI, e.nexus.Gateway, e.nexusTenant)
		e.weave.SetDependencies([]string{groveext.ExtensionName, nexusext.ExtensionName})
		cortexDeps = append(cortexDeps, weaveext.ExtensionName)
	}

	e.cortex = cortexext.New(e.cortexOptions()...)
	e.cortex.SetDependencies(cortexDeps)
}

// nexusExtension is nexus's extension, with the store it finds in the
// container kept open when it stops. Nexus's Shutdown closes its store,
// and the store it builds from the container wraps Loomplane's own
// database, which the rest of the app still uses while it stops; grove
// closes it last.
type nexusExtension struct {
	*nexusext.Extension
}

// Register registers nexus, then gives its gateway, which hasn't started,
// the same store behind a Close that does nothing.
func (n *nexusExtension) Register(app forge.App) error {
	if err := n.Extension.Register(app); err != nil {
		return err
	}
	if gw := n.Gateway(); gw != nil && gw.Store() != nil {
		nexus.WithDatabase(borrowedStore{Store: gw.Store()})(gw)
	}
	return nil
}

// borrowedStore is a nexus store over a database nexus doesn't own.
type borrowedStore struct{ nexusstore.Store }

// Close leaves the database open for its owner.
func (borrowedStore) Close() error { return nil }

// LibraryExtensions are nexus, shield, weave (only when knowledge is
// configured) and cortex, in that order, for the platform to register
// before this extension.
func (e *Extension) LibraryExtensions() []forge.Extension {
	exts := []forge.Extension{e.nexus, e.shield}
	if e.weave != nil {
		exts = append(exts, e.weave)
	}
	return append(exts, e.cortex)
}

// cortexOptions configure the engine cortex builds in its Register from
// what nexus, shield and weave provided: the nexus adapter maps a run's
// scope to its workspace's tenant and provider, the shield adapter scans
// under agentscope.App, the weave adapter (only when knowledge is
// configured) searches the docs besides the workspace's own collections,
// and the engine options come last, so they win over cortex's config and
// its automatic wiring. The last of them is the publication policy.
func (e *Extension) cortexOptions() []cortexext.ExtOption {
	opts := []cortexext.ExtOption{
		// No WithDisableRoutes: cortex's REST handlers are a module of
		// their own, which Loomplane doesn't mount.
		cortexext.WithDisableMigrate(),
		cortexext.WithNexusOptions(nexusllm.WithTenant(e.cortexTenant), nexusllm.WithProvider(e.cortexProvider)),
		cortexext.WithShieldOptions(shieldsafety.WithApp(agentscope.App)),
	}
	if e.cfg.AI.Knowledge.Configured() {
		opts = append(opts,
			cortexext.WithWeaveOptions(knowledgeOptions()...),
			// A host engine option runs after the weave adapter is wired,
			// so this wraps it.
			cortexext.WithEngineOption(quietKnowledgeOption(e.warnKnowledge, e.knowledgeAuthz)),
		)
	}
	engineOpts := e.engineOptions()
	out := make([]cortexext.ExtOption, 0, len(opts)+len(engineOpts)+1)
	out = append(out, opts...)
	for _, o := range engineOpts {
		out = append(out, cortexext.WithEngineOption(o))
	}
	return append(out, cortexext.WithEngineOption(cortexengine.WithPublicationPolicy(publicationPolicy)))
}

// knowledgeOptions configure cortex's weave adapter. Its default tenant,
// cortex.RootTenant, is the scope's workspace, so a run searches its own
// workspace's collections, never another's; the docs collection, under
// its own tenant, is shared with every run, read only.
func knowledgeOptions() []cortexweave.Option {
	return []cortexweave.Option{
		cortexweave.WithShared(cortexweave.SharedCollection{Tenant: agentknowledge.DocsTenant, Collection: agentknowledge.DocsCollection}),
	}
}

// publicationPolicy is how cortex lets the model's text out: in segments
// shield has checked, as they arrive, so an answer streams token by token
// and no unchecked text is published. Required: the engine refuses to run
// without shield's segment checker rather than publish unchecked.
var publicationPolicy = safety.Policy{Mode: safety.ModeCheckedSegments, Required: true}

// engineOptions are the engine's own options: how long a run waits on a
// person, run leases and the run driver's hooks, rejections the model
// reads, the per-process run cap, the tool authorizer with the principal
// codec it needs (cortex refuses the first without the second), and the
// agent's operations.
func (e *Extension) engineOptions() []cortexengine.Option {
	tools := e.toolOptions()
	opts := make([]cortexengine.Option, 0, 7+len(tools))
	opts = append(opts,
		cortexengine.WithSuspensionTTL(suspensionTTL),
		// Runs are leased, so one whose worker dies or drains is left for
		// another replica. The run driver's hooks continue it: nothing in
		// cortex does.
		cortexengine.WithRunLease(runLease),
		cortexengine.WithExtension(e.hooks),
		cortexengine.WithRejectionsReturnToModel(),
		cortexengine.WithRunConcurrency(RunsPerProcess),
		cortexengine.WithToolAuthorizer(e.toolAuthorizer),
		cortexengine.WithPrincipalCodec(e.codec),
	)
	return append(opts, tools...)
}

// nexusTenant is the workspace's nexus tenant, or agentsettings.ErrDisabled
// while its agent is off.
func (e *Extension) nexusTenant(ctx context.Context, orgID string) (string, error) {
	s := e.agentSettings()
	if s == nil {
		return "", errNotReady
	}
	return s.NexusTenant(ctx, orgID)
}

// cortexTenant names the nexus tenant of a call whose context has none
// (agentscope.Binder puts it there): the tenant of the scope's workspace.
func (e *Extension) cortexTenant(ctx context.Context, s cortex.Scope) (string, error) {
	org, err := cortex.RootTenant(ctx, s)
	if err != nil {
		return "", err
	}
	return e.nexusTenant(ctx, org)
}

// cortexProvider names the nexus provider of a call from its scope: the
// provider the workspace's agent runs on. The server's provider when that
// can't be read; the workspace's credential resolver then refuses a call
// that doesn't match the workspace's key.
func (e *Extension) cortexProvider(ctx context.Context, s cortex.Scope) string {
	org, err := cortex.RootTenant(ctx, s)
	settings := e.agentSettings()
	if err == nil && settings != nil {
		eff, eerr := settings.Effective(ctx, org)
		if eerr == nil {
			return eff.Provider
		}
		err = eerr
	}
	if err == nil {
		err = errNotReady
	}
	if l := e.Logger(); l != nil {
		l.Warn("aiagent: couldn't read the workspace's agent provider; using the server's", forge.F("error", err.Error()))
	}
	return e.cfg.AI.Provider
}
