package repositories

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultGitLabURL is gitlab.com.
const DefaultGitLabURL = "https://gitlab.com"

// GitLab paths nest groups, so a project path has 2 to 21 segments: up to 20
// groups and the project.
const (
	gitlabMinSegments = 2
	gitlabMaxSegments = 21
	// gitlabMaxPages caps one listing at 1000 projects.
	gitlabMaxPages = 10
	// gitlabProjectsPageBytes caps one page of a project listing. A page
	// holds 100 full project entities, which a busy GitLab can push past
	// the 1 MiB every other JSON read keeps to.
	gitlabProjectsPageBytes = 8 << 20
)

// gitlabSegment is one group or project name in a path.
var gitlabSegment = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_.-]*$`)

// gitlabPath reads a project path such as grp/sub/api, with a page path
// after it (grp/sub/api/-/tree/main) or a .git suffix. GitLab compares paths
// without regard to case, so the path keeps the case it was written in and
// whoever compares it folds.
func gitlabPath(host, path string) (Repository, error) {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if s == "-" {
			segs = segs[:i]
			break
		}
	}
	if len(segs) < gitlabMinSegments || len(segs) > gitlabMaxSegments {
		return Repository{}, ErrInvalidURL
	}
	segs[len(segs)-1] = strings.TrimSuffix(segs[len(segs)-1], ".git")
	for _, s := range segs {
		if !gitlabSegment.MatchString(s) || s == "." || s == ".." ||
			strings.HasSuffix(s, ".git") || strings.HasSuffix(s, ".atom") {
			return Repository{}, ErrInvalidURL
		}
	}
	return Repository{
		Host:  canonicalGitLabHost(host),
		Owner: strings.Join(segs[:len(segs)-1], "/"),
		Name:  segs[len(segs)-1],
	}, nil
}

var _ Reader = (*GitLab)(nil)

// GitLab reads projects through GitLab's REST API.
type GitLab struct {
	// BaseURL is the instance's address, DefaultGitLabURL for gitlab.com.
	BaseURL string
	HTTP    *http.Client

	MaxArchiveBytes int64
	MaxTotalBytes   int64
	MaxFileBytes    int64
}

// GitLabProject is what GitLab says about a project.
type GitLabProject struct {
	ID                int64  `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	Name              string `json:"name"`
	DefaultBranch     string `json:"default_branch"`
	Visibility        string `json:"visibility"`
	WebURL            string `json:"web_url"`
	Namespace         struct {
		FullPath string `json:"full_path"`
	} `json:"namespace"`
}

// NewGitLab returns a GitLab client for the instance at baseURL, with the
// default limits. A nil client is a plain http.Client.
func NewGitLab(baseURL string, client *http.Client) *GitLab {
	if baseURL == "" {
		baseURL = DefaultGitLabURL
	}
	if client == nil {
		client = &http.Client{}
	}
	return &GitLab{
		BaseURL:         strings.TrimRight(baseURL, "/"),
		HTTP:            client,
		MaxArchiveBytes: DefaultMaxArchiveBytes,
		MaxTotalBytes:   DefaultMaxTotalBytes,
		MaxFileBytes:    DefaultMaxFileBytes,
	}
}

// projectPath is the API path of a project. GitLab takes its full path as
// the ID, with the slashes escaped.
func projectPath(repo Repository) string {
	return "/api/v4/projects/" + url.PathEscape(repo.FullName())
}

// Resolve returns the commit ref names, or the default branch's head when
// ref is empty. token may be empty for a public project.
func (g *GitLab) Resolve(ctx context.Context, repo Repository, ref, token string) (string, error) {
	if ref == "" {
		p, err := g.Project(ctx, repo, token)
		if err != nil {
			return "", err
		}
		ref = p.DefaultBranch
	}
	if !ValidRef(ref) {
		return "", fmt.Errorf("%w: %q isn't a valid ref", ErrNotFound, ref)
	}
	if shaPattern.MatchString(ref) {
		return ref, nil
	}
	var commit struct {
		ID string `json:"id"`
	}
	if err := g.getJSON(ctx, projectPath(repo)+"/repository/commits/"+url.PathEscape(ref), token, &commit); err != nil {
		return "", err
	}
	if !shaPattern.MatchString(commit.ID) {
		return "", fmt.Errorf("GitLab answered %q for a commit SHA", commit.ID)
	}
	return commit.ID, nil
}

func (g *GitLab) limits() Limits {
	return Limits{Archive: g.MaxArchiveBytes, Total: g.MaxTotalBytes, File: g.MaxFileBytes}
}

