package vercel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/progress"
)

// Project is a Vercel project.
type Project struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Framework     *string `json:"framework"`
	RootDirectory *string `json:"rootDirectory"`
	Targets       map[string]struct {
		ID string `json:"id"`
	} `json:"targets,omitempty"`
}

// Production is the deployment the project's production domain serves,
// or empty.
func (p *Project) Production() string { return p.Targets["production"].ID }

// ProjectSettings are the project settings Loomplane keeps in step with
// the manifest. A nil framework lets Vercel detect it.
type ProjectSettings struct {
	Framework     *string `json:"framework"`
	RootDirectory *string `json:"rootDirectory"`
}

// GetProject reads a project by ID or name; a missing one is IsNotFound.
func (c *Client) GetProject(ctx context.Context, tok Token, idOrName string) (*Project, error) {
	var p Project
	if err := c.json(ctx, tok, http.MethodGet, "/v9/projects/"+url.PathEscape(idOrName), nil, nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// EnsureProject creates a project with settings, or brings an existing
// one's settings in step.
func (c *Client) EnsureProject(ctx context.Context, tok Token, name string, s ProjectSettings) (*Project, error) {
	p, _, err := c.EnsureProjectCreated(ctx, tok, name, s)
	return p, err
}

// EnsureProjectCreated is EnsureProject, and says whether this call
// created the project. One that existed, or that another caller created
// first, is not created by this call.
func (c *Client) EnsureProjectCreated(ctx context.Context, tok Token, name string, s ProjectSettings) (*Project, bool, error) {
	p, err := c.GetProject(ctx, tok, name)
	switch {
	case IsNotFound(err):
		var created Project
		body := map[string]any{"name": name, "framework": s.Framework, "rootDirectory": s.RootDirectory}
		cerr := c.json(ctx, tok, http.MethodPost, "/v10/projects", nil, body, &created)
		var ae *APIError
		if errors.As(cerr, &ae) && ae.Status == http.StatusConflict {
			// Another environment of the service made it first.
			p, err = c.GetProject(ctx, tok, name)
			return p, false, err
		}
		if cerr != nil {
			return nil, false, fmt.Errorf("create the vercel project: %w", cerr)
		}
		return &created, true, nil
	case err != nil:
		return nil, false, err
	}
	if !same(p.Framework, s.Framework) || !same(p.RootDirectory, s.RootDirectory) {
		if err := c.json(ctx, tok, http.MethodPatch, "/v9/projects/"+url.PathEscape(p.ID), nil, s, p); err != nil {
			return nil, false, fmt.Errorf("update the vercel project: %w", err)
		}
	}
	return p, false, nil
}

func same(a, b *string) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

// SetEnv sets variables on one target of a project, replacing earlier
// values of the same keys. Every value is encrypted; the secret ones are
// sensitive too, so Vercel never shows them again.
func (c *Client) SetEnv(ctx context.Context, tok Token, projectID string, t Target, env map[string]string, secret map[string]bool) error {
	if err := t.check(); err != nil {
		return err
	}
	if len(env) == 0 {
		return nil
	}
	vars := make([]map[string]any, 0, len(env))
	for k, v := range env {
		kind := "encrypted"
		if secret[k] {
			kind = "sensitive"
		}
		item := t.envScope()
		item["key"], item["value"], item["type"] = k, v, kind
		vars = append(vars, item)
	}
	q := url.Values{"upsert": {"true"}}
	if err := c.json(ctx, tok, http.MethodPost, "/v10/projects/"+url.PathEscape(projectID)+"/env", q, vars, nil); err != nil {
		return fmt.Errorf("set the project's variables: %w", err)
	}
	return nil
}

// UploadFile uploads one file's content, named by its SHA-1. Vercel keeps
// each content once, so uploading it again is harmless.
func (c *Client) UploadFile(ctx context.Context, tok Token, sha string, data []byte) error {
	q := url.Values{}
	if tok.TeamID != "" {
		q.Set("teamId", tok.TeamID)
	}
	u := c.endpoint + "/v2/files"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(tok.Value))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("x-vercel-digest", sha)
	req.ContentLength = int64(len(data))
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("vercel: upload: %w", err)
	}
	defer res.Body.Close() // read-only
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096)) //nolint:errcheck // only for the message
		return &APIError{Status: res.StatusCode, Message: strings.TrimSpace(string(body))}
	}
	return nil
}

