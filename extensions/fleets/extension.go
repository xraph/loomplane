// Package fleets serves project-scoped Fleet metadata and operation reads.
package fleets

import (
	"context"
	"errors"
	"net/http"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"

	environmentsext "github.com/xraph/loomplane/extensions/environments"
	"github.com/xraph/loomplane/extensions/identity"
	projectsext "github.com/xraph/loomplane/extensions/projects"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/environments"
	domain "github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

const Name = "loomplane-fleets"

type readService interface {
	GetProjectFleet(context.Context, security.Principal, string) (*fleet.Fleet, error)
	GetFleetInstance(context.Context, security.Principal, string, string) (*fleet.Instance, error)
	ListInstances(context.Context, security.Principal, string, fleet.ListInstancesInput) (*fleet.InstancePage, error)
	GetFleetOperation(context.Context, security.Principal, string, string) (*fleet.Operation, error)
	GetRelease(context.Context, security.Principal, string, string) (*fleet.TemplateRelease, error)
}

// Extension provides one shared domain service to Fleet API and worker adapters.
type Extension struct {
	*forge.BaseExtension
	cfg config.Config
	svc readService
}

func New(cfg config.Config) *Extension {
	return &Extension{BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Instance fleet operations"), cfg: cfg}
}
func (e *Extension) Dependencies() []string {
	return []string{identity.Name, projectsext.Name, environmentsext.Name}
}
func (e *Extension) Register(app forge.App) error {
	if err := e.BaseExtension.Register(app); err != nil {
		return err
	}
	c := app.Container()
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return err
	}
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return err
	}
	guard, err := forge.InjectType[identity.Guard](c)
	if err != nil {
		return err
	}
	keys, err := forge.InjectType[security.PrincipalVerifier](c)
	if err != nil {
		return err
	}
	secrets, err := forge.InjectType[*environments.Service](c)
	if err != nil {
		return err
	}
	audit, err := forge.InjectType[*chronicle.Chronicle](c)
	if err != nil {
		return err
	}
	pg := pgdriver.Unwrap(db)
	svc := domain.NewService(domain.Deps{Store: domain.NewPgStore(pg), Projects: projects.NewPgStore(pg), Authz: authz, Keys: keys, Secrets: secrets, Audit: projectsext.NewAuditor(audit, e.cfg.Identity.AppID, e.Logger()), AdoptionInventory: lazyAdoptionInventory{container: c}})
	e.svc = svc
	if provideErr := forge.ProvideValue(c, svc); provideErr != nil {
		return provideErr
	}
	return e.routes(app.Router(), guard)
}
func (e *Extension) Start(context.Context) error { e.MarkStarted(); return nil }
func (e *Extension) Stop(context.Context) error  { e.MarkStopped(); return nil }

// Planning registers inventory after the Fleet service. Resolve it at review time.
type lazyAdoptionInventory struct {
	container forge.Container
}

func (r lazyAdoptionInventory) InspectAdoption(ctx context.Context, p security.Principal, scope domain.FleetScope, in fleet.AdoptionInput) (domain.AdoptionInventorySnapshot, error) {
	resources, err := forge.InjectType[*providerresources.Service](r.container)
	if err != nil || resources == nil {
		return domain.AdoptionInventorySnapshot{}, domain.ErrAdoptionInventory
	}
	return orchestration.NewFleetAdoptionInventory(resources, nil).InspectAdoption(ctx, p, scope, in)
}

func fail(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, projects.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, "Fleet resource not found")
	case errors.Is(err, domain.ErrInvalid), errors.Is(err, fleet.ErrTemplate):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, "Invalid fleet request")
	case errors.Is(err, domain.ErrCapacity):
		return forge.NewHTTPError(http.StatusTooManyRequests, "Fleet admission limit reached")
	case errors.Is(err, domain.ErrConflict):
		return forge.NewHTTPError(http.StatusConflict, "Fleet state changed; reload before retrying")
	}
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, http.StatusText(status))
	}
	return forge.NewHTTPError(http.StatusInternalServerError, "Could not complete the fleet request")
}
