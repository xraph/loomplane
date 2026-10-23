package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type NativeRecoverySource struct {
	ID            string     `json:"id"`
	OrgID         string     `json:"org_id"`
	ProjectID     string     `json:"project_id"`
	EnvironmentID string     `json:"environment_id"`
	Name          string     `json:"name"`
	Provider      string     `json:"provider"`
	ConnectionID  string     `json:"connection_id"`
	Region        string     `json:"region"`
	PhysicalID    string     `json:"physical_id"`
	CapturedAt    time.Time  `json:"captured_at"`
	State         string     `json:"state"`
	Complete      bool       `json:"complete"`
	CheckedAt     *time.Time `json:"checked_at,omitempty"`
	ObservedAt    *time.Time `json:"observed_at,omitempty"`
	Error         string     `json:"error,omitempty"`
}
type NativeRecoveryPoint struct {
	ID         string     `json:"id"`
	SourceID   string     `json:"source_id"`
	ProviderID string     `json:"provider_id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	Engine     string     `json:"engine"`
	Version    string     `json:"version"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	Encrypted  bool       `json:"encrypted"`
	KMSKey     string     `json:"kms_key,omitempty"`
	Present    bool       `json:"present"`
	LastSeenAt time.Time  `json:"last_seen_at"`
}
type NativeRecoverySources struct {
	Sources    []NativeRecoverySource `json:"sources"`
	NextBefore string                 `json:"next_before,omitempty"`
}
type NativeRecoveryInventory struct {
	Source     NativeRecoverySource  `json:"source"`
	Points     []NativeRecoveryPoint `json:"points"`
	NextBefore string                `json:"next_before,omitempty"`
}

// NativeRecoveryPage keeps each cursor inside its environment and resource.
type NativeRecoveryPage struct {
	EnvironmentID string
	Resource      string
	Before        string
	Limit         int
}

func nativeRecoveryQuery(page NativeRecoveryPage) (string, error) {
	if page.Limit < 0 || page.Limit > 100 {
		return "", fmt.Errorf("limit must be from 1 to 100")
	}
	query := url.Values{}
	if page.EnvironmentID != "" {
		query.Set("environment_id", page.EnvironmentID)
	}
	if page.Resource != "" {
		query.Set("resource", page.Resource)
	}
	if page.Before != "" {
		query.Set("before", page.Before)
	}
	if page.Limit > 0 {
		query.Set("limit", strconv.Itoa(page.Limit))
	}
	if len(query) == 0 {
		return "", nil
	}
	return "?" + query.Encode(), nil
}
func (c *Client) NativeRecoverySources(ctx context.Context, project, before string, limit int) (*NativeRecoverySources, error) {
	return c.NativeRecoverySourcesPage(ctx, project, NativeRecoveryPage{Before: before, Limit: limit})
}

func (c *Client) NativeRecoverySourcesPage(ctx context.Context, project string, page NativeRecoveryPage) (*NativeRecoverySources, error) {
	query, err := nativeRecoveryQuery(page)
	if err != nil {
		return nil, err
	}
	var out NativeRecoverySources
	err = c.do(ctx, http.MethodGet, "/api/v1/projects/"+esc(project)+"/native-recovery-sources"+query, nil, &out)
	return &out, err
}
func (c *Client) NativeRecoveryInventory(ctx context.Context, source, before string, limit int) (*NativeRecoveryInventory, error) {
	return c.NativeRecoveryInventoryPage(ctx, source, NativeRecoveryPage{Before: before, Limit: limit})
}

func (c *Client) NativeRecoveryInventoryPage(ctx context.Context, source string, page NativeRecoveryPage) (*NativeRecoveryInventory, error) {
	query, err := nativeRecoveryQuery(page)
	if err != nil {
		return nil, err
	}
	var out NativeRecoveryInventory
	err = c.do(ctx, http.MethodGet, "/api/v1/native-recovery-sources/"+esc(source)+query, nil, &out)
	return &out, err
}
func (c *Client) RefreshNativeRecovery(ctx context.Context, source string) (*NativeRecoveryInventory, error) {
	var out NativeRecoveryInventory
	err := c.do(ctx, http.MethodPost, "/api/v1/native-recovery-sources/"+esc(source)+"/refresh", nil, &out)
	return &out, err
}
