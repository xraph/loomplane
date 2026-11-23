// Package runners is the Forge extension for runner pools: the runner pools
// API, the hooks that check a runner when it connects, and the relay every
// connection that names a pool dials through.
package runners

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xraph/dispatch/cron"
	"github.com/xraph/dispatch/engine"
	dispatchext "github.com/xraph/dispatch/extension"
	"github.com/xraph/dispatch/job"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	groveext "github.com/xraph/grove/extension"

	connectionsext "github.com/xraph/loomplane/extensions/connections"
	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/buildrunner"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/platformcloud"
	"github.com/xraph/loomplane/internal/relay"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
	"github.com/xraph/loomplane/pkg/provider"
)

// Name is the extension's name, for other extensions' Dependencies.
const Name = "loomplane-runners"

// Extension serves runner pools.
type Extension struct {
	*forge.BaseExtension
	container forge.Container
	cfg       config.Config
	hub       *nodes.Hub
	svc       *buildrunner.Service
	engineOf  func() *engine.Engine
}

// New returns the extension. Its service exists from here on, so
// extensions built before it registers can be given it; every method waits
// for Register to bind its store.
func New(cfg config.Config, hub *nodes.Hub, engineOf func() *engine.Engine) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Runner pools: runners that build in Firecracker microVMs and relay deploys"),
		cfg:           cfg,
		hub:           hub,
		svc:           &buildrunner.Service{PublicURL: cfg.PublicURL},
		engineOf:      engineOf,
	}
}

// SetAdmins gives the service who may manage global runner pools. The
// identity extension is it. Call it before the platform starts.
func (e *Extension) SetAdmins(a buildrunner.PlatformAdmins) { e.svc.Admins = a }

// SetRevoker gives the service what revokes an on-demand runner whose
// machine is gone. The nodes extension is it. Call it before the platform
// starts.
func (e *Extension) SetRevoker(r buildrunner.Revoker) { e.svc.Revoker = r }

// Service is the runner pool service.
func (e *Extension) Service() *buildrunner.Service { return e.svc }

// Dependencies implements forge.Extension.
func (e *Extension) Dependencies() []string {
	return []string{groveext.ExtensionName, identity.Name, connectionsext.Name, dispatchext.ExtensionName}
}

// Register binds the store, hooks runners coming online, registers the
// relay and mounts the API.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	c := fapp.Container()
	e.container = c
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return fmt.Errorf("runners needs the grove extension: %w", err)
	}
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return fmt.Errorf("runners needs the identity extension: %w", err)
	}
	clouds, err := forge.InjectType[*connections.Service](c)
	if err != nil {
		return fmt.Errorf("runners needs the connections extension: %w", err)
	}
	keys, err := forge.InjectType[*apikeys.Service](c)
	if err != nil {
		return fmt.Errorf("runners needs the identity extension's API keys: %w", err)
	}
	guard, err := forge.InjectType[identity.Guard](c)
	if err != nil {
		return fmt.Errorf("runners needs the identity extension: %w", err)
	}
	pg := pgdriver.Unwrap(db)
	e.svc.Streams = e.hub
	e.svc.Nodes = nodes.NewPgStore(pg)
	e.svc.Connections = clouds
	e.svc.Authz = authz
	e.svc.Tokens = runnerTokens{keys: keys}
	e.svc.Provisioner = cloudProvisioner{clouds: clouds, do: digitalocean.New(e.cfg.Clouds.DigitalOceanEndpoint), aws: aws.New(e.cfg.Clouds.AWSEndpoint)}
	e.svc.Bind(buildrunner.NewPgStore(pg))
	relay.Set(e.svc)
	e.hub.OnOnline(e.svc.RunnerOnline)
	e.hub.OnInventory(func(ctx context.Context, n *nodes.Node, _, _ nodeproto.Inventory) { e.svc.RunnerOnline(ctx, n) })
	if err = forge.ProvideValue(c, e.svc); err != nil {
		return fmt.Errorf("provide the runner pool service: %w", err)
	}
	if eng := e.engineOf(); eng != nil {
		engine.Register(eng, job.NewDefinition(jobReconcile, func(ctx context.Context, _ tick) error { return e.svc.Reconcile(ctx) }, job.WithMaxRetries(0)))
	}
	if err := e.routes(fapp.Router(), guard...); err != nil {
		return err
	}
	return e.platformRoutes(fapp.Router(), guard...)
}

