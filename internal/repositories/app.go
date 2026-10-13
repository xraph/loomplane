package repositories

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Errors connecting a GitHub App installation.
var (
	ErrNotYourInstallation = errors.New("you don't have access to that GitHub App installation")
	ErrAppNotConfigured    = errors.New("this Loomplane has no GitHub App configured, so it reads public repositories only")
)

const (
	// jwtLifetime is GitHub's maximum for an App JWT, less a minute.
	jwtLifetime = 9 * time.Minute
	// jwtClockSkew backdates a JWT's issue time, as GitHub recommends.
	jwtClockSkew = time.Minute
	// maxPages caps paging through installations or repositories.
	maxPages = 10
	perPage  = 100
)

// AppConfig is a GitHub App's settings.
type AppConfig struct {
	ID           int64
	Slug         string
	PrivateKey   string // PEM
	ClientID     string
	ClientSecret string
	APIURL       string
	WebURL       string
}

// App is a GitHub App: Loomplane's way into private repositories. It
// mints short-lived tokens for one repository at a time and keeps none.
type App struct {
	id           int64
	slug         string
	clientID     string
	clientSecret string
	key          *rsa.PrivateKey
	apiURL       string
	webURL       string
	http         *http.Client
	now          func() time.Time
}

// NewApp returns the App, or an error when its key doesn't parse.
func NewApp(cfg AppConfig) (*App, error) {
	key, err := parsePrivateKey(cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &App{
		id: cfg.ID, slug: cfg.Slug, clientID: cfg.ClientID, clientSecret: cfg.ClientSecret, key: key,
		apiURL: strings.TrimRight(cfg.APIURL, "/"), webURL: strings.TrimRight(cfg.WebURL, "/"),
		http: &http.Client{}, now: time.Now,
	}, nil
}

var pemBlock = regexp.MustCompile(`-{5}BEGIN ([A-Z ]+)-{5}(.*?)-{5}END ([A-Z ]+)-{5}`)

// parsePrivateKey reads a PEM RSA key, repairing what environment
// variables do to one: newlines written as \n, or folded into spaces.
func parsePrivateKey(raw string) (*rsa.PrivateKey, error) {
	s := strings.TrimSpace(strings.ReplaceAll(raw, `\n`, "\n"))
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		m := pemBlock.FindStringSubmatch(strings.ReplaceAll(s, "\n", " "))
		if m == nil {
			return nil, errors.New("the GitHub App private key isn't PEM")
		}
		body := strings.Join(strings.Fields(m[2]), "")
		block = &pem.Block{Type: m[1]}
		der, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return nil, errors.New("the GitHub App private key isn't valid PEM")
		}
		block.Bytes = der
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("the GitHub App private key isn't an RSA key")
	}
	rsaKey, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the GitHub App private key isn't an RSA key")
	}
	return rsaKey, nil
}

// InstallURL is where someone installs the App. GitHub sends them back
// with state, which says who started the installation and for which
// organization.
func (a *App) InstallURL(state string) string {
	return a.webURL + "/apps/" + url.PathEscape(a.slug) + "/installations/new?state=" + url.QueryEscape(state)
}

// AccountURLs are an installation's account picture and the page on
// GitHub where its owner chooses which repositories the App can read: an
// organization's settings for an organization, the person's own for a
// user.
func (a *App) AccountURLs(account, accountType string, id int64) (avatar, manage string) {
	installation := "/settings/installations/" + strconv.FormatInt(id, 10)
	if accountType == "Organization" {
		installation = "/organizations/" + url.PathEscape(account) + installation
	}
	return a.webURL + "/" + url.PathEscape(account) + ".png", a.webURL + installation
}

// jwt is the App's own credential, signed with its private key (RS256).
func (a *App) jwt() (string, error) {
	now := a.now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-jwtClockSkew).Unix(),
		"exp": now.Add(jwtLifetime).Unix(),
		"iss": strconv.FormatInt(a.id, 10),
	})
	if err != nil {
		return "", fmt.Errorf("encode JWT claims: %w", err)
	}
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign JWT: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Installation is the App installed on one GitHub account.
type Installation struct {
	ID          int64  `json:"id"`
	Account     string `json:"account"`
	AccountType string `json:"account_type"`
}

