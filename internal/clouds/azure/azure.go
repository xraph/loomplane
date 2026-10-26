// Package azure talks to Microsoft Azure for Loomplane: it signs in as a
// service principal, checks what the principal may do in a subscription,
// builds images with ACR Tasks and runs them on Azure Container Apps.
//
// It uses Azure Resource Manager's REST API over net/http rather than the
// Azure SDK for Go, which would bring in dozens of modules for a handful
// of calls.
package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

// Azure's own hosts.
const (
	DefaultLogin      = "https://login.microsoftonline.com"
	DefaultManagement = "https://management.azure.com"
	// DefaultResourceGroup is where Loomplane makes everything when a
	// connection names no resource group.
	DefaultResourceGroup = "loomplane"
)

const (
	requestTimeout = 60 * time.Second
	maxResponse    = 8 << 20
	maxSecret      = 1024
	// tokenSkew refreshes a token this long before Azure says it expires.
	tokenSkew = 2 * time.Minute
)

// Errors.
var (
	// ErrInvalid is a credential or setting that can't be right, found
	// without asking Azure.
	ErrInvalid = errors.New("that isn't an Azure service principal or sign-in")
	// ErrRejected is a principal Azure refused: a wrong secret, an
	// expired one, or one with no access to the subscription. Or a
	// sign-in whose refresh token Azure no longer takes.
	ErrRejected = errors.New("azure refused the service principal or sign-in")
)