// platformRoutes are the platform admin's: the global runner pools every
// enabled workspace may build on.
func (e *Extension) platformRoutes(r forge.Router, guard ...forge.Middleware) error {
	g := r.Group("/api/v1/platform/runner-pools", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Runners"))
	if err := g.GET("", e.platformList, forge.WithName("platformRunnerPools.list"), forge.WithSummary("The global runner pools, for a platform admin"),
		forge.WithResponseSchema(http.StatusOK, "RunnerPoolList", PoolList{})); err != nil {
		return err
	}
	if err := g.POST("", e.platformCreate, forge.WithName("platformRunnerPools.create"), forge.WithSummary("Create a global runner pool, for a platform admin"),
		forge.WithRequestSchema(buildrunner.PoolInput{}), forge.WithResponseSchema(http.StatusCreated, "RunnerPoolView", buildrunner.PoolView{})); err != nil {
		return err
	}
	return g.POST("/:id/join-tokens", e.platformJoinToken, forge.WithName("platformRunnerPools.joinToken"), forge.WithSummary("Make a single-use token for one global runner to join, for a platform admin"),
		forge.WithRequestSchema(JoinTokenRequest{}), forge.WithResponseSchema(http.StatusCreated, "RunnerJoinToken", JoinToken{}))
}

func (e *Extension) platformList(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	pools, err := e.svc.ListPlatformPools(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, PoolList{Pools: pools})
}

func (e *Extension) platformCreate(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in buildrunner.PoolInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON")
	}
	pool, err := e.svc.CreatePlatformPool(c.Request().Context(), p, in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, buildrunner.PoolView{Pool: *pool, Runners: []buildrunner.RunnerView{}})
}

func (e *Extension) platformJoinToken(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in JoinTokenRequest
	if err = c.BindJSON(&in); err != nil && !errors.Is(err, io.EOF) {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON, or empty")
	}
	token, expires, err := e.svc.PlatformJoinToken(c.Request().Context(), p, c.Param("id"), time.Duration(in.LifetimeMinutes)*time.Minute)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, JoinToken{Token: token, ExpiresAt: expires, Command: RunnerCommand(e.cfg.PublicURL, token)})
}

// runnerTokens mints a runner's join token through the API keys service.
type runnerTokens struct{ keys *apikeys.Service }

func (t runnerTokens) MintRunnerJoin(ctx context.Context, p security.Principal, poolID, machineID string, lifetime time.Duration) (string, time.Time, error) {
	scope := apikeys.NodeJoinScope{Role: apikeys.RoleRunner, PoolID: poolID}
	if machineID != "" {
		scope.Labels = map[string]string{buildrunner.MachineLabel: machineID}
	}
	tok, err := t.keys.CreateNodeJoinToken(ctx, p, scope, lifetime)
	if err != nil {
		return "", time.Time{}, err
	}
	return tok.Secret, tok.ExpiresAt, nil
}

// PoolList is the organization's runner pools.
type PoolList struct {
	Pools []buildrunner.PoolView `json:"pools"`
}

// JoinTokenRequest asks for a runner's join token.
type JoinTokenRequest struct {
	// LifetimeMinutes is how long the token can be used, an hour at most.
	LifetimeMinutes int `json:"lifetime_minutes,omitempty"`
}

// JoinToken is a runner's single-use join token and the command that uses it.
type JoinToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Command   string    `json:"command"`
}

