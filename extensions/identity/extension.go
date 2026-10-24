// Package identity is the Forge extension for who's calling Loomplane and
// what they may do.
//
// It runs authsome in-process for accounts, sessions and organizations,
// mounted at identity.base_path (/authsome), with Warden as its
// authorization engine and Chronicle as its audit trail, all on
// Loomplane's Postgres. Loomplane's own roles live in Warden too, one
// tenant per organization (internal/security).
//
// This wires authsome's engine directly, the way tap does
// (github.com/xraph/tap, extensions/identity), rather than registering
// authsome's own Forge extension. That extension needs separate chronicle
// and warden extensions in DI, probes for keysmith, relay, vault, ledger
// and herald, and runs its own migrations and config loading; Loomplane
// needs none of that and wants one migration pass (docs/forge-integration.md).
//
// Other extensions take Guard from DI and put it on their route groups.
package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	authsome "github.com/xraph/authsome"
	"github.com/xraph/authsome/api"
	"github.com/xraph/authsome/app"
	"github.com/xraph/authsome/bridge/chronicleadapter"
	"github.com/xraph/authsome/environment"
	authid "github.com/xraph/authsome/id"
	authmw "github.com/xraph/authsome/middleware"
	"github.com/xraph/authsome/plugins/organization"
	"github.com/xraph/authsome/plugins/password"
	"github.com/xraph/authsome/ratelimit"
	"github.com/xraph/authsome/settings"
	authstore "github.com/xraph/authsome/store"
	authpg "github.com/xraph/authsome/store/postgres"
	"github.com/xraph/chronicle"
	chronstore "github.com/xraph/chronicle/store"
	chronpg "github.com/xraph/chronicle/store/postgres"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	groveext "github.com/xraph/grove/extension"
	"github.com/xraph/keysmith"
	kspg "github.com/xraph/keysmith/store/postgres"
	"github.com/xraph/warden"
	wardenpg "github.com/xraph/warden/store/postgres"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/telemetry"
)

// Name is the extension's name, for other extensions' Dependencies.
const Name = "loomplane-identity"

const (
	// authsomeAPIPath is where authsome's routes sit inside its router.
	// authsome registers its core API and the organization plugin under
	// /v1; plugins that build paths from the engine's base path need it set
	// to the same, or they'd land under /authsome/authsome/....
	authsomeAPIPath = "/v1"
	appSlug         = "loomplane"
	pingTimeout     = 3 * time.Second
)

// Guard is the middleware for organization-scoped Loomplane routes: it
// authenticates the authsome session and puts a security.Principal on the
// request context.
type Guard []forge.Middleware

// Extension runs authsome, Warden and Chronicle.
type Extension struct {
	*forge.BaseExtension
	cfg config.Config

	store         *authpg.Store
	orgs          *organization.Plugin
	warden        *warden.Engine
	authz         *security.Authorizer
	audit         *chronicle.Chronicle
	engine        *authsome.Engine
	keys          *apikeys.Service
	telemetry     *telemetry.Recorder
	notifications *notifications.Service
	resetBusy     atomic.Bool
	router        forge.Router
}

// New returns the identity extension.
func New(cfg config.Config) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Accounts, organizations, authorization and audit"),
		cfg:           cfg,
	}
}

// Dependencies implements forge.Extension. The grove extension owns the
// database handle and, with central migrations, every table used here.
func (e *Extension) Dependencies() []string {
	return []string{groveext.ExtensionName, "loomplane-notification-delivery"}
}

// Register builds the engines. Nothing here touches the database: with
// Forge's central migrations the schema only exists once every extension
// has registered, so anything that reads or writes waits for Start.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	db, err := forge.InjectType[*grove.DB](fapp.Container())
	if err != nil {
		return fmt.Errorf("identity needs the grove extension: %w", err)
	}
	e.notifications, err = forge.InjectType[*notifications.Service](fapp.Container())
	if err != nil {
		return fmt.Errorf("identity needs notification delivery: %w", err)
	}
	e.store = authpg.New(db)
	wstore := wardenpg.New(db)
	if e.warden, err = warden.NewEngine(warden.WithStore(wstore), warden.WithLogger(e.Logger())); err != nil {
		return fmt.Errorf("warden: %w", err)
	}
	if e.audit, err = chronicle.New(
		chronicle.WithStore(chronstore.NewAdapter(chronpg.New(db))),
		chronicle.WithLogger(e.Logger()),
	); err != nil {
		return fmt.Errorf("chronicle: %w", err)
	}
	e.authz = security.NewAuthorizer(e.warden, wstore, e.membership)
	smith, err := keysmith.NewEngine(keysmith.WithStore(kspg.New(pgdriver.Unwrap(db))))
	if err != nil {
		return fmt.Errorf("keysmith: %w", err)
	}
	e.telemetry = telemetry.New(fapp.Metrics())
	e.keys = apikeys.New(smith, e.cfg.Identity.AppID, e.authz, keyAuditor{chronicle: e.audit, appID: e.cfg.Identity.AppID, log: e.Logger()})
	if e.engine, err = e.buildEngine(); err != nil {
		return err
	}
	// ProvideValue keys on its type parameter, so each value goes in with
	// its own concrete type.
	c := fapp.Container()
	if err := errors.Join(
		forge.ProvideValue(c, e.authz),
		forge.ProvideValue[security.PrincipalVerifier](c, e.keys),
		forge.ProvideValue(c, e.audit),
		forge.ProvideValue(c, e.engine),
		forge.ProvideValue(c, Guard{e.guard()}),
		forge.ProvideValue(c, RevalidateStream(e.revalidateStream)),
	); err != nil {
		return fmt.Errorf("provide identity services: %w", err)
	}
	return e.routes(fapp.Router())
}

