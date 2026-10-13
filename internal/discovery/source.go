package discovery

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/repositories"
	"github.com/xraph/loomplane/pkg/discovery"
)

// Source is a repository, or a subtree of one, that a project builds from.
// A project has any number. Its alias names it in graph IDs, the manifest
// and variables, so an alias never changes and is never reused, even after
// the source is removed: an old plan's "web" always means the same thing.
type Source struct {
	ID        string `json:"id" forge:"id"`
	OrgID     string `json:"org_id"`
	ProjectID string `json:"project_id"`
	Alias     string `json:"alias"`
	// Repository is "owner/name" on GitHub.
	Repository string `json:"repository"`
	// Root is the directory the project builds from, "" for the whole
	// repository.
	Root string `json:"root"`
	// Ref is the branch analyses read, "" for the default branch.
	Ref       string     `json:"ref"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	RemovedAt *time.Time `json:"removed_at,omitempty"`
}

// normalize puts the times a store returns into UTC, so a source compares
// the same whichever session read it.
func (s *Source) normalize() {
	s.CreatedAt = s.CreatedAt.UTC()
	s.UpdatedAt = s.UpdatedAt.UTC()
	if s.RemovedAt != nil {
		t := s.RemovedAt.UTC()
		s.RemovedAt = &t
	}
}

// Errors about sources.
var (
	ErrSourceNotFound     = errors.New("source not found")
	ErrInvalidSource      = errors.New("invalid source")
	ErrNoSources          = errors.New("this project has no repositories; add one first")
	ErrAliasTaken         = errors.New("this project has already used that alias, and an alias is never reused")
	ErrAliasReserved      = errors.New("that alias is an ecosystem name; pick another")
	ErrRootsOverlap       = errors.New("this repository is already attached at a directory that contains this one, or sits inside it")
	ErrSourceInaccessible = errors.New("that repository or branch can't be read by Loomplane; a private repository needs a GitHub installation that can see it")
)

var aliasPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// reservedAliases are the ecosystem names. An alias of "go" would make
// svc:go:go:cmd/api read two ways.
var reservedAliases = map[string]bool{
	string(discovery.Go): true, string(discovery.Rust): true,
	string(discovery.Node): true, string(discovery.Container): true,
}

// ValidAlias reports why alias can't name a source, or nil.
func ValidAlias(alias string) error {
	if reservedAliases[alias] {
		return ErrAliasReserved
	}
	if !aliasPattern.MatchString(alias) {
		return fmt.Errorf("%w: an alias is 1 to 32 lower case letters, digits and hyphens, starting with a letter", ErrInvalidSource)
	}
	return nil
}

// AliasFor proposes an alias from a repository's name. The migration that
// attaches each project's old repository_url does the same in SQL
// (database/migrations, sourcesUp), so change both together.
func AliasFor(name string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(strings.TrimSuffix(name, ".git")) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			hyphen = false
		case !hyphen:
			b.WriteByte('-')
			hyphen = true
		}
	}
	s := strings.Trim(b.String(), "-")
	switch {
	case s == "":
		s = "repo"
	case s[0] >= '0' && s[0] <= '9':
		s = "r-" + s
	}
	if len(s) > 32 {
		s = strings.TrimRight(s[:32], "-")
	}
	if reservedAliases[s] {
		s += "-src"
	}
	return s
}

// branchRef tidies a branch someone typed, so a source stores the name
// GitHub's push events compare with: "refs/heads/main" is kept as "main".
func branchRef(ref string) string {
	return strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/")
}

// CleanRoot tidies a directory someone typed: no leading or trailing
// slash, and "" for the whole repository. A directory that climbs out of
// the repository is refused.
func CleanRoot(root string) (string, error) {
	r := strings.Trim(strings.TrimSpace(root), "/")
	if r == "" || r == "." {
		return "", nil
	}
	if strings.Contains(r, `\`) || path.Clean(r) != r || r == ".." || strings.HasPrefix(r, "../") {
		return "", fmt.Errorf("%w: %q isn't a directory in the repository", ErrInvalidSource, root)
	}
	return r, nil
}

// RootsOverlap reports whether two directories of one repository share
// files, which would put the same service under two aliases.
func RootsOverlap(a, b string) bool {
	return a == "" || b == "" || a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// ParseRepository reads a repository from a GitHub URL, a git@ address,
// or plain "owner/name".
func ParseRepository(raw string) (repositories.Repository, error) {
	s := strings.TrimSpace(raw)
	if !strings.Contains(s, "://") && !strings.Contains(s, "@") && !strings.Contains(s, "github.com") && strings.Count(s, "/") == 1 {
		s = "github.com/" + s
	}
	return repositories.ParseGitHubURL(s)
}
