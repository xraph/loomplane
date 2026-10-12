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
)

// Name is the API extension's name.
const Name = "loomplane-discovery"

// The compiler proves the service is what a new project attaches its
// repository through.
var _ projects.Attacher = (*discovery.Service)(nil)

// Extension serves the analysis API and the GitHub connection.
type Extension struct {
	*forge.BaseExtension
	cfg      config.Config
	engineOf func() *engine.Engine
	svc      *discovery.Service
	github   *repositories.Connections
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
	if e.github, err = connections(e.cfg, pg, authz, connectionAuditor{chronicle: audit, appID: e.cfg.Identity.AppID, log: e.Logger()}); err != nil {
		return err
	}
	e.svc.SetAccess(discovery.GitHubAccess{Tokens: e.github, Fetcher: repositories.NewGitHub(e.cfg.GitHub.APIURL)})
	// A new project's repository is attached as its first source, which
	// only discovery knows how to do.
	projectSvc, err := forge.InjectType[*projects.Service](c)
	if err != nil {
		return fmt.Errorf("discovery needs the projects extension: %w", err)
	}
	projectSvc.SetAttacher(e.svc)
	// Deployments mint repository tokens with the same connections.
	if err := forge.ProvideValue(c, e.github); err != nil {
		return fmt.Errorf("provide the GitHub connections: %w", err)
	}
	if err := e.routes(fapp.Router(), guard); err != nil {
		return err
	}
	return e.githubRoutes(fapp.Router(), guard)
}

// connections builds the organization GitHub connections, over the
// configured GitHub App or none.
func connections(cfg config.Config, pg *pgdriver.PgDB, authz repositories.Authorizer, audit repositories.Auditor) (*repositories.Connections, error) {
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
	return repositories.NewConnections(repositories.NewPgStore(pg), app, authz, audit, stateKey), nil
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
	case errors.Is(err, repositories.ErrBadState):
		return forge.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, repositories.ErrNotYourInstallation):
		return forge.NewHTTPError(http.StatusForbidden, err.Error())
	case errors.Is(err, repositories.ErrAppNotConfigured):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
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
	conns, err := connections(w.cfg, pg, nil, nil)
	if err != nil {
		return err
	}
	runner := discovery.NewRunner(discovery.NewPgStore(pg), repositories.NewGitHub(w.cfg.GitHub.APIURL),
		conns, w.cfg.Analysis.WorkDir, orchestration.AnalysisRetries+1)
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
