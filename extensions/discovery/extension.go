// Package discovery is the Forge extension for repository analysis. It
// has two halves that never run in the same process:
//
//   - New serves the analysis API from loomplane. It queues analyses but
//     never runs one.
//   - NewWorker runs analyses in loomplane-worker, which works the
//     analysis queue and nothing else.
//
// An analysis reads code nobody at Loomplane has vetted. Keeping it out of
// the API process keeps it away from sessions and the secret key.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/xraph/chronicle"
	"github.com/xraph/dispatch/engine"
	dispatchext "github.com/xraph/dispatch/extension"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/extensions/identity"
	projectsext "github.com/xraph/loomplane/extensions/projects"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/telemetry"
)

// Name is the API extension's name.
const Name = "loomplane-discovery"

// The compiler proves the service is what a new project attaches its
// repository through.
var _ projects.Attacher = (*discovery.Service)(nil)

// Deleting a project, or rolling a new one back, tells the service too.
var _ projects.Reaper = (*discovery.Service)(nil)

// Extension serves the analysis API and the GitHub, Bitbucket and GitLab
// connections.
type Extension struct {
	*forge.BaseExtension
	cfg      config.Config
	engineOf func() *engine.Engine
	svc      *discovery.Service
	github   *repositories.Connections
	// bitbucket is the Bitbucket workspaces, which read public repositories
	// even where no OAuth consumer is configured.
	bitbucket *repositories.BitbucketConnections
	// bitbucketHooks keeps the push webhooks on Bitbucket repositories.
	bitbucketHooks *discovery.BitbucketWebhooks
	gitlab         *repositories.GitLabConnections
}

// New returns the API extension. engineOf returns the dispatch
// extension's engine; it's only used to enqueue (see projects.New for why
// it isn't resolved from DI).
func New(cfg config.Config, engineOf func() *engine.Engine) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Repository analysis"),
		cfg:           cfg,
		engineOf:      engineOf,
	}
}

// Dependencies implements forge.Extension.
func (e *Extension) Dependencies() []string {
	return []string{identity.Name, dispatchext.ExtensionName, projectsext.Name}
}

