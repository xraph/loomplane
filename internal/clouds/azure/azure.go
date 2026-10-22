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
	ErrInvalid = errors.New("that isn't an Azure service principal")
	// ErrRejected is a principal Azure refused: a wrong secret, an
	// expired one, or one with no access to the subscription.
	ErrRejected = errors.New("azure refused the service principal")
)

var (
	guidRE   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	domainRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}(\.[A-Za-z0-9][A-Za-z0-9-]{0,62})+$`)
	// groupRE is a resource group name: letters, digits, hyphens,
	// underscores, periods and parentheses, not ending in a period.
	groupRE = regexp.MustCompile(`^[A-Za-z0-9_().-]{0,89}[A-Za-z0-9_()-]$`)
)

// Credential is a service principal and the subscription it deploys to.
// Only Secret is a secret; the rest is safe to show.
type Credential struct {
	TenantID       string
	ClientID       string
	Secret         string
	SubscriptionID string
	ResourceGroup  string
}

// ParseCredential checks a client secret and the settings that go with
// it: tenant_id, client_id, subscription_id and, optionally,
// resource_group. It asks Azure nothing.
func ParseCredential(secret []byte, settings map[string]string) (Credential, error) {
	c := Credential{
		TenantID:       strings.TrimSpace(settings["tenant_id"]),
		ClientID:       strings.ToLower(strings.TrimSpace(settings["client_id"])),
		Secret:         strings.TrimSpace(string(secret)),
		SubscriptionID: strings.ToLower(strings.TrimSpace(settings["subscription_id"])),
		ResourceGroup:  strings.TrimSpace(settings["resource_group"]),
	}
	if c.ResourceGroup == "" {
		c.ResourceGroup = DefaultResourceGroup
	}
	switch {
	case c.Secret == "" || len(c.Secret) > maxSecret || strings.ContainsAny(c.Secret, " \t\r\n"):
		return c, fmt.Errorf("%w: the client secret is empty, too long, or has spaces in it", ErrInvalid)
	case !guidRE.MatchString(c.TenantID) && !domainRE.MatchString(c.TenantID):
		return c, fmt.Errorf("%w: tenant_id %q is neither a directory ID nor a domain such as contoso.onmicrosoft.com", ErrInvalid, c.TenantID)
	case !guidRE.MatchString(c.ClientID):
		return c, fmt.Errorf("%w: client_id %q isn't an application (client) ID; they look like 00000000-0000-0000-0000-000000000000", ErrInvalid, c.ClientID)
	case !guidRE.MatchString(c.SubscriptionID):
		return c, fmt.Errorf("%w: subscription_id %q isn't a subscription ID; they look like 00000000-0000-0000-0000-000000000000", ErrInvalid, c.SubscriptionID)
	case !groupRE.MatchString(c.ResourceGroup):
		return c, fmt.Errorf("%w: %q isn't a resource group name", ErrInvalid, c.ResourceGroup)
	}
	if guidRE.MatchString(c.TenantID) {
		c.TenantID = strings.ToLower(c.TenantID)
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

// IsNotFound reports whether err is Azure's 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// Session is a signed-in service principal.
type Session struct {
	c    *Client
	cred Credential

	mu      sync.Mutex
	token   string
	expires time.Time
}

// Session signs in as cred's service principal. A secret Azure refuses
// is ErrRejected.
func (c *Client) Session(ctx context.Context, cred Credential) (*Session, error) {
	s := &Session{c: c, cred: cred}
	if _, err := s.bearer(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Credential is the session's credential.
func (s *Session) Credential() Credential { return s.cred }

// bearer returns a Resource Manager token, signing in again when the one
// it has is about to expire.
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
	u := s.c.login + "/" + url.PathEscape(s.cred.TenantID) + "/oauth2/v2.0/token"
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
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(data, &out) //nolint:errcheck // an unreadable answer is judged by its status
	switch {
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
