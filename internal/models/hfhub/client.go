// Package hfhub reads models from the Hugging Face Hub: a repo's metadata,
// one of its files, whether a token may download it, and whose token it
// is. A token goes only to the configured Hub host, never to a host a
// redirect names, and never into an error.
package hfhub

import (
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
	"unicode/utf8"
)

// Errors a Client returns. Test with errors.Is.
var (
	ErrNotFound     = errors.New("not found on Hugging Face")
	ErrGated        = errors.New("gated on Hugging Face: the token's account hasn't been granted access")
	ErrUnauthorized = errors.New("the token wasn't accepted by Hugging Face")
	ErrUnavailable  = errors.New("no answer from Hugging Face")
)

// DefaultURL is the Hub. Config ai.models.huggingface_url overrides it for
// tests and mirrors.
const DefaultURL = "https://huggingface.co"

const (
	// timeout bounds one request, redirects included.
	timeout = 15 * time.Second
	// maxModelBytes caps a model's metadata. The largest listings are well
	// under a megabyte.
	maxModelBytes = 4 << 20
	// maxWhoAmIBytes caps the whoami answer.
	maxWhoAmIBytes = 1 << 20
	// maxErrorBytes caps an error's text.
	maxErrorBytes = 512
	// maxErrorBody is how much of a refusal's body is read, before the
	// token is taken out and the text cut to maxErrorBytes.
	maxErrorBody = 4096
	// maxRedirects is how many redirects one request may follow.
	maxRedirects = 5
	redacted     = "[redacted]"
)

// Values of the X-Error-Code header the Hub sets on a refusal. An
// anonymous request for a missing repo gets 401, like a gated one, and only
// this header tells the two apart.
const (
	hubRepoNotFound = "RepoNotFound"
	hubGatedRepo    = "GatedRepo"
)

var (
	repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}/[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)
	shaPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// refPattern is a branch or tag name, for Model's revision.
	refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// ValidRepo reports whether repo is a Hugging Face model ID the way a
// registry entry ID is: "<owner>/<name>".
func ValidRepo(repo string) bool {
	return repoPattern.MatchString(repo) && !strings.Contains(repo, "..")
}

// Client talks to the Hub. It's safe for concurrent use.
type Client struct {
	base *url.URL
	hc   *http.Client
}

// New returns a Client for the Hub at baseURL (DefaultURL when empty). hc
// may be nil; either way the Client works on its own copy, with a 15 second
// timeout and its own redirect rules.
func New(baseURL string, hc *http.Client) *Client {
	if baseURL == "" {
		baseURL = DefaultURL
	}
	base, err := url.Parse(strings.TrimSuffix(baseURL, "/"))
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		// A bad URL is a config mistake; every request fails until it's fixed.
		base = &url.URL{Scheme: "https", Host: "invalid.invalid"}
	}
	base.User, base.RawQuery, base.Fragment = nil, "", ""
	c := &Client{base: base}
	own := &http.Client{}
	if hc != nil {
		*own = *hc
	}
	if own.Timeout <= 0 || own.Timeout > timeout {
		own.Timeout = timeout
	}
	own.CheckRedirect = c.checkRedirect
	c.hc = own
	return c
}

// requestKind says which redirects a request may follow.
type requestKind int

const (
	kindAPI     requestKind = iota // the Hub host only
	kindRawFile                    // the Hub host and its file CDNs
	kindNoFollow
)

type kindKey struct{}

// checkRedirect refuses a redirect off the Hub host, except a file download
// to the Hub's CDNs, and drops the token from every redirect that leaves
// the Hub host. net/http would otherwise forward it to any subdomain.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	kind, ok := req.Context().Value(kindKey{}).(requestKind)
	if !ok {
		kind = kindAPI
	}
	if kind == kindNoFollow {
		return http.ErrUseLastResponse
	}
	if len(via) > maxRedirects {
		return fmt.Errorf("more than %d redirects", maxRedirects)
	}
	if c.isBase(req.URL) {
		return nil
	}
	req.Header.Del("Authorization")
	if kind == kindRawFile && fileCDN(req.URL) {
		return nil
	}
	return fmt.Errorf("refused a redirect to %s", req.URL.Host)
}

func (c *Client) isBase(u *url.URL) bool {
	return u.Scheme == c.base.Scheme && strings.EqualFold(u.Host, c.base.Host)
}

// fileCDN reports whether u is one of the hosts the Hub sends file
// downloads to: cdn-lfs*.huggingface.co or cas-bridge.xethub.hf.co, over
// https on the default port.
func fileCDN(u *url.URL) bool {
	if u.Scheme != "https" || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "cas-bridge.xethub.hf.co" ||
		(strings.HasPrefix(host, "cdn-lfs") && strings.HasSuffix(host, ".huggingface.co") && !strings.Contains(strings.TrimSuffix(host, ".huggingface.co"), "."))
}

