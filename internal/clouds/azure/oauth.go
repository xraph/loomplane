package azure

import (
	"context"
	"encoding/base64"
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

// azureUser is the type of a key that signs in as a person.
const azureUser = "azure_user"

// signInScope is what an Azure sign-in asks for: who the person is, a
// refresh token, and Resource Manager as them. Their role assignments
// narrow it to what they may do in the subscription.
const signInScope = "openid profile offline_access " + DefaultManagement + "/user_impersonation"

// refreshScope is what a renewal asks for: a Resource Manager token, and
// a refresh token for next time.
const refreshScope = DefaultManagement + "/.default offline_access"

const (
	// maxUserKey bounds a sign-in key. Entra ID's refresh tokens are
	// opaque and can run to a few kilobytes.
	maxUserKey = 16 << 10
	// maxErrorCode bounds the error code a token endpoint's answer is
	// quoted by.
	maxErrorCode = 64
)

// userKey is the key a sign-in hands a session: the sign-in app, the
// person's refresh token, and who they are. Where it deploys comes from
// the connection's settings.
type userKey struct {
	Type         string `json:"type"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
	Username     string `json:"username"`
}

// parseUserKey reads an azure_user key. Its errors never carry a secret.
func parseUserKey(data []byte) (Credential, error) {
	if len(data) > maxUserKey {
		return Credential{}, fmt.Errorf("%w: the sign-in key is larger than %d KiB", ErrInvalid, maxUserKey>>10)
	}
	var k userKey
	if err := json.Unmarshal(data, &k); err != nil {
		return Credential{}, fmt.Errorf("%w: the sign-in key isn't JSON", ErrInvalid)
	}
	c := Credential{
		ClientID:     strings.ToLower(strings.TrimSpace(k.ClientID)),
		Secret:       strings.TrimSpace(k.ClientSecret),
		RefreshToken: strings.TrimSpace(k.RefreshToken),
		Username:     strings.TrimSpace(k.Username),
	}
	switch {
	case k.Type != azureUser:
		return Credential{}, fmt.Errorf("%w: the key's type is %.32q, not %s", ErrInvalid, k.Type, azureUser)
	case !guidRE.MatchString(c.ClientID):
		return Credential{}, fmt.Errorf("%w: the sign-in key's client_id isn't an application (client) ID", ErrInvalid)
	case c.Secret == "" || c.RefreshToken == "" || c.Username == "":
		return Credential{}, fmt.Errorf("%w: the sign-in key has no client secret, refresh token or username", ErrInvalid)
	}
	return c, nil
}

// endpoint is one of tenant's sign-in endpoints, at Microsoft or a test's
// fake.
func (c *Client) endpoint(tenant, name string) string {
	return c.login + "/" + url.PathEscape(tenant) + "/oauth2/v2.0/" + name
}

// tokenURL is tenant's token endpoint.
func (c *Client) tokenURL(tenant string) string { return c.endpoint(tenant, "token") }

// AuthorizeURL is the page to send someone to, to sign in to the
// settings' tenant and let Loomplane use Azure as them. Microsoft sends
// them back to the redirect with a code and the state in the query.
func (c *Client) AuthorizeURL(app oauth.App, r oauth.AuthorizeRequest) string {
	q := url.Values{
		"client_id":             {app.ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {r.Redirect},
		"response_mode":         {"query"},
		"scope":                 {signInScope},
		"state":                 {r.State},
		"code_challenge":        {r.Challenge},
		"code_challenge_method": {"S256"},
	}
	return c.endpoint(strings.TrimSpace(r.Settings["tenant_id"]), "authorize") + "?" + q.Encode()
}

// Exchange trades the code Microsoft sent back, and the PKCE verifier its
// challenge came from, for a grant. The grant's account says who signed
// in and where the sign-in deploys.
func (c *Client) Exchange(ctx context.Context, app oauth.App, r oauth.CodeRequest) (oauth.Grant, error) {
	tenant := strings.TrimSpace(r.Settings["tenant_id"])
	out, err := c.token(ctx, tenant, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {r.Code},
		"code_verifier": {r.Verifier},
		"client_id":     {app.ClientID},
		"client_secret": {app.ClientSecret},
		"redirect_uri":  {r.Redirect},
		"scope":         {signInScope},
	})
	if err != nil {
		return oauth.Grant{}, err
	}
	if out.RefreshToken == "" {
		return oauth.Grant{}, fmt.Errorf("%w: Azure sent no refresh token; sign in again", oauth.ErrRefused)
	}
	who, err := signedIn(out.IDToken)
	if err != nil {
		return oauth.Grant{}, err
	}
	return oauth.Grant{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		Expiry:       out.expiry(),
		Account: map[string]string{
			"subject": who.ObjectID, "email": who.Username, "tenant_id": tenant,
			"subscription_id": r.Settings["subscription_id"], "resource_group": r.Settings["resource_group"],
		},
	}, nil
}

// person is who an ID token says signed in.
type person struct {
	ObjectID string `json:"oid"`
	Username string `json:"preferred_username"`
}

// signedIn reads who an ID token is for. Its signature isn't checked: it
// came straight from Microsoft's token endpoint over TLS, which OpenID
// Connect allows in place of checking it.
func signedIn(idToken string) (who person, err error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return who, errors.New("azure answered its token request without an ID token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil || json.Unmarshal(payload, &who) != nil {
		return who, errors.New("azure answered its token request with an ID token that can't be read")
	}
	if who.ObjectID == "" || who.Username == "" {
		return who, errors.New("azure's ID token doesn't say who signed in")
	}
	return who, nil
}

// Refresh trades the grant's refresh token for a new access token, at
// the grant's own tenant. Azure may rotate the refresh token; the old one
// is kept when no new one comes back.
func (c *Client) Refresh(ctx context.Context, app oauth.App, g oauth.Grant) (oauth.Grant, error) {
	out, err := c.token(ctx, strings.TrimSpace(g.Account["tenant_id"]), url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {g.RefreshToken},
		"client_id":     {app.ClientID},
		"client_secret": {app.ClientSecret},
		"scope":         {refreshScope},
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

// Probe asks Azure whether it still honors the grant by renewing it, and
// drops the result. A refresh token Azure rotates stays valid until it
// expires, so the one held still works.
func (c *Client) Probe(ctx context.Context, app oauth.App, g oauth.Grant) error {
	_, err := c.Refresh(ctx, app, g)
	return err
}

// Handout is the azure_user key a deployment's session renews itself
// from: the app, the grant's refresh token and the person's username.
// The session deploys where the connection's settings say. Its errors
// never carry a secret.
func (c *Client) Handout(app oauth.App, g oauth.Grant, settings map[string]string) ([]byte, error) {
	switch {
	case g.RefreshToken == "":
		return nil, errors.New("the Azure sign-in has no refresh token; sign in again")
	case g.Account["email"] == "":
		return nil, errors.New("the Azure sign-in doesn't say who signed in; sign in again")
	}
	if _, _, _, err := CheckPlacement(settings); err != nil {
		return nil, err
	}
	key := userKey{Type: azureUser, ClientID: app.ClientID, ClientSecret: app.ClientSecret, RefreshToken: g.RefreshToken, Username: g.Account["email"]}
	raw, err := json.Marshal(key) // #nosec G117 -- the handout is a credential for a session, never a response.
	if err != nil {
		return nil, errors.New("encode the Azure sign-in key")
	}
	return raw, nil
}

// tokenAnswer is what Entra ID's token endpoint answers.
type tokenAnswer struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	IDToken      string `json:"id_token"`
}

func (t tokenAnswer) expiry() time.Time {
	return time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
}

// errorCode is how an OAuth error code is quoted: only so much of it,
// and something when there's none.
func errorCode(code string) string {
	switch {
	case code == "":
		return "no reason given"
	case len(code) > maxErrorCode:
		return code[:maxErrorCode]
	}
	return code
}

func (c *Client) token(ctx context.Context, tenant string, form url.Values) (tokenAnswer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL(tenant), strings.NewReader(form.Encode()))
	if err != nil {
		return tokenAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return tokenAnswer{}, fmt.Errorf("azure sign-in: %w", err)
	}
	defer res.Body.Close() // read-only; errors surface through ReadAll
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return tokenAnswer{}, fmt.Errorf("azure sign-in: read response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		// Only the error code is kept, never the body: an AADSTS
		// description runs on with a trace and correlation IDs.
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) != nil {
			e.Error = "" // a body that isn't JSON says nothing
		}
		if res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusUnauthorized {
			switch e.Error {
			case "invalid_grant", "interaction_required", "consent_required":
				return tokenAnswer{}, fmt.Errorf("%w: azure answered %d: %s", oauth.ErrRefused, res.StatusCode, e.Error)
			case "invalid_client", "unauthorized_client":
				return tokenAnswer{}, fmt.Errorf("%w: azure answered %d: %s", oauth.ErrAppRejected, res.StatusCode, e.Error)
			}
		}
		return tokenAnswer{}, fmt.Errorf("azure answered %d from its token endpoint: %s", res.StatusCode, errorCode(e.Error))
	}
	var out tokenAnswer
	if err := json.Unmarshal(data, &out); err != nil {
		return tokenAnswer{}, errors.New("azure sign-in: its token answer isn't JSON")
	}
	if out.AccessToken == "" {
		return tokenAnswer{}, errors.New("azure answered its token request without an access token")
	}
	return out, nil
}
