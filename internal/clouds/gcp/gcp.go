// Package gcp talks to Google Cloud for Loomplane: it turns a service
// account key, a sign-in, or a keyless handout that impersonates a
// service account, into an authorized client and checks what that
// account may do. Provisioning uses Google's REST APIs. PostgreSQL
// connections use the official Cloud SQL Go Connector for IAM
// authorization and TLS.
package gcp

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	neturl "net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/xraph/loomplane/internal/clouds/keyless"
	"github.com/xraph/loomplane/internal/clouds/oauth"
)

// Scope is the OAuth scope Loomplane asks for. Google's own APIs narrow it
// to what the service account's IAM roles allow.
const Scope = "https://www.googleapis.com/auth/cloud-platform"

const (
	requestTimeout = 30 * time.Second
	maxResponse    = 4 << 20
	maxKey         = 16 << 10
)

// Permissions are what a service account needs for Loomplane to build a
// service with Cloud Build, store its image in Artifact Registry and run
// it on Cloud Run. Each is checked on the project before a connection
// counts as ready.
var Permissions = []string{
	"artifactregistry.repositories.create",
	"artifactregistry.repositories.get",
	"cloudbuild.builds.create",
	"cloudbuild.builds.get",
	"cloudsql.databases.create",
	"cloudsql.instances.create",
	"cloudsql.instances.delete",
	"cloudsql.instances.get",
	"cloudsql.users.create",
	"cloudsql.users.update",
	"iam.serviceAccounts.actAs",
	"resourcemanager.projects.get",
	"run.operations.get",
	"run.services.create",
	"run.services.get",
	"run.services.setIamPolicy",
	"run.services.update",
	"secretmanager.secrets.create",
	"secretmanager.secrets.setIamPolicy",
	"secretmanager.versions.add",
	"redis.instances.create",
	"redis.instances.delete",
	"redis.instances.get",
	"redis.instances.getAuthString",
	"storage.buckets.create",
	"storage.buckets.delete",
	"storage.buckets.get",
	"storage.buckets.getIamPolicy",
	"storage.buckets.setIamPolicy",
	"storage.objects.create",
	"storage.objects.delete",
	"storage.objects.list",
}

// UserPermissions are what a signed-in person needs: Permissions, and
// leave to bill their API calls to the project, which a service account
// doesn't need because its calls bill there anyway.
var UserPermissions = append(slices.Clone(Permissions), "serviceusage.services.use")

// Errors.
var (
	// ErrInvalidKey is a key that isn't a service account key.
	ErrInvalidKey = errors.New("that isn't a Google Cloud service account key")
	// ErrRejected is a key Google refused: deleted, disabled or revoked,
	// or a sign-in whose refresh token was revoked.
	ErrRejected = errors.New("google refused the service account key or sign-in")
)

// Key is a parsed service account key, or a signed-in person's
// authorized_user key, or a keyless handout. Only its project and email
// are ever shown; the private key, refresh token or ticket stays in the
// bytes it came from.
type Key struct {
	ProjectID string
	// ClientEmail is the service account's email, or the person's.
	ClientEmail string
	// User is true for a person's authorized_user key.
	User bool
	// Keyless is true for a keyless handout, which holds no secret of
	// Google's: Google's STS trusts a token minted for it through
	// WorkloadProvider, and the session acts as the service account.
	Keyless          bool
	WorkloadProvider string
	// quotaProject is the project a person's API calls are billed to.
	quotaProject string
	handout      *keyless.Handout
	raw          []byte
}

