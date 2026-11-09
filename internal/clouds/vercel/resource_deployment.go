package vercel

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
)

// VerifyRuntimeResourceIsolation refuses project-level variables with the same
// names as resource runtime inputs, since project variables reach builds. It
// reads metadata without requesting plaintext values or changing foreign vars.
func (c *Client) VerifyRuntimeResourceIsolation(ctx context.Context, tok Token, projectID string, t Target, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if validateResourceID(projectID) != nil {
		return errors.New("vercel deployment project identifier is required")
	}
	if err := t.check(); err != nil {
		return err
	}
	var out struct {
		Envs []struct {
			Key    string   `json:"key"`
			Target []string `json:"target"`
		} `json:"envs"`
	}
	if err := c.resourceJSON(ctx, tok, http.MethodGet, "/v10/projects/"+url.PathEscape(projectID)+"/env", t.listQuery(), nil, &out); err != nil {
		return err
	}
	if out.Envs == nil {
		return errors.New("vercel did not confirm the deployment project's environment variable metadata")
	}
	for _, env := range out.Envs {
		if slices.Contains(keys, env.Key) {
			return errors.New("a resource runtime variable is already present in Vercel project settings; remove that project variable before deploying")
		}
	}
	return nil
}

// VerifyResourceProject confirms the credential bridge project's immutable ID
// and account before a resource can inject its application credentials there.
func (c *Client) VerifyResourceProject(ctx context.Context, tok Token, projectID, ownerID string) error {
	if validateResourceID(projectID) != nil || ownerID == "" {
		return errors.New("vercel resource project needs a pinned project and account")
	}
	var out struct {
		ID        string `json:"id"`
		AccountID string `json:"accountId"`
	}
	if err := c.resourceJSON(ctx, tok, http.MethodGet, "/v9/projects/"+url.PathEscape(projectID), nil, nil, &out); err != nil {
		return err
	}
	if out.ID != projectID || out.AccountID != ownerID {
		return errors.New("vercel resource project belongs to a different project or account")
	}
	return nil
}

// CreateDeploymentWithRuntimeEnv uses the same separate env and build.env
// request fields as the official Vercel CLI. Resource application credentials
// belong only to runtimeEnv, while buildEnv contains explicitly supplied build
// inputs. Neither environment is persisted as project configuration here.
func (c *Client) CreateDeploymentWithRuntimeEnv(ctx context.Context, tok Token, project *Project, t Target, files []File, meta, runtimeEnv, buildEnv map[string]string) (*Deployment, error) {
	body := map[string]any{
		"name": project.Name, "project": project.ID, "files": files, "meta": meta,
		"projectSettings": map[string]any{"framework": project.Framework, "rootDirectory": project.RootDirectory},
		"env":             runtimeEnv, "build": map[string]any{"env": buildEnv},
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