// Installation reads one installation, as the App.
func (a *App) Installation(ctx context.Context, id int64) (Installation, error) {
	tok, err := a.jwt()
	if err != nil {
		return Installation{}, err
	}
	var body struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"account"`
	}
	if err := a.call(ctx, http.MethodGet, a.apiURL+"/app/installations/"+strconv.FormatInt(id, 10), tok, nil, &body); err != nil {
		return Installation{}, err
	}
	return Installation{ID: body.ID, Account: body.Account.Login, AccountType: body.Account.Type}, nil
}

// VerifyInstaller checks that the person who just installed the App can
// see installation id. code is the one-time code GitHub's install
// redirect carries when the App asks for user authorization during
// installation. Without this check, anyone could claim someone else's
// installation by its number.
func (a *App) VerifyInstaller(ctx context.Context, code string, id int64) error {
	if code == "" {
		return ErrNotYourInstallation
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	form := url.Values{"client_id": {a.clientID}, "client_secret": {a.clientSecret}, "code": {code}}
	if err := a.call(ctx, http.MethodPost, a.webURL+"/login/oauth/access_token", "", form, &tok); err != nil {
		return err
	}
	if tok.AccessToken == "" {
		return ErrNotYourInstallation
	}
	for page := 1; page <= maxPages; page++ {
		var body struct {
			Installations []struct {
				ID int64 `json:"id"`
			} `json:"installations"`
		}
		u := fmt.Sprintf("%s/user/installations?per_page=%d&page=%d", a.apiURL, perPage, page)
		if err := a.call(ctx, http.MethodGet, u, tok.AccessToken, nil, &body); err != nil {
			return err
		}
		for _, inst := range body.Installations {
			if inst.ID == id {
				return nil
			}
		}
		if len(body.Installations) < perPage {
			break
		}
	}
	return ErrNotYourInstallation
}

// RepositoryToken mints a token that can read one repository's contents,
// for an hour. GitHub refuses if the installation can't see it.
func (a *App) RepositoryToken(ctx context.Context, installationID int64, repo string) (string, error) {
	return a.installationToken(ctx, installationID, map[string]any{
		"repositories": []string{repo},
		"permissions":  map[string]string{"contents": "read"},
	})
}

func (a *App) installationToken(ctx context.Context, installationID int64, scope map[string]any) (string, error) {
	jwt, err := a.jwt()
	if err != nil {
		return "", err
	}
	var body struct {
		Token string `json:"token"`
	}
	u := a.apiURL + "/app/installations/" + strconv.FormatInt(installationID, 10) + "/access_tokens"
	if err := a.call(ctx, http.MethodPost, u, jwt, scope, &body); err != nil {
		return "", err
	}
	if body.Token == "" {
		return "", errors.New("GitHub returned no installation token")
	}
	return body.Token, nil
}

// InstalledRepository is a repository an installation can read.
type InstalledRepository struct {
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	URL           string `json:"url"`
}

// Repositories lists what one installation can read.
func (a *App) Repositories(ctx context.Context, installationID int64) ([]InstalledRepository, error) {
	tok, err := a.installationToken(ctx, installationID, map[string]any{
		"permissions": map[string]string{"metadata": "read"},
	})
	if err != nil {
		return nil, err
	}
	var out []InstalledRepository
	for page := 1; page <= maxPages; page++ {
		var body struct {
			Repositories []struct {
				FullName      string `json:"full_name"`
				Private       bool   `json:"private"`
				DefaultBranch string `json:"default_branch"`
				HTMLURL       string `json:"html_url"`
			} `json:"repositories"`
		}
		u := fmt.Sprintf("%s/installation/repositories?per_page=%d&page=%d", a.apiURL, perPage, page)
		if err := a.call(ctx, http.MethodGet, u, tok, nil, &body); err != nil {
			return nil, err
		}
		for _, r := range body.Repositories {
			out = append(out, InstalledRepository{FullName: r.FullName, Private: r.Private, DefaultBranch: r.DefaultBranch, URL: r.HTMLURL})
		}
		if len(body.Repositories) < perPage {
			break
		}
	}
	return out, nil
}

// call makes one request. body is JSON, or a form when it's url.Values.
func (a *App) call(ctx context.Context, method, u, token string, body, out any) error {
	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var rd io.Reader = http.NoBody
	contentType := ""
	switch b := body.(type) {
	case nil:
	case url.Values:
		rd, contentType = strings.NewReader(b.Encode()), "application/x-www-form-urlencoded"
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return fmt.Errorf("encode GitHub request: %w", err)
		}
		rd, contentType = bytes.NewReader(raw), "application/json"
	}
	req, err := http.NewRequestWithContext(rctx, method, u, rd)
	if err != nil {
		return fmt.Errorf("build GitHub request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "loomplane")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("call GitHub: %w", err)
	}
	defer resp.Body.Close() // read-only body
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0":
		return ErrRateLimited
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden,
		resp.StatusCode == http.StatusUnprocessableEntity:
		return ErrNotFound
	case resp.StatusCode >= http.StatusMultipleChoices:
		return fmt.Errorf("GitHub answered %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBytes)).Decode(out); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}
