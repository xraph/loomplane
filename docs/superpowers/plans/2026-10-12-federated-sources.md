# Federated Sources Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let one Loomplane project build from several GitHub repositories (or subtrees of them), compose their analyses into one application graph, and plan deployments that move every repository or only a chosen few.

**Architecture:** A project gets any number of sources (`loomplane_project_sources`). Each source is analyzed on its own exactly as a repository is today, and the per-source graphs stay alias-free. A pure function, `graph.Compose`, qualifies IDs with each source's alias, applies its root, merges resources and links dependencies across repositories. A plan pins a lockset, one analysis and commit per source. A scope says which sources move, and the rest stay at what `loomplane_environment_sources` says is deployed.

**Tech Stack:** Go 1.26, Forge, grove/pgdriver over Postgres (pgx v5), Dispatch jobs, React 19 + TanStack Router dashboard with a Forge-generated client, Vitest.

**Spec:** `docs/superpowers/specs/2026-10-12-federated-sources-design.md`. Read it before starting; this plan argues from it.

## Global Constraints

- Aliases match `^[a-z][a-z0-9-]{0,31}$`. `go`, `rust`, `node` and `container` are reserved. An alias never changes and is never reused, even after its source is removed.
- Graph IDs: `svc:<alias>:<eco>:<path>`, `pkg:<alias>:<eco>:<path>`, `res:<kind>` (shared across sources). Graphs stored on analyses stay alias-free; only `graph.Compose` qualifies.
- `graph.SchemaVersion` becomes 2.
- Every migration statement is safe to re-run (`IF NOT EXISTS`, `ON CONFLICT DO NOTHING`, guarded `UPDATE`s) and is its own string, as `database/migrations/migrations.go` explains. Register each new migration after the last `statements(...)` line with the next free version. The versions below assume `20261012000007` is the last one; take the next free number if more have landed.
- Old columns stay, unread: `loomplane_projects.repository_url`, `loomplane_plans.analysis_id`, `loomplane_plans.commit_sha`. Nothing writes them after this work. A later migration drops them.
- Every task that changes the API regenerates the dashboard client in the same task, then runs `make lint-dashboard test-dashboard`. Regenerating needs the API running: `make up`, then `make dev-api` in another terminal, then `make gen-client`.
- Comments follow the house style: full sentences that say why. Error messages talk to the person reading them ("this project has no repositories; add one first").
- Commit messages are conventional (`feat:`, `test:`, `docs:`), contain no em dashes, and carry no `Co-Authored-By` or other attribution lines.
- Phase 3 touches `internal/planning`, which has uncommitted work on cloud connections in it. Start Phase 3 only after that work is committed, and fit the edits below around it.
- Unit tests: `go test ./...`. Integration tests: `make up`, then `make test-integration`. Migration tests: `LOOMPLANE_TEST_DATABASE_URL='postgres://loomplane:loomplane@localhost:55532/loomplane?sslmode=disable' go test -tags=integration ./database/migrations/`.

## Review Focus

1. A project created with a repository Loomplane can't read (private, no installation) must fail without leaving a half-made project behind. Test in Task 5.
2. Asking to analyze every source while one of them is still running must refuse the whole request and queue nothing for the others. Test in Task 4.
3. Re-adding a removed source's alias must be refused, so an old plan's alias keeps meaning the same repository. Test in Task 3.
4. An in-scope service that needs a held service which runs nowhere yet must get a `need-held` problem, never a plan that silently drops its address variable. Test in Task 18.
5. A plan scope that names a removed or unknown source must be refused with a clear error, never ignored. Test in Task 19.

---

# Phase 1: Sources

Ships a project with any number of repositories, analyzed one by one. The page still shows one analysis's graph until Phase 2 composes them.

### Task 1: Source rules

**Files:**
- Create: `internal/discovery/source.go`
- Test: `internal/discovery/source_test.go`

**Interfaces:**
- Produces: `type Source struct`, `ValidAlias(string) error`, `AliasFor(name string) string`, `CleanRoot(string) (string, error)`, `RootsOverlap(a, b string) bool`, `ParseRepository(string) (repositories.Repository, error)`, and the errors `ErrSourceNotFound`, `ErrInvalidSource`, `ErrAliasTaken`, `ErrAliasReserved`, `ErrRootsOverlap`, `ErrSourceInaccessible`, `ErrNoSources`.

- [ ] **Step 1: Write the failing tests**

```go
package discovery

import (
	"errors"
	"strings"
	"testing"
)

func TestValidAlias(t *testing.T) {
	tests := []struct {
		alias string
		want  error
	}{
		{"web", nil},
		{"api-2", nil},
		{"a", nil},
		{"abcdefghijklmnopqrstuvwxyz012345", nil},
		{"abcdefghijklmnopqrstuvwxyz0123456", ErrInvalidSource},
		{"Web", ErrInvalidSource},
		{"2web", ErrInvalidSource},
		{"", ErrInvalidSource},
		{"go", ErrAliasReserved},
		{"container", ErrAliasReserved},
	}
	for _, tt := range tests {
		t.Run(tt.alias, func(t *testing.T) {
			err := ValidAlias(tt.alias)
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("ValidAlias(%q) = %v, want %v", tt.alias, err, tt.want)
			}
		})
	}
}

func TestAliasFor(t *testing.T) {
	tests := []struct{ name, want string }{
		{"storefront", "storefront"},
		{"Storefront.git", "storefront"},
		{"my_cool.Repo", "my-cool-repo"},
		{"go", "go-src"},
		{"node", "node-src"},
		{"123-api", "r-123-api"},
		{"---", "repo"},
		{strings.Repeat("a", 40), strings.Repeat("a", 32)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AliasFor(tt.name)
			if got != tt.want {
				t.Fatalf("AliasFor(%q) = %q, want %q", tt.name, got, tt.want)
			}
			if err := ValidAlias(got); err != nil {
				t.Fatalf("AliasFor(%q) gave an invalid alias: %v", tt.name, err)
			}
		})
	}
}

func TestCleanRoot(t *testing.T) {
	tests := []struct {
		in, want string
		bad      bool
	}{
		{"", "", false},
		{"/", "", false},
		{".", "", false},
		{" apps/web/ ", "apps/web", false},
		{"apps//web", "", true},
		{"../x", "", true},
		{"a/../b", "", true},
		{"..", "", true},
		{`a\b`, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := CleanRoot(tt.in)
			if tt.bad {
				if !errors.Is(err, ErrInvalidSource) {
					t.Fatalf("CleanRoot(%q) = %q, %v; want ErrInvalidSource", tt.in, got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("CleanRoot(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
		})
	}
}

func TestRootsOverlap(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"", "apps", true},
		{"apps", "apps", true},
		{"apps", "apps/web", true},
		{"apps/web", "apps", true},
		{"apps/web", "apps/webby", false},
		{"apps", "services", false},
	}
	for _, tt := range tests {
		if got := RootsOverlap(tt.a, tt.b); got != tt.want {
			t.Errorf("RootsOverlap(%q, %q) = %v", tt.a, tt.b, got)
		}
	}
}

func TestParseRepository(t *testing.T) {
	for _, in := range []string{"acme/mono", " https://github.com/acme/mono.git ", "git@github.com:acme/mono.git", "github.com/acme/mono"} {
		repo, err := ParseRepository(in)
		if err != nil || repo.FullName() != "acme/mono" {
			t.Errorf("ParseRepository(%q) = %v, %v", in, repo, err)
		}
	}
	for _, in := range []string{"https://gitlab.com/acme/mono", "acme", ""} {
		if _, err := ParseRepository(in); err == nil {
			t.Errorf("ParseRepository(%q) succeeded", in)
		}
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/discovery/ -run 'TestValidAlias|TestAliasFor|TestCleanRoot|TestRootsOverlap|TestParseRepository'`
Expected: FAIL, `undefined: ValidAlias` and friends.

- [ ] **Step 3: Write `internal/discovery/source.go`**

```go
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

func (s *Source) normalize() {
	s.CreatedAt, s.UpdatedAt = s.CreatedAt.UTC(), s.UpdatedAt.UTC()
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
	ErrSourceInaccessible = errors.New("Loomplane can't read that repository or branch; a private repository needs a GitHub installation that can see it")
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
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test ./internal/discovery/ -run 'TestValidAlias|TestAliasFor|TestCleanRoot|TestRootsOverlap|TestParseRepository'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/discovery/source.go internal/discovery/source_test.go
git commit -m "feat: add the rules for a project's sources"
```

### Task 2: The sources migration

**Files:**
- Modify: `database/migrations/migrations.go` (register `create_project_sources`, add `sourcesUp`, `sourcesDown`)
- Test: `database/migrations/sources_test.go`

**Interfaces:**
- Produces: tables and columns later tasks read: `loomplane_project_sources`, `loomplane_analyses.source_id`, `loomplane_analyses.alias`.

- [ ] **Step 1: Write the failing test**

```go
//go:build integration

package migrations

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// scratch is an empty database of its own on the test server, dropped when
// the test ends.
func scratch(t *testing.T) *pgx.Conn {
	t.Helper()
	url := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("LOOMPLANE_TEST_DATABASE_URL isn't set; make test-integration sets it")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("loomplane_migrations_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = name
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func exec(t *testing.T, conn *pgx.Conn, stmts ...string) {
	t.Helper()
	for i, s := range stmts {
		if _, err := conn.Exec(context.Background(), s); err != nil {
			t.Fatalf("statement %d: %v", i+1, err)
		}
	}
}

func strs(t *testing.T, conn *pgx.Conn, query string) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSources_eachProjectsRepositoryBecomesItsFirstSource(t *testing.T) {
	conn := scratch(t)
	exec(t, conn, projectsUp...)
	exec(t, conn, analysesUp...)
	exec(t, conn, variablesUp...)
	exec(t, conn, projectStyleUp...)
	exec(t, conn,
		`INSERT INTO loomplane_projects (id, org_id, name, slug, repository_url, status, created_by, created_at, updated_at) VALUES
			('proj_a', 'org', 'A', 'a', 'https://github.com/acme/Storefront.git', 'ready', 'usr', now(), now()),
			('proj_b', 'org', 'B', 'b', 'git@github.com:acme/go.git', 'ready', 'usr', now(), now()),
			('proj_c', 'org', 'C', 'c', 'https://gitlab.com/acme/c', 'ready', 'usr', now(), now()),
			('proj_d', 'org', 'D', 'd', '', 'ready', 'usr', now(), now())`,
		`INSERT INTO loomplane_analyses (id, org_id, project_id, status, repository, created_by, created_at) VALUES
			('anl_a', 'org', 'proj_a', 'succeeded', 'acme/Storefront', 'usr', now())`,
		`INSERT INTO loomplane_variables (id, org_id, project_id, service, key, created_by, updated_by, created_at, updated_at) VALUES
			('var_a', 'org', 'proj_a', 'svc:go:cmd/api', 'PORT', 'usr', 'usr', now(), now()),
			('var_b', 'org', 'proj_a', '', 'LOG', 'usr', 'usr', now(), now())`,
	)
	// Twice: grove migrations aren't transactional, so one interrupted
	// halfway runs again from the top.
	exec(t, conn, sourcesUp...)
	exec(t, conn, sourcesUp...)

	got := strs(t, conn, `SELECT project_id || ' ' || alias || ' ' || repository || ' [' || root || '] [' || ref || ']'
		FROM loomplane_project_sources ORDER BY project_id`)
	want := []string{"proj_a storefront acme/Storefront [] []", "proj_b go-src acme/go [] []"}
	if !slices.Equal(got, want) {
		t.Fatalf("sources %q, want %q", got, want)
	}
	got = strs(t, conn, `SELECT a.alias || ' ' || (a.source_id = s.id)::text
		FROM loomplane_analyses a JOIN loomplane_project_sources s ON s.project_id = a.project_id`)
	if !slices.Equal(got, []string{"storefront true"}) {
		t.Fatalf("analysis source %q", got)
	}
	got = strs(t, conn, `SELECT service FROM loomplane_variables ORDER BY id`)
	if !slices.Equal(got, []string{"svc:storefront:go:cmd/api", ""}) {
		t.Fatalf("variable services %q", got)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `make up`, then `LOOMPLANE_TEST_DATABASE_URL='postgres://loomplane:loomplane@localhost:55532/loomplane?sslmode=disable' go test -tags=integration ./database/migrations/ -run TestSources`
Expected: FAIL, `undefined: sourcesUp`.

- [ ] **Step 3: Add the migration**

Register it after the last `statements(...)` line in `Group()`:

```go
		statements("20261012000008", "create_project_sources", sourcesUp, sourcesDown),
```

Append to `migrations.go`:

```go
// A source is a repository, or a directory of one, that a project builds
// from; a project has any number. A removed source keeps its row, so its
// alias stays taken: plans and variables name sources by alias, and an
// alias must always mean the same repository. An analysis reads one
// source; its alias is copied onto it, which is safe because aliases never
// change.
//
// The backfill turns each project's repository_url into its first source,
// with the alias internal/discovery.AliasFor would pick, and qualifies the
// service IDs its variables name with that alias. A URL that isn't on
// GitHub gets no source: it could never be analyzed. repository_url stays,
// unread, so a rollback still finds it; a later migration drops it. Each
// UPDATE only touches a project with exactly one source, which keeps a
// re-run from guessing between two.
var sourcesUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_project_sources (
		id         TEXT        PRIMARY KEY,
		org_id     TEXT        NOT NULL,
		project_id TEXT        NOT NULL REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		alias      TEXT        NOT NULL,
		repository TEXT        NOT NULL,
		root       TEXT        NOT NULL DEFAULT '',
		ref        TEXT        NOT NULL DEFAULT '',
		created_by TEXT        NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		removed_at TIMESTAMPTZ,
		CONSTRAINT loomplane_project_sources_alias UNIQUE (project_id, alias)
	)`,
	`CREATE INDEX IF NOT EXISTS loomplane_project_sources_repository
		ON loomplane_project_sources (repository)`,
	`ALTER TABLE loomplane_analyses ADD COLUMN IF NOT EXISTS source_id TEXT
		REFERENCES loomplane_project_sources (id) ON DELETE CASCADE`,
	`ALTER TABLE loomplane_analyses ADD COLUMN IF NOT EXISTS alias TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX IF NOT EXISTS loomplane_analyses_source_created
		ON loomplane_analyses (org_id, source_id, created_at DESC)`,
	`WITH m AS (
		SELECT id, org_id, created_by, created_at,
			regexp_match(repository_url, 'github\.com[/:]([A-Za-z0-9][A-Za-z0-9-]*)/([A-Za-z0-9._-]+)') AS parts
		FROM loomplane_projects WHERE repository_url <> ''
	), r AS (
		SELECT id, org_id, created_by, created_at,
			parts[1] || '/' || regexp_replace(parts[2], '\.git$', '') AS repository,
			trim(both '-' FROM regexp_replace(lower(regexp_replace(parts[2], '\.git$', '')), '[^a-z0-9]+', '-', 'g')) AS base
		FROM m WHERE parts IS NOT NULL
	), a AS (
		SELECT *, rtrim(left(CASE WHEN base = '' THEN 'repo' WHEN base ~ '^[0-9]' THEN 'r-' || base ELSE base END, 32), '-') AS alias
		FROM r
	)
	INSERT INTO loomplane_project_sources (id, org_id, project_id, alias, repository, root, ref, created_by, created_at, updated_at)
	SELECT 'src_' || substr(md5('source:' || id), 1, 26), org_id, id,
		CASE WHEN alias IN ('go', 'rust', 'node', 'container') THEN alias || '-src' ELSE alias END,
		repository, '', '', created_by, created_at, created_at
	FROM a
	ON CONFLICT DO NOTHING`,
	`UPDATE loomplane_analyses a SET source_id = s.id, alias = s.alias
		FROM loomplane_project_sources s
		WHERE a.source_id IS NULL AND s.project_id = a.project_id
		AND (SELECT count(*) FROM loomplane_project_sources x WHERE x.project_id = a.project_id) = 1`,
	`UPDATE loomplane_variables v SET service = 'svc:' || s.alias || ':' || substr(v.service, 5)
		FROM loomplane_project_sources s
		WHERE v.project_id = s.project_id AND v.service LIKE 'svc:%'
		AND split_part(v.service, ':', 2) IN ('go', 'rust', 'node', 'container')
		AND (SELECT count(*) FROM loomplane_project_sources x WHERE x.project_id = v.project_id) = 1`,
}

var sourcesDown = []string{
	`UPDATE loomplane_variables v SET service = 'svc:' || substr(v.service, length('svc:' || s.alias || ':') + 1)
		FROM loomplane_project_sources s
		WHERE v.project_id = s.project_id AND v.service LIKE 'svc:' || s.alias || ':%'`,
	`ALTER TABLE loomplane_analyses DROP COLUMN IF EXISTS alias`,
	`ALTER TABLE loomplane_analyses DROP COLUMN IF EXISTS source_id`,
	`DROP TABLE IF EXISTS loomplane_project_sources`,
}
```

The variable update is idempotent on its own: once rewritten, the second segment is the alias, which can never be an ecosystem name.

- [ ] **Step 4: Run the test to see it pass**

Run: `LOOMPLANE_TEST_DATABASE_URL='postgres://loomplane:loomplane@localhost:55532/loomplane?sslmode=disable' go test -tags=integration ./database/migrations/ -run TestSources -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add database/migrations/migrations.go database/migrations/sources_test.go
git commit -m "feat: give projects a table of sources and attach each one's repository"
```

### Task 3: Adding, changing and removing sources

**Files:**
- Modify: `internal/discovery/analysis.go` (Store gains the source methods)
- Create: `internal/discovery/sources.go` (service methods, `Access`, `GitHubAccess`)
- Create: `internal/discovery/sourcestore.go` (PgStore source methods)
- Modify: `internal/discovery/service.go` (`access` field, `SetAccess`, `record` helper)
- Test: `internal/discovery/service_test.go`

**Interfaces:**
- Consumes: Task 1's `Source`, `ValidAlias`, `AliasFor`, `CleanRoot`, `RootsOverlap`, `ParseRepository`.
- Produces:
  - Store methods `CreateSource(ctx, *Source) error`, `GetSource(ctx, orgID, projectID, alias string) (*Source, error)`, `ListSources(ctx, orgID, projectID string, withRemoved bool) ([]*Source, error)`, `UpdateSource(ctx, *Source) error`, `RemoveSource(ctx, orgID, projectID, alias string, now time.Time) error`.
  - `type Access interface { Check(ctx, orgID string, repo repositories.Repository, ref string) error }` and `GitHubAccess{Tokens TokenSource; Fetcher Fetcher}`.
  - `(*Service).SetAccess(Access)`, `AddSource(ctx, p, projectID string, AddSourceInput) (*Source, error)`, `Sources(ctx, p, projectID) ([]*Source, error)`, `UpdateSource(ctx, p, projectID, alias string, UpdateSourceInput) (*Source, error)`, `RemoveSource(ctx, p, projectID, alias string) error`, `AttachRepository(ctx, p, *projects.Project, url string) (string, error)`.
  - `AddSourceInput{Repository, Alias, Root, Ref string}`, `UpdateSourceInput{Root, Ref *string}`.

- [ ] **Step 1: Give the test store sources, and write the failing tests**

In `internal/discovery/service_test.go`, add `sources map[string]*Source` to `memStore`, initialize it in `newMemStore`, and add:

```go
func (m *memStore) CreateSource(_ context.Context, s *Source) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.sources {
		if x.ProjectID == s.ProjectID && x.Alias == s.Alias {
			return ErrAliasTaken
		}
	}
	c := *s
	m.sources[s.ID] = &c
	return nil
}

func (m *memStore) findSource(orgID, projectID, alias string, live bool) *Source {
	for _, x := range m.sources {
		if x.OrgID == orgID && x.ProjectID == projectID && x.Alias == alias && (!live || x.RemovedAt == nil) {
			return x
		}
	}
	return nil
}

func (m *memStore) GetSource(_ context.Context, orgID, projectID, alias string) (*Source, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	x := m.findSource(orgID, projectID, alias, false)
	if x == nil {
		return nil, ErrSourceNotFound
	}
	c := *x
	return &c, nil
}

func (m *memStore) ListSources(_ context.Context, orgID, projectID string, withRemoved bool) ([]*Source, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Source{}
	for _, x := range m.sources {
		if x.OrgID == orgID && x.ProjectID == projectID && (withRemoved || x.RemovedAt == nil) {
			c := *x
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out, nil
}

func (m *memStore) UpdateSource(_ context.Context, s *Source) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	x := m.findSource(s.OrgID, s.ProjectID, s.Alias, true)
	if x == nil {
		return ErrSourceNotFound
	}
	x.Root, x.Ref, x.UpdatedAt = s.Root, s.Ref, s.UpdatedAt
	return nil
}

func (m *memStore) RemoveSource(_ context.Context, orgID, projectID, alias string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	x := m.findSource(orgID, projectID, alias, true)
	if x == nil {
		return ErrSourceNotFound
	}
	x.RemovedAt, x.UpdatedAt = &now, now
	return nil
}

// fakeAccess stands in for GitHub: it records what was checked and
// answers err.
type fakeAccess struct {
	err     error
	checked []string
}

func (a *fakeAccess) Check(_ context.Context, _ string, repo repositories.Repository, ref string) error {
	a.checked = append(a.checked, repo.FullName()+"@"+ref)
	return a.err
}

// source attaches a source straight through the store.
func (f *fixture) source(t *testing.T, projectID, alias, repo, root string) *Source {
	t.Helper()
	now := time.Now().UTC()
	s := &Source{
		ID: "src_" + projectID + "_" + alias, OrgID: org, ProjectID: projectID, Alias: alias,
		Repository: repo, Root: root, CreatedBy: "ausr_seed", CreatedAt: now, UpdatedAt: now,
	}
	if err := f.store.CreateSource(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return s
}
```

