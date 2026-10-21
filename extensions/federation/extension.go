// Package federation wires the destination management service into Forge.
// Public product routes are added with the federation dashboard milestone.
package federation

import (
	"context"
	"fmt"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/extensions/identity"
	projectsext "github.com/xraph/loomplane/extensions/projects"
	realtimeext "github.com/xraph/loomplane/extensions/realtime"
	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	domain "github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/runtimeapi"
	"github.com/xraph/loomplane/internal/security"
)

// Name identifies this extension.
const Name = "loomplane-federation"

// Extension provides destination management and an explicitly configured agent listener.
type Extension struct {
	*forge.BaseExtension
	cfg     config.Config
	runtime *runtimeapi.Runtime
}

// New constructs the extension with explicit, default-off management settings.
func New(cfg config.Config) *Extension {
	return &Extension{BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Federation destinations"), cfg: cfg}
}

// Dependencies puts authentication before destination management.
func (e *Extension) Dependencies() []string { return []string{identity.Name, realtimeext.Name} }

// Register provides the scoped service over the shared database and audit trail.
func (e *Extension) Register(app forge.App) error {
	if err := e.BaseExtension.Register(app); err != nil {
		return err
	}
	c := app.Container()
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return fmt.Errorf("federation database: %w", err)
	}
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return fmt.Errorf("federation authorization: %w", err)
	}
	audit, err := forge.InjectType[*chronicle.Chronicle](c)
	if err != nil {
		return fmt.Errorf("federation audit: %w", err)
	}
	pg := pgdriver.Unwrap(db)
	svc := domain.NewService(domain.NewPgStore(pg), connections.NewPgStore(pg), projects.NewPgStore(pg), authz, projectsext.NewAuditor(audit, e.cfg.Identity.AppID, e.Logger()), e.cfg.Federation.Enabled)
	if err = forge.ProvideValue(c, svc); err != nil {
		return fmt.Errorf("provide federation service: %w", err)
	}
	if err = forge.ProvideValue(c, domain.NewAccessService(svc, access.NewPolicyStore(pg))); err != nil {
		return fmt.Errorf("provide federation access service: %w", err)
	}
	if e.cfg.Federation.Runtime.Enabled {
		if validationErr := e.cfg.Federation.Validate(); validationErr != nil {
			return validationErr
		}
		bus, injectErr := forge.InjectType[realtime.WakeSource](c)
		if injectErr != nil {
			return fmt.Errorf("federation runtime wake source: %w", injectErr)
		}
		e.runtime, err = runtimeapi.NewRuntime(e.cfg.Federation.Runtime, pg, bus)
		if err != nil {
			return err
		}
	}
	return nil
}

// Start opens the agent listener after shared database migrations and bus startup.
func (e *Extension) Start(ctx context.Context) error {
	if e.runtime != nil {
		if err := e.runtime.Start(ctx); err != nil {
			return err
		}
	}
	e.MarkStarted()
	return nil
}

func (e *Extension) Stop(ctx context.Context) error {
	defer e.MarkStopped()
	if e.runtime != nil {
		return e.runtime.Stop(ctx)
	}
	return nil
}

func (e *Extension) Health(ctx context.Context) error {
	if e.runtime != nil {
		return e.runtime.Health(ctx)
	}
	return e.BaseExtension.Health(ctx)
}
