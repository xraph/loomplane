package octopusloom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

// adminPort is the name of the gateway component's control port, which
// serves the admin API and FARP.
const adminPort = "admin"

// releaseTimeout bounds what one release's syncs take, together.
const releaseTimeout = 10 * time.Second

// readySyncTimeout bounds the sync Ready runs on the deploy job's context,
// which has no deadline of its own. It matches a write's.
const readySyncTimeout = writeSyncTimeout

// Revoker ends the admin proxy sessions open on a composed resource.
// *catalogproxy.Service is the real one.
type Revoker interface {
	RevokeResource(ctx context.Context, orgID, environmentID, resource string) error
}

// InternalAccess reaches a composed resource's components and generated
// secrets for Loomplane's own background work, checking no permission.
// *deployments.CatalogAccess is the real one.
type InternalAccess interface {
	InternalChannel(ctx context.Context, orgID string, ref deployments.ComponentRef) (*deployments.ComponentChannel, error)
	InternalSecret(ctx context.Context, orgID string, ref deployments.ResourceRef, name string) ([]byte, error)
}

var _ InternalAccess = (*deployments.CatalogAccess)(nil)

// InternalOpener opens the gateway's admin port through a.
func InternalOpener(a InternalAccess) ChannelOpener {
	return func(ctx context.Context, ref GatewayRef) (*deployments.ComponentChannel, error) {
		return a.InternalChannel(ctx, ref.OrgID, deployments.ComponentRef{
			ResourceRef: resourceRef(ref), Component: recipes.OctopusGateway, Port: adminPort,
		})
	}
}

// InternalSecrets reads the gateway's generated secrets through a.
func InternalSecrets(a InternalAccess) SecretReader {
	return func(ctx context.Context, ref GatewayRef, name string) ([]byte, error) {
		return a.InternalSecret(ctx, ref.OrgID, resourceRef(ref), name)
	}
}

func resourceRef(ref GatewayRef) deployments.ResourceRef {
	return deployments.ResourceRef{ProjectID: ref.ProjectID, EnvironmentID: ref.EnvironmentID, Resource: ref.Resource}
}

// HookDeps are what the Octopus kind's hooks use. The config file is built
// from what the Syncer reads, so it and what the Syncer pushes agree. A nil
// Revoker ends no sessions.
type HookDeps struct {
	Store   Store
	Syncer  *Syncer
	Revoker Revoker
}

// Hooks are the Octopus kind's catalog hooks and its release observer.
// They render the gateway's config file from what Loomplane knows, push
// routes and upstreams once the gateway is up, sync each gateway a service
// release affects, and forget the gateway when it's deleted.
type Hooks struct {
	deployments.NoHooks
	d HookDeps
	// releaseTimeout bounds Released's syncs, together.
	releaseTimeout time.Duration
}

var (
	_ deployments.CatalogHooks    = (*Hooks)(nil)
	_ deployments.ReleaseObserver = (*Hooks)(nil)
)

// NewHooks returns the Octopus kind's hooks.
func NewHooks(d HookDeps) *Hooks { return &Hooks{d: d, releaseTimeout: releaseTimeout} }

func runRef(run deployments.ComponentRun) GatewayRef {
	return GatewayRef{OrgID: run.OrgID, ProjectID: run.ProjectID, EnvironmentID: run.EnvironmentID, Resource: run.Resource}
}

// Files implements deployments.CatalogHooks. It records the gateway, then
// renders its config file from the environment's bindings, the gateway's
// manual routes and the hostnames attached to it. A gateway that moved to
// another provider or namespace gets a new generation, since what it
// should hold changed.
func (h *Hooks) Files(ctx context.Context, run deployments.ComponentRun) (map[string][]byte, error) {
	if run.Component.Name != recipes.OctopusGateway {
		return map[string][]byte{}, nil
	}
	ref := runRef(run)
	before, err := h.d.Store.Gateway(ctx, ref)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("read Octopus gateway %s: %w", ref.Resource, err)
	}
	g := &Gateway{GatewayRef: ref, Provider: run.Provider, Namespace: run.TargetSettings["namespace"]}
	if err = h.d.Store.EnsureGateway(ctx, g); err != nil {
		return nil, fmt.Errorf("record Octopus gateway %s: %w", ref.Resource, err)
	}
	if before != nil && (before.Provider != g.Provider || before.Namespace != g.Namespace) {
		if err = h.d.Store.BumpGeneration(ctx, ref); err != nil {
			return nil, fmt.Errorf("record Octopus gateway %s: %w", ref.Resource, err)
		}
	}
	// On a first deploy the gateway has no binding yet, so d has no
	// connection and the found the Syncer needs doesn't matter here: no
	// manual route can exist before the gateway's row, and registrations
	// happen at runtime, so rendering without one leaves nothing out.
	d, _, _, err := h.d.Syncer.desired(ctx, g)
	if err != nil {
		return nil, fmt.Errorf("work out what Octopus gateway %s serves: %w", ref.Resource, err)
	}
	cfg, err := RenderConfig(d)
	if err != nil {
		return nil, fmt.Errorf("render Octopus gateway %s's config: %w", ref.Resource, err)
	}
	return map[string][]byte{recipes.OctopusConfigPath: cfg}, nil
}