// ParseKey reads a service account key, as downloaded from the console,
// the authorized_user key a Google sign-in hands out, or a keyless
// handout whose settings name a project_id, a workload_provider and a
// service_account.
func ParseKey(data []byte) (*Key, error) {
	if len(data) > maxKey {
		return nil, fmt.Errorf("%w: it's larger than %d KiB", ErrInvalidKey, maxKey>>10)
	}
	if h, ok, err := keyless.Parse(data); ok {
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
		}
		return keylessKey(h, data)
	}
	var k struct {
		Type         string `json:"type"`
		ProjectID    string `json:"project_id"`
		ClientEmail  string `json:"client_email"`
		PrivateKey   string `json:"private_key"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		RefreshToken string `json:"refresh_token"`
		QuotaProject string `json:"quota_project_id"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &k); err != nil {
		return nil, fmt.Errorf("%w: it isn't JSON", ErrInvalidKey)
	}
	switch {
	case k.Type == authorizedUser:
		if k.ProjectID == "" || k.ClientEmail == "" || k.ClientID == "" || k.ClientSecret == "" || k.RefreshToken == "" {
			return nil, fmt.Errorf("%w: the sign-in key has no project, email, client or refresh token", ErrInvalidKey)
		}
		return &Key{ProjectID: k.ProjectID, ClientEmail: k.ClientEmail, User: true, quotaProject: cmp.Or(k.QuotaProject, k.ProjectID), raw: data}, nil
	case k.Type != "service_account":
		return nil, fmt.Errorf("%w: its type is %q, not service_account", ErrInvalidKey, k.Type)
	case k.ProjectID == "" || k.ClientEmail == "" || k.PrivateKey == "":
		return nil, fmt.Errorf("%w: it has no project, email or private key", ErrInvalidKey)
	}
	return &Key{ProjectID: k.ProjectID, ClientEmail: k.ClientEmail, raw: data}, nil
}

// Client calls Google Cloud's APIs.
type Client struct {
	// endpoint replaces every Google API host when set, for tests.
	endpoint string
	base     *http.Client
	// poll is how often a long-running operation is checked.
	poll     time.Duration
	resolver DNSResolver
	// minter mints a keyless key's tokens. Without one a keyless key gets
	// no session.
	minter keyless.Minter
}

// DNSResolver resolves a provider-owned PSC name from the verifier network.
// Only trusted Go integrations choose it; HTTP input cannot replace DNS checks.
type DNSResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// WithDNSResolver returns an independent client configuration for a verifier.
func (c *Client) WithDNSResolver(resolver DNSResolver) *Client {
	configured := *c
	configured.resolver = resolver
	return &configured
}

// New returns a Client. An empty endpoint means Google's own hosts; any
// other is a test's fake, which answers at once, so it's polled often.
func New(endpoint string) *Client {
	c := &Client{endpoint: strings.TrimRight(endpoint, "/"), base: &http.Client{Timeout: requestTimeout}, poll: 5 * time.Second, resolver: net.DefaultResolver}
	if c.endpoint != "" {
		c.poll = 100 * time.Millisecond
	}
	return c
}

// url returns an API URL on host, such as "run.googleapis.com".
func (c *Client) url(host, path string) string {
	if c.endpoint != "" {
		return c.endpoint + path
	}
	return "https://" + host + path
}

// Session is an authorized client for one service account or person.
type Session struct {
	c   *Client
	key *Key
	// ts is the session's access tokens, renewed as they expire.
	ts   oauth2.TokenSource
	http *http.Client
}

// Session authorizes as key's service account, or as the person a user
// key signed in, or as the service account a keyless key impersonates.
// Each renews its own access token; a keyless one mints a fresh token to
// do it.
func (c *Client) Session(ctx context.Context, key *Key) (*Session, error) {
	// The token source keeps the context it's made with, so every
	// renewal goes through the base client and shares its timeout.
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.base)
	var ts oauth2.TokenSource
	base := c.base.Transport
	if key.Keyless {
		// It acts as the service account, so its calls bill to the
		// account's project without X-Goog-User-Project.
		var err error
		if ts, err = c.keylessTokens(ctx, key); err != nil {
			return nil, err
		}
	} else {
		kind := google.ServiceAccount
		if key.User {
			kind = google.AuthorizedUser
		}
		creds, err := google.CredentialsFromJSONWithType(ctx, key.raw, kind, Scope)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
		}
		ts = oauth2.ReuseTokenSource(nil, creds.TokenSource)
		if key.User {
			base = userProject{project: key.quotaProject, next: base}
		}
	}
	ts = oauth2.ReuseTokenSource(nil, tokenSource{ts: ts})
	return &Session{c: c, key: key, ts: ts, http: &http.Client{
		Timeout:   requestTimeout,
		Transport: &oauth2.Transport{Source: ts, Base: base},
	}}, nil
}

// userProject bills a person's API calls to the project they deploy to.
// Without it Google bills them to the project the sign-in app lives in,
// and refuses APIs that project hasn't turned on. x/oauth2 reads
// quota_project_id from the key but doesn't send it.
type userProject struct {
	project string
	next    http.RoundTripper
}

