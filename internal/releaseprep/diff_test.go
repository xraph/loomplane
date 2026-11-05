package releaseprep_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/releaseprep"
	"github.com/xraph/loomplane/internal/repositories"
)

func TestIsMigration(t *testing.T) {
	for path, want := range map[string]bool{
		"db/migrate/20260101_add_users.rb":   true,
		"prisma/migrations/x/migration.sql":  true,
		"alembic/versions/ab12.py":           true,
		"internal/migrations/001.go":         true,
		"schema.prisma":                      true,
		"db/schema.rb":                       true,
		"db/structure.sql":                   true,
		"sql/queries.sql":                    true,
		"DB/Migrate/1.rb":                    true,
		"src/migration/1.ts":                 true,
		"ops/flyway/V1__init.txt":            true,
		"ops/liquibase/master.xml":           true,
		"db/changelog/001.xml":               true,
		"cmd/migrator/main.go":               false,
		"docs/migrating.md":                  false,
		"src/app.ts":                         false,
		"changelog/2026.md":                  false,
		"docs/changelog/db.md":               false,
		"migrations.md":                      false,
		"alembic/env.py":                     false,
		"internal/migrations_test/helper.go": false,
		"":                                   false,
	} {
		if got := releaseprep.IsMigration(path); got != want {
			t.Errorf("IsMigration(%q) = %v, want %v", path, got, want)
		}
	}
}

func comparison(n int) *repositories.Comparison {
	c := &repositories.Comparison{Status: "ahead", AheadBy: n, TotalCommits: n}
	for i := range n {
		c.Commits = append(c.Commits, repositories.ComparedCommit{
			SHA: fmt.Sprintf("sha%03d", i), Message: fmt.Sprintf("commit %d\n\nbody", i), Author: "Ada",
			Date: time.Date(2026, 11, 1, 0, i, 0, 0, time.UTC),
		})
	}
	return c
}

func TestBuildDiffOrdersAndBounds(t *testing.T) {
	c := comparison(180)
	c.Commits[0].Message = strings.Repeat("é", 300) + "\nrest"
	got := releaseprep.BuildDiff(releaseprep.SourceDiff{SourceID: "s"}, c)
	if len(got.Commits) != releaseprep.MaxCommits || got.TotalCommits != 180 {
		t.Fatalf("commits = %d, total = %d", len(got.Commits), got.TotalCommits)
	}
	if got.Commits[0].SHA != "sha179" || got.Commits[99].SHA != "sha080" {
		t.Fatalf("order: first %s last %s", got.Commits[0].SHA, got.Commits[99].SHA)
	}
	if got.Commits[0].Title != "commit 179" || got.Commits[0].Author != "Ada" || got.Commits[0].At.IsZero() {
		t.Fatalf("commit = %+v", got.Commits[0])
	}
	short := comparison(1)
	short.Commits[0].Message = strings.Repeat("é", 300) + "\nrest"
	if title := releaseprep.BuildDiff(releaseprep.SourceDiff{}, short).Commits[0].Title; len([]rune(title)) != 200 || strings.Contains(title, "rest") {
		t.Fatalf("title = %d runes", len([]rune(title)))
	}
}

func TestBuildDiffFindsMigrations(t *testing.T) {
	c := comparison(1)
	for i := range 12 {
		c.Files = append(c.Files, repositories.ChangedFile{
			Filename: fmt.Sprintf("db/migrate/%02d_x.sql", i), Status: "added", Additions: 5,
			Patch: strings.Repeat("+x\n", 7000), // about 21 KiB
		})
	}
	c.Files = append(c.Files, repositories.ChangedFile{Filename: "src/app.ts", Status: "modified", Patch: "@@"})
	got := releaseprep.BuildDiff(releaseprep.SourceDiff{}, c)
	if len(got.Migrations) != 12 {
		t.Fatalf("migrations = %d", len(got.Migrations))
	}
	for i, m := range got.Migrations {
		if i < releaseprep.MaxPatchedMigrations {
			if m.Patch == "" || len(m.Patch) > releaseprep.MaxPatchBytes || !m.Truncated {
				t.Errorf("migration %d: patch %d bytes, truncated %v", i, len(m.Patch), m.Truncated)
			}
		} else if m.Patch != "" {
			t.Errorf("migration %d has a patch", i)
		}
	}
	if m := got.Migrations[0]; m.Path != "db/migrate/00_x.sql" || m.Status != "added" || m.Additions != 5 {
		t.Fatalf("migration = %+v", m)
	}
}

