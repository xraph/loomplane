package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	checkTimeout        = 8 * time.Second
	cancelUploadTimeout = 2 * time.Second
)

type Client struct {
	Config *Config
	HTTP   *http.Client
}
type Result struct {
	Details map[string]string
	Missing []string
}

func (c *Client) closeIdle() {
	if c != nil && c.HTTP != nil {
		c.HTTP.CloseIdleConnections()
	}
}

func drainBody(body io.Reader) {
	if body == nil {
		return
	}
	if _, err := io.Copy(io.Discard, io.LimitReader(body, 64<<10)); err != nil {
		return
	}
}

// Check verifies pull access and starts then cancels an empty push upload.
// It leaves no image, manifest or blob in the registry.
func (c *Client) Check(ctx context.Context) (*Result, error) {
	defer c.closeIdle()
	// All challenge exchanges share one budget inside the dashboard request.
	ctx, stop := context.WithTimeout(ctx, checkTimeout)
	defer stop()
	out := &Result{Details: maps.Clone(c.Config.Details), Missing: []string{}}
	repository := c.Config.Repository + "/loomplane-check"
	base := "/v2/" + repository
	var cachedChallenge string
	for _, check := range []struct{ method, path, permission, actions string }{
		{http.MethodGet, "/v2/", "registry:api", ""},
		{http.MethodGet, base + "/tags/list", "registry:pull", "pull"},
		{http.MethodPost, base + "/blobs/uploads/", "registry:push", "pull,push"},
	} {
		var reqHeaders http.Header
		passedChallenge := ""
		if cachedChallenge != "" && check.actions != "" && c.Config.Token == "" {
			token, err := c.bearerToken(ctx, cachedChallenge, repository, check.actions)
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, fmt.Errorf("%s timed out waiting for the registry or its token service: %w", check.permission, context.DeadlineExceeded)
			}
			if err != nil {
				return nil, fmt.Errorf("%s check: %w", check.permission, err)
			}
			reqHeaders = http.Header{"Authorization": {"Bearer " + token}}
			passedChallenge = cachedChallenge
		}
		response, challenge, err := c.requestScopedWithChallenge(ctx, check.method, check.path, repository, check.actions, reqHeaders, passedChallenge)
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s timed out waiting for the registry or its token service: %w", check.permission, context.DeadlineExceeded)
		}
		if err != nil {
			return nil, fmt.Errorf("%s check: %w", check.permission, err)
		}
		if challenge != "" {
			cachedChallenge = challenge
		}
		status, location := response.StatusCode, response.Header.Get("Location")
		drainBody(response.Body)
		_ = response.Body.Close()
		capability := strings.TrimPrefix(check.permission, "registry:")
		out.Details[capability] = "ready"
		switch {
		case status == http.StatusUnauthorized:
			return nil, fmt.Errorf("%s check: %w (HTTP 401)", check.permission, ErrRejected)
		case status == http.StatusForbidden:
			out.Details[capability] = "unavailable"
			out.Missing = append(out.Missing, check.permission)
		case status == http.StatusNotFound && check.permission == "registry:pull":
		case status < 200 || status >= 300:
			return nil, fmt.Errorf("%s check returned HTTP %d", check.permission, status)
		case check.method == http.MethodPost:
			if status != http.StatusAccepted || location == "" {
				return nil, fmt.Errorf("registry did not acknowledge the empty upload")
			}
			if err := c.cancelCheckUpload(ctx, location, repository, response.Request); err != nil {
				return nil, fmt.Errorf("%s check: %w", check.permission, err)
			}
		}
	}
	return out, nil
}

func (c *Client) cancelCheckUpload(ctx context.Context, location, repository string, accepted *http.Request) error {
	path, err := c.uploadPath(location, "/v2/"+repository)
	if err != nil {
		return err
	}
	// The validated location belongs to the same registry and repository.
	headers := make(http.Header)
	if accepted != nil {
		if auth := accepted.Header.Get("Authorization"); auth != "" {
			headers.Set("Authorization", auth)
		}
	}
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), cancelUploadTimeout)
	defer stop()
	response, err := c.requestScoped(cleanup, http.MethodDelete, path, repository, "pull,push", headers)
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("timed out cancelling the registry test upload: %w", context.DeadlineExceeded)
	}
	if err != nil {
		// A transport error may contain the signed upload location.
		return errors.New("could not cancel the registry test upload")
	}
	defer response.Body.Close()
	defer drainBody(response.Body)
	if !uploadSettled(response.StatusCode) {
		return fmt.Errorf("cancel empty registry upload returned HTTP %d", response.StatusCode)
	}
	return nil
}

