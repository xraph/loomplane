package client

import (
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

// Signing in as yourself, without an API key: the OAuth 2.0 device
// authorization grant (RFC 8628), which Loomplane's authsome serves.
//
//	login, err := client.StartDeviceLogin(ctx, "https://loomplane.example.com")
//	fmt.Println("Open", login.VerificationURI, "and enter", login.UserCode)
//	token, err := login.Wait(ctx)
//	c := client.New(login.Base, token.AccessToken)
//
// The token starts with no organization; pick one with UseOrganization.

// CLIClientID is the OAuth client loomctl signs in as.
const CLIClientID = "loomctl"

const deviceCodeGrant = "urn:ietf:params:oauth:grant-type:device_code"

// Device login errors, from the token endpoint's answer.
var (
	ErrDeviceDenied  = errors.New("the sign-in was denied in the browser")
	ErrDeviceExpired = errors.New("the code expired before anyone approved it; run login again")
)

// DeviceLogin is a device code waiting for you to approve it.
type DeviceLogin struct {
	// Base is the Loomplane the code is for.
	Base     string `json:"-"`
	UserCode string `json:"user_code"`
	// VerificationURI is the dashboard page that approves the code, and
	// VerificationURIComplete the same with the code filled in.
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`

	deviceCode string
	oauth      oauthEndpoints
	http       *http.Client
	// sleep waits between polls. Tests replace it.
	sleep func(context.Context, time.Duration) error
}

// Token is what a device login, or refreshing one, gets you.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// oauthEndpoints are authsome's OAuth routes at one Loomplane. They're
// built from the auth base path the public config names rather than read
// from discovery: discovery names Loomplane's public URL, which isn't
// always the address this client reached it at.
type oauthEndpoints struct {
	device, token, revoke string
}

func discoverOAuth(ctx context.Context, h *http.Client, base string) (oauthEndpoints, error) {
	c := (&Client{base: base, http: h})
	var cfg struct {
		AuthBasePath string `json:"auth_base_path"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/public-config", nil, &cfg); err != nil {
		return oauthEndpoints{}, fmt.Errorf("read %s's public config: %w", base, err)
	}
	if cfg.AuthBasePath == "" {
		return oauthEndpoints{}, fmt.Errorf("%s doesn't say where its sign-in is", base)
	}
	root := base + strings.TrimRight(cfg.AuthBasePath, "/") + "/v1/oauth"
	return oauthEndpoints{device: root + "/device/authorize", token: root + "/token", revoke: root + "/revoke"}, nil
}

// StartDeviceLogin asks the Loomplane at baseURL for a code to approve.
func StartDeviceLogin(ctx context.Context, baseURL string) (*DeviceLogin, error) {
	return startDeviceLogin(ctx, &http.Client{Timeout: 30 * time.Second}, strings.TrimRight(baseURL, "/"))
}

func startDeviceLogin(ctx context.Context, h *http.Client, base string) (*DeviceLogin, error) {
	ep, err := discoverOAuth(ctx, h, base)
	if err != nil {
		return nil, err
	}
	var out struct {
		DeviceLogin
		DeviceCode string `json:"device_code"`
	}
	if err := postForm(ctx, h, ep.device, url.Values{"client_id": {CLIClientID}}, &out); err != nil {
		return nil, fmt.Errorf("ask %s for a sign-in code: %w", base, err)
	}
	login := out.DeviceLogin
	login.Base, login.deviceCode, login.oauth, login.http = base, out.DeviceCode, ep, h
	if login.Interval <= 0 {
		login.Interval = 5
	}
	return &login, nil
}

