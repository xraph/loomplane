package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

// ErrBitbucketRefused means Bitbucket won't honour the grant any more: the
// code is spent, or the refresh token was revoked or has lapsed. Nothing
// short of signing in again fixes it.
var ErrBitbucketRefused = errors.New("Bitbucket refused the sign-in; connect the workspace again")

// ErrNotAMember means the Bitbucket account that signed in isn't in the
// workspace it was asked to connect.
var ErrNotAMember = errors.New("your Bitbucket account isn't a member of that workspace")

// bitbucketSlug is what a workspace slug can look like. Checking it keeps a
// typed slug from rewriting the filter or the path it's put into.
var bitbucketSlug = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

// bitbucketUUID is what a workspace UUID looks like, braces included. It's
// checked for the same reason as the slug.
var bitbucketUUID = regexp.MustCompile(`^\{[0-9A-Fa-f-]{1,64}\}$`)

// BitbucketGrant is what Bitbucket hands back for a sign-in. Both tokens are
// secrets, so every way of printing one is redacted.
type BitbucketGrant struct {
	AccessToken  string
	RefreshToken string
	// Expires is when AccessToken stops working.
	Expires time.Time
}

// String hides the tokens, so a grant printed by mistake shows none.
func (BitbucketGrant) String() string { return "BitbucketGrant{redacted}" }

// GoString covers %#v, which skips String.
func (BitbucketGrant) GoString() string { return "BitbucketGrant{redacted}" }

// LogValue covers structured logging, which would otherwise walk the fields.
func (BitbucketGrant) LogValue() slog.Value { return slog.StringValue("BitbucketGrant{redacted}") }

// Format covers every verb, %d and %x included, which would otherwise
// print the fields without asking String.
func (BitbucketGrant) Format(f fmt.State, _ rune) {
	if _, err := io.WriteString(f, "BitbucketGrant{redacted}"); err != nil {
		return // fmt has nowhere to report it either
	}
}

// MarshalJSON keeps the tokens out of JSON. The refresh token goes to Vault
// in a struct of its own, never as a marshalled grant.
func (BitbucketGrant) MarshalJSON() ([]byte, error) { return []byte(`"BitbucketGrant{redacted}"`), nil }

// BitbucketUser is the account that signed in.
type BitbucketUser struct {
	UUID string
	Name string
}

// BitbucketWorkspace is a workspace the signed-in account belongs to.
type BitbucketWorkspace struct {
	UUID      string
	Slug      string
	Name      string
	AvatarURL string
}

// BitbucketRepository is one repository in a workspace.
type BitbucketRepository struct {
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
}

// BitbucketOAuth is the client for the OAuth 2.0 consumer an operator
// registers with Bitbucket Cloud. It signs people in and reads on their
// behalf, with tokens it never keeps.
type BitbucketOAuth struct {
	clientID     string
	clientSecret string
	webURL       string
	apiURL       string

	// HTTP makes every call. The token endpoint's calls go through a copy
	// that never follows a redirect.
	HTTP *http.Client
	// api shares the public reader's request code and its status mapping.
	api *Bitbucket
}

// NewBitbucketOAuth returns a client for the consumer in cfg. Empty URLs mean
// bitbucket.org's.
func NewBitbucketOAuth(cfg config.BitbucketConfig) *BitbucketOAuth {
	api := NewBitbucket(cfg.APIURL, cfg.WebURL)
	return &BitbucketOAuth{
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		webURL:       api.WebURL,
		apiURL:       api.APIURL,
		HTTP:         api.HTTP,
		api:          api,
	}
}

// AuthorizeURL is where to send someone to approve the consumer. state comes
// back unchanged on the callback.
func (o *BitbucketOAuth) AuthorizeURL(state string) string {
	q := url.Values{"client_id": {o.clientID}, "response_type": {"code"}, "state": {state}}
	return o.webURL + "/site/oauth2/authorize?" + q.Encode()
}

// Exchange trades the code from the callback for a grant.
func (o *BitbucketOAuth) Exchange(ctx context.Context, code string) (BitbucketGrant, error) {
	return o.token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}}, "")
}