var (
	guidRE   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	domainRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}(\.[A-Za-z0-9][A-Za-z0-9-]{0,62})+$`)
	// groupRE is a resource group name: letters, digits, hyphens,
	// underscores, periods and parentheses, not ending in a period.
	groupRE = regexp.MustCompile(`^[A-Za-z0-9_().-]{0,89}[A-Za-z0-9_()-]$`)
)

// Credential is a service principal, or a person signed in through
// Loomplane's sign-in app, and the subscription it deploys to. Secret
// and RefreshToken are secrets; the rest is safe to show.
type Credential struct {
	TenantID string
	// ClientID is the principal's application, or the sign-in app's.
	ClientID       string
	Secret         string
	SubscriptionID string
	ResourceGroup  string
	// RefreshToken is set for a signed-in person, whose session renews
	// itself from it as Username.
	RefreshToken string
	Username     string
}

// CheckPlacement checks where a connection deploys: tenant_id, a
// directory ID or domain; subscription_id; and, optionally,
// resource_group, which defaults to DefaultResourceGroup. GUIDs come back
// lower-cased. It asks Azure nothing.
func CheckPlacement(settings map[string]string) (tenant, subscription, group string, err error) {
	tenant = strings.TrimSpace(settings["tenant_id"])
	subscription = strings.ToLower(strings.TrimSpace(settings["subscription_id"]))
	group = strings.TrimSpace(settings["resource_group"])
	if group == "" {
		group = DefaultResourceGroup
	}
	switch {
	case !guidRE.MatchString(tenant) && !domainRE.MatchString(tenant):
		return "", "", "", fmt.Errorf("%w: tenant_id %q is neither a directory ID nor a domain such as contoso.onmicrosoft.com", ErrInvalid, tenant)
	case !guidRE.MatchString(subscription):
		return "", "", "", fmt.Errorf("%w: subscription_id %q isn't a subscription ID; they look like 00000000-0000-0000-0000-000000000000", ErrInvalid, subscription)
	case !groupRE.MatchString(group):
		return "", "", "", fmt.Errorf("%w: %q isn't a resource group name", ErrInvalid, group)
	}
	if guidRE.MatchString(tenant) {
		tenant = strings.ToLower(tenant)
	}
	return tenant, subscription, group, nil
}

// ParseCredential checks a client secret and the settings that go with
// it: client_id and the placement CheckPlacement checks. A secret that
// is JSON is the azure_user key a sign-in hands out, which carries its
// own client. It asks Azure nothing.
func ParseCredential(secret []byte, settings map[string]string) (Credential, error) {
	var c Credential
	if trimmed := bytes.TrimSpace(secret); len(trimmed) > 0 && trimmed[0] == '{' {
		user, err := parseUserKey(trimmed)
		if err != nil {
			return c, err
		}
		c = user
	} else {
		c.ClientID = strings.ToLower(strings.TrimSpace(settings["client_id"]))
		c.Secret = strings.TrimSpace(string(secret))
		switch {
		case c.Secret == "" || len(c.Secret) > maxSecret || strings.ContainsAny(c.Secret, " \t\r\n"):
			return c, fmt.Errorf("%w: the client secret is empty, too long, or has spaces in it", ErrInvalid)
		case !guidRE.MatchString(c.ClientID):
			return c, fmt.Errorf("%w: client_id %q isn't an application (client) ID; they look like 00000000-0000-0000-0000-000000000000", ErrInvalid, c.ClientID)
		}
	}
	var err error
	if c.TenantID, c.SubscriptionID, c.ResourceGroup, err = CheckPlacement(settings); err != nil {
		return c, err
	}
	return c, nil
}

// Client calls Azure.
type Client struct {
	login, management string
	http              *http.Client
	// poll is how often a long-running operation is checked.
	poll time.Duration
}

// New returns a Client. An empty endpoint means Azure's own hosts; any
// other is a test's fake, which serves both sign-in and Resource
// Manager, and answers at once, so it's polled often.
func New(endpoint string) *Client {
	c := &Client{login: DefaultLogin, management: DefaultManagement, http: &http.Client{Timeout: requestTimeout}, poll: 10 * time.Second}
	if endpoint = strings.TrimRight(endpoint, "/"); endpoint != "" {
		c.login, c.management, c.poll = endpoint, endpoint, 100*time.Millisecond
	}
	return c
}

// APIError is an error response from Resource Manager.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("azure answered %d (%s): %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("azure answered %d: %s", e.Status, e.Message)
}

// IsForbidden reports whether err is Azure's 403: the principal may not
// do what it asked.
func IsForbidden(err error) bool { return isForbidden(err) }

// IsNotFound reports whether err is Azure's 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// Session is a signed-in service principal or person.
type Session struct {
	c    *Client
	cred Credential

	mu      sync.Mutex
	token   string
	expires time.Time
	// refresh is the refresh token the next renewal spends. Azure may
	// hand back a new one with each token.
	refresh string
}

// Session signs in as cred's service principal, or renews cred's
// sign-in. A secret or refresh token Azure refuses is ErrRejected.
func (c *Client) Session(ctx context.Context, cred Credential) (*Session, error) {
	s := &Session{c: c, cred: cred, refresh: cred.RefreshToken}
	if _, err := s.bearer(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Credential is the session's credential.
func (s *Session) Credential() Credential { return s.cred }

// bearer returns a Resource Manager token, signing in again when the one
// it has is about to expire. A signed-in person's session renews from its
// refresh token, and keeps the one Azure hands back for next time.
func (s *Session) bearer(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && time.Now().Before(s.expires) {
		return s.token, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {s.cred.ClientID},
		"client_secret": {s.cred.Secret},
		"scope":         {DefaultManagement + "/.default"},
	}
	if s.refresh != "" {
		form = url.Values{
			"grant_type":    {"refresh_token"},
			"client_id":     {s.cred.ClientID},
			"client_secret": {s.cred.Secret},
			"refresh_token": {s.refresh},
			"scope":         {refreshScope},
		}
	}
	u := s.c.tokenURL(s.cred.TenantID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := s.c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("azure sign-in: %w", err)
	}
	defer res.Body.Close() // read-only; errors surface through ReadAll
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return "", fmt.Errorf("azure sign-in: read response: %w", err)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	_ = json.Unmarshal(data, &out) //nolint:errcheck // an unreadable answer is judged by its status
	switch {
	case s.refresh != "" && res.StatusCode >= 300:
		// Only the error code is kept: the description of a refused
		// refresh token is long and says nothing a person can act on. A
		// refused app is told apart, as a renewal's is, since signing in
		// again with it won't help.
		if res.StatusCode != http.StatusBadRequest && res.StatusCode != http.StatusUnauthorized {
			return "", fmt.Errorf("azure sign-in answered %d: %s", res.StatusCode, errorCode(out.Error))
		}
		if out.Error == "invalid_client" || out.Error == "unauthorized_client" {
			return "", fmt.Errorf("%w: %w (%s)", ErrRejected, oauth.ErrAppRejected, errorCode(out.Error))
		}
		return "", fmt.Errorf("%w: %w (%s); sign in again", ErrRejected, oauth.ErrRefused, errorCode(out.Error))
	case res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusUnauthorized:
		// Microsoft Entra ID answers 400 for a wrong secret, an unknown
		// application or an unknown tenant, with an AADSTS code that
		// says which.
		msg := firstLine(out.Description)
		if msg == "" {
			msg = out.Error
		}
		return "", fmt.Errorf("%w: %s", ErrRejected, msg)
	case res.StatusCode >= 300:
		return "", fmt.Errorf("azure sign-in answered %d: %s", res.StatusCode, firstLine(string(data)))
	case out.AccessToken == "":
		return "", errors.New("azure sign-in returned no token")
	}
	s.token = out.AccessToken
	s.expires = time.Now().Add(time.Duration(out.ExpiresIn)*time.Second - tokenSkew)
	if s.refresh != "" && out.RefreshToken != "" {
		s.refresh = out.RefreshToken
	}
	return s.token, nil
}

// firstLine is the first line of an Entra ID error description, which
// goes on with a trace and correlation IDs.
func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\r\n")
	s, _, _ = strings.Cut(s, "\n")
	return strings.TrimSpace(s)
}

// do sends a JSON request to Resource Manager at path, with api-version,
// and decodes the response into out. It returns the response's headers,
// which long-running operations point on with.
func (s *Session) do(ctx context.Context, method, path, version string, in, out any) (http.Header, error) {
	u := path
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		u = s.c.management + path
	}
	if version != "" {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + "api-version=" + url.QueryEscape(version)
	}
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	token, err := s.bearer(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := s.c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("azure: %w", err)
	}
	defer res.Body.Close() // read-only; errors surface through ReadAll
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return nil, fmt.Errorf("azure: read response: %w", err)
	}
	if res.StatusCode >= 300 {
		ae := &APIError{Status: res.StatusCode, Message: strings.TrimSpace(string(data))}
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			ae.Code, ae.Message = e.Error.Code, e.Error.Message
		}
		if res.StatusCode == http.StatusUnauthorized {
			return res.Header, fmt.Errorf("%w: %w", ErrRejected, ae)
		}
		return res.Header, ae
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return res.Header, nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return res.Header, fmt.Errorf("azure: decode response: %w", err)
	}
	return res.Header, nil
}

func (s *Session) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.c.poll):
		return nil
	}
}

// subscription is the path of the session's subscription.
func (s *Session) subscription() string { return "/subscriptions/" + s.cred.SubscriptionID }

// group is the path of the session's resource group.
func (s *Session) group() string {
	return s.subscription() + "/resourceGroups/" + url.PathEscape(s.cred.ResourceGroup)
}