// archivePath is the API path of a project's tarball at commit sha. LFS
// objects stay out of it: a checkout is read for its manifests, not blobs.
func archivePath(repo Repository, sha string) string {
	return projectPath(repo) + "/repository/archive.tar.gz?sha=" + sha + "&include_lfs_blobs=false"
}

// Fetch extracts the project at commit sha into dest, which must exist and
// be empty.
func (g *GitLab) Fetch(ctx context.Context, repo Repository, sha, token, dest string) (Stats, error) {
	if !shaPattern.MatchString(sha) {
		return Stats{}, fmt.Errorf("fetch needs a full commit SHA, got %q", sha)
	}
	resp, err := g.get(ctx, archivePath(repo, sha), token, "", downloadTimeout)
	if err != nil {
		return Stats{}, err
	}
	defer resp.Body.Close() // read-only body
	zr, err := gzip.NewReader(g.limits().Cap(resp.Body))
	if err != nil {
		return Stats{}, fmt.Errorf("read tarball: %w", err)
	}
	defer zr.Close() // read-only stream
	return extractArchive(ctx, tar.NewReader(zr), dest, g.limits())
}

// Download writes the project's tarball at commit sha to w, as GitLab serves
// it (gzip, with one top-level directory), and returns its size. It reads
// nothing inside: a deployment hands the bytes to a builder.
func (g *GitLab) Download(ctx context.Context, repo Repository, sha, token string, w io.Writer) (int64, error) {
	if !shaPattern.MatchString(sha) {
		return 0, fmt.Errorf("download needs a full commit SHA, got %q", sha)
	}
	resp, err := g.get(ctx, archivePath(repo, sha), token, "", downloadTimeout)
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

// Project reads one project.
func (g *GitLab) Project(ctx context.Context, repo Repository, token string) (GitLabProject, error) {
	var p GitLabProject
	err := g.getJSON(ctx, projectPath(repo), token, &p)
	return p, err
}

// Projects lists the projects token's owner is a member of with at least
// reporter access, skipping archived ones, up to gitlabMaxPages pages.
func (g *GitLab) Projects(ctx context.Context, token string) ([]GitLabProject, error) {
	var all []GitLabProject
	page := 1
	for range gitlabMaxPages {
		p := "/api/v4/projects?membership=true&min_access_level=20&archived=false&order_by=path&sort=asc&per_page=100&page=" + strconv.Itoa(page)
		resp, err := g.get(ctx, p, token, "application/json", requestTimeout)
		if err != nil {
			return nil, err
		}
		var batch []GitLabProject
		err = json.NewDecoder(io.LimitReader(resp.Body, gitlabProjectsPageBytes)).Decode(&batch)
		next := resp.Header.Get("X-Next-Page")
		resp.Body.Close() // read-only body
		if err != nil {
			return nil, fmt.Errorf("decode GitLab response: %w", err)
		}
		all = append(all, batch...)
		// No X-Next-Page, one that isn't a number, or one that doesn't move
		// forward ends the listing, so a host can't hold the loop on a page.
		n, err := strconv.Atoi(next)
		if err != nil || n <= page {
			break
		}
		page = n
	}
	return all, nil
}

func (g *GitLab) getJSON(ctx context.Context, p, token string, out any) error {
	resp, err := g.get(ctx, p, token, "application/json", requestTimeout)
	if err != nil {
		return err
	}
	defer resp.Body.Close() // read-only body
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBytes)).Decode(out); err != nil {
		return fmt.Errorf("decode GitLab response: %w", err)
	}
	return nil
}

// get calls the API and maps GitLab's error statuses. A non-nil response is
// a 200 whose body the caller closes. The token goes in a header only when
// there is one, and never into an error. accept may be "" for a download.
func (g *GitLab) get(ctx context.Context, p, token, accept string, timeout time.Duration) (*http.Response, error) {
	rctx, cancel := context.WithTimeout(ctx, timeout)
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, g.BaseURL+p, http.NoBody)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("build GitLab request: %w", err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("User-Agent", "loomplane")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := g.HTTP.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("call GitLab: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	case http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden:
		err = ErrNotFound
	case http.StatusTooManyRequests:
		err = ErrRateLimited
		if after := resp.Header.Get("Retry-After"); after != "" {
			err = fmt.Errorf("%w (GitLab asks to retry after %s)", ErrRateLimited, trimHeader(after))
		}
	default:
		err = fmt.Errorf("GitLab answered %s for %s", resp.Status, strings.SplitN(p, "?", 2)[0])
	}
	resp.Body.Close() // error path; the status is what matters
	cancel()
	return nil, err
}

// trimHeader shortens a header value before it goes into an error.
func trimHeader(s string) string {
	if len(s) > 40 {
		return s[:40]
	}
	return s
}