// Refresh mints a new access token. Bitbucket may or may not rotate the
// refresh token: the grant carries the new one when it does, the old one
// otherwise, so a caller always stores what comes back.
func (o *BitbucketOAuth) Refresh(ctx context.Context, refreshToken string) (BitbucketGrant, error) {
	return o.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}, refreshToken)
}

// token posts to Bitbucket's token endpoint with the consumer's key and
// secret as basic auth. The response body can hold tokens, so no error
// repeats any of it. keep is the refresh token to fall back on.
func (o *BitbucketOAuth) token(ctx context.Context, form url.Values, keep string) (BitbucketGrant, error) {
	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, o.webURL+"/site/oauth2/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return BitbucketGrant{}, errors.New("build Bitbucket token request failed")
	}
	req.SetBasicAuth(o.clientID, o.clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "loomplane")
	resp, err := o.tokenClient().Do(req)
	if err != nil {
		return BitbucketGrant{}, fmt.Errorf("call Bitbucket: %w", err)
	}
	defer resp.Body.Close() // read-only body
	body := io.LimitReader(resp.Body, maxJSONBytes)
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		// The redirect wasn't followed, and neither its body nor where it
		// pointed goes into the error.
		return BitbucketGrant{}, fmt.Errorf("Bitbucket answered the token request with a redirect (%d), which Loomplane doesn't follow", resp.StatusCode)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return BitbucketGrant{}, ErrBitbucketRefused
	case http.StatusTooManyRequests:
		return BitbucketGrant{}, ErrRateLimited
	case http.StatusBadRequest:
		// Only the error code is read, and only to tell a spent grant
		// from a malformed request.
		var e struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(body).Decode(&e) == nil && e.Error == "invalid_grant" {
			return BitbucketGrant{}, ErrBitbucketRefused
		}
		return BitbucketGrant{}, fmt.Errorf("Bitbucket answered %s to the token request", resp.Status)
	default:
		return BitbucketGrant{}, fmt.Errorf("Bitbucket answered %s to the token request", resp.Status)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return BitbucketGrant{}, errors.New("decode Bitbucket token response failed")
	}
	if out.AccessToken == "" || out.ExpiresIn <= 0 {
		return BitbucketGrant{}, errors.New("the token response had no access token or expiry")
	}
	if out.RefreshToken == "" {
		out.RefreshToken = keep
	}
	return BitbucketGrant{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		Expires:      time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
	}, nil
}

