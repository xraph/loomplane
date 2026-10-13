// Package render talks to Render's REST API for Loomplane: it checks an
// API key, and runs services, Postgres and Key Value with it.
//
// Render builds from a repository through its own GitHub connection, which
// a workspace sets up once in Render's dashboard. Loomplane can't hand it
// one, so it creates each service from the repository's address and then
// deploys the exact commit a plan approved.
package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/progress"
)

// DefaultEndpoint is Render's API.
const DefaultEndpoint = "https://api.render.com/v1"

const (
	requestTimeout = 60 * time.Second
	maxResponse    = 4 << 20
	maxKey         = 512
)

// Errors.
var (
	// ErrInvalidKey is something that can't be a Render API key.
	ErrInvalidKey = errors.New("that isn't a Render API key")
	// ErrRejected is a key Render refused: revoked, or without access to
	// the workspace.
	ErrRejected = errors.New("render refused the API key")
)

// Client calls Render's API.
type Client struct {
	endpoint string
	http     *http.Client
	// poll is how often a deploy or a database is checked.
	poll time.Duration
}

// New returns a Client. An empty endpoint means Render's own; any other
// is a test's fake, which answers at once, so it's polled often.
func New(endpoint string) *Client {
	c := &Client{endpoint: DefaultEndpoint, http: &http.Client{Timeout: requestTimeout}, poll: 10 * time.Second}
	if endpoint != "" {
		c.endpoint, c.poll = strings.TrimRight(endpoint, "/"), 100*time.Millisecond
	}
	return c
}

// Key is an API key, and the workspace it acts in. Render's keys are a
// person's, and can reach every workspace they're in, so OwnerID says
// which one Loomplane uses.
type Key struct {
	Value   string
	OwnerID string
}

// CheckKey tells whether s could be an API key, without asking Render.
func CheckKey(s string) error {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxKey || strings.ContainsAny(s, " \t\r\n") {
		return ErrInvalidKey
	}
	return nil
}

// APIError is an error response from Render.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("render answered %d: %s", e.Status, e.Message)
}

// IsNotFound reports whether err is Render's 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, key Key, method, path string, query url.Values, in, out any) error {
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
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(key.Value))
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	defer res.Body.Close() // read-only; errors surface through ReadAll
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("render: read response: %w", err)
	}
	if res.StatusCode >= 300 {
		ae := &APIError{Status: res.StatusCode, Message: strings.TrimSpace(string(data))}
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &e) == nil && e.Message != "" {
			ae.Message = e.Message
		}
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w: %s", ErrRejected, ae.Message)
		}
		return ae
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("render: decode response: %w", err)
	}
	return nil
}

// Owner is a workspace a key can act in.
type Owner struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// Owners lists the workspaces key can act in.
func (c *Client) Owners(ctx context.Context, key Key) ([]Owner, error) {
	var page []struct {
		Owner Owner `json:"owner"`
	}
	if err := c.do(ctx, key, http.MethodGet, "/owners", url.Values{"limit": {"100"}}, nil, &page); err != nil {
		return nil, err
	}
	out := make([]Owner, 0, len(page))
	for _, p := range page {
		out = append(out, p.Owner)
	}
	return out, nil
}

// Service types.
const (
	WebService        = "web_service"
	PrivateService    = "private_service"
	BackgroundWorker  = "background_worker"
	StaticSite        = "static_site"
	deployLive        = "live"
	resourceAvailable = "available"
)

// ServiceSpec is how a service is built and run.
type ServiceSpec struct {
	Type    string
	Name    string
	Repo    string
	RootDir string
	Runtime string // docker, node, go, python, rust, ruby, elixir
	Build   string
	Start   string
	// Dockerfile and Context are paths from the repository's root.
	Dockerfile string
	Context    string
	// PublishPath is a static site's output directory.
	PublishPath string
	Plan        string
	Region      string
	Instances   int
	Env         map[string]string
}

// Service is a Render service.
type Service struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Slug           string `json:"slug"`
	Type           string `json:"type"`
	Repo           string `json:"repo"`
	Suspended      string `json:"suspended"`
	ServiceDetails struct {
		URL string `json:"url"`
	} `json:"serviceDetails"`
}

func (s ServiceSpec) details() map[string]any {
	if s.Type == StaticSite {
		return map[string]any{"buildCommand": s.Build, "publishPath": s.PublishPath, "pullRequestPreviewsEnabled": "no"}
	}
	env := map[string]any{"buildCommand": s.Build, "startCommand": s.Start}
	if s.Runtime == "docker" {
		env = map[string]any{"dockerfilePath": s.Dockerfile, "dockerContext": s.Context}
	}
	d := map[string]any{
		"runtime": s.Runtime, "plan": s.Plan, "region": s.Region,
		"numInstances": max(1, s.Instances), "envSpecificDetails": env,
	}
	if s.Type == WebService || s.Type == PrivateService {
		d["pullRequestPreviewsEnabled"] = "no"
	}
	return d
}

