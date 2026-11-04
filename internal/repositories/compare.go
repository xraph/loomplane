package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// maxCompareBytes bounds a compare response. GitHub lists at most 250 commits
// and 300 files, each file with its patch, so a larger body isn't a diff
// Loomplane should read.
const maxCompareBytes = 16 << 20

// GitHub lists at most 250 commits and 300 files of a comparison, 100 a page.
const (
	maxCompareCommits = 250
	maxCompareFiles   = 300
	maxComparePages   = 5
)

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
	// Truncated is true when the lists don't hold everything that changed:
	// GitHub's caps were hit, it reported more commits than were collected, or
	// a later page failed. A Comparison with Truncated set is never complete.
	Truncated bool
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

// Compare asks GitHub what changed from base to head, following the
// comparison's pages up to GitHub's caps. Both refs must be full commit SHAs.
// token may be empty for a public repository.
//
// Only GitHub has a compare yet. A repository on any other host gets
// ErrUnsupportedHost, never an empty Comparison that would read as no change.
// If the first page can't be read that is an error. If a later page fails, the
// Comparison so far comes back with Truncated set and no error, so a partial
// read is never taken for a complete one.
func (g *GitHub) Compare(ctx context.Context, repo Repository, base, head, token string) (*Comparison, error) {
	if !repo.onGitHub() {
		return nil, fmt.Errorf("%w: comparing commits is only available on GitHub", ErrUnsupportedHost)
	}
	if !shaPattern.MatchString(base) || !shaPattern.MatchString(head) {
		return nil, ErrNotACommit
	}
	path := "/repos/" + repo.FullName() + "/compare/" + base + "..." + head
	budget := int64(maxCompareBytes)
	out := &Comparison{}
	for page := 1; page <= maxComparePages; page++ {
		raw, more, err := g.comparePage(ctx, path, page, token, &budget)
		if err != nil {
			if page == 1 || errors.Is(err, errCompareTooLarge) {
				return nil, err
			}
			out.Truncated = true
			return out, nil
		}
		if page == 1 {
			out.Status, out.AheadBy, out.BehindBy, out.TotalCommits = raw.Status, raw.AheadBy, raw.BehindBy, raw.TotalCommits
		}
		for _, c := range raw.Commits {
			out.Commits = append(out.Commits, ComparedCommit{SHA: c.SHA, Message: c.Commit.Message, Author: c.Commit.Author.Name, Date: c.Commit.Author.Date})
		}
		for _, f := range raw.Files {
			out.Files = append(out.Files, ChangedFile{Filename: f.Filename, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch})
		}
		if !more || (len(out.Commits) >= maxCompareCommits && len(out.Files) >= maxCompareFiles) {
			break
		}
		if page == maxComparePages {
			out.Truncated = true
		}
	}
	if len(out.Commits) > maxCompareCommits {
		out.Commits, out.Truncated = out.Commits[:maxCompareCommits], true
	}
	if len(out.Files) > maxCompareFiles {
		out.Files, out.Truncated = out.Files[:maxCompareFiles], true
	}
	if out.TotalCommits > len(out.Commits) || len(out.Files) >= maxCompareFiles {
		out.Truncated = true
	}
	return out, nil
}

var errCompareTooLarge = errors.New("GitHub's comparison is larger than 16 MiB")

type comparePage struct {
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

// comparePage reads one page, spending from budget the bytes all pages share.
// more is true when GitHub's Link header offers a next page.
func (g *GitHub) comparePage(ctx context.Context, path string, page int, token string, budget *int64) (*comparePage, bool, error) {
	resp, err := g.get(ctx, path+"?per_page=100&page="+strconv.Itoa(page), token, "application/vnd.github+json", requestTimeout)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close() // read-only body
	body, err := io.ReadAll(io.LimitReader(resp.Body, *budget+1))
	if err != nil {
		return nil, false, fmt.Errorf("read GitHub comparison: %w", err)
	}
	if int64(len(body)) > *budget {
		return nil, false, errCompareTooLarge
	}
	*budget -= int64(len(body))
	var raw comparePage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, false, fmt.Errorf("decode GitHub comparison: %w", err)
	}
	return &raw, hasNextLink(resp.Header.Get("Link")), nil
}

// hasNextLink reports whether a Link header offers rel="next". Only the fact
// is used: the next URL is never followed, so a token can't be sent elsewhere.
func hasNextLink(h string) bool {
	for part := range strings.SplitSeq(h, ",") {
		if strings.Contains(part, `rel="next"`) {
			return true
		}
	}
	return false
}
