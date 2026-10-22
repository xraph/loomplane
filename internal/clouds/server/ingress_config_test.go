package server

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func testBackend() IngressBackend {
	return IngressBackend{OrgID: "org", ProjectID: "shop", EnvironmentID: "prod", Service: "octo--gateway", InstanceID: "instance", ReleaseID: "release", ContainerID: "container", Port: 8080, Address: "10.0.0.2"}
}

type renderedRoute struct {
	Hosts      []string
	Paths      []string
	Handler    string
	StatusCode int
}

// gjsonRoutes decodes apps.http.servers.<server>.routes into one entry per route.
func gjsonRoutes(t *testing.T, raw []byte, server string) []renderedRoute {
	t.Helper()
	var doc struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Match []struct {
							Host []string `json:"host"`
							Path []string `json:"path"`
						} `json:"match"`
						Handle []struct {
							Handler    string `json:"handler"`
							StatusCode int    `json:"status_code"`
						} `json:"handle"`
						Terminal bool `json:"terminal"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	routes := doc.Apps.HTTP.Servers[server].Routes
	out := make([]renderedRoute, 0, len(routes))
	for _, r := range routes {
		if len(r.Match) != 1 || len(r.Handle) != 1 || !r.Terminal {
			t.Fatalf("%s route shape: %+v", server, r)
		}
		out = append(out, renderedRoute{Hosts: r.Match[0].Host, Paths: r.Match[0].Path, Handler: r.Handle[0].Handler, StatusCode: r.Handle[0].StatusCode})
	}
	return out
}

func TestIngressConfigBlocksPrefixesBeforeProxy(t *testing.T) {
	other := testBackend()
	other.Service = "web"
	raw, err := IngressConfig{OrgID: "org", ServerID: "s", Version: "v", Routes: []IngressRoute{
		{ID: "r", Hostname: "api.acme.dev", BlockedPathPrefixes: []string{"/_farp", "/admin"}, Backend: testBackend()},
		{ID: "w", Hostname: "web.acme.dev", Backend: other},
	}}.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, server := range []string{"https", "probe"} {
		routes := gjsonRoutes(t, raw, server)
		if len(routes) != 3 {
			t.Fatalf("%s: %+v", server, routes)
		}
		blocked, proxy, unblocked := routes[0], routes[1], routes[2]
		if blocked.Handler != "static_response" || blocked.StatusCode != 404 || !slices.Equal(blocked.Hosts, []string{"api.acme.dev"}) || !slices.Equal(blocked.Paths, []string{"/_farp", "/_farp/*", "/admin", "/admin/*"}) {
			t.Fatalf("%s blocked route: %+v", server, blocked)
		}
		if proxy.Handler != "reverse_proxy" || !slices.Equal(proxy.Hosts, []string{"api.acme.dev"}) || proxy.Paths != nil {
			t.Fatalf("%s proxy route: %+v", server, proxy)
		}
		if unblocked.Handler != "reverse_proxy" || !slices.Equal(unblocked.Hosts, []string{"web.acme.dev"}) {
			t.Fatalf("%s route without prefixes: %+v", server, unblocked)
		}
	}
}

func TestIngressConfigRefusesUnsafeBlockedPrefixes(t *testing.T) {
	for _, bad := range [][]string{{"admin"}, {"/a*"}, {"/a?b"}, {"/a#b"}, {""}, {"/"}, {"/admin/"}, {"//admin"}, {"/%61dmin"}, {"/api/../admin"}} {
		_, err := IngressConfig{OrgID: "org", ServerID: "s", Version: "v", Routes: []IngressRoute{{ID: "r", Hostname: "api.acme.dev", BlockedPathPrefixes: bad, Backend: testBackend()}}}.JSON()
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}