// Register builds the service and mounts the routes.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	c := fapp.Container()
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return fmt.Errorf("discovery needs the grove extension: %w", err)
	}
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return fmt.Errorf("discovery needs the identity extension: %w", err)
	}
	guard, err := forge.InjectType[identity.Guard](c)
	if err != nil {
		return fmt.Errorf("discovery needs the identity extension: %w", err)
	}
	audit, err := forge.InjectType[*chronicle.Chronicle](c)
	if err != nil {
		return fmt.Errorf("discovery needs the audit trail: %w", err)
	}
	eng := e.engineOf()
	if eng == nil {
		return errors.New("discovery needs the dispatch extension to have registered")
	}
	pg := pgdriver.Unwrap(db)
	e.svc = discovery.NewService(discovery.NewPgStore(pg), projects.NewPgStore(pg), authz,
		orchestration.NewEnqueuer(eng), projectsext.NewAuditor(audit, e.cfg.Identity.AppID, e.Logger()))
	if e.github, err = connections(e.cfg, pg, authz, connectionAuditor{chronicle: audit, appID: e.cfg.Identity.AppID, log: e.Logger()}, e.Logger()); err != nil {
		return err
	}
	if e.bitbucket, err = bitbucketConnections(e.cfg, db, authz,
		bitbucketAuditor{chronicle: audit, appID: e.cfg.Identity.AppID, log: e.Logger()}, e.Logger()); err != nil {
		return err
	}
	var registry *repositories.GitLabRegistry
	e.gitlab, registry, err = gitlabConnections(e.cfg, db, pg, authz, gitlabAuditor{chronicle: audit, appID: e.cfg.Identity.AppID, log: e.Logger()})
	if err != nil {
		return err
	}
	// A source may be on any GitLab the organization can use.
	e.svc.SetHosts(repositories.Hosts{Registry: registry})
	e.svc.SetAccess(discovery.Access{
		Tokens:  SourceTokens(e.github, e.bitbucket, e.gitlab),
		Fetcher: SourceReaders(e.cfg, registry),
	})
	if e.bitbucketHooks, err = bitbucketWebhooks(e.cfg, pg, e.bitbucket, authz, e.svc,
		bitbucketHookAuditor{chronicle: audit, appID: e.cfg.Identity.AppID, log: e.Logger()}, e.Logger()); err != nil {
		return err
	}
	e.svc.SetSourceHooks(e.bitbucketHooks.SourceHooks())
	// A disconnect removes the workspace's webhooks before its row and
	// sign-in go.
	e.bitbucket.SetOnDisconnect(e.bitbucketHooks.ReleaseAll)
	// A new project's repository is attached as its first source, which
	// only discovery knows how to do.
	projectSvc, err := forge.InjectType[*projects.Service](c)
	if err != nil {
		return fmt.Errorf("discovery needs the projects extension: %w", err)
	}
	projectSvc.SetAttacher(e.svc)
	// Deleting a project takes its sources in the cascade, so discovery
	// reads them first and releases their webhooks after.
	projectSvc.AddReaper(e.svc)
	// Deployments mint repository tokens with the same connections.
	if err := forge.ProvideValue(c, e.github); err != nil {
		return fmt.Errorf("provide the GitHub connections: %w", err)
	}
	if err := forge.ProvideValue(c, e.bitbucket); err != nil {
		return fmt.Errorf("provide the Bitbucket connections: %w", err)
	}
	if err := forge.ProvideValue(c, e.gitlab); err != nil {
		return fmt.Errorf("provide the GitLab connections: %w", err)
	}
	if err := forge.ProvideValue(c, registry); err != nil {
		return fmt.Errorf("provide the GitLab hosts: %w", err)
	}
	if err := forge.ProvideValue(c, e.svc); err != nil {
		return fmt.Errorf("provide the discovery service: %w", err)
	}
	if err := e.routes(fapp.Router(), guard); err != nil {
		return err
	}
	if err := e.githubRoutes(fapp.Router(), guard); err != nil {
		return err
	}
	if err := e.bitbucketRoutes(fapp.Router(), guard); err != nil {
		return err
	}
	if err := e.bitbucketWebhookRoute(fapp.Router(), e.bitbucketHooks); err != nil {
		return err
	}
	if err := e.gitlabRoutes(fapp.Router(), guard); err != nil {
		return err
	}
	if err := e.webhookRoute(fapp.Router(), &discovery.Webhooks{
		Secret:        []byte(e.cfg.GitHub.WebhookSecret),
		Installations: repositories.NewPgStore(pg).InstallationOrgs,
		Sources:       discovery.NewPgStore(pg).SourcesByRepository,
		Start:         e.svc.StartFromPush,
	}); err != nil {
		return err
	}
	return e.gitlabWebhookRoute(fapp.Router(), &discovery.GitLabWebhooks{
		Target:  e.gitlab.WebhookTarget,
		Sources: discovery.NewPgStore(pg).SourcesByRepository,
		Start:   e.svc.StartFromGitLabPush,
		Unreadable: func(_ context.Context, id string) {
			e.Logger().Warn("a verified GitLab push couldn't be read", forge.F("connection", id))
		},
	})
}

// connections builds the organization GitHub connections, over the
// configured GitHub App or none. What fails without failing a call goes
// to log.
func connections(cfg config.Config, pg *pgdriver.PgDB, authz repositories.Authorizer, audit repositories.Auditor, log forge.Logger) (*repositories.Connections, error) {
	stateKey, err := security.DeriveKey(cfg.SecretKey, repositories.StateKeyLabel)
	if err != nil {
		return nil, err
	}
	// An interface holding a nil *App isn't nil, so build it only when set.
	var app repositories.GitHubApp
	if cfg.GitHub.AppConfigured() {
		a, err := newGitHubApp(cfg.GitHub)
		if err != nil {
			return nil, err
		}
		app = a
	}
	conns := repositories.NewConnections(repositories.NewPgStore(pg), app, authz, audit, stateKey)
	conns.SetWarn(func(msg string, err error) {
		if log != nil && err != nil {
			log.Warn("github: "+msg, forge.F("error", err.Error()))
		}
	})
	return conns, nil
}

