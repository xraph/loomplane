package deployments

import (
	"context"
	"fmt"
	"io"

	"github.com/xraph/loomplane/internal/artifacts"
	"github.com/xraph/loomplane/internal/repositories"
)

// RepositorySources stores commits from Git hosts as artifacts.
// repositories.Readers and repositories.Tokens are the real ones, and pick
// by the repository's host.
type RepositorySources struct {
	Readers   repositories.Reader
	Tokens    repositories.TokenSource
	Artifacts *artifacts.Store
}

var _ Sources = (*RepositorySources)(nil)

// Store implements Sources. It streams the tarball from the host straight
// into storage, without reading what's inside.
func (g *RepositorySources) Store(ctx context.Context, orgID, repository, sha, key string) error {
	if ok, err := g.Artifacts.Exists(ctx, key); err != nil || ok {
		return err
	}
	repo, err := repositories.ParseKey(repository)
	if err != nil {
		return err
	}
	token, err := g.Tokens.Token(ctx, orgID, repo)
	if err != nil {
		return fmt.Errorf("get a token for %s: %w", repo.FullName(), err)
	}
	pr, pw := io.Pipe()
	go func() {
		_, err := g.Readers.Download(ctx, repo, sha, token, pw)
		pw.CloseWithError(err)
	}()
	if _, err := g.Artifacts.Put(ctx, key, pr, "application/gzip"); err != nil {
		_ = pr.CloseWithError(err) // stop the download too
		return err
	}
	return nil
}

// Open implements Sources.
func (g *RepositorySources) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	return g.Artifacts.Open(ctx, key)
}
