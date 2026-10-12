package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/internal/security"
)

// Access checks that Loomplane can read a repository at a ref, the way an
// analysis would, so a source that can't be analyzed fails when it's
// added, not three steps later.
type Access interface {
	Check(ctx context.Context, orgID string, repo repositories.Repository, ref string) error
}

// GitHubAccess reads through the organization's installations, or
// anonymously when none covers the repository.
type GitHubAccess struct {
	Tokens  TokenSource
	Fetcher Fetcher
}

// Check implements Access.
func (a GitHubAccess) Check(ctx context.Context, orgID string, repo repositories.Repository, ref string) error {
	token := ""
	if a.Tokens != nil {
		t, err := a.Tokens.Token(ctx, orgID, repo)
		if err != nil {
			return err
		}
		token = t
	}
	_, err := a.Fetcher.Resolve(ctx, repo, ref, token)
	return err
}

// SetAccess sets how sources are checked before they're attached. Without
// one, nothing is checked. Call it while the app registers.
func (s *Service) SetAccess(a Access) { s.access = a }

// AddSourceInput attaches a repository to a project.
type AddSourceInput struct {
	// Repository is a GitHub URL or "owner/name".
	Repository string `json:"repository"`
	// Alias names the source. Empty picks one from the repository's name.
	Alias string `json:"alias,omitempty"`
	// Root is the directory the project builds from. Empty means the whole
	// repository.
	Root string `json:"root,omitempty"`
	// Ref is the branch analyses read. Empty means the default branch.
	Ref string `json:"ref,omitempty"`
}

// UpdateSourceInput changes a source. A nil field is left alone. The alias
// and repository never change: graph IDs, the manifest and variables name
// them.
type UpdateSourceInput struct {
	Root *string `json:"root,omitempty"`
	Ref  *string `json:"ref,omitempty"`
}

// AddSource attaches a repository to a project.
func (s *Service) AddSource(ctx context.Context, p security.Principal, projectID string, in AddSourceInput) (*Source, error) {
	if err := s.authz.Require(ctx, p, security.ProjectUpdate, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	return s.addSource(ctx, p, projectID, in)
}

// AttachRepository attaches the repository a new project was created with,
// as its first source. It implements projects.Attacher; the project's
// creation already checked the caller.
func (s *Service) AttachRepository(ctx context.Context, p security.Principal, proj *projects.Project, url string) (string, error) {
	src, err := s.addSource(ctx, p, proj.ID, AddSourceInput{Repository: url})
	if err != nil {
		return "", err
	}
	return src.Repository, nil
}

func (s *Service) addSource(ctx context.Context, p security.Principal, projectID string, in AddSourceInput) (*Source, error) {
	repo, err := ParseRepository(in.Repository)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSource, err)
	}
	alias := strings.TrimSpace(in.Alias)
	if alias == "" {
		alias = AliasFor(repo.Name)
	}
	err = ValidAlias(alias)
	if err != nil {
		return nil, err
	}
	root, err := CleanRoot(in.Root)
	if err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(in.Ref)
	if ref != "" && !repositories.ValidRef(ref) {
		return nil, fmt.Errorf("%w: %q isn't a branch, tag or commit name", ErrInvalidSource, ref)
	}
	existing, err := s.store.ListSources(ctx, p.OrgID, projectID, true)
	if err != nil {
		return nil, err
	}
	for _, e := range existing {
		if e.Alias == alias {
			return nil, ErrAliasTaken
		}
		if e.RemovedAt == nil && strings.EqualFold(e.Repository, repo.FullName()) && RootsOverlap(e.Root, root) {
			return nil, ErrRootsOverlap
		}
	}
	err = s.check(ctx, p.OrgID, repo, ref)
	if err != nil {
		return nil, err
	}
	id, err := typeid.Generate("src")
	if err != nil {
		return nil, fmt.Errorf("generate source id: %w", err)
	}
	now := s.now().UTC()
	src := &Source{
		ID: id.String(), OrgID: p.OrgID, ProjectID: projectID, Alias: alias, Repository: repo.FullName(),
		Root: root, Ref: ref, CreatedBy: p.UserID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateSource(ctx, src); err != nil {
		return nil, err
	}
	s.record(ctx, p, projectID, "source.add", "info", map[string]any{"alias": alias, "repository": src.Repository, "root": root})
	return src, nil
}

// check reads the repository the way an analysis would. A 404 from GitHub
// means no access as often as no repository, so it says both.
func (s *Service) check(ctx context.Context, orgID string, repo repositories.Repository, ref string) error {
	if s.access == nil {
		return nil
	}
	err := s.access.Check(ctx, orgID, repo, ref)
	if errors.Is(err, repositories.ErrNotFound) {
		return ErrSourceInaccessible
	}
	return err
}

// Sources lists a project's live sources by alias.
func (s *Service) Sources(ctx context.Context, p security.Principal, projectID string) ([]*Source, error) {
	if err := s.authz.Require(ctx, p, security.ProjectRead, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	return s.store.ListSources(ctx, p.OrgID, projectID, false)
}

// UpdateSource changes a source's directory or branch.
func (s *Service) UpdateSource(ctx context.Context, p security.Principal, projectID, alias string, in UpdateSourceInput) (*Source, error) {
	if err := s.authz.Require(ctx, p, security.ProjectUpdate, projectID); err != nil {
		return nil, err
	}
	src, err := s.store.GetSource(ctx, p.OrgID, projectID, alias)
	if err != nil {
		return nil, err
	}
	if src.RemovedAt != nil {
		return nil, ErrSourceNotFound
	}
	fields := []string{}
	if in.Root != nil {
		root, err := CleanRoot(*in.Root)
		if err != nil {
			return nil, err
		}
		others, err := s.store.ListSources(ctx, p.OrgID, projectID, false)
		if err != nil {
			return nil, err
		}
		for _, o := range others {
			if o.ID != src.ID && strings.EqualFold(o.Repository, src.Repository) && RootsOverlap(o.Root, root) {
				return nil, ErrRootsOverlap
			}
		}
		src.Root = root
		fields = append(fields, "root")
	}
	if in.Ref != nil {
		ref := strings.TrimSpace(*in.Ref)
		if ref != "" && !repositories.ValidRef(ref) {
			return nil, fmt.Errorf("%w: %q isn't a branch, tag or commit name", ErrInvalidSource, ref)
		}
		owner, name, _ := strings.Cut(src.Repository, "/")
		if err := s.check(ctx, p.OrgID, repositories.Repository{Owner: owner, Name: name}, ref); err != nil {
			return nil, err
		}
		src.Ref = ref
		fields = append(fields, "ref")
	}
	src.UpdatedAt = s.now().UTC()
	if err := s.store.UpdateSource(ctx, src); err != nil {
		return nil, err
	}
	s.record(ctx, p, projectID, "source.update", "info", map[string]any{"alias": alias, "fields": fields})
	return src, nil
}

// RemoveSource detaches a source. Its analyses stay, since plans name
// them, and so does its alias.
func (s *Service) RemoveSource(ctx context.Context, p security.Principal, projectID, alias string) error {
	if err := s.authz.Require(ctx, p, security.ProjectUpdate, projectID); err != nil {
		return err
	}
	if err := s.store.RemoveSource(ctx, p.OrgID, projectID, alias, s.now().UTC()); err != nil {
		return err
	}
	s.record(ctx, p, projectID, "source.remove", "warning", map[string]any{"alias": alias})
	return nil
}
