package vercel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Resource contains provider metadata only. Application credentials, config
// items and arbitrary integration metadata deliberately have no fields here.
type Resource struct {
	ID                 string               `json:"id"`
	Name               string               `json:"name"`
	Type               string               `json:"type"`
	OwnerID            string               `json:"ownerId,omitempty"`
	Region             string               `json:"region,omitempty"`
	Access             string               `json:"access,omitempty"`
	Status             string               `json:"status,omitempty"`
	Kind               string               `json:"kind,omitempty"`
	ExternalResourceID string               `json:"externalResourceId,omitempty"`
	Product            ResourceProduct      `json:"product,omitempty"`
	ProjectsMetadata   []ResourceConnection `json:"projectsMetadata,omitempty"`
}

// ResourceProduct keeps the actual Marketplace product and provider identity.
type ResourceProduct struct {
	ID                         string              `json:"id,omitempty"`
	Slug                       string              `json:"slug,omitempty"`
	Name                       string              `json:"name,omitempty"`
	IntegrationConfigurationID string              `json:"integrationConfigurationId,omitempty"`
	Integration                ResourceIntegration `json:"integration,omitempty"`
}

// ResourceIntegration identifies the underlying Marketplace provider.
type ResourceIntegration struct {
	ID   string `json:"id,omitempty"`
	Slug string `json:"slug,omitempty"`
	Name string `json:"name,omitempty"`
}

// ResourceConnection identifies the project and exact environment variable names
// that Vercel created for this resource, without their values.
type ResourceConnection struct {
	ID                   string   `json:"id,omitempty"`
	ProjectID            string   `json:"projectId"`
	Name                 string   `json:"name,omitempty"`
	Environments         []string `json:"environments,omitempty"`
	EnvironmentVariables []string `json:"environmentVariables,omitempty"`
}

// CreateResourceOptions contains consumer-account provisioning options. An
// integration requires an existing installation and its selected product.
type CreateResourceOptions struct {
	Name                       string
	Region                     string
	Access                     string
	IntegrationConfigurationID string
	IntegrationProductIDOrSlug string
	Metadata                   map[string]any
	ExternalID                 string
	BillingPlanID              string
}

var resourceIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func validateResourceID(id string) error {
	if !resourceIdentifier.MatchString(id) || len(id) > 256 {
		return errors.New("vercel resource identifier is invalid")
	}
	return nil
}

func validateResourceName(name string) error {
	if strings.TrimSpace(name) == "" || len(name) > 128 {
		return errors.New("vercel resource name is required and must be at most 128 characters")
	}
	return nil
}