func envVars(env map[string]string) []map[string]string {
	out := make([]map[string]string, 0, len(env))
	for k, v := range env {
		out = append(out, map[string]string{"key": k, "value": v})
	}
	return out
}

// FindService finds the workspace's service called name.
func (c *Client) FindService(ctx context.Context, key Key, name string) (*Service, error) {
	var page []struct {
		Service Service `json:"service"`
	}
	q := url.Values{"name": {name}, "ownerId": {key.OwnerID}, "limit": {"20"}}
	if err := c.do(ctx, key, http.MethodGet, "/services", q, nil, &page); err != nil {
		return nil, err
	}
	for _, p := range page {
		if p.Service.Name == name {
			s := p.Service
			return &s, nil
		}
	}
	return nil, nil
}

// EnsureService creates the service, or updates the one already called
// spec.Name to match spec. A new service is made with automatic deploys
// off, and the deploy Render starts on its own when one is made is
// cancelled: what runs is only ever a commit someone approved.
func (c *Client) EnsureService(ctx context.Context, key Key, spec ServiceSpec) (*Service, error) {
	s, err := c.FindService(ctx, key, spec.Name)
	if err != nil {
		return nil, err
	}
	if s == nil {
		body := map[string]any{
			"type": spec.Type, "name": spec.Name, "ownerId": key.OwnerID, "repo": spec.Repo,
			"autoDeploy": "no", "rootDir": spec.RootDir, "envVars": envVars(spec.Env),
			"serviceDetails": spec.details(),
		}
		var created struct {
			Service  Service `json:"service"`
			DeployID string  `json:"deployId"`
		}
		if err := c.do(ctx, key, http.MethodPost, "/services", nil, body, &created); err != nil {
			var ae *APIError
			if errors.As(err, &ae) && strings.Contains(strings.ToLower(ae.Message), "repo") {
				return nil, fmt.Errorf("%w. Render builds from the repository through its own GitHub connection: give Render access to %s in its dashboard, under Account settings, Git", err, spec.Repo)
			}
			return nil, err
		}
		if created.DeployID != "" {
			if err := c.CancelDeploy(ctx, key, created.Service.ID, created.DeployID); err != nil && !IsNotFound(err) {
				return nil, err
			}
		}
		return &created.Service, nil
	}
	body := map[string]any{"rootDir": spec.RootDir, "serviceDetails": spec.details(), "autoDeploy": "no"}
	var updated Service
	if err := c.do(ctx, key, http.MethodPatch, "/services/"+url.PathEscape(s.ID), nil, body, &updated); err != nil {
		return nil, err
	}
	if err := c.SetEnv(ctx, key, s.ID, spec.Env); err != nil {
		return nil, err
	}
	return &updated, nil
}