// uploadSettled reports whether an empty upload is gone or will expire.
// Cancelling is optional in the distribution spec and only tidies up: the
// accepted POST already proved push, and an empty session holds no data and
// expires on its own. GHCR answers DELETE with 405. DigitalOcean answers 401
// to a pull,push token, because distribution-based registries want a
// separate delete grant for any DELETE.
func uploadSettled(status int) bool {
	switch status {
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent, http.StatusNotFound,
		http.StatusUnauthorized, http.StatusForbidden, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	}
	return false
}

// uploadPath accepts the Location a registry returns for a new blob upload.
// The distribution spec makes that location opaque, and registries differ:
// GHCR answers /v2/<repo>/blobs/upload/<id> rather than .../uploads/<id>. So
// it only has to stay on this registry and inside the repository's blob
// namespace. The location itself never reaches an error: it can be signed.
func (c *Client) uploadPath(location, base string) (string, error) {
	u, err := url.Parse(location)
	if err != nil || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("%w: the registry returned an unusable upload location", ErrInvalid)
	}
	endpoint, err := url.Parse(c.Config.Endpoint)
	if err != nil {
		return "", ErrInvalid
	}
	if u.IsAbs() && (u.Scheme != endpoint.Scheme || u.Host != endpoint.Host) {
		return "", fmt.Errorf("%w: upload location points outside the registry", ErrInvalid)
	}
	if !strings.HasPrefix(u.Path, base+"/blobs/") || slices.ContainsFunc(strings.Split(u.Path, "/"), func(s string) bool { return s == "." || s == ".." }) {
		return "", fmt.Errorf("%w: upload location points outside %s", ErrInvalid, strings.TrimPrefix(base, "/v2/"))
	}
	return u.RequestURI(), nil
}

func (c *Client) requestFor(ctx context.Context, method, path, repository string) (*http.Response, error) {
	return c.requestScoped(ctx, method, path, repository, "pull,push", nil)
}

func (c *Client) newRequest(ctx context.Context, method, path string, headers http.Header, authHeader string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Config.Endpoint+path, http.NoBody)
	if err != nil {
		return nil, err
	}
	for key, values := range headers {
		req.Header[key] = slices.Clone(values)
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	} else if req.Header.Get("Authorization") == "" {
		if c.Config.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Config.Token)
		} else if c.Config.Username != "" {
			req.SetBasicAuth(c.Config.Username, c.Config.Password)
		}
	}
	return req, nil
}

func (c *Client) requestScoped(ctx context.Context, method, path, repository, actions string, headers http.Header) (*http.Response, error) {
	response, _, err := c.requestScopedWithChallenge(ctx, method, path, repository, actions, headers, "")
	return response, err
}

func (c *Client) requestScopedWithChallenge(ctx context.Context, method, path, repository, actions string, headers http.Header, cachedChallenge string) (*http.Response, string, error) {
	req, err := c.newRequest(ctx, method, path, headers, "")
	if err != nil {
		return nil, "", err
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("reach registry: %w", err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		return response, "", nil
	}
	challenge := response.Header.Get("WWW-Authenticate")
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return response, "", nil
	}
	if headers != nil && headers.Get("Authorization") != "" && cachedChallenge != "" && challenge == cachedChallenge {
		// Pre-fetched bearer token for this exact challenge was refused.
		return response, challenge, nil
	}
	drainBody(response.Body)
	_ = response.Body.Close()
	token, err := c.bearerToken(ctx, challenge, repository, actions)
	if err != nil {
		return nil, "", err
	}
	retryReq, err := c.newRequest(ctx, method, path, headers, "Bearer "+token)
	if err != nil {
		return nil, "", err
	}
	retryResp, err := c.HTTP.Do(retryReq)
	if err != nil {
		return nil, "", fmt.Errorf("reach registry: %w", err)
	}
	return retryResp, challenge, nil
}