func newGitHubApp(g config.GitHubConfig) (*repositories.App, error) {
	key := g.PrivateKey
	if g.PrivateKeyFile != "" {
		raw, err := os.ReadFile(g.PrivateKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read the GitHub App private key: %w", err)
		}
		key = string(raw)
	}
	app, err := repositories.NewApp(repositories.AppConfig{
		ID: g.AppID, Slug: g.AppSlug, PrivateKey: key, ClientID: g.ClientID, ClientSecret: g.ClientSecret,
		APIURL: g.APIURL, WebURL: g.WebURL,
	})
	if err != nil {
		return nil, fmt.Errorf("GitHub App: %w", err)
	}
	return app, nil
}

// Start has nothing to start.
func (e *Extension) Start(context.Context) error {
	e.MarkStarted()
	return nil
}

// fail maps a service error to its HTTP response.
func fail(err error) error {
	switch {
	case errors.Is(err, discovery.ErrNotFound), errors.Is(err, discovery.ErrSourceNotFound), errors.Is(err, projects.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, discovery.ErrAlreadyActive), errors.Is(err, discovery.ErrNoGraph):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, discovery.ErrAliasTaken), errors.Is(err, discovery.ErrRootsOverlap):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, discovery.ErrNoSources), errors.Is(err, discovery.ErrInvalid), errors.Is(err, discovery.ErrAliasReserved),
		errors.Is(err, discovery.ErrInvalidSource), errors.Is(err, discovery.ErrSourceInaccessible):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, readable(err))
	case errors.Is(err, repositories.ErrInstallationNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, repositories.ErrBadState), errors.Is(err, repositories.ErrBadWorkspace), errors.Is(err, repositories.ErrGitLabBadState):
		return forge.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, repositories.ErrGitLabHostExists), errors.Is(err, repositories.ErrGitLabHostInUse), errors.Is(err, repositories.ErrGitLabSignInLapsed):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, repositories.ErrGitLabHostsOff):
		return forge.NewHTTPError(http.StatusForbidden, err.Error())
	case errors.Is(err, repositories.ErrGitLabSubpath), errors.Is(err, repositories.ErrGitLabInsecure),
		errors.Is(err, repositories.ErrGitLabHostIncomplete), errors.Is(err, repositories.ErrGitLabSigningToken), errors.Is(err, repositories.ErrPrivateAddress):
		return forge.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, repositories.ErrGitLabConnectionNotFound), errors.Is(err, repositories.ErrGitLabHostNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, repositories.ErrNotYourInstallation), errors.Is(err, repositories.ErrNotAMember):
		return forge.NewHTTPError(http.StatusForbidden, err.Error())
	case errors.Is(err, repositories.ErrWorkspaceNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, repositories.ErrAppNotConfigured), errors.Is(err, repositories.ErrBitbucketNotConfigured), errors.Is(err, repositories.ErrBitbucketRefused):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, repositories.ErrBitbucketConsumerRejected):
		// Like a consumer that isn't set up at all, it's the operator's
		// configuration that's wrong, and nothing the caller can retry.
		return forge.NewHTTPError(http.StatusConflict, repositories.ErrBitbucketConsumerRejected.Error())
	case errors.Is(err, repositories.ErrRateLimited):
		return forge.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, err.Error())
	}
	return err
}

// readable drops the "invalid analysis: " prefix when a clearer cause
// follows it.
func readable(err error) string {
	for _, cause := range []error{repositories.ErrUnsupportedHost, repositories.ErrInvalidURL} {
		if errors.Is(err, cause) {
			return cause.Error()
		}
	}
	return err.Error()
}