// GetService reads a service.
func (c *Client) GetService(ctx context.Context, key Key, id string) (*Service, error) {
	var s Service
	if err := c.do(ctx, key, http.MethodGet, "/services/"+url.PathEscape(id), nil, nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// SetEnv replaces a service's environment variables with env.
func (c *Client) SetEnv(ctx context.Context, key Key, id string, env map[string]string) error {
	return c.do(ctx, key, http.MethodPut, "/services/"+url.PathEscape(id)+"/env-vars", nil, envVars(env), nil)
}

// Deploy is one deploy of a service.
type Deploy struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
}

// DeployCommit deploys commit to a service.
func (c *Client) DeployCommit(ctx context.Context, key Key, id, commit string) (*Deploy, error) {
	var d Deploy
	body := map[string]any{"commitId": commit, "clearCache": "do_not_clear"}
	if err := c.do(ctx, key, http.MethodPost, "/services/"+url.PathEscape(id)+"/deploys", nil, body, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// CancelDeploy cancels a deploy that hasn't finished.
func (c *Client) CancelDeploy(ctx context.Context, key Key, id, deploy string) error {
	return c.do(ctx, key, http.MethodPost, "/services/"+url.PathEscape(id)+"/deploys/"+url.PathEscape(deploy)+"/cancel", nil, nil, nil)
}

// WaitDeploy polls a deploy until it's live, and fails if it doesn't get
// there.
func (c *Client) WaitDeploy(ctx context.Context, key Key, id, deploy string) (*Deploy, error) {
	for {
		var d Deploy
		if err := c.do(ctx, key, http.MethodGet, "/services/"+url.PathEscape(id)+"/deploys/"+url.PathEscape(deploy), nil, nil, &d); err != nil {
			return nil, err
		}
		switch d.Status {
		case deployLive:
			return &d, nil
		case "build_failed", "update_failed", "pre_deploy_failed", "canceled", "deactivated":
			return &d, fmt.Errorf("render deploy %s: %s. Its log is on the service's Events page in Render", deploy, strings.ReplaceAll(d.Status, "_", " "))
		}
		progress.Report(ctx, "Building on Render ("+strings.ReplaceAll(d.Status, "_", " ")+")")
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.poll):
		}
	}
}

// LatestDeploy is a service's newest deploy, or nil.
func (c *Client) LatestDeploy(ctx context.Context, key Key, id string) (*Deploy, error) {
	var page []struct {
		Deploy Deploy `json:"deploy"`
	}
	if err := c.do(ctx, key, http.MethodGet, "/services/"+url.PathEscape(id)+"/deploys", url.Values{"limit": {"1"}}, nil, &page); err != nil {
		return nil, err
	}
	if len(page) == 0 {
		return nil, nil
	}
	return &page[0].Deploy, nil
}

// DeleteService deletes a service. One that's already gone is not an
// error.
func (c *Client) DeleteService(ctx context.Context, key Key, id string) error {
	err := c.do(ctx, key, http.MethodDelete, "/services/"+url.PathEscape(id), nil, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// Datastore kinds, by their path in Render's API.
const (
	Postgres = "postgres"
	KeyValue = "key-value"
)

// DatastoreSpec is a Postgres database or Key Value instance to make.
type DatastoreSpec struct {
	Kind    string
	Name    string
	Plan    string
	Region  string
	Version string // Postgres only
}

// Datastore is a Postgres database or Key Value instance.
type Datastore struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// FindDatastore finds the workspace's datastore of kind called name.
func (c *Client) FindDatastore(ctx context.Context, key Key, kind, name string) (*Datastore, error) {
	var page []map[string]Datastore
	q := url.Values{"name": {name}, "ownerId": {key.OwnerID}, "limit": {"20"}}
	if err := c.do(ctx, key, http.MethodGet, "/"+kind, q, nil, &page); err != nil {
		return nil, err
	}
	for _, p := range page {
		for _, d := range p {
			if d.Name == name {
				return &d, nil
			}
		}
	}
	return nil, nil
}

// EnsureDatastore makes the datastore unless the workspace has one called
// spec.Name, and waits until it's available.
func (c *Client) EnsureDatastore(ctx context.Context, key Key, spec DatastoreSpec) (*Datastore, error) {
	d, err := c.FindDatastore(ctx, key, spec.Kind, spec.Name)
	if err != nil {
		return nil, err
	}
	if d == nil {
		body := map[string]any{"name": spec.Name, "ownerId": key.OwnerID, "plan": spec.Plan, "region": spec.Region}
		if spec.Kind == Postgres && spec.Version != "" {
			body["version"] = spec.Version
		}
		d = &Datastore{}
		if err := c.do(ctx, key, http.MethodPost, "/"+spec.Kind, nil, body, d); err != nil {
			return nil, err
		}
	}
	for d.Status != resourceAvailable {
		progress.Report(ctx, "Waiting for Render to make "+spec.Name+" ("+d.Status+")")
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.poll):
		}
		if err := c.do(ctx, key, http.MethodGet, "/"+spec.Kind+"/"+url.PathEscape(d.ID), nil, nil, d); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// Address is a datastore's address inside Render's private network,
// credentials included.
func (c *Client) Address(ctx context.Context, key Key, kind, id string) (string, error) {
	var info struct {
		Internal string `json:"internalConnectionString"`
	}
	if err := c.do(ctx, key, http.MethodGet, "/"+kind+"/"+url.PathEscape(id)+"/connection-info", nil, nil, &info); err != nil {
		return "", err
	}
	if info.Internal == "" {
		return "", fmt.Errorf("render has no address for %s yet", id)
	}
	return info.Internal, nil
}

// GetDatastore reads a datastore.
func (c *Client) GetDatastore(ctx context.Context, key Key, kind, id string) (*Datastore, error) {
	var d Datastore
	if err := c.do(ctx, key, http.MethodGet, "/"+kind+"/"+url.PathEscape(id), nil, nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// DeleteDatastore deletes a datastore and its data. One that's already
// gone is not an error.
func (c *Client) DeleteDatastore(ctx context.Context, key Key, kind, id string) error {
	err := c.do(ctx, key, http.MethodDelete, "/"+kind+"/"+url.PathEscape(id), nil, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}
