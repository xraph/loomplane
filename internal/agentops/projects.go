package agentops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// errProjectScoped is create_project in a run about one project.
var errProjectScoped = fmt.Errorf("%w: this conversation is about one project; create projects from the workspace", errInvalid)

// errProjectNotFound is a project reference that matches nothing.
var errProjectNotFound = errors.New("project not found")

// findProject resolves a project by ID, slug or name, the way loomctl's
// FindProject does: an exact ID or slug wins, then a name ignoring case,
// which must match one project only. It reads through Projects.List, so
// it only ever sees what the principal may read in its organization, and
// it refuses a project outside the run's scope.
func (d *Deps) findProject(ctx context.Context, call Call, ref string) (*projects.Project, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("%w: name the project: its slug, name or ID", errInvalid)
	}
	list, err := d.Projects.List(ctx, call.Principal)
	if err != nil {
		return nil, err
	}
	var byName []*projects.Project
	var found *projects.Project
	for _, p := range list {
		if p.ID == ref || p.Slug == ref {
			found = p
			break
		}
		if strings.EqualFold(p.Name, ref) {
			byName = append(byName, p)
		}
	}
	if found == nil {
		switch len(byName) {
		case 0:
			return nil, fmt.Errorf("%w: no project is called %q", errProjectNotFound, ref)
		case 1:
			found = byName[0]
		default:
			return nil, fmt.Errorf("%w: more than one project is called %q; use its slug or ID", errInvalid, ref)
		}
	}
	if err := call.inScope(ctx, found.ID, ""); err != nil {
		return nil, err
	}
	return found, nil
}

// FindProject is findProject for operations served outside this package:
// a project by ID, slug or name, as the principal sees it and within the
// run's scope.
func (d *Deps) FindProject(ctx context.Context, call Call, ref string) (*projects.Project, error) {
	return d.findProject(ctx, call, ref)
}

// FindEnvironment is environment for operations served outside this
// package: one of the project's environments by name, within the run's
// scope.
func (d *Deps) FindEnvironment(ctx context.Context, call Call, projectID, name string) (*projects.Environment, error) {
	return d.environment(ctx, call, projectID, name)
}

// environment finds one of a project's environments by name.
func (d *Deps) environment(ctx context.Context, call Call, projectID, name string) (*projects.Environment, error) {
	envs, err := d.Projects.Environments(ctx, call.Principal, projectID)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	for _, e := range envs {
		if e.Name == name {
			if err := call.inScope(ctx, projectID, e.ID); err != nil {
				return nil, err
			}
			return e, nil
		}
	}
	return nil, fmt.Errorf("%w: the project has no environment called %q", projects.ErrEnvironmentNotFound, name)
}

// environments are the project's environments the run may see: one when
// it's scoped to an environment, else every one.
func (d *Deps) environments(ctx context.Context, call Call, projectID string) ([]*projects.Environment, error) {
	envs, err := d.Projects.Environments(ctx, call.Principal, projectID)
	if err != nil {
		return nil, err
	}
	if call.Scope.EnvironmentID == "" {
		return envs, nil
	}
	out := make([]*projects.Environment, 0, 1)
	for _, e := range envs {
		if e.ID == call.Scope.EnvironmentID {
			out = append(out, e)
		}
	}
	return out, nil
}

// Outputs.
type (
	whoamiOutput struct {
		UserID      string   `json:"user_id"`
		OrgID       string   `json:"org_id"`
		OnBehalfOf  string   `json:"on_behalf_of,omitempty"`
		Permissions []string `json:"permissions"`
	}
	projectsOutput struct {
		Projects []projects.Project `json:"projects"`
	}
	statusOutput struct {
		Environments []environmentStatus `json:"environments"`
	}
	environmentStatus struct {
		Environment string             `json:"environment"`
		Bindings    []planning.Binding `json:"bindings"`
	}
)

