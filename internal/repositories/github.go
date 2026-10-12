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
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/xraph/loomplane/pkg/discovery"
)

// Errors a fetch can fail with. Each is permanent: retrying won't help.
var (
	ErrUnsupportedHost = errors.New("only GitHub repositories can be analyzed so far")
	ErrInvalidURL      = errors.New("that isn't a GitHub repository URL")
	ErrNotFound        = errors.New("the repository or ref doesn't exist, or it's private and Loomplane can't read it")
	ErrTooLarge        = errors.New("the repository is larger than Loomplane analyzes")
)

// ErrRateLimited means GitHub refused for now. It's worth retrying later.
var ErrRateLimited = errors.New("GitHub's rate limit was reached; try again later")

// DefaultAPIURL is github.com's REST API.
const DefaultAPIURL = "https://api.github.com"

// Limits on one checkout.
const (
	DefaultMaxArchiveBytes int64 = 1 << 30 // compressed tarball
	DefaultMaxTotalBytes   int64 = 2 << 30 // extracted files kept
	DefaultMaxFileBytes    int64 = 4 << 20 // one file; bigger ones are skipped
	requestTimeout               = 30 * time.Second
	downloadTimeout              = 15 * time.Minute
	maxJSONBytes                 = 1 << 20
)

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	refPattern   = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,250}$`)
	shaPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Repository is a GitHub repository, by owner and name.
type Repository struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

// FullName is "owner/name".
func (r Repository) FullName() string { return r.Owner + "/" + r.Name }

// URL is the repository's page on github.com.
func (r Repository) URL() string { return "https://github.com/" + r.FullName() }

// ParseGitHubURL reads a repository from the URLs people paste:
// https://github.com/o/r, https://github.com/o/r.git, github.com/o/r and
// git@github.com:o/r.git.
func ParseGitHubURL(raw string) (Repository, error) {
	s := strings.TrimSpace(raw)
	if rest, ok := strings.CutPrefix(s, "git@github.com:"); ok {
		s = "https://github.com/" + rest
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return Repository{}, ErrInvalidURL
	}
	if host := strings.ToLower(u.Hostname()); host != "github.com" && host != "www.github.com" {
		return Repository{}, ErrUnsupportedHost
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return Repository{}, ErrInvalidURL
	}
	r := Repository{Owner: parts[0], Name: strings.TrimSuffix(parts[1], ".git")}
	if !ownerPattern.MatchString(r.Owner) || !repoPattern.MatchString(r.Name) || r.Name == "." || r.Name == ".." {
		return Repository{}, ErrInvalidURL
	}
	return r, nil
}

// ValidRef reports whether ref is a plausible branch, tag or commit name.
// Anything else is refused before it reaches a URL.
func ValidRef(ref string) bool {
	return refPattern.MatchString(ref) && !strings.Contains(ref, "..") && !strings.HasPrefix(ref, "/")
}

// GitHub reads repositories through GitHub's REST API.
type GitHub struct {
	// APIURL is the REST API's base URL, DefaultAPIURL for github.com.
	APIURL string
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

// Resolve returns the commit ref names, or the default branch's head when
// ref is empty. token may be empty for a public repository.
func (g *GitHub) Resolve(ctx context.Context, repo Repository, ref, token string) (string, error) {
	if ref == "" {
		var info struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := g.getJSON(ctx, "/repos/"+repo.FullName(), token, &info); err != nil {
			return "", err
		}
		ref = info.DefaultBranch
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

// Stats describe a checkout.
type Stats struct {
	Files   int   `json:"files"`
	Bytes   int64 `json:"bytes"`
	Skipped int   `json:"skipped"`
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
	limited := &limitedReader{r: resp.Body, left: g.MaxArchiveBytes}
	zr, err := gzip.NewReader(limited)
	if err != nil {
		return Stats{}, fmt.Errorf("read tarball: %w", err)
	}
	defer zr.Close() // read-only stream
	return g.extract(ctx, tar.NewReader(zr), dest)
}

// limitedReader fails, instead of returning EOF, past its limit, so an
// oversized archive can't pass for a short one.
type limitedReader struct {
	r    io.Reader
	left int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, ErrTooLarge
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}

func (g *GitHub) extract(ctx context.Context, tr *tar.Reader, dest string) (Stats, error) {
	var st Stats
	root, err := filepath.Abs(dest)
	if err != nil {
		return st, fmt.Errorf("checkout directory: %w", err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return st, nil
		}
		if err != nil {
			if errors.Is(err, ErrTooLarge) {
				return st, err
			}
			return st, fmt.Errorf("read tarball: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // directories are made as needed; links are never followed
		}
		name, ok := entryPath(hdr.Name)
		if !ok {
			st.Skipped++
			continue
		}
		if hdr.Size > g.MaxFileBytes {
			st.Skipped++
			continue
		}
		if st.Bytes+hdr.Size > g.MaxTotalBytes {
			return st, ErrTooLarge
		}
		if st.Files >= maxFiles {
			return st, ErrTooLarge
		}
		if err := writeFile(root, name, tr, hdr.Size); err != nil {
			return st, err
		}
		st.Files++
		st.Bytes += hdr.Size
	}
}

// maxFiles caps the files one checkout writes.
const maxFiles = 200_000

// entryPath turns a tarball entry name into a clean path under the
// checkout, dropping GitHub's top-level "owner-repo-sha/" directory. It
// refuses absolute paths, "..", and anything under a directory a detector
// never reads.
func entryPath(name string) (string, bool) {
	if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return "", false
	}
	_, rest, ok := strings.Cut(name, "/")
	if !ok || rest == "" {
		return "", false
	}
	clean := path.Clean(rest)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	segs := strings.Split(clean, "/")
	for _, seg := range segs[:len(segs)-1] {
		if seg == ".." || discovery.SkipDir(seg) {
			return "", false
		}
	}
	return clean, true
}

func writeFile(root, name string, r io.Reader, size int64) error {
	target := filepath.Join(root, filepath.FromSlash(name))
	if !strings.HasPrefix(target, root+string(filepath.Separator)) {
		return fmt.Errorf("tarball entry %q escapes the checkout", name)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create directory for %s: %w", name, err)
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	_, err = io.CopyN(f, r, size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			return err
		}
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
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
