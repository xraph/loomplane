// Package releaseprep prepares a release for promotion: what would change in
// production, and whether the agent may promote it alone.
package releaseprep

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xraph/loomplane/internal/repositories"
)

// Directions of staging's commit relative to production's.
const (
	DirectionAhead     = "ahead"
	DirectionBehind    = "behind"
	DirectionDiverged  = "diverged"
	DirectionIdentical = "identical"
	// DirectionNew means production never ran this source.
	DirectionNew = "new"
)

// Bounds on what a SourceDiff carries, so a large release can't fill a report.
const (
	MaxCommits           = 100
	MaxPatchBytes        = 8 << 10
	MaxPatchedMigrations = 10

	maxTitleRunes = 200
	// githubCommitCap and githubFileCap are the most a GitHub comparison lists.
	githubCommitCap = 250
	githubFileCap   = 300
)

// DiffCommit is one commit staging has and production doesn't.
type DiffCommit struct {
	SHA string `json:"sha"`
	// Title is the first line of the message, at most 200 characters. Untrusted.
	Title  string    `json:"title"`
	Author string    `json:"author"`
	At     time.Time `json:"at"`
}

// MigrationFile is a changed database migration or schema file.
type MigrationFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	// Patch is the first MaxPatchBytes of the change. Untrusted.
	Patch string `json:"patch"`
	// Truncated is true when Patch was cut short or left out.
	Truncated bool `json:"truncated"`
}

// SourceDiff is what changes in one source between production and staging.
type SourceDiff struct {
	SourceID   string `json:"source_id"`
	Alias      string `json:"alias"`
	Repository string `json:"repository"`
	// ProductionCommit is empty when production never ran this source.
	ProductionCommit string `json:"production_commit"`
	StagingCommit    string `json:"staging_commit"`
	Direction        string `json:"direction"`
	AheadBy          int    `json:"ahead_by"`
	BehindBy         int    `json:"behind_by"`
	TotalCommits     int    `json:"total_commits"`
	// Commits are newest first, at most MaxCommits.
	Commits      []DiffCommit    `json:"commits"`
	FilesChanged int             `json:"files_changed"`
	Additions    int             `json:"additions"`
	Deletions    int             `json:"deletions"`
	Migrations   []MigrationFile `json:"migrations"`
	// Truncated is true when GitHub's 250-commit or 300-file cap was hit.
	Truncated bool `json:"truncated"`
	// Error says why the diff couldn't be read. A diff with an Error counts as
	// unreadable evidence, never as no change.
	Error string `json:"error,omitempty"`
}

// BuildDiff fills d from a comparison of production's commit (base) to
// staging's (head). A nil comparison means there was nothing to compare: a
// source production never ran is DirectionNew, and anything else is unreadable.
func BuildDiff(d SourceDiff, c *repositories.Comparison) SourceDiff {
	if c == nil {
		if d.ProductionCommit == "" {
			d.Direction = DirectionNew
		} else if d.Error == "" {
			d.Error = "the comparison couldn't be read"
		}
		return d
	}
	switch c.Status {
	case DirectionAhead, DirectionBehind, DirectionDiverged, DirectionIdentical:
		d.Direction = c.Status
	default:
		d.Direction = ""
		d.Error = "GitHub answered a comparison status Loomplane doesn't know"
	}
	d.AheadBy, d.BehindBy, d.TotalCommits = c.AheadBy, c.BehindBy, c.TotalCommits
	d.Truncated = c.TotalCommits > len(c.Commits) && len(c.Commits) >= githubCommitCap || len(c.Files) >= githubFileCap

	d.Commits = nil
	for i := len(c.Commits) - 1; i >= 0 && len(d.Commits) < MaxCommits; i-- {
		cc := c.Commits[i]
		d.Commits = append(d.Commits, DiffCommit{SHA: cc.SHA, Title: title(cc.Message), Author: cc.Author, At: cc.Date})
	}

	d.FilesChanged, d.Additions, d.Deletions, d.Migrations = len(c.Files), 0, 0, nil
	for _, f := range c.Files {
		d.Additions += f.Additions
		d.Deletions += f.Deletions
		if !IsMigration(f.Filename) {
			continue
		}
		m := MigrationFile{Path: f.Filename, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions}
		switch {
		case len(d.Migrations) >= MaxPatchedMigrations:
			m.Truncated = f.Patch != ""
		default:
			m.Patch, m.Truncated = cut(f.Patch, MaxPatchBytes)
		}
		d.Migrations = append(d.Migrations, m)
	}
	return d
}

// title is the first line of a commit message, at most 200 characters.
func title(msg string) string {
	line, _, _ := strings.Cut(msg, "\n")
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) > maxTitleRunes {
		line = string([]rune(line)[:maxTitleRunes])
	}
	return line
}

// cut returns at most n bytes of s without splitting a character.
func cut(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// IsMigration reports whether a changed path is a database migration or
// schema file.
func IsMigration(path string) bool {
	segs := strings.Split(strings.ToLower(path), "/")
	base := segs[len(segs)-1]
	if strings.HasSuffix(base, ".sql") || base == "schema.rb" || base == "schema.prisma" {
		return true
	}
	dirs := segs[:len(segs)-1]
	seenDB := false
	for i, s := range dirs {
		switch s {
		case "migrations", "migrate", "migration", "flyway", "liquibase":
			return true
		case "versions":
			if i > 0 && dirs[i-1] == "alembic" {
				return true
			}
		case "db":
			seenDB = true
		case "changelog":
			if seenDB {
				return true
			}
		}
	}
	return false
}