// resourceJSON prevents upstream error bodies from leaking resource secrets.
func (c *Client) resourceJSON(ctx context.Context, tok Token, method, path string, query url.Values, in, out any) error {
	err := c.json(ctx, tok, method, path, query, in, out)
	if err == nil {
		return nil
	}
	var ae *APIError
	if errors.As(err, &ae) {
		safe := &APIError{Status: ae.Status, Message: "resource request failed"}
		if errors.Is(err, ErrRejected) {
			return fmt.Errorf("%w: %w", ErrRejected, safe)
		}
		return safe
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("vercel resource request or response failed")
}

func validateResource(tok Token, r Resource, expectedID string) error {
	if err := validateResourceID(r.ID); err != nil {
		return errors.New("vercel resource response omitted a valid immutable id")
	}
	if expectedID != "" && r.ID != expectedID {
		return errors.New("vercel resource response did not confirm the requested id")
	}
	if tok.TeamID != "" && r.OwnerID != "" && r.OwnerID != tok.TeamID {
		return errors.New("vercel resource belongs to a different account")
	}
	return nil
}

// ListStorageResources follows the same account-consumer endpoint as Vercel's
// CLI (util/integration-resource/get-resources.ts). It includes Blob and native
// Marketplace stores. The current API returns all stores without pagination;
// an unfamiliar continuation fails rather than reporting a complete inventory.
func (c *Client) ListStorageResources(ctx context.Context, tok Token) ([]Resource, error) {
	var out struct {
		Stores     []Resource      `json:"stores"`
		Pagination json.RawMessage `json:"pagination"`
		Cursor     string          `json:"cursor"`
		NextCursor string          `json:"nextCursor"`
		HasMore    bool            `json:"hasMore"`
	}
	if err := c.resourceJSON(ctx, tok, http.MethodGet, "/v1/storage/stores", nil, nil, &out); err != nil {
		return nil, err
	}
	if out.Stores == nil {
		return nil, errors.New("vercel storage inventory omitted stores")
	}
	var pagination struct {
		Next json.RawMessage `json:"next"`
	}
	if len(out.Pagination) > 0 && string(out.Pagination) != "null" {
		if err := json.Unmarshal(out.Pagination, &pagination); err != nil {
			return nil, errors.New("vercel storage pagination could not be verified")
		}
	}
	if out.Cursor != "" || out.NextCursor != "" || out.HasMore || (len(pagination.Next) > 0 && string(pagination.Next) != "null") {
		return nil, errors.New("vercel storage returned an unsupported pagination continuation")
	}
	for _, r := range out.Stores {
		if err := validateResource(tok, r, ""); err != nil {
			return nil, err
		}
	}
	return out.Stores, nil
}

// GetStorageResource retrieves fresh store metadata from the account consumer API.
func (c *Client) GetStorageResource(ctx context.Context, tok Token, id string) (Resource, error) {
	if err := validateResourceID(id); err != nil {
		return Resource{}, err
	}
	var out struct {
		Store Resource `json:"store"`
	}
	if err := c.resourceJSON(ctx, tok, http.MethodGet, "/v1/storage/stores/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return Resource{}, err
	}
	if err := validateResource(tok, out.Store, id); err != nil {
		return Resource{}, err
	}
	return out.Store, nil
}

// CreateBlobStore requires explicit visibility rather than accepting the API's
// public-store default. It never creates a project connection implicitly.
func (c *Client) CreateBlobStore(ctx context.Context, tok Token, opts CreateResourceOptions) (Resource, error) {
	if err := validateResourceName(opts.Name); err != nil {
		return Resource{}, err
	}
	if opts.Access != "public" && opts.Access != "private" {
		return Resource{}, errors.New("vercel blob access must be public or private")
	}
	in := struct {
		Name   string `json:"name"`
		Region string `json:"region,omitempty"`
		Access string `json:"access"`
	}{opts.Name, opts.Region, opts.Access}
	return c.createStorageResource(ctx, tok, "/v1/storage/stores/blob", "blob", in)
}

// CreateIntegrationStore calls the documented account-consumer provisioning
// API. It does not impersonate an integration vendor or install integrations.
func (c *Client) CreateIntegrationStore(ctx context.Context, tok Token, opts CreateResourceOptions) (Resource, error) {
	if err := validateResourceName(opts.Name); err != nil {
		return Resource{}, err
	}
	if validateResourceID(opts.IntegrationConfigurationID) != nil || validateResourceID(opts.IntegrationProductIDOrSlug) != nil {
		return Resource{}, errors.New("vercel marketplace installation and product are required")
	}
	in := struct {
		Name                       string         `json:"name"`
		IntegrationConfigurationID string         `json:"integrationConfigurationId"`
		IntegrationProductIDOrSlug string         `json:"integrationProductIdOrSlug"`
		Metadata                   map[string]any `json:"metadata"`
		ExternalID                 string         `json:"externalId,omitempty"`
		BillingPlanID              string         `json:"billingPlanId,omitempty"`
	}{opts.Name, opts.IntegrationConfigurationID, opts.IntegrationProductIDOrSlug, opts.Metadata, opts.ExternalID, opts.BillingPlanID}
	if in.Metadata == nil {
		in.Metadata = map[string]any{}
	}
	return c.createStorageResource(ctx, tok, "/v1/storage/stores/integration/direct", "integration", in)
}

func (c *Client) createStorageResource(ctx context.Context, tok Token, path, typ string, in any) (Resource, error) {
	var out struct {
		Store Resource `json:"store"`
	}
	if err := c.resourceJSON(ctx, tok, http.MethodPost, path, nil, in, &out); err != nil {
		return Resource{}, err
	}
	if err := validateResource(tok, out.Store, ""); err != nil {
		return Resource{}, err
	}
	if out.Store.Type == "" {
		out.Store.Type = typ
	}
	if out.Store.Type != typ {
		return Resource{}, errors.New("vercel created a different resource type")
	}
	return out.Store, nil
}

// DeleteBlobStore removes a Blob store. Existing project connections must be
// detached explicitly by the caller; this method does not remove other access.
func (c *Client) DeleteBlobStore(ctx context.Context, tok Token, id string) error {
	return c.deleteResource(ctx, tok, "/v1/storage/stores/blob/", id)
}

// DeleteIntegrationStore removes a Marketplace store through the consumer API.
func (c *Client) DeleteIntegrationStore(ctx context.Context, tok Token, id string) error {
	return c.deleteResource(ctx, tok, "/v1/storage/stores/integration/", id)
}

func (c *Client) deleteResource(ctx context.Context, tok Token, prefix, id string) error {
	if err := validateResourceID(id); err != nil {
		return err
	}
	return c.resourceJSON(ctx, tok, http.MethodDelete, prefix+url.PathEscape(id), nil, nil, nil)
}

type globalConfigMetadata struct {
	ID      string `json:"id"`
	Slug    string `json:"slug"`
	OwnerID string `json:"ownerId"`
}

func (m globalConfigMetadata) resource() Resource {
	return Resource{ID: m.ID, Name: m.Slug, OwnerID: m.OwnerID, Type: "global-config"}
}

// ListGlobalConfigs returns metadata without reading config items or tokens.
func (c *Client) ListGlobalConfigs(ctx context.Context, tok Token) ([]Resource, error) {
	var out []globalConfigMetadata
	if err := c.resourceJSON(ctx, tok, http.MethodGet, "/v1/global-config", nil, nil, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("vercel global config inventory omitted its array")
	}
	resources := make([]Resource, 0, len(out))
	for _, m := range out {
		r := m.resource()
		if err := validateResource(tok, r, ""); err != nil {
			return nil, err
		}
		resources = append(resources, r)
	}
	return resources, nil
}

// GetGlobalConfig returns metadata using the current Global Config API.
func (c *Client) GetGlobalConfig(ctx context.Context, tok Token, id string) (Resource, error) {
	if err := validateResourceID(id); err != nil {
		return Resource{}, err
	}
	var out globalConfigMetadata
	if err := c.resourceJSON(ctx, tok, http.MethodGet, "/v1/global-config/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return Resource{}, err
	}
	r := out.resource()
	return r, validateResource(tok, r, id)
}

// CreateGlobalConfig creates an empty config without creating application tokens.
func (c *Client) CreateGlobalConfig(ctx context.Context, tok Token, opts CreateResourceOptions) (Resource, error) {
	if err := validateResourceName(opts.Name); err != nil {
		return Resource{}, err
	}
	var out globalConfigMetadata
	in := struct {
		Slug string `json:"slug"`
	}{opts.Name}
	if err := c.resourceJSON(ctx, tok, http.MethodPost, "/v1/global-config", nil, in, &out); err != nil {
		return Resource{}, err
	}
	r := out.resource()
	return r, validateResource(tok, r, "")
}

// DeleteGlobalConfig deletes a config and its scoped tokens.
func (c *Client) DeleteGlobalConfig(ctx context.Context, tok Token, id string) error {
	return c.deleteResource(ctx, tok, "/v1/global-config/", id)
}

// ConnectResourceOptions chooses one project and explicit deployment environments.
type ConnectResourceOptions struct {
	ProjectID    string
	Environments []string
	EnvVarPrefix string
}

// ConnectStorageResource lets Vercel inject the store's own application secrets.
func (c *Client) ConnectStorageResource(ctx context.Context, tok Token, id string, opts ConnectResourceOptions) error {
	if validateResourceID(id) != nil || validateResourceID(opts.ProjectID) != nil || len(opts.Environments) == 0 {
		return errors.New("vercel store connection requires a resource, project and environments")
	}
	for _, environment := range opts.Environments {
		if validateResourceID(environment) != nil {
			return errors.New("vercel store connection environment is invalid")
		}
	}
	in := struct {
		ProjectID    string   `json:"projectId"`
		Environments []string `json:"envVarEnvironments"`
		Type         string   `json:"type"`
		EnvVarPrefix string   `json:"envVarPrefix,omitempty"`
	}{opts.ProjectID, opts.Environments, "integration", opts.EnvVarPrefix}
	return c.resourceJSON(ctx, tok, http.MethodPost, "/v1/storage/stores/"+url.PathEscape(id)+"/connections", nil, in, nil)
}

// DisconnectStorageResource removes only the named project connection.
func (c *Client) DisconnectStorageResource(ctx context.Context, tok Token, id, connectionID string) error {
	if validateResourceID(id) != nil || validateResourceID(connectionID) != nil {
		return errors.New("vercel resource and connection identifiers are required")
	}
	return c.resourceJSON(ctx, tok, http.MethodDelete, "/v1/storage/stores/"+url.PathEscape(id)+"/connections/"+url.PathEscape(connectionID), nil, nil, nil)
}
