package repositories_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/repositories"
)

// fakeReader records which repositories it was asked about.
type fakeReader struct {
	name  string
	calls []string
}

func (f *fakeReader) note(op string, repo repositories.Repository) {
	f.calls = append(f.calls, op+" "+repo.Key())
}

func (f *fakeReader) Resolve(_ context.Context, repo repositories.Repository, _, _ string) (string, error) {
	f.note("resolve", repo)
	return f.name, nil
}

func (f *fakeReader) Fetch(_ context.Context, repo repositories.Repository, _, _, _ string) (repositories.Stats, error) {
	f.note("fetch", repo)
	return repositories.Stats{Files: 1}, nil
}

func (f *fakeReader) Download(_ context.Context, repo repositories.Repository, _, _ string, w io.Writer) (int64, error) {
	f.note("download", repo)
	n, err := io.WriteString(w, f.name)
	return int64(n), err
}

// through runs all three reads of repo through r and returns the first
// error, or the name of the reader that answered Resolve.
func through(t *testing.T, r repositories.Readers, repo repositories.Repository) (string, error) {
	t.Helper()
	ctx := context.Background()
	name, resolveErr := r.Resolve(ctx, repo, "main", "")
	_, fetchErr := r.Fetch(ctx, repo, testSHA, "", t.TempDir())
	var buf bytes.Buffer
	_, downloadErr := r.Download(ctx, repo, testSHA, "", &buf)
	if resolveErr != nil || fetchErr != nil || downloadErr != nil {
		if !errors.Is(fetchErr, resolveErr) || !errors.Is(downloadErr, resolveErr) {
			t.Fatalf("Resolve, Fetch and Download disagree: %v, %v, %v", resolveErr, fetchErr, downloadErr)
		}
		return "", resolveErr
	}
	if buf.String() != name {
		t.Fatalf("Download reached %q, Resolve reached %q", buf.String(), name)
	}
	return name, nil
}

func TestReaders_emptyHostIsGitHub(t *testing.T) {
	gh, bb := &fakeReader{name: "github"}, &fakeReader{name: "bitbucket"}
	r := repositories.Readers{Static: map[string]repositories.Reader{"github.com": gh, "bitbucket.org": bb}}
	if got, err := through(t, r, repositories.Repository{Owner: "acme", Name: "api"}); err != nil || got != "github" {
		t.Fatalf("an empty host reached %q, %v", got, err)
	}
	if len(gh.calls) != 3 || len(bb.calls) != 0 {
		t.Fatalf("github saw %v, bitbucket saw %v", gh.calls, bb.calls)
	}
}

func TestReaders_matchesHostCaseInsensitively(t *testing.T) {
	gh, bb := &fakeReader{name: "github"}, &fakeReader{name: "bitbucket"}
	r := repositories.Readers{Static: map[string]repositories.Reader{"github.com": gh, "bitbucket.org": bb}}
	if got, err := through(t, r, repositories.Repository{Host: "Bitbucket.ORG", Owner: "acme", Name: "api"}); err != nil || got != "bitbucket" {
		t.Fatalf("a mixed-case host reached %q, %v", got, err)
	}
	if len(gh.calls) != 0 || len(bb.calls) != 3 {
		t.Fatalf("github saw %v, bitbucket saw %v", gh.calls, bb.calls)
	}
}

func TestReaders_fallsBackToLookup(t *testing.T) {
	gh, self := &fakeReader{name: "github"}, &fakeReader{name: "self-managed"}
	var asked []string
	r := repositories.Readers{
		Static: map[string]repositories.Reader{"github.com": gh},
		Lookup: func(_ context.Context, host string) (repositories.Reader, bool) {
			asked = append(asked, host)
			return self, host == "git.example.com"
		},
	}
	repo := repositories.Repository{Host: "git.example.com", Owner: "g/s", Name: "p"}
	if got, err := through(t, r, repo); err != nil || got != "self-managed" {
		t.Fatalf("a looked-up host reached %q, %v", got, err)
	}
	want := []string{"resolve git.example.com/g/s/p", "fetch git.example.com/g/s/p", "download git.example.com/g/s/p"}
	if !slices.Equal(self.calls, want) {
		t.Fatalf("the looked-up reader saw %v", self.calls)
	}
	asked = nil
	if _, err := through(t, r, repositories.Repository{Owner: "acme", Name: "api"}); err != nil || len(asked) != 0 {
		t.Fatalf("a static host asked Lookup %v, %v", asked, err)
	}
}

func TestReaders_unknownHost(t *testing.T) {
	gh := &fakeReader{name: "github"}
	repo := repositories.Repository{Host: "git.example.com", Owner: "acme", Name: "api"}
	nilLookup := repositories.Readers{Static: map[string]repositories.Reader{"github.com": gh}}
	if _, err := through(t, nilLookup, repo); !errors.Is(err, repositories.ErrUnsupportedHost) {
		t.Fatalf("nil Lookup: %v", err)
	}
	misses := repositories.Readers{Lookup: func(context.Context, string) (repositories.Reader, bool) { return nil, false }}
	if _, err := through(t, misses, repo); !errors.Is(err, repositories.ErrUnsupportedHost) {
		t.Fatalf("a Lookup that misses: %v", err)
	}
	if len(gh.calls) != 0 {
		t.Fatalf("github saw %v", gh.calls)
	}
}

type fakeTokens struct{ got []repositories.Repository }

func (f *fakeTokens) Token(_ context.Context, orgID string, repo repositories.Repository) (string, error) {
	f.got = append(f.got, repo)
	return "tok-" + orgID, nil
}

func TestTokens_unknownHostReadsAnonymously(t *testing.T) {
	ctx := context.Background()
	if tok, err := (repositories.Tokens{}).Token(ctx, "org", repositories.Repository{Host: "bitbucket.org", Owner: "acme", Name: "api"}); tok != "" || err != nil {
		t.Fatalf("Token = %q, %v", tok, err)
	}
	gh := &fakeTokens{}
	tokens := repositories.Tokens{Static: map[string]repositories.TokenSource{"github.com": gh}}
	if tok, err := tokens.Token(ctx, "org", repositories.Repository{Owner: "acme", Name: "api"}); tok != "tok-org" || err != nil {
		t.Fatalf("GitHub's Token = %q, %v", tok, err)
	}
	if tok, err := tokens.Token(ctx, "org", repositories.Repository{Host: "bitbucket.org", Owner: "acme", Name: "api"}); tok != "" || err != nil || len(gh.got) != 1 {
		t.Fatalf("another host's Token = %q, %v (GitHub asked %d times)", tok, err, len(gh.got))
	}
}