func (e *Extension) buildEngine() (*authsome.Engine, error) {
	// Kept: Loomplane's member and invitation routes call its service.
	e.orgs = organization.New()
	tokens, err := security.TokenEncryptor(e.cfg.SecretKey)
	if err != nil {
		return nil, err
	}
	engine, err := authsome.NewEngine(
		authsome.WithStore(e.store),
		authsome.WithLogger(e.Logger()),
		authsome.WithChronicle(chronicleadapter.New(categorized{Emitter: e.audit, category: authAuditCategory})),
		authsome.WithTokenEncryptor(tokens),
		authsome.WithRateLimiter(routeLimiter{Limiter: ratelimit.NewKVLimiter(e.store)}),
		authsome.WithWarden(e.warden),
		// The grove extension migrates authsome's tables with everything
		// else (database/migrations).
		authsome.WithDisableMigrate(),
		authsome.WithAppID(e.cfg.Identity.AppID),
		authsome.WithBasePath(authsomeAPIPath),
		// Bootstrap adopts the app seed() creates as the platform app and
		// applies authsome's default platform roles. Nobody is promoted on
		// sign-up; organization roles come from organization membership.
		authsome.WithBootstrap(
			authsome.WithBootstrapAppSlug(appSlug),
			authsome.WithSkipDefaultEnvs(),
			authsome.WithInitialOwnerCount(0),
		),
		authsome.WithHerald(e.notifications.AuthBridge()),
		authsome.WithPlugin(password.New()),
		authsome.WithPlugin(recoveryNotifications(e.cfg)),
		authsome.WithPlugin(e.orgs),
		authsome.WithPlugin(security.NewSync(e.authz)),
	)
	if err != nil {
		return nil, fmt.Errorf("authsome engine: %w", err)
	}
	return engine, nil
}

// Start seeds Loomplane's authsome app, starts the engines and mounts
// authsome's API. authsome's plugins receive the engine while it starts, so
// their routes can only be registered after that.
func (e *Extension) Start(ctx context.Context) error {
	if err := e.seed(ctx); err != nil {
		return err
	}
	if err := e.warden.Start(ctx); err != nil {
		return fmt.Errorf("start warden: %w", err)
	}
	if err := e.engine.Start(ctx); err != nil {
		return fmt.Errorf("start authsome: %w", err)
	}
	if err := e.setting(ctx, "auth.require_email_verification", e.cfg.Identity.RequireEmailVerification); err != nil {
		return err
	}
	// Global, not per app: authsome only knows a request's app when it
	// sends the publishable key, which calls to Loomplane's own API don't.
	// This engine holds Loomplane's app alone, so global is per app anyway.
	if err := e.setting(ctx, authsome.SettingCookieName.Def.Key, e.cfg.Identity.SessionCookie); err != nil {
		return err
	}
	if err := e.mount(ctx); err != nil {
		return err
	}
	e.MarkStarted()
	return nil
}

// setting sets one of authsome's settings for the whole engine.
func (e *Extension) setting(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := e.engine.Settings().Set(ctx, key, raw, settings.ScopeGlobal, "", "", "", appSlug); err != nil {
		return fmt.Errorf("authsome setting %s: %w", key, err)
	}
	return nil
}