// ModelInfo is a repo at one revision.
type ModelInfo struct {
	ID, SHA, PipelineTag, LibraryName string
	Gated                             bool   // "auto" or "manual" count as gated
	License                           string // from the "license:" tag
	Tags                              []string
	Params                            int64 // safetensors.total
	Files                             []FileInfo
	Downloads, Likes                  int
}

// FileInfo is one file of a repo.
type FileInfo struct {
	Name string
	Size int64
}

// modelJSON is the part of GET /api/models/<repo>/revision/<rev> a
// ModelInfo comes from.
type modelJSON struct {
	ID          string          `json:"id"`
	SHA         string          `json:"sha"`
	PipelineTag string          `json:"pipeline_tag"`
	LibraryName string          `json:"library_name"`
	Gated       json.RawMessage `json:"gated"` // false, "auto" or "manual"
	Tags        []string        `json:"tags"`
	Downloads   int             `json:"downloads"`
	Likes       int             `json:"likes"`
	Safetensors *struct {
		Total int64 `json:"total"`
	} `json:"safetensors"`
	Siblings []struct {
		Name string `json:"rfilename"`
		Size int64  `json:"size"`
		LFS  *struct {
			Size int64 `json:"size"`
		} `json:"lfs"`
	} `json:"siblings"`
}

// Model returns repo's metadata at revision ("" is main), with every file's
// size: GET /api/models/<repo>/revision/<revision>?blobs=true, at most
// 4 MiB. token may be "".
func (c *Client) Model(ctx context.Context, repo, revision, token string) (*ModelInfo, error) {
	if !ValidRepo(repo) {
		return nil, fmt.Errorf("%w: %q isn't a Hugging Face model ID like owner/name", ErrNotFound, repo)
	}
	if revision == "" {
		revision = "main"
	}
	if !refPattern.MatchString(revision) {
		return nil, fmt.Errorf("%w: revision %q isn't a commit SHA or branch name", ErrNotFound, revision)
	}
	body, err := c.get(ctx, kindAPI, http.MethodGet, "/api/models/"+repo+"/revision/"+url.PathEscape(revision), "blobs=true", token, maxModelBytes)
	if err != nil {
		return nil, err
	}
	var m modelJSON
	if err := json.Unmarshal(body, &m); err != nil {
		// Not wrapped: a decode error can quote the body.
		return nil, fmt.Errorf("%w: the metadata of %s isn't JSON Loomplane can read", ErrUnavailable, repo)
	}
	info := &ModelInfo{
		ID: m.ID, SHA: m.SHA, PipelineTag: m.PipelineTag, LibraryName: m.LibraryName,
		Gated: gated(m.Gated), Tags: m.Tags, Downloads: m.Downloads, Likes: m.Likes,
	}
	for _, t := range m.Tags {
		if l, ok := strings.CutPrefix(t, "license:"); ok {
			info.License = l
			break
		}
	}
	if m.Safetensors != nil {
		info.Params = m.Safetensors.Total
	}
	for _, s := range m.Siblings {
		size := s.Size
		if size == 0 && s.LFS != nil {
			size = s.LFS.Size
		}
		info.Files = append(info.Files, FileInfo{Name: s.Name, Size: size})
	}
	return info, nil
}

// gated reads the Hub's "gated": false, or "auto" or "manual" when a
// download needs an accepted license.
func gated(raw json.RawMessage) bool {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s != "" && s != "false"
	}
	return false
}

// RawFile returns one file of repo at revision, a commit SHA: GET
// /<repo>/resolve/<revision>/<path>, refused when it's over limit bytes. Only
// this request may follow a redirect to the Hub's file CDNs, and the token
// never goes with it.
func (c *Client) RawFile(ctx context.Context, repo, revision, path, token string, limit int64) ([]byte, error) {
	p, err := filePath(repo, revision, path)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, errors.New("hfhub: RawFile needs a positive size limit")
	}
	return c.get(ctx, kindRawFile, http.MethodGet, p, "", token, limit)
}

// Access reports whether token may download repo at revision, a commit
// SHA: HEAD /<repo>/resolve/<revision>/config.json. nil when it may,
// ErrGated when the Hub refuses (401 or 403), ErrNotFound when there's no
// such repo or file. A redirect counts as access and isn't followed.
func (c *Client) Access(ctx context.Context, repo, revision, token string) error {
	p, err := filePath(repo, revision, "config.json")
	if err != nil {
		return err
	}
	_, err = c.get(ctx, kindNoFollow, http.MethodHead, p, "", token, 0)
	return err
}

// whoAmI is the part of GET /api/whoami-v2 WhoAmI reads.
type whoAmI struct {
	Name string `json:"name"`
}

