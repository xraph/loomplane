package render

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Project is a Render project: a collection of environments.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Environment is one environment of a Render project. Its resources are
// the services, datastores and env groups it holds.
type Environment struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ProjectID string `json:"projectId"`
}

// EnsureProject finds the workspace's project called name, or makes it
// with firstEnvironment in it (Render won't make one with none).
func (c *Client) EnsureProject(ctx context.Context, key Key, name, firstEnvironment string) (*Project, error) {
	p, _, err := c.EnsureProjectCreated(ctx, key, name, firstEnvironment)
	return p, err
}

// EnsureProjectCreated is EnsureProject, and says whether this call made
// the project, and with it firstEnvironment.
func (c *Client) EnsureProjectCreated(ctx context.Context, key Key, name, firstEnvironment string) (*Project, bool, error) {
	var page []struct {
		Project Project `json:"project"`
	}
	q := url.Values{"name": {name}, "ownerId": {key.OwnerID}, "limit": {"20"}}
	if err := c.do(ctx, key, http.MethodGet, "/projects", q, nil, &page); err != nil {
		return nil, false, err
	}
	for _, p := range page {
		if p.Project.Name == name {
			pr := p.Project
			return &pr, false, nil
		}
	}
	body := map[string]any{
		"name": name, "ownerId": key.OwnerID,
		"environments": []map[string]string{{"name": firstEnvironment}},
	}
	var created Project
	if err := c.do(ctx, key, http.MethodPost, "/projects", nil, body, &created); err != nil {
		return nil, false, err
	}
	return &created, true, nil
}

func (c *Client) findEnvironment(ctx context.Context, key Key, projectID, name string) (*Environment, error) {
	var page []struct {
		Environment Environment `json:"environment"`
	}
	q := url.Values{"projectId": {projectID}, "name": {name}, "limit": {"20"}}
	if err := c.do(ctx, key, http.MethodGet, "/environments", q, nil, &page); err != nil {
		return nil, err
	}
	for _, p := range page {
		if p.Environment.Name == name && (p.Environment.ProjectID == "" || p.Environment.ProjectID == projectID) {
			e := p.Environment
			return &e, nil
		}
	}
	return nil, nil
}

// EnsureEnvironment finds the project's environment called name, or makes
// it. A 409 from a concurrent make is settled by reading it again.
func (c *Client) EnsureEnvironment(ctx context.Context, key Key, projectID, name string) (*Environment, error) {
	e, _, err := c.EnsureEnvironmentCreated(ctx, key, projectID, name)
	return e, err
}

// EnsureEnvironmentCreated is EnsureEnvironment, and says whether this
// call made the environment. One a concurrent caller made is not.
func (c *Client) EnsureEnvironmentCreated(ctx context.Context, key Key, projectID, name string) (*Environment, bool, error) {
	e, err := c.findEnvironment(ctx, key, projectID, name)
	if err != nil || e != nil {
		return e, false, err
	}
	var created Environment
	err = c.do(ctx, key, http.MethodPost, "/environments", nil, map[string]string{"name": name, "projectId": projectID}, &created)
	if err == nil {
		return &created, true, nil
	}
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusConflict {
		return nil, false, err
	}
	e, ferr := c.findEnvironment(ctx, key, projectID, name)
	if ferr != nil {
		return nil, false, ferr
	}
	if e == nil {
		return nil, false, err
	}
	return e, false, nil
}

// AddToEnvironment moves resources into an environment. They keep their
// IDs and data.
func (c *Client) AddToEnvironment(ctx context.Context, key Key, environmentID string, resourceIDs []string) error {
	return c.do(ctx, key, http.MethodPost, "/environments/"+url.PathEscape(environmentID)+"/resources", nil,
		map[string]any{"resourceIds": resourceIDs}, nil)
}

// EnvironmentEmpty tells whether an environment holds no services,
// datastores or env groups.
func (c *Client) EnvironmentEmpty(ctx context.Context, key Key, environmentID string) (bool, error) {
	ids, err := c.EnvironmentResources(ctx, key, environmentID)
	return len(ids) == 0, err
}

// EnvironmentResources lists the IDs of the services, datastores and env
// groups an environment holds.
func (c *Client) EnvironmentResources(ctx context.Context, key Key, environmentID string) ([]string, error) {
	var e struct {
		DatabasesIDs []string `json:"databasesIds"`
		RedisIDs     []string `json:"redisIds"`
		ServiceIDs   []string `json:"serviceIds"`
		EnvGroupIDs  []string `json:"envGroupIds"`
	}
	if err := c.do(ctx, key, http.MethodGet, "/environments/"+url.PathEscape(environmentID), nil, nil, &e); err != nil {
		return nil, err
	}
	return slices.Concat(e.DatabasesIDs, e.RedisIDs, e.ServiceIDs, e.EnvGroupIDs), nil
}

// DeleteEnvironment deletes an environment. One that's gone is not an
// error.
func (c *Client) DeleteEnvironment(ctx context.Context, key Key, id string) error {
	err := c.do(ctx, key, http.MethodDelete, "/environments/"+url.PathEscape(id), nil, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// DeleteProject deletes a project. One that's gone is not an error.
func (c *Client) DeleteProject(ctx context.Context, key Key, id string) error {
	err := c.do(ctx, key, http.MethodDelete, "/projects/"+url.PathEscape(id), nil, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// FallbackReason says whether err means native environments can't be used
// for this connection, so the caller should fall back. permanent is true
// when retrying can't help. Transient errors (transport, 429, 5xx) give
// ok == false: they fail the deploy and never cause a fallback. So does a
// 401: a key Render doesn't accept fails every request, not just these.
func FallbackReason(err error) (reason string, permanent, ok bool) {
	if err == nil {
		return "", false, false
	}
	var rej *RejectedError
	if errors.As(err, &rej) {
		if rej.Status == http.StatusForbidden {
			return "Render refused the request: the API key can't manage projects and environments", false, true
		}
		return "", false, false
	}
	var ae *APIError
	if !errors.As(err, &ae) {
		return "", false, false
	}
	switch ae.Status {
	case http.StatusPaymentRequired:
		return "Render's plan for this workspace doesn't include environments", false, true
	case http.StatusBadRequest:
		m := strings.ToLower(ae.Message)
		if strings.Contains(m, "plan") || strings.Contains(m, "limit") {
			return "Render's plan limit stops this workspace making an environment", false, true
		}
	}
	return "", false, false
}
