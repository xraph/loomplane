package vercel

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// CredentialResult is private application access material, never inventory.
// JSON and ordinary formatting deliberately do not reveal its values.
type CredentialResult struct {
	Values  map[string]string `json:"-"`
	TokenID string            `json:"-"`
}

func (CredentialResult) String() string   { return "[private Vercel application credentials]" }
func (CredentialResult) GoString() string { return "[private Vercel application credentials]" }

// CreateGlobalConfigReadToken creates the config-scoped read token documented
// by Vercel's Global Config API. It never exposes the account access token.
func (c *Client) CreateGlobalConfigReadToken(ctx context.Context, tok Token, id, label string) (CredentialResult, error) {
	if err := validateResourceID(id); err != nil {
		return CredentialResult{}, err
	}
	if err := validateResourceName(label); err != nil {
		return CredentialResult{}, err
	}
	var out struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	in := struct {
		Label string `json:"label"`
	}{label}
	if err := c.resourceJSON(ctx, tok, http.MethodPost, "/v1/global-config/"+url.PathEscape(id)+"/token", nil, in, &out); err != nil {
		return CredentialResult{}, err
	}
	if out.Token == "" || out.ID == "" || out.Token == tok.Value {
		return CredentialResult{}, errors.New("vercel did not return a scoped global config read token")
	}
	connection := "https://global-config.vercel.com/" + url.PathEscape(id) + "?" + url.Values{"token": {out.Token}}.Encode()
	return CredentialResult{TokenID: out.ID, Values: map[string]string{"GLOBAL_CONFIG": connection}}, nil
}

// RevokeGlobalConfigReadToken compensates for a failed attachment without
// deleting the config or affecting tokens created by other applications.
func (c *Client) RevokeGlobalConfigReadToken(ctx context.Context, tok Token, id, tokenID string) error {
	if validateResourceID(id) != nil || validateResourceID(tokenID) != nil {
		return errors.New("vercel config and token identifiers are required")
	}
	in := struct {
		IDs []string `json:"ids"`
	}{[]string{tokenID}}
	return c.resourceJSON(ctx, tok, http.MethodDelete, "/v1/global-config/"+url.PathEscape(id)+"/tokens", nil, in, nil)
}

// ReadStorageResourceCredentials reads only environment variable names belonging
// to this store's existing project connection. This is the same project env
// endpoint used by Vercel CLI. Unrelated project credentials never leave this
// method. Sensitive values that Vercel refuses to decrypt remain an error.
func (c *Client) ReadStorageResourceCredentials(ctx context.Context, tok Token, id, projectID, environment string) (CredentialResult, error) {
	if validateResourceID(projectID) != nil || validateResourceID(environment) != nil {
		return CredentialResult{}, errors.New("vercel resource credentials require a project and environment")
	}
	resource, err := c.GetStorageResource(ctx, tok, id)
	if err != nil {
		return CredentialResult{}, err
	}
	wanted := map[string]bool{}
	for _, connection := range resource.ProjectsMetadata {
		if connection.ProjectID == projectID && slices.Contains(connection.Environments, environment) {
			for _, name := range connection.EnvironmentVariables {
				if name != "" {
					wanted[name] = true
				}
			}
		}
	}
	if len(wanted) == 0 {
		return CredentialResult{}, errors.New("vercel resource has no credential mapping for this project environment")
	}
	var out struct {
		Envs []struct {
			ID                   string   `json:"id"`
			Key                  string   `json:"key"`
			Value                string   `json:"value"`
			Target               []string `json:"target"`
			GitBranch            string   `json:"gitBranch"`
			CustomEnvironmentIDs []string `json:"customEnvironmentIds"`
		} `json:"envs"`
	}
	query := url.Values{"decrypt": {"true"}}
	if environment == "production" || environment == "preview" || environment == "development" {
		query.Set("target", environment)
	} else {
		query.Set("customEnvironmentId", environment)
	}
	if err := c.resourceJSON(ctx, tok, http.MethodGet, "/v10/projects/"+url.PathEscape(projectID)+"/env", query, nil, &out); err != nil {
		return CredentialResult{}, err
	}
	values := map[string]string{}
	for _, env := range out.Envs {
		if !wanted[env.Key] {
			continue
		}
		if env.GitBranch != "" {
			continue
		}
		if (len(env.Target) > 0 || len(env.CustomEnvironmentIDs) > 0) && !slices.Contains(env.Target, environment) && !slices.Contains(env.CustomEnvironmentIDs, environment) {
			continue
		}
		if env.Key == "" || env.Value == "" || env.Value == tok.Value || strings.EqualFold(env.Value, "sensitive") || env.Value == "<sensitive>" {
			return CredentialResult{}, errors.New("vercel did not reveal the resource application credential; supply scoped credentials explicitly")
		}
		if _, duplicate := values[env.Key]; duplicate {
			return CredentialResult{}, errors.New("vercel resource credential mapping contains duplicate keys")
		}
		values[env.Key] = env.Value
	}
	if len(values) != len(wanted) {
		return CredentialResult{}, errors.New("vercel did not return every resource credential for this environment")
	}
	if resource.Type == "blob" {
		found := false
		for key, value := range values {
			if !strings.HasSuffix(key, "_READ_WRITE_TOKEN") {
				continue
			}
			parts := strings.SplitN(value, "_", 5)
			if len(parts) != 5 || parts[0] != "vercel" || parts[1] != "blob" || parts[2] != "rw" || parts[3] != strings.TrimPrefix(id, "store_") || parts[4] == "" {
				return CredentialResult{}, errors.New("the Vercel Blob application token belongs to a different store")
			}
			found = true
		}
		if !found {
			return CredentialResult{}, errors.New("vercel did not provide a static application token for this Blob store")
		}
	}
	return CredentialResult{Values: values}, nil
}
