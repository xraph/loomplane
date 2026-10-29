// Package digitalocean talks to DigitalOcean's API for Loomplane: it checks
// an API token, runs services on App Platform and provisions Managed
// Databases with it.
//
// App Platform builds from a repository through DigitalOcean's own GitHub
// connection, which an account sets up once in DigitalOcean's control
// panel. It builds the newest commit of a branch and can't be told which
// commit to build, so WaitDeployment checks the commit every component was
// built from, and reports one that isn't the commit a plan approved.
package digitalocean

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
	"time"

	"github.com/xraph/loomplane/internal/clouds/progress"
)

// DefaultEndpoint is DigitalOcean's API.
const DefaultEndpoint = "https://api.digitalocean.com"

const (
	requestTimeout = 60 * time.Second
	maxResponse    = 8 << 20
	maxToken       = 512
	pageSize       = 200
)

// Errors.
var (
	// ErrInvalidToken is something that can't be a DigitalOcean API token.
	ErrInvalidToken = errors.New("that isn't a DigitalOcean API token")
	// ErrRejected is a token DigitalOcean refused: revoked, expired, or
	// without the scopes the call needs.
	ErrRejected = errors.New("digitalocean refused the API token")
)

// Client calls DigitalOcean's API.
type Client struct {
	endpoint string
	http     *http.Client
	// poll is how often a deployment or a database is checked.
	poll time.Duration
}

// New returns a Client. An empty endpoint means DigitalOcean's own; any
// other is a test's fake, which answers at once, so it's polled often.
func New(endpoint string) *Client {
	c := &Client{endpoint: DefaultEndpoint, http: &http.Client{Timeout: requestTimeout}, poll: 10 * time.Second}
	if endpoint != "" {
		c.endpoint, c.poll = strings.TrimRight(endpoint, "/"), 100*time.Millisecond
	}
	return c
}

// CheckToken tells whether s could be an API token, without asking
// DigitalOcean.
func CheckToken(s string) error {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxToken || strings.ContainsAny(s, " \t\r\n") {
		return ErrInvalidToken
	}
	return nil
}

// APIError is an error response from DigitalOcean.
type APIError struct {
	RetryAfter time.Duration
	Status     int
	ID         string
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("digitalocean answered %d: %s", e.Status, e.Message)
}

// IsNotFound reports whether err is DigitalOcean's 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, token, method, path string, query url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	u := c.endpoint + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("digitalocean: %w", err)
	}
	defer res.Body.Close() // read-only; errors surface through ReadAll
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("digitalocean: read response: %w", err)
	}
	if res.StatusCode >= 300 {
		ae := &APIError{RetryAfter: retryAfter(res.Header.Get("Retry-After")), Status: res.StatusCode, Message: strings.TrimSpace(string(data))}
		var e struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &e) == nil && e.Message != "" {
			ae.ID, ae.Message = e.ID, e.Message
		}
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w: %w", ErrRejected, ae)
		}
		return ae
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("digitalocean: decode response: %w", err)
	}
	return nil
}

// Account is who a token acts for. Team is the team a token belongs to;
// DigitalOcean scopes every token to one.
type Account struct {
	DropletLimit *int64 `json:"droplet_limit"`
	UUID         string `json:"uuid"`
	Email        string `json:"email"`
	Status       string `json:"status"`
	Team         *struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	} `json:"team"`
}

