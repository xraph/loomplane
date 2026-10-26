package gcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

// authorizedUser is the type of a key that signs in as a person.
const authorizedUser = "authorized_user"

// signInScope is what a Google sign-in asks for: who the person is, and
// Google Cloud as them. IAM narrows it to what they may do in the project.
const signInScope = "openid email " + Scope

// projectRE matches Google Cloud project IDs: 6 to 30 lowercase letters,
// digits and hyphens, starting with a letter and not ending in a hyphen.
var projectRE = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// CheckProjectID refuses anything that isn't a Google Cloud project ID.
func CheckProjectID(id string) error {
	if !projectRE.MatchString(id) {
		return fmt.Errorf("%q isn't a Google Cloud project ID; they're 6 to 30 lowercase letters, digits and hyphens, starting with a letter", id)
	}
	return nil
}

// authURL is Google's sign-in page, or a test's fake.
func (c *Client) authURL() string { return c.url("accounts.google.com", "/o/oauth2/v2/auth") }

// tokenURL is Google's token endpoint, or a test's fake.
func (c *Client) tokenURL() string { return c.url("oauth2.googleapis.com", "/token") }

// AuthorizeURL is the page to send someone to, to sign in to Google and
// let Loomplane use Google Cloud as them. Google sends them back to the
// redirect with a code and the state. Offline access and consent make it
// hand back a refresh token every time, not only the first.
func (c *Client) AuthorizeURL(app oauth.App, r oauth.AuthorizeRequest) string {
	q := url.Values{
		"client_id":              {app.ClientID},
		"redirect_uri":           {r.Redirect},
		"response_type":          {"code"},
		"scope":                  {signInScope},
		"access_type":            {"offline"},
		"prompt":                 {"consent"},
		"include_granted_scopes": {"true"},
		"state":                  {r.State},
		"code_challenge":         {r.Challenge},
		"code_challenge_method":  {"S256"},
	}
	return c.authURL() + "?" + q.Encode()
}

// Exchange trades the code Google sent back, and the PKCE verifier its
// challenge came from, for a grant. The grant's account says who signed
// in and which project the sign-in is for.
func (c *Client) Exchange(ctx context.Context, app oauth.App, r oauth.CodeRequest) (oauth.Grant, error) {
	out, err := c.token(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {r.Code},
		"code_verifier": {r.Verifier},
		"client_id":     {app.ClientID},
		"client_secret": {app.ClientSecret},
		"redirect_uri":  {r.Redirect},
	})
	if err != nil {
		return oauth.Grant{}, err
	}
	if out.RefreshToken == "" {
		return oauth.Grant{}, fmt.Errorf("%w: Google sent no refresh token; remove Loomplane's access at myaccount.google.com/permissions and sign in again", oauth.ErrRefused)
	}
	who, err := signedIn(out.IDToken)
	if err != nil {
		return oauth.Grant{}, err
	}
	return oauth.Grant{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		Expiry:       out.expiry(),
		Account:      map[string]string{"subject": who.Subject, "email": who.Email, "project_id": r.Settings["project_id"]},
	}, nil
}

// person is who an ID token says signed in.
type person struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
}

// signedIn reads who an ID token is for. Its signature isn't checked: it
// came straight from Google's token endpoint over TLS, which OpenID
// Connect allows in place of checking it.
func signedIn(idToken string) (who person, err error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return who, errors.New("google answered its token request without an ID token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil || json.Unmarshal(payload, &who) != nil {
		return who, errors.New("google answered its token request with an ID token that can't be read")
	}
	if who.Subject == "" || who.Email == "" {
		return who, errors.New("google's ID token doesn't say who signed in")
	}
	return who, nil
}