// WorkerName is the worker extension's name.
const WorkerName = "loomplane-discovery-worker"

// Worker runs analyses. It only exists in loomplane-worker.
type Worker struct {
	*forge.BaseExtension
	cfg      config.Config
	engineOf func() *engine.Engine
}

// NewWorker returns the worker extension.
func NewWorker(cfg config.Config, engineOf func() *engine.Engine) *Worker {
	return &Worker{
		BaseExtension: forge.NewBaseExtension(WorkerName, "0.1.0", "Runs repository analyses"),
		cfg:           cfg,
		engineOf:      engineOf,
	}
}

// Dependencies implements forge.Extension.
func (w *Worker) Dependencies() []string {
	return []string{dispatchext.ExtensionName}
}

// Register registers the analysis job. Dispatch only takes work once every
// extension has started.
func (w *Worker) Register(fapp forge.App) error {
	if err := w.BaseExtension.Register(fapp); err != nil {
		return err
	}
	db, err := forge.InjectType[*grove.DB](fapp.Container())
	if err != nil {
		return fmt.Errorf("the analysis worker needs the grove extension: %w", err)
	}
	eng := w.engineOf()
	if eng == nil {
		return errors.New("the analysis worker needs the dispatch extension to have registered")
	}
	pg := pgdriver.Unwrap(db)
	// The worker mints repository tokens; it never decides who may, so
	// its Connections get no Authorizer or auditor.
	conns, err := connections(w.cfg, pg, nil, nil, w.Logger())
	if err != nil {
		return err
	}
	// Likewise Bitbucket's and GitLab's: no Authorizer or auditor.
	bitbucket, err := bitbucketConnections(w.cfg, db, nil, nil, w.Logger())
	if err != nil {
		return err
	}
	glConns, registry, err := gitlabConnections(w.cfg, db, pg, nil, nil)
	if err != nil {
		return err
	}
	readers := SourceReaders(w.cfg, registry)
	tokens := SourceTokens(conns, bitbucket, glConns)
	runner := discovery.NewRunner(discovery.NewPgStore(pg), readers, tokens, w.cfg.Analysis.WorkDir, orchestration.AnalysisRetries+1)
	runner.Telemetry = telemetry.New(fapp.Metrics())
	engine.Register(eng, orchestration.AnalysisJob(loggedRunner{runner: runner, log: w.Logger()}))
	return nil
}

// Start has nothing to start: Dispatch works the queue.
func (w *Worker) Start(context.Context) error {
	w.MarkStarted()
	return nil
}

// loggedRunner logs each failed attempt. Dispatch retries it; the log is
// where an operator finds out why.
type loggedRunner struct {
	runner *discovery.Runner
	log    forge.Logger
}

func (l loggedRunner) Run(ctx context.Context, orgID, analysisID string) error {
	err := l.runner.Run(ctx, orgID, analysisID)
	if err != nil {
		l.log.Warn("analysis attempt failed", forge.F("analysis_id", analysisID), forge.F("error", err.Error()))
	}
	return err
}

// connectionAuditor records GitHub connections in Chronicle, in the
// organization's stream.
type connectionAuditor struct {
	chronicle *chronicle.Chronicle
	appID     string
	log       forge.Logger
}

func (a connectionAuditor) Record(ctx context.Context, action string, p security.Principal, inst repositories.Installation) {
	id := strconv.FormatInt(inst.ID, 10)
	b := a.chronicle.Info(ctx, action, "github_installation", id).
		Category("integrations").AppID(a.appID).TenantID(p.OrgID).UserID(p.UserID).Outcome("success")
	if inst.Account != "" {
		b = b.Meta("account", inst.Account)
	}
	if err := b.Record(); err != nil {
		a.log.Error("record audit event", forge.F("action", action), forge.F("error", err.Error()))
	}
}