// WhoAmI returns the name of the account token belongs to, or
// ErrUnauthorized when the Hub doesn't accept it.
func (c *Client) WhoAmI(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", ErrUnauthorized
	}
	body, err := c.get(ctx, kindAPI, http.MethodGet, "/api/whoami-v2", "", token, maxWhoAmIBytes)
	if errors.Is(err, ErrGated) {
		// 401 and 403 here are about the token, not a repo.
		return "", ErrUnauthorized
	}
	if err != nil {
		return "", err
	}
	var w whoAmI
	if err := json.Unmarshal(body, &w); err != nil || w.Name == "" {
		return "", fmt.Errorf("%w: its whoami answer has no account name", ErrUnavailable)
	}
	return w.Name, nil
}

// filePath checks a file's repo, revision and path and returns the URL path
// that resolves it. Each path segment is escaped on its own, so a file name
// can't reach another repo or the API.
func filePath(repo, revision, path string) (string, error) {
	if !ValidRepo(repo) {
		return "", fmt.Errorf("%w: %q isn't a Hugging Face model ID like owner/name", ErrNotFound, repo)
	}
	if !shaPattern.MatchString(revision) {
		return "", fmt.Errorf("%w: revision %q isn't a 40-character commit SHA", ErrNotFound, revision)
	}
	segments := strings.Split(path, "/")
	for i, s := range segments {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "\\\x00\r\n") {
			return "", fmt.Errorf("%w: %q isn't a file path in a repo", ErrNotFound, path)
		}
		segments[i] = url.PathEscape(s)
	}
	return "/" + repo + "/resolve/" + revision + "/" + strings.Join(segments, "/"), nil
}

// get makes one request to the Hub and returns the body, at most limit bytes
// (0 reads none). The token goes in the Authorization header, and the
// redirect rules keep it on the Hub host.
func (c *Client) get(ctx context.Context, kind requestKind, method, path, query, token string, limit int64) ([]byte, error) {
	// path is already escaped, segment by segment.
	target := strings.TrimSuffix(c.base.String(), "/") + path
	if query != "" {
		target += "?" + query
	}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, kindKey{}, kind), method, target, http.NoBody)
	if err != nil {
		return nil, fail(token, ErrUnavailable, "build the request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, ctxErr)
		}
		return nil, fail(token, ErrUnavailable, "%s %s: %v", method, path, err)
	}
	defer resp.Body.Close() // read-only

	switch code := resp.Header.Get("X-Error-Code"); {
	case resp.StatusCode >= 200 && resp.StatusCode < 300,
		kind == kindNoFollow && resp.StatusCode >= 300 && resp.StatusCode < 400:
	case code == hubRepoNotFound || resp.StatusCode == http.StatusNotFound:
		return nil, fail(token, ErrNotFound, "%s %s: %s", method, path, errorText(resp))
	case code == hubGatedRepo || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fail(token, ErrGated, "%s %s: %s", method, path, errorText(resp))
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, fail(token, ErrUnavailable, "%s %s: %s", method, path, errorText(resp))
	default:
		return nil, fail(token, nil, "Hugging Face refused %s %s: %s", method, path, errorText(resp))
	}
	if limit == 0 {
		return nil, nil
	}
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("%s %s: the answer is %d bytes, over the %d byte limit", method, path, resp.ContentLength, limit)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fail(token, ErrUnavailable, "%s %s: %v", method, path, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s %s: the answer is over the %d byte limit", method, path, limit)
	}
	return body, nil
}

// errorText is a refusal's status and the start of its body.
func errorText(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody)) //nolint:errcheck // best effort: the status says enough
	msg := strings.TrimSpace(string(b))
	if msg == "" {
		return resp.Status
	}
	return resp.Status + ": " + msg
}

// hubError is a failure whose text has had the token taken out. It wraps
// only the sentinel, never the error it was made from, so nothing that
// unwraps it reaches the token either.
type hubError struct {
	sentinel error
	msg      string
}

func (e *hubError) Error() string { return e.msg }
func (e *hubError) Unwrap() error { return e.sentinel }

// fail builds a hubError: the sentinel's text (when there is one) and the
// formatted detail, with every copy of token replaced by [redacted], cut
// to 512 bytes.
func fail(token string, sentinel error, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if sentinel != nil {
		msg = sentinel.Error() + ": " + msg
	}
	return &hubError{sentinel: sentinel, msg: Redact(msg, token)}
}

// Redact replaces every copy of token in s with [redacted] and cuts the
// result to 512 bytes on a character boundary. When the cut, or a body read
// only so far, leaves the start of the token at the end, that goes too.
func Redact(s, token string) string {
	if token != "" {
		s = strings.ReplaceAll(s, token, redacted)
	}
	if len(s) > maxErrorBytes {
		cut := maxErrorBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	for k := len(token) - 1; k >= minTokenPrefix; k-- {
		if strings.HasSuffix(s, token[:k]) {
			return s[:len(s)-k] + redacted
		}
	}
	return s
}

// minTokenPrefix is the shortest start of a token Redact takes off the end
// of a text: "hf_" and one more character.
const minTokenPrefix = 4
