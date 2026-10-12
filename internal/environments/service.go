package environments

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

const (
	maxKeyLength   = 255
	maxValueLength = 64 << 10
)

var keyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Authorizer decides whether a principal may act. *security.Authorizer is
// the real one.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// Projects reads the projects and environments variables hang off.
// *projects.PgStore is the real one.
type Projects interface {
	GetProject(ctx context.Context, orgID, id string) (*projects.Project, error)
	ListEnvironments(ctx context.Context, orgID, projectID string) ([]*projects.Environment, error)
}

// Services lists the services in a project's graph, the newest successful
// analysis of each of its repositories, by ID, with their names. A project never analyzed has none.
type Services interface {
	Services(ctx context.Context, orgID, projectID string) (map[string]string, error)
}

// Auditor records variable changes. It must never be given a value, and
// must not fail the change.
type Auditor interface {
	Record(ctx context.Context, action string, p security.Principal, v *Variable)
}

// Service is the variable API: every method checks the caller's role
// first.
type Service struct {
	store    Store
	secrets  Secrets
	projects Projects
	services Services
	authz    Authorizer
	audit    Auditor
	logf     func(msg string, err error)
	now      func() time.Time
}

// NewService returns a Service. audit may be nil; logf reports what a
// background sweep couldn't clean up, and may be nil.
func NewService(store Store, secrets Secrets, prj Projects, svcs Services, authz Authorizer, audit Auditor, logf func(string, error)) *Service {
	if logf == nil {
		logf = func(string, error) {}
	}
	return &Service{store: store, secrets: secrets, projects: prj, services: svcs, authz: authz, audit: audit, logf: logf, now: time.Now}
}

// CreateVariableInput is what a caller sends to set a variable.
type CreateVariableInput struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
	// Environment limits it to one environment, by name. Empty means
	// every environment.
	Environment string `json:"environment,omitempty"`
	// Service sets it for one service of the project, by its ID in the
	// project's graph. Only for a project's variables.
	Service string `json:"service,omitempty"`
}

// UpdateVariableInput changes a variable's value, and whether it's secret.
// A secret's value can't be read back, so changing one means sending it
// again.
type UpdateVariableInput struct {
	Value  string `json:"value"`
	Secret *bool  `json:"secret,omitempty"`
}

// ListOrganization returns the organization-wide variables.
func (s *Service) ListOrganization(ctx context.Context, p security.Principal) ([]*Variable, error) {
	if err := s.authz.Require(ctx, p, security.VariableRead, ""); err != nil {
		return nil, err
	}
	return s.store.ListOrganization(ctx, p.OrgID)
}

