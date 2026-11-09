// Package repositories reads source code from Git hosts. GitHub is the
// only host so far.
//
// A checkout is a tarball of one exact commit, extracted with hard limits:
// only regular files, no symlinks, nothing outside the target directory,
// no dependency or build output directories, nothing too big. Nothing in
// it is ever run.
package repositories

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Errors a fetch can fail with. Each is permanent: retrying won't help.
var (
	ErrNotFound = errors.New("the repository or ref doesn't exist, or it's private and Loomplane can't read it")
	ErrTooLarge = errors.New("the repository is larger than Loomplane analyzes")
)

// ErrRateLimited means the repository host refused for now. It's worth
// retrying later.
var ErrRateLimited = errors.New("the repository host's rate limit was reached; try again later")

// DefaultAPIURL is github.com's REST API.
const DefaultAPIURL = "https://api.github.com"

const (
	requestTimeout  = 30 * time.Second
	downloadTimeout = 15 * time.Minute
	maxJSONBytes    = 1 << 20
)

var (
	refPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,250}$`)
	shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// ValidRef reports whether ref is a plausible branch, tag or commit name.
// Anything else is refused before it reaches a URL.
func ValidRef(ref string) bool {
	return refPattern.MatchString(ref) && !strings.Contains(ref, "..") && !strings.HasPrefix(ref, "/")
}

// IsCommitSHA reports whether ref is a full commit SHA, which Resolve
// returns as it is without asking GitHub.
func IsCommitSHA(ref string) bool { return shaPattern.MatchString(ref) }

// GitHub reads repositories through GitHub's REST API.
type GitHub struct {
	// APIURL is the REST API's base URL, DefaultAPIURL for github.com.
	APIURL string
	// WebURL is where GitHub serves its pages, which commit and release
	// links point at. Empty is DefaultGitHubWebURL.
	WebURL string
	HTTP   *http.Client

	MaxArchiveBytes int64
	MaxTotalBytes   int64
	MaxFileBytes    int64
}

// NewGitHub returns a GitHub client for apiURL, with the default limits.
func NewGitHub(apiURL string) *GitHub {
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	return &GitHub{
		APIURL:          strings.TrimRight(apiURL, "/"),
		HTTP:            &http.Client{},
		MaxArchiveBytes: DefaultMaxArchiveBytes,
		MaxTotalBytes:   DefaultMaxTotalBytes,
		MaxFileBytes:    DefaultMaxFileBytes,
	}
}

func (g *GitHub) limits() Limits {
	return Limits{Archive: g.MaxArchiveBytes, Total: g.MaxTotalBytes, File: g.MaxFileBytes}
}

// Resolve returns the commit ref names, or the default branch's head when
// ref is empty. token may be empty for a public repository.
func (g *GitHub) Resolve(ctx context.Context, repo Repository, ref, token string) (string, error) {
	if ref == "" {
		branch, err := g.defaultBranch(ctx, repo, token)
		if err != nil {
			return "", err
		}
		ref = branch
	}
	if !ValidRef(ref) {
		return "", fmt.Errorf("%w: %q isn't a valid ref", ErrNotFound, ref)
	}
	if shaPattern.MatchString(ref) {
		return ref, nil
	}
	resp, err := g.get(ctx, "/repos/"+repo.FullName()+"/commits/"+escapeRef(ref), token, "application/vnd.github.sha", requestTimeout)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() // read-only body
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", fmt.Errorf("read commit: %w", err)
	}
	sha := strings.TrimSpace(string(body))
	if !shaPattern.MatchString(sha) {
		return "", fmt.Errorf("GitHub answered %q for a commit SHA", sha)
	}
	return sha, nil
}

// escapeRef escapes each segment of a ref for a URL path, keeping the
// slashes of names like release/1.2.
func escapeRef(ref string) string {
	segs := strings.Split(ref, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// Fetch extracts the repository at commit sha into dest, which must exist
// and be empty.
func (g *GitHub) Fetch(ctx context.Context, repo Repository, sha, token, dest string) (Stats, error) {
	if !shaPattern.MatchString(sha) {
		return Stats{}, fmt.Errorf("fetch needs a full commit SHA, got %q", sha)
	}
	resp, err := g.get(ctx, "/repos/"+repo.FullName()+"/tarball/"+sha, token, "", downloadTimeout)
	if err != nil {
		return Stats{}, err
	}
	defer resp.Body.Close() // read-only body
	limited := g.limits().Cap(resp.Body)
	zr, err := gzip.NewReader(limited)
	if err != nil {
		return Stats{}, fmt.Errorf("read tarball: %w", err)
	}
	defer zr.Close() // read-only stream
	return extractArchive(ctx, tar.NewReader(zr), dest, g.limits())
}

// Download writes the repository's tarball at commit sha to w, as GitHub
// serves it (gzip, with one top-level directory), and returns its size.
// It reads nothing inside: a deployment hands the bytes to a builder.
func (g *GitHub) Download(ctx context.Context, repo Repository, sha, token string, w io.Writer) (int64, error) {
	if !shaPattern.MatchString(sha) {
		return 0, fmt.Errorf("download needs a full commit SHA, got %q", sha)
	}
	resp, err := g.get(ctx, "/repos/"+repo.FullName()+"/tarball/"+sha, token, "", downloadTimeout)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close() // read-only body
	n, err := io.Copy(w, g.limits().Cap(resp.Body))
	if err != nil {
		return n, fmt.Errorf("download tarball: %w", err)
	}
	return n, nil
}

func (g *GitHub) getJSON(ctx context.Context, p, token string, out any) error {
	resp, err := g.get(ctx, p, token, "application/vnd.github+json", requestTimeout)
	if err != nil {
		return err
	}
	defer resp.Body.Close() // read-only body
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBytes)).Decode(out); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}

// get calls the API and maps GitHub's error statuses. A non-nil response
// is a 200 whose body the caller closes. timeout covers reading the body
// too, which for a tarball is the download.
func (g *GitHub) get(ctx context.Context, p, token, accept string, timeout time.Duration) (*http.Response, error) {
	rctx, cancel := context.WithTimeout(ctx, timeout)
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, g.APIURL+p, http.NoBody)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("build GitHub request: %w", err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "loomplane")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := g.HTTP.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("call GitHub: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	case resp.StatusCode == http.StatusNotFound:
		err = ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0":
		err = ErrRateLimited
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		err = ErrNotFound
	default:
		err = fmt.Errorf("GitHub answered %s for %s", resp.Status, p)
	}
	resp.Body.Close() // error path; the status is what matters
	cancel()
	return nil, err
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
