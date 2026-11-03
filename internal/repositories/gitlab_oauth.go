package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

// gitlabScope is the only scope Loomplane ever asks GitLab for. It reads
// projects and archives and can't write.
const gitlabScope = "read_api"

// gitlabDefaultLifetime is how long GitLab's access tokens last, used when
// a token response doesn't say.
const gitlabDefaultLifetime = 2 * time.Hour

// gitlabMinMajor is the oldest GitLab release Loomplane supports.
const gitlabMinMajor = 17

// gitlabErrorCode is the shape of an OAuth error code. A body that sends
// anything else as its "error" says nothing we would repeat.
var gitlabErrorCode = regexp.MustCompile(`^[a-z_]{1,40}$`)

// GitLabUser is the account a GitLab sign-in is for.
type GitLabUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
}

// AuthorizeURL is the page to send someone to, to sign in to GitLab and let
// Loomplane read their projects. GitLab sends them back to the redirect
// with a code and the state. The challenge is the S256 PKCE challenge of
// the verifier Exchange will send.
func (g *GitLab) AuthorizeURL(clientID, redirect, state, challenge string) string {
	q := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"state":                 {state},
		"scope":                 {gitlabScope},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	return g.BaseURL + "/oauth/authorize?" + q.Encode()
}

// Exchange trades the code GitLab sent back for a grant.
func (g *GitLab) Exchange(ctx context.Context, app oauth.App, redirect, code, verifier string) (oauth.Grant, error) {
	return g.token(ctx, app, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirect},
	})
}

// Refresh trades the grant's refresh token for a new grant. GitLab spends
// the old refresh token, so the caller must keep the new one.
func (g *GitLab) Refresh(ctx context.Context, app oauth.App, grant oauth.Grant) (oauth.Grant, error) {
	return g.token(ctx, app, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {grant.RefreshToken},
	})
}

// Revoke tells GitLab to stop honoring token, an access or a refresh token.
func (g *GitLab) Revoke(ctx context.Context, app oauth.App, token string) error {
	resp, err := g.postForm(ctx, "/oauth/revoke", app, url.Values{"token": {token}})
	if err != nil {
		return err
	}
	defer resp.Body.Close() // read-only body; its content is not used
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitLab answered %d from its revoke endpoint: %s", resp.StatusCode, readErrorCode(resp.Body))
	}
	return nil
}

// User reads the account token is for.
func (g *GitLab) User(ctx context.Context, token string) (GitLabUser, error) {
	var u GitLabUser
	err := g.getJSON(ctx, "/api/v4/user", token, &u)
	return u, err
}

// Version reads the instance's release, such as "17.4.2-ee".
func (g *GitLab) Version(ctx context.Context, token string) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	if err := g.getJSON(ctx, "/api/v4/version", token, &v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// GitLabVersionSupported reports whether a release such as "17.4.2-ee" is
// one Loomplane supports: major version 17 or newer. A value that doesn't
// start with a version number isn't.
func GitLabVersionSupported(v string) bool {
	major, _, _ := strings.Cut(v, ".")
	major, _, _ = strings.Cut(major, "-")
	for _, r := range major {
		if r < '0' || r > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(major)
	return err == nil && n >= gitlabMinMajor
}

// token calls the token endpoint and reads the grant it hands back.
func (g *GitLab) token(ctx context.Context, app oauth.App, form url.Values) (oauth.Grant, error) {
	resp, err := g.postForm(ctx, "/oauth/token", app, form)
	if err != nil {
		return oauth.Grant{}, err
	}
	defer resp.Body.Close() // read-only body
	if resp.StatusCode != http.StatusOK {
		code := readErrorCode(resp.Body)
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
			switch code {
			case "invalid_grant":
				return oauth.Grant{}, fmt.Errorf("%w: GitLab answered %d: %s", oauth.ErrRefused, resp.StatusCode, code)
			case "invalid_client", "unauthorized_client":
				return oauth.Grant{}, fmt.Errorf("%w: GitLab answered %d: %s", oauth.ErrAppRejected, resp.StatusCode, code)
			}
		}
		return oauth.Grant{}, fmt.Errorf("GitLab answered %d from its token endpoint: %s", resp.StatusCode, code)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBytes)).Decode(&out); err != nil {
		// The decode error isn't wrapped: it could quote the body.
		return oauth.Grant{}, errors.New("GitLab answered its token request with a body that can't be read")
	}
	if out.AccessToken == "" {
		return oauth.Grant{}, errors.New("GitLab answered its token request without an access token")
	}
	life := gitlabDefaultLifetime
	if out.ExpiresIn > 0 {
		life = time.Duration(out.ExpiresIn) * time.Second
	}
	return oauth.Grant{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		Expiry:       time.Now().Add(life),
	}, nil
}

// postForm posts form, with the app's credentials added, to one of GitLab's
// OAuth endpoints. The caller closes the body. Errors from the transport
// are not wrapped, since a URL error can quote the request.
func (g *GitLab) postForm(ctx context.Context, p string, app oauth.App, form url.Values) (*http.Response, error) {
	f := url.Values{"client_id": {app.ClientID}}
	if app.ClientSecret != "" {
		f.Set("client_secret", app.ClientSecret)
	}
	for k, v := range form {
		f[k] = v
	}
	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, g.BaseURL+p, strings.NewReader(f.Encode()))
	if err != nil {
		cancel()
		return nil, errors.New("build GitLab request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "loomplane")
	resp, err := g.HTTP.Do(req)
	if err != nil {
		cancel()
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("call GitLab: %w", err)
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// readErrorCode reads the "error" field of an OAuth error body, and
// nothing else: descriptions can quote what was sent. A body that isn't
// JSON, or whose code isn't an OAuth error code, is "no reason given".
func readErrorCode(r io.Reader) string {
	var e struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(r, maxJSONBytes)).Decode(&e); err != nil || !gitlabErrorCode.MatchString(e.Error) {
		return "no reason given"
	}
	return e.Error
}
