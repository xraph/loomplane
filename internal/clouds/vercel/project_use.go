package vercel

import (
	"context"
	"net/http"
	"net/url"
)

// ProjectInUse reports whether a project still holds a custom environment
// or any deployment, as one an environment Loomplane no longer records,
// or something Loomplane didn't make, still uses. A project that's gone
// isn't in use.
func (c *Client) ProjectInUse(ctx context.Context, tok Token, projectID string) (bool, error) {
	var envs struct {
		Environments []CustomEnvironment `json:"environments"`
	}
	err := c.json(ctx, tok, http.MethodGet, customEnvPath(projectID), nil, nil, &envs)
	if IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, e := range envs.Environments {
		// Production, Preview and Development are every project's own.
		switch e.Slug {
		case "production", "preview", "development":
			continue
		}
		return true, nil
	}
	var page struct {
		Deployments []struct {
			UID string `json:"uid"`
		} `json:"deployments"`
	}
	err = c.json(ctx, tok, http.MethodGet, "/v7/deployments", url.Values{"projectId": {projectID}, "limit": {"1"}}, nil, &page)
	if IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(page.Deployments) > 0, nil
}
