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
	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	domain "github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// Name identifies this extension.
const Name = "loomplane-federation"

// Extension provides destination management without activating a data plane.
type Extension struct {
	*forge.BaseExtension
	cfg config.Config
}

// New constructs the extension with explicit, default-off management settings.
func New(cfg config.Config) *Extension {
	return &Extension{BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Federation destinations"), cfg: cfg}
}

// Dependencies puts authentication before destination management.
func (e *Extension) Dependencies() []string { return []string{identity.Name} }

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
	return nil
}

// Start marks the service ready; there is no background provider mutation.
func (e *Extension) Start(context.Context) error { e.MarkStarted(); return nil }
