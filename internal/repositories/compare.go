package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// maxCompareBytes bounds a compare response. GitHub lists at most 250 commits
// and 300 files, each file with its patch, so a larger body isn't a diff
// Loomplane should read.
const maxCompareBytes = 16 << 20

// ErrNotACommit means compare was given something other than a full commit
// SHA. A branch name would move between the request and the answer.
var ErrNotACommit = errors.New("compare needs full 40-character commit SHAs")

// Comparison is what changed between two commits.
type Comparison struct {
	// Status is GitHub's word: "ahead", "behind", "diverged" or "identical".
	Status       string
	AheadBy      int
	BehindBy     int
	TotalCommits int
	// Commits are oldest first, as GitHub returns them, at most 250.
	Commits []ComparedCommit
	// Files are at most 300.
	Files []ChangedFile
}

// ComparedCommit is one commit in a comparison.
type ComparedCommit struct {
	SHA string
	// Message is the full message. It is untrusted text.
	Message string
	// Author is the commit author's name.
	Author string
	Date   time.Time
}

// ChangedFile is one file in a comparison.
type ChangedFile struct {
	Filename string
	// Status is added, removed, modified, renamed and so on.
	Status    string
	Additions int
	Deletions int
	// Patch may be empty for binary or very large files. It is untrusted text.
	Patch string
}

// Compare asks GitHub what changed from base to head. Both must be full
// commit SHAs. token may be empty for a public repository.
//
// Only GitHub has a compare yet. A repository on any other host gets
// ErrUnsupportedHost, never an empty Comparison that would read as no change.
func (g *GitHub) Compare(ctx context.Context, repo Repository, base, head, token string) (*Comparison, error) {
	if !repo.onGitHub() {
		return nil, fmt.Errorf("%w: comparing commits is only available on GitHub", ErrUnsupportedHost)
	}
	if !shaPattern.MatchString(base) || !shaPattern.MatchString(head) {
		return nil, ErrNotACommit
	}
	resp, err := g.get(ctx, "/repos/"+repo.FullName()+"/compare/"+base+"..."+head+"?per_page=100", token, "application/vnd.github+json", requestTimeout)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() // read-only body
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCompareBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read GitHub comparison: %w", err)
	}
	if len(body) > maxCompareBytes {
		return nil, fmt.Errorf("GitHub's comparison is larger than %d MiB", maxCompareBytes>>20)
	}
	var raw struct {
		Status       string `json:"status"`
		AheadBy      int    `json:"ahead_by"`
		BehindBy     int    `json:"behind_by"`
		TotalCommits int    `json:"total_commits"`
		Commits      []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
				Author  struct {
					Name string    `json:"name"`
					Date time.Time `json:"date"`
				} `json:"author"`
			} `json:"commit"`
		} `json:"commits"`
		Files []struct {
			Filename  string `json:"filename"`
			Status    string `json:"status"`
			Additions int    `json:"additions"`
			Deletions int    `json:"deletions"`
			Patch     string `json:"patch"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode GitHub comparison: %w", err)
	}
	out := &Comparison{Status: raw.Status, AheadBy: raw.AheadBy, BehindBy: raw.BehindBy, TotalCommits: raw.TotalCommits}
	for _, c := range raw.Commits {
		out.Commits = append(out.Commits, ComparedCommit{SHA: c.SHA, Message: c.Commit.Message, Author: c.Commit.Author.Name, Date: c.Commit.Author.Date})
	}
	for _, f := range raw.Files {
		out.Files = append(out.Files, ChangedFile{Filename: f.Filename, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch})
	}
	return out, nil
}
