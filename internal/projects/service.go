package projects

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/security"
)

const (
	maxNameLength        = 64
	maxSlugLength        = 48
	maxDescriptionLength = 500
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Authorizer decides whether a principal may act. *security.Authorizer is
// the real one.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// Initializer starts a new project's initialization in the background.
// Loomplane's is a Dispatch job (internal/orchestration).
type Initializer interface {
	InitializeProject(ctx context.Context, orgID, projectID string) error
}

// Auditor records changes worth an audit trail. Recording must not fail the
// change, so it returns nothing; the implementation logs its own failures.
type Auditor interface {
	Record(ctx context.Context, e AuditEvent)
}

// AuditEvent is one audited change.
type AuditEvent struct {
	Action     string
	Severity   string // info, warning or critical
	ProjectID  string
	Principal  security.Principal
	Attributes map[string]any
}

// Service is the project API: every method checks the caller's role first.
type Service struct {
	store Store
	authz Authorizer
	init  Initializer
	audit Auditor
	now   func() time.Time
}

// NewService returns a Service. audit may be nil.
func NewService(store Store, authz Authorizer, init Initializer, audit Auditor) *Service {
	return &Service{store: store, authz: authz, init: init, audit: audit, now: time.Now}
}

// CreateProjectInput is what a caller sends to create a project.
type CreateProjectInput struct {
	Name          string `json:"name"`
	Slug          string `json:"slug,omitempty"`
	Description   string `json:"description,omitempty"`
	RepositoryURL string `json:"repository_url,omitempty"`
}

// UpdateProjectInput changes a project. A nil field is left alone. The slug never
// changes: other systems will key on it.
type UpdateProjectInput struct {
	Name          *string `json:"name,omitempty"`
	Description   *string `json:"description,omitempty"`
	RepositoryURL *string `json:"repository_url,omitempty"`
}

// Create adds a project to the caller's organization and starts its
// initialization.
func (s *Service) Create(ctx context.Context, p security.Principal, in CreateProjectInput) (*Project, error) {
	if err := s.authz.Require(ctx, p, security.ProjectCreate, ""); err != nil {
		return nil, err
	}
	proj, err := s.build(p, in)
	if err != nil {
		return nil, err
	}
	if err := s.store.CreateProject(ctx, proj); err != nil {
		return nil, err
	}
	if err := s.init.InitializeProject(ctx, proj.OrgID, proj.ID); err != nil {
		// Without its job the project would stay initializing forever, so
		// take it back out rather than leave it stranded.
		if derr := s.store.DeleteProject(context.WithoutCancel(ctx), proj.OrgID, proj.ID); derr != nil {
			err = errors.Join(err, fmt.Errorf("remove the project again: %w", derr))
		}
		return nil, fmt.Errorf("start project initialization: %w", err)
	}
	s.record(ctx, AuditEvent{Action: "project.create", Severity: "info", ProjectID: proj.ID, Principal: p,
		Attributes: map[string]any{"slug": proj.Slug}})
	return proj, nil
}

func (s *Service) build(p security.Principal, in CreateProjectInput) (*Project, error) {
	name := strings.TrimSpace(in.Name)
	if err := validateName(name); err != nil {
		return nil, err
	}
	slug := strings.TrimSpace(in.Slug)
	if slug == "" {
		slug = Slugify(name)
	}
	if err := validateSlug(slug); err != nil {
		return nil, err
	}
	desc := strings.TrimSpace(in.Description)
	if err := validateDescription(desc); err != nil {
		return nil, err
	}
	repo := strings.TrimSpace(in.RepositoryURL)
	if err := validateRepositoryURL(repo); err != nil {
		return nil, err
	}
	id, err := typeid.Generate("proj")
	if err != nil {
		return nil, fmt.Errorf("generate project id: %w", err)
	}
	now := s.now().UTC()
	return &Project{
		ID: id.String(), OrgID: p.OrgID, Name: name, Slug: slug, Description: desc,
		RepositoryURL: repo, Status: StatusInitializing, CreatedBy: p.UserID,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

// Get returns one project in the caller's organization.
func (s *Service) Get(ctx context.Context, p security.Principal, id string) (*Project, error) {
	if err := s.authz.Require(ctx, p, security.ProjectRead, id); err != nil {
		return nil, err
	}
	return s.store.GetProject(ctx, p.OrgID, id)
}

// List returns the caller's organization's projects, newest first.
func (s *Service) List(ctx context.Context, p security.Principal) ([]*Project, error) {
	if err := s.authz.Require(ctx, p, security.ProjectRead, ""); err != nil {
		return nil, err
	}
	return s.store.ListProjects(ctx, p.OrgID)
}

// Update changes a project's name, description or repository.
func (s *Service) Update(ctx context.Context, p security.Principal, id string, in UpdateProjectInput) (*Project, error) {
	if err := s.authz.Require(ctx, p, security.ProjectUpdate, id); err != nil {
		return nil, err
	}
	proj, err := s.store.GetProject(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if err := validateName(name); err != nil {
			return nil, err
		}
		proj.Name = name
	}
	if in.Description != nil {
		desc := strings.TrimSpace(*in.Description)
		if err := validateDescription(desc); err != nil {
			return nil, err
		}
		proj.Description = desc
	}
	if in.RepositoryURL != nil {
		repo := strings.TrimSpace(*in.RepositoryURL)
		if err := validateRepositoryURL(repo); err != nil {
			return nil, err
		}
		proj.RepositoryURL = repo
	}
	proj.UpdatedAt = s.now().UTC()
	if err := s.store.UpdateProject(ctx, proj); err != nil {
		return nil, err
	}
	s.record(ctx, AuditEvent{Action: "project.update", Severity: "info", ProjectID: proj.ID, Principal: p})
	return proj, nil
}

// Delete removes a project and its environments.
func (s *Service) Delete(ctx context.Context, p security.Principal, id string) error {
	if err := s.authz.Require(ctx, p, security.ProjectDelete, id); err != nil {
		return err
	}
	if err := s.store.DeleteProject(ctx, p.OrgID, id); err != nil {
		return err
	}
	s.record(ctx, AuditEvent{Action: "project.delete", Severity: "warning", ProjectID: id, Principal: p})
	return nil
}

// Environments lists a project's environments.
func (s *Service) Environments(ctx context.Context, p security.Principal, projectID string) ([]*Environment, error) {
	if err := s.authz.Require(ctx, p, security.EnvironmentRead, projectID); err != nil {
		return nil, err
	}
	if _, err := s.store.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	return s.store.ListEnvironments(ctx, p.OrgID, projectID)
}

// Initialize gives a new project its default environments and marks it
// ready. The initialization job calls it, with no caller to check: the job
// was only enqueued by a Create that passed its check. It's safe to run
// more than once, since Dispatch retries a failed job and may run one again
// after a crash. A project deleted in the meantime is not an error.
func (s *Service) Initialize(ctx context.Context, orgID, projectID string) error {
	proj, err := s.store.GetProject(ctx, orgID, projectID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	now := s.now().UTC()
	for _, def := range DefaultEnvironments() {
		id, err := typeid.Generate("env")
		if err != nil {
			return fmt.Errorf("generate environment id: %w", err)
		}
		env := def
		env.ID, env.OrgID, env.ProjectID = id.String(), orgID, projectID
		env.CreatedAt, env.UpdatedAt = now, now
		if err := s.store.EnsureEnvironment(ctx, &env); err != nil {
			return fmt.Errorf("ensure environment %s: %w", def.Name, err)
		}
	}
	if proj.Status == StatusReady {
		return nil
	}
	proj.Status, proj.UpdatedAt = StatusReady, now
	return s.store.UpdateProject(ctx, proj)
}

func (s *Service) record(ctx context.Context, e AuditEvent) {
	if s.audit != nil {
		s.audit.Record(ctx, e)
	}
}

// Slugify turns a project name into a URL-safe slug: lower case letters,
// digits and single hyphens, trimmed to the maximum length.
func Slugify(name string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			hyphen = false
		case b.Len() > 0 && !hyphen:
			b.WriteByte('-')
			hyphen = true
		}
	}
	slug := strings.TrimRight(b.String(), "-")
	if len(slug) > maxSlugLength {
		slug = strings.TrimRight(slug[:maxSlugLength], "-")
	}
	return slug
}

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: a name is required", ErrInvalid)
	}
	if utf8.RuneCountInString(name) > maxNameLength {
		return fmt.Errorf("%w: the name can be at most %d characters", ErrInvalid, maxNameLength)
	}
	return nil
}

func validateSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("%w: the name needs at least one letter or digit for its slug", ErrInvalid)
	}
	if len(slug) > maxSlugLength || !slugPattern.MatchString(slug) {
		return fmt.Errorf("%w: a slug is up to %d lower case letters, digits and single hyphens", ErrInvalid, maxSlugLength)
	}
	if strings.Contains(slug, "--") {
		return fmt.Errorf("%w: a slug can't contain two hyphens in a row", ErrInvalid)
	}
	return nil
}

func validateDescription(desc string) error {
	if utf8.RuneCountInString(desc) > maxDescriptionLength {
		return fmt.Errorf("%w: the description can be at most %d characters", ErrInvalid, maxDescriptionLength)
	}
	return nil
}

// validateRepositoryURL accepts an empty URL (connect one later), an https
// URL, or an scp-style SSH address such as git@github.com:org/repo.git.
func validateRepositoryURL(repo string) error {
	if repo == "" {
		return nil
	}
	if strings.HasPrefix(repo, "git@") && strings.Contains(repo, ":") {
		return nil
	}
	u, err := url.Parse(repo)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("%w: the repository must be an https URL or an SSH address like git@github.com:org/repo.git, without credentials", ErrInvalid)
	}
	return nil
}