Add `access *fakeAccess` to `fixture`. In `newFixture`, set `access: &fakeAccess{}` and call `f.svc.SetAccess(f.access)` after `NewService`. Then add the tests:

```go
func TestSources_lifecycle(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	viewer := f.as(t, "ausr_viewer", security.RoleViewer)

	mono, err := f.svc.AddSource(ctx, dev, "proj_bare", AddSourceInput{Repository: "https://github.com/acme/mono.git"})
	if err != nil {
		t.Fatal(err)
	}
	if mono.Alias != "mono" || mono.Repository != "acme/mono" || mono.Root != "" || !strings.HasPrefix(mono.ID, "src_") {
		t.Fatalf("source %+v", mono)
	}
	web, err := f.svc.AddSource(ctx, dev, "proj_bare", AddSourceInput{Repository: "acme/site", Alias: "web", Root: "/apps/web/", Ref: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if web.Root != "apps/web" || web.Ref != "main" {
		t.Fatalf("source %+v", web)
	}
	list, err := f.svc.Sources(ctx, viewer, "proj_bare")
	if err != nil || len(list) != 2 || list[0].Alias != "mono" || list[1].Alias != "web" {
		t.Fatalf("Sources: %v, %+v", err, list)
	}

	ref := "release"
	web, err = f.svc.UpdateSource(ctx, dev, "proj_bare", "web", UpdateSourceInput{Ref: &ref})
	if err != nil || web.Ref != "release" || f.access.checked[len(f.access.checked)-1] != "acme/site@release" {
		t.Fatalf("UpdateSource: %v, %+v, checked %v", err, web, f.access.checked)
	}

	if err = f.svc.RemoveSource(ctx, dev, "proj_bare", "mono"); err != nil {
		t.Fatal(err)
	}
	list, _ = f.svc.Sources(ctx, viewer, "proj_bare")
	if len(list) != 1 || list[0].Alias != "web" {
		t.Fatalf("after removing mono: %+v", list)
	}
	// An alias is never reused, so an old plan's "mono" still means acme/mono.
	if _, err = f.svc.AddSource(ctx, dev, "proj_bare", AddSourceInput{Repository: "acme/other", Alias: "mono"}); !errors.Is(err, ErrAliasTaken) {
		t.Fatalf("re-adding a removed alias: %v", err)
	}
	var actions []string
	for _, e := range f.audit.events {
		actions = append(actions, e.Action)
	}
	if !slices.Equal(actions, []string{"source.add", "source.add", "source.update", "source.remove"}) {
		t.Fatalf("audit %v", actions)
	}
}

func TestSources_refusals(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	viewer := f.as(t, "ausr_viewer", security.RoleViewer)
	f.source(t, "proj_bare", "platform", "acme/mono", "services")
	tests := []struct {
		name    string
		p       security.Principal
		project string
		in      AddSourceInput
		want    error
	}{
		{"viewer", viewer, "proj_bare", AddSourceInput{Repository: "acme/site"}, security.ErrForbidden},
		{"another org's project", dev, "proj_elsewhere", AddSourceInput{Repository: "acme/site"}, projects.ErrNotFound},
		{"not GitHub", dev, "proj_bare", AddSourceInput{Repository: "https://gitlab.com/acme/site"}, ErrInvalidSource},
		{"reserved alias", dev, "proj_bare", AddSourceInput{Repository: "acme/site", Alias: "go"}, ErrAliasReserved},
		{"bad alias", dev, "proj_bare", AddSourceInput{Repository: "acme/site", Alias: "Web"}, ErrInvalidSource},
		{"root climbs out", dev, "proj_bare", AddSourceInput{Repository: "acme/site", Root: "../x"}, ErrInvalidSource},
		{"bad ref", dev, "proj_bare", AddSourceInput{Repository: "acme/site", Ref: "../../etc"}, ErrInvalidSource},
		{"nested root", dev, "proj_bare", AddSourceInput{Repository: "acme/mono", Alias: "api", Root: "services/api"}, ErrRootsOverlap},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := f.svc.AddSource(ctx, tt.p, tt.project, tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("AddSource: %v, want %v", err, tt.want)
			}
		})
	}
	// Another directory of the same repository is fine.
	if _, err := f.svc.AddSource(ctx, dev, "proj_bare", AddSourceInput{Repository: "acme/mono", Alias: "apps", Root: "apps"}); err != nil {
		t.Fatalf("a separate directory: %v", err)
	}
	f.access.err = repositories.ErrNotFound
	if _, err := f.svc.AddSource(ctx, dev, "proj_bare", AddSourceInput{Repository: "acme/private"}); !errors.Is(err, ErrSourceInaccessible) {
		t.Fatalf("a repository Loomplane can't read: %v", err)
	}
}
```

Add `"slices"` to the test file's imports.

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/discovery/ -run TestSources`
Expected: FAIL to compile: `f.svc.AddSource undefined`, `f.svc.SetAccess undefined`.

- [ ] **Step 3: Add the source methods to `Store`**

In `internal/discovery/analysis.go`, inside `type Store interface`, after `ActiveAnalysis`:

```go
	// CreateSource attaches a source. An alias the project has used before,
	// even on a removed source, is ErrAliasTaken.
	CreateSource(ctx context.Context, s *Source) error
	// GetSource returns a project's source by alias, removed or not, or
	// ErrSourceNotFound.
	GetSource(ctx context.Context, orgID, projectID, alias string) (*Source, error)
	// ListSources returns a project's sources by alias: the live ones, or
	// every one ever attached.
	ListSources(ctx context.Context, orgID, projectID string, withRemoved bool) ([]*Source, error)
	// UpdateSource writes a live source's root, ref and updated_at.
	UpdateSource(ctx context.Context, s *Source) error
	// RemoveSource marks a live source removed. Its row stays, so its
	// alias stays taken.
	RemoveSource(ctx context.Context, orgID, projectID, alias string, now time.Time) error
