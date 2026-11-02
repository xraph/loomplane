package fabriqloom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
)

// apiVersion is the admin API version Loomplane speaks: Fabriq's Plan 2a,
// released in 1.9.0.
const apiVersion = "1"

// maxResponse bounds what Loomplane reads of one admin API answer.
const maxResponse = 4 << 20

// Errors the admin API's answers become. Plan 2a's error bodies aren't a
// contract, so these come from status codes alone, and none carries what
// Fabriq wrote.
var (
	// ErrOperatorKey is a 401: Fabriq refused the operator key that
	// fabriq-admin's nginx sent for Loomplane.
	ErrOperatorKey = errors.New("Fabriq refused Loomplane's operator key") //nolint:staticcheck // ST1005: a sentence people read, and Fabriq is a name
	// ErrAPIVersion is a 426.
	ErrAPIVersion = errors.New("this Fabriq server speaks another admin API version")
	// ErrUnreachable is a 502, 503 or 504 from the console: its nginx is up
	// and the Fabriq server behind it isn't answering.
	ErrUnreachable = errors.New("the Fabriq server behind its console isn't answering")
	// ErrNotReported is an endpoint this Fabriq server doesn't serve
	// (403, 404 or 501), so what it reports is unknown.
	ErrNotReported = errors.New("this Fabriq server doesn't report that")
)

// StatusError is an admin API answer Loomplane didn't expect. It unwraps
// to ErrOperatorKey, ErrAPIVersion or ErrUnreachable when it is one.
type StatusError struct {
	Method, Path string
	Status       int
}

func (e *StatusError) Error() string {
	switch sentinel := e.Unwrap(); {
	case sentinel != nil:
		return fmt.Sprintf("%s (HTTP %d)", sentinel, e.Status)
	default:
		return fmt.Sprintf("Fabriq answered %s %s with HTTP %d", e.Method, e.Path, e.Status)
	}
}

// Unwrap is the sentinel the status means, or nil.
func (e *StatusError) Unwrap() error {
	switch e.Status {
	case http.StatusUnauthorized:
		return ErrOperatorKey
	case http.StatusUpgradeRequired:
		return ErrAPIVersion
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return ErrUnreachable
	}
	return nil
}

// AdminClient speaks Fabriq's admin API (Plan 2a) through fabriq-admin, the
// stack's console: a channel to component "admin", port "http", path
// /admin/.... It sends no Authorization header, which no component channel
// may carry: the console's nginx adds the operator key itself, from
// FABRIQ_ADMIN_UPSTREAM_KEY, and passes X-Tenant-ID and
// X-Fabriq-Api-Version on to the server.
type AdminClient struct {
	Channel *deployments.ComponentChannel
}

