package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

// OAuthScopes are the permissions the Cloudflare sign-in app must allow.
const OAuthScopes = "account-settings.read zone.read dns.write offline_access"

func (c *Client) oauthEndpoint() string {
	if c.endpoint == DefaultEndpoint {
		return "https://dash.cloudflare.com"
	}
	return c.endpoint
}

// AuthorizeURL sends the browser to Cloudflare with a one-use state and PKCE.
func (c *Client) AuthorizeURL(app oauth.App, r oauth.AuthorizeRequest) string {
	q := url.Values{
		"client_id": {app.ClientID}, "redirect_uri": {r.Redirect}, "response_type": {"code"},
		"state": {r.State}, "scope": {OAuthScopes}, "code_challenge": {r.Challenge}, "code_challenge_method": {"S256"},
	}
	return c.oauthEndpoint() + "/oauth2/auth?" + q.Encode()
}

// Exchange keeps the account chosen before consent with the verified grant.
func (c *Client) Exchange(ctx context.Context, app oauth.App, r oauth.CodeRequest) (oauth.Grant, error) {
	g, err := c.oauthToken(ctx, app, url.Values{
		"grant_type": {"authorization_code"}, "code": {r.Code}, "redirect_uri": {r.Redirect}, "code_verifier": {r.Verifier},
	})
	if err != nil {
		return oauth.Grant{}, err
	}
	if g.RefreshToken == "" {
		return oauth.Grant{}, fmt.Errorf("%w: enable refresh tokens and offline_access on the Cloudflare app, then sign in again", oauth.ErrAppRejected)
	}
	g.Account = map[string]string{"account_id": r.Settings["account_id"]}
	return g, nil
}

// Refresh preserves refresh and account information omitted from the response.
func (c *Client) Refresh(ctx context.Context, app oauth.App, old oauth.Grant) (oauth.Grant, error) {
	if old.RefreshToken == "" {
		return oauth.Grant{}, oauth.ErrRefused
	}
	g, err := c.oauthToken(ctx, app, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {old.RefreshToken}})
	if err != nil {
		return oauth.Grant{}, err
	}
	if g.RefreshToken == "" {
		g.RefreshToken = old.RefreshToken
	}
	g.Account = maps.Clone(old.Account)
	return g, nil
}

// Probe checks the selected account without using API-token management APIs.
func (c *Client) Probe(ctx context.Context, _ oauth.App, g oauth.Grant) error {
	if g.Account["account_id"] == "" {
		return oauth.ErrRefused
	}
	_, err := c.Account(ctx, g.AccessToken, g.Account["account_id"])
	if errors.Is(err, ErrRejected) {
		return fmt.Errorf("%w: Cloudflare refused account access", oauth.ErrRefused)
	}
	return err
}

func (c *Client) oauthToken(ctx context.Context, app oauth.App, form url.Values) (oauth.Grant, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.oauthEndpoint()+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return oauth.Grant{}, errors.New("cloudflare: could not prepare token request")
	}
	req.SetBasicAuth(url.QueryEscape(app.ClientID), url.QueryEscape(app.ClientSecret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return oauth.Grant{}, errors.New("cloudflare: token endpoint could not be reached")
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse {
		return oauth.Grant{}, errors.New("cloudflare: token response could not be read")
	}
	if res.StatusCode != http.StatusOK {
		var out struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &out) != nil {
			out.Error = ""
		}
		if res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusUnauthorized {
			switch out.Error {
			case "invalid_grant":
				return oauth.Grant{}, oauth.ErrRefused
			case "invalid_client", "unauthorized_client":
				return oauth.Grant{}, oauth.ErrAppRejected
			}
		}
		return oauth.Grant{}, fmt.Errorf("cloudflare: token endpoint answered %d", res.StatusCode)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := json.Unmarshal(data, &out); err != nil || !validOAuthToken(out.AccessToken) || !strings.EqualFold(out.TokenType, "Bearer") || out.ExpiresIn <= 0 || out.ExpiresIn > int64((365*24*time.Hour)/time.Second) {
		return oauth.Grant{}, errors.New("cloudflare: token endpoint returned an invalid grant")
	}
	return oauth.Grant{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, Expiry: time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)}, nil
}

func validOAuthToken(token string) bool {
	return token != "" && len(token) <= 16<<10 && !strings.ContainsAny(token, " \t\r\n")
}

// GrantToken validates a stored OAuth bearer, which can be larger than API tokens.
func GrantToken(token string) error {
	if !validOAuthToken(token) {
		return errors.New("the Cloudflare sign-in has no usable access token")
	}
	return nil
}
