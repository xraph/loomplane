package digitalocean

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

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

// webEndpoint is where DigitalOcean's sign-in pages and token endpoint live.
const webEndpoint = "https://cloud.digitalocean.com"

// web is the sign-in host: DigitalOcean's own, or a test's fake.
func (c *Client) web() string {
	if c.endpoint == DefaultEndpoint {
		return webEndpoint
	}
	return c.endpoint
}

// AuthorizeURL is the page to send someone to, to sign in to DigitalOcean
// and let Loomplane read and write their team's resources. DigitalOcean
// sends them back to the redirect with a code and the state. It takes no
// PKCE and no settings.
func (c *Client) AuthorizeURL(app oauth.App, r oauth.AuthorizeRequest) string {
	q := url.Values{
		"client_id":     {app.ClientID},
		"redirect_uri":  {r.Redirect},
		"response_type": {"code"},
		"scope":         {"read write"},
		"state":         {r.State},
	}
	return c.web() + "/v1/oauth/authorize?" + q.Encode()
}

// Exchange trades the code DigitalOcean sent back for a grant.
func (c *Client) Exchange(ctx context.Context, app oauth.App, r oauth.CodeRequest) (oauth.Grant, error) {
	return c.token(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {r.Code},
		"client_id":     {app.ClientID},
		"client_secret": {app.ClientSecret},
		"redirect_uri":  {r.Redirect},
	})
}

// Refresh trades the grant's refresh token for a new grant. DigitalOcean
// spends the old refresh token, so the caller must keep the new one.
func (c *Client) Refresh(ctx context.Context, app oauth.App, g oauth.Grant) (oauth.Grant, error) {
	return c.token(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {g.RefreshToken},
		"client_id":     {app.ClientID},
		"client_secret": {app.ClientSecret},
	})
}

// Probe asks DigitalOcean whose account the grant's access token is for,
// without renewing it. A token it no longer honors is oauth.ErrRefused.
func (c *Client) Probe(ctx context.Context, _ oauth.App, g oauth.Grant) error {
	_, err := c.Account(ctx, g.AccessToken)
	if errors.Is(err, ErrRejected) {
		return fmt.Errorf("%w: %w", oauth.ErrRefused, err)
	}
	return err
}

func (c *Client) token(ctx context.Context, form url.Values) (oauth.Grant, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.web()+"/v1/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return oauth.Grant{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return oauth.Grant{}, fmt.Errorf("digitalocean: %w", err)
	}
	defer res.Body.Close() // read-only; errors surface through ReadAll
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return oauth.Grant{}, fmt.Errorf("digitalocean: read response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if err := json.Unmarshal(data, &e); err != nil {
			e.Error, e.Description = "", "" // a body that isn't JSON says nothing
		}
		if res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusUnauthorized {
			switch e.Error {
			case "invalid_grant":
				return oauth.Grant{}, fmt.Errorf("%w: digitalocean answered %d: %s", oauth.ErrRefused, res.StatusCode, e.Error)
			case "invalid_client", "unauthorized_client":
				return oauth.Grant{}, fmt.Errorf("%w: digitalocean answered %d: %s", oauth.ErrAppRejected, res.StatusCode, e.Error)
			}
		}
		// Only the error code is kept, never the raw body.
		msg := e.Error
		if msg == "" {
			msg = "no reason given"
		}
		return oauth.Grant{}, fmt.Errorf("digitalocean answered %d from its token endpoint: %s", res.StatusCode, msg)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Info         struct {
			Email string `json:"email"`
			UUID  string `json:"uuid"`
			Team  struct {
				UUID string `json:"uuid"`
				Name string `json:"name"`
			} `json:"team"`
		} `json:"info"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return oauth.Grant{}, fmt.Errorf("digitalocean: decode token response: %w", err)
	}
	if out.AccessToken == "" {
		return oauth.Grant{}, fmt.Errorf("digitalocean answered its token request without an access token")
	}
	return oauth.Grant{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
		Account: map[string]string{
			"account_uuid": out.Info.UUID,
			"team_uuid":    out.Info.Team.UUID,
			"team_name":    out.Info.Team.Name,
			"email":        out.Info.Email,
		},
	}, nil
}
