package vercel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
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

// includes reports whether the row belongs to t, alone or with others.
func (t Target) includes(target, custom stringList) bool {
	if t.CustomID != "" {
		return slices.Contains(custom, t.CustomID)
	}
	return slices.Contains(target, t.Builtin)
}

// without is the scope a shared row keeps once t is taken out of it. Both
// lists are always sent, as listed less t: Vercel doesn't say whether a
// list left out of an update is kept or cleared, and clearing one would
// take the variable from another environment.
func (t Target) without(target, custom stringList) map[string]any {
	drop := func(l stringList, x string) []string {
		out := []string{}
		for _, v := range l {
			if v != x {
				out = append(out, v)
			}
		}
		return out
	}
	if t.CustomID != "" {
		return map[string]any{"target": drop(target, ""), "customEnvironmentIds": drop(custom, t.CustomID)}
	}
	return map[string]any{"target": drop(target, t.Builtin), "customEnvironmentIds": drop(custom, "")}
}

// envRow is one of a project's variables as Vercel lists it, without its
// value.
type envRow struct {
	ID                   string     `json:"id"`
	Key                  string     `json:"key"`
	Type                 string     `json:"type"`
	Target               stringList `json:"target"`
	CustomEnvironmentIDs stringList `json:"customEnvironmentIds"`
	GitBranch            string     `json:"gitBranch"`
}

// maxEnvPages bounds how many pages of a project's variables are read.
const maxEnvPages = 20

func envPath(projectID string) string { return "/projects/" + url.PathEscape(projectID) + "/env" }

// errEnvListTruncated is a listing that ran past maxEnvPages with more
// to come. Acting on part of it could miss a row that matters.
var errEnvListTruncated = fmt.Errorf("vercel: the project has more than %d pages of variables", maxEnvPages)

// listEnv lists the project's variables matching q, every page of them,
// never asking Vercel to decrypt a value. A row listed twice is returned
// once.
func (c *Client) listEnv(ctx context.Context, tok Token, projectID string, q url.Values) ([]envRow, error) {
	q.Set("decrypt", "false")
	var all []envRow
	seen := map[string]bool{}
	var last *int64
	for page := 1; ; page++ {
		var res struct {
			Envs       []envRow `json:"envs"`
			Pagination *struct {
				Next *int64 `json:"next"`
			} `json:"pagination"`
		}
		if err := c.json(ctx, tok, http.MethodGet, "/v10"+envPath(projectID), q, nil, &res); err != nil {
			return nil, err
		}
		for _, r := range res.Envs {
			if r.ID != "" {
				if seen[r.ID] {
					continue
				}
				seen[r.ID] = true
			}
			all = append(all, r)
		}
		if res.Pagination == nil || res.Pagination.Next == nil || len(res.Envs) == 0 || (last != nil && *last == *res.Pagination.Next) {
			return all, nil
		}
		if page == maxEnvPages {
			return nil, errEnvListTruncated
		}
		last = res.Pagination.Next
		q.Set("until", fmt.Sprint(*last))
	}
}

// deleteEnvRow deletes one variable. One that's already gone is not an
// error.
func (c *Client) deleteEnvRow(ctx context.Context, tok Token, projectID, id string) error {
	err := c.json(ctx, tok, http.MethodDelete, "/v9"+envPath(projectID)+"/"+url.PathEscape(id), nil, nil, nil)
	if err != nil && !IsNotFound(err) {
		return err
	}
	return nil
}