func (u userProject) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("X-Goog-User-Project", u.project)
	if u.next == nil {
		return http.DefaultTransport.RoundTrip(req)
	}
	return u.next.RoundTrip(req)
}

// tokenSource reports a refused key or sign-in as ErrRejected. A
// sign-in Google no longer honors is also oauth.ErrRefused, and a refused
// sign-in app is oauth.ErrAppRejected, as a renewal's are. Any other
// answer is Google failing to answer, which a retry may fix.
type tokenSource struct{ ts oauth2.TokenSource }

func (t tokenSource) Token() (*oauth2.Token, error) {
	tok, err := t.ts.Token()
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) {
		return tok, err
	}
	status := 0
	if re.Response != nil {
		status = re.Response.StatusCode
	}
	// Only the error code is kept, never the raw body, and only so much
	// of it.
	code := re.ErrorCode
	if code == "" {
		code = "no reason given"
	} else if len(code) > maxErrorCode {
		code = code[:maxErrorCode]
	}
	switch {
	case status != http.StatusBadRequest && status != http.StatusUnauthorized:
		return nil, fmt.Errorf("google answered %d from its token endpoint: %s", status, code)
	case re.ErrorCode == "invalid_client" || re.ErrorCode == "unauthorized_client":
		return nil, fmt.Errorf("%w: %w: google answered %d: %s", ErrRejected, oauth.ErrAppRejected, status, code)
	case re.ErrorCode == "invalid_grant":
		// The background job counts only this as a refusal too, so a
		// session and the job agree on what needs a new sign-in.
		return nil, fmt.Errorf("%w: %w: %s", ErrRejected, oauth.ErrRefused, cmp.Or(reason(re.ErrorDescription, maxReason), code))
	default:
		return nil, fmt.Errorf("google answered %d from its token endpoint: %s", status, code)
	}
}

// Key is the session's key.
func (s *Session) Key() *Key { return s.key }

// APIError is an error response from a Google API.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("google cloud answered %d: %s", e.Status, e.Message)
}

// IsNotFound reports whether err is Google's 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// IsForbidden reports whether err is Google's 403: the account lacks a
// permission.
func IsForbidden(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusForbidden
}

// IsConflict reports whether err is Google's 409, such as creating
// something that exists.
func IsConflict(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusConflict
}

// do sends a JSON request and decodes a JSON response into out.
func (s *Session) do(ctx context.Context, method, url string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return s.send(req, out)
}

func (s *Session) send(req *http.Request, out any) error {
	res, err := s.http.Do(req)
	if err != nil {
		// A keyless refusal says what was refused, so the request it
		// stopped isn't put in front of it.
		var ue *neturl.Error
		if errors.As(err, &ue) && keyless.Refused(ue.Err) {
			return ue.Err
		}
		if errors.Is(err, ErrRejected) || errors.Is(err, ErrInvalidKey) {
			return err
		}
		return fmt.Errorf("google cloud: %w", err)
	}
	defer res.Body.Close() // read-only; errors surface through ReadAll
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("google cloud: read response: %w", err)
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		return &APIError{Status: res.StatusCode, Message: msg}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("google cloud: decode response: %w", err)
	}
	return nil
}

// Missing returns which of Permissions the service account lacks on its
// project, keyless or not, or which of UserPermissions the signed-in
// person lacks. Any caller may ask this of any project, so it works
// before anything else does.
func (s *Session) Missing(ctx context.Context) ([]string, error) {
	needed := Permissions
	if s.key.User {
		needed = UserPermissions
	}
	return s.MissingOf(ctx, needed)
}

// MissingOf returns which of permissions the service account, or the
// signed-in person, lacks on its project.
func (s *Session) MissingOf(ctx context.Context, permissions []string) ([]string, error) {
	var out struct {
		Permissions []string `json:"permissions"`
	}
	url := s.c.url("cloudresourcemanager.googleapis.com", "/v3/projects/"+s.key.ProjectID+":testIamPermissions")
	if err := s.do(ctx, http.MethodPost, url, map[string]any{"permissions": permissions}, &out); err != nil {
		return nil, err
	}
	var missing []string
	for _, p := range permissions {
		if !slices.Contains(out.Permissions, p) {
			missing = append(missing, p)
		}
	}
	return missing, nil
}
