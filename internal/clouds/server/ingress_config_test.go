package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
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

func backendFor(project string) IngressBackend {
	b := testBackend()
	b.ProjectID, b.ContainerID = project, "container-"+project
	return b
}

// routeHosts lists the first host each route of a server matches, in order.
func routeHosts(t *testing.T, raw []byte, server string) []string {
	t.Helper()
	routes := gjsonRoutes(t, raw, server)
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		out = append(out, r.Hosts[0])
	}
	return out
}

func TestIngressConfigOrdersExactBeforeWildcard(t *testing.T) {
	cfg := IngressConfig{OrgID: "org", ServerID: "s", Version: "v", Routes: []IngressRoute{
		{ID: "w", Hostname: "*.acme.dev", DNSChallenge: &IngressDNSChallenge{ZoneID: "zone_1", Zone: "acme.dev"}, Backend: backendFor("docs")},
		{ID: "e", Hostname: "api.acme.dev", Backend: backendFor("shop")},
	}}
	raw, err := cfg.JSON()
	if err != nil {
		t.Fatal(err)
	}
	hosts := routeHosts(t, raw, "https")
	if !slices.Equal(hosts, []string{"api.acme.dev", "*.acme.dev"}) {
		t.Fatal(hosts)
	}
	if !bytes.Contains(raw, []byte(`{file./config/dns/zone_1.token}`)) {
		t.Fatal("no file placeholder")
	}
	if probe := routeHosts(t, raw, "probe"); !slices.Equal(probe, []string{"api.acme.dev", "*.acme.dev"}) {
		t.Fatal("probe order", probe)
	}
	var doc struct {
		Apps struct {
			TLS struct {
				Automation struct {
					Policies []struct {
						Subjects []string `json:"subjects"`
						Issuers  []struct {
							Module     string `json:"module"`
							Challenges struct {
								DNS *struct {
									Provider map[string]string `json:"provider"`
								} `json:"dns"`
								HTTP    *struct{ Disabled bool } `json:"http"`
								TLSALPN *struct{ Disabled bool } `json:"tls-alpn"`
							} `json:"challenges"`
						} `json:"issuers"`
					} `json:"policies"`
				} `json:"automation"`
			} `json:"tls"`
		} `json:"apps"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	policies := doc.Apps.TLS.Automation.Policies
	if len(policies) != 2 || !slices.Equal(policies[0].Subjects, []string{"api.acme.dev"}) || !slices.Equal(policies[1].Subjects, []string{"*.acme.dev"}) {
		t.Fatalf("policies: %+v", policies)
	}
	if exact := policies[0].Issuers[0].Challenges; exact.DNS != nil || exact.HTTP != nil || exact.TLSALPN == nil || !exact.TLSALPN.Disabled {
		t.Fatalf("exact policy changed: %+v", exact)
	}
	wildcard := policies[1].Issuers[0].Challenges
	if wildcard.DNS == nil || wildcard.DNS.Provider["name"] != "cloudflare" || wildcard.DNS.Provider["api_token"] != "{file./config/dns/zone_1.token}" || !wildcard.HTTP.Disabled || !wildcard.TLSALPN.Disabled {
		t.Fatalf("wildcard policy: %+v", wildcard)
	}
}

func TestIngressConfigKeepsBlockedPrefixesWithTheirWildcard(t *testing.T) {
	raw, err := IngressConfig{OrgID: "org", ServerID: "s", Version: "v", Routes: []IngressRoute{
		{ID: "w", Hostname: "*.acme.dev", BlockedPathPrefixes: []string{"/admin"}, DNSChallenge: &IngressDNSChallenge{ZoneID: "zone_1", Zone: "acme.dev"}, Backend: backendFor("docs")},
		{ID: "z", Hostname: "zz.acme.dev", BlockedPathPrefixes: []string{"/admin"}, Backend: backendFor("shop")},
		{ID: "a", Hostname: "aa.acme.dev", Backend: backendFor("web")},
	}}.JSON()
	if err != nil {
		t.Fatal(err)
	}
	routes := gjsonRoutes(t, raw, "https")
	want := []struct{ host, handler string }{{"aa.acme.dev", "reverse_proxy"}, {"zz.acme.dev", "static_response"}, {"zz.acme.dev", "reverse_proxy"}, {"*.acme.dev", "static_response"}, {"*.acme.dev", "reverse_proxy"}}
	if len(routes) != len(want) {
		t.Fatalf("%+v", routes)
	}
	for i, w := range want {
		if routes[i].Hosts[0] != w.host || routes[i].Handler != w.handler {
			t.Fatalf("route %d: %+v", i, routes[i])
		}
	}
}

func TestIngressConfigRefusesUnscopedWildcards(t *testing.T) {
	challenge := func(zoneID, zone string) *IngressDNSChallenge {
		return &IngressDNSChallenge{ZoneID: zoneID, Zone: zone}
	}
	for _, r := range []IngressRoute{
		{ID: "w", Hostname: "*.acme.dev"},
		{ID: "w", Hostname: "*.evil.dev", DNSChallenge: challenge("zone_1", "acme.dev")},
		{ID: "w", Hostname: "*.acme.dev", DNSChallenge: challenge("../zone", "acme.dev")},
		{ID: "w", Hostname: "*.acme.dev", DNSChallenge: challenge("", "acme.dev")},
		{ID: "w", Hostname: "*.*.acme.dev", DNSChallenge: challenge("zone_1", "acme.dev")},
		{ID: "w", Hostname: "a.*.acme.dev", DNSChallenge: challenge("zone_1", "acme.dev")},
		{ID: "w", Hostname: "*acme.dev", DNSChallenge: challenge("zone_1", "acme.dev")},
		{ID: "e", Hostname: "api.acme.dev", DNSChallenge: challenge("zone_1", "acme.dev")},
	} {
		r.Backend = testBackend()
		if _, err := (IngressConfig{OrgID: "org", ServerID: "s", Version: "v", Routes: []IngressRoute{r}}).JSON(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", r, err)
		}
	}
}

func TestIngressProbeHostname(t *testing.T) {
	if got := IngressProbeHostname("route_1", "*.acme.dev"); !regexp.MustCompile(`^lp-probe-[0-9a-f]{8}\.acme\.dev$`).MatchString(got) || got != IngressProbeHostname("route_1", "*.acme.dev") {
		t.Fatal(got)
	}
	if got := IngressProbeHostname("route_1", "api.acme.dev"); got != "api.acme.dev" {
		t.Fatal(got)
	}
}

func TestIngressImagePinIsADigest(t *testing.T) {
	if IngressImage != "" && !regexp.MustCompile(`^`+regexp.QuoteMeta(IngressImageTag)+`@sha256:[0-9a-f]{64}$`).MatchString(IngressImage) {
		t.Fatalf("IngressImage %q isn't %s pinned by digest", IngressImage, IngressImageTag)
	}
	if !regexp.MustCompile(`@sha256:[0-9a-f]{64}$`).MatchString(LegacyIngressImage) {
		t.Fatal(LegacyIngressImage)
	}
}
