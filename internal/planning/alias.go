package planning

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// ErrAliasTaken is an alias another binding of the environment already has.
var ErrAliasTaken = errors.New("another service or resource in this environment already has that alias")

// maxAliasLength keeps an alias a DNS label, so it can name things later.
const maxAliasLength = 63

// aliasRE is a lowercase DNS label: a letter first, then letters, digits
// and single hyphens, never ending in one.
var aliasRE = regexp.MustCompile(`^[a-z]([a-z0-9]*(-[a-z0-9]+)*)?$`)

// ValidateAlias reports whether alias may name a binding. The empty alias
// is valid: it clears the one set.
func ValidateAlias(alias string) error {
	if alias == "" {
		return nil
	}
	if len(alias) > maxAliasLength {
		return fmt.Errorf("%w: an alias can be at most %d characters", ErrInvalid, maxAliasLength)
	}
	if !aliasRE.MatchString(alias) {
		return fmt.Errorf("%w: an alias is lowercase letters, digits and single hyphens, starting with a letter", ErrInvalid)
	}
	return nil
}

// SuggestAlias proposes a readable alias for what a binding runs, from
// the project's slug, the environment's name and the binding's name (a
// manifest service or resource key). The cloud names Loomplane makes, such
// as api-prod-ab12cd34 or lp-inst-01jx..., are built to be unique, not
// read. This one is for people: project, then name, then environment, as
// in shop-api-prod. A suggestion too long for an alias drops the project
// first, then loses its end. It returns "" when the name has nothing
// readable, and whatever it returns passes ValidateAlias. Services and
// resources are suggested alike for now.
func SuggestAlias(projectSlug, environment string, _ Subject, name string) string {
	label := projects.Slugify(name)
	if label == "" {
		return ""
	}
	env := projects.Slugify(environment)
	if short, ok := shortEnvironments[env]; ok {
		env = short
	}
	alias := joinAlias(projects.Slugify(projectSlug), label, env)
	if len(alias) > maxAliasLength {
		alias = joinAlias(label, env)
	}
	if len(alias) > maxAliasLength {
		alias = strings.TrimRight(alias[:maxAliasLength], "-")
	}
	return alias
}

// shortEnvironments are the environment names a suggestion shortens.
var shortEnvironments = map[string]string{"production": "prod", "staging": "stage", "development": "dev"}

// joinAlias joins the parts that aren't empty with hyphens, dropping
// whatever comes before the first letter, since an alias starts with one.
func joinAlias(parts ...string) string {
	return strings.TrimLeft(strings.Join(slices.DeleteFunc(parts, func(p string) bool { return p == "" }), "-"),
		"0123456789-")
}

// BindingAliasInput is the body of PATCH /api/v1/projects/:id/bindings/:binding.
type BindingAliasInput struct {
	// Alias is the name people see for what the binding runs. Empty clears
	// it, and the suggestion shows again.
	Alias string `json:"alias"`
}

// SetBindingAlias names what a binding runs. It changes no cloud
// resource: the alias is what Loomplane shows, and a redeploy keeps it.
func (s *Service) SetBindingAlias(ctx context.Context, p security.Principal, projectID, bindingID string, in BindingAliasInput) (*Binding, error) {
	if err := s.authz.Require(ctx, p, security.DeploymentCreate, projectID); err != nil {
		return nil, err
	}
	project, err := s.projects.GetProject(ctx, p.OrgID, projectID)
	if err != nil {
		return nil, err
	}
	b, err := s.store.GetBinding(ctx, p.OrgID, bindingID)
	if err != nil {
		return nil, err
	}
	if b.ProjectID != projectID {
		return nil, fmt.Errorf("%w: project %s has no binding %s", ErrNotFound, projectID, bindingID)
	}
	alias := strings.ToLower(strings.TrimSpace(in.Alias))
	if err := ValidateAlias(alias); err != nil {
		return nil, err
	}
	if err := s.store.SetBindingAlias(ctx, p.OrgID, b.ID, alias); err != nil {
		return nil, err
	}
	if s.audit != nil {
		s.audit.Record(ctx, projects.AuditEvent{
			Action: "binding.alias", Severity: "info", ProjectID: projectID, Principal: p,
			Attributes: map[string]any{"binding_id": b.ID, "name": b.Name, "from": b.Alias, "to": alias},
		})
	}
	b.Alias = alias
	if err := s.suggestAliases(ctx, p.OrgID, project, []*Binding{b}); err != nil {
		return nil, err
	}
	return b, nil
}

// suggestAliases fills SuggestedAlias on each binding of the project.
func (s *Service) suggestAliases(ctx context.Context, orgID string, project *projects.Project, bindings []*Binding) error {
	envs, err := s.projects.ListEnvironments(ctx, orgID, project.ID)
	if err != nil {
		return err
	}
	names := make(map[string]string, len(envs))
	for _, e := range envs {
		names[e.ID] = e.Name
	}
	for _, b := range bindings {
		b.SuggestedAlias = SuggestAlias(project.Slug, names[b.EnvironmentID], b.Subject, b.Name)
	}
	return nil
}