```

- [ ] **Step 4: Write `internal/discovery/sources.go`**

```go
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
	if err = ValidAlias(alias); err != nil {
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
	if err = s.check(ctx, p.OrgID, repo, ref); err != nil {
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
	if err = s.store.CreateSource(ctx, src); err != nil {
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
```

In `internal/discovery/service.go`, add `access Access` to the `Service` struct, and this helper:

```go
func (s *Service) record(ctx context.Context, p security.Principal, projectID, action, severity string, attrs map[string]any) {
	if s.audit != nil {
		s.audit.Record(ctx, projects.AuditEvent{
			Action: action, Severity: severity, ProjectID: projectID, Principal: p, Attributes: attrs,
		})
	}
}
```

- [ ] **Step 5: Write the Postgres methods in `internal/discovery/sourcestore.go`**

```go
package discovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const sourceColumns = `id, org_id, project_id, alias, repository, root, ref, created_by, created_at, updated_at, removed_at`

func scanSource(row pgx.Row) (*Source, error) {
	var s Source
	if err := row.Scan(&s.ID, &s.OrgID, &s.ProjectID, &s.Alias, &s.Repository, &s.Root, &s.Ref,
		&s.CreatedBy, &s.CreatedAt, &s.UpdatedAt, &s.RemovedAt); err != nil {
		return nil, err
	}
	s.normalize()
	return &s, nil
}

// CreateSource implements Store. The unique constraint on (project_id,
// alias) covers removed rows too, which is what keeps an alias from being
// reused.
func (s *PgStore) CreateSource(ctx context.Context, src *Source) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_project_sources (`+sourceColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		src.ID, src.OrgID, src.ProjectID, src.Alias, src.Repository, src.Root, src.Ref,
		src.CreatedBy, src.CreatedAt, src.UpdatedAt, src.RemovedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrAliasTaken
	}
	if err != nil {
		return fmt.Errorf("insert source: %w", err)
	}
	return nil
}

// GetSource implements Store.
func (s *PgStore) GetSource(ctx context.Context, orgID, projectID, alias string) (*Source, error) {
	src, err := scanSource(s.db.QueryRow(ctx, `SELECT `+sourceColumns+` FROM loomplane_project_sources
		WHERE org_id = $1 AND project_id = $2 AND alias = $3`, orgID, projectID, alias))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSourceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get source: %w", err)
	}
	return src, nil
}

// ListSources implements Store.
func (s *PgStore) ListSources(ctx context.Context, orgID, projectID string, withRemoved bool) ([]*Source, error) {
	rows, err := s.db.Query(ctx, `SELECT `+sourceColumns+` FROM loomplane_project_sources
		WHERE org_id = $1 AND project_id = $2 AND ($3 OR removed_at IS NULL) ORDER BY alias`, orgID, projectID, withRemoved)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Source{}
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		out = append(out, src)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	return out, nil
}

// UpdateSource implements Store.
func (s *PgStore) UpdateSource(ctx context.Context, src *Source) error {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_project_sources SET root = $4, ref = $5, updated_at = $6
		WHERE org_id = $1 AND project_id = $2 AND alias = $3 AND removed_at IS NULL`,
		src.OrgID, src.ProjectID, src.Alias, src.Root, src.Ref, src.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update source: %w", err)
	}
	return oneSource(res)
}

// RemoveSource implements Store.
func (s *PgStore) RemoveSource(ctx context.Context, orgID, projectID, alias string, now time.Time) error {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_project_sources SET removed_at = $4, updated_at = $4
		WHERE org_id = $1 AND project_id = $2 AND alias = $3 AND removed_at IS NULL`, orgID, projectID, alias, now)
	if err != nil {
		return fmt.Errorf("remove source: %w", err)
	}
	return oneSource(res)
}

func oneSource(res interface{ RowsAffected() (int64, error) }) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrSourceNotFound
	}
	return nil
}
```

If `s.db.Exec` returns a result type without `RowsAffected`, follow `requireOneRow` in `internal/projects/pgstore.go`, which works against the same driver.

- [ ] **Step 6: Run the tests to see them pass**

Run: `go test ./internal/discovery/`
Expected: PASS, including the existing analysis tests.

- [ ] **Step 7: Commit**

```bash
git add internal/discovery/
git commit -m "feat: add, change and remove a project's sources, checking access first"
```

### Task 4: Analyses per source

**Files:**
- Modify: `internal/discovery/analysis.go` (Analysis fields, `ActiveAnalysis` per source, `ListSourceAnalyses`, error text)
- Modify: `internal/discovery/service.go` (`StartAnalysisInput`, `Start`)
- Modify: `internal/discovery/pgstore.go`
- Test: `internal/discovery/service_test.go`

**Interfaces:**
- Consumes: Task 3's source store methods.
- Produces:
  - `Analysis.SourceID string` (`json:"source_id"`) and `Analysis.Source string` (`json:"source"`, the alias).
  - `Store.ActiveAnalysis(ctx, orgID, sourceID string, since time.Time)` and `Store.ListSourceAnalyses(ctx, orgID, sourceID string, limit int) ([]*Analysis, error)`.
  - `StartAnalysisInput{Sources []string; Ref string}` and `(*Service).Start(...) ([]*Analysis, error)`.
  - `ErrNoRepository` is gone: callers use `ErrNoSources` from Task 1.

- [ ] **Step 1: Update the test store and write the failing tests**

In `service_test.go`:

- `memStore.ActiveAnalysis` matches `a.SourceID == sourceID` instead of the project.
- Add:

```go
func (m *memStore) ListSourceAnalyses(_ context.Context, orgID, sourceID string, limit int) ([]*Analysis, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Analysis{}
	for _, a := range m.analyses {
		if a.OrgID == orgID && a.SourceID == sourceID {
			c := *a
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
```

- In `newFixture`, drop `RepositoryURL` from every `fakeProjects` entry and delete `proj_lab`. After `f.svc.SetAccess(...)`, attach `f.source(t, project, "mono", "acme/mono", "")`.
- Every `a, err := f.svc.Start(...)` becomes `as, err := f.svc.Start(...)` followed by `a := as[0]`.
- In `TestService_startQueuesAnAnalysis`, also assert `a.Source == "mono" && a.SourceID == "src_proj_mono_mono"`.
- In `TestService_startRefusals`, give the table a `sources []string` column. Change the rows to: `{"no repository", dev, "proj_bare", nil, "", ErrNoSources}`, delete the `not GitHub` row, and add `{"unknown source", dev, project, []string{"nope"}, "", ErrSourceNotFound}`. Pass `StartAnalysisInput{Sources: tt.sources, Ref: tt.ref}`.

Then add:

```go
func TestService_startAnalyzesEachSource(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	web := f.source(t, project, "web", "acme/site", "")
	web.Ref = "develop"
	if err := f.store.UpdateSource(ctx, web); err != nil {
		t.Fatal(err)
	}
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)

	if _, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{Ref: "main"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a ref with two sources: %v", err)
	}
	all, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Source != "mono" || all[1].Source != "web" || all[1].Ref != "develop" || len(f.queue.ids) != 2 {
		t.Fatalf("analyses %+v, queued %v", all, f.queue.ids)
	}
}

func TestService_oneRunningSourceRefusesTheWholeRequest(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	f.source(t, project, "web", "acme/site", "")
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)

	if _, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{Sources: []string{"mono"}}); err != nil {
		t.Fatal(err)
	}
	// mono is still running, so analyzing everything is refused, and web
	// isn't queued on its own either.
	if _, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{}); !errors.Is(err, ErrAlreadyActive) {
		t.Fatalf("Start while mono runs: %v", err)
	}
	if len(f.queue.ids) != 1 {
		t.Fatalf("queued %v", f.queue.ids)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/discovery/`
Expected: FAIL to compile (`Start` returns one analysis, `Analysis.Source` undefined).

- [ ] **Step 3: Change the analysis model**

In `analysis.go`:

```go
	// SourceID is the source the analysis read, and Source its alias.
	SourceID string `json:"source_id"`
	Source   string `json:"source"`
```

Add these after `ProjectID` in `Analysis`. Remove `ErrNoRepository`. Change `ErrAlreadyActive` to `errors.New("an analysis of that repository is already running")`. In `Store`, change `ActiveAnalysis`'s parameter `projectID` to `sourceID`, update its comment ("the source's analysis that's still running"), and add:

```go
	// ListSourceAnalyses returns a source's latest analyses, newest first.
	ListSourceAnalyses(ctx context.Context, orgID, sourceID string, limit int) ([]*Analysis, error)
```

- [ ] **Step 4: Rewrite `Start`**

In `service.go`, replace `StartAnalysisInput` and `Start`:

```go
// StartAnalysisInput is what a caller sends to analyze a project's
// repositories.
type StartAnalysisInput struct {
	// Sources are the aliases to analyze. Empty means every source.
	Sources []string `json:"sources,omitempty"`
	// Ref is a branch, tag or commit, for one source only. Empty means the
	// source's tracked branch.
	Ref string `json:"ref,omitempty"`
}

// Start queues one analysis per source asked for. If any of them is still
// being analyzed, nothing is queued: half a request is harder to reason
// about than none.
func (s *Service) Start(ctx context.Context, p security.Principal, projectID string, in StartAnalysisInput) ([]*Analysis, error) {
	if err := s.authz.Require(ctx, p, security.AnalysisRun, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	live, err := s.store.ListSources(ctx, p.OrgID, projectID, false)
	if err != nil {
		return nil, err
	}
	if len(live) == 0 {
		return nil, ErrNoSources
	}
	picked, err := pick(live, in.Sources)
	if err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(in.Ref)
	if ref != "" {
		if len(picked) != 1 {
			return nil, fmt.Errorf("%w: a ref is a branch of one repository, so name one source with it", ErrInvalid)
		}
		if !repositories.ValidRef(ref) {
			return nil, fmt.Errorf("%w: %q isn't a branch, tag or commit name", ErrInvalid, ref)
		}
	}
	now := s.now().UTC()
	for _, src := range picked {
		_, err := s.store.ActiveAnalysis(ctx, p.OrgID, src.ID, now.Add(-activeWindow))
		switch {
		case err == nil:
			return nil, fmt.Errorf("%w: %s", ErrAlreadyActive, src.Alias)
		case !errors.Is(err, ErrNotFound):
			return nil, err
		}
	}
	out := make([]*Analysis, 0, len(picked))
	for _, src := range picked {
		target := ref
		if target == "" {
			target = src.Ref
		}
		a, err := s.queue(ctx, p, src, target, now)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// pick finds the sources aliases name, in alias order. No aliases means all
// of them.
func pick(live []*Source, aliases []string) ([]*Source, error) {
	if len(aliases) == 0 {
		return live, nil
	}
	by := map[string]*Source{}
	for _, src := range live {
		by[src.Alias] = src
	}
	seen := map[string]bool{}
	var out []*Source
	for _, a := range aliases {
		a = strings.TrimSpace(a)
		src := by[a]
		if src == nil {
			return nil, fmt.Errorf("%w: this project has no repository called %q", ErrSourceNotFound, a)
		}
		if !seen[a] {
			seen[a] = true
			out = append(out, src)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out, nil
}

// queue creates one analysis and its job.
func (s *Service) queue(ctx context.Context, p security.Principal, src *Source, ref string, now time.Time) (*Analysis, error) {
	id, err := typeid.Generate("anl")
	if err != nil {
		return nil, fmt.Errorf("generate analysis id: %w", err)
	}
	a := &Analysis{
		ID: id.String(), OrgID: p.OrgID, ProjectID: src.ProjectID, SourceID: src.ID, Source: src.Alias,
		Status: StatusQueued, Repository: src.Repository, Ref: ref, CreatedBy: p.UserID, CreatedAt: now,
	}
	if err = s.store.CreateAnalysis(ctx, a); err != nil {
		return nil, err
	}
	if err = s.enqueue.AnalyzeRepository(ctx, a.OrgID, a.ID); err != nil {
		// Without its job the analysis would sit queued, blocking the next.
		if derr := s.store.DeleteAnalysis(context.WithoutCancel(ctx), a.OrgID, a.ID); derr != nil {
			err = errors.Join(err, fmt.Errorf("remove the analysis again: %w", derr))
		}
		return nil, fmt.Errorf("start analysis: %w", err)
	}
	s.record(ctx, p, src.ProjectID, "analysis.start", "info",
		map[string]any{"analysis_id": a.ID, "source": src.Alias, "repository": a.Repository, "ref": a.Ref})
	return a, nil
}
```

Add `"sort"` to the imports. Remove the `ParseGitHubURL` use and the `proj.RepositoryURL` check.

- [ ] **Step 5: Update `PgStore`**

In `pgstore.go`, read and write the two new columns. `source_id` is nullable, since an analysis of a project whose URL wasn't on GitHub has none:

```go
const analysisColumns = `id, org_id, project_id, COALESCE(source_id, ''), alias, status, repository, ref, commit_sha, error, summary,
	created_by, created_at, started_at, finished_at`

const analysisInsertColumns = `id, org_id, project_id, source_id, alias, status, repository, ref, commit_sha, error, summary,
	created_by, created_at, started_at, finished_at`
```

- `scanAnalysis` scans `&a.SourceID, &a.Source` after `&a.ProjectID`.
- `CreateAnalysis` inserts into `(`+analysisInsertColumns+`)` with 15 placeholders, passing `a.SourceID, a.Source` after `a.ProjectID`.
- `ActiveAnalysis` filters `source_id = $2` instead of `project_id = $2`.
- `ListSourceAnalyses` is `ListAnalyses` with `source_id = $2`.

- [ ] **Step 6: Run the tests to see them pass**

Run: `go test ./internal/discovery/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/discovery/
git commit -m "feat: analyze each of a project's sources on its own"
```

### Task 5: Projects name their repositories

**Files:**
- Modify: `internal/projects/project.go`, `internal/projects/service.go`, `internal/projects/pgstore.go`
- Modify: `extensions/projects/extension.go` (`fail`)
- Test: `internal/projects/service_test.go`

**Interfaces:**
- Produces:
  - `Project.Repositories []string` (`json:"repositories"`); `Project.RepositoryURL` is gone.
  - `UpdateProjectInput.RepositoryURL` is gone. `CreateProjectInput.RepositoryURL` stays.
  - `type Attacher interface { AttachRepository(ctx context.Context, p security.Principal, proj *Project, repositoryURL string) (string, error) }` and `(*Service).SetAttacher(Attacher)`.
  - `ErrRepository`.

- [ ] **Step 1: Write the failing tests**

In `internal/projects/service_test.go`, add:

```go
// attachRecorder stands in for discovery: it attaches every repository as
// ada/engine, or fails with err.
type attachRecorder struct {
	urls []string
	err  error
}

func (a *attachRecorder) AttachRepository(_ context.Context, _ security.Principal, proj *Project, url string) (string, error) {
	if a.err != nil {
		return "", a.err
	}
	a.urls = append(a.urls, proj.ID+" "+url)
	return "ada/engine", nil
}
```

Add `attach *attachRecorder` to `fixture`. In `newFixture`, create it and call `svc.SetAttacher(attach)`. In `TestService_createInitializeAndRead`, after the create that passes `RepositoryURL: "git@github.com:ada/engine.git"`, assert:

```go
	if !slices.Equal(p.Repositories, []string{"ada/engine"}) || len(f.attach.urls) != 1 {
		t.Fatalf("repositories %v, attached %v", p.Repositories, f.attach.urls)
	}
```

Delete any assertion on `p.RepositoryURL`, and any `UpdateProjectInput{RepositoryURL: ...}` in the file (`grep -n RepositoryURL internal/projects/service_test.go` lists them). Then add:

```go
func TestService_createRemovesTheProjectWhenItsRepositoryCantBeAttached(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	f.attach.err = errors.New("Loomplane can't read that repository")

	_, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Engine", RepositoryURL: "https://github.com/ada/private"})
	if !errors.Is(err, ErrRepository) {
		t.Fatalf("Create: %v", err)
	}
	list, err := f.svc.List(ctx, dev)
	if err != nil || len(list) != 0 {
		t.Fatalf("a half-made project was left behind: %v, %+v", err, list)
	}
}

func TestService_aRepositoryNeedsAnAttacher(t *testing.T) {
	f := newFixture(t)
	f.svc.SetAttacher(nil)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	if _, err := f.svc.Create(context.Background(), dev, CreateProjectInput{Name: "Engine", RepositoryURL: "https://github.com/ada/engine"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Create: %v", err)
	}
}
```

Adjust the `f.principal` call and `List` signature to match the helpers in the file if they differ.

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/projects/`
Expected: FAIL to compile (`SetAttacher`, `Repositories` and `ErrRepository` undefined).

- [ ] **Step 3: Change the model and the service**

In `project.go`, replace the `RepositoryURL` field:

```go
	// Repositories are the "owner/name" of each repository the project
	// builds from, by alias. Its sources belong to discovery; the store
	// reads them so a list of projects can show them.
	Repositories []string `json:"repositories"`
```

Add to the errors:

```go
	ErrRepository = errors.New("the repository couldn't be attached")
```

In `service.go`:

```go
// Attacher attaches the repository a new project was created with, as its
// first source, and returns its "owner/name". discovery's Service is the
// real one; projects can't import it, since it imports projects.
type Attacher interface {
	AttachRepository(ctx context.Context, p security.Principal, proj *Project, repositoryURL string) (string, error)
}

// SetAttacher sets the Attacher. Call it while the app registers.
func (s *Service) SetAttacher(a Attacher) { s.attacher = a }
```

Add `attacher Attacher` to the `Service` struct. Change the `CreateProjectInput.RepositoryURL` comment to "is attached as the project's first source". Delete `RepositoryURL` from `UpdateProjectInput` and its branch in `Update`. Update the `Update` doc comment and the `projects.update` route summary in `extensions/projects/routes.go` to drop "repository". In `build`, keep validating the URL but stop putting it on the `Project`. In `Create`:

```go
	repo := strings.TrimSpace(in.RepositoryURL)
	if repo != "" && s.attacher == nil {
		return nil, fmt.Errorf("%w: this Loomplane can't attach repositories", ErrInvalid)
	}
```

Put that before `s.build`. Then, between `s.store.CreateProject` and `s.init.InitializeProject`:

```go
	if repo != "" {
		full, err := s.attacher.AttachRepository(ctx, p, proj, repo)
		if err != nil {
			// A project that can't read its own repository is worse than
			// none, so take it back out.
			if derr := s.store.DeleteProject(context.WithoutCancel(ctx), proj.OrgID, proj.ID); derr != nil {
				err = errors.Join(err, fmt.Errorf("remove the project again: %w", derr))
			}
			return nil, fmt.Errorf("%w: %w", ErrRepository, err)
		}
		proj.Repositories = []string{full}
	}
```

In `pgstore.go`, `normalize` fills a nil `Repositories` with `[]string{}`.

- [ ] **Step 4: Read repositories in the store**

In `internal/projects/pgstore.go`:

```go
const projectColumns = `id, org_id, name, slug, description, icon, color, status, created_by, created_at, updated_at`

// projectSelect reads a project with the repositories its live sources
// name. The sources table is discovery's; reading it here saves the
// dashboard a request per project.
const projectSelect = `SELECT p.id, p.org_id, p.name, p.slug, p.description, p.icon, p.color, p.status,
	p.created_by, p.created_at, p.updated_at,
	COALESCE((SELECT array_agg(s.repository ORDER BY s.alias) FROM loomplane_project_sources s
		WHERE s.project_id = p.id AND s.removed_at IS NULL), '{}')
	FROM loomplane_projects p`

func scanProject(row pgx.Row) (*Project, error) {
	var p Project
	if err := row.Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.Description, &p.Icon, &p.Color, &p.Status,
		&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.Repositories); err != nil {
		return nil, err
	}
	p.normalize()
	return &p, nil
}
```

- `CreateProject`'s INSERT uses the 11 `projectColumns` and 11 placeholders, without `p.RepositoryURL`.
- `GetProject` is `scanProject(s.db.QueryRow(ctx, projectSelect+` WHERE p.org_id = $1 AND p.id = $2`, orgID, id))`, with the same not-found handling.
- `ListProjects` queries `projectSelect+` WHERE p.org_id = $1 ORDER BY p.created_at DESC, p.id DESC`` and scans each row with `scanProject`.
- `UpdateProject` drops `repository_url = $7` and renumbers the rest.

- [ ] **Step 5: Map the new error**

In `extensions/projects/extension.go`'s `fail`, before the `ErrInvalid` case:

```go
	case errors.Is(err, projects.ErrRepository):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
```

- [ ] **Step 6: Run the tests to see them pass**

Run: `go test ./internal/projects/ ./internal/discovery/`
Expected: PASS. Integration tests that read `repository_url` from a response fail until Task 6 updates them; that's expected.

- [ ] **Step 7: Commit**

```bash
git add internal/projects/ extensions/projects/
git commit -m "feat: list a project's repositories and attach the first when it's created"
```

### Task 6: The sources API

**Files:**
- Modify: `extensions/discovery/routes.go`, `extensions/discovery/extension.go`
- Modify: `tests/integration/discovery_test.go`, `tests/integration/planning_test.go`, and any other test that reads `repository_url` from a response
- Create: `tests/integration/sources_test.go`

**Interfaces:**
- Consumes: Tasks 3 to 5.
- Produces:
  - Routes `sources.list` (GET `/api/v1/projects/:id/sources`, `SourceList{Sources []discovery.Source}`), `sources.create` (POST, 201 `Source`), `sources.update` (PATCH `/projects/:id/sources/:alias`, 200 `Source`), `sources.delete` (DELETE, 204).
  - `analyses.create` now answers 201 with `AnalysisList`.
  - Test helpers `fakeGitHubRepos(t, map[string][]byte)`, `tarball(t, top string, files map[string]string) []byte`, `analyzeAll(t, cfg, c, projectID) []analysisBody`.

- [ ] **Step 1: Write the failing integration test**

In `tests/integration/discovery_test.go`, replace `fakeGitHubServing`:

```go
// fakeGitHubRepos serves each tarball as the repository its key names, on
// a main branch at exampleSHA, the way GitHub's API serves a public
// repository.
func fakeGitHubRepos(t *testing.T, repos map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, tgz := range repos {
			switch r.URL.Path {
			case "/repos/" + name:
				_, _ = w.Write([]byte(`{"default_branch":"main"}`))
				return
			case "/repos/" + name + "/commits/main":
				_, _ = w.Write([]byte(exampleSHA))
				return
			case "/repos/" + name + "/tarball/" + exampleSHA:
				_, _ = w.Write(tgz)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeGitHubServing serves examples/polyglot as acme/mono.
func fakeGitHubServing(t *testing.T) *httptest.Server {
	t.Helper()
	return fakeGitHubRepos(t, map[string][]byte{"acme/mono": exampleTarball(t)})
}

// tarball packs files the way GitHub's tarball endpoint does: under one
// top directory.
func tarball(t *testing.T, top string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: top + "/" + name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// siteFiles is a Next.js app in a repository of its own, which uses a
// package published from elsewhere.
var siteFiles = map[string]string{
	"package.json": `{"name": "site", "scripts": {"build": "next build", "start": "next start"},
  "dependencies": {"next": "15.0.0", "react": "19.0.0", "@acme/ui": "^1.0.0"}}`,
	"app/page.tsx": "export default function Page() { return null }\n",
}
```

If the Node detector finds no service in `siteFiles`, add what it reads for a Next.js app (a `next.config.js`, say) until `go test ./internal/discovery/` agrees on a fixture with the same files. Tasks 13 and 20 rely on a service at `.` in this repository.

Add `Source string `json:"source"`` to `analysisBody`. In `planning_test.go`, replace the body of `analyze` with a call to a new helper and add it:

```go
// analyze runs one analysis of each of a project's sources with a worker
// of its own, and stops the worker once they're done.
func analyze(t *testing.T, cfg config.Config, c *client, projectID string) {
	t.Helper()
	analyzeAll(t, cfg, c, projectID)
}

func analyzeAll(t *testing.T, cfg config.Config, c *client, projectID string) []analysisBody {
	t.Helper()
	worker, err := platform.NewWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	if err = worker.Start(ctx); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	defer func() {
		if serr := worker.Stop(context.Background()); serr != nil {
			t.Errorf("stop worker: %v", serr)
		}
	}()
	var list struct {
		Analyses []analysisBody `json:"analyses"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+projectID+"/analyses", nil, &list)
	deadline := time.Now().Add(readyTimeout)
	for i := range list.Analyses {
		a := &list.Analyses[i]
		for a.Status != "succeeded" && a.Status != "failed" {
			if time.Now().After(deadline) {
				t.Fatalf("analysis of %s still %s after %s", a.Source, a.Status, readyTimeout)
			}
			time.Sleep(100 * time.Millisecond)
			c.must(http.StatusOK, http.MethodGet, "/api/v1/analyses/"+a.ID, nil, a)
		}
		if a.Status != "succeeded" {
			t.Fatalf("analysis of %s failed: %s", a.Source, a.Error)
		}
	}
	return list.Analyses
}
```

In `TestDiscovery_theWorkerAnalyzesAProjectsRepository`:
- The `Lab` project with a GitLab URL is now refused when it's created: `c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Lab", "repository_url": "https://gitlab.com/acme/lab"}, nil)`. Delete the line that analyzed it.
- The first `POST .../analyses` decodes into `var queued struct{ Analyses []analysisBody `json:"analyses"` }`, and `a := queued.Analyses[0]`.

Create `tests/integration/sources_test.go`:

```go
//go:build integration

package integration

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

type sourceBody struct {
	ID         string `json:"id"`
	Alias      string `json:"alias"`
	Repository string `json:"repository"`
	Root       string `json:"root"`
	Ref        string `json:"ref"`
}

// signedIn signs up, makes an organization and switches to it.
func signedIn(t *testing.T, c *client) {
	t.Helper()
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
}

func TestSources_aProjectBuildsFromSeveralRepositories(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubRepos(t, map[string][]byte{
		"acme/mono": exampleTarball(t),
		"acme/site": tarball(t, "acme-site-fedcba9", siteFiles),
	}).URL
	cfg.Analysis.WorkDir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)

	var proj struct {
		ID           string   `json:"id"`
		Repositories []string `json:"repositories"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Shop", "repository_url": "https://github.com/acme/mono"}, &proj)
	if !slices.Equal(proj.Repositories, []string{"acme/mono"}) {
		t.Fatalf("new project's repositories %v", proj.Repositories)
	}
	base := "/api/v1/projects/" + proj.ID + "/sources"

	var site sourceBody
	c.must(http.StatusCreated, http.MethodPost, base, map[string]string{"repository": "acme/site", "alias": "site"}, &site)
	if site.Alias != "site" || site.Repository != "acme/site" {
		t.Fatalf("site %+v", site)
	}
	// One the fake GitHub doesn't serve can't be read, so it isn't added.
	c.must(http.StatusUnprocessableEntity, http.MethodPost, base, map[string]string{"repository": "acme/nowhere"}, nil)
	c.must(http.StatusConflict, http.MethodPost, base, map[string]string{"repository": "acme/other", "alias": "mono"}, nil)
	c.must(http.StatusConflict, http.MethodPost, base, map[string]string{"repository": "acme/mono", "alias": "api", "root": "services"}, nil)

	var list struct {
		Sources []sourceBody `json:"sources"`
	}
	c.must(http.StatusOK, http.MethodGet, base, nil, &list)
	if len(list.Sources) != 2 || list.Sources[0].Alias != "mono" || list.Sources[1].Alias != "site" {
		t.Fatalf("sources %+v", list.Sources)
	}

	analyses := analyzeAll(t, cfg, c, proj.ID)
	if len(analyses) != 2 || analyses[0].Source != "mono" || analyses[1].Source != "site" || analyses[1].Summary.Services == 0 {
		t.Fatalf("analyses %+v", analyses)
	}

	c.must(http.StatusOK, http.MethodPatch, base+"/site", map[string]string{"ref": "main"}, &site)
	if site.Ref != "main" {
		t.Fatalf("updated site %+v", site)
	}
	c.must(http.StatusNoContent, http.MethodDelete, base+"/site", nil, nil)
	c.must(http.StatusNotFound, http.MethodDelete, base+"/site", nil, nil)
	c.must(http.StatusConflict, http.MethodPost, base, map[string]string{"repository": "acme/site", "alias": "site"}, nil)
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+proj.ID, nil, &proj)
	if !slices.Equal(proj.Repositories, []string{"acme/mono"}) {
		t.Fatalf("repositories after removing site %v", proj.Repositories)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `make up`, then `LOOMPLANE_TEST_DATABASE_URL='postgres://loomplane:loomplane@localhost:55532/loomplane?sslmode=disable' go test -tags=integration -count=1 ./tests/integration/ -run TestSources`
Expected: FAIL to compile, or 404 on `/sources`.

- [ ] **Step 3: Add the routes**

In `extensions/discovery/routes.go`:

```go
// SourceList is the body of GET /api/v1/projects/:id/sources.
type SourceList struct {
	Sources []discovery.Source `json:"sources"`
}

// ForgeEnvelope tells the generated client the list wraps Sources.
func (SourceList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "sources"}
}
```

At the start of `routes`, after the analyses group is declared, register a second group:

```go
	s := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Sources"))
	if err := s.GET("/projects/:id/sources", e.listSources,
		forge.WithName("sources.list"),
		forge.WithSummary("The repositories the project builds from, by alias"),
		forge.WithResponseSchema(http.StatusOK, "Sources", SourceList{}),
	); err != nil {
		return err
	}
	if err := s.POST("/projects/:id/sources", e.addSource,
		forge.WithName("sources.create"),
		forge.WithSummary("Attach a repository, or a directory of one, after checking Loomplane can read it"),
		forge.WithRequestSchema(discovery.AddSourceInput{}),
		forge.WithResponseSchema(http.StatusCreated, "The source", discovery.Source{}),
	); err != nil {
		return err
	}
	if err := s.PATCH("/projects/:id/sources/:alias", e.updateSource,
		forge.WithName("sources.update"),
		forge.WithSummary("Change the directory or branch a source reads"),
		forge.WithRequestSchema(discovery.UpdateSourceInput{}),
		forge.WithResponseSchema(http.StatusOK, "The source", discovery.Source{}),
	); err != nil {
		return err
	}
	if err := s.DELETE("/projects/:id/sources/:alias", e.removeSource,
		forge.WithName("sources.delete"),
		forge.WithSummary("Detach a source. Its alias stays taken, so old plans keep their meaning."),
		forge.WithNoContentResponse(),
		forge.WithEntity(forge.EntityDef{Type: "Source", IDField: "id"}),
	); err != nil {
		return err
	}
```

Change `analyses.create`'s response to `forge.WithResponseSchema(http.StatusCreated, "The queued analyses, one per source", AnalysisList{})` and its summary to "Analyze the project's repositories, or the ones named". The `start` handler builds an `AnalysisList` from the slice, the way `list` does, and replies 201 with it. Its bad-JSON message becomes `the body must be JSON: {"sources": ["web"], "ref": "main"}`. The handlers:

```go
func (e *Extension) listSources(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	list, err := e.svc.Sources(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	out := SourceList{Sources: make([]discovery.Source, 0, len(list))}
	for _, s := range list {
		out.Sources = append(out.Sources, *s)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) addSource(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in discovery.AddSourceInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"repository": "acme/web"}`)
	}
	src, err := e.svc.AddSource(c.Request().Context(), p, c.Param("id"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusCreated, src)
}

func (e *Extension) updateSource(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in discovery.UpdateSourceInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, `the body must be JSON: {"ref": "main"}`)
	}
	src, err := e.svc.UpdateSource(c.Request().Context(), p, c.Param("id"), c.Param("alias"), in)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, src)
}

func (e *Extension) removeSource(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err = e.svc.RemoveSource(c.Request().Context(), p, c.Param("id"), c.Param("alias")); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}
```

- [ ] **Step 4: Map the errors and wire it up**

In `extension.go`'s `fail`:
- add `errors.Is(err, discovery.ErrSourceNotFound)` to the 404 case;
- add a case `errors.Is(err, discovery.ErrAliasTaken), errors.Is(err, discovery.ErrRootsOverlap)` returning 409;
- replace `discovery.ErrNoRepository` with `discovery.ErrNoSources` in the 422 case, and add `ErrAliasReserved`, `ErrInvalidSource` and `ErrSourceInaccessible` to it.

In `Register`, after `e.github` is built:

```go
	e.svc.SetAccess(discovery.GitHubAccess{Tokens: e.github, Fetcher: repositories.NewGitHub(e.cfg.GitHub.APIURL)})
	projectSvc, err := forge.InjectType[*projects.Service](c)
	if err != nil {
		return fmt.Errorf("discovery needs the projects extension: %w", err)
	}
	projectSvc.SetAttacher(e.svc)
```

- [ ] **Step 5: Run every integration test**

Run: `make test-integration`
Expected: PASS. A test that creates a project with `repository_url` needs `cfg.GitHub.APIURL` pointing at a fake that serves the repository. `environments_test.go` and `github_test.go` already do. Add it to any test that fails with `source-inaccessible`.

- [ ] **Step 6: Commit**

```bash
git add extensions/discovery/ tests/integration/
git commit -m "feat: serve a project's sources and analyze them together or one at a time"
```

### Task 7: The dashboard shows and manages sources

**Files:**
- Regenerate: `apps/dashboard/specs/loomplane.openapi.json`, `apps/dashboard/src/client/forge/loomplane/**`
- Modify: `apps/dashboard/src/lib/format.ts`, `apps/dashboard/src/lib/architecture.ts`, `apps/dashboard/src/lib/lib.test.ts`, `apps/dashboard/src/lib/architecture.test.ts`
- Create: `apps/dashboard/src/components/sources.tsx`
- Modify: `apps/dashboard/src/components/architecture.tsx`, `apps/dashboard/src/pages/project-detail.tsx`, `apps/dashboard/src/pages/projects.tsx`, `apps/dashboard/src/components/app-sidebar.tsx`, `apps/dashboard/src/components/command-menu.tsx`, `apps/dashboard/src/components/new-project-dialog.tsx`, `apps/dashboard/src/client/runtime.test.ts`

**Interfaces:**
- Consumes: Task 6's routes, as the generated hooks `useSourcesList`, `useSourcesCreate`, `useSourcesUpdate`, `useSourcesDelete` and types `Source`, `SourceList`. Check the names in `client/forge/loomplane/hooks.ts` after regenerating.
- Produces: `repositoriesLabel(repos: string[]): string`, `latestBySource(analyses: Analysis[]): Map<string, Analysis>`, `SourcesCard`, `AddSourceDialog`.

- [ ] **Step 1: Regenerate the client**

Run: `make up`, then `make dev-api` in another terminal, then `make gen-client`.
Expected: `types.ts` has `Source`, `Project.repositories: string[]`, `Analysis.source`. `pnpm --dir apps/dashboard typecheck` now lists every place still reading `repositoryUrl`.

- [ ] **Step 2: Write the failing tests**

In `apps/dashboard/src/lib/lib.test.ts`:

```ts
import { repositoriesLabel } from "@/lib/format"

describe("repositoriesLabel", () => {
  it.each([
    [[], ""],
    [["acme/web"], "acme/web"],
    [["acme/web", "acme/api", "acme/kit"], "acme/web +2"],
  ])("repositoriesLabel(%j) is %j", (repos, want) => {
    expect(repositoriesLabel(repos)).toBe(want)
  })
})
```

In `apps/dashboard/src/lib/architecture.test.ts`:

```ts
import { latestBySource } from "@/lib/architecture"

describe("latestBySource", () => {
  it("keeps each source's newest analysis from a list newest first", () => {
    const list = [
      { id: "3", source: "web", status: "queued" },
      { id: "2", source: "api", status: "succeeded" },
      { id: "1", source: "web", status: "succeeded" },
    ] as Analysis[]
    const got = latestBySource(list)
    expect([...got.entries()].map(([k, a]) => `${k}:${a.id}`)).toEqual([
      "web:3",
      "api:2",
    ])
  })
})
```

In `client/runtime.test.ts`, change the fixture's `repository_url: "https://github.com/a/b"` to `repositories: ["a/b"]` and its expectation to `expect(list.projects[0]?.repositories).toEqual(["a/b"])`.

- [ ] **Step 3: Run them to see them fail**

Run: `pnpm --dir apps/dashboard test`
Expected: FAIL, `repositoriesLabel` and `latestBySource` are not exported.

- [ ] **Step 4: Add the helpers**

In `lib/format.ts`:

```ts
/**
 * repositoriesLabel names a project's repositories in the room one takes:
 * the first, and how many more.
 */
export function repositoriesLabel(repos: string[]): string {
  if (repos.length === 0) return ""
  return repos.length === 1 ? repos[0] : `${repos[0]} +${repos.length - 1}`
}
```

In `lib/architecture.ts`:

```ts
/** latestBySource is each source's newest analysis, from a list newest first. */
export function latestBySource(analyses: Analysis[]): Map<string, Analysis> {
  const out = new Map<string, Analysis>()
  for (const a of analyses) if (!out.has(a.source)) out.set(a.source, a)
  return out
}
```

In `components/architecture.tsx`, `useAnalyses` sets `running` from every source, not only the newest analysis:

```ts
  const running = [...latestBySource(list).values()].some((a) =>
    isActive(a.status)
  )
```

- [ ] **Step 5: Write `components/sources.tsx`**

```tsx
// The repositories a project builds from: each one with what was last read
// from it, the controls that analyze one or all of them, and the dialog
// that adds another.
import {
  GitBranchIcon,
  PlusIcon,
  ScanSearchIcon,
  Trash2Icon,
} from "lucide-react"
import { type FormEvent, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useAnalysesCreate,
  useSourcesCreate,
  useSourcesDelete,
  useSourcesList,
} from "@/client/forge/loomplane/hooks"
import type { Project, Source } from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { useAnalyses } from "@/components/architecture"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  isActive,
  latestBySource,
  shortSHA,
  stepLabel,
} from "@/lib/architecture"
import { githubRepo } from "@/lib/format"
import { useOrganization } from "@/lib/organization"

/** sourceURL links to a source on GitHub, at its directory when it has one. */
function sourceURL(s: Source): string {
  const base = `https://github.com/${s.repository}`
  return s.root ? `${base}/tree/${s.ref || "HEAD"}/${s.root}` : base
}

/** SourcesCard lists a project's repositories, with what was last read from each. */
export function SourcesCard({ project }: { project: Project }) {
  const { can } = useOrganization()
  const sources = useQuery(useSourcesList, { path: { id: project.id } })
  const analyses = useAnalyses(project.id)
  const start = useMutation(useAnalysesCreate)
  const [adding, setAdding] = useState(false)
  const [removing, setRemoving] = useState<Source | null>(null)
  const latest = latestBySource(analyses.list)
  const list = sources.data?.sources ?? []
  const mayEdit = can("project:update")
  const mayRun = can("analysis:run")

  const analyze = (alias?: string) =>
    start
      .mutateAsync({
        path: { id: project.id },
        body: alias ? { sources: [alias] } : {},
      })
      .catch((err: unknown) =>
        toast.error(messageOf(err, "The analysis didn't start."))
      )

  return (
    <Card className="pb-0">
      <CardHeader>
        <CardTitle>Repositories</CardTitle>
        <CardDescription>
          Everything this project builds from. Each one is analyzed on its own.
        </CardDescription>
        <CardAction className="flex items-center gap-2">
          {mayRun && list.length > 1 && (
            <Button
              size="sm"
              variant="outline"
              className="rounded-full"
              disabled={analyses.running || start.isPending}
              onClick={() => void analyze()}
            >
              <ScanSearchIcon />
              Analyze all
            </Button>
          )}
          {mayEdit && (
            <Button
              size="sm"
              className="rounded-full"
              onClick={() => setAdding(true)}
            >
              <PlusIcon />
              Add
            </Button>
          )}
        </CardAction>
      </CardHeader>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="pl-6">Alias</TableHead>
            <TableHead>Repository</TableHead>
            <TableHead>Last read</TableHead>
            <TableHead className="pr-6" />
          </TableRow>
        </TableHeader>
        <TableBody>
          {list.map((s) => {
            const a = latest.get(s.alias)
            const running = !!a && isActive(a.status)
            return (
              <TableRow key={s.id}>
                <TableCell className="pl-6 font-mono text-xs">{s.alias}</TableCell>
                <TableCell>
                  <a
                    className="font-mono text-xs underline-offset-4 hover:underline"
                    href={sourceURL(s)}
                    target="_blank"
                    rel="noreferrer"
                  >
                    {s.repository}
                    {s.root ? `/${s.root}` : ""}
                  </a>
                  {s.ref && (
                    <Badge variant="outline" className="ml-2 font-normal">
                      <GitBranchIcon />
                      {s.ref}
                    </Badge>
                  )}
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {!a
                    ? "Not analyzed"
                    : running
                      ? stepLabel(a.status)
                      : a.status === "failed"
                        ? "Failed"
                        : `@${shortSHA(a.commitSha)}`}
                </TableCell>
                <TableCell className="pr-6 text-right">
                  {mayRun && (
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={running || start.isPending}
                      onClick={() => void analyze(s.alias)}
                    >
                      Analyze
                    </Button>
                  )}
                  {mayEdit && (
                    <Button
                      size="icon-xs"
                      variant="ghost"
                      className="text-muted-foreground"
                      aria-label={`Remove ${s.alias}`}
                      onClick={() => setRemoving(s)}
                    >
                      <Trash2Icon />
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
      <AddSourceDialog project={project} open={adding} onOpenChange={setAdding} />
      <RemoveSource
        project={project}
        source={removing}
        onDone={() => setRemoving(null)}
      />
    </Card>
  )
}

function RemoveSource({
  project,
  source,
  onDone,
}: {
  project: Project
  source: Source | null
  onDone: () => void
}) {
  const remove = useMutation(useSourcesDelete)
  const confirm = async () => {
    if (!source) return
    try {
      await remove.mutateAsync({ path: { id: project.id, alias: source.alias } })
      toast.success(`Removed ${source.alias}`)
      onDone()
    } catch (err) {
      toast.error(messageOf(err, "The repository wasn't removed."))
    }
  }
  return (
    <AlertDialog open={!!source} onOpenChange={(open) => !open && onDone()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Remove {source?.alias}?</AlertDialogTitle>
          <AlertDialogDescription>
            Its services leave the graph. The alias {source?.alias} stays
            taken, so plans made with it keep meaning {source?.repository}, and
            you can't give it to another repository later.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Keep it</AlertDialogCancel>
          <AlertDialogAction onClick={() => void confirm()}>Remove</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

/** AddSourceDialog attaches another repository, or a directory of one. */
export function AddSourceDialog({
  project,
  open,
  onOpenChange,
}: {
  project: Project
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const add = useMutation(useSourcesCreate)
  const [repository, setRepository] = useState("")
  const [alias, setAlias] = useState("")
  const [root, setRoot] = useState("")
  const [ref, setRef] = useState("")

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    try {
      const s = await add.mutateAsync({
        path: { id: project.id },
        body: {
          repository: repository.trim(),
          alias: alias.trim() || undefined,
          root: root.trim() || undefined,
          ref: ref.trim() || undefined,
        },
      })
      toast.success(`Added ${s.alias}`, {
        description: "Analyze it to see what it adds to the graph.",
      })
      setRepository("")
      setAlias("")
      setRoot("")
      setRef("")
      onOpenChange(false)
    } catch (err) {
      toast.error(messageOf(err, "The repository wasn't added."))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={(e) => void submit(e)}>
          <DialogHeader>
            <DialogTitle>Add a repository</DialogTitle>
            <DialogDescription>
              Public repositories work as they are. A private one needs the
              GitHub connection to see it, and Loomplane checks that before
              adding it.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-4">
            <Field>
              <FieldLabel htmlFor="source-repository">Repository</FieldLabel>
              <Input
                id="source-repository"
                value={repository}
                onChange={(e) =>
                  setRepository(githubRepo(e.target.value) || e.target.value)
                }
                placeholder="acme/storefront"
                className="font-mono"
                spellCheck={false}
                required
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="source-alias">Alias</FieldLabel>
              <Input
                id="source-alias"
                value={alias}
                onChange={(e) => setAlias(e.target.value.toLowerCase())}
                placeholder="Picked from the repository's name"
                className="font-mono"
                spellCheck={false}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="source-root">Directory</FieldLabel>
              <Input
                id="source-root"
                value={root}
                onChange={(e) => setRoot(e.target.value)}
                placeholder="The whole repository"
                className="font-mono"
                spellCheck={false}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="source-ref">Branch</FieldLabel>
              <Input
                id="source-ref"
                value={ref}
                onChange={(e) => setRef(e.target.value)}
                placeholder="The default branch"
                className="font-mono"
                spellCheck={false}
              />
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button type="submit" disabled={add.isPending || !repository.trim()}>
              {add.isPending && <Spinner />}
              Add
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
```

For suggestions in the repository field, copy the block in `new-project-dialog.tsx` that reads `useQuery(useGithubRepositories)` and lists matches under the field.

- [ ] **Step 6: Replace every `repositoryUrl`**

Run `pnpm --dir apps/dashboard typecheck` and fix each error:

- `pages/project-detail.tsx`:
  - The header's repository badge maps `project.repositories` to one badge each, linking `https://github.com/${r}`. With none, it keeps the "No repository yet" badge.
  - `actions` checks `project.repositories.length > 0 && mayRun`.
  - `Discovered` checks `project.repositories.length === 0`.
  - The `Details` row is titled "Repositories" and shows `project.repositories.join(", ")`.
  - Render `<SourcesCard project={project} />` above the graph in the architecture tab when `project.repositories.length > 0`.
- `components/architecture.tsx`:
  - `ConnectRepository` uses `useSourcesCreate` with `body: { repository: repo }` in place of `useProjectsUpdate`. It invalidates `useProjectsGet` on success so the page sees the repository.
  - `NotAnalyzed` names `repositoriesLabel(project.repositories)`.
  - `AnalyzeActions` offers its "another branch" choice only when `project.repositories.length === 1`.
  - `AnalyzeOnce` keeps `body: {}`.
- `pages/projects.tsx`: the search includes `...p.repositories`, and the tile shows `repositoriesLabel(p.repositories) || "No repository yet"`.
- `components/app-sidebar.tsx`: copy `https://github.com/${project.repositories[0]}` when there is one.
- `components/command-menu.tsx`: search on `p.repositories.join(" ")` and label with `repositoriesLabel(p.repositories)`.
- `components/new-project-dialog.tsx`: keep sending `repositoryUrl`. Read `created.repositories[0]` in place of `created.repositoryUrl`.

- [ ] **Step 7: Run the dashboard checks**

Run: `make lint-dashboard test-dashboard`
Expected: PASS.

- [ ] **Step 8: Check it in the browser**

With `make dev` running:
- create a project from `acme/mono`;
- add a second repository from the Repositories card;
- analyze one, then all;
- remove one and confirm the alias is refused when re-added.

Fix anything that looks wrong before committing.

- [ ] **Step 9: Commit**

```bash
git add apps/dashboard/
git commit -m "feat: show a project's repositories and add or remove them in the dashboard"
```

---

# Phase 2: Federation

Ships one composed graph per project, with dependencies drawn across repositories.

### Task 8: Detectors keep what they can't resolve (Go)

**Files:**
- Modify: `pkg/discovery/discovery.go` (`Requirement`, `Unit.Requires`, `Source` on `Evidence`, `Workspace`, `BuildSystem` and `Note`)
- Modify: `internal/discovery/detect/golang.go`
- Test: `internal/discovery/detect/detect_test.go`

**Interfaces:**
- Produces: `type Requirement struct{ Name, Version, Git string; Evidence Evidence }`, `Unit.Requires []Requirement` (`json:"requires,omitempty"`), and `Source string` (`json:"source,omitempty"`) on `Evidence`, `Workspace`, `BuildSystem` and `Note`.

- [ ] **Step 1: Write the failing test**

```go
func TestGo_requirementsOutsideTheCheckoutAreKept(t *testing.T) {
	r := run(t, Go{}, map[string]string{
		"go.mod": "module example.com/root\n\ngo 1.26\n\nrequire (\n\tgithub.com/acme/kit v1.2.0\n\texample.com/lib v0.0.0\n\tgolang.org/x/sync v0.8.0 // indirect\n)\n\nreplace example.com/lib => ./lib\n",
		"main.go":    "package main\n\nfunc main() {}\n",
		"lib/go.mod": "module example.com/lib\n\ngo 1.26\n",
		"lib/lib.go": "package lib\n",
	})
	root := onlyUnit(t, r, "go:.")
	if len(root.Requires) != 1 {
		t.Fatalf("requires %+v", root.Requires)
	}
	req := root.Requires[0]
	if req.Name != "github.com/acme/kit" || req.Version != "v1.2.0" || req.Evidence.Path != "go.mod" || req.Evidence.Line != 6 {
		t.Fatalf("requirement %+v", req)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/discovery/detect/ -run TestGo_requirements`
Expected: FAIL to compile, `root.Requires undefined`.

- [ ] **Step 3: Extend the contract**

In `pkg/discovery/discovery.go`, add to `Evidence`:

```go
	// Source is the alias of the repository Path is in. It's empty in one
	// repository's analysis, and set when graphs from several are joined.
	Source string `json:"source,omitempty"`
```

Add the same field, with the comment "Source is the alias of the repository it was found in, set when graphs are joined.", to `Workspace`, `BuildSystem` and `Note`. Add to `Unit`, after `DependsOn`:

```go
	// Requires are the dependencies it declares that nothing in this
	// checkout provides. Another repository of the same project might.
	Requires []Requirement `json:"requires,omitempty"`
```

And the type:

```go
// Requirement is a dependency a unit declares on something outside the
// checkout: a module, crate or package, by the name it's published under.
type Requirement struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	// Git is the repository URL a Rust git dependency names.
	Git      string   `json:"git,omitempty"`
	Evidence Evidence `json:"evidence"`
}
```

Also fix the `Dependency` comment to "a unit's use of another unit in the same checkout".

- [ ] **Step 4: Record Go requirements**

In `golang.go`, in `goUnit`, after `u.DependsOn = goDependencies(m, mods)`:

```go
	u.Requires = goRequires(m, mods)
```

```go
// goRequires lists the modules m requires that no module in the checkout
// is, and no directory replace points into it. Indirect requirements are
// left out: they're what m's dependencies need, not what it uses.
func goRequires(m *goModule, mods []*goModule) []discovery.Requirement {
	local := map[string]bool{}
	for _, other := range mods {
		local[other.path] = true
	}
	for _, rep := range m.mod.Replace {
		if modfile.IsDirectoryPath(rep.New.Path) {
			local[rep.Old.Path] = true
		}
	}
	var out []discovery.Requirement
	for _, req := range m.mod.Require {
		if req.Indirect || local[req.Mod.Path] {
			continue
		}
		out = append(out, discovery.Requirement{
			Name: req.Mod.Path, Version: req.Mod.Version,
			Evidence: evidence(m.data, m.file, req.Syntax.Start.Line),
		})
	}
	return out
}
```

- [ ] **Step 5: Run the detector tests**

Run: `go test ./internal/discovery/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/discovery/ internal/discovery/detect/
git commit -m "feat: keep the Go modules a checkout requires but doesn't hold"
```

### Task 9: Detectors keep what they can't resolve (Node and Rust)

**Files:**
- Modify: `internal/discovery/detect/node.go`, `internal/discovery/detect/rust.go`
- Test: `internal/discovery/detect/detect_test.go`

**Interfaces:**
- Consumes: Task 8's `Requirement`.

- [ ] **Step 1: Write the failing tests**

```go
func TestNode_requirementsOutsideTheCheckoutAreKept(t *testing.T) {
	r := run(t, Node{}, map[string]string{
		"package.json":          `{"name": "root", "private": true, "workspaces": ["packages/*"]}`,
		"packages/ui/package.json": `{"name": "@acme/ui"}`,
		"packages/app/package.json": "{\n  \"name\": \"app\",\n  \"dependencies\": {\n    \"@acme/ui\": \"workspace:*\",\n    \"@acme/tokens\": \"^2.0.0\"\n  }\n}\n",
	})
	app := onlyUnit(t, r, "node:packages/app")
	if len(app.Requires) != 1 || app.Requires[0].Name != "@acme/tokens" || app.Requires[0].Version != "^2.0.0" || app.Requires[0].Evidence.Line != 5 {
		t.Fatalf("requires %+v", app.Requires)
	}
}

func TestRust_requirementsOutsideTheCheckoutAreKept(t *testing.T) {
	r := run(t, Rust{}, map[string]string{
		"Cargo.toml": "[workspace]\nmembers = [\"crates/*\"]\n\n[workspace.dependencies]\nkit = { git = \"https://github.com/acme/kit\" }\n",
		"crates/app/Cargo.toml": "[package]\nname = \"app\"\nversion = \"0.1.0\"\n\n[dependencies]\nkit = { workspace = true }\nserde = \"1\"\ncore = { path = \"../core\" }\n",
		"crates/app/src/main.rs":  "fn main() {}\n",
		"crates/core/Cargo.toml":  "[package]\nname = \"core\"\nversion = \"0.1.0\"\n",
		"crates/core/src/lib.rs":  "",
	})
	app := onlyUnit(t, r, "rust:crates/app")
	got := map[string]string{}
	for _, req := range app.Requires {
		got[req.Name] = req.Version + "|" + req.Git
	}
	if len(got) != 2 || got["kit"] != "|https://github.com/acme/kit" || got["serde"] != "1|" {
		t.Fatalf("requires %+v", app.Requires)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/discovery/detect/ -run requirementsOutside`
Expected: FAIL. `Requires` is empty for both.

- [ ] **Step 3: Record Node requirements**

In `node.go`'s `nodeUnit`, in the loop over `sortedStringKeys(all)`, right after the `if target, ok := byName[dep]; ok && target != p.dir { ...; continue }` block:

```go
		if _, local := byName[dep]; !local {
			u.Requires = append(u.Requires, discovery.Requirement{Name: dep, Version: all[dep], Evidence: ev})
		}
```

- [ ] **Step 4: Record Rust requirements**

In `rust.go`'s `rustUnit`, in the dependency loop, a path dependency ends in `if pathDep != "" { ...; continue }`. Everything after that block is a dependency from outside the checkout. Insert this directly after the block, before the `cargoFrameworks` lookup:

```go
		remote := spec
		if inherits(spec) && ws != nil && ws.m.Workspace != nil {
			remote = ws.m.Workspace.Dependencies[name]
		}
		version, git := cargoRemote(remote)
		u.Requires = append(u.Requires, discovery.Requirement{Name: name, Version: version, Git: git, Evidence: ev})
```

```go
// cargoRemote reads where a non-path dependency comes from: a version, a
// git repository, or both.
func cargoRemote(spec any) (version, git string) {
	switch s := spec.(type) {
	case string:
		return s, ""
	case map[string]any:
		version, _ = s["version"].(string)
		git, _ = s["git"].(string)
	}
	return version, git
}
```

- [ ] **Step 5: Run the detector tests**

Run: `go test ./internal/discovery/...`
Expected: PASS. If an analysis test compares the polyglot report byte for byte, update its expected requirements to what the detectors now report, after checking each one by eye.

- [ ] **Step 6: Commit**

```bash
git add internal/discovery/detect/
git commit -m "feat: keep the packages and crates a checkout requires but doesn't hold"
```

### Task 10: Graph nodes carry their source and requirements

**Files:**
- Modify: `internal/graph/graph.go`, `internal/graph/build.go`
- Test: `internal/graph/build_test.go`

**Interfaces:**
- Consumes: Task 8's `Requirement`.
- Produces:
  - `SchemaVersion = 2`.
  - `Service.Source`, `Service.UnitName`, `Service.Requires`.
  - `Package.Source`, `Package.Requires`.
  - `Graph.Sources []SourceRef` (the type is declared in Task 11; declare it here so this task compiles).
  - `(Service).Local() string`.
  - `builder.edge(from, to string, kind EdgeKind, conf Confidence, evs ...Evidence)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestBuild_nodesCarryWhatTheirUnitRequires(t *testing.T) {
	req := pd.Requirement{Name: "github.com/acme/kit", Version: "v1.2.0", Evidence: pd.Evidence{Path: "go.mod", Line: 5}}
	g := Build(&pd.Report{Units: []pd.Unit{
		{ID: "go:.", Ecosystem: pd.Go, Name: "github.com/acme/api", Path: ".",
			Entrypoints: []pd.Entrypoint{{Name: "api", Path: "cmd/api"}}, Requires: []pd.Requirement{req}},
		{ID: "go:lib", Ecosystem: pd.Go, Name: "github.com/acme/api/lib", Path: "lib", Requires: []pd.Requirement{req}},
	}})
	if g.Version != 2 {
		t.Fatalf("version %d", g.Version)
	}
	svc := g.Services[0]
	if svc.UnitName != "github.com/acme/api" || len(svc.Requires) != 1 || svc.Requires[0] != req {
		t.Fatalf("service %+v", svc)
	}
	if pkg := g.Packages[0]; len(pkg.Requires) != 1 {
		t.Fatalf("package %+v", pkg)
	}
}

func TestService_Local(t *testing.T) {
	for _, tt := range []struct {
		s    Service
		want string
	}{
		{Service{ID: "svc:go:cmd/api"}, "go:cmd/api"},
		{Service{ID: "svc:api:go:cmd/api", Source: "api"}, "go:cmd/api"},
		{Service{ID: "svc:web:container:Dockerfile", Source: "web"}, "container:Dockerfile"},
	} {
		if got := tt.s.Local(); got != tt.want {
			t.Errorf("%s.Local() = %q, want %q", tt.s.ID, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/graph/`
Expected: FAIL to compile.

- [ ] **Step 3: Extend the graph types**

In `graph.go`:
- `SchemaVersion = 2`. Change the comment to: "Version 2 added sources and requirements; a version 1 graph still reads, without them."
- Add to `Graph`, first field after `Version`:

```go
	// Sources are where a composed graph was read from, by alias. A graph
	// one analysis built has none.
	Sources []SourceRef `json:"sources,omitempty"`
```

- Add to `Service`, after `ID`:

```go
	// Source is the alias of the repository it's in, once graphs are
	// composed.
	Source string `json:"source,omitempty"`
```

  and after `Unit`:

```go
	// UnitName is the unit's published name, such as its module path,
	// which another repository's requirement can name.
	UnitName string `json:"unit_name,omitempty"`
	// Requires are what its unit declares that its repository doesn't hold.
	Requires []discovery.Requirement `json:"requires,omitempty"`
```

- Add `Source` and `Requires` to `Package` the same way.
- Add:

```go
// Local is the service's ID inside its own repository, such as
// "go:cmd/api": what a manifest's source names.
func (s Service) Local() string {
	id := strings.TrimPrefix(s.ID, "svc:")
	if s.Source != "" {
		id = strings.TrimPrefix(id, s.Source+":")
	}
	return id
}
```

Create `internal/graph/compose.go` now with only the `SourceRef` type, so `Graph.Sources` compiles:

```go
package graph

// SourceRef says where one part of a composed graph was read from.
type SourceRef struct {
	Alias      string `json:"alias"`
	Repository string `json:"repository"`
	Root       string `json:"root,omitempty"`
	CommitSHA  string `json:"commit_sha"`
	AnalysisID string `json:"analysis_id"`
}
```

- [ ] **Step 4: Carry requirements in `Build`**

In `build.go`:
- `service(u, ep)` sets `UnitName: u.Name` and `Requires: slices.Clone(u.Requires)`.
- `addPackage` sets `Requires: slices.Clone(u.Requires)`.
- Change `edge` to take evidence variadically, so a composed edge can carry all of its evidence:

```go
func (b *builder) edge(from, to string, kind EdgeKind, conf discovery.Confidence, evs ...discovery.Evidence) {
	key := from + "|" + to + "|" + string(kind)
	e := b.edges[key]
	if e == nil {
		e = &Edge{From: from, To: to, Kind: kind, Confidence: conf}
		b.edges[key] = e
	}
	if conf.Rank() > e.Confidence.Rank() {
		e.Confidence = conf
	}
	for _, ev := range evs {
		e.Evidence = addEvidence(e.Evidence, ev)
	}
}
```

`slices.Clone(nil)` is nil, so a unit with no requirements stays `omitempty`.

- [ ] **Step 5: Run the graph and planning tests**

Run: `go test ./internal/graph/ ./internal/planning/ ./internal/discovery/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/graph/
git commit -m "feat: carry each node's source and requirements in the graph"
```

### Task 11: Compose joins graphs

**Files:**
- Modify: `internal/graph/compose.go`
- Test: `internal/graph/compose_test.go`

**Interfaces:**
- Consumes: Task 10.
- Produces: `type Part struct{ Source SourceRef; Graph *Graph }`, `Compose(parts []Part) *Graph`. Cross-repository links come in Task 12; this task calls an empty `link`.

- [ ] **Step 1: Write the failing tests**

```go
package graph

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	pd "github.com/xraph/loomplane/pkg/discovery"
)

func ref(alias, repo string) SourceRef {
	return SourceRef{Alias: alias, Repository: repo, CommitSHA: strings.Repeat(alias[:1], 40), AnalysisID: "anl_" + alias}
}

// apiGraph is a Go server that might use Postgres and requires acme/kit.
func apiGraph() *Graph {
	return Build(&pd.Report{Units: []pd.Unit{{
		ID: "go:.", Ecosystem: pd.Go, Name: "github.com/acme/api", Path: ".",
		Entrypoints: []pd.Entrypoint{{Name: "api", Path: "cmd/api", Resources: []pd.ResourceUse{
			{Kind: "postgres", Confidence: pd.Possible, Evidence: pd.Evidence{Path: "go.mod", Line: 7}},
		}}},
		Requires: []pd.Requirement{{Name: "github.com/acme/kit/log", Version: "v1.2.0", Evidence: pd.Evidence{Path: "go.mod", Line: 5}}},
	}}})
}

// webGraph is a Next.js app that surely uses Postgres, beside a shared UI
// package, with a server in services/ that a root of apps/web leaves out.
func webGraph() *Graph {
	return Build(&pd.Report{Units: []pd.Unit{
		{ID: "node:apps/web", Ecosystem: pd.Node, Name: "web", Path: "apps/web",
			Entrypoints: []pd.Entrypoint{{Name: "web", Path: "apps/web", Resources: []pd.ResourceUse{
				{Kind: "postgres", Confidence: pd.Likely, Evidence: pd.Evidence{Path: "apps/web/package.json", Line: 4}},
			}}},
			DependsOn: []pd.Dependency{{Unit: "node:packages/ui", Evidence: pd.Evidence{Path: "apps/web/package.json", Line: 3}}}},
		{ID: "node:packages/ui", Ecosystem: pd.Node, Name: "@acme/ui", Path: "packages/ui"},
		{ID: "node:services/edge", Ecosystem: pd.Node, Name: "edge", Path: "services/edge",
			Entrypoints: []pd.Entrypoint{{Name: "edge", Path: "services/edge"}}},
	}})
}

func ids[T any](list []T, id func(T) string) []string {
	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, id(x))
	}
	return out
}

func TestCompose_qualifiesAndMerges(t *testing.T) {
	g := Compose([]Part{{Source: ref("web", "acme/web"), Graph: webGraph()}, {Source: ref("api", "acme/api"), Graph: apiGraph()}})
	if got := ids(g.Sources, func(s SourceRef) string { return s.Alias }); !slices.Equal(got, []string{"api", "web"}) {
		t.Fatalf("sources %v", got)
	}
	wantSvcs := []string{"svc:api:go:cmd/api", "svc:web:node:apps/web", "svc:web:node:services/edge"}
	if got := ids(g.Services, func(s Service) string { return s.ID }); !slices.Equal(got, wantSvcs) {
		t.Fatalf("services %v", got)
	}
	if g.Services[0].Source != "api" || g.Services[0].Requires[0].Evidence.Source != "api" {
		t.Fatalf("api service %+v", g.Services[0])
	}
	if len(g.Resources) != 1 {
		t.Fatalf("resources %+v", g.Resources)
	}
	pg := g.Resources[0]
	if pg.ID != "res:postgres" || pg.Confidence != pd.Likely || pg.NeedsConfirmation || len(pg.Evidence) != 2 {
		t.Fatalf("postgres %+v", pg)
	}
	if !slices.ContainsFunc(g.Edges, func(e Edge) bool {
		return e.From == "svc:web:node:apps/web" && e.To == "pkg:web:node:packages/ui" && e.Evidence[0].Source == "web"
	}) {
		t.Fatalf("edges %+v", g.Edges)
	}
}

func TestCompose_isDeterministic(t *testing.T) {
	a := Compose([]Part{{Source: ref("web", "acme/web"), Graph: webGraph()}, {Source: ref("api", "acme/api"), Graph: apiGraph()}})
	b := Compose([]Part{{Source: ref("api", "acme/api"), Graph: apiGraph()}, {Source: ref("web", "acme/web"), Graph: webGraph()}})
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatalf("order changed the graph:\n%s\n%s", ja, jb)
	}
}

func TestCompose_rootKeepsWhatItsServicesUse(t *testing.T) {
	web := ref("web", "acme/web")
	web.Root = "apps/web"
	g := Compose([]Part{{Source: web, Graph: webGraph()}})
	if got := ids(g.Services, func(s Service) string { return s.ID }); !slices.Equal(got, []string{"svc:web:node:apps/web"}) {
		t.Fatalf("services %v", got)
	}
	if got := ids(g.Packages, func(p Package) string { return p.ID }); !slices.Equal(got, []string{"pkg:web:node:packages/ui"}) {
		t.Fatalf("packages %v: a root must keep what its services use", got)
	}
}

func TestCompose_notesWhatItCouldntJoin(t *testing.T) {
	old := apiGraph()
	old.Version = 1
	g := Compose([]Part{{Source: ref("api", "acme/api"), Graph: old}, {Source: ref("web", "acme/web")}})
	msgs := ids(g.Notes, func(n pd.Note) string { return n.Source + ": " + n.Message })
	if len(msgs) != 2 || !strings.Contains(msgs[0], "api: api was analyzed before") || !strings.Contains(msgs[1], "web: web has no successful analysis") {
		t.Fatalf("notes %q", msgs)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/graph/ -run TestCompose`
Expected: FAIL to compile, `undefined: Compose`.

- [ ] **Step 3: Write `Compose`**

Add to `internal/graph/compose.go`:

```go
// Part is one source's graph, as its analysis stored it.
type Part struct {
	Source SourceRef
	// Graph is nil when the source has no successful analysis yet.
	Graph *Graph
}

// Compose joins one graph per source into the application's graph. Each
// service and package ID gains its source's alias, a source's root keeps
// only what's under it, resources of one kind become one node, and a
// dependency one source declares on a package another publishes becomes
// an edge between them. It's deterministic, like Build: the same parts, in
// any order, give the same graph.
func Compose(parts []Part) *Graph {
	parts = slices.Clone(parts)
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].Source.Alias < parts[j].Source.Alias })
	b := &builder{
		g: &Graph{
			Version: SchemaVersion, Sources: []SourceRef{}, Services: []Service{}, Packages: []Package{},
			Resources: []Resource{}, Edges: []Edge{}, Workspaces: []discovery.Workspace{},
			BuildSystems: []discovery.BuildSystem{}, Notes: []discovery.Note{},
		},
		resources: map[string]*Resource{},
		edges:     map[string]*Edge{},
	}
	for _, p := range parts {
		b.g.Sources = append(b.g.Sources, p.Source)
		b.add(p)
	}
	b.link()
	b.finish()
	sort.SliceStable(b.g.Notes, func(i, j int) bool {
		x, y := b.g.Notes[i], b.g.Notes[j]
		if x.Source != y.Source {
			return x.Source < y.Source
		}
		if x.Path != y.Path {
			return x.Path < y.Path
		}
		return x.Message < y.Message
	})
	return b.g
}

func (b *builder) add(p Part) {
	alias := p.Source.Alias
	if p.Graph == nil {
		b.g.Notes = append(b.g.Notes, discovery.Note{Source: alias,
			Message: fmt.Sprintf("%s has no successful analysis yet, so nothing from %s is in the graph.", alias, p.Source.Repository)})
		return
	}
	g := rooted(p.Graph, p.Source.Root)
	if g.Version < SchemaVersion {
		b.g.Notes = append(b.g.Notes, discovery.Note{Source: alias,
			Message: fmt.Sprintf("%s was analyzed before Loomplane read dependencies between repositories. Analyze it again to find them.", alias)})
	}
	for _, s := range g.Services {
		s.ID, s.Source, s.Evidence, s.Requires = qualify(s.ID, alias), alias, tagged(s.Evidence, alias), taggedRequires(s.Requires, alias)
		b.g.Services = append(b.g.Services, s)
	}
	for _, pk := range g.Packages {
		pk.ID, pk.Source, pk.Evidence, pk.Requires = qualify(pk.ID, alias), alias, tagged(pk.Evidence, alias), taggedRequires(pk.Requires, alias)
		b.g.Packages = append(b.g.Packages, pk)
	}
	for _, r := range g.Resources {
		res := b.resources[r.ID]
		if res == nil {
			res = &Resource{ID: r.ID, Kind: r.Kind, Confidence: r.Confidence}
			b.resources[r.ID] = res
		}
		if r.Confidence.Rank() > res.Confidence.Rank() {
			res.Confidence = r.Confidence
		}
		for _, ev := range tagged(r.Evidence, alias) {
			res.Evidence = addEvidence(res.Evidence, ev)
		}
	}
	for _, e := range g.Edges {
		b.edge(qualify(e.From, alias), qualify(e.To, alias), e.Kind, e.Confidence, tagged(e.Evidence, alias)...)
	}
	for _, w := range g.Workspaces {
		w.Source, w.Evidence = alias, tagged(w.Evidence, alias)
		b.g.Workspaces = append(b.g.Workspaces, w)
	}
	for _, bs := range g.BuildSystems {
		bs.Source, bs.Evidence.Source = alias, alias
		b.g.BuildSystems = append(b.g.BuildSystems, bs)
	}
	for _, n := range g.Notes {
		n.Source = alias
		b.g.Notes = append(b.g.Notes, n)
	}
}

// link is filled in by the next task.
func (b *builder) link() {}

// qualify puts alias into a service or package ID. Resource IDs are the
// project's, not a repository's, so they stay as they are.
func qualify(id, alias string) string {
	for _, prefix := range []string{"svc:", "pkg:"} {
		if rest, ok := strings.CutPrefix(id, prefix); ok {
			return prefix + alias + ":" + rest
		}
	}
	return id
}

func tagged(evs []discovery.Evidence, alias string) []discovery.Evidence {
	out := make([]discovery.Evidence, len(evs))
	for i, ev := range evs {
		ev.Source = alias
		out[i] = ev
	}
	return out
}

func taggedRequires(reqs []discovery.Requirement, alias string) []discovery.Requirement {
	if reqs == nil {
		return nil
	}
	out := make([]discovery.Requirement, len(reqs))
	for i, r := range reqs {
		r.Evidence.Source = alias
		out[i] = r
	}
	return out
}

// rooted keeps the part of g under root: the services whose entrypoint is
// there, the packages there, everything those reach through edges however
// deep (a web app under apps/web still builds with packages/ui), and the
// resources whose evidence is there.
func rooted(g *Graph, root string) *Graph {
	if root == "" {
		return g
	}
	keep := map[string]bool{}
	var queue []string
	for _, s := range g.Services {
		if under(s.Entrypoint, root) {
			keep[s.ID] = true
			queue = append(queue, s.ID)
		}
	}
	for _, p := range g.Packages {
		if under(p.Path, root) && !keep[p.ID] {
			keep[p.ID] = true
			queue = append(queue, p.ID)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, e := range g.Edges {
			if e.From == id && !keep[e.To] {
				keep[e.To] = true
				queue = append(queue, e.To)
			}
		}
	}
	out := *g
	out.Services = slices.DeleteFunc(slices.Clone(g.Services), func(s Service) bool { return !keep[s.ID] })
	out.Packages = slices.DeleteFunc(slices.Clone(g.Packages), func(p Package) bool { return !keep[p.ID] })
	out.Resources = slices.DeleteFunc(slices.Clone(g.Resources), func(r Resource) bool {
		return !keep[r.ID] && !slices.ContainsFunc(r.Evidence, func(ev discovery.Evidence) bool { return under(ev.Path, root) })
	})
	out.Edges = slices.DeleteFunc(slices.Clone(g.Edges), func(e Edge) bool { return !keep[e.From] || !keep[e.To] })
	return &out
}

func under(p, root string) bool { return p == root || strings.HasPrefix(p, root+"/") }
```

Imports: `fmt`, `slices`, `sort`, `strings`, `github.com/xraph/loomplane/pkg/discovery`.

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test ./internal/graph/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/graph/
git commit -m "feat: compose a project's graph from one graph per repository"
```

### Task 12: Compose links dependencies across repositories

**Files:**
- Modify: `internal/graph/compose.go` (`link`, `structural`)
- Test: `internal/graph/compose_test.go`

**Interfaces:**
- Consumes: Task 11.

- [ ] **Step 1: Write the failing tests**

```go
func kitGraph(name string) *Graph {
	return Build(&pd.Report{Units: []pd.Unit{{ID: "go:.", Ecosystem: pd.Go, Name: name, Path: "."}}})
}

func uiGraph() *Graph {
	return Build(&pd.Report{Units: []pd.Unit{{ID: "node:.", Ecosystem: pd.Node, Name: "@acme/tokens", Path: "."}}})
}

func edgeBetween(g *Graph, from, to string) *Edge {
	for i := range g.Edges {
		if g.Edges[i].From == from && g.Edges[i].To == to {
			return &g.Edges[i]
		}
	}
	return nil
}

func TestCompose_linksAGoModuleOnItsRepositoryAsCertain(t *testing.T) {
	g := Compose([]Part{{Source: ref("api", "acme/api"), Graph: apiGraph()}, {Source: ref("kit", "Acme/Kit"), Graph: kitGraph("github.com/acme/kit/log")}})
	e := edgeBetween(g, "svc:api:go:cmd/api", "pkg:kit:go:.")
	if e == nil || e.Kind != Uses || e.Confidence != pd.Certain || e.Evidence[0].Source != "api" || e.Evidence[0].Line != 5 {
		t.Fatalf("edge %+v in %+v", e, g.Edges)
	}
}

func TestCompose_linksAnNPMNameAsLikely(t *testing.T) {
	site := Build(&pd.Report{Units: []pd.Unit{{
		ID: "node:.", Ecosystem: pd.Node, Name: "site", Path: ".",
		Entrypoints: []pd.Entrypoint{{Name: "site", Path: "."}},
		Requires:    []pd.Requirement{{Name: "@acme/tokens", Evidence: pd.Evidence{Path: "package.json", Line: 9}}},
	}}})
	g := Compose([]Part{{Source: ref("site", "acme/site"), Graph: site}, {Source: ref("ui", "acme/ui"), Graph: uiGraph()}})
	if e := edgeBetween(g, "svc:site:node:.", "pkg:ui:node:."); e == nil || e.Confidence != pd.Likely {
		t.Fatalf("edge %+v in %+v", e, g.Edges)
	}

	// Published twice, the name is ambiguous: no edge, only a note.
	g = Compose([]Part{
		{Source: ref("site", "acme/site"), Graph: site},
		{Source: ref("ui", "acme/ui"), Graph: uiGraph()},
		{Source: ref("vendor", "acme/vendor"), Graph: uiGraph()},
	})
	if slices.ContainsFunc(g.Edges, func(e Edge) bool { return e.From == "svc:site:node:." }) {
		t.Fatalf("an ambiguous name got an edge: %+v", g.Edges)
	}
	if !slices.ContainsFunc(g.Notes, func(n pd.Note) bool {
		return n.Source == "site" && strings.Contains(n.Message, "@acme/tokens is published by ui and vendor")
	}) {
		t.Fatalf("notes %+v", g.Notes)
	}
}

func TestCompose_linksARustGitDependencyAsCertain(t *testing.T) {
	app := Build(&pd.Report{Units: []pd.Unit{{
		ID: "rust:.", Ecosystem: pd.Rust, Name: "app", Path: ".",
		Entrypoints: []pd.Entrypoint{{Name: "app", Path: "src/main.rs"}},
		Requires:    []pd.Requirement{{Name: "kit", Git: "https://github.com/Acme/Crates.git", Evidence: pd.Evidence{Path: "Cargo.toml", Line: 6}}},
	}}})
	crates := Build(&pd.Report{Units: []pd.Unit{{ID: "rust:kit", Ecosystem: pd.Rust, Name: "kit", Path: "kit"}}})
	g := Compose([]Part{{Source: ref("app", "acme/app"), Graph: app}, {Source: ref("crates", "acme/crates"), Graph: crates}})
	if e := edgeBetween(g, "svc:app:rust:src/main.rs", "pkg:crates:rust:kit"); e == nil || e.Confidence != pd.Certain {
		t.Fatalf("edge %+v in %+v", e, g.Edges)
	}
}

func TestCompose_neverLinksARepositoryToItself(t *testing.T) {
	g := Compose([]Part{{Source: ref("api", "acme/api"), Graph: apiGraph()}})
	if len(g.Edges) != 1 || g.Edges[0].To != "res:postgres" {
		t.Fatalf("edges %+v", g.Edges)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/graph/ -run TestCompose_links`
Expected: FAIL, no cross-repository edges.

- [ ] **Step 3: Write `link` and `structural`**

Replace the stub `link` in `compose.go`:

```go
// link adds an edge for each dependency one source declares on a package
// another source publishes. The edge is certain when the declaration
// names the other repository itself (a Rust git dependency, a Go module
// path on github.com) and likely when only the name matches. A name two
// other sources publish gets no edge, only a note: a wrong edge is worse
// than a missing one.
func (b *builder) link() {
	type key struct {
		eco  discovery.Ecosystem
		name string
	}
	published := map[key]map[string][]string{} // source alias to node IDs
	publish := func(k key, source, id string) {
		if published[k] == nil {
			published[k] = map[string][]string{}
		}
		published[k][source] = append(published[k][source], id)
	}
	hasPackage := map[string]bool{}
	for _, p := range b.g.Packages {
		if p.Name == "" {
			continue
		}
		publish(key{p.Ecosystem, p.Name}, p.Source, p.ID)
		hasPackage[p.Source+"\x00"+string(p.Ecosystem)+"\x00"+p.Name] = true
	}
	// A unit with no library, only services, is published by its services.
	for _, s := range b.g.Services {
		if s.UnitName != "" && !hasPackage[s.Source+"\x00"+string(s.Ecosystem)+"\x00"+s.UnitName] {
			publish(key{s.Ecosystem, s.UnitName}, s.Source, s.ID)
		}
	}
	repos := map[string]string{}
	for _, src := range b.g.Sources {
		repos[src.Alias] = src.Repository
	}
	noted := map[string]bool{}
	use := func(from, source string, eco discovery.Ecosystem, reqs []discovery.Requirement) {
		for _, req := range reqs {
			others := map[string][]string{}
			for src, nodes := range published[key{eco, req.Name}] {
				if src != source {
					others[src] = nodes
				}
			}
			if len(others) > 1 {
				names := slices.Sorted(maps.Keys(others))
				msg := fmt.Sprintf("%s is published by %s, so Loomplane can't tell which one %s uses.", req.Name, strings.Join(names, " and "), source)
				if !noted[msg] {
					noted[msg] = true
					b.g.Notes = append(b.g.Notes, discovery.Note{Source: source, Path: req.Evidence.Path, Message: msg})
				}
				continue
			}
			for target, nodes := range others {
				conf := discovery.Likely
				if structural(req, eco, repos[target]) {
					conf = discovery.Certain
				}
				for _, id := range nodes {
					b.edge(from, id, Uses, conf, req.Evidence)
				}
			}
		}
	}
	for _, s := range b.g.Services {
		use(s.ID, s.Source, s.Ecosystem, s.Requires)
	}
	for _, p := range b.g.Packages {
		use(p.ID, p.Source, p.Ecosystem, p.Requires)
	}
}

// structural reports whether a requirement names the repository itself,
// not only a package name anyone could publish. GitHub owners and names
// ignore case, so the comparison does too.
func structural(req discovery.Requirement, eco discovery.Ecosystem, repository string) bool {
	if repository == "" {
		return false
	}
	repo := strings.ToLower(repository)
	switch eco {
	case discovery.Rust:
		git := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(req.Git)), "/"), ".git")
		for _, prefix := range []string{"https://github.com/", "http://github.com/", "ssh://git@github.com/", "git@github.com:"} {
			if rest, ok := strings.CutPrefix(git, prefix); ok {
				return rest == repo
			}
		}
	case discovery.Go:
		name := strings.ToLower(req.Name)
		return name == "github.com/"+repo || strings.HasPrefix(name, "github.com/"+repo+"/")
	}
	return false
}
```

Add `maps` to the imports.

A requirement for `github.com/acme/kit/log` only matches a package published under exactly that name. If a repository publishes the module `github.com/acme/kit` and the requirement names a subpackage path, Go's `require` lists the module, not the subpackage, so the names match in practice.

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test ./internal/graph/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/graph/
git commit -m "feat: link dependencies between a project's repositories"
```

### Task 13: Serve the project's composed graph

**Files:**
- Create: `internal/discovery/project_graph.go`
- Modify: `internal/discovery/analysis.go` (`ServiceCatalog`), `extensions/discovery/routes.go`
- Modify: `internal/planning/service.go` (`Analyses`, `DraftManifest`, `ManifestDraft`), `internal/planning/draft.go`
- Test: `internal/discovery/service_test.go`, `tests/integration/sources_test.go`

**Interfaces:**
- Consumes: Tasks 4 and 11.
- Produces:
  - `type Graphs interface { ListSources(...); ListSourceAnalyses(...); Graph(...) }`.
  - `type SourceState struct{ Source Source; Latest, Shown *Analysis }`.
  - `ProjectGraph(ctx, Graphs, orgID, projectID string) (*graph.Graph, []SourceState, error)`.
  - `type ProjectView struct{ ProjectID string; Graph *graph.Graph; Sources []SourceState }` and `(*Service).ProjectGraph(ctx, p, projectID) (*ProjectView, error)`.
  - Route `projects.graph`: GET `/api/v1/projects/:id/graph`.
  - `planning.Analyses` becomes `discovery.Graphs`.
  - `ManifestDraft.Sources []graph.SourceRef` replaces `AnalysisID` and `CommitSHA`.

- [ ] **Step 1: Write the failing test**

In `internal/discovery/service_test.go`:

```go
func TestService_projectGraphComposesEverySource(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, 2)
	f.source(t, project, "web", "acme/site", "")
	dev := f.as(t, "ausr_dev", security.RoleDeveloper)
	queued, err := f.svc.Start(ctx, dev, project, StartAnalysisInput{})
	if err != nil {
		t.Fatal(err)
	}
	// Only mono's analysis runs; web's is still queued.
	if err = f.runner.Run(ctx, org, queued[0].ID); err != nil {
		t.Fatal(err)
	}
	view, err := f.svc.ProjectGraph(ctx, f.as(t, "ausr_viewer", security.RoleViewer), project)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Sources) != 2 || view.Sources[0].Shown == nil || view.Sources[1].Shown != nil || view.Sources[1].Latest == nil {
		t.Fatalf("sources %+v", view.Sources)
	}
	if len(view.Graph.Services) != 7 || !strings.HasPrefix(view.Graph.Services[0].ID, "svc:mono:") {
		t.Fatalf("services %+v", view.Graph.Services)
	}
	known, err := ServiceCatalog{Store: f.store}.Services(ctx, org, project)
	if err != nil || known[view.Graph.Services[0].ID] == "" {
		t.Fatalf("catalog %v, %v", known, err)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/discovery/ -run TestService_projectGraph`
Expected: FAIL to compile.

- [ ] **Step 3: Write `internal/discovery/project_graph.go`**

```go
package discovery

import (
	"context"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/security"
)

// Graphs reads what a project's graph is composed from. Store is one.
type Graphs interface {
	ListSources(ctx context.Context, orgID, projectID string, withRemoved bool) ([]*Source, error)
	ListSourceAnalyses(ctx context.Context, orgID, sourceID string, limit int) ([]*Analysis, error)
	Graph(ctx context.Context, orgID, id string) (*graph.Graph, error)
}

// SourceState is one source of a project's graph: its newest analysis, in
// any state, and the newest that succeeded, which is the one in the graph.
type SourceState struct {
	Source Source    `json:"source"`
	Latest *Analysis `json:"latest,omitempty"`
	Shown  *Analysis `json:"shown,omitempty"`
}

// ProjectGraph composes the newest successful analysis of each of a
// project's live sources into its application graph. Nothing stores the
// result, so it can't go stale.
func ProjectGraph(ctx context.Context, g Graphs, orgID, projectID string) (*graph.Graph, []SourceState, error) {
	sources, err := g.ListSources(ctx, orgID, projectID, false)
	if err != nil {
		return nil, nil, err
	}
	parts := make([]graph.Part, 0, len(sources))
	states := make([]SourceState, 0, len(sources))
	for _, src := range sources {
		list, err := g.ListSourceAnalyses(ctx, orgID, src.ID, listLimit)
		if err != nil {
			return nil, nil, err
		}
		st := SourceState{Source: *src}
		part := graph.Part{Source: graph.SourceRef{Alias: src.Alias, Repository: src.Repository, Root: src.Root}}
		if len(list) > 0 {
			st.Latest = list[0]
		}
		for _, a := range list {
			if a.Status != StatusSucceeded {
				continue
			}
			gr, err := g.Graph(ctx, orgID, a.ID)
			if err != nil {
				return nil, nil, err
			}
			st.Shown = a
			part.Source.CommitSHA, part.Source.AnalysisID, part.Graph = a.CommitSHA, a.ID, gr
			break
		}
		parts = append(parts, part)
		states = append(states, st)
	}
	return graph.Compose(parts), states, nil
}

// ProjectView is a project's application graph and what each of its
// sources contributed.
type ProjectView struct {
	ProjectID string        `json:"project_id" forge:"id"`
	Graph     *graph.Graph  `json:"graph"`
	Sources   []SourceState `json:"sources"`
}

// ProjectGraph returns the project's composed graph.
func (s *Service) ProjectGraph(ctx context.Context, p security.Principal, projectID string) (*ProjectView, error) {
	if err := s.authz.Require(ctx, p, security.ProjectRead, projectID); err != nil {
		return nil, err
	}
	if _, err := s.projects.GetProject(ctx, p.OrgID, projectID); err != nil {
		return nil, err
	}
	g, states, err := ProjectGraph(ctx, s.store, p.OrgID, projectID)
	if err != nil {
		return nil, err
	}
	return &ProjectView{ProjectID: projectID, Graph: g, Sources: states}, nil
}
```

Replace `ServiceCatalog.Services` in `analysis.go`:

```go
// Services returns the services of the project's composed graph by ID,
// with their names. A project never analyzed has none.
func (c ServiceCatalog) Services(ctx context.Context, orgID, projectID string) (map[string]string, error) {
	g, _, err := ProjectGraph(ctx, c.Store, orgID, projectID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, s := range g.Services {
		out[s.ID] = s.Name
	}
	return out, nil
}
```

Change its doc comment to "from the newest successful analysis of each of its sources".

- [ ] **Step 4: Add the route**

In `extensions/discovery/routes.go`, in the analyses group:

```go
	if err := g.GET("/projects/:id/graph", e.projectGraph,
		forge.WithName("projects.graph"),
		forge.WithSummary("The project's application graph, joined from the newest successful analysis of each repository"),
		forge.WithResponseSchema(http.StatusOK, "The graph", discovery.ProjectView{}),
	); err != nil {
		return err
	}
```

```go
func (e *Extension) projectGraph(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	view, err := e.svc.ProjectGraph(c.Request().Context(), p, c.Param("id"))
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, view)
}
```

- [ ] **Step 5: Draft manifests from the composed graph**

In `internal/planning/service.go`:
- Replace the `Analyses` interface with `type Analyses = discovery.Graphs`.
- Replace `ManifestDraft`'s `AnalysisID` and `CommitSHA` with:

```go
	// Sources are the commits the draft was drawn from.
	Sources []graph.SourceRef `json:"sources"`
```

- `DraftManifest` calls `g, states, err := discovery.ProjectGraph(ctx, s.analyses, p.OrgID, projectID)`. It returns `ErrNoAnalysis` when no state has `Shown`, keeps only the sources with a commit (`slices.DeleteFunc(slices.Clone(g.Sources), func(r graph.SourceRef) bool { return r.CommitSHA == "" })`) for `Sources`, and passes `g` to `Draft`.
- `latestAnalysis` still serves `Create` until Task 19. Rewrite it over `ListSources` and `ListSourceAnalyses`, returning the newest succeeded analysis of the first source with one, so it compiles against the new interface.

In `draft.go`, `Source: strings.TrimPrefix(gs.ID, "svc:")` becomes `Source: gs.Local()`.

`planning.compute` still loads one analysis graph by `plan.AnalysisID`; that changes in Task 19.

- [ ] **Step 6: Extend the integration test**

At the end of `TestSources_aProjectBuildsFromSeveralRepositories`, before the site is removed:

```go
	var view struct {
		Graph struct {
			Services []struct {
				ID     string `json:"id"`
				Source string `json:"source"`
			} `json:"services"`
			Sources []struct {
				Alias     string `json:"alias"`
				CommitSHA string `json:"commit_sha"`
			} `json:"sources"`
		} `json:"graph"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+proj.ID+"/graph", nil, &view)
	if len(view.Graph.Sources) != 2 || view.Graph.Sources[1].CommitSHA != exampleSHA ||
		!slices.ContainsFunc(view.Graph.Services, func(s struct {
			ID     string `json:"id"`
			Source string `json:"source"`
		}) bool {
			return s.ID == "svc:site:node:."
		}) {
		t.Fatalf("graph %+v", view.Graph)
	}
```

- [ ] **Step 7: Run everything**

Run: `go test ./... && make test-integration`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/discovery/ internal/planning/ extensions/discovery/ tests/integration/
git commit -m "feat: serve a project's graph joined from every repository"
```

### Task 14: The dashboard draws the composed graph

**Files:**
- Regenerate the client.
- Modify: `apps/dashboard/src/lib/architecture.ts`, `apps/dashboard/src/lib/architecture.test.ts`
- Modify: `apps/dashboard/src/components/architecture.tsx`, `apps/dashboard/src/components/service-graph.tsx`, `apps/dashboard/src/pages/project-detail.tsx`, `apps/dashboard/src/components/deploy.tsx`

**Interfaces:**
- Consumes: Task 13's `projects.graph` route, generated as `useProjectsGraph` with type `ProjectView`.
- Produces: `SourceLink`, `sourceOf`, `linkTo`, `lockLabel`, `clusters`.

- [ ] **Step 1: Regenerate the client**

Run: `make gen-client` with the API running.
Expected: `types.ts` has `ProjectView`, `SourceState`, `SourceRef`, `Graph.sources`, `Evidence.source` and `Service.source`.

- [ ] **Step 2: Write the failing tests**

In `lib/architecture.test.ts`:

```ts
import { clusters, linkTo, lockLabel, sourceOf } from "@/lib/architecture"

const web = { alias: "web", repository: "acme/web", commitSha: sha }
const api = { alias: "api", repository: "acme/api", commitSha: "f".repeat(40) }

describe("linkTo", () => {
  it("links into the source the evidence names", () => {
    expect(linkTo([web, api], { path: "go.mod", line: 3, detail: "", source: "api" })).toBe(
      `https://github.com/acme/api/blob/${"f".repeat(40)}/go.mod#L3`
    )
  })
  it("falls back to the only source, and to nothing when it can't tell", () => {
    expect(sourceOf([web])?.alias).toBe("web")
    expect(linkTo([web, api], { path: "go.mod", detail: "" })).toBe("")
  })
})

describe("lockLabel", () => {
  it("names each source's commit", () => {
    expect(lockLabel([web, api])).toBe("web@0123456 · api@fffffff")
  })
})

describe("clusters", () => {
  it("groups services by source, in alias order", () => {
    const g = {
      services: [
        { id: "svc:web:node:.", source: "web" },
        { id: "svc:api:go:cmd/api", source: "api" },
      ],
    } as Graph
    expect(clusters(g).map((c) => `${c.alias}:${c.services.length}`)).toEqual([
      "api:1",
      "web:1",
    ])
  })
})
```

- [ ] **Step 3: Run them to see them fail**

Run: `pnpm --dir apps/dashboard test`
Expected: FAIL, the functions are not exported.

- [ ] **Step 4: Add the helpers**

In `lib/architecture.ts` (import `Service` from the generated types):

```ts
/** SourceLink is a source as a graph or a plan names it: enough to link into it. */
export interface SourceLink {
  alias: string
  repository: string
  commitSha: string
}

/**
 * sourceOf finds the source a finding was read from. A graph of one
 * source may not name it, so the only one is the answer then.
 */
export function sourceOf(
  sources: SourceLink[],
  alias?: string
): SourceLink | undefined {
  if (alias) return sources.find((s) => s.alias === alias)
  return sources.length === 1 ? sources[0] : undefined
}

/** linkTo links a finding to its line in whichever source it came from, or "" when that isn't known. */
export function linkTo(sources: SourceLink[], ev: Evidence): string {
  const s = sourceOf(sources, ev.source)
  return s?.commitSha ? evidenceURL(s.repository, s.commitSha, ev) : ""
}

/** lockLabel names the commits something was read from: "web@3f2c1a9 · api@9a1b02e". */
export function lockLabel(
  sources: { alias: string; commitSha: string }[]
): string {
  return sources.map((s) => `${s.alias}@${shortSHA(s.commitSha)}`).join(" · ")
}

/** clusters groups a graph's services by the source they came from, in alias order. */
export function clusters(
  graph: Graph
): { alias: string; services: Service[] }[] {
  const by = new Map<string, Service[]>()
  for (const s of graph.services) {
    const alias = s.source ?? ""
    by.set(alias, [...(by.get(alias) ?? []), s])
  }
  return [...by.keys()].sort().map((alias) => ({ alias, services: by.get(alias)! }))
}
```

- [ ] **Step 5: Use the composed graph on the project page**

In `pages/project-detail.tsx`, load the graph with `useQuery(useProjectsGraph, { path: { id: project.id } })` in place of `useAnalysesGraph` on the shown analysis, and refetch it while `analyses.running`. `Discovered`:
- shows `NotAnalyzed` when no `view.sources` entry has `shown`;
- passes `(graph)` to its children, not `(graph, analysis)`.

Every component that took `analysis` to build links takes `sources: SourceLink[]` (pass `graph.sources`) and calls `linkTo(sources, ev)`. In `components/architecture.tsx` that's `Structure`, `EvidenceList`, `ServiceDetail` and `ResourceDetail`. Render an evidence link only when `linkTo` returns a URL.

Replace `GraphCaption`:

```tsx
/** GraphCaption says which commit of each repository the graph was read from. */
export function GraphCaption({ graph }: { graph: Graph }) {
  const read = (graph.sources ?? []).filter((s) => s.commitSha)
  return (
    <p className="text-xs text-muted-foreground">
      Read from{" "}
      {read.map((s, i) => (
        <span key={s.alias}>
          {i > 0 && ", "}
          <a
            className="font-mono text-foreground underline-offset-4 hover:underline"
            href={commitURL(s.repository, s.commitSha)}
            target="_blank"
            rel="noreferrer"
          >
            {read.length > 1 ? `${s.alias}@` : `${s.repository}@`}
            {shortSHA(s.commitSha)}
          </a>
        </span>
      ))}
      . Hover a node to trace what it needs, click it for the evidence. Dashed
      links lead to resources nobody has confirmed yet.
    </p>
  )
}
```

`graph.notes` already shows on the page wherever the existing notes list renders. Check that a "has no successful analysis" note reads well there.

- [ ] **Step 6: One column per repository in the graph**

In `components/service-graph.tsx`'s `columnsOf`, when `clusters(graph).length > 1`, replace the single "Services" column with one column per cluster, titled by alias, keeping the frontends-first order inside each:

```ts
  const groups = clusters(graph)
  const serviceColumns =
    groups.length > 1
      ? groups.map((g) => ({
          title: g.alias,
          items: [...g.services].sort((a, b) => rank(a) - rank(b)).map(asNode),
        }))
      : [{ title: "Services", items: ordered.map(asNode) }]
  return [
    ...serviceColumns,
    {
      title: "Resources",
      items: graph.resources.map((r) => ({
        id: r.id,
        kind: "resource" as const,
        w: nodeW,
        h: nodeH,
        resource: r,
      })),
    },
  ].filter((c) => c.items.length > 0)
```

Packages aren't nodes in this view, so a cross-repository `uses` edge shows in the service's detail sheet. There, under "Uses", a package whose `source` differs from the service's gets the alias as a small outline badge, and a dashed one when the edge's confidence is `likely`. Find the edge with `graph.edges.find((e) => e.from === service.id && e.to === pkg.id)`.

- [ ] **Step 7: Fix the manifest draft's caption**

In `components/deploy.tsx`, `draftedFrom={draft.data.commitSha}` becomes `draftedFrom={lockLabel(draft.data.sources)}`. Wherever that prop is rendered, show the label as it is and drop the `shortSHA` call.

- [ ] **Step 8: Check it**

Run: `make lint-dashboard test-dashboard`, then open a project with two analyzed repositories in `make dev`. You should see two service columns, a caption with two commits, and evidence links into the right repository.
Expected: PASS, and the page reads right.

- [ ] **Step 9: Commit**

```bash
git add apps/dashboard/
git commit -m "feat: draw a project's graph across its repositories"
```

---

# Phase 3: Planning

Ships locksets, scopes and held services. Start only once the cloud connections work in `internal/planning` is committed.

### Task 15: The planning migration

**Files:**
- Modify: `database/migrations/migrations.go` (`create_plan_sources`, `planSourcesUp`, `planSourcesDown`)
- Test: `database/migrations/sources_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestPlanSources_eachPlanKeepsItsCommit(t *testing.T) {
	conn := scratch(t)
	exec(t, conn, projectsUp...)
	exec(t, conn, analysesUp...)
	exec(t, conn, variablesUp...)
	exec(t, conn, projectStyleUp...)
	exec(t, conn, planningUp...)
	exec(t, conn,
		`INSERT INTO loomplane_projects (id, org_id, name, slug, repository_url, status, created_by, created_at, updated_at) VALUES
			('proj_a', 'org', 'A', 'a', 'https://github.com/acme/mono', 'ready', 'usr', now(), now())`,
		`INSERT INTO loomplane_environments (id, org_id, project_id, name, kind, created_at, updated_at) VALUES
			('env_a', 'org', 'proj_a', 'production', 'production', now(), now())`,
		`INSERT INTO loomplane_analyses (id, org_id, project_id, status, repository, commit_sha, created_by, created_at) VALUES
			('anl_a', 'org', 'proj_a', 'succeeded', 'acme/mono', 'abc', 'usr', now())`,
		`INSERT INTO loomplane_manifests (id, org_id, project_id, revision, body, digest, created_by, created_at) VALUES
			('man_a', 'org', 'proj_a', 1, 'version: 1', 'd', 'usr', now())`,
		`INSERT INTO loomplane_plans (id, org_id, project_id, environment_id, environment, status, analysis_id, commit_sha,
			manifest_id, manifest_revision, created_by, created_at) VALUES
			('plan_a', 'org', 'proj_a', 'env_a', 'production', 'approved', 'anl_a', 'abc', 'man_a', 1, 'usr', now())`,
	)
	exec(t, conn, sourcesUp...)
	exec(t, conn, planSourcesUp...)
	exec(t, conn, planSourcesUp...)
	got := strs(t, conn, `SELECT plan_id || ' ' || alias || ' ' || analysis_id || ' ' || commit_sha || ' ' || in_scope::text FROM loomplane_plan_sources`)
	if !slices.Equal(got, []string{"plan_a mono anl_a abc true"}) {
		t.Fatalf("plan sources %q", got)
	}
}
```

If the cloud connections migration altered `loomplane_plans`, apply its statements before `planSourcesUp` too.

- [ ] **Step 2: Run it to see it fail**

Run: `LOOMPLANE_TEST_DATABASE_URL=... go test -tags=integration ./database/migrations/ -run TestPlanSources`
Expected: FAIL, `undefined: planSourcesUp`.

- [ ] **Step 3: Add the migration**

Register `statements("<next free version>", "create_plan_sources", planSourcesUp, planSourcesDown)`. Append:

```go
// A plan pins one analysis of each source it was made from, and whether
// it moves that source or holds it at what's deployed. Each row copies the
// source's alias, repository and directory, so a plan recomposes exactly
// even after someone changes a source. An environment's sources are what
// a deployment last put there; only a successful deployment writes them.
// The backfill gives every older plan the one source it was made from,
// moving. loomplane_plans.analysis_id and commit_sha stay, unread, until a
// later migration drops them.
var planSourcesUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_plan_sources (
		plan_id     TEXT    NOT NULL REFERENCES loomplane_plans (id) ON DELETE CASCADE,
		source_id   TEXT    NOT NULL REFERENCES loomplane_project_sources (id) ON DELETE CASCADE,
		alias       TEXT    NOT NULL,
		repository  TEXT    NOT NULL,
		root        TEXT    NOT NULL DEFAULT '',
		analysis_id TEXT    NOT NULL REFERENCES loomplane_analyses (id) ON DELETE CASCADE,
		commit_sha  TEXT    NOT NULL,
		in_scope    BOOLEAN NOT NULL,
		PRIMARY KEY (plan_id, source_id)
	)`,
	`CREATE TABLE IF NOT EXISTS loomplane_environment_sources (
		environment_id TEXT        NOT NULL REFERENCES loomplane_environments (id) ON DELETE CASCADE,
		source_id      TEXT        NOT NULL REFERENCES loomplane_project_sources (id) ON DELETE CASCADE,
		org_id         TEXT        NOT NULL,
		analysis_id    TEXT        NOT NULL REFERENCES loomplane_analyses (id) ON DELETE CASCADE,
		commit_sha     TEXT        NOT NULL,
		plan_id        TEXT        NOT NULL REFERENCES loomplane_plans (id) ON DELETE CASCADE,
		deployed_at    TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (environment_id, source_id)
	)`,
	`ALTER TABLE loomplane_plans ALTER COLUMN analysis_id DROP NOT NULL`,
	`INSERT INTO loomplane_plan_sources (plan_id, source_id, alias, repository, root, analysis_id, commit_sha, in_scope)
		SELECT p.id, a.source_id, a.alias, a.repository, '', p.analysis_id, p.commit_sha, TRUE
		FROM loomplane_plans p JOIN loomplane_analyses a ON a.id = p.analysis_id
		WHERE a.source_id IS NOT NULL
		ON CONFLICT DO NOTHING`,
}

var planSourcesDown = []string{
	`DROP TABLE IF EXISTS loomplane_environment_sources`,
	`DROP TABLE IF EXISTS loomplane_plan_sources`,
}
```

- [ ] **Step 4: Run it to see it pass**

Run: `LOOMPLANE_TEST_DATABASE_URL=... go test -tags=integration ./database/migrations/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add database/migrations/
git commit -m "feat: pin a plan to one commit per repository"
```

### Task 16: A manifest names each service's repository

**Files:**
- Modify: `pkg/manifest/manifest.go`, `pkg/manifest/schema.json`, `internal/planning/draft.go`
- Test: `pkg/manifest/manifest_test.go`, `internal/planning/draft_test.go`

**Interfaces:**
- Produces: `manifest.Service.Repo string` (`json:"repo,omitempty" yaml:"repo,omitempty"`). `Draft` sets it when the graph has more than one source.

- [ ] **Step 1: Write the failing tests**

In `pkg/manifest/manifest_test.go`:

```go
func TestParse_repo(t *testing.T) {
	m, err := Parse([]byte("version: 1\nservices:\n  site:\n    repo: web\n    source: apps/site\n"))
	if err != nil || m.Services["site"].Repo != "web" {
		t.Fatalf("Parse: %v, %+v", err, m)
	}
	_, err = Parse([]byte("version: 1\nservices:\n  site:\n    repo: Web\n    source: apps/site\n"))
	var ps Problems
	if !errors.As(err, &ps) || len(ps) != 1 || ps[0].Path != "services.site.repo" {
		t.Fatalf("Parse with a bad repo: %v", err)
	}
}
```

In `internal/planning/draft_test.go`:

```go
func TestDraft_namesTheRepositoryWhenThereAreSeveral(t *testing.T) {
	g := graph.Compose([]graph.Part{
		{Source: graph.SourceRef{Alias: "mono", Repository: "acme/mono", CommitSHA: strings.Repeat("a", 40)}, Graph: polyglot(t)},
		{Source: graph.SourceRef{Alias: "kit", Repository: "acme/kit", CommitSHA: strings.Repeat("b", 40)}, Graph: &graph.Graph{Version: graph.SchemaVersion}},
	})
	m := Draft(g, providers.Registry())
	for name, s := range m.Services {
		if s.Repo != "mono" || strings.HasPrefix(s.Source, "mono:") {
			t.Fatalf("%s: %+v", name, s)
		}
	}
}
```

Add the `strings` and `providers` imports if the file lacks them.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./pkg/manifest/ ./internal/planning/ -run 'TestParse_repo|TestDraft_names'`
Expected: FAIL to compile, `Repo` undefined.

- [ ] **Step 3: Add the field, its check and its schema**

In `manifest.go`, before `Source` in `Service`:

```go
	// Repo is the alias of the repository the service is in. A project
	// with one repository can leave it out.
	Repo string `json:"repo,omitempty" yaml:"repo,omitempty"`
```

Next to `sourceRE`, add `repoRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)`. In `Validate`'s service loop, after the source checks:

```go
		if s.Repo != "" && !repoRE.MatchString(s.Repo) {
			add(path+".repo", "%q isn't a repository alias", s.Repo)
		}
```

In `schema.json`, in `$defs.service.properties`, before `source`:

```json
        "repo": {
          "description": "The alias of the repository the service is in. A project with one repository can leave it out.",
          "type": "string",
          "pattern": "^[a-z][a-z0-9-]{0,31}$"
        },
```

In `draft.go`, after building `s`:

```go
		if len(g.Sources) > 1 {
			s.Repo = gs.Source
		}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test ./pkg/manifest/ ./internal/planning/`
Expected: PASS, including `TestSchema_matchesTypes`.

- [ ] **Step 5: Commit**

```bash
git add pkg/manifest/ internal/planning/draft.go internal/planning/draft_test.go
git commit -m "feat: let a manifest say which repository each service is in"
```

### Task 17: The planner matches within a repository

**Files:**
- Modify: `internal/planning/planner.go`, `internal/planning/plan.go`
- Test: `internal/planning/planner_test.go`

**Interfaces:**
- Consumes: Tasks 10 to 12 and 16.
- Produces:
  - `planning.Input.Held map[string]bool`, declared here and used in Task 18.
  - `ServiceConfig.Repo` and `ServiceConfig.CommitSHA`.
  - The finding codes `unknown-repo` and `ambiguous-repo`.
  - Test helpers `federated(t)`, `federatedYAML`, `computeFed(t, yaml, held, bindings...)`.

- [ ] **Step 1: Write the failing tests**

In `planner_test.go`, add `pd "github.com/xraph/loomplane/pkg/discovery"` to the imports, then:

```go
// federated is polyglot as "mono", beside a Next.js site in a repository
// of its own that uses @acme/ui, which a third repository, "kit",
// publishes.
func federated(t *testing.T) *graph.Graph {
	t.Helper()
	site := graph.Build(&pd.Report{Units: []pd.Unit{{
		ID: "node:.", Ecosystem: pd.Node, Name: "site", Path: ".",
		Entrypoints: []pd.Entrypoint{{Name: "site", Path: ".", Frameworks: []pd.Framework{{Name: "next", Kind: pd.Web, Confidence: pd.Likely}}}},
		Requires:    []pd.Requirement{{Name: "@acme/ui", Version: "^1.0.0", Evidence: pd.Evidence{Path: "package.json", Line: 9}}},
	}}})
	kit := graph.Build(&pd.Report{Units: []pd.Unit{{ID: "node:.", Ecosystem: pd.Node, Name: "@acme/ui", Path: "."}}})
	return graph.Compose([]graph.Part{
		{Source: graph.SourceRef{Alias: "mono", Repository: "acme/mono", CommitSHA: strings.Repeat("a", 40)}, Graph: polyglot(t)},
		{Source: graph.SourceRef{Alias: "site", Repository: "acme/site", CommitSHA: strings.Repeat("b", 40)}, Graph: site},
		{Source: graph.SourceRef{Alias: "kit", Repository: "acme/kit", CommitSHA: strings.Repeat("c", 40)}, Graph: kit},
	})
}

const federatedYAML = `version: 1
defaults:
  provider: cloud-run
services:
  api:
    repo: mono
    source: services/api
    needs: [db]
  site:
    repo: site
    source: .
    provider: vercel
    needs: [api]
resources:
  db:
    kind: postgres
    provider: cloud-sql
`

func computeFed(t *testing.T, yaml string, held []string, bindings ...*Binding) *Result {
	t.Helper()
	h := map[string]bool{}
	for _, a := range held {
		h[a] = true
	}
	return Compute(Input{
		Environment: "production", Graph: federated(t), Manifest: parse(t, yaml),
		Providers: providers.Registry(), Bindings: bindings, Held: h,
	})
}

func TestCompute_matchesWithinARepository(t *testing.T) {
	r := computeFed(t, federatedYAML, nil)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", codes(r.Problems))
	}
	site := action(t, r, "service:site")
	if site.Service.Repo != "site" || site.Service.CommitSHA != strings.Repeat("b", 40) {
		t.Fatalf("site %+v", site.Service)
	}
	if api := action(t, r, "service:api"); api.Service.Repo != "mono" {
		t.Fatalf("api %+v", api.Service)
	}

	tests := []struct{ name, yaml, want string }{
		{"no repo with three", strings.Replace(federatedYAML, "    repo: site\n", "", 1), "ambiguous-repo:site"},
		{"unknown repo", strings.Replace(federatedYAML, "repo: site", "repo: nope", 1), "unknown-repo:site"},
		{"wrong repo", strings.Replace(federatedYAML, "repo: mono", "repo: site", 1), "service-not-found:api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := computeFed(t, tt.yaml, nil)
			if !slices.Contains(codes(r.Problems), tt.want) {
				t.Fatalf("problems %v, want %s", codes(r.Problems), tt.want)
			}
		})
	}
	r = computeFed(t, strings.Replace(federatedYAML, "repo: mono", "repo: site", 1), nil)
	for _, p := range r.Problems {
		if p.Code == "service-not-found" && !strings.Contains(p.Message, "site@bbbbbbb") {
			t.Fatalf("the message should name the commit: %s", p.Message)
		}
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/planning/ -run TestCompute_matchesWithin`
Expected: FAIL to compile (`Held`, `ServiceConfig.Repo` undefined).

- [ ] **Step 3: Extend the inputs and the configuration**

In `planner.go`'s `Input`:

```go
	// Held are the aliases of repositories this plan doesn't move: their
	// services stay as deployed.
	Held map[string]bool
```

In `plan.go`'s `ServiceConfig`, after `Source`:

```go
	// Repo is the alias of the repository the service is built from, and
	// CommitSHA the commit: what a deployment fetches. A new commit is a
	// changed configuration, which is what deploying new code means.
	Repo      string `json:"repo,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
```

Change `Source`'s comment to "is the service's entrypoint in its repository".

- [ ] **Step 4: Match within a repository**

In `planServices`, `gs := p.match(name, base, s.Source)` becomes `gs := p.match(name, base, s)`. Replace `match`:

```go
// match finds the graph service a manifest service names: in the
// repository its repo names, at its source.
func (p *planner) match(name, base string, s manifest.Service) *graph.Service {
	repo, ok := p.repoOf(name, base, s)
	if !ok {
		return nil
	}
	source := s.Source
	in := func(gs *graph.Service) bool { return repo == "" || gs.Source == repo }
	var found []*graph.Service
	for i := range p.in.Graph.Services {
		gs := &p.in.Graph.Services[i]
		if in(gs) && (gs.Local() == source || gs.Entrypoint == source) {
			found = append(found, gs)
		}
	}
	if len(found) == 0 {
		// A unit's directory names its one service too.
		for i := range p.in.Graph.Services {
			if gs := &p.in.Graph.Services[i]; in(gs) && gs.Path == source {
				found = append(found, gs)
			}
		}
	}
	switch len(found) {
	case 0:
		p.problem(Finding{
			Code: "service-not-found", Subject: ServiceSubject, Name: name, Path: base + ".source",
			Message: fmt.Sprintf("Analysis found no service at %s in %s.", source, p.where(repo)),
		})
		return nil
	case 1:
		return found[0]
	}
	ids := make([]string, 0, len(found))
	for _, gs := range found {
		ids = append(ids, gs.Local())
	}
	p.problem(Finding{
		Code: "ambiguous-source", Subject: ServiceSubject, Name: name, Path: base + ".source",
		Message: fmt.Sprintf("More than one service is at %s. Name one: %s.", source, strings.Join(ids, ", ")),
	})
	return nil
}

// repoOf settles which repository a manifest service is in. A graph from
// before sources has none, and matches anywhere.
func (p *planner) repoOf(name, base string, s manifest.Service) (string, bool) {
	sources := p.in.Graph.Sources
	if s.Repo != "" {
		if slices.ContainsFunc(sources, func(r graph.SourceRef) bool { return r.Alias == s.Repo }) {
			return s.Repo, true
		}
		p.problem(Finding{
			Code: "unknown-repo", Subject: ServiceSubject, Name: name, Path: base + ".repo",
			Message: fmt.Sprintf("%s.repo is %s, which isn't one of this project's repositories: %s.", base, s.Repo, aliases(sources)),
		})
		return "", false
	}
	switch len(sources) {
	case 0:
		return "", true
	case 1:
		return sources[0].Alias, true
	}
	p.problem(Finding{
		Code: "ambiguous-repo", Subject: ServiceSubject, Name: name, Path: base + ".repo",
		Message: fmt.Sprintf("This project builds from %d repositories (%s), so say which one %s is in with %s.repo.", len(sources), aliases(sources), name, base),
	})
	return "", false
}

func aliases(sources []graph.SourceRef) string {
	names := make([]string, 0, len(sources))
	for _, s := range sources {
		names = append(names, s.Alias)
	}
	return strings.Join(names, ", ")
}

// where names a repository at the commit it was read at, for messages.
func (p *planner) where(repo string) string {
	if c := p.commit(repo); repo != "" && c != "" {
		return repo + "@" + c[:min(7, len(c))]
	}
	return "this commit"
}

func (p *planner) commit(alias string) string {
	for _, s := range p.in.Graph.Sources {
		if s.Alias == alias {
			return s.CommitSHA
		}
	}
	return ""
}
```

In `serviceConfig`, set `Repo: gs.Source, CommitSHA: p.commit(gs.Source)` on the `ServiceConfig` it builds.

A graph with no sources leaves both empty, so every existing planner test, and every binding written before this, still compares equal.

- [ ] **Step 5: Run the planning tests**

Run: `go test ./internal/planning/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/planning/
git commit -m "feat: match manifest services within the repository they name"
```

### Task 18: Held services, and what moving a package touches

**Files:**
- Modify: `internal/planning/planner.go`
- Test: `internal/planning/planner_test.go`

**Interfaces:**
- Consumes: Task 17's `Input.Held`.
- Produces: the finding codes `held`, `need-held` and `cross-repo-impact`.

- [ ] **Step 1: Write the failing tests**

```go
const federatedWithoutSite = `version: 1
defaults:
  provider: cloud-run
services:
  api:
    repo: mono
    source: services/api
    needs: [db]
resources:
  db:
    kind: postgres
    provider: cloud-sql
`

func TestCompute_heldServicesStayAsDeployed(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))

	// The site's repository is held, so a change to it waits.
	changed := strings.Replace(federatedYAML, "    provider: vercel\n", "    provider: vercel\n    public: false\n", 1)
	r := computeFed(t, changed, []string{"site"}, live...)
	site := action(t, r, "service:site")
	if site.Op != Keep || !strings.Contains(site.Reason, "Held") || !site.Service.Public {
		t.Fatalf("site %+v", site)
	}
	if !slices.Contains(codes(r.Warnings), "held:site") {
		t.Fatalf("warnings %v", codes(r.Warnings))
	}

	// Gone from the manifest, a held service still isn't deleted.
	r = computeFed(t, federatedWithoutSite, []string{"site"}, live...)
	if slices.ContainsFunc(r.Actions, func(a Action) bool { return a.ID == "service:site" }) {
		t.Fatalf("a held service was planned: %+v", r.Actions)
	}
	if !slices.Contains(codes(r.Warnings), "held:site") {
		t.Fatalf("warnings %v", codes(r.Warnings))
	}
}

func TestCompute_aServiceNeedingAHeldServiceThatRunsNowhereIsAProblem(t *testing.T) {
	r := computeFed(t, federatedYAML, []string{"mono"})
	if slices.ContainsFunc(r.Actions, func(a Action) bool { return a.ID == "service:api" }) {
		t.Fatal("a held service that runs nowhere was created")
	}
	if !slices.Contains(codes(r.Warnings), "held:api") || !slices.Contains(codes(r.Problems), "need-held:site") {
		t.Fatalf("warnings %v, problems %v", codes(r.Warnings), codes(r.Problems))
	}
}

func TestCompute_movingAPackageNamesWhatItsHeldUsersAre(t *testing.T) {
	live := deployed(t, computeFed(t, federatedYAML, nil))
	r := computeFed(t, federatedYAML, []string{"site"}, live...)
	var impact *Finding
	for i := range r.Warnings {
		if r.Warnings[i].Code == "cross-repo-impact" {
			impact = &r.Warnings[i]
		}
	}
	if impact == nil || impact.Name != "@acme/ui" || !strings.Contains(impact.Message, "site in site") {
		t.Fatalf("warnings %+v", r.Warnings)
	}
	// Nothing held, nothing to warn about.
	if slices.ContainsFunc(computeFed(t, federatedYAML, nil, live...).Warnings, func(f Finding) bool { return f.Code == "cross-repo-impact" }) {
		t.Fatal("a plan that moves everything warned about impact")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/planning/ -run 'Held|heldServices|movingAPackage'`
Expected: FAIL. Held services are planned like any other.

- [ ] **Step 3: Hold services**

Add `heldNew map[string]string // manifest name to alias, held services that run nowhere yet` to `planner`, and initialize it in `Compute`.

In `planServices`, right after `if !manifest.On(set.Enabled) { continue }`:

```go
		if p.in.Held[gs.Source] && p.bindings[actionID(ServiceSubject, name)] == nil {
			p.heldNew[name] = gs.Source
			p.warn(Finding{
				Code: "held", Subject: ServiceSubject, Name: name, Path: base,
				Message: fmt.Sprintf("%s is in %s, which this plan holds back, and nothing runs it in %s yet, so it isn't created.", name, gs.Source, p.in.Environment),
			})
			continue
		}
```

After the loop that calls `p.link`, and before `p.order()`, call `p.hold()`:

```go
// hold keeps each held service as it's deployed: a keep action carrying
// the deployed configuration, so whatever needs it still gets its address
// and nothing deletes it. A change the manifest would make waits for a
// plan that moves its repository, with a warning saying so.
func (p *planner) hold() {
	for _, name := range p.in.Manifest.ServiceNames() {
		a, gs := p.services[name], p.graphServices[name]
		if a == nil || gs == nil || !p.in.Held[gs.Source] {
			continue
		}
		b := p.bindings[a.ID]
		if b == nil {
			continue // planServices left those out
		}
		if a.Op != Keep {
			p.warn(Finding{
				Code: "held", Subject: ServiceSubject, Name: name, Path: "services." + name,
				Message: fmt.Sprintf("%s is in %s, which this plan holds back, so this waits for a plan that moves %s: %s", name, gs.Source, gs.Source, a.Reason),
			})
		}
		var cfg ServiceConfig
		if json.Unmarshal(b.Config, &cfg) == nil {
			a.Service = &cfg
		}
		a.Op, a.Changes, a.Provider, a.Region = Keep, nil, b.Provider, b.Region
		a.Reason = fmt.Sprintf("Held: this plan doesn't move %s.", gs.Source)
	}
}
```

In `link`, inside the `dep == nil` branch for a needed service, before the existing `need-off` check:

```go
			if alias, held := p.heldNew[need]; held {
				p.problem(Finding{
					Code: "need-held", Subject: ServiceSubject, Name: name, Path: base,
					Message: fmt.Sprintf("%s needs %s, which is in %s. This plan holds %s back and nothing runs %s in %s yet, so add %s to the scope.", name, need, alias, alias, need, p.in.Environment, alias),
				})
				continue
			}
```

In `planDeletes`, after the `continue` for subjects still planned:

```go
		if b.Subject == ServiceSubject {
			var cfg ServiceConfig
			if json.Unmarshal(b.Config, &cfg) == nil && p.in.Held[cfg.Repo] {
				p.warn(Finding{
					Code: "held", Subject: ServiceSubject, Name: b.Name, Path: "services",
					Message: fmt.Sprintf("The manifest no longer has %s, but it's in %s, which this plan holds back, so it keeps running.", b.Name, cfg.Repo),
				})
				continue
			}
		}
```

- [ ] **Step 4: Warn about impact**

Call `p.impact()` in `Compute` right after `p.checkFindings()`:

```go
// impact warns when a package this plan moves is built into services it
// holds back. Those keep the version they declare, so nothing breaks now,
// but the next plan that moves them picks the change up.
func (p *planner) impact() {
	if len(p.in.Held) == 0 {
		return
	}
	g := p.in.Graph
	source, label := map[string]string{}, map[string]string{}
	for _, s := range g.Services {
		source[s.ID], label[s.ID] = s.Source, s.Name
	}
	for _, pk := range g.Packages {
		source[pk.ID], label[pk.ID] = pk.Source, pk.Name
	}
	users := map[string][]string{}
	for _, e := range g.Edges {
		from, to := source[e.From], source[e.To]
		if e.Kind != graph.Uses || from == "" || to == "" || from == to || !p.in.Held[from] || p.in.Held[to] {
			continue
		}
		users[e.To] = append(users[e.To], fmt.Sprintf("%s in %s", label[e.From], from))
	}
	for _, id := range slices.Sorted(maps.Keys(users)) {
		list := slices.Compact(slices.Sorted(slices.Values(users[id])))
		p.warn(Finding{
			Code: "cross-repo-impact", Name: label[id],
			Message: fmt.Sprintf("%s, from %s, moves in this plan and is built into %s, which it holds back. Those keep the version they declare until a plan moves them.", label[id], source[id], strings.Join(list, ", ")),
		})
	}
}
```

- [ ] **Step 5: Run the planning tests**

Run: `go test ./internal/planning/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/planning/
git commit -m "feat: hold back the services of repositories a plan doesn't move"
```

### Task 19: Plans pin a lockset

**Files:**
- Create: `internal/planning/lockset.go`
- Test: `internal/planning/lockset_test.go`
- Modify: `internal/planning/plan.go`, `internal/planning/service.go`, `internal/planning/pgstore.go`, `extensions/planning/extension.go`

**Interfaces:**
- Consumes: Tasks 13 and 15 to 18.
- Produces:
  - `type PlanSource struct{ SourceID, Alias, Repository, Root, AnalysisID, CommitSHA string; InScope bool }`.
  - `type DeployedSource struct{ EnvironmentID, SourceID, AnalysisID, CommitSHA, PlanID string; DeployedAt time.Time }`.
  - `Plan.Sources []PlanSource` replaces `AnalysisID` and `CommitSHA`.
  - `CreatePlanInput.Scope []string`.
  - `Store.DeployedSources(ctx, orgID, environmentID string) ([]*DeployedSource, error)`.
  - `ErrNoSources`, `ErrNeverDeployed`.
  - `lockset(...)`.

- [ ] **Step 1: Write the failing test**

```go
package planning

import (
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/discovery"
)

func TestLockset(t *testing.T) {
	sources := []*discovery.Source{
		{ID: "src_api", Alias: "api", Repository: "acme/api"},
		{ID: "src_web", Alias: "web", Repository: "acme/web", Root: "apps/web"},
	}
	latest := map[string]*discovery.Analysis{
		"src_api": {ID: "anl_api2", CommitSHA: "api2"},
		"src_web": {ID: "anl_web2", CommitSHA: "web2"},
	}
	deployed := map[string]*DeployedSource{"src_api": {SourceID: "src_api", AnalysisID: "anl_api1", CommitSHA: "api1"}}

	all, err := lockset(sources, latest, deployed, nil, "production")
	if err != nil || len(all) != 2 || !all[0].InScope || all[0].CommitSHA != "api2" || all[1].Root != "apps/web" {
		t.Fatalf("everything: %v, %+v", err, all)
	}
	web, err := lockset(sources, latest, deployed, []string{"web"}, "production")
	if err != nil || web[0].InScope || web[0].CommitSHA != "api1" || web[0].AnalysisID != "anl_api1" || !web[1].InScope {
		t.Fatalf("web only: %v, %+v", err, web)
	}

	tests := []struct {
		name     string
		sources  []*discovery.Source
		latest   map[string]*discovery.Analysis
		deployed map[string]*DeployedSource
		scope    []string
		want     error
	}{
		{"no sources", nil, latest, deployed, nil, ErrNoSources},
		{"a source never analyzed", sources, map[string]*discovery.Analysis{"src_api": latest["src_api"]}, deployed, nil, ErrNoAnalysis},
		{"holding one never deployed", sources, latest, deployed, []string{"api"}, ErrNeverDeployed},
		{"an unknown or removed alias", sources, latest, deployed, []string{"gone"}, ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := lockset(tt.sources, tt.latest, tt.deployed, tt.scope, "production"); !errors.Is(err, tt.want) {
				t.Fatalf("lockset: %v, want %v", err, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/planning/ -run TestLockset`
Expected: FAIL to compile.

- [ ] **Step 3: Add the types and `lockset`**

In `plan.go`:

```go
// PlanSource pins one repository of a plan to the analysis it was planned
// from, copying what it needs to recompose the graph exactly later.
type PlanSource struct {
	SourceID   string `json:"source_id"`
	Alias      string `json:"alias"`
	Repository string `json:"repository"`
	Root       string `json:"root"`
	AnalysisID string `json:"analysis_id"`
	CommitSHA  string `json:"commit_sha"`
	// InScope says whether the plan moves this repository. One it holds
	// stays at what's deployed.
	InScope bool `json:"in_scope"`
}

// DeployedSource is the commit of one repository deployed in one
// environment. A successful deployment writes it (milestone 4).
type DeployedSource struct {
	EnvironmentID string    `json:"environment_id"`
	SourceID      string    `json:"source_id"`
	AnalysisID    string    `json:"analysis_id"`
	CommitSHA     string    `json:"commit_sha"`
	PlanID        string    `json:"plan_id"`
	DeployedAt    time.Time `json:"deployed_at"`
}
```

- In `Plan`, replace `AnalysisID` and `CommitSHA` with `Sources []PlanSource `json:"sources"``. Update the type comment to "pinned to one commit of each repository and the manifest revision it was made from".
- `normalize` fills a nil `Sources` with `[]PlanSource{}`.
- Add `DeployedSources(ctx context.Context, orgID, environmentID string) ([]*DeployedSource, error)` to `Store`.
- Add the errors:

```go
	ErrNoSources     = errors.New("this project has no repositories; add one first")
	ErrNeverDeployed = errors.New("a repository this plan holds back has never been deployed here")
```

Create `lockset.go`:

```go
package planning

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/xraph/loomplane/internal/discovery"
)

// lockset pins each live repository of a plan: one in scope to its newest
// successful analysis, one held to what's deployed in the environment.
// scope names the repositories that move; empty means all of them. A name
// scope gives that isn't a live repository is refused, never ignored.
func lockset(sources []*discovery.Source, latest map[string]*discovery.Analysis, deployed map[string]*DeployedSource, scope []string, env string) ([]PlanSource, error) {
	if len(sources) == 0 {
		return nil, ErrNoSources
	}
	moves := map[string]bool{}
	for _, a := range scope {
		a = strings.TrimSpace(a)
		if !slices.ContainsFunc(sources, func(s *discovery.Source) bool { return s.Alias == a }) {
			return nil, fmt.Errorf("%w: %q isn't one of this project's repositories", ErrInvalid, a)
		}
		moves[a] = true
	}
	out := make([]PlanSource, 0, len(sources))
	for _, s := range sources {
		ps := PlanSource{SourceID: s.ID, Alias: s.Alias, Repository: s.Repository, Root: s.Root, InScope: len(moves) == 0 || moves[s.Alias]}
		if ps.InScope {
			a := latest[s.ID]
			if a == nil {
				return nil, fmt.Errorf("%w: %s has no successful analysis", ErrNoAnalysis, s.Alias)
			}
			ps.AnalysisID, ps.CommitSHA = a.ID, a.CommitSHA
		} else {
			d := deployed[s.ID]
			if d == nil {
				return nil, fmt.Errorf("%w: %s has never been deployed to %s, so include it in the plan", ErrNeverDeployed, s.Alias, env)
			}
			ps.AnalysisID, ps.CommitSHA = d.AnalysisID, d.CommitSHA
		}
		out = append(out, ps)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out, nil
}
```

- [ ] **Step 4: Pin, plan and digest from the lockset**

In `service.go`:
- `CreatePlanInput` gains `Scope []string `json:"scope,omitempty"``, with the comment "Scope names the repositories the plan moves. Empty means all of them; the rest stay at what's deployed."
- Delete `latestAnalysis`, and add:

```go
// pin builds a plan's lockset from what's analyzed and what's deployed.
func (s *Service) pin(ctx context.Context, orgID, projectID string, env *projects.Environment, scope []string) ([]PlanSource, error) {
	sources, err := s.analyses.ListSources(ctx, orgID, projectID, false)
	if err != nil {
		return nil, err
	}
	latest := map[string]*discovery.Analysis{}
	for _, src := range sources {
		list, err := s.analyses.ListSourceAnalyses(ctx, orgID, src.ID, analysisLookback)
		if err != nil {
			return nil, err
		}
		for _, a := range list {
			if a.Status == discovery.StatusSucceeded {
				latest[src.ID] = a
				break
			}
		}
	}
	rows, err := s.store.DeployedSources(ctx, orgID, env.ID)
	if err != nil {
		return nil, err
	}
	deployed := map[string]*DeployedSource{}
	for _, d := range rows {
		deployed[d.SourceID] = d
	}
	return lockset(sources, latest, deployed, scope, env.Name)
}
```

- In `Create`, replace the `latestAnalysis` call with `pinned, err := s.pin(ctx, p.OrgID, projectID, env, in.Scope)`. Set `Sources: pinned` on the plan, in place of `AnalysisID` and `CommitSHA`. The audit attribute `commit_sha` becomes `"sources": lockDescription(pinned)`:

```go
// lockDescription is a lockset for the audit trail: "api@3f2c1a9 moves".
func lockDescription(ps []PlanSource) []string {
	out := make([]string, 0, len(ps))
	for _, s := range ps {
		state := "held"
		if s.InScope {
			state = "moves"
		}
		out = append(out, s.Alias+"@"+s.CommitSHA+" "+state)
	}
	return out
}
```

- In `revisionInput`, replace `AnalysisID` and `CommitSHA` with `Sources []PlanSource `json:"sources"``.
- In `compute`, replace loading one graph with:

```go
	parts := make([]graph.Part, 0, len(plan.Sources))
	held := map[string]bool{}
	for _, ps := range plan.Sources {
		g, err := s.analyses.Graph(ctx, plan.OrgID, ps.AnalysisID)
		if err != nil {
			return nil, "", fmt.Errorf("load the analysis of %s: %w", ps.Alias, err)
		}
		parts = append(parts, graph.Part{
			Source: graph.SourceRef{Alias: ps.Alias, Repository: ps.Repository, Root: ps.Root, CommitSHA: ps.CommitSHA, AnalysisID: ps.AnalysisID},
			Graph:  g,
		})
		if !ps.InScope {
			held[ps.Alias] = true
		}
	}
	g := graph.Compose(parts)
```

  Pass `Held: held` in the `Input`, and `Sources: plan.Sources` in the `revisionInput`.

In `extensions/planning/extension.go`'s `fail`, add `errors.Is(err, planning.ErrNoSources), errors.Is(err, planning.ErrNeverDeployed)` to the `ErrNoAnalysis` case, which answers 412.

- [ ] **Step 5: Store the lockset**

In `pgstore.go`, remove `analysis_id, commit_sha` from `planColumns` and from `scanPlan`, and leave the CreatePlan INSERT without them. In `CreatePlan`, after the plan INSERT succeeds and before `tx.Commit`:

```go
	for _, ps := range p.Sources {
		if _, err = tx.Exec(ctx, `INSERT INTO loomplane_plan_sources
			(plan_id, source_id, alias, repository, root, analysis_id, commit_sha, in_scope)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			p.ID, ps.SourceID, ps.Alias, ps.Repository, ps.Root, ps.AnalysisID, ps.CommitSHA, ps.InScope); err != nil {
			return nil, fmt.Errorf("insert plan source %s: %w", ps.Alias, err)
		}
	}
```

Add, and call it from `GetPlan` (with the one plan) and `ListPlans` (with the whole page) before returning:

```go
// withSources loads each plan's lockset in one query.
func (s *PgStore) withSources(ctx context.Context, plans ...*Plan) error {
	if len(plans) == 0 {
		return nil
	}
	ids := make([]string, len(plans))
	by := map[string]*Plan{}
	for i, p := range plans {
		ids[i], by[p.ID] = p.ID, p
		p.Sources = []PlanSource{}
	}
	rows, err := s.db.Query(ctx, `SELECT plan_id, source_id, alias, repository, root, analysis_id, commit_sha, in_scope
		FROM loomplane_plan_sources WHERE plan_id = ANY($1) ORDER BY plan_id, alias`, ids)
	if err != nil {
		return fmt.Errorf("load plan sources: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	for rows.Next() {
		var planID string
		var ps PlanSource
		if err := rows.Scan(&planID, &ps.SourceID, &ps.Alias, &ps.Repository, &ps.Root, &ps.AnalysisID, &ps.CommitSHA, &ps.InScope); err != nil {
			return fmt.Errorf("scan plan source: %w", err)
		}
		by[planID].Sources = append(by[planID].Sources, ps)
	}
	return rows.Err()
}

// DeployedSources implements Store.
func (s *PgStore) DeployedSources(ctx context.Context, orgID, environmentID string) ([]*DeployedSource, error) {
	rows, err := s.db.Query(ctx, `SELECT environment_id, source_id, analysis_id, commit_sha, plan_id, deployed_at
		FROM loomplane_environment_sources WHERE org_id = $1 AND environment_id = $2 ORDER BY source_id`, orgID, environmentID)
	if err != nil {
		return nil, fmt.Errorf("list deployed sources: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*DeployedSource{}
	for rows.Next() {
		var d DeployedSource
		if err := rows.Scan(&d.EnvironmentID, &d.SourceID, &d.AnalysisID, &d.CommitSHA, &d.PlanID, &d.DeployedAt); err != nil {
			return nil, fmt.Errorf("scan deployed source: %w", err)
		}
		d.DeployedAt = d.DeployedAt.UTC()
		out = append(out, &d)
	}
	return out, rows.Err()
}
```

Fix every other compile error from the removed fields. `go build ./... && go vet ./...` lists them, including any test fakes of `planning.Store`.

- [ ] **Step 6: Run everything**

Run: `go test ./... && make test-integration`
Expected: PASS. The existing planning integration test plans every source, the default, and keeps working.

- [ ] **Step 7: Commit**

```bash
git add internal/planning/ extensions/planning/
git commit -m "feat: plan from one commit per repository, moving all of them or a scope"
```

### Task 20: Scoped plans, end to end and in the dashboard

**Files:**
- Modify: `tests/integration/sources_test.go`, `tests/integration/planning_test.go` (`planBody`)
- Regenerate the client.
- Modify: `apps/dashboard/src/lib/plans.ts`, `apps/dashboard/src/lib/plans.test.ts`, `apps/dashboard/src/components/deploy.tsx`

**Interfaces:**
- Consumes: Task 19.
- Produces: `scopeBody(all: string[], chosen: string[]): { scope?: string[] }` in `lib/plans.ts`.

- [ ] **Step 1: Write the failing integration test**

Add `EnvironmentID string `json:"environment_id"`` and the lockset to `planBody`:

```go
	Sources []struct {
		SourceID   string `json:"source_id"`
		Alias      string `json:"alias"`
		AnalysisID string `json:"analysis_id"`
		CommitSHA  string `json:"commit_sha"`
		InScope    bool   `json:"in_scope"`
	} `json:"sources"`
```

Add to `sources_test.go` (imports `context` and `github.com/jackc/pgx/v5`):

```go
func TestPlanning_aScopedPlanHoldsWhatsDeployed(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubRepos(t, map[string][]byte{
		"acme/mono": exampleTarball(t),
		"acme/site": tarball(t, "acme-site-fedcba9", siteFiles),
	}).URL
	cfg.Analysis.WorkDir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)

	var proj struct {
		ID    string `json:"id"`
		OrgID string `json:"org_id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Shop", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	c.must(http.StatusCreated, http.MethodPost, base+"/sources", map[string]string{"repository": "acme/site", "alias": "site"}, nil)
	analyzeAll(t, cfg, c, proj.ID)
	manifest := "version: 1\ndefaults:\n  provider: cloud-run\nservices:\n  api:\n    repo: mono\n    source: services/api\n  site:\n    repo: site\n    source: .\n    provider: vercel\n"
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": manifest}, nil)

	var full planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]any{"environment": "production"}, &full)
	waitPlanned(t, c, &full)
	if full.Status != "ready" || len(full.Sources) != 2 || !full.Sources[0].InScope || !full.Sources[1].InScope {
		t.Fatalf("full plan %+v", full)
	}

	// Nothing of mono is deployed, so a plan that holds it can't be made.
	c.must(http.StatusPreconditionFailed, http.MethodPost, base+"/plans", map[string]any{"environment": "production", "scope": []string{"site"}}, nil)
	c.must(http.StatusUnprocessableEntity, http.MethodPost, base+"/plans", map[string]any{"environment": "production", "scope": []string{"gone"}}, nil)

	// What milestone 4's executor will write once a deployment succeeds.
	conn, err := pgx.Connect(context.Background(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	mono := full.Sources[0]
	if _, err = conn.Exec(context.Background(), `INSERT INTO loomplane_environment_sources
		(environment_id, source_id, org_id, analysis_id, commit_sha, plan_id, deployed_at) VALUES ($1, $2, $3, $4, $5, $6, now())`,
		full.EnvironmentID, mono.SourceID, proj.OrgID, mono.AnalysisID, mono.CommitSHA, full.ID); err != nil {
		t.Fatal(err)
	}

	var scoped planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]any{"environment": "production", "scope": []string{"site"}}, &scoped)
	waitPlanned(t, c, &scoped)
	if scoped.Status != "ready" || scoped.Sources[0].Alias != "mono" || scoped.Sources[0].InScope || scoped.Sources[0].CommitSHA != mono.CommitSHA || !scoped.Sources[1].InScope {
		t.Fatalf("scoped plan %+v", scoped)
	}
}
```

If `ErrInvalid` maps to another status in `extensions/planning`, use that status for the `gone` scope.

- [ ] **Step 2: Run it**

Run: `make test-integration`
Expected: PASS once Task 19 is in. If it fails, fix the cause before going on.

- [ ] **Step 3: Write the failing dashboard test**

In `lib/plans.test.ts`:

```ts
import { scopeBody } from "@/lib/plans"

describe("scopeBody", () => {
  it("sends no scope when every repository moves", () => {
    expect(scopeBody(["api", "web"], ["web", "api"])).toEqual({})
  })
  it("names the repositories that move otherwise", () => {
    expect(scopeBody(["api", "web"], ["web"])).toEqual({ scope: ["web"] })
  })
})
```

Run: `make gen-client`, then `pnpm --dir apps/dashboard test`.
Expected: FAIL, `scopeBody` isn't exported. `typecheck` lists `plan.analysisId` and `plan.commitSha` in `deploy.tsx`.

- [ ] **Step 4: Add `scopeBody` and the scope picker**

In `lib/plans.ts`:

```ts
/**
 * scopeBody is the part of a new plan's body that says which repositories
 * move. With all of them, it says nothing, so the plan follows the
 * project when it gains another.
 */
export function scopeBody(
  all: string[],
  chosen: string[]
): { scope?: string[] } {
  const moving = all.filter((a) => chosen.includes(a))
  return moving.length === all.length ? {} : { scope: moving }
}
```

In `components/deploy.tsx`'s `PlansCard`:
- Load the sources with `useQuery(useSourcesList, { path: { id: project.id } })`.
- Keep `const [moving, setMoving] = useState<string[] | null>(null)`, where `null` means every source.
- When there's more than one source, render one toggle button per alias before the environment select. A moving source is `variant="secondary"`, a held one `variant="outline"`, each `size="sm"` and labelled with the alias. Clicking one adds or removes it. Disable the plan button when nothing is chosen.
- The create body becomes `{ environment, ...scopeBody(aliases, moving ?? aliases) }`.

In `PlanBody`, the replan keeps the plan's own scope: `{ environment: plan.environment, ...scopeBody(plan.sources.map((s) => s.alias), plan.sources.filter((s) => s.inScope).map((s) => s.alias)) }`.

- [ ] **Step 5: Show the lockset**

In `deploy.tsx`:
- The plan list row's `shortSHA(p.commitSha)` becomes `lockLabel(p.sources)`.
- In `PlanBody`, the line that names the commit the plan was made from shows one row per entry of `plan.sources`. Each row has the alias, a commit link built with `commitURL(s.repository, s.commitSha)` and `shortSHA`, and a badge reading "moves" or "held". Held rows are muted.
- Drop `evidenceFrom` and the `analysis` prop. `Findings` links evidence with `linkTo(plan.sources, ev)`, which works because a `PlanSource` has `alias`, `repository` and `commitSha`.
- The `held`, `need-held` and `cross-repo-impact` findings render through the existing `Findings` list. Check that each message reads well there.

- [ ] **Step 6: Check it**

Run: `make lint-dashboard test-dashboard`. Then, in `make dev`, plan a project with two repositories twice, once with both and once with one held. The held one is refused with the "never been deployed" message until something is deployed. The lockset shows on the plan.
Expected: PASS, and the screens read right.

- [ ] **Step 7: Commit**

```bash
git add tests/integration/ apps/dashboard/
git commit -m "feat: plan a scope of repositories and show each plan's lockset"
```

---

## After the last task

- Update `docs/architecture.md` where it says an analysis is "one commit's tarball" and that a plan pins one commit. Each analysis is still one tarball, but a project's graph is now composed and a plan pins a lockset. Write it in the house voice: no em dashes.
- Add "drop `repository_url`, `loomplane_plans.analysis_id` and `commit_sha`" and "write `loomplane_environment_sources` on a successful deployment" to `docs/implementation-plan.md` under what's left.
