package render

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

// VerifyResourceEndpoint reads sensitive connection info only for an explicitly
// selected resource. Only a matching hostname and port leave this method.
func (c *Client) VerifyResourceEndpoint(ctx context.Context, key Key, selected provider.ResourceObservation, host string, port int) (provider.ResourceObservation, error) {
	fail := func() (provider.ResourceObservation, error) {
		return provider.ResourceObservation{}, errors.New("render could not verify the selected resource endpoint")
	}
	if key.OwnerID == "" || selected.ID == "" || selected.Incarnation == "" || strings.ContainsAny(selected.ID, "/\\?#.%") || host == "" || port < 1 || port > 65535 {
		return fail()
	}
	kind, defaultPort := Postgres, 5432
	switch selected.Kind {
	case "postgres":
	case "redis":
		kind, defaultPort = KeyValue, 6379
	default:
		return fail()
	}
	path := "/" + kind + "/" + url.PathEscape(selected.ID)
	read := func() bool {
		var resource inventoryDatastore
		if err := c.do(ctx, key, http.MethodGet, path, nil, nil, &resource); err != nil {
			return false
		}
		return resource.ID == selected.ID && resource.CreatedAt == selected.Incarnation && resource.Owner.ID == key.OwnerID
	}
	if !read() {
		return fail()
	}
	var connection struct{ InternalConnectionString, ExternalConnectionString string }
	if err := c.do(ctx, key, http.MethodGet, path+"/connection-info", nil, nil, &connection); err != nil {
		return fail()
	}
	matched := false
	for _, raw := range []string{connection.ExternalConnectionString, connection.InternalConnectionString} {
		u, err := url.Parse(raw)
		if err != nil || u.Opaque != "" || u.Hostname() == "" || u.Fragment != "" {
			continue
		}
		validScheme := u.Scheme == "postgres" || u.Scheme == "postgresql"
		if selected.Kind == "redis" {
			validScheme = u.Scheme == "redis" || u.Scheme == "rediss"
		}
		if !validScheme {
			continue
		}
		observedPort := defaultPort
		if p := u.Port(); p != "" {
			observedPort, err = strconv.Atoi(p)
			if err != nil {
				continue
			}
		}
		if strings.EqualFold(u.Hostname(), host) && observedPort == port {
			matched = true
		}
	}
	if !matched || !read() {
		return fail()
	}
	selected.Host, selected.Port = host, port
	return selected, nil
}
