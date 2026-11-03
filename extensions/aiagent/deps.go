package aiagent

import (
	"errors"
	"fmt"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"

	projectsext "github.com/xraph/loomplane/extensions/projects"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
)

// fillDeps gives the agent's operations Loomplane's services, from the
// container. Every one of them goes through its service's own checks.
func (e *Extension) fillDeps(c forge.Container, projectSvc *projects.Service) error {
	var errs []error
	resolve := func(name string, fn func() error) {
		if err := fn(); err != nil {
			errs = append(errs, fmt.Errorf("the agent's operations need %s: %w", name, err))
		}
	}
	d := e.deps
	d.Projects = projectSvc
	resolve("the environments service", func() (err error) { d.Environments, err = forge.InjectType[*environments.Service](c); return })
	resolve("the discovery service", func() (err error) { d.Discovery, err = forge.InjectType[*discovery.Service](c); return })
	resolve("the GitHub connections", func() (err error) { d.GitHub, err = forge.InjectType[*repositories.Connections](c); return })
	resolve("the manifests service", func() (err error) { d.Manifests, err = forge.InjectType[*manifests.Service](c); return })
	resolve("the planning service", func() (err error) { d.Plans, err = forge.InjectType[*planning.Service](c); return })
	resolve("the deployments service", func() (err error) { d.Deployments, err = forge.InjectType[*deployments.Service](c); return })
	resolve("the realtime store", func() (err error) { d.Logs, err = forge.InjectType[*realtime.PgStore](c); return })
	resolve("the authorizer", func() (err error) { d.Authz, err = forge.InjectType[*security.Authorizer](c); return })
	resolve("the audit log", func() error {
		audit, err := forge.InjectType[*chronicle.Chronicle](c)
		if err == nil {
			d.Audit = projectsext.NewAuditor(audit, e.cfg.Identity.AppID, e.Logger())
		}
		return err
	})
	resolve("the release health reader", func() (err error) { d.Health, err = forge.InjectType[releasehealth.Reader](c); return })
	resolve("the planning store", func() (err error) { d.Bindings, err = forge.InjectType[*planning.PgStore](c); return })
	resolve("the release watches", func() (err error) { d.Watches, err = forge.InjectType[*releasewatch.Service](c); return })
	resolve("the teams service", func() (err error) { d.Teams, err = forge.InjectType[*teams.Service](c); return })
	d.Safety = e.scanner
	return errors.Join(errs...)
}
