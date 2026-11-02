package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// NormalizeServices keeps older connections on DNS until you explicitly add R2.
func NormalizeServices(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case "", "dns":
		return "dns", nil
	case "r2":
		return "r2", nil
	case "dns,r2":
		return "dns,r2", nil
	default:
		return "", errors.New("choose DNS, R2 storage or DNS and R2")
	}
}

func oauthScopesFor(services string) string {
	scopes := []string{"account-settings.read"}
	if services != "r2" {
		scopes = append(scopes, "zone.read", "dns.write")
	}
	if services == "r2" || services == "dns,r2" {
		scopes = append(scopes, "workers-r2.write")
	}
	return strings.Join(append(scopes, "offline_access"), " ")
}

// R2ReadPermission checks the bucket-list API without creating or reading objects.
// Read access does not establish permission to create buckets or application access.
func (c *Client) R2ReadPermission(ctx context.Context, token, account string) (PermissionState, error) {
	var result struct {
		Buckets *[]json.RawMessage `json:"buckets"`
	}
	err := c.json(ctx, token, http.MethodGet, "/accounts/"+url.PathEscape(account)+"/r2/buckets", url.Values{"per_page": {"1"}}, nil, &result)
	var api *APIError
	if errors.As(err, &api) && api.Status == http.StatusForbidden {
		return Missing, nil
	}
	if err != nil {
		return Unchecked, err
	}
	if result.Buckets == nil {
		return Unchecked, errors.New("cloudflare: R2 permission check returned an invalid bucket list")
	}
	return Granted, nil
}
