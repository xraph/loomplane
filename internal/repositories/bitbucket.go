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
	"strings"
	"time"
)

// Bitbucket Cloud's addresses.
const (
	DefaultBitbucketAPIURL = "https://api.bitbucket.org/2.0"
	DefaultBitbucketWebURL = "https://bitbucket.org"
)

// errMissing is a 404 in particular, which tells Resolve to try the next
// kind of ref. Every other refusal is plain ErrNotFound.
var errMissing = fmt.Errorf("%w", ErrNotFound)

// Bitbucket reads repositories from Bitbucket Cloud. Its API answers
// questions about refs; the tarball itself comes from the web address.
type Bitbucket struct {
	// APIURL is the REST API's base URL, DefaultBitbucketAPIURL for
	// bitbucket.org.
	APIURL string
	// WebURL is where archives are served, DefaultBitbucketWebURL for
	// bitbucket.org.
	WebURL string
	HTTP   *http.Client

	Limits Limits
}

// NewBitbucket returns a Bitbucket client, with the default limits. An
// empty URL means bitbucket.org's.
func NewBitbucket(apiURL, webURL string) *Bitbucket {
	if apiURL == "" {
		apiURL = DefaultBitbucketAPIURL
	}
	if webURL == "" {
		webURL = DefaultBitbucketWebURL
	}
	return &Bitbucket{
		APIURL: strings.TrimRight(apiURL, "/"),
		WebURL: strings.TrimRight(webURL, "/"),
		HTTP:   &http.Client{},
		Limits: Limits{Archive: DefaultMaxArchiveBytes, Total: DefaultMaxTotalBytes, File: DefaultMaxFileBytes},
	}
}

// DefaultBranch returns the name of the repository's main branch. token may
// be empty for a public repository.
func (b *Bitbucket) DefaultBranch(ctx context.Context, repo Repository, token string) (string, error) {
	var info struct {
		MainBranch struct {
			Name string `json:"name"`
		} `json:"mainbranch"`
	}
	if err := b.getJSON(ctx, b.repoAPI(repo), token, &info); err != nil {
		return "", normalize(err)
	}
	if info.MainBranch.Name == "" {
		return "", fmt.Errorf("%w: %s has no default branch", ErrNotFound, repo.FullName())
	}
	return info.MainBranch.Name, nil
}

// Resolve returns the commit ref names, or the default branch's head when
// ref is empty. A branch wins over a tag of the same name, and either wins
// over a commit that starts with the same letters. token may be empty for a
// public repository.
func (b *Bitbucket) Resolve(ctx context.Context, repo Repository, ref, token string) (string, error) {
	if ref == "" {
		name, err := b.DefaultBranch(ctx, repo, token)
		if err != nil {
			return "", err
		}
		ref = name
	}
	if !ValidRef(ref) {
		return "", fmt.Errorf("%w: %q isn't a valid ref", ErrNotFound, ref)
	}
	if shaPattern.MatchString(ref) {
		return ref, nil
	}
	esc := escapeRef(ref)
	for _, p := range []string{"/refs/branches/" + esc, "/refs/tags/" + esc, "/commit/" + esc} {
		sha, err := b.hashAt(ctx, b.repoAPI(repo)+p, token)
		if err == nil {
			return sha, nil
		}
		if err != errMissing { //nolint:errorlint // errMissing is compared by identity on purpose
			return "", normalize(err)
		}
	}
	return "", ErrNotFound
}

// hashAt reads the commit hash a branch, tag or commit answers with.
func (b *Bitbucket) hashAt(ctx context.Context, p, token string) (string, error) {
	var out struct {
		Hash   string `json:"hash"`
		Target struct {
			Hash string `json:"hash"`
		} `json:"target"`
	}
	if err := b.getJSON(ctx, p, token, &out); err != nil {
		return "", err
	}
	sha := out.Target.Hash
	if sha == "" {
		sha = out.Hash
	}
	if !shaPattern.MatchString(sha) {
		return "", fmt.Errorf("Bitbucket answered %q for a commit hash", sha)
	}
	return sha, nil
}

// Fetch extracts the repository at commit sha into dest, which must exist
// and be empty.
func (b *Bitbucket) Fetch(ctx context.Context, repo Repository, sha, token, dest string) (Stats, error) {
	if !shaPattern.MatchString(sha) {
		return Stats{}, fmt.Errorf("fetch needs a full commit SHA, got %q", sha)
	}
	resp, err := b.get(ctx, b.archiveURL(repo, sha), token, "", downloadTimeout)
	if err != nil {
		return Stats{}, normalize(err)
	}
	defer resp.Body.Close() // read-only body
	zr, err := gzip.NewReader(b.Limits.Cap(resp.Body))
	if err != nil {
		return Stats{}, fmt.Errorf("read tarball: %w", err)
	}
	defer zr.Close() // read-only stream
	return extractArchive(ctx, tar.NewReader(zr), dest, b.Limits)
}

// Download writes the repository's tarball at commit sha to w, as Bitbucket
// serves it (gzip, with one top-level directory), and returns its size. It
// reads nothing inside: a deployment hands the bytes to a builder.
func (b *Bitbucket) Download(ctx context.Context, repo Repository, sha, token string, w io.Writer) (int64, error) {
	if !shaPattern.MatchString(sha) {
		return 0, fmt.Errorf("download needs a full commit SHA, got %q", sha)
	}
	resp, err := b.get(ctx, b.archiveURL(repo, sha), token, "", downloadTimeout)
	if err != nil {
		return 0, normalize(err)
	}
	defer resp.Body.Close() // read-only body
	n, err := io.Copy(w, b.Limits.Cap(resp.Body))
	if err != nil {
		return n, fmt.Errorf("download tarball: %w", err)
	}
	return n, nil
}

// repoAPI is the API path of a repository, without a trailing slash.
func (b *Bitbucket) repoAPI(repo Repository) string {
	return b.APIURL + "/repositories/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name)
}

func (b *Bitbucket) archiveURL(repo Repository, sha string) string {
	return b.WebURL + "/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name) + "/get/" + sha + ".tar.gz"
}

// normalize turns the 404 marker back into the error callers see.
func normalize(err error) error {
	if err == errMissing { //nolint:errorlint // identity comparison on purpose
		return ErrNotFound
	}
	return err
}

func (b *Bitbucket) getJSON(ctx context.Context, u, token string, out any) error {
	resp, err := b.get(ctx, u, token, "application/json", requestTimeout)
	if err != nil {
		return err
	}
	defer resp.Body.Close() // read-only body
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBytes)).Decode(out); err != nil {
		return fmt.Errorf("decode Bitbucket response: %w", err)
	}
	return nil
}

// get calls Bitbucket and maps its error statuses. A non-nil response is a
// 200 whose body the caller closes. timeout covers reading the body too,
// which for a tarball is the download. The error never carries the token.
func (b *Bitbucket) get(ctx context.Context, u, token, accept string, timeout time.Duration) (*http.Response, error) {
	rctx, cancel := context.WithTimeout(ctx, timeout)
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		cancel()
		return nil, errors.New("build Bitbucket request failed")
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("User-Agent", "loomplane")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := b.HTTP.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("call Bitbucket: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	case http.StatusNotFound:
		err = errMissing
	case http.StatusTooManyRequests:
		err = ErrRateLimited
	case http.StatusUnauthorized, http.StatusForbidden:
		err = ErrNotFound
	default:
		err = fmt.Errorf("Bitbucket answered %s for %s", resp.Status, req.URL.Path)
	}
	resp.Body.Close() // error path; the status is what matters
	cancel()
	return nil, err
}