func (e *Extension) routes(r forge.Router, guard ...forge.Middleware) error {
	g := r.Group("/api/v1/runner-pools", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Runners"))
	for _, route := range []struct {
		method, path string
		handler      forge.Handler
		opts         []forge.RouteOption
	}{
		{http.MethodGet, "", e.list, []forge.RouteOption{forge.WithName("runnerPools.list"), forge.WithSummary("The organization's runner pools and their runners"), forge.WithResponseSchema(http.StatusOK, "RunnerPoolList", PoolList{})}},
		{http.MethodPost, "", e.create, []forge.RouteOption{forge.WithName("runnerPools.create"), forge.WithSummary("Create a runner pool for the organization or one project"), forge.WithRequestSchema(buildrunner.PoolInput{}), forge.WithResponseSchema(http.StatusCreated, "RunnerPoolView", buildrunner.PoolView{})}},
		{http.MethodGet, "/:id", e.get, []forge.RouteOption{forge.WithName("runnerPools.get"), forge.WithSummary("One runner pool and its runners"), forge.WithResponseSchema(http.StatusOK, "RunnerPoolView", buildrunner.PoolView{})}},
		{http.MethodPatch, "/:id", e.update, []forge.RouteOption{forge.WithName("runnerPools.update"), forge.WithSummary("Replace a runner pool's settings, such as whether it is the default"), forge.WithRequestSchema(buildrunner.PoolSettings{}), forge.WithResponseSchema(http.StatusOK, "RunnerPoolView", buildrunner.PoolView{})}},
		{http.MethodDelete, "/:id", e.delete, []forge.RouteOption{forge.WithName("runnerPools.delete"), forge.WithSummary("Remove a runner pool once none of its runners is joined"), forge.WithNoContentResponse()}},
		{http.MethodPost, "/:id/join-tokens", e.joinToken, []forge.RouteOption{forge.WithName("runnerPools.joinToken"), forge.WithSummary("Make a single-use token for one runner to join the pool, and the command that uses it"), forge.WithRequestSchema(JoinTokenRequest{}), forge.WithResponseSchema(http.StatusCreated, "RunnerJoinToken", JoinToken{})}},
	} {
		var err error
		switch route.method {
		case http.MethodGet:
			err = g.GET(route.path, route.handler, route.opts...)
		case http.MethodPost:
			err = g.POST(route.path, route.handler, route.opts...)
		case http.MethodDelete:
			err = g.DELETE(route.path, route.handler, route.opts...)
		case http.MethodPatch:
			err = g.PATCH(route.path, route.handler, route.opts...)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func principal(c forge.Context) (security.Principal, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return p, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	return p, nil
}

func (e *Extension) list(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	pools, err := e.svc.ListPools(c.Request().Context(), p)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, PoolList{Pools: pools})
}

func (e *Extension) create(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in buildrunner.PoolInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON")
	}
	pool, err := e.svc.CreatePool(c.Request().Context(), p, in)
	if err != nil {
		return fail(err)
	}
	// The same view list and get answer with, so the dashboard's cache
	// knows the new pool belongs in the list.
	return c.JSON(http.StatusCreated, buildrunner.PoolView{Pool: *pool, Runners: []buildrunner.RunnerView{}})
}

func (e *Extension) get(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	pool, err := e.svc.GetPool(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, pool)
}

func (e *Extension) update(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in buildrunner.PoolSettings
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be the pool's settings, as JSON")
	}
	if _, err = e.svc.UpdateSettings(c.Request().Context(), p, c.Param("id"), in); err != nil {
		return fail(err)
	}
	view, err := e.svc.GetPool(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, view)
}

func (e *Extension) delete(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err = e.svc.DeletePool(c.Request().Context(), p, c.Param("id")); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (e *Extension) joinToken(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in JoinTokenRequest
	if err = c.BindJSON(&in); err != nil && !errors.Is(err, io.EOF) {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON, or empty")
	}
	token, expires, err := e.svc.JoinToken(c.Request().Context(), p, c.Param("id"), time.Duration(in.LifetimeMinutes)*time.Minute)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, JoinToken{Token: token, ExpiresAt: expires, Command: RunnerCommand(e.cfg.PublicURL, token)})
}

// RunnerCommand is the line to run on the machine that becomes a runner:
// node.sh installs the signed loomctl this Loomplane runs, then joins with
// the token and starts the runner as a service.
func RunnerCommand(publicURL, token string) string {
	return "curl -fsSL " + strings.TrimRight(publicURL, "/") + "/install/node.sh | LOOMPLANE_ROLE=runner LOOMPLANE_JOIN_TOKEN=" + token + " sh"
}

// fail maps a service error to the status the caller should see.
func fail(err error) error {
	switch {
	case errors.Is(err, buildrunner.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, buildrunner.ErrInvalid):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, buildrunner.ErrConflict), errors.Is(err, buildrunner.ErrInUse):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, apikeys.ErrInvalid):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, err.Error())
	}
	return err
}

// The scheduled job that keeps on-demand pools to their settings. Keep the
// names stable: queued jobs refer to them by name.
const (
	cronReconcile = "runner-pools-reconcile"
	jobReconcile  = "loomplane.runner_pools.reconcile"
)

// tick is the reconcile job's payload: each run covers every pool.
type tick struct{}