// File is one file of a deployment.
type File struct {
	File string `json:"file"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

// Deployment is a Vercel deployment.
type Deployment struct {
	ID            string   `json:"id"`
	URL           string   `json:"url"`
	ReadyState    string   `json:"readyState"`
	Alias         []string `json:"alias"`
	AliasAssigned bool     `json:"aliasAssigned"`
	InspectorURL  string   `json:"inspectorUrl"`
	ErrorMessage  string   `json:"errorMessage"`
}

// MissingFilesError is a deployment Vercel refused because it hasn't got
// some of its files yet.
type MissingFilesError struct{ SHAs []string }

func (e *MissingFilesError) Error() string {
	return fmt.Sprintf("vercel is missing %d of the deployment's files", len(e.SHAs))
}

// deployTarget adds t to a deployment body. Production names itself,
// Preview is Vercel's default so it names nothing, and a custom
// environment is named by ID. The zero Target is refused, since it would
// otherwise deploy to Preview.
func (t Target) deployTarget(body map[string]any) error {
	if err := t.check(); err != nil {
		return err
	}
	switch {
	case t.CustomID != "":
		body["customEnvironmentSlugOrId"] = t.CustomID
	case t.Builtin == Production.Builtin:
		body["target"] = "production"
	}
	return nil
}

// CreateDeployment deploys files to one target of a project. meta is
// shown on the deployment, such as the commit it's from.
func (c *Client) CreateDeployment(ctx context.Context, tok Token, project *Project, t Target, files []File, meta map[string]string) (*Deployment, error) {
	body := map[string]any{
		"name": project.Name, "project": project.ID, "files": files, "meta": meta,
		"projectSettings": map[string]any{"framework": project.Framework, "rootDirectory": project.RootDirectory},
	}
	if err := t.deployTarget(body); err != nil {
		return nil, err
	}
	var d Deployment
	err := c.json(ctx, tok, http.MethodPost, "/v13/deployments", url.Values{"skipAutoDetectionConfirmation": {"1"}}, body, &d)
	var ae *APIError
	if errors.As(err, &ae) && ae.Code == "missing_files" {
		return nil, &MissingFilesError{SHAs: ae.Missing}
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// GetDeployment reads a deployment.
func (c *Client) GetDeployment(ctx context.Context, tok Token, id string) (*Deployment, error) {
	var d Deployment
	if err := c.json(ctx, tok, http.MethodGet, "/v13/deployments/"+url.PathEscape(id), nil, nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// WaitDeployment polls a deployment of target t until it's ready, and
// fails if its build failed. A production deployment also waits for its
// production domain to point at it; Preview and custom environments are
// done once they're ready.
func (c *Client) WaitDeployment(ctx context.Context, tok Token, id string, t Target) (*Deployment, error) {
	for {
		d, err := c.GetDeployment(ctx, tok, id)
		if err != nil {
			return nil, err
		}
		switch d.ReadyState {
		case "READY":
			if t != Production || d.AliasAssigned || len(d.Alias) > 0 {
				return d, nil
			}
		case "ERROR", "CANCELED":
			msg := strings.TrimSpace(d.ErrorMessage)
			if msg == "" {
				msg = strings.ToLower(d.ReadyState)
			}
			return d, fmt.Errorf("vercel deployment %s: %s. Its log: %s", id, msg, d.InspectorURL)
		}
		progress.Report(ctx, "Building on Vercel ("+strings.ToLower(d.ReadyState)+")")
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.poll):
		}
	}
}

// DeleteProject deletes a project and its deployments. One that's
// already gone is not an error.
func (c *Client) DeleteProject(ctx context.Context, tok Token, id string) error {
	err := c.json(ctx, tok, http.MethodDelete, "/v9/projects/"+url.PathEscape(id), nil, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// Address is the https address of a deployment: its production domain
// when it has one.
func (d *Deployment) Address() string {
	host := d.URL
	if len(d.Alias) > 0 {
		host = d.Alias[0]
	}
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return host
	}
	return "https://" + host
}
