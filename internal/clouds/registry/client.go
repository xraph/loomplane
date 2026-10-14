package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

type Client struct {
	Config *Config
	HTTP   *http.Client
}
type Result struct {
	Details map[string]string
	Missing []string
}

// Check verifies pull access and starts then cancels an empty push upload.
// It leaves no image, manifest or blob in the registry.
func (c *Client) Check(ctx context.Context) (*Result, error) {
	out := &Result{Details: maps.Clone(c.Config.Details), Missing: []string{}}
	base := "/v2/" + c.Config.Repository + "/loomplane-check"
	for _, check := range []struct{ method, path, permission string }{
		{http.MethodGet, "/v2/", "registry:api"},
		{http.MethodGet, base + "/tags/list", "registry:pull"},
		{http.MethodPost, base + "/blobs/uploads/", "registry:push"},
	} {
		response, err := c.request(ctx, check.method, check.path)
		if err != nil {
			return nil, err
		}
		status, location := response.StatusCode, response.Header.Get("Location")
		_ = response.Body.Close()
		capability := strings.TrimPrefix(check.permission, "registry:")
		out.Details[capability] = "ready"
		switch {
		case status == http.StatusUnauthorized:
			return nil, ErrRejected
		case status == http.StatusForbidden:
			out.Details[capability] = "unavailable"
			out.Missing = append(out.Missing, check.permission)
		case status == http.StatusNotFound && check.permission == "registry:pull":
		case status < 200 || status >= 300:
			return nil, fmt.Errorf("registry check returned HTTP %d", status)
		case check.method == http.MethodPost:
			if status != http.StatusAccepted || location == "" {
				return nil, fmt.Errorf("registry did not acknowledge the empty upload")
			}
			cancelPath, err := c.uploadPath(location, base)
			if err != nil {
				return nil, err
			}
			cancel, cancelErr := c.request(context.WithoutCancel(ctx), http.MethodDelete, cancelPath)
			if cancelErr != nil {
				return nil, fmt.Errorf("cancel empty registry upload: %w", cancelErr)
			}
			_ = cancel.Body.Close()
			if cancel.StatusCode != http.StatusNoContent && cancel.StatusCode != http.StatusNotFound {
				return nil, fmt.Errorf("cancel empty registry upload returned HTTP %d", cancel.StatusCode)
			}
		}
	}
	return out, nil
}

func (c *Client) uploadPath(location, base string) (string, error) {
	u, err := url.Parse(location)
	if err != nil || u.User != nil || u.Fragment != "" {
		return "", ErrInvalid
	}
	endpoint, err := url.Parse(c.Config.Endpoint)
	if err != nil {
		return "", ErrInvalid
	}
	if u.IsAbs() && (u.Scheme != endpoint.Scheme || u.Host != endpoint.Host) {
		return "", fmt.Errorf("%w: upload location points outside the registry", ErrInvalid)
	}
	if !strings.HasPrefix(u.Path, base+"/blobs/uploads/") {
		return "", ErrInvalid
	}
	return u.RequestURI(), nil
}

func (c *Client) request(ctx context.Context, method, path string) (*http.Response, error) {
	return c.requestFor(ctx, method, path, c.Config.Repository+"/loomplane-check")
}

func (c *Client) requestFor(ctx context.Context, method, path, repository string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Config.Endpoint+path, http.NoBody)
	if err != nil {
		return nil, err
	}
	if c.Config.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Config.Token)
	} else if c.Config.Username != "" {
		req.SetBasicAuth(c.Config.Username, c.Config.Password)
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach registry: %w", err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		return response, nil
	}
	challenge := response.Header.Get("WWW-Authenticate")
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return response, nil
	}
	_ = response.Body.Close()
	token, err := c.bearerToken(ctx, challenge, repository, "pull,push")
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return c.HTTP.Do(req)
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
	query.Set("scope", "repository:"+repository+":"+actions)
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
	if tokenResponse.StatusCode == 401 || tokenResponse.StatusCode == 403 {
		return "", ErrRejected
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