// mount puts authsome on its own router under the base path, so its
// middleware and routes stay out of Loomplane's API.
func (e *Extension) mount(ctx context.Context) error {
	r := forge.NewRouter()
	r.Use(rateLimitScope)
	r.Use(e.resetFence())
	r.Use(e.sessionMiddleware()...)
	if err := api.New(e.engine, r).RegisterRoutes(r); err != nil {
		return fmt.Errorf("authsome routes: %w", err)
	}
	for _, rp := range e.engine.Plugins().RouteProviders() {
		if err := rp.RegisterRoutes(r); err != nil {
			return fmt.Errorf("authsome plugin routes: %w", err)
		}
	}
	if err := r.Start(ctx); err != nil {
		return fmt.Errorf("start authsome router: %w", err)
	}
	e.router = r
	base := e.cfg.Identity.BasePath
	if err := e.App().Router().Handle(base, http.StripPrefix(base, r.Handler())); err != nil {
		return fmt.Errorf("mount authsome at %s: %w", base, err)
	}
	return nil
}

// Stop stops authsome, its router and Warden. The database closes with the
// grove extension.
func (e *Extension) Stop(ctx context.Context) error {
	var errs []error
	if e.router != nil {
		errs = append(errs, e.router.Stop(ctx))
	}
	if e.engine != nil {
		errs = append(errs, e.engine.Stop(ctx))
	}
	if e.warden != nil {
		errs = append(errs, e.warden.Stop(ctx))
	}
	e.MarkStopped()
	return errors.Join(errs...)
}

// Health checks authsome's store.
func (e *Extension) Health(ctx context.Context) error {
	if e.store == nil {
		return errors.New("identity not registered")
	}
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	return e.store.Ping(ctx)
}

// middleware authenticates the authsome session (its HttpOnly cookie, or a
// bearer token for API clients), keeps it alive (sessions.go) and resolves
// the principal.
func (e *Extension) middleware() []forge.Middleware {
	return append(e.sessionMiddleware(), requirePrincipal)
}

func requirePrincipal(next forge.Handler) forge.Handler {
	return func(c forge.Context) error {
		ctx := c.Request().Context()
		var userID, orgID string
		if u, ok := authmw.UserIDFrom(ctx); ok {
			userID = u.String()
		}
		if o, ok := authmw.OrgIDFrom(ctx); ok {
			orgID = o.String()
		}
		p, err := security.Resolve(userID, orgID, c.Request().Header.Get(security.HeaderOrganization))
		if err != nil {
			return forge.NewHTTPError(security.StatusFor(err), err.Error())
		}
		c.WithContext(security.WithPrincipal(ctx, p))
		return next(c)
	}
}

// membership is a user's role in an organization, if they're in it. The
// Authorizer uses it to repair a member whose Warden role is missing, and
// the session read uses it to notice someone who was removed.
func (e *Extension) membership(ctx context.Context, orgID, userID string) (memberRole string, found bool, err error) {
	oid, err := authid.ParseOrgID(orgID)
	if err != nil {
		return "", false, nil //nolint:nilerr // not an authsome org ID, so not a member
	}
	uid, err := authid.ParseUserID(userID)
	if err != nil {
		return "", false, nil //nolint:nilerr // not an authsome user ID, so not a member
	}
	m, err := e.store.GetMemberByUserAndOrg(ctx, uid, oid)
	if errors.Is(err, authstore.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(m.Role), true, nil
}

// seed creates Loomplane's authsome app and its default environment on the
// first start. Both are looked up first, so restarts change nothing.
func (e *Extension) seed(ctx context.Context) error {
	appID, err := authid.ParseAppID(e.cfg.Identity.AppID)
	if err != nil {
		return fmt.Errorf("identity.app_id: %w", err)
	}
	now := time.Now().UTC()
	if _, err := e.store.GetApp(ctx, appID); errors.Is(err, authstore.ErrNotFound) {
		if cerr := e.store.CreateApp(ctx, &app.App{
			ID: appID, Name: "Loomplane", Slug: appSlug, PublishableKey: e.cfg.Identity.PublishableKey,
			IsPlatform: true, CreatedAt: now, UpdatedAt: now,
		}); cerr != nil {
			return fmt.Errorf("seed authsome app: %w", cerr)
		}
	} else if err != nil {
		return fmt.Errorf("look up authsome app: %w", err)
	}
	if _, err := e.store.GetDefaultEnvironment(ctx, appID); errors.Is(err, authstore.ErrNotFound) {
		if cerr := e.store.CreateEnvironment(ctx, &environment.Environment{
			ID: authid.NewEnvironmentID(), AppID: appID, Name: "Default", Slug: "default",
			Type: environment.TypeDevelopment, IsDefault: true, CreatedAt: now, UpdatedAt: now,
		}); cerr != nil {
			return fmt.Errorf("seed authsome environment: %w", cerr)
		}
	} else if err != nil {
		return fmt.Errorf("look up authsome environment: %w", err)
	}
	return nil
}