func (c *Client) bearerToken(ctx context.Context, challenge, repository, actions string) (string, error) {
	// MIME's quoted-parameter parser preserves commas inside scope values.
	var parameters strings.Builder
	quoted, escaped := false, false
	for _, r := range challenge[len("Bearer "):] {
		if r == '"' && !escaped {
			quoted = !quoted
		}
		if r == ',' && !quoted {
			r = ';'
		}
		parameters.WriteRune(r)
		escaped = r == '\\' && !escaped
	}
	_, params, err := mime.ParseMediaType("bearer;" + parameters.String())
	// Scope is generated from the reviewed repository, not the challenge.
	if err != nil {
		return "", fmt.Errorf("%w: malformed registry challenge", ErrInvalid)
	}
	realm, err := safeURL(params["realm"], false)
	if err != nil {
		return "", err
	}
	endpoint, err := url.Parse(c.Config.Endpoint)
	if err != nil {
		return "", ErrInvalid
	}
	if realm.String() != c.Config.AuthEndpoint && (realm.Scheme != endpoint.Scheme || realm.Host != endpoint.Host) {
		return "", fmt.Errorf("%w: set auth_endpoint to approve the registry's token service", ErrInvalid)
	}
	query := realm.Query()
	query.Set("service", params["service"])
	// Some registries, including GHCR, require a repository scope even
	// for /v2/. Use the configured probe repository for that read.
	if actions == "" && params["scope"] != "" {
		actions = "pull"
	}
	if actions != "" {
		query.Set("scope", "repository:"+repository+":"+actions)
	}
	realm.RawQuery = query.Encode()
	tokenReq, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), http.NoBody)
	if err != nil {
		return "", err
	}
	if c.Config.Username != "" {
		tokenReq.SetBasicAuth(c.Config.Username, c.Config.Password)
	}
	tokenResponse, err := c.HTTP.Do(tokenReq)
	if err != nil {
		return "", fmt.Errorf("reach registry token service: %w", err)
	}
	defer tokenResponse.Body.Close()
	defer drainBody(tokenResponse.Body)
	if tokenResponse.StatusCode == 401 || tokenResponse.StatusCode == 403 {
		return "", tokenDenial(tokenResponse, repository, actions)
	}
	if tokenResponse.StatusCode != 200 {
		return "", fmt.Errorf("registry token service returned HTTP %d", tokenResponse.StatusCode)
	}
	var token struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err = json.NewDecoder(io.LimitReader(tokenResponse.Body, 1<<20)).Decode(&token); err != nil {
		return "", fmt.Errorf("decode registry token: %w", err)
	}
	if token.Token == "" {
		token.Token = token.AccessToken
	}
	if token.Token == "" || strings.ContainsAny(token.Token, "\r\n") {
		return "", ErrRejected
	}
	return token.Token, nil
}

// Registry errors can echo credentials or signed URLs. Only known reasons and
// the repository name from our own request reach the connection form.
func tokenDenial(response *http.Response, repository, actions string) error {
	err := fmt.Errorf("%w at the token service (HTTP %d)", ErrRejected, response.StatusCode)
	if response.StatusCode != http.StatusForbidden || actions != "pull,push" {
		return err
	}
	var body struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	denied := fmt.Errorf("%w: push access to %s was denied", err, repository)
	if decodeErr := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&body); decodeErr != nil {
		return denied
	}
	messages := []string{body.Message}
	for _, item := range body.Errors {
		messages = append(messages, item.Message)
	}
	reason := strings.ToLower(strings.Join(messages, " "))
	switch {
	case strings.Contains(reason, "garbage collection"):
		return fmt.Errorf("%w: garbage collection is restricting writes to %s", err, repository)
	case strings.Contains(reason, "read-only") || strings.Contains(reason, "read only"):
		return fmt.Errorf("%w: the registry reported read-only access to %s", err, repository)
	case strings.Contains(reason, "quota") || strings.Contains(reason, "repository limit"):
		return fmt.Errorf("%w: the registry reported a quota or repository limit for %s", err, repository)
	default:
		return denied
	}
}
