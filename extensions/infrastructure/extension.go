// Package infrastructure is the Forge extension that connects Loomplane to
// Ctrl Plane.
//
// Ctrl Plane itself is registered through its own Forge extension (see
// internal/platform), on Loomplane's Postgres, with its HTTP routes off:
// Ctrl Plane's handlers check the tenant but never call the authorization
// hook, so Loomplane reaches Ctrl Plane only through its own API, where
// Warden decides. This extension binds Ctrl Plane's auth provider to
// authsome and Warden, refuses to run if Ctrl Plane fell back to its
// in-memory store, and reports what Ctrl Plane can deploy to.
package infrastructure

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	authsome "github.com/xraph/authsome"
	"github.com/xraph/ctrlplane/app"
	cpext "github.com/xraph/ctrlplane/extension"
	"github.com/xraph/ctrlplane/provider"
	cppg "github.com/xraph/ctrlplane/store/postgres"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/clouds/cloudrun"
	"github.com/xraph/loomplane/internal/clouds/ecs"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/security"
)

// Name is the extension's name.
const Name = "loomplane-infrastructure"

// Extension binds Ctrl Plane to Loomplane's identity.
type Extension struct {
	*forge.BaseExtension
	auth      *infrastructure.AuthProvider
	ctrlplane func() *app.CtrlPlane
	cp        *app.CtrlPlane
	authz     *security.Authorizer
}

// New returns the extension. auth is the provider Ctrl Plane's extension
// was built with; this extension binds it. ctrlplane returns the Ctrl Plane
// the ctrlplane extension built (cpext.Extension.CtrlPlane). It's taken
// from the extension rather than DI because vessel would start Ctrl Plane
// on first resolve, before the migration pass.
func New(auth *infrastructure.AuthProvider, ctrlplane func() *app.CtrlPlane) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Ctrl Plane integration"),
		auth:          auth,
		ctrlplane:     ctrlplane,
	}
}

// Dependencies implements forge.Extension.
func (e *Extension) Dependencies() []string {
	return []string{identity.Name, cpext.ExtensionName}
}

// Register binds the auth provider and checks Ctrl Plane's store.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	c := fapp.Container()
	if e.cp = e.ctrlplane(); e.cp == nil {
		return errors.New("infrastructure needs the ctrlplane extension to have registered")
	}
	// Ctrl Plane falls back to an in-memory store, with only a warning, when
	// it can't find the database. Infrastructure state that vanishes on
	// restart is worse than not starting.
	if _, ok := e.cp.Store().(*cppg.Store); !ok {
		return fmt.Errorf("ctrlplane is using a %T store, not Postgres; it must register after the grove extension", e.cp.Store())
	}
	var err error
	if e.authz, err = forge.InjectType[*security.Authorizer](c); err != nil {
		return fmt.Errorf("infrastructure needs the identity extension: %w", err)
	}
	engine, err := forge.InjectType[*authsome.Engine](c)
	if err != nil {
		return fmt.Errorf("infrastructure needs the identity extension: %w", err)
	}
	e.auth.Bind(sessionResolver(engine), e.authz)
	guard, err := forge.InjectType[identity.Guard](c)
	if err != nil {
		return fmt.Errorf("infrastructure needs the identity extension: %w", err)
	}
	g := fapp.Router().Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Infrastructure"))
	return g.GET("/infrastructure", e.status,
		forge.WithName("infrastructure.get"),
		forge.WithSummary("What Ctrl Plane can deploy to"),
		forge.WithResponseSchema(http.StatusOK, "Infrastructure status", InfrastructureStatus{}),
	)
}

// Start has nothing to start; Ctrl Plane's extension runs Ctrl Plane.
func (e *Extension) Start(context.Context) error {
	e.MarkStarted()
	return nil
}

func sessionResolver(engine *authsome.Engine) infrastructure.SessionResolver {
	return func(ctx context.Context, token string) (string, string, error) {
		sess, err := engine.ResolveSessionByToken(ctx, token)
		if err != nil {
			return "", "", err
		}
		if sess == nil {
			return "", "", errors.New("no session")
		}
		return sess.UserID.String(), sess.OrgID.String(), nil
	}
}

// InfrastructureStatus is what GET /api/v1/infrastructure reports.
//
// The stutter is deliberate: the type's name is its OpenAPI component name,
// and so the generated client's type, where a bare "Status" says nothing.
type InfrastructureStatus struct { //nolint:revive // see above
	// Engine names the lifecycle engine: Ctrl Plane.
	Engine string `json:"engine"`
	// Store is Ctrl Plane's persistence backend.
	Store string `json:"store"`
	// Providers are the deployment providers registered with Ctrl Plane.
	Providers []ProviderStatus `json:"providers"`
}

// ProviderStatus is one registered provider.
type ProviderStatus struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Region       string   `json:"region"`
	Capabilities []string `json:"capabilities"`
}

func (e *Extension) status(c forge.Context) error {
	ctx := c.Request().Context()
	p, ok := security.PrincipalFrom(ctx)
	if !ok {
		return forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	if err := e.authz.Require(ctx, p, security.InfrastructureRead, ""); err != nil {
		if status := security.StatusFor(err); status != http.StatusInternalServerError {
			return forge.NewHTTPError(status, err.Error())
		}
		return err
	}
	return c.JSON(http.StatusOK, InfrastructureStatus{Engine: "ctrlplane", Store: "postgres", Providers: providers(e.cp.Providers())})
}

// providers lists the registered providers every organization shares.
// Cloud Run providers are each one organization's connection, keyed by
// its ID, so they're left out: an organization sees its own under
// Integrations.
func providers(reg *provider.Registry) []ProviderStatus {
	all := reg.All()
	out := make([]ProviderStatus, 0, len(all))
	for name, p := range all {
		if strings.HasPrefix(name, cloudrun.KeyPrefix) || strings.HasPrefix(name, ecs.KeyPrefix) {
			continue
		}
		info := p.Info()
		caps := make([]string, 0, len(p.Capabilities()))
		for _, c := range p.Capabilities() {
			caps = append(caps, string(c))
		}
		sort.Strings(caps)
		out = append(out, ProviderStatus{Name: name, Version: info.Version, Region: info.Region, Capabilities: caps})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
