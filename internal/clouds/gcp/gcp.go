// Package gcp talks to Google Cloud for Loomplane: it turns a service
// account key into an authorized client and checks what that account may
// do. It uses Google's REST APIs over net/http rather than the Go SDKs,
// which would bring in gRPC and dozens of modules for a handful of calls.
package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
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
	"iam.serviceAccounts.actAs",
	"run.operations.get",
	"run.services.create",
	"run.services.get",
	"run.services.setIamPolicy",
	"run.services.update",
	"storage.buckets.create",
	"storage.objects.create",
}

// Errors.
var (
	// ErrInvalidKey is a key that isn't a service account key.
	ErrInvalidKey = errors.New("that isn't a Google Cloud service account key")
	// ErrRejected is a key Google refused: deleted, disabled or revoked.
	ErrRejected = errors.New("google refused the service account key")
)

// Key is a parsed service account key. Only its project and email are
// ever shown; the private key stays in the bytes it came from.
type Key struct {
	ProjectID   string
	ClientEmail string
	raw         []byte
}

// ParseKey reads a service account key, as downloaded from the console.
func ParseKey(data []byte) (*Key, error) {
	if len(data) > maxKey {
		return nil, fmt.Errorf("%w: it's larger than %d KiB", ErrInvalidKey, maxKey>>10)
	}
	var k struct {
		Type        string `json:"type"`
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &k); err != nil {
		return nil, fmt.Errorf("%w: it isn't JSON", ErrInvalidKey)
	}
	switch {
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
	poll time.Duration
}

// New returns a Client. An empty endpoint means Google's own hosts; any
// other is a test's fake, which answers at once, so it's polled often.
func New(endpoint string) *Client {
	c := &Client{endpoint: strings.TrimRight(endpoint, "/"), base: &http.Client{Timeout: requestTimeout}, poll: 5 * time.Second}
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

// Session is an authorized client for one service account.
type Session struct {
	c    *Client
	key  *Key
	http *http.Client
}

// Session authorizes as key's service account.
func (c *Client) Session(ctx context.Context, key *Key) (*Session, error) {
	creds, err := google.CredentialsFromJSONWithType(ctx, key.raw, google.ServiceAccount, Scope)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}
	// The token exchange shares the base client's timeout.
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.base)
	ts := oauth2.ReuseTokenSource(nil, creds.TokenSource)
	return &Session{c: c, key: key, http: &http.Client{
		Timeout:   requestTimeout,
		Transport: &oauth2.Transport{Source: oauth2.ReuseTokenSource(nil, tokenSource{ctx: ctx, ts: ts}), Base: c.base.Transport},
	}}, nil
}

// tokenSource reports a refused key as ErrRejected.
type tokenSource struct {
	ctx context.Context
	ts  oauth2.TokenSource
}

func (t tokenSource) Token() (*oauth2.Token, error) {
	tok, err := t.ts.Token()
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return nil, fmt.Errorf("%w: %s", ErrRejected, re.ErrorDescription)
	}
	return tok, err
}

// Key is the session's service account key.
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
// project. Any caller may ask this of any project, so it works before
// anything else does.
func (s *Session) Missing(ctx context.Context) ([]string, error) {
	var out struct {
		Permissions []string `json:"permissions"`
	}
	url := s.c.url("cloudresourcemanager.googleapis.com", "/v3/projects/"+s.key.ProjectID+":testIamPermissions")
	if err := s.do(ctx, http.MethodPost, url, map[string]any{"permissions": Permissions}, &out); err != nil {
		return nil, err
	}
	var missing []string
	for _, p := range Permissions {
		if !slices.Contains(out.Permissions, p) {
			missing = append(missing, p)
		}
	}
	return missing, nil
}
