package deployments

import (
	"context"
	"io"
	"testing"

	"github.com/xraph/loomplane/internal/artifacts"
	"github.com/xraph/loomplane/internal/repositories"
)

// hostReader answers Download with its name and records what it was asked
// to read.
type hostReader struct {
	name  string
	repos []repositories.Repository
}

func (h *hostReader) Resolve(context.Context, repositories.Repository, string, string) (string, error) {
	return "", nil
}

func (h *hostReader) Fetch(context.Context, repositories.Repository, string, string, string) (repositories.Stats, error) {
	return repositories.Stats{}, nil
}

func (h *hostReader) Download(_ context.Context, repo repositories.Repository, _, _ string, w io.Writer) (int64, error) {
	h.repos = append(h.repos, repo)
	n, err := io.WriteString(w, h.name)
	return int64(n), err
}

func TestRepositorySources_readsThroughTheHost(t *testing.T) {
	ctx := context.Background()
	store, err := artifacts.Open(ctx, "mem://")
	if err != nil {
		t.Fatal(err)
	}
	gh, bb := &hostReader{name: "github"}, &hostReader{name: "bitbucket"}
	src := &RepositorySources{
		Readers:   repositories.Readers{Static: map[string]repositories.Reader{"github.com": gh, "bitbucket.org": bb}},
		Tokens:    repositories.Tokens{},
		Artifacts: store,
	}
	sha := "0123456789abcdef0123456789abcdef01234567"
	if err = src.Store(ctx, "org", "bitbucket.org/acme/api", sha, "bb-key"); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err = src.Store(ctx, "org", "acme/api", sha, "gh-key"); err != nil {
		t.Fatalf("Store: %v", err)
	}

	if want := (repositories.Repository{Host: "bitbucket.org", Owner: "acme", Name: "api"}); len(bb.repos) != 1 || bb.repos[0] != want {
		t.Fatalf("Bitbucket's reader was asked for %+v", bb.repos)
	}
	if want := (repositories.Repository{Owner: "acme", Name: "api"}); len(gh.repos) != 1 || gh.repos[0] != want {
		t.Fatalf("GitHub's reader was asked for %+v", gh.repos)
	}
	rc, _, err := src.Open(ctx, "bb-key")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if body, _ := io.ReadAll(rc); string(body) != "bitbucket" {
		t.Fatalf("stored %q", body)
	}
}