// Tenant is a Fabriq tenant.
type Tenant struct {
	ID        string    `json:"id" forge:"id"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
}

// KeyInfo is a Fabriq API key as the admin API lists it. Dashboard
// sessions are keys too, labelled "session".
type KeyInfo struct {
	ID, Prefix, Label, TenantID      string
	CanManageKeys                    bool
	CreatedAt                        time.Time
	RevokedAt, ExpiresAt, LastUsedAt *time.Time
}

// IssuedKey is a key Fabriq just issued. Key is the plaintext, which Fabriq
// shows once.
type IssuedKey struct{ ID, Prefix, Key string }

type tenantJSON struct {
	TenantID  string    `json:"tenantId"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"createdAt"`
}

func (t tenantJSON) tenant() Tenant {
	return Tenant{ID: t.TenantID, Label: t.Label, CreatedAt: t.CreatedAt}
}

var tenantPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidTenant says whether id is a tenant id Loomplane accepts: lowercase
// letters, digits and dashes, starting with a letter or digit, at most 63
// characters. It's stricter than Fabriq's own rule.
func ValidTenant(id string) bool { return tenantPattern.MatchString(id) }

// EnsureTenant creates tenant id, or finds it: Fabriq answers 201 for a new
// one and 200 for one it has. created says which.
func (c *AdminClient) EnsureTenant(ctx context.Context, id string) (tenant Tenant, created bool, err error) {
	var out struct {
		Tenant tenantJSON `json:"tenant"`
	}
	status, err := c.call(ctx, http.MethodPost, "/admin/tenants", "", map[string]string{"tenantId": id}, &out, http.StatusOK, http.StatusCreated)
	if err != nil {
		return Tenant{}, false, err
	}
	return out.Tenant.tenant(), status == http.StatusCreated, nil
}

// Tenants lists Fabriq's tenants, by id.
func (c *AdminClient) Tenants(ctx context.Context) ([]Tenant, error) {
	var out struct {
		Tenants []tenantJSON `json:"tenants"`
	}
	if _, err := c.call(ctx, http.MethodGet, "/admin/tenants", "", nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	tenants := make([]Tenant, 0, len(out.Tenants))
	for _, t := range out.Tenants {
		tenants = append(tenants, t.tenant())
	}
	return tenants, nil
}

// IssueKey issues a key labelled label that may use any tenant and can't
// manage keys: a service picks its tenant per request, and the DSN's
// tenant is only its default.
func (c *AdminClient) IssueKey(ctx context.Context, label string) (IssuedKey, error) {
	var out struct {
		ID     string `json:"id"`
		Prefix string `json:"prefix"`
		Key    string `json:"key"`
	}
	body := struct {
		Label         string `json:"label"`
		CanManageKeys bool   `json:"canManageKeys"`
	}{Label: label}
	if _, err := c.call(ctx, http.MethodPost, "/admin/keys", "", body, &out, http.StatusCreated); err != nil {
		return IssuedKey{}, err
	}
	if out.ID == "" || out.Key == "" {
		return IssuedKey{}, errors.New("the Fabriq server issued a key without an id or a key")
	}
	return IssuedKey{ID: out.ID, Prefix: out.Prefix, Key: out.Key}, nil
}

// Keys lists Fabriq's keys, newest first, revoked ones included.
func (c *AdminClient) Keys(ctx context.Context) ([]KeyInfo, error) {
	var out struct {
		Keys []struct {
			ID            string     `json:"id"`
			Prefix        string     `json:"prefix"`
			Label         string     `json:"label"`
			TenantID      string     `json:"tenantId"`
			CanManageKeys bool       `json:"canManageKeys"`
			CreatedAt     time.Time  `json:"createdAt"`
			RevokedAt     *time.Time `json:"revokedAt"`
			ExpiresAt     *time.Time `json:"expiresAt"`
			LastUsedAt    *time.Time `json:"lastUsedAt"`
		} `json:"keys"`
	}
	if _, err := c.call(ctx, http.MethodGet, "/admin/keys", "", nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	keys := make([]KeyInfo, 0, len(out.Keys))
	for _, k := range out.Keys {
		keys = append(keys, KeyInfo{ID: k.ID, Prefix: k.Prefix, Label: k.Label, TenantID: k.TenantID, CanManageKeys: k.CanManageKeys,
			CreatedAt: k.CreatedAt, RevokedAt: k.RevokedAt, ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt})
	}
	return keys, nil
}

// RevokeKey revokes key id. Revoking it again is no error, and neither is
// a key Fabriq doesn't know (404): it can't be used either way.
func (c *AdminClient) RevokeKey(ctx context.Context, id string) error {
	_, err := c.call(ctx, http.MethodDelete, "/admin/keys/"+url.PathEscape(id), "", nil, nil, http.StatusOK, http.StatusNotFound)
	return err
}

// EntityTypeCount counts the entity types tenant has. A server that
// doesn't report them is ErrNotReported.
func (c *AdminClient) EntityTypeCount(ctx context.Context, tenant string) (int, error) {
	var out struct {
		Types []json.RawMessage `json:"types"`
	}
	status, err := c.call(ctx, http.MethodGet, "/admin/entities/types", tenant, nil, &out, http.StatusOK, http.StatusForbidden, http.StatusNotFound, http.StatusNotImplemented)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return 0, ErrNotReported
	}
	return len(out.Types), nil
}

// AppliedMigrations lists the versions applied in Fabriq's own migration
// group, "fabriq", sent with X-Tenant-ID when tenant isn't empty. A server
// that doesn't report them (403, 404 or 501) gives known false and no
// error.
func (c *AdminClient) AppliedMigrations(ctx context.Context, tenant string) (versions []string, known bool, err error) {
	var out struct {
		Groups []struct {
			Name    string `json:"name"`
			Applied []struct {
				Version string `json:"version"`
			} `json:"applied"`
		} `json:"groups"`
	}
	status, err := c.call(ctx, http.MethodGet, "/admin/migrations", tenant, nil, &out, http.StatusOK, http.StatusForbidden, http.StatusNotFound, http.StatusNotImplemented)
	if err != nil || status != http.StatusOK {
		return nil, false, err
	}
	versions = []string{}
	for _, g := range out.Groups {
		if g.Name != "fabriq" {
			continue
		}
		for _, m := range g.Applied {
			versions = append(versions, m.Version)
		}
	}
	return versions, true, nil
}

// call sends a request to the admin API, at path (escaped), and reads a
// 2xx answer into out.
// An answer whose status isn't in accept is a *StatusError, and what
// Fabriq wrote is never read for it.
func (c *AdminClient) call(ctx context.Context, method, path, tenant string, body, out any, accept ...int) (int, error) {
	if c.Channel == nil || c.Channel.Client == nil || c.Channel.BaseURL == nil {
		return 0, errors.New("no channel to Fabriq's console")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(raw)
	}
	ref, err := url.Parse(path)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Channel.BaseURL.ResolveReference(ref).String(), reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Fabriq-Api-Version", apiVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tenant != "" {
		req.Header.Set("X-Tenant-ID", tenant)
	}
	res, err := c.Channel.Client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("reach Fabriq's admin API: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxResponse)) //nolint:errcheck // draining for reuse only
		_ = res.Body.Close()
	}()
	if !slices.Contains(accept, res.StatusCode) {
		return res.StatusCode, &StatusError{Method: method, Path: path, Status: res.StatusCode}
	}
	if out != nil && res.StatusCode/100 == 2 {
		if err := json.NewDecoder(io.LimitReader(res.Body, maxResponse)).Decode(out); err != nil {
			return res.StatusCode, fmt.Errorf("read Fabriq's answer to %s %s: %w", method, path, err)
		}
	}
	return res.StatusCode, nil
}
