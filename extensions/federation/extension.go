// Package federation wires the destination management service into Forge.
// Public product routes are added with the federation dashboard milestone.
package federation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"

	connectionsext "github.com/xraph/loomplane/extensions/connections"
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
	"github.com/xraph/loomplane/internal/workloadidentity"
)

// Name identifies this extension.
const Name = "loomplane-federation"

// Extension provides destination management and an explicitly configured agent listener.
type Extension struct {
	*forge.BaseExtension
	cfg     config.Config
	runtime *runtimeapi.Runtime
	cleanup *workloadidentity.CleanupRuntime
}

// New constructs the extension with explicit, default-off management settings.
func New(cfg config.Config) *Extension {
	return &Extension{BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Federation destinations"), cfg: cfg}
}

// Dependencies puts authentication before destination management.
func (e *Extension) Dependencies() []string {
	return []string{identity.Name, realtimeext.Name, connectionsext.Name}
}

// Register provides the scoped service over the shared database and audit trail.
func (e *Extension) Register(app forge.App) error {
	if err := e.cfg.Federation.Validate(); err != nil {
		return err
	}
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
	if e.cfg.Federation.Identity.Enabled {
		e.cleanup, err = workloadidentity.NewCleanupRuntime(e.cfg.Federation.Identity, workloadidentity.NewEnrollmentStore(pg, e.cfg.Federation.Identity.TrustDomain), func(result workloadidentity.CleanupResult, sweepErr error) {
			if sweepErr != nil && !errors.Is(sweepErr, context.Canceled) {
				e.Logger().Warn("federation enrollment cleanup incomplete", forge.F("org_id", e.cfg.Federation.Identity.OrganizationID), forge.F("attempted", result.Attempted), forge.F("deleted", result.Deleted), forge.F("failed", result.Failed), forge.F("error", sweepErr.Error()))
			}
		})
		if err != nil {
			return err
		}
		credentials, injectErr := forge.InjectType[*connections.Service](c)
		if injectErr != nil {
			return fmt.Errorf("federation installation credentials: %w", injectErr)
		}
		installations := domain.NewInstallationService(svc, domain.NewPgStore(pg), domain.VerifyKubernetesInstallation(credentials, e.cleanup, e.cfg.Federation.Identity.TrustDomain, e.cfg.Federation.Identity.OrganizationID))
		if provideErr := forge.ProvideValue(c, installations); provideErr != nil {
			return provideErr
		}
		enrollment := domain.NewEnrollmentService(svc, workloadidentity.NewEnrollmentStore(pg, e.cfg.Federation.Identity.TrustDomain), installations.Resolve, e.cleanup)
		if provideErr := forge.ProvideValue(c, enrollment); provideErr != nil {
			return provideErr
		}
	}
	if e.cfg.Federation.Runtime.Enabled {
		bus, injectErr := forge.InjectType[realtime.WakeSource](c)
		if injectErr != nil {
			return fmt.Errorf("federation runtime wake source: %w", injectErr)
		}
		e.runtime, err = runtimeapi.NewRuntime(e.cfg.Federation.Runtime, pg, bus)
		if err != nil {
			return err
		}
	}
	if e.cfg.Federation.Launch.Enabled {
		installations, injectErr := forge.InjectType[*domain.InstallationService](c)
		if injectErr != nil {
			return injectErr
		}
		credentials, injectErr := forge.InjectType[*connections.Service](c)
		if injectErr != nil {
			return injectErr
		}
		launch := domain.NewLaunchService(installations, workloadidentity.NewEnrollmentStore(pg, e.cfg.Federation.Identity.TrustDomain), credentials, e.runtime.PublicSigningKey, e.cfg.Federation.Launch, e.cfg.Federation.Identity.OrganizationID, domain.NewPgStore(pg))
		if provideErr := forge.ProvideValue(c, launch); provideErr != nil {
			return provideErr
		}
		enrollmentStore := workloadidentity.NewEnrollmentStore(pg, e.cfg.Federation.Identity.TrustDomain)
		retirement := domain.NewLaunchRetirementService(launch, func(ctx context.Context, org, id string) error {
			return enrollmentStore.Revoke(ctx, org, id, e.cleanup)
		})
		if provideErr := forge.ProvideValue(c, retirement); provideErr != nil {
			return provideErr
		}
		preparation := domain.NewPreparationService(launch, domain.NewPgStore(pg))
		if provideErr := forge.ProvideValue(c, preparation); provideErr != nil {
			return provideErr
		}
		preparationRetirement := domain.NewPreparationRetirementService(preparation, retirement, func(ctx context.Context, org, destination, id string) error {
			return enrollmentStore.RevokePreparation(ctx, org, destination, id, e.cleanup)
		})
		if provideErr := forge.ProvideValue(c, preparationRetirement); provideErr != nil {
			return provideErr
		}
	}
	return nil
}

// Start opens the agent listener after shared database migrations and bus startup.
func (e *Extension) Start(ctx context.Context) error {
	if e.cleanup != nil {
		if err := e.cleanup.Start(ctx); err != nil {
			return err
		}
	}
	if e.runtime != nil {
		if err := e.runtime.Start(ctx); err != nil {
			if e.cleanup != nil {
				stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				return errors.Join(err, e.cleanup.Stop(stop))
			}
			return err
		}
	}
	e.MarkStarted()
	return nil
}

func (e *Extension) Stop(ctx context.Context) error {
	defer e.MarkStopped()
	var err error
	if e.runtime != nil {
		err = e.runtime.Stop(ctx)
	}
	if e.cleanup != nil {
		err = errors.Join(err, e.cleanup.Stop(ctx))
	}
	return err
}

func (e *Extension) Health(ctx context.Context) error {
	var err error
	if e.cleanup != nil {
		err = e.cleanup.Health(ctx)
	}
	if e.runtime != nil {
		err = errors.Join(err, e.runtime.Health(ctx))
	}
	return errors.Join(err, e.BaseExtension.Health(ctx))
}
