// Package agentwatch turns what happens in a workspace into the agent's
// reports. Detectors read recorded events and current state and say which
// problems started and which went away. They call no model: only an
// investigation of a report does, and the watcher starts that.
package agentwatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/security"
)

// World is what detectors read. ServiceWorld is the real one: every call
// uses security.AgentPrincipal(orgID, nil).
type World interface {
	Deployment(ctx context.Context, orgID, id string) (*deployments.Deployment, error)
	Canary(ctx context.Context, orgID, deploymentID string) (*deployments.CanaryReport, error)
	// RecentDeployments returns up to n of the environment's deployments,
	// newest first.
	RecentDeployments(ctx context.Context, orgID, projectID, environmentID string, n int) ([]*deployments.Deployment, error)
	Plan(ctx context.Context, orgID, id string) (*planning.Plan, error)
	// Watch returns the deployment's release watch; releasewatch.ErrNotFound
	// passes through.
	Watch(ctx context.Context, orgID, deploymentID string) (*releasewatch.Watch, error)
	Binding(ctx context.Context, orgID, id string) (*planning.Binding, error)
	// ProductionBindings returns the bindings of every project's
	// production-kind environments.
	ProductionBindings(ctx context.Context, orgID string) ([]*planning.Binding, error)
	// RampingCanary returns the deployment whose canary holds the
	// environment, or "".
	RampingCanary(ctx context.Context, orgID, environmentID string) (string, error)
	// WatchingNow reports whether the environment's latest release watch
	// is still in progress.
	WatchingNow(ctx context.Context, orgID, projectID, environment string) (bool, error)
	Environment(ctx context.Context, orgID, projectID, environmentID string) (*projects.Environment, error)
	Project(ctx context.Context, orgID, id string) (*projects.Project, error)
	Analysis(ctx context.Context, orgID, id string) (*discovery.Analysis, error)
	// RecentAnalyses returns up to n of a source's analyses, newest first,
	// leaving out the pinned ones a promotion asked for.
	RecentAnalyses(ctx context.Context, orgID, projectID, sourceID string, n int) ([]*discovery.Analysis, error)
	// RepositoryFor returns the repository key a component is built from,
	// or "" when Loomplane can't tell (see PickRepository).
	RepositoryFor(ctx context.Context, orgID, projectID, component string) (string, error)
}

// ServiceWorld reads through Loomplane's services as the workspace's agent
// acting alone.
type ServiceWorld struct {
	Deployments *deployments.Service
	Plans       *planning.Service
	PlanStore   *planning.PgStore
	Watches     *releasewatch.Service
	Projects    *projects.Service
	Discovery   *discovery.Service
	Bindings    interface {
		GetBinding(ctx context.Context, orgID, id string) (*planning.Binding, error)
	}
}

var _ World = (*ServiceWorld)(nil)

// How far RecentDeployments looks.
const (
	deploymentPage     = 50
	maxDeploymentPages = 5
)

func agent(orgID string) security.Principal { return security.AgentPrincipal(orgID, nil) }

// Deployment implements World.
func (w *ServiceWorld) Deployment(ctx context.Context, orgID, id string) (*deployments.Deployment, error) {
	return w.Deployments.Get(ctx, agent(orgID), id)
}

// Canary implements World.
func (w *ServiceWorld) Canary(ctx context.Context, orgID, deploymentID string) (*deployments.CanaryReport, error) {
	return w.Deployments.Canary(ctx, agent(orgID), deploymentID)
}

// RecentDeployments implements World. Deployments are listed per project,
// so it pages the project's list and keeps the environment's.
func (w *ServiceWorld) RecentDeployments(ctx context.Context, orgID, projectID, environmentID string, n int) ([]*deployments.Deployment, error) {
	return recentInEnvironment(func(before page.Cursor) ([]*deployments.Deployment, page.Cursor, error) {
		return w.Deployments.List(ctx, agent(orgID), projectID, before, deploymentPage)
	}, environmentID, n)
}

// recentInEnvironment pages list, newest first, and keeps up to n of the
// environment's deployments, reading at most maxDeploymentPages pages.
func recentInEnvironment(list func(before page.Cursor) ([]*deployments.Deployment, page.Cursor, error), environmentID string, n int) ([]*deployments.Deployment, error) {
	var out []*deployments.Deployment
	var before page.Cursor
	for range maxDeploymentPages {
		rows, next, err := list(before)
		if err != nil {
			return nil, err
		}
		for _, d := range rows {
			if d.EnvironmentID != environmentID {
				continue
			}
			out = append(out, d)
			if len(out) == n {
				return out, nil
			}
		}
		if next.IsZero() {
			break
		}
		before = next
	}
	return out, nil
}

// Plan implements World.
func (w *ServiceWorld) Plan(ctx context.Context, orgID, id string) (*planning.Plan, error) {
	return w.Plans.Get(ctx, agent(orgID), id)
}

// Watch implements World.
func (w *ServiceWorld) Watch(ctx context.Context, orgID, deploymentID string) (*releasewatch.Watch, error) {
	return w.Watches.ForDeployment(ctx, agent(orgID), deploymentID)
}

// Binding implements World. Bindings are read from the store by the
// workspace, as the drift checker writes them.
func (w *ServiceWorld) Binding(ctx context.Context, orgID, id string) (*planning.Binding, error) {
	return w.Bindings.GetBinding(ctx, orgID, id)
}

