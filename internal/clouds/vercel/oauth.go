package vercel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

// webEndpoint is where Vercel's integration install pages live.
const webEndpoint = "https://vercel.com"

// web is the install page's host: Vercel's own, or a test's fake.
func (c *Client) web() string {
	if c.endpoint == DefaultEndpoint {
		return webEndpoint
	}
	return c.endpoint
}

// AuthorizeURL is the page to send someone to, to install Loomplane's
// Vercel Integration on their team or account. Vercel sends them back to
// the integration's redirect with a code. It may not pass the state along;
// the dashboard keeps its own copy for that case.
func (c *Client) AuthorizeURL(app oauth.App, r oauth.AuthorizeRequest) string {
	return c.web() + "/integrations/" + url.PathEscape(app.Slug) + "/new?" + url.Values{"state": {r.State}}.Encode()
}

// Exchange trades the code Vercel sent back for a grant. The access token
// doesn't expire and has no refresh token; it stops working when the
// integration is uninstalled.
func (c *Client) Exchange(ctx context.Context, app oauth.App, r oauth.CodeRequest) (oauth.Grant, error) {
	form := url.Values{
		"client_id":     {app.ClientID},
		"client_secret": {app.ClientSecret},
		"code":          {r.Code},
		"redirect_uri":  {r.Redirect},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/v2/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return oauth.Grant{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return oauth.Grant{}, fmt.Errorf("vercel: %w", err)
	}
	defer res.Body.Close() // read-only; errors surface through ReadAll
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return oauth.Grant{}, fmt.Errorf("vercel: read response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		code := errorCode(data)
		if res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusUnauthorized {
			switch code {
			case "invalid_grant":
				return oauth.Grant{}, fmt.Errorf("%w: vercel answered %d: %s", oauth.ErrRefused, res.StatusCode, code)
			case "invalid_client", "unauthorized_client":
				return oauth.Grant{}, fmt.Errorf("%w: vercel answered %d: %s", oauth.ErrAppRejected, res.StatusCode, code)
			}
		}
		// Only the error code is kept, never the raw body.
		if code == "" {
			code = "no reason given"
		}
		return oauth.Grant{}, fmt.Errorf("vercel answered %d from its token endpoint: %s", res.StatusCode, code)
	}
	var out struct {
		AccessToken    string `json:"access_token"`
		InstallationID string `json:"installation_id"`
		UserID         string `json:"user_id"`
		TeamID         string `json:"team_id"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return oauth.Grant{}, fmt.Errorf("vercel: decode token response: %w", err)
	}
	if out.AccessToken == "" {
		return oauth.Grant{}, fmt.Errorf("vercel answered its token request without an access token")
	}
	account := map[string]string{}
	for k, v := range map[string]string{"team_id": out.TeamID, "user_id": out.UserID, "installation_id": out.InstallationID} {
		if v != "" {
			account[k] = v
		}
	}
	return oauth.Grant{AccessToken: out.AccessToken, Account: account}, nil
}

// errorCode reads the error code out of a token endpoint's body: either
// OAuth's {"error":"invalid_grant"} or Vercel's {"error":{"code":...}}.
func errorCode(data []byte) string {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &e) != nil || len(e.Error) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(e.Error, &s) == nil {
		return s
	}
	var o struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(e.Error, &o) == nil {
		return o.Code
	}
	return ""
}

// Refresh returns the grant as it is: a Vercel integration's token has no
// expiry and no refresh token, so it's never due.
func (c *Client) Refresh(_ context.Context, _ oauth.App, g oauth.Grant) (oauth.Grant, error) {
	return g, nil
}

// Probe asks Vercel whose token this is, for the grant's team. A token
// Vercel no longer honors, including one for an integration that has been
// uninstalled or disabled, is oauth.ErrRefused.
func (c *Client) Probe(ctx context.Context, _ oauth.App, g oauth.Grant) error {
	err := c.json(ctx, Token{Value: g.AccessToken, TeamID: g.Account["team_id"]}, http.MethodGet, "/v2/user", nil, nil, nil)
	if Revoked(err) {
		return fmt.Errorf("%w: %w", oauth.ErrRefused, err)
	}
	return err
}

// Revoked reports whether err is Vercel no longer honoring a token at
// all: revoked, or its integration uninstalled or disabled. A token
// merely kept out of one team or project isn't.
func Revoked(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (ae.Status == http.StatusUnauthorized || ae.Code == "integration_configuration_disabled")
}