// Account reads the account token acts for.
func (c *Client) Account(ctx context.Context, token string) (*Account, error) {
	var out struct {
		Account Account `json:"account"`
	}
	if err := c.do(ctx, token, http.MethodGet, "/v2/account", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out.Account, nil
}

// AppSpec is an App Platform app: one region, and the components in it.
type AppSpec struct {
	Name        string          `json:"name"`
	Region      string          `json:"region,omitempty"`
	Services    []Component     `json:"services,omitempty"`
	Workers     []Component     `json:"workers,omitempty"`
	StaticSites []Component     `json:"static_sites,omitempty"`
	Domains     []AppDomainSpec `json:"domains,omitempty"`
}

// AppDomainSpec is a hostname an app answers on. Type is DEFAULT (the
// ondigitalocean.app one), PRIMARY or ALIAS. Loomplane adds custom
// hostnames as ALIAS, so the app's primary domain stays as it was. Zone
// is the DigitalOcean DNS domain, set only when DigitalOcean manages the
// records. MinimumTLSVersion is "1.2" or "1.3". Per the API reference
// (docs.digitalocean.com/reference/api/reference/apps/, read 2026-10-29).
//
// Whether an update that changes only domains starts a deployment is not
// stated in DigitalOcean's docs. The API example's deployment cause of
// "app spec updated" suggests any spec update deploys, so callers must
// treat a domains-only update as possibly starting one.
type AppDomainSpec struct {
	Domain            string `json:"domain"`
	Type              string `json:"type,omitempty"`
	Wildcard          bool   `json:"wildcard,omitempty"`
	Zone              string `json:"zone,omitempty"`
	MinimumTLSVersion string `json:"minimum_tls_version,omitempty"`
}

// AppDomainAlias is the type of a hostname that isn't the app's primary.
const AppDomainAlias = "ALIAS"

// App domain phases. The API reference also lists UNKNOWN.
const (
	AppDomainPending     = "PENDING"
	AppDomainConfiguring = "CONFIGURING"
	AppDomainActive      = "ACTIVE"
	AppDomainError       = "ERROR"
)

// AppIngressIPv4 and AppIngressIPv6 are App Platform's shared public
// ingress addresses, for A and AAAA records on a hostname that can't be
// a CNAME. Checked against "How to Add Static IP Addresses to App
// Platform Apps" (docs.digitalocean.com/products/app-platform/how-to/add-ip-address/)
// on 2026-10-29.
var (
	AppIngressIPv4 = []string{"162.159.140.98", "172.66.0.96"}
	AppIngressIPv6 = []string{"2606:4700:7::60", "2a06:98c1:58::60"}
)

// AppDomain is a hostname on a live app, and how far along its DNS and
// certificate setup is.
type AppDomain struct {
	ID                   string             `json:"id"`
	Spec                 AppDomainSpec      `json:"spec"`
	Phase                string             `json:"phase"`
	Progress             *AppDomainProgress `json:"progress,omitempty"`
	CertificateExpiresAt time.Time          `json:"certificate_expires_at"`
}

// AppDomainProgress is the steps App Platform takes to bring a domain up,
// such as verify-cname, ensure-ssl-txt-record and ensure-certificate.
type AppDomainProgress struct {
	Steps []AppDomainStep `json:"steps"`
}

// AppDomainStep is one of them. Reason is only set on a step that failed.
type AppDomainStep struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Reason    *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"reason,omitempty"`
}

// Component is a service, worker or static site, and how App Platform
// builds and runs it. Not every field applies to every kind: a static
// site has no instances or ports, and only it has an output directory.
type Component struct {
	Name   string        `json:"name"`
	GitHub *GitHubSource `json:"github,omitempty"`
	// SourceDir and DockerfilePath are paths from the repository's root.
	SourceDir      string `json:"source_dir,omitempty"`
	DockerfilePath string `json:"dockerfile_path,omitempty"`
	BuildCommand   string `json:"build_command,omitempty"`
	RunCommand     string `json:"run_command,omitempty"`
	OutputDir      string `json:"output_dir,omitempty"`
	// InstanceSizeSlug and InstanceCount don't apply to static sites.
	InstanceSizeSlug string `json:"instance_size_slug,omitempty"`
	InstanceCount    int    `json:"instance_count,omitempty"`
	// HTTPPort is a public service's port. InternalPorts are a private
	// one's, reachable only from inside the app.
	HTTPPort      int   `json:"http_port,omitempty"`
	InternalPorts []int `json:"internal_ports,omitempty"`
	Envs          []Env `json:"envs,omitempty"`
}

// GitHubSource is a branch of a GitHub repository, as owner/name.
// DeployOnPush is always false here: Loomplane deploys what was approved,
// not whatever was pushed last.
type GitHubSource struct {
	Repo         string `json:"repo"`
	Branch       string `json:"branch"`
	DeployOnPush bool   `json:"deploy_on_push"`
}

// Env is an environment variable. Type SECRET hides its value in
// DigitalOcean's control panel and API.
type Env struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Scope string `json:"scope,omitempty"`
	Type  string `json:"type,omitempty"`
}

// App is an App Platform app.
type App struct {
	ID                   string      `json:"id"`
	LiveURL              string      `json:"live_url"`
	DefaultIngress       string      `json:"default_ingress"`
	Spec                 AppSpec     `json:"spec"`
	ActiveDeployment     *Deployment `json:"active_deployment"`
	InProgressDeployment *Deployment `json:"in_progress_deployment"`
	PendingDeployment    *Deployment `json:"pending_deployment"`
	Domains              []AppDomain `json:"domains,omitempty"`
}

// Deployment phases.
const (
	PhaseActive     = "ACTIVE"
	PhaseError      = "ERROR"
	PhaseCanceled   = "CANCELED"
	PhaseSuperseded = "SUPERSEDED"
)

// Deployment is one build and release of an app.
type Deployment struct {
	ID          string     `json:"id"`
	Phase       string     `json:"phase"`
	Services    []Deployed `json:"services"`
	Workers     []Deployed `json:"workers"`
	StaticSites []Deployed `json:"static_sites"`
	Progress    *struct {
		SuccessSteps int `json:"success_steps"`
		TotalSteps   int `json:"total_steps"`
	} `json:"progress"`
}

// Deployed is a component of a deployment, and the commit it was built
// from, once App Platform has fetched it.
type Deployed struct {
	Name             string `json:"name"`
	SourceCommitHash string `json:"source_commit_hash"`
}

func (d *Deployment) components() []Deployed {
	out := append(append(append([]Deployed{}, d.Services...), d.Workers...), d.StaticSites...)
	return out
}

// Built reports whether every component of d was built from commit, as
// WaitDeployment decides for a deployment that went live.
func (d *Deployment) Built(commit string) bool {
	for _, comp := range d.components() {
		if !sameCommit(comp.SourceCommitHash, commit) {
			return false
		}
	}
	return true
}

// FindApp finds the account's app called name.
func (c *Client) FindApp(ctx context.Context, token, name string) (*App, error) {
	for page := 1; ; page++ {
		var out struct {
			Apps  []App `json:"apps"`
			Links struct {
				Pages struct {
					Next string `json:"next"`
				} `json:"pages"`
			} `json:"links"`
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
		if err := c.do(ctx, token, http.MethodGet, "/v2/apps", q, nil, &out); err != nil {
			return nil, err
		}
		for _, a := range out.Apps {
			if a.Spec.Name == name {
				return &a, nil
			}
		}
		if out.Links.Pages.Next == "" || len(out.Apps) == 0 {
			return nil, nil
		}
	}
}

// CreateApp creates an app. App Platform starts a deployment of it at
// once, from each branch's newest commit.
func (c *Client) CreateApp(ctx context.Context, token string, spec AppSpec) (*App, error) {
	var out struct {
		App App `json:"app"`
	}
	if err := c.do(ctx, token, http.MethodPost, "/v2/apps", nil, map[string]any{"spec": spec}, &out); err != nil {
		var ae *APIError
		if errors.As(err, &ae) && strings.Contains(strings.ToLower(ae.Message), "github") {
			return nil, fmt.Errorf("%w. App Platform builds from your repository through DigitalOcean's own GitHub connection: give DigitalOcean access to it in the control panel, under Settings, GitHub", err)
		}
		return nil, err
	}
	return &out.App, nil
}

// UpdateApp replaces an app's spec. It leaves each component on the
// commit it was last built from; a deployment fetches new ones.
func (c *Client) UpdateApp(ctx context.Context, token, id string, spec AppSpec) (*App, error) {
	var out struct {
		App App `json:"app"`
	}
	if err := c.do(ctx, token, http.MethodPut, "/v2/apps/"+url.PathEscape(id), nil, map[string]any{"spec": spec}, &out); err != nil {
		return nil, err
	}
	return &out.App, nil
}

// GetApp reads an app.
func (c *Client) GetApp(ctx context.Context, token, id string) (*App, error) {
	var out struct {
		App App `json:"app"`
	}
	if err := c.do(ctx, token, http.MethodGet, "/v2/apps/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out.App, nil
}

// DeleteApp deletes an app. One that's already gone is not an error.
func (c *Client) DeleteApp(ctx context.Context, token, id string) error {
	err := c.do(ctx, token, http.MethodDelete, "/v2/apps/"+url.PathEscape(id), nil, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// CreateDeployment deploys an app from each branch's newest commit.
func (c *Client) CreateDeployment(ctx context.Context, token, appID string) (*Deployment, error) {
	var out struct {
		Deployment Deployment `json:"deployment"`
	}
	path := "/v2/apps/" + url.PathEscape(appID) + "/deployments"
	if err := c.do(ctx, token, http.MethodPost, path, nil, map[string]any{"force_build": false}, &out); err != nil {
		return nil, err
	}
	if out.Deployment.ID == "" {
		return nil, errors.New("digitalocean accepted the deployment without a deployment ID")
	}
	return &out.Deployment, nil
}

// GetDeployment reads a deployment.
func (c *Client) GetDeployment(ctx context.Context, token, appID, id string) (*Deployment, error) {
	var out struct {
		Deployment Deployment `json:"deployment"`
	}
	path := "/v2/apps/" + url.PathEscape(appID) + "/deployments/" + url.PathEscape(id)
	if err := c.do(ctx, token, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out.Deployment, nil
}

// CancelDeployment cancels a deployment that hasn't gone live. One that
// already finished is not an error.
func (c *Client) CancelDeployment(ctx context.Context, token, appID, id string) error {
	path := "/v2/apps/" + url.PathEscape(appID) + "/deployments/" + url.PathEscape(id) + "/cancel"
	err := c.do(ctx, token, http.MethodPost, path, nil, nil, nil)
	var ae *APIError
	if IsNotFound(err) || (errors.As(err, &ae) && ae.Status == http.StatusBadRequest) {
		return nil
	}
	return err
}

// Rollback puts an app back on an earlier deployment, without pinning it,
// so later deployments still go ahead.
func (c *Client) Rollback(ctx context.Context, token, appID, deploymentID string) error {
	path := "/v2/apps/" + url.PathEscape(appID) + "/rollback"
	return c.do(ctx, token, http.MethodPost, path, nil, map[string]any{"deployment_id": deploymentID, "skip_pin": true}, nil)
}

// CommitMismatchError is a deployment App Platform built from a commit other
// than the one approved: someone pushed to the branch after the plan was
// approved, or the branch isn't the one the commit is on.
type CommitMismatchError struct {
	Deployment *Deployment
	Component  string
	Built      string
	Want       string
}

func (e *CommitMismatchError) Error() string {
	built := e.Built
	if built == "" {
		built = "a commit it didn't report"
	}
	return fmt.Sprintf("app platform built %s from %s, not the approved commit %s. Its branch has moved on since the plan was approved, or isn't the branch the commit is on", e.Component, short(built), short(e.Want))
}

func short(sha string) string { return sha[:min(12, len(sha))] }

// sameCommit compares a full commit hash with another that may be
// shortened.
func sameCommit(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if a == "" || b == "" {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// WaitDeployment polls a deployment until it's live, and fails if it
// doesn't get there. As soon as App Platform reports the commit a
// component was built from, it's checked against commit: a different one
// stops the wait with a *CommitMismatchError, so the caller can cancel the
// deployment before it goes live. A live deployment must have reported a
// matching commit for every component.
func (c *Client) WaitDeployment(ctx context.Context, token, appID, id, commit string) (*Deployment, error) {
	if appID == "" || id == "" {
		return nil, errors.New("app platform deployment polling requires app and deployment IDs")
	}
	for {
		d, err := c.GetDeployment(ctx, token, appID, id)
		if err != nil {
			return nil, err
		}
		for _, comp := range d.components() {
			if comp.SourceCommitHash != "" && !sameCommit(comp.SourceCommitHash, commit) {
				return d, &CommitMismatchError{Deployment: d, Component: comp.Name, Built: comp.SourceCommitHash, Want: commit}
			}
		}
		switch d.Phase {
		case PhaseActive:
			for _, comp := range d.components() {
				if !sameCommit(comp.SourceCommitHash, commit) {
					return d, &CommitMismatchError{Deployment: d, Component: comp.Name, Built: comp.SourceCommitHash, Want: commit}
				}
			}
			return d, nil
		case PhaseError, PhaseCanceled, PhaseSuperseded:
			return d, fmt.Errorf("app platform deployment %s: %s. Its build and deploy logs are on the app's Activity tab in DigitalOcean", id, strings.ToLower(d.Phase))
		}
		msg := "Building on App Platform (" + strings.ToLower(strings.ReplaceAll(d.Phase, "_", " ")) + ")"
		if p := d.Progress; p != nil && p.TotalSteps > 0 {
			msg += fmt.Sprintf(", step %d of %d", min(p.SuccessSteps+1, p.TotalSteps), p.TotalSteps)
		}
		progress.Report(ctx, msg)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.poll):
		}
	}
}

// Database engines, by DigitalOcean's names for them.
const (
	Postgres = "pg"
	MySQL    = "mysql"
	Valkey   = "valkey"
)

const databaseOnline = "online"

// DatabaseSpec is a Managed Database cluster to make. Region is a
// datacenter, such as nyc3, not an App Platform region.
type DatabaseSpec struct {
	Name     string `json:"name"`
	Engine   string `json:"engine"`
	Version  string `json:"version,omitempty"`
	Region   string `json:"region"`
	Size     string `json:"size"`
	NumNodes int    `json:"num_nodes"`
}

// Database is a Managed Database cluster.
type Database struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Engine     string `json:"engine"`
	Version    string `json:"version"`
	Region     string `json:"region"`
	Status     string `json:"status"`
	Connection *struct {
		URI string `json:"uri"`
	} `json:"connection"`
}

// FindDatabase finds the account's database cluster called name.
func (c *Client) FindDatabase(ctx context.Context, token, name string) (*Database, error) {
	var out struct {
		Databases []Database `json:"databases"`
	}
	if err := c.do(ctx, token, http.MethodGet, "/v2/databases", nil, nil, &out); err != nil {
		return nil, err
	}
	for _, d := range out.Databases {
		if d.Name == name {
			return &d, nil
		}
	}
	return nil, nil
}

// GetDatabase reads a database cluster.
func (c *Client) GetDatabase(ctx context.Context, token, id string) (*Database, error) {
	var out struct {
		Database Database `json:"database"`
	}
	if err := c.do(ctx, token, http.MethodGet, "/v2/databases/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out.Database, nil
}

// EnsureDatabase makes the cluster unless the account has one called
// spec.Name, and waits until it's online.
func (c *Client) EnsureDatabase(ctx context.Context, token string, spec DatabaseSpec) (*Database, error) {
	d, err := c.FindDatabase(ctx, token, spec.Name)
	if err != nil {
		return nil, err
	}
	if d == nil {
		var out struct {
			Database Database `json:"database"`
		}
		if cerr := c.do(ctx, token, http.MethodPost, "/v2/databases", nil, spec, &out); cerr != nil {
			return nil, cerr
		}
		d = &out.Database
	}
	for d.Status != databaseOnline {
		progress.Report(ctx, "Waiting for DigitalOcean to make "+spec.Name+" ("+d.Status+")")
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.poll):
		}
		if d, err = c.GetDatabase(ctx, token, d.ID); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// DeleteDatabase deletes a cluster and its data. One that's already gone
// is not an error.
func (c *Client) DeleteDatabase(ctx context.Context, token, id string) error {
	err := c.do(ctx, token, http.MethodDelete, "/v2/databases/"+url.PathEscape(id), nil, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

func retryAfter(value string) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(min(seconds, 86400)) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(0, min(time.Until(at), 24*time.Hour))
	}
	return 0
}