// SetEnv sets variables on one target of a project, replacing earlier
// values of the same keys for that target and no other. Every value is
// encrypted; the secret ones are sensitive too, so Vercel never shows
// them again.
//
// Vercel's upsert matches a variable by key alone, so it would overwrite
// another environment's row for the same key. SetEnv reads the project's
// rows instead: a row for t alone is updated in place, a row t shares
// with other environments loses t and keeps its value for them, and t
// then gets a row of its own. Rows of other environments, and preview
// rows for one git branch, are never changed.
func (c *Client) SetEnv(ctx context.Context, tok Token, projectID string, t Target, env map[string]string, secret map[string]bool) error {
	if err := t.check(); err != nil {
		return err
	}
	if len(env) == 0 {
		return nil
	}
	rows, err := c.listEnv(ctx, tok, projectID, url.Values{})
	if err != nil {
		return fmt.Errorf("list the project's variables: %w", err)
	}
	own := map[string]envRow{}
	shared := map[string][]envRow{}
	for _, r := range rows {
		if _, want := env[r.Key]; !want || r.ID == "" || r.GitBranch != "" || !t.includes(r.Target, r.CustomEnvironmentIDs) {
			continue
		}
		if t.only(r.Target, r.CustomEnvironmentIDs) {
			own[r.Key] = r
		} else {
			shared[r.Key] = append(shared[r.Key], r)
		}
	}
	base := "/v9" + envPath(projectID)
	// creates are the new rows for keys t had no row for. A key whose row
	// was narrowed or deleted gets its new row at once instead, so a later
	// key's failure never leaves t without it.
	creates := make([]map[string]any, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		kind := "encrypted"
		if secret[k] {
			kind = "sensitive"
		}
		item := t.envScope()
		item["value"], item["type"] = env[k], kind
		took := false
		for _, s := range shared[k] {
			if err := c.json(ctx, tok, http.MethodPatch, base+"/"+url.PathEscape(s.ID), nil, t.without(s.Target, s.CustomEnvironmentIDs), nil); err != nil {
				return fmt.Errorf("take %s out of a shared project variable: %w", k, err)
			}
			took = true
		}
		if row, ok := own[k]; ok {
			// A secret's value is never sent to a row that isn't
			// sensitive, even one about to become so.
			replace := kind == "sensitive" && row.Type != "sensitive"
			if !replace {
				err := c.json(ctx, tok, http.MethodPatch, base+"/"+url.PathEscape(row.ID), nil, item, nil)
				if err == nil {
					continue
				}
				var ae *APIError
				if !errors.As(err, &ae) || ae.Status != http.StatusBadRequest || row.Type == kind {
					return fmt.Errorf("update the project variable %s: %w", k, err)
				}
				// Vercel won't move this row to the new type.
			}
			if err := c.deleteEnvRow(ctx, tok, projectID, row.ID); err != nil {
				return fmt.Errorf("replace the project variable %s: %w", k, err)
			}
			took = true
		}
		item["key"] = k
		if took {
			if err := c.createEnv(ctx, tok, projectID, []map[string]any{item}); err != nil {
				return err
			}
			continue
		}
		creates = append(creates, item)
	}
	if len(creates) == 0 {
		return nil
	}
	return c.createEnv(ctx, tok, projectID, creates)
}

// createEnv creates rows in one request, without upsert. Vercel answers
// 201 even when some weren't created, listing those as failed.
func (c *Client) createEnv(ctx context.Context, tok Token, projectID string, items []map[string]any) error {
	var out struct {
		Failed []struct {
			Error struct {
				Code      string `json:"code"`
				Key       string `json:"key"`
				EnvVarKey string `json:"envVarKey"`
				Message   string `json:"message"`
			} `json:"error"`
		} `json:"failed"`
	}
	if err := c.json(ctx, tok, http.MethodPost, "/v10"+envPath(projectID), nil, items, &out); err != nil {
		return fmt.Errorf("set the project's variables: %w", err)
	}
	if len(out.Failed) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(out.Failed))
	for _, f := range out.Failed {
		key := f.Error.Key
		if key == "" {
			key = f.Error.EnvVarKey
		}
		msgs = append(msgs, fmt.Sprintf("%s: %s (%s)", key, f.Error.Message, f.Error.Code))
	}
	return fmt.Errorf("set the project's variables: vercel didn't create %s", strings.Join(msgs, "; "))
}

// DeleteEnv deletes the project's variables that belong to t alone.
// Variables shared with another environment are left in place.
func (c *Client) DeleteEnv(ctx context.Context, tok Token, projectID string, t Target) error {
	if err := t.check(); err != nil {
		return err
	}
	rows, err := c.listEnv(ctx, tok, projectID, t.listQuery())
	if err != nil {
		return fmt.Errorf("list the project's variables: %w", err)
	}
	for _, e := range rows {
		if e.ID == "" || !t.only(e.Target, e.CustomEnvironmentIDs) {
			continue
		}
		if err := c.deleteEnvRow(ctx, tok, projectID, e.ID); err != nil {
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
		if msg := strings.TrimSpace(ae.Message); msg != "" {
			return "Vercel says this needs a paid plan: " + msg, false, true
		}
		return "Vercel says this needs a paid plan", false, true
	case ae.Status == http.StatusForbidden:
		return "Vercel refused: " + ae.Message, false, true
	case ae.Status == http.StatusBadRequest && strings.Contains(strings.ToLower(ae.Message), "reserved"):
		return "Vercel reserves that environment name: " + ae.Message, true, true
	}
	return "", false, false
}