// Wait polls until the code is approved, denied or expires, at the pace the
// server asks for.
func (d *DeviceLogin) Wait(ctx context.Context) (*Token, error) {
	sleep := d.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	interval := time.Duration(d.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(d.ExpiresIn) * time.Second)
	form := url.Values{"grant_type": {deviceCodeGrant}, "device_code": {d.deviceCode}, "client_id": {CLIClientID}}
	for {
		if err := sleep(ctx, interval); err != nil {
			return nil, err
		}
		tok, err := requestToken(ctx, d.http, d.oauth.token, form)
		var oe *oauthError
		switch {
		case err == nil:
			return tok, nil
		case !errors.As(err, &oe):
			return nil, err
		case oe.Code == "authorization_pending":
		case oe.Code == "slow_down":
			// RFC 8628 section 3.5: five seconds more, from now on.
			interval += 5 * time.Second
		case oe.Code == "access_denied":
			return nil, ErrDeviceDenied
		case oe.Code == "expired_token":
			return nil, ErrDeviceExpired
		default:
			return nil, err
		}
		if d.ExpiresIn > 0 && time.Now().After(deadline) {
			return nil, ErrDeviceExpired
		}
	}
}

// RefreshToken trades a device login's refresh token for a new token.
func RefreshToken(ctx context.Context, baseURL, refreshToken string) (*Token, error) {
	h := &http.Client{Timeout: 30 * time.Second}
	base := strings.TrimRight(baseURL, "/")
	ep, err := discoverOAuth(ctx, h, base)
	if err != nil {
		return nil, err
	}
	return requestToken(ctx, h, ep.token, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}, "client_id": {CLIClientID},
	})
}

// RevokeToken ends a device login's session. Revoking a token that's
// already gone succeeds.
func RevokeToken(ctx context.Context, baseURL, token string) error {
	h := &http.Client{Timeout: 30 * time.Second}
	base := strings.TrimRight(baseURL, "/")
	ep, err := discoverOAuth(ctx, h, base)
	if err != nil {
		return err
	}
	return postForm(ctx, h, ep.revoke, url.Values{"token": {token}, "client_id": {CLIClientID}}, nil)
}

func requestToken(ctx context.Context, h *http.Client, endpoint string, form url.Values) (*Token, error) {
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := postForm(ctx, h, endpoint, form, &out); err != nil {
		return nil, err
	}
	if out.AccessToken == "" {
		return nil, errors.New("the token endpoint answered without a token")
	}
	return &Token{
		AccessToken: out.AccessToken, RefreshToken: out.RefreshToken,
		ExpiresAt: time.Now().Add(time.Duration(out.ExpiresIn) * time.Second).UTC(),
	}, nil
}

// oauthError is an RFC 6749 section 5.2 error answer.
type oauthError struct {
	Status      int
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *oauthError) Error() string {
	if e.Description != "" {
		return e.Description
	}
	return fmt.Sprintf("%s (%d)", e.Code, e.Status)
}

// postForm posts an OAuth form and decodes the JSON answer into out. A
// refusal comes back as an *oauthError when it is one, else an *Error.
func postForm(ctx context.Context, h *http.Client, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := h.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		oe := &oauthError{Status: res.StatusCode}
		if json.Unmarshal(data, oe) == nil && oe.Code != "" {
			return oe
		}
		return &Error{Status: res.StatusCode, Message: message(data)}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// --- The organization a device login acts in ---

// Organization is one you belong to.
type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// Organizations are the ones you belong to, and the one the session acts
// in, if it has one yet.
type Organizations struct {
	Organizations []Organization `json:"organizations"`
	Active        string         `json:"active,omitempty"`
}

// Organizations lists the organizations a device login's account belongs
// to. An API key is bound to one organization and gets 401 here.
func (c *Client) Organizations(ctx context.Context) (*Organizations, error) {
	var out Organizations
	return &out, c.do(ctx, http.MethodGet, "/api/v1/cli/organizations", nil, &out)
}

// UseOrganization makes one of your organizations the one a device login
// acts in.
func (c *Client) UseOrganization(ctx context.Context, orgID string) (*Organizations, error) {
	var out Organizations
	return &out, c.do(ctx, http.MethodPut, "/api/v1/cli/organization", map[string]string{"org_id": orgID}, &out)
}