// tokenClient is HTTP with redirects turned off. Go resends a POST's body
// on a 307 or 308, and the body holds the code or the refresh token, so
// the token endpoint's answer is taken as it comes.
func (o *BitbucketOAuth) tokenClient() *http.Client {
	c := http.Client{}
	if o.HTTP != nil {
		c = *o.HTTP
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// CurrentUser says who the token belongs to (GET /2.0/user).
func (o *BitbucketOAuth) CurrentUser(ctx context.Context, token string) (BitbucketUser, error) {
	var out struct {
		UUID        string `json:"uuid"`
		DisplayName string `json:"display_name"`
	}
	if err := o.api.getJSON(ctx, o.apiURL+"/user", token, &out); err != nil {
		return BitbucketUser{}, normalize(err)
	}
	return BitbucketUser{UUID: out.UUID, Name: out.DisplayName}, nil
}

// Membership returns the workspace with that slug if the token's account
// belongs to it, and ErrNotAMember if not.
//
// It asks "List workspaces for the current user", GET /2.0/user/workspaces,
// from Bitbucket Cloud's REST API (the Workspaces group, which answers with
// workspace_access objects and takes the q filter). The older
// GET /2.0/workspaces is deprecated, and /2.0/user/permissions/workspaces
// lists memberships with their permission level, which is more than we
// need. The slug is also compared on our side, so a filter the server
// ignored can't make anyone a member.
func (o *BitbucketOAuth) Membership(ctx context.Context, token, slug string) (BitbucketWorkspace, error) {
	if !bitbucketSlug.MatchString(slug) {
		return BitbucketWorkspace{}, ErrNotAMember
	}
	return o.findWorkspace(ctx, token, `workspace.slug="`+slug+`"`, func(w BitbucketWorkspace) bool {
		return strings.EqualFold(w.Slug, slug)
	})
}

// Workspace returns the workspace with that UUID if the token's account
// belongs to it, and ErrNotAMember if not. It's how a renamed workspace's
// new slug is found: the UUID doesn't change. It asks the same endpoint as
// Membership, filtered by UUID and checked on our side.
func (o *BitbucketOAuth) Workspace(ctx context.Context, token, uuid string) (BitbucketWorkspace, error) {
	if !bitbucketUUID.MatchString(uuid) {
		return BitbucketWorkspace{}, ErrNotAMember
	}
	return o.findWorkspace(ctx, token, `workspace.uuid="`+uuid+`"`, func(w BitbucketWorkspace) bool {
		return strings.EqualFold(w.UUID, uuid)
	})
}

// findWorkspace pages through the token's workspaces, filtered by q, for
// the first that match accepts. Bitbucket may ignore the filter, so it
// reads up to maxPages pages of 100 and decides on our side.
func (o *BitbucketOAuth) findWorkspace(ctx context.Context, token, q string, match func(BitbucketWorkspace) bool) (BitbucketWorkspace, error) {
	next := o.apiURL + "/user/workspaces?" + url.Values{"q": {q}, "pagelen": {strconv.Itoa(perPage)}}.Encode()
	for page := 1; page <= maxPages && next != ""; page++ {
		var out struct {
			Values []struct {
				Workspace struct {
					UUID  string `json:"uuid"`
					Slug  string `json:"slug"`
					Name  string `json:"name"`
					Links struct {
						Avatar struct {
							Href string `json:"href"`
						} `json:"avatar"`
					} `json:"links"`
				} `json:"workspace"`
			} `json:"values"`
			Next string `json:"next"`
		}
		if err := o.api.getJSON(ctx, next, token, &out); err != nil {
			return BitbucketWorkspace{}, normalize(err)
		}
		for _, v := range out.Values {
			w := v.Workspace
			ws := BitbucketWorkspace{UUID: w.UUID, Slug: w.Slug, Name: w.Name, AvatarURL: w.Links.Avatar.Href}
			if match(ws) {
				return ws, nil
			}
		}
		next = out.Next
		if next != "" && !o.sameAPI(next) {
			// The token goes wherever we send it, as in Repositories.
			return BitbucketWorkspace{}, errors.New("the next page pointed away from the Bitbucket API")
		}
	}
	return BitbucketWorkspace{}, ErrNotAMember
}

// Repositories lists the repositories in a workspace that the token's
// account is a member of (GET /2.0/repositories/{workspace}?role=member),
// following next links for up to maxPages pages.
func (o *BitbucketOAuth) Repositories(ctx context.Context, token, workspaceSlug string) ([]BitbucketRepository, error) {
	if !bitbucketSlug.MatchString(workspaceSlug) {
		return nil, fmt.Errorf("%w: %q isn't a valid workspace", ErrNotFound, workspaceSlug)
	}
	next := fmt.Sprintf("%s/repositories/%s?role=member&pagelen=%d", o.apiURL, url.PathEscape(workspaceSlug), perPage)
	var repos []BitbucketRepository
	for page := 1; page <= maxPages && next != ""; page++ {
		var body struct {
			Values []struct {
				FullName   string `json:"full_name"`
				IsPrivate  bool   `json:"is_private"`
				MainBranch struct {
					Name string `json:"name"`
				} `json:"mainbranch"`
			} `json:"values"`
			Next string `json:"next"`
		}
		if err := o.api.getJSON(ctx, next, token, &body); err != nil {
			return nil, normalize(err)
		}
		for _, r := range body.Values {
			repos = append(repos, BitbucketRepository{FullName: r.FullName, Private: r.IsPrivate, DefaultBranch: r.MainBranch.Name})
		}
		next = body.Next
		if next != "" && !o.sameAPI(next) {
			// The token goes wherever we send it, so a link to anywhere
			// but Bitbucket's API is a refusal, not a page.
			return nil, errors.New("the next page pointed away from the Bitbucket API")
		}
	}
	return repos, nil
}

// sameAPI reports whether u is on the API's scheme and host.
func (o *BitbucketOAuth) sameAPI(u string) bool {
	a, err := url.Parse(o.apiURL)
	if err != nil {
		return false
	}
	b, err := url.Parse(u)
	return err == nil && b.Scheme == a.Scheme && b.Host == a.Host
}