// Start schedules the reconcile job, which the cron leader runs every
// minute, so one replica starts and removes machines.
func (e *Extension) Start(ctx context.Context) error {
	if err := e.BaseExtension.Start(ctx); err != nil {
		return err
	}
	// Global builds are metered like Loomplane Cloud's, and only the
	// workspaces it enabled may build on global runners. Both come from
	// extensions that register after this one.
	if _, meter, err := metering.Resolve(e.container); err == nil {
		e.svc.Meter = meter
	}
	if platform, err := forge.InjectType[*platformcloud.Service](e.container); err == nil {
		e.svc.Workspaces = platform
	}
	eng := e.engineOf()
	if eng == nil {
		return nil
	}
	return engine.RegisterCron(ctx, eng, &cron.Definition[tick]{Name: cronReconcile, Schedule: "@every 1m", JobName: jobReconcile, Queue: orchestration.Queue})
}

// cloudProvisioner starts and removes on-demand runners' machines through
// the pool's DigitalOcean or AWS connection.
type cloudProvisioner struct {
	clouds *connections.Service
	do     *digitalocean.Client
	aws    *aws.Client
}

func (p cloudProvisioner) connection(ctx context.Context, orgID, conn string) (*connections.Connection, []byte, error) {
	c, raw, err := p.clouds.Credential(ctx, orgID, conn)
	if err != nil {
		return nil, nil, err
	}
	if c.Cloud != connections.DigitalOcean && c.Cloud != connections.AWS {
		return nil, nil, fmt.Errorf("%s isn't a DigitalOcean or AWS connection", conn)
	}
	return c, raw, nil
}

func (p cloudProvisioner) ec2(c *connections.Connection, raw []byte, region string) (*aws.Session, error) {
	key, err := aws.ParseKey(raw)
	if err != nil {
		return nil, err
	}
	return p.aws.Session(key, region, c.Details["account_id"]), nil
}

// Create implements buildrunner.Provisioner. The machine's ID is the
// operation or client token, so a retry finds what an earlier attempt
// started.
func (p cloudProvisioner) Create(ctx context.Context, orgID, conn string, spec buildrunner.MachineSpec) (string, error) {
	c, raw, err := p.connection(ctx, orgID, conn)
	if err != nil {
		return "", err
	}
	if c.Cloud == connections.AWS {
		s, serr := p.ec2(c, raw, spec.Region)
		if serr != nil {
			return "", serr
		}
		return s.StartRunner(ctx, aws.RunnerSpec{ID: spec.ID, Name: spec.Name, InstanceType: spec.Size, ImageID: spec.Image, UserData: spec.UserData, PoolTag: spec.PoolTag, Spot: spec.Spot})
	}
	cred, err := cloudinfra.Credential(c, raw)
	if err != nil {
		return "", err
	}
	res, err := p.do.CreateServer(ctx, cred, provider.ServerSpec{OperationID: spec.ID, Name: spec.Name, Region: spec.Region, Size: spec.Size, Image: spec.Image,
		Tags: spec.Tags, UserData: spec.UserData, ReconcileOnly: spec.ReconcileOnly})
	if err != nil {
		return "", err
	}
	return res.Server.ID, nil
}

// Delete implements buildrunner.Provisioner.
func (p cloudProvisioner) Delete(ctx context.Context, orgID, conn, region, providerID string) error {
	c, raw, err := p.connection(ctx, orgID, conn)
	if err != nil {
		return err
	}
	if c.Cloud == connections.AWS {
		s, serr := p.ec2(c, raw, region)
		if serr != nil {
			return serr
		}
		return s.StopRunner(ctx, providerID)
	}
	cred, err := cloudinfra.Credential(c, raw)
	if err != nil {
		return err
	}
	return p.do.DeleteServer(ctx, cred, providerID)
}

// List implements buildrunner.Provisioner.
func (p cloudProvisioner) List(ctx context.Context, orgID, conn, region, tag string) (map[string]string, error) {
	c, raw, err := p.connection(ctx, orgID, conn)
	if err != nil {
		return nil, err
	}
	if c.Cloud == connections.AWS {
		s, serr := p.ec2(c, raw, region)
		if serr != nil {
			return nil, serr
		}
		return s.Runners(ctx, tag)
	}
	cred, err := cloudinfra.Credential(c, raw)
	if err != nil {
		return nil, err
	}
	servers, err := p.do.ListServersTagged(ctx, cred, tag)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(servers))
	for _, s := range servers {
		out[s.ID] = s.Name
	}
	return out, nil
}