func TestBuildDiffKeepsShortPatchesWhole(t *testing.T) {
	c := comparison(1)
	c.Files = []repositories.ChangedFile{{Filename: "a.sql", Patch: "+create"}}
	m := releaseprep.BuildDiff(releaseprep.SourceDiff{}, c).Migrations[0]
	if m.Patch != "+create" || m.Truncated {
		t.Fatalf("migration = %+v", m)
	}
}

func TestBuildDiffCountsFiles(t *testing.T) {
	c := comparison(1)
	c.Files = []repositories.ChangedFile{
		{Filename: "a", Additions: 3, Deletions: 1}, {Filename: "b", Additions: 4, Deletions: 2}, {Filename: "c.sql", Additions: 1},
	}
	got := releaseprep.BuildDiff(releaseprep.SourceDiff{}, c)
	if got.FilesChanged != 3 || got.Additions != 8 || got.Deletions != 3 {
		t.Fatalf("got %d files +%d -%d", got.FilesChanged, got.Additions, got.Deletions)
	}
}

func TestBuildDiffDirection(t *testing.T) {
	for status, want := range map[string]string{
		"ahead": releaseprep.DirectionAhead, "behind": releaseprep.DirectionBehind,
		"diverged": releaseprep.DirectionDiverged, "identical": releaseprep.DirectionIdentical,
	} {
		c := &repositories.Comparison{Status: status, AheadBy: 1, BehindBy: 7}
		got := releaseprep.BuildDiff(releaseprep.SourceDiff{}, c)
		if got.Direction != want || got.AheadBy != 1 || got.BehindBy != 7 || got.Error != "" {
			t.Errorf("%s: %+v", status, got)
		}
	}
	if got := releaseprep.BuildDiff(releaseprep.SourceDiff{}, &repositories.Comparison{Status: "mystery"}); got.Direction != "" || got.Error == "" {
		t.Errorf("unknown status must be unreadable: %+v", got)
	}
}

func TestBuildDiffNewSourceAndNoComparison(t *testing.T) {
	got := releaseprep.BuildDiff(releaseprep.SourceDiff{StagingCommit: "abc"}, nil)
	// Production never ran it, so nothing compares it: unread, never "no change".
	if got.Direction != releaseprep.DirectionNew || got.Error == "" {
		t.Fatalf("new: %+v", got)
	}
	got = releaseprep.BuildDiff(releaseprep.SourceDiff{ProductionCommit: "p", StagingCommit: "abc"}, nil)
	if got.Direction != "" || got.Error == "" {
		t.Fatalf("nil comparison for a known production commit must be unreadable: %+v", got)
	}
}

func TestBuildDiffMarksGitHubCaps(t *testing.T) {
	c := comparison(250)
	c.TotalCommits = 400
	if !releaseprep.BuildDiff(releaseprep.SourceDiff{}, c).Truncated {
		t.Error("400 commits with 250 listed must be truncated")
	}
	f := comparison(1)
	for i := range 300 {
		f.Files = append(f.Files, repositories.ChangedFile{Filename: fmt.Sprint(i)})
	}
	if !releaseprep.BuildDiff(releaseprep.SourceDiff{}, f).Truncated {
		t.Error("300 files must be truncated")
	}
	if releaseprep.BuildDiff(releaseprep.SourceDiff{}, comparison(180)).Truncated {
		t.Error("180 of 180 commits is not GitHub's cap")
	}
}

// A diff that isn't complete is unreadable evidence: the gate reads Error and
// asks a person, so a migration that wasn't listed can never pass unseen.
func TestBuildDiffIncompleteDiffIsUnreadable(t *testing.T) {
	partial := comparison(10)
	partial.Truncated = true
	short := comparison(10)
	short.TotalCommits = 40
	capped := comparison(250)
	capped.TotalCommits = 400
	for name, c := range map[string]*repositories.Comparison{"partial read": partial, "total above collected": short, "commit cap": capped} {
		got := releaseprep.BuildDiff(releaseprep.SourceDiff{}, c)
		if !got.Truncated || got.Error == "" {
			t.Errorf("%s: Truncated %v, Error %q", name, got.Truncated, got.Error)
		}
	}
	if got := releaseprep.BuildDiff(releaseprep.SourceDiff{}, comparison(10)); got.Truncated || got.Error != "" {
		t.Errorf("complete diff: %+v", got)
	}
}