// Ready implements deployments.CatalogHooks. Once the gateway answers it
// pushes the routes and upstreams over the deploy's own channel, with the
// deploy's own secrets. Octopus not taking all of it doesn't fail the
// deploy: it's logged and the reconcile pushes it again within 30
// seconds. On a first deploy the push always waits for the reconcile,
// since the gateway's binding is written after Ready. Only Loomplane failing on
// its side, such as reading the store or the secrets, is an error.
func (h *Hooks) Ready(ctx context.Context, run deployments.ComponentRun) error {
	if run.Component.Name != recipes.OctopusGateway {
		return nil
	}
	open := func(ctx context.Context, _ GatewayRef) (*deployments.ComponentChannel, error) {
		if run.Channel == nil {
			return nil, errors.New("the deploy has no channel to the gateway")
		}
		return run.Channel(ctx, recipes.OctopusGateway, adminPort)
	}
	secret := func(_ context.Context, _ GatewayRef, name string) ([]byte, error) {
		v := run.Secrets[name]
		if len(v) == 0 {
			return nil, fmt.Errorf("the deploy has no %s secret", name)
		}
		return v, nil
	}
	// The deploy job's context has no deadline, and a gateway lease only
	// outlives a sync bounded like the others (see syncLease).
	ctx, cancel := context.WithTimeout(ctx, readySyncTimeout)
	defer cancel()
	res, err := h.d.Syncer.SyncWith(ctx, runRef(run), open, secret)
	if err == nil {
		return nil
	}
	if errors.Is(err, errLoomplaneSide) {
		return fmt.Errorf("push Octopus gateway %s's routes: %w", run.Resource, err)
	}
	h.d.Syncer.warn(fmt.Sprintf("Octopus gateway %s is up, and its routes are %s: %s", run.Resource, res.State, res.Message), err)
	return nil
}

// Removed implements deployments.CatalogHooks. It ends the admin sessions
// open on the gateway, then forgets the gateway and its manual routes. A
// session left open only warns, as the owned targets' own ending of them
// does: nothing it reaches is there any more, and it expires on its own.
// Domain routes to the gateway keep it from being removed before this
// (ValidateRuntimeRemoval).
func (h *Hooks) Removed(ctx context.Context, res deployments.ComposedResource) error {
	if h.d.Revoker != nil {
		if err := h.d.Revoker.RevokeResource(ctx, res.OrgID, res.EnvironmentID, res.Resource); err != nil {
			h.d.Syncer.warn(fmt.Sprintf("the admin sessions open on Octopus gateway %s couldn't be ended, and expire on their own once idle", res.Resource), err)
		}
	}
	ref := GatewayRef{OrgID: res.OrgID, ProjectID: res.ProjectID, EnvironmentID: res.EnvironmentID, Resource: res.Resource}
	if err := h.d.Store.DeleteGateway(ctx, ref); err != nil {
		return fmt.Errorf("forget Octopus gateway %s: %w", res.Resource, err)
	}
	return nil
}

// Released implements deployments.ReleaseObserver. Each gateway of the
// release's environment that the service feeds gets a new generation and
// a sync: one the service opts in to over FARP, one with a manual route to
// it, and one it's registered with, which is how a removed or opted-out
// service is deregistered. Every affected gateway gets its generation
// before any sync runs, so one sync that hangs can't keep the others from
// being marked for the reconcile. The syncs share 10 seconds; one that
// doesn't get through leaves the generation for the reconcile. It returns
// the first error.
func (h *Hooks) Released(ctx context.Context, r deployments.Release) error {
	b := r.Binding
	if b == nil || b.Subject != planning.ServiceSubject {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, h.releaseTimeout)
	defer cancel()
	gateways, err := h.d.Store.GatewaysIn(ctx, r.OrgID, r.EnvironmentID)
	if err != nil {
		return fmt.Errorf("list the Octopus gateways: %w", err)
	}
	var cfg planning.ServiceConfig
	if len(b.Config) > 0 && json.Unmarshal(b.Config, &cfg) != nil {
		cfg = planning.ServiceConfig{}
	}
	var first error
	var bumped []GatewayRef
	keep := func(err error) {
		if first == nil {
			first = err
		}
	}
	for _, g := range gateways {
		affected, err := h.affects(ctx, g, b.Name, cfg)
		if err != nil {
			keep(err)
			continue
		}
		if !affected {
			continue
		}
		if err := h.d.Store.BumpGeneration(ctx, g.GatewayRef); err != nil {
			if !errors.Is(err, ErrNotFound) {
				keep(fmt.Errorf("mark Octopus gateway %s for a sync: %w", g.Resource, err))
			}
			continue
		}
		bumped = append(bumped, g.GatewayRef)
	}
	for _, ref := range bumped {
		if _, err := h.d.Syncer.Sync(ctx, ref); err != nil {
			keep(err)
		}
	}
	return first
}

// affects reports whether the service is one the gateway serves.
func (h *Hooks) affects(ctx context.Context, g *Gateway, service string, cfg planning.ServiceConfig) (bool, error) {
	if cfg.Farp != nil && cfg.Farp.Gateway == g.Resource {
		return true, nil
	}
	if _, ok := g.State.Registrations[service]; ok {
		return true, nil
	}
	routes, err := h.d.Store.Routes(ctx, g.GatewayRef)
	if err != nil {
		return false, fmt.Errorf("read Octopus gateway %s's routes: %w", g.Resource, err)
	}
	return slices.ContainsFunc(routes, func(r *Route) bool { return r != nil && r.Service == service }), nil
}