// ListProject returns a project's own variables, project-wide and per
// service.
func (s *Service) ListProject(ctx context.Context, p security.Principal, projectID string) ([]*Variable, error) {
	if err := s.authz.Require(ctx, p, security.VariableRead, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	return s.store.ListProject(ctx, p.OrgID, projectID)
}

// CreateOrganization sets an organization-wide variable, which every
// project inherits. Its environment may be any name: it applies in every
// project's environment of that name.
func (s *Service) CreateOrganization(ctx context.Context, p security.Principal, in CreateVariableInput) (*Variable, error) {
	if err := s.authz.Require(ctx, p, security.OrganizationVariableManage, ""); err != nil {
		return nil, err
	}
	if in.Service != "" {
		return nil, fmt.Errorf("%w: a service variable belongs to a project", ErrInvalid)
	}
	v, err := s.build(p, ScopeOrganization, "", in)
	if err != nil {
		return nil, err
	}
	if v.Environment != "" && !projects.ValidEnvironmentName(v.Environment) {
		return nil, fmt.Errorf("%w: %q isn't an environment name", ErrInvalid, v.Environment)
	}
	return s.create(ctx, p, v, in.Value)
}

// CreateProject sets a variable for a project, or for one of its
// services.
func (s *Service) CreateProject(ctx context.Context, p security.Principal, projectID string, in CreateVariableInput) (*Variable, error) {
	if err := s.authz.Require(ctx, p, security.VariableManage, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	scope := ScopeProject
	if in.Service = strings.TrimSpace(in.Service); in.Service != "" {
		scope = ScopeService
		known, err := s.services.Services(ctx, p.OrgID, projectID)
		if err != nil {
			return nil, err
		}
		if _, ok := known[in.Service]; !ok {
			return nil, fmt.Errorf("%w: %q isn't a service in the project's graph; service IDs look like svc:<alias>:<ecosystem>:<path>", ErrInvalid, in.Service)
		}
	}
	v, err := s.build(p, scope, projectID, in)
	if err != nil {
		return nil, err
	}
	if v.Environment != "" {
		env, err := s.environment(ctx, p.OrgID, projectID, v.Environment)
		if err != nil {
			return nil, err
		}
		v.environmentID = env.ID
	}
	return s.create(ctx, p, v, in.Value)
}

func (s *Service) build(p security.Principal, scope, projectID string, in CreateVariableInput) (*Variable, error) {
	key := strings.TrimSpace(in.Key)
	if len(key) > maxKeyLength || !keyPattern.MatchString(key) {
		return nil, fmt.Errorf("%w: a key is letters, digits and underscores, not starting with a digit", ErrInvalid)
	}
	if len(in.Value) > maxValueLength {
		return nil, fmt.Errorf("%w: a value can be at most 64 KiB", ErrInvalid)
	}
	id, err := typeid.Generate("var")
	if err != nil {
		return nil, fmt.Errorf("generate variable id: %w", err)
	}
	now := s.now().UTC()
	v := &Variable{
		ID: id.String(), OrgID: p.OrgID, Scope: scope, ProjectID: projectID, Service: in.Service,
		Key: key, Secret: in.Secret, Environment: strings.TrimSpace(in.Environment),
		CreatedBy: p.UserID, UpdatedBy: p.UserID, CreatedAt: now, UpdatedAt: now,
	}
	if !v.Secret {
		v.Value = in.Value
	}
	return v, nil
}

// create stores the variable, and its secret in Vault. The row goes in
// first so a secret is never stored for a variable that doesn't exist; if
// Vault then fails, the row is taken out again.
func (s *Service) create(ctx context.Context, p security.Principal, v *Variable, value string) (*Variable, error) {
	if err := s.store.CreateVariable(ctx, v); err != nil {
		return nil, err
	}
	if v.Secret {
		if err := s.secrets.Put(ctx, v.OrgID, v.ID, []byte(value)); err != nil {
			if derr := s.store.DeleteVariable(context.WithoutCancel(ctx), v.OrgID, v.ID); derr != nil {
				err = errors.Join(err, fmt.Errorf("remove the variable again: %w", derr))
			}
			return nil, err
		}
	}
	s.record(ctx, "variable.create", p, v)
	return v, nil
}

func (s *Service) environment(ctx context.Context, orgID, projectID, name string) (*projects.Environment, error) {
	envs, err := s.projects.ListEnvironments(ctx, orgID, projectID)
	if err != nil {
		return nil, err
	}
	for _, e := range envs {
		if e.Name == name {
			return e, nil
		}
	}
	return nil, fmt.Errorf("%w: the project has no environment named %q", ErrInvalid, name)
}

// manage checks the caller may change v: an organization-wide variable
// needs more than a project's.
func (s *Service) manage(ctx context.Context, p security.Principal, v *Variable) error {
	if v.Scope == ScopeOrganization {
		return s.authz.Require(ctx, p, security.OrganizationVariableManage, "")
	}
	return s.authz.Require(ctx, p, security.VariableManage, v.ProjectID)
}

// Update changes a variable's value, and whether it's secret.
func (s *Service) Update(ctx context.Context, p security.Principal, id string, in UpdateVariableInput) (*Variable, error) {
	if err := s.authz.Require(ctx, p, security.VariableRead, ""); err != nil {
		return nil, err
	}
	v, err := s.store.GetVariable(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	err = s.manage(ctx, p, v)
	if err != nil {
		return nil, err
	}
	if len(in.Value) > maxValueLength {
		return nil, fmt.Errorf("%w: a value can be at most 64 KiB", ErrInvalid)
	}
	wasSecret := v.Secret
	if in.Secret != nil {
		v.Secret = *in.Secret
	}
	if v.Secret {
		err = s.secrets.Put(ctx, v.OrgID, v.ID, []byte(in.Value))
		if err != nil {
			return nil, err
		}
		v.Value = ""
	} else {
		v.Value = in.Value
	}
	v.UpdatedBy, v.UpdatedAt = p.UserID, s.now().UTC()
	err = s.store.UpdateVariable(ctx, v)
	if err != nil {
		return nil, err
	}
	if wasSecret && !v.Secret {
		// No longer secret: the stored value is the plain one now.
		err = s.secrets.Delete(ctx, v.OrgID, v.ID)
		if err != nil {
			s.logf("delete a variable's old secret", err)
		}
	}
	s.record(ctx, "variable.update", p, v)
	return v, nil
}

// Delete removes a variable, and its secret.
func (s *Service) Delete(ctx context.Context, p security.Principal, id string) error {
	if err := s.authz.Require(ctx, p, security.VariableRead, ""); err != nil {
		return err
	}
	v, err := s.store.GetVariable(ctx, p.OrgID, id)
	if err != nil {
		return err
	}
	err = s.manage(ctx, p, v)
	if err != nil {
		return err
	}
	err = s.store.DeleteVariable(ctx, p.OrgID, id)
	if err != nil {
		return err
	}
	if v.Secret {
		err = s.secrets.Delete(ctx, v.OrgID, v.ID)
		if err != nil {
			// Unreachable without its variable; the next sweep removes it.
			s.logf("delete a variable's secret", err)
		}
	}
	s.record(ctx, "variable.delete", p, v)
	return nil
}

// ResolvedVariable is one key as a service sees it in an environment.
type ResolvedVariable struct {
	Key string `json:"key"`
	// Variable is the one that wins: the most specific layer setting Key.
	Variable Variable `json:"variable"`
	// Overrides are the less specific ones it shadows, most specific
	// first.
	Overrides []Variable `json:"overrides"`
}

// Resolution is every variable one service, or a project as a whole, gets
// in one environment.
type Resolution struct {
	ProjectID   string             `json:"project_id"`
	Environment string             `json:"environment"`
	Service     string             `json:"service,omitempty"`
	Variables   []ResolvedVariable `json:"variables"`
}

// Resolve works out the variables in a project's environment, for one of
// its services when service isn't empty.
func (s *Service) Resolve(ctx context.Context, p security.Principal, projectID, environment, service string) (*Resolution, error) {
	if err := s.authz.Require(ctx, p, security.VariableRead, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	env, err := s.environment(ctx, p.OrgID, projectID, environment)
	if err != nil {
		return nil, err
	}
	candidates, err := s.store.ForEnvironment(ctx, p.OrgID, projectID, env.ID, env.Name)
	if err != nil {
		return nil, err
	}
	return &Resolution{ProjectID: projectID, Environment: env.Name, Service: service, Variables: resolve(candidates, service)}, nil
}

// resolve layers candidates: per key, the most specific wins and the rest
// are what it overrides. Service variables count only for service.
func resolve(candidates []*Variable, service string) []ResolvedVariable {
	byKey := map[string][]*Variable{}
	for _, v := range candidates {
		if v.Scope == ScopeService && v.Service != service {
			continue
		}
		byKey[v.Key] = append(byKey[v.Key], v)
	}
	out := make([]ResolvedVariable, 0, len(byKey))
	for key, layers := range byKey {
		sort.SliceStable(layers, func(i, j int) bool { return layers[i].rank() > layers[j].rank() })
		rv := ResolvedVariable{Key: key, Variable: *layers[0], Overrides: make([]Variable, 0, len(layers)-1)}
		for _, v := range layers[1:] {
			rv.Overrides = append(rv.Overrides, *v)
		}
		out = append(out, rv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Sweep deletes the organization's secrets whose variables are gone. A
// project or environment deletion takes its variables with it in the
// database, which can't reach Vault; this is what does. It implements
// projects.Cleaner.
func (s *Service) Sweep(ctx context.Context, orgID string) {
	live, err := s.store.SecretIDs(ctx, orgID)
	if err != nil {
		s.logf("sweep variable secrets", err)
		return
	}
	stored, err := s.secrets.IDs(ctx, orgID)
	if err != nil {
		s.logf("sweep variable secrets", err)
		return
	}
	for _, id := range stored {
		if live[id] {
			continue
		}
		if err := s.secrets.Delete(ctx, orgID, id); err != nil {
			s.logf("sweep variable secrets", err)
		}
	}
}

func (s *Service) record(ctx context.Context, action string, p security.Principal, v *Variable) {
	if s.audit != nil {
		s.audit.Record(ctx, action, p, v)
	}
}

// Values returns what one service gets in an environment as plain values,
// secrets revealed, for a deployment to set on it. It's the only path that
// reveals a secret, and it checks no role: only Loomplane's deployment
// workflow calls it, for a plan someone was allowed to approve. Nothing it
// returns is logged or stored.
func (s *Service) Values(ctx context.Context, orgID, projectID, environment, service string) (map[string]string, error) {
	env, err := s.environment(ctx, orgID, projectID, environment)
	if err != nil {
		return nil, err
	}
	candidates, err := s.store.ForEnvironment(ctx, orgID, projectID, env.ID, env.Name)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range resolve(candidates, service) {
		v := r.Variable
		if !v.Secret {
			out[r.Key] = v.Value
			continue
		}
		value, err := s.secrets.Reveal(ctx, orgID, v.ID)
		if err != nil {
			return nil, fmt.Errorf("reveal %s: %w", r.Key, err)
		}
		out[r.Key] = string(value)
	}
	return out, nil
}
