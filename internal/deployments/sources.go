package deployments

import (
	"context"
	"fmt"
	"io"

	"github.com/xraph/loomplane/internal/artifacts"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/repositories"
)

// Downloader downloads a repository's tarball at a commit.
// *repositories.GitHub is the real one.
type Downloader interface {
	Download(ctx context.Context, repo repositories.Repository, sha, token string, w io.Writer) (int64, error)
}

// Tokens mints a token that reads a repository for an organization, or
// returns "" for a public one. *repositories.Connections is the real one.
type Tokens interface {
	Token(ctx context.Context, orgID string, repo repositories.Repository) (string, error)
}

// GitHubSources stores commits from GitHub as artifacts.
type GitHubSources struct {
	GitHub    Downloader
	Tokens    Tokens
	Artifacts *artifacts.Store
}

var _ Sources = (*GitHubSources)(nil)

// Store implements Sources. It streams the tarball from GitHub straight
// into storage, without reading what's inside.
func (g *GitHubSources) Store(ctx context.Context, orgID string, project *projects.Project, sha, key string) error {
	if ok, err := g.Artifacts.Exists(ctx, key); err != nil || ok {
		return err
	}
	repo, err := repositories.ParseGitHubURL(project.RepositoryURL)
	if err != nil {
		return err
	}
	token, err := g.Tokens.Token(ctx, orgID, repo)
	if err != nil {
		return fmt.Errorf("get a token for %s: %w", repo.FullName(), err)
	}
	pr, pw := io.Pipe()
	go func() {
		_, err := g.GitHub.Download(ctx, repo, sha, token, pw)
		pw.CloseWithError(err)
	}()
	if _, err := g.Artifacts.Put(ctx, key, pr, "application/gzip"); err != nil {
		_ = pr.CloseWithError(err) // stop the download too
		return err
	}
	return nil
}

// Open implements Sources.
func (g *GitHubSources) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	return g.Artifacts.Open(ctx, key)
}
