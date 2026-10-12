// Package projects is the Forge extension for Loomplane's project API.
//
// It wires internal/projects to its Postgres store, Warden (through the
// identity extension's Authorizer), Chronicle for the audit trail, and
// Dispatch for the job that initializes a new project.
package projects

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/xraph/chronicle"
	"github.com/xraph/dispatch/engine"
	dispatchext "github.com/xraph/dispatch/extension"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// Name is the extension's name.
const Name = "loomplane-projects"

// Extension serves the project API.
type Extension struct {
	*forge.BaseExtension
	cfg      config.Config
	engineOf func() *engine.Engine
	svc      *projects.Service
}

// New returns the projects extension. engineOf returns the dispatch
// extension's engine (dispatchext.Extension.Engine).
//
// The engine comes from the extension rather than from DI on purpose: the
// dispatch extension provides it to DI lazily, and vessel starts a lazily
// provided service the first time anything resolves it. Resolving it while
// extensions register would start Dispatch's workers before the migration
// pass has created their tables.
func New(cfg config.Config, engineOf func() *engine.Engine) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Projects and environments"),
		cfg:           cfg,
		engineOf:      engineOf,
	}
}

// Dependencies implements forge.Extension. Dispatch must have registered,
// and so built its engine, before this extension registers its job.
func (e *Extension) Dependencies() []string {
	return []string{identity.Name, dispatchext.ExtensionName}
}

// Register builds the service, registers the initialization job with
// Dispatch and mounts the routes. Dispatch only starts working its queue in
// Start, after every extension has registered, so no job can arrive before
// its handler exists.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	c := fapp.Container()
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return fmt.Errorf("projects needs the grove extension: %w", err)
	}
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return fmt.Errorf("projects needs the identity extension: %w", err)
	}
	guard, err := forge.InjectType[identity.Guard](c)
	if err != nil {
		return fmt.Errorf("projects needs the identity extension: %w", err)
	}
	audit, err := forge.InjectType[*chronicle.Chronicle](c)
	if err != nil {
		return fmt.Errorf("projects needs the audit trail: %w", err)
	}
	eng := e.engineOf()
	if eng == nil {
		return errors.New("projects needs the dispatch extension to have registered")
	}
	store := projects.NewPgStore(pgdriver.Unwrap(db))
	e.svc = projects.NewService(store, authz, orchestration.NewEnqueuer(eng), NewAuditor(audit, e.cfg.Identity.AppID, e.Logger()))
	engine.Register(eng, orchestration.ProjectInitJob(e.svc))
	if err := forge.ProvideValue(c, e.svc); err != nil {
		return fmt.Errorf("provide project service: %w", err)
	}
	return e.routes(fapp.Router(), guard)
}

// Start has nothing to start: the routes and the job are registered.
func (e *Extension) Start(context.Context) error {
	e.MarkStarted()
	return nil
}

// fail maps a service error to its HTTP response.
func fail(err error) error {
	switch {
	case errors.Is(err, projects.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, projects.ErrNotFound.Error())
	case errors.Is(err, projects.ErrEnvironmentNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, projects.ErrSlugTaken), errors.Is(err, projects.ErrEnvironmentExists),
		errors.Is(err, projects.ErrLastEnvironment):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, projects.ErrInvalid):
		return forge.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, err.Error())
	}
	return err
}
