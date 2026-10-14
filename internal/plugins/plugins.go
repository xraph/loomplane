// Package plugins talks to provider plugins: deployment targets that run
// as their own services and speak pkg/provider/sdk's protocol. Loomplane
// reads each one's description when it starts and treats it like a
// built-in target from then on.
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/pkg/provider/sdk"
)

// Plugin is one provider plugin, described.
type Plugin struct {
	*Client
	Description sdk.Description
}

// Name is the provider's name, as manifests name it.
func (p *Plugin) Name() string { return p.Description.Provider.Name }

// Cloud is the kind of connection the provider deploys with.
func (p *Plugin) Cloud() string { return p.Description.Provider.Cloud }

// Load describes each configured plugin. A plugin that doesn't answer, or
// speaks another protocol, is an error: Loomplane would otherwise start
// with plans it can't carry out.
func Load(ctx context.Context, cfg []config.PluginConfig) ([]*Plugin, error) {
	out := make([]*Plugin, 0, len(cfg))
	seen := map[string]bool{}
	for _, pc := range cfg {
		c := New(pc.URL, pc.Token)
		d, err := c.Describe(ctx)
		if err != nil {
			return nil, fmt.Errorf("provider plugin at %s: %w", pc.URL, err)
		}
		if d.Protocol != sdk.Protocol {
			return nil, fmt.Errorf("provider plugin at %s speaks %q; this Loomplane speaks %q", pc.URL, d.Protocol, sdk.Protocol)
		}
		// A plugin carries out its own actions: that's the point of it.
		d.Provider.Available, d.Provider.Note = true, ""
		if err := d.Provider.Validate(); err != nil {
			return nil, fmt.Errorf("provider plugin at %s: %w", pc.URL, err)
		}
		if seen[d.Provider.Name] {
			return nil, fmt.Errorf("two provider plugins are called %q", d.Provider.Name)
		}
		seen[d.Provider.Name] = true
		out = append(out, &Plugin{Client: c, Description: *d})
	}
	return out, nil
}

// Client calls one plugin.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New returns a Client for the plugin at baseURL.
func New(baseURL, token string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), token: token, http: &http.Client{Timeout: 2 * time.Minute}}
}

// Error is an answer the plugin gave instead of a result.
type Error struct {
	Status   int
	Message  string
	Rejected bool
}

func (e *Error) Error() string { return e.Message }

// Unwrap makes a refused credential match sdk.ErrRejected.
func (e *Error) Unwrap() error {
	if e.Rejected {
		return sdk.ErrRejected
	}
	return nil
}

// ErrNotFound is an operation the plugin doesn't have, such as one it lost
// when it restarted.
var ErrNotFound = errors.New("the plugin has no such operation")

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var f sdk.Failure
		if jerr := json.Unmarshal(data, &f); jerr != nil || f.Error == "" {
			f.Error = fmt.Sprintf("the plugin answered %d", res.StatusCode)
		}
		if res.StatusCode == http.StatusNotFound && strings.HasPrefix(path, "/v1/operations/") {
			return fmt.Errorf("%w: %s", ErrNotFound, f.Error)
		}
		return &Error{Status: res.StatusCode, Message: f.Error, Rejected: f.Rejected}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, path, bytes.NewReader(data), "application/json", out)
}

// Describe reads what the plugin deploys.
func (c *Client) Describe(ctx context.Context) (*sdk.Description, error) {
	var d sdk.Description
	return &d, c.do(ctx, http.MethodGet, "/v1/describe", nil, "", &d)
}

// Check asks the plugin whether a credential works.
func (c *Client) Check(ctx context.Context, cred sdk.Credential) (*sdk.Account, error) {
	var a sdk.Account
	return &a, c.post(ctx, "/v1/check", cred, &a)
}

// HasSource reports whether the plugin has a source already.
func (c *Client) HasSource(ctx context.Context, id string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.base+"/v1/sources/"+url.PathEscape(id), http.NoBody)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	_ = res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("the plugin answered %d", res.StatusCode)
}

// PutSource uploads a source tarball.
func (c *Client) PutSource(ctx context.Context, id string, src io.Reader) error {
	return c.do(ctx, http.MethodPut, "/v1/sources/"+url.PathEscape(id), src, "application/gzip", nil)
}

// Start starts an operation, or finds the one already running for the
// same job.
func (c *Client) Start(ctx context.Context, req sdk.OperationRequest) (*sdk.Operation, error) {
	var op sdk.Operation
	return &op, c.post(ctx, "/v1/operations", req, &op)
}

// Operation reads an operation.
func (c *Client) Operation(ctx context.Context, id string) (*sdk.Operation, error) {
	var op sdk.Operation
	return &op, c.do(ctx, http.MethodGet, "/v1/operations/"+url.PathEscape(id), nil, "", &op)
}

// Observe reads what's live for a target.
func (c *Client) Observe(ctx context.Context, t sdk.Target) (*sdk.Observation, error) {
	var o sdk.Observation
	return &o, c.post(ctx, "/v1/observe", t, &o)
}

// Set is the configured plugins, described the first time someone asks.
// Building it does no I/O; the first extension to register loads it, so
// both connections and deployments see the same plugins.
type Set struct {
	cfg  []config.PluginConfig
	once sync.Once
	list []*Plugin
	err  error
}

// NewSet returns the configured plugins, not yet loaded.
func NewSet(cfg []config.PluginConfig) *Set { return &Set{cfg: cfg} }

// loadTimeout bounds describing every plugin when Loomplane starts.
const loadTimeout = 30 * time.Second

// Plugins loads the plugins, once.
func (s *Set) Plugins() ([]*Plugin, error) {
	if s == nil {
		return nil, nil
	}
	s.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		s.list, s.err = Load(ctx, s.cfg)
	})
	return s.list, s.err
}

// OperationLogs reads an advertised optional output page from a single operation.
func (c *Client) OperationLogs(ctx context.Context, id, cursor string, limit int) (*sdk.LogPage, error) {
	query := url.Values{"cursor": {cursor}, "limit": {strconv.Itoa(limit)}}
	var page sdk.LogPage
	return &page, c.do(ctx, http.MethodGet, "/v1/operations/"+url.PathEscape(id)+"/logs?"+query.Encode(), nil, "", &page)
}

// RedactionValues are used only in worker memory to sanitize provider output.
func (c *Client) RedactionValues() []string { return []string{c.token} }