// Refresh trades the grant's refresh token for a new access token.
// Google keeps the refresh token, so the old one is kept when no new one
// comes back.
func (c *Client) Refresh(ctx context.Context, app oauth.App, g oauth.Grant) (oauth.Grant, error) {
	out, err := c.token(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {g.RefreshToken},
		"client_id":     {app.ClientID},
		"client_secret": {app.ClientSecret},
	})
	if err != nil {
		return oauth.Grant{}, err
	}
	fresh := oauth.Grant{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, Expiry: out.expiry(), Account: g.Account}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = g.RefreshToken
	}
	return fresh, nil
}

// Probe asks Google whether it still honors the grant by renewing it, and
// drops the result. Google's refresh tokens don't rotate, so nothing is
// lost.
func (c *Client) Probe(ctx context.Context, app oauth.App, g oauth.Grant) error {
	_, err := c.Refresh(ctx, app, g)
	return err
}

// userKeyFile is an authorized_user key, as Google's own tools write one.
type userKeyFile struct {
	Type         string `json:"type"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
	ProjectID    string `json:"project_id"`
	QuotaProject string `json:"quota_project_id"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`
}

// Handout is the authorized_user key a deployment's session renews itself
// from: the app, the grant's refresh token, the connection's project and
// the person's email. The project is also the one their API calls bill
// to, not the sign-in app's. Its errors never carry a secret.
func (c *Client) Handout(app oauth.App, g oauth.Grant, settings map[string]string) ([]byte, error) {
	project, email := settings["project_id"], g.Account["email"]
	switch {
	case g.RefreshToken == "":
		return nil, errors.New("the Google sign-in has no refresh token; sign in again")
	case email == "":
		return nil, errors.New("the Google sign-in doesn't say who signed in; sign in again")
	}
	if err := CheckProjectID(project); err != nil {
		return nil, err
	}
	key := userKeyFile{Type: authorizedUser, ClientID: app.ClientID, ClientSecret: app.ClientSecret, RefreshToken: g.RefreshToken,
		ProjectID: project, QuotaProject: project, ClientEmail: email, TokenURI: c.tokenURL()}
	raw, err := json.Marshal(key) // #nosec G117 -- the handout is a credential for a session, never a response.
	if err != nil {
		return nil, errors.New("encode the Google sign-in key")
	}
	return raw, nil
}

// maxErrorCode bounds the error code a token endpoint's answer is quoted by.
const maxErrorCode = 64

// tokenAnswer is what Google's token endpoint answers.
type tokenAnswer struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	IDToken      string `json:"id_token"`
}

func (t tokenAnswer) expiry() time.Time {
	return time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
}

func (c *Client) token(ctx context.Context, form url.Values) (tokenAnswer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return tokenAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := c.base.Do(req)
	if err != nil {
		return tokenAnswer{}, fmt.Errorf("google: %w", err)
	}
	defer res.Body.Close() // read-only; errors surface through ReadAll
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return tokenAnswer{}, fmt.Errorf("google: read response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) != nil {
			e.Error = "" // a body that isn't JSON says nothing
		}
		if res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusUnauthorized {
			switch e.Error {
			case "invalid_grant":
				return tokenAnswer{}, fmt.Errorf("%w: google answered %d: %s", oauth.ErrRefused, res.StatusCode, e.Error)
			case "invalid_client", "unauthorized_client":
				return tokenAnswer{}, fmt.Errorf("%w: google answered %d: %s", oauth.ErrAppRejected, res.StatusCode, e.Error)
			}
		}
		// Only the error code is kept, never the raw body, and only so much
		// of it.
		msg := e.Error
		if msg == "" {
			msg = "no reason given"
		} else if len(msg) > maxErrorCode {
			msg = msg[:maxErrorCode]
		}
		return tokenAnswer{}, fmt.Errorf("google answered %d from its token endpoint: %s", res.StatusCode, msg)
	}
	var out tokenAnswer
	if err := json.Unmarshal(data, &out); err != nil {
		return tokenAnswer{}, fmt.Errorf("google: decode token response: %w", err)
	}
	if out.AccessToken == "" {
		return tokenAnswer{}, errors.New("google answered its token request without an access token")
	}
	return out, nil
}