// ProductionBindings implements World: every project's environments of
// kind production, and what runs in each.
func (w *ServiceWorld) ProductionBindings(ctx context.Context, orgID string) ([]*planning.Binding, error) {
	p := agent(orgID)
	list, err := w.Projects.List(ctx, p)
	if err != nil {
		return nil, err
	}
	var out []*planning.Binding
	for _, project := range list {
		envs, err := w.Projects.Environments(ctx, p, project.ID)
		if err != nil {
			return nil, err
		}
		for _, env := range envs {
			if env.Kind != projects.KindProduction {
				continue
			}
			bindings, err := w.Plans.Bindings(ctx, p, project.ID, env.Name)
			if err != nil {
				return nil, err
			}
			out = append(out, bindings...)
		}
	}
	return out, nil
}

// RampingCanary implements World.
func (w *ServiceWorld) RampingCanary(ctx context.Context, orgID, environmentID string) (string, error) {
	return w.PlanStore.RampingCanary(ctx, orgID, environmentID)
}

// WatchingNow implements World: the environment's latest watch is still
// watching, or rolling back what it found.
func (w *ServiceWorld) WatchingNow(ctx context.Context, orgID, projectID, environment string) (bool, error) {
	watch, err := w.Watches.Latest(ctx, agent(orgID), projectID, environment)
	if errors.Is(err, releasewatch.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return watch.Open(), nil
}

// Environment implements World. Projects list their environments, so it
// finds the one with the ID.
func (w *ServiceWorld) Environment(ctx context.Context, orgID, projectID, environmentID string) (*projects.Environment, error) {
	envs, err := w.Projects.Environments(ctx, agent(orgID), projectID)
	if err != nil {
		return nil, err
	}
	for _, env := range envs {
		if env.ID == environmentID {
			return env, nil
		}
	}
	return nil, projects.ErrEnvironmentNotFound
}

// Project implements World.
func (w *ServiceWorld) Project(ctx context.Context, orgID, id string) (*projects.Project, error) {
	return w.Projects.Get(ctx, agent(orgID), id)
}

// Analysis implements World.
func (w *ServiceWorld) Analysis(ctx context.Context, orgID, id string) (*discovery.Analysis, error) {
	return w.Discovery.Get(ctx, agent(orgID), id)
}

// RecentAnalyses implements World. Discovery lists a project's latest
// analyses, so it keeps the source's, leaving out pinned ones: those serve
// a promotion only and say nothing about the source's own analyses.
func (w *ServiceWorld) RecentAnalyses(ctx context.Context, orgID, projectID, sourceID string, n int) ([]*discovery.Analysis, error) {
	list, err := w.Discovery.List(ctx, agent(orgID), projectID)
	if err != nil {
		return nil, err
	}
	return sourceAnalyses(list, sourceID, n), nil
}

// sourceAnalyses keeps up to n of the source's unpinned analyses, in
// list's order.
func sourceAnalyses(list []*discovery.Analysis, sourceID string, n int) []*discovery.Analysis {
	out := make([]*discovery.Analysis, 0, n)
	for _, a := range list {
		if a.SourceID != sourceID || a.Pinned {
			continue
		}
		out = append(out, a)
		if len(out) == n {
			break
		}
	}
	return out
}

// RepositoryFor implements World. It reads the project's sources and, for
// a service, its bindings in each environment, which say the repository
// alias it's built from.
func (w *ServiceWorld) RepositoryFor(ctx context.Context, orgID, projectID, component string) (string, error) {
	p := agent(orgID)
	sources, err := w.Discovery.Sources(ctx, p, projectID)
	if err != nil || len(sources) == 0 {
		return "", err
	}
	var bindings []*planning.Binding
	if _, ok := serviceName(component); ok {
		envs, err := w.Projects.Environments(ctx, p, projectID)
		if err != nil {
			return "", err
		}
		for _, env := range envs {
			list, err := w.Plans.Bindings(ctx, p, projectID, env.Name)
			if err != nil {
				return "", err
			}
			bindings = append(bindings, list...)
		}
	}
	return PickRepository(component, bindings, sources), nil
}

// PickRepository is RepositoryFor's rule. A component naming a service
// binding uses the source its ServiceConfig.Repo alias names. An empty
// alias, a resource, or "deployment" uses the project's only source when
// it has exactly one. Otherwise it's "". Keys may be GitLab or Bitbucket
// ones: delivery decides what it can open an issue on.
func PickRepository(component string, bindings []*planning.Binding, sources []*discovery.Source) string {
	alias := ""
	if name, ok := serviceName(component); ok {
		alias = repoAlias(bindings, name)
	}
	if alias != "" {
		for _, s := range sources {
			if s.Alias == alias {
				return s.Repository
			}
		}
		return ""
	}
	if len(sources) == 1 {
		return sources[0].Repository
	}
	return ""
}

// serviceName returns the service a component may name: "service:api" or
// a bare binding name. ok is false for "deployment" and any other action.
func serviceName(component string) (string, bool) {
	if component == "" || component == componentDeployment {
		return "", false
	}
	subject, name, found := strings.Cut(component, ":")
	if !found {
		return component, true
	}
	return name, subject == string(planning.ServiceSubject) && name != ""
}

// repoAlias is the Repo alias of the service binding called name, "" when
// there's none or it names no alias.
func repoAlias(bindings []*planning.Binding, name string) string {
	for _, b := range bindings {
		if b.Subject != planning.ServiceSubject || b.Name != name {
			continue
		}
		var cfg struct {
			Repo string `json:"repo"`
		}
		if json.Unmarshal(b.Config, &cfg) == nil && cfg.Repo != "" {
			return cfg.Repo
		}
	}
	return ""
}
