package vercel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Target is the one Vercel environment a variable or a deploy belongs to:
// a built-in one ("production" or "preview"), or a custom environment by
// ID. Exactly one field is set.
type Target struct {
	Builtin  string
	CustomID string
}

// Production is the project's production environment.
var Production = Target{Builtin: "production"}

// Preview is the project's preview environment.
var Preview = Target{Builtin: "preview"}

// errNoTarget is a Target that names no environment, or two. A variable
// written for it would have no scope or several, and a deployment to the
// zero Target would silently go to Preview.
var errNoTarget = errors.New("vercel: a variable or deployment needs exactly one environment, and none was named")

// check refuses a Target that doesn't name exactly one environment.
func (t Target) check() error {
	if (t.Builtin == "") == (t.CustomID == "") {
		return errNoTarget
	}
	return nil
}

// CustomEnvironment is a project's custom environment, such as staging.
type CustomEnvironment struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

func customEnvPath(projectID string) string {
	return "/v9/projects/" + url.PathEscape(projectID) + "/custom-environments"
}

// GetCustomEnvironment reads a custom environment by ID or slug; a
// missing one is IsNotFound.
func (c *Client) GetCustomEnvironment(ctx context.Context, tok Token, projectID, idOrSlug string) (*CustomEnvironment, error) {
	var e CustomEnvironment
	if err := c.json(ctx, tok, http.MethodGet, customEnvPath(projectID)+"/"+url.PathEscape(idOrSlug), nil, nil, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// EnsureCustomEnvironment returns the project's custom environment with
// this slug, creating it when it's missing. Two environments racing to
// create the same one both end up with it.
func (c *Client) EnsureCustomEnvironment(ctx context.Context, tok Token, projectID, slug string) (*CustomEnvironment, error) {
	e, _, err := c.EnsureCustomEnvironmentCreated(ctx, tok, projectID, slug)
	return e, err
}

// EnsureCustomEnvironmentCreated is EnsureCustomEnvironment, and says
// whether this call created it. One that existed, or that another caller
// created first, is not created by this call.
func (c *Client) EnsureCustomEnvironmentCreated(ctx context.Context, tok Token, projectID, slug string) (*CustomEnvironment, bool, error) {
	e, err := c.GetCustomEnvironment(ctx, tok, projectID, slug)
	if err == nil {
		return e, false, nil
	}
	if !IsNotFound(err) {
		return nil, false, fmt.Errorf("read the vercel custom environment: %w", err)
	}
	var created CustomEnvironment
	err = c.json(ctx, tok, http.MethodPost, customEnvPath(projectID), nil, map[string]any{"slug": slug}, &created)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusConflict {
		if e, err = c.GetCustomEnvironment(ctx, tok, projectID, slug); err != nil {
			return nil, false, fmt.Errorf("read the vercel custom environment: %w", err)
		}
		return e, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("create the vercel custom environment: %w", err)
	}
	return &created, true, nil
}

// DeleteCustomEnvironment deletes a custom environment. One that's
// already gone is not an error.
func (c *Client) DeleteCustomEnvironment(ctx context.Context, tok Token, projectID, idOrSlug string) error {
	err := c.json(ctx, tok, http.MethodDelete, customEnvPath(projectID)+"/"+url.PathEscape(idOrSlug), nil, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// envScope is how a variable names its environment: `target` for the
// built-in ones, `customEnvironmentIds` for a custom one, never both.
func (t Target) envScope() map[string]any {
	if t.CustomID != "" {
		return map[string]any{"customEnvironmentIds": []string{t.CustomID}}
	}
	return map[string]any{"target": []string{t.Builtin}}
}

// stringList tolerates Vercel sending a target as one string or a list.
type stringList []string

func (s *stringList) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*s = stringList{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

// listQuery filters a variable list to t.
func (t Target) listQuery() url.Values {
	if t.CustomID != "" {
		return url.Values{"customEnvironmentId": {t.CustomID}}
	}
	return url.Values{"target": {t.Builtin}}
}

// only reports whether the row belongs to t and to no other environment.
func (t Target) only(target, custom stringList) bool {
	if t.CustomID != "" {
		return len(custom) == 1 && custom[0] == t.CustomID && len(target) == 0
	}
	return len(target) == 1 && target[0] == t.Builtin && len(custom) == 0
}

// DeleteEnv deletes the project's variables that belong to t alone.
// Variables shared with another environment are left in place.
func (c *Client) DeleteEnv(ctx context.Context, tok Token, projectID string, t Target) error {
	if err := t.check(); err != nil {
		return err
	}
	var out struct {
		Envs []struct {
			ID                   string     `json:"id"`
			Target               stringList `json:"target"`
			CustomEnvironmentIDs stringList `json:"customEnvironmentIds"`
		} `json:"envs"`
	}
	base := "/v9/projects/" + url.PathEscape(projectID) + "/env"
	if err := c.json(ctx, tok, http.MethodGet, "/v10/projects/"+url.PathEscape(projectID)+"/env", t.listQuery(), nil, &out); err != nil {
		return fmt.Errorf("list the project's variables: %w", err)
	}
	for _, e := range out.Envs {
		if e.ID == "" || !t.only(e.Target, e.CustomEnvironmentIDs) {
			continue
		}
		err := c.json(ctx, tok, http.MethodDelete, base+"/"+url.PathEscape(e.ID), nil, nil, nil)
		if err != nil && !IsNotFound(err) {
			return fmt.Errorf("delete a project variable: %w", err)
		}
	}
	return nil
}

// maxDeploymentPages bounds how far TargetDeployments walks a project's
// history.
const maxDeploymentPages = 20

// TargetDeployments lists the project's deployments in t, newest first.
// Vercel filters the built-in targets itself, but has no filter for a
// custom environment, so those are matched on the deployment's own
// customEnvironment.
func (c *Client) TargetDeployments(ctx context.Context, tok Token, projectID string, t Target) ([]Deployment, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	q := url.Values{"projectId": {projectID}, "limit": {"100"}}
	if t.CustomID == "" {
		q.Set("target", t.Builtin)
	}
	var all []Deployment
	for range maxDeploymentPages {
		var page struct {
			Deployments []struct {
				UID               string   `json:"uid"`
				URL               string   `json:"url"`
				ReadyState        string   `json:"readyState"`
				Alias             []string `json:"alias"`
				InspectorURL      string   `json:"inspectorUrl"`
				ErrorMessage      string   `json:"errorMessage"`
				CustomEnvironment *struct {
					ID string `json:"id"`
				} `json:"customEnvironment"`
			} `json:"deployments"`
			Pagination struct {
				Next *int64 `json:"next"`
			} `json:"pagination"`
		}
		if err := c.json(ctx, tok, http.MethodGet, "/v7/deployments", q, nil, &page); err != nil {
			return nil, fmt.Errorf("list the project's deployments: %w", err)
		}
		for _, d := range page.Deployments {
			if t.CustomID != "" && (d.CustomEnvironment == nil || d.CustomEnvironment.ID != t.CustomID) {
				continue
			}
			all = append(all, Deployment{ID: d.UID, URL: d.URL, ReadyState: d.ReadyState, Alias: d.Alias, InspectorURL: d.InspectorURL, ErrorMessage: d.ErrorMessage})
		}
		if page.Pagination.Next == nil || len(page.Deployments) == 0 {
			break
		}
		q.Set("until", fmt.Sprint(*page.Pagination.Next))
	}
	return all, nil
}

// DeleteDeployment deletes a deployment. One that's already gone is not
// an error.
func (c *Client) DeleteDeployment(ctx context.Context, tok Token, id string) error {
	err := c.json(ctx, tok, http.MethodDelete, "/v13/deployments/"+url.PathEscape(id), nil, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// FallbackReason says whether err means Vercel won't give this project a
// native environment, so the environment should fall back to the shared
// project. permanent is true when trying again can't help. Transient
// errors (transport failures, 429, 5xx) return ok == false: they fail
// the deploy and never record a fallback.
func FallbackReason(err error) (reason string, permanent, ok bool) {
	var ae *APIError
	if !errors.As(err, &ae) {
		return "", false, false
	}
	switch {
	case ae.Status == http.StatusPaymentRequired:
		return "Vercel says this needs a paid plan", false, true
	case ae.Status == http.StatusForbidden:
		return "Vercel refused: " + ae.Message, false, true
	case ae.Status == http.StatusBadRequest && strings.Contains(strings.ToLower(ae.Message), "reserved"):
		return "Vercel reserves that environment name: " + ae.Message, true, true
	}
	return "", false, false
}