func registerProjects(r *Registry, d *Deps) {
	Register(r, def("whoami"), security.Permission{}, func(ctx context.Context, call Call, _ defs.NoInput) (whoamiOutput, error) {
		out := whoamiOutput{UserID: call.Principal.UserID, OrgID: call.Principal.OrgID, Permissions: []string{}}
		if person, ok := call.Principal.Person(); ok {
			out.OnBehalfOf = person.UserID
		}
		// What the run may do where it's scoped: a run about one project
		// counts the grants a person's teams have there.
		for _, perm := range security.AllPermissions() {
			ok, err := d.Authz.AllowedOn(ctx, call.Principal, perm, call.Scope.ProjectID)
			if err != nil {
				return whoamiOutput{}, err
			}
			if ok {
				out.Permissions = append(out.Permissions, perm.Name())
			}
		}
		return out, nil
	})

	Register(r, def("list_projects"), security.ProjectRead, func(ctx context.Context, call Call, _ defs.NoInput) (projectsOutput, error) {
		list, err := d.Projects.List(ctx, call.Principal)
		if err != nil {
			return projectsOutput{}, err
		}
		out := projectsOutput{Projects: make([]projects.Project, 0, len(list))}
		for _, p := range list {
			if call.Scope.ProjectID == "" || p.ID == call.Scope.ProjectID {
				out.Projects = append(out.Projects, *p)
			}
		}
		return out, nil
	})

	Register(r, def("status"), security.DeploymentRead, func(ctx context.Context, call Call, in defs.ProjectInput) (statusOutput, error) {
		return d.status(ctx, call, in.Project, "", false)
	})

	Register(r, def("drift"), security.DeploymentRead, func(ctx context.Context, call Call, in defs.DriftInput) (statusOutput, error) {
		return d.status(ctx, call, in.Project, in.Environment, true)
	})

	Register(r, def("create_project"), security.ProjectCreate, func(ctx context.Context, call Call, in defs.CreateProjectInput) (*projects.Project, error) {
		if call.Scope.ProjectID != "" {
			return nil, errProjectScoped
		}
		// Repositories, not RepositoryURL: it takes owner/name as well as a
		// URL, which the definition promises.
		create := projects.CreateProjectInput{Name: in.Name}
		if repo := strings.TrimSpace(in.Repository); repo != "" {
			create.Repositories = []projects.RepositoryInput{{Repository: repo}}
		}
		p, err := d.Projects.Create(ctx, call.Principal, create)
		if err != nil {
			return nil, err
		}
		noteProject(ctx, p.ID)
		return p, nil
	})

	Register(r, def("create_environment"), security.EnvironmentManage, func(ctx context.Context, call Call, in defs.CreateEnvironmentInput) (*projects.Environment, error) {
		p, err := d.findProject(ctx, call, in.Project)
		if err != nil {
			return nil, err
		}
		return d.Projects.CreateEnvironment(ctx, call.Principal, p.ID, projects.CreateEnvironmentInput{Name: in.Name, Kind: in.Kind})
	})
}

// status is what runs in each of a project's environments, or in one
// (environment), and with drifted only the bindings that drifted.
func (d *Deps) status(ctx context.Context, call Call, ref, environment string, drifted bool) (statusOutput, error) {
	p, err := d.findProject(ctx, call, ref)
	if err != nil {
		return statusOutput{}, err
	}
	var envs []*projects.Environment
	if environment != "" {
		env, eerr := d.environment(ctx, call, p.ID, environment)
		if eerr != nil {
			return statusOutput{}, eerr
		}
		envs = []*projects.Environment{env}
	} else if envs, err = d.environments(ctx, call, p.ID); err != nil {
		return statusOutput{}, err
	}
	out := statusOutput{Environments: make([]environmentStatus, 0, len(envs))}
	for _, e := range envs {
		list, berr := d.Plans.Bindings(ctx, call.Principal, p.ID, e.Name)
		if berr != nil {
			return statusOutput{}, berr
		}
		st := environmentStatus{Environment: e.Name, Bindings: make([]planning.Binding, 0, len(list))}
		for _, b := range list {
			if drifted && b.Drift == "" {
				continue
			}
			st.Bindings = append(st.Bindings, d.quoteBinding(ctx, b))
		}
		out.Environments = append(out.Environments, st)
	}
	return out, nil
}
