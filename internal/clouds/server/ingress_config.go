package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// IngressImage pins the multi-platform Caddy 2.11.7 Alpine release.
const IngressImage = "caddy:2.11.7-alpine@sha256:d8542f48d34a9cf4e4c11a478865229840e87e4c96ea3f439101f31a5d35f75f"

type IngressBackend struct {
	OrgID         string `json:"org_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	Service       string `json:"service"`
	InstanceID    string `json:"instance_id"`
	ReleaseID     string `json:"release_id"`
	ContainerID   string `json:"container_id"`
	Port          int    `json:"port"`
	Address       string `json:"address"`
}

type IngressRoute struct {
	ID              string          `json:"id"`
	Hostname        string          `json:"hostname"`
	Backend         IngressBackend  `json:"backend"`
	PreparedBackend *IngressBackend `json:"prepared_backend,omitempty"`
	// ChallengeAddress is a verified destination server's port 80. Normal
	// requests continue to use Backend until the accepted DNS cutover.
	ChallengeAddress string `json:"challenge_address,omitempty"`
	// BlockedPathPrefixes answer 404 on this hostname before the proxy sees them.
	BlockedPathPrefixes []string `json:"blocked_path_prefixes,omitempty"`
}

type IngressConfig struct {
	OrgID    string         `json:"org_id"`
	ServerID string         `json:"server_id"`
	Version  string         `json:"version"`
	Routes   []IngressRoute `json:"routes"`
	// InternalCA and ACME overrides are restricted to isolated fixture callers.
	InternalCA bool     `json:"-"`
	ACMEDir    string   `json:"-"`
	ACMERoots  []string `json:"-"`
}

func IngressName(org, server string) string {
	h := sha256.Sum256([]byte(org + "/" + server))
	return fmt.Sprintf("lp-ingress-%x", h[:12])
}

func IngressNetwork(b IngressBackend) string {
	h := sha256.Sum256([]byte(b.OrgID + "/" + b.ProjectID + "/" + b.EnvironmentID + "/" + b.Service + "/" + b.ContainerID))
	return fmt.Sprintf("lp-route-%x", h[:12])
}

// ValidBlockedPathPrefixes accepts at most 8 literal prefixes of at most 128
// bytes that look like /admin. Caddy reads *, ? and # in a path matcher, stops
// merging slashes for a pattern with //, and matches the raw escaped path for
// one with %, so none of those may appear. A trailing / would block only the
// paths below it, and a dot segment never matches a cleaned request path.
func ValidBlockedPathPrefixes(prefixes []string) bool {
	if len(prefixes) > 8 {
		return false
	}
	for _, p := range prefixes {
		if !strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || len(p) > 128 || strings.Contains(p, "//") || strings.ContainsAny(p, "*?#%") {
			return false
		}
		for _, segment := range strings.Split(p[1:], "/") {
			if segment == "." || segment == ".." {
				return false
			}
		}
	}
	return true
}

func blockedPathRoute(r IngressRoute) map[string]any {
	paths := make([]string, 0, 2*len(r.BlockedPathPrefixes))
	for _, p := range r.BlockedPathPrefixes {
		paths = append(paths, p, p+"/*")
	}
	return map[string]any{"match": []any{map[string]any{"host": []string{r.Hostname}, "path": paths}}, "handle": []any{map[string]any{"handler": "static_response", "status_code": 404}}, "terminal": true}
}

func validIngressHost(host string) bool {
	if host == "" || len(host) > 253 || host != strings.ToLower(host) || net.ParseIP(host) != nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
				return false
			}
		}
	}
	return true
}

// JSON produces one complete configuration. The loopback probe follows the
// same reverse proxy routes as HTTPS and cannot return a synthetic success.
func (v IngressConfig) JSON() ([]byte, error) {
	if v.OrgID == "" || v.ServerID == "" || v.Version == "" {
		return nil, ErrInvalid
	}
	routes := append([]IngressRoute(nil), v.Routes...)
	sort.Slice(routes, func(i, j int) bool { return routes[i].Hostname < routes[j].Hostname })
	httpsRoutes, probeRoutes, challengeRoutes := []any{}, []any{}, []any{}
	hosts := []string{}
	seen := map[string]bool{}
	for _, r := range routes {
		b := r.Backend
		if !validIngressHost(r.Hostname) || seen[r.Hostname] || r.ID == "" || b.OrgID != v.OrgID || b.ProjectID == "" || b.EnvironmentID == "" || b.Service == "" || b.InstanceID == "" || b.ReleaseID == "" || b.ContainerID == "" || net.ParseIP(b.Address) == nil || b.Port < 1 || b.Port > 65535 || !ValidBlockedPathPrefixes(r.BlockedPathPrefixes) {
			return nil, ErrInvalid
		}
		seen[r.Hostname] = true
		hosts = append(hosts, r.Hostname)
		if len(r.BlockedPathPrefixes) > 0 {
			httpsRoutes = append(httpsRoutes, blockedPathRoute(r))
			probeRoutes = append(probeRoutes, blockedPathRoute(r))
		}
		httpsRoutes = append(httpsRoutes, map[string]any{"match": []any{map[string]any{"host": []string{r.Hostname}}}, "handle": []any{map[string]any{"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": net.JoinHostPort(b.Address, strconv.Itoa(b.Port))}}}}, "terminal": true})
		if r.PreparedBackend != nil {
			candidate := *r.PreparedBackend
			if candidate.OrgID != b.OrgID || candidate.ProjectID != b.ProjectID || candidate.EnvironmentID != b.EnvironmentID || candidate.Service != b.Service || candidate.InstanceID == "" || candidate.ReleaseID == "" || candidate.ContainerID == "" || net.ParseIP(candidate.Address) == nil || candidate.Port < 1 || candidate.Port > 65535 {
				return nil, ErrInvalid
			}
			b = candidate
		}
		probeRoutes = append(probeRoutes, map[string]any{"match": []any{map[string]any{"host": []string{r.Hostname}}}, "handle": []any{map[string]any{"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": net.JoinHostPort(b.Address, strconv.Itoa(b.Port))}}, "headers": map[string]any{"response": map[string]any{"delete": []string{"Location"}}}}}, "terminal": true})
		if r.ChallengeAddress != "" {
			if net.ParseIP(r.ChallengeAddress) == nil {
				return nil, ErrInvalid
			}
			challengeRoutes = append(challengeRoutes, map[string]any{"match": []any{map[string]any{"host": []string{r.Hostname}, "path": []string{"/.well-known/acme-challenge/*"}}}, "handle": []any{map[string]any{"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": net.JoinHostPort(r.ChallengeAddress, "80")}}}}, "terminal": true})
		}
	}
	servers := map[string]any{
		"probe": map[string]any{"listen": []string{"127.0.0.1:8081"}, "routes": probeRoutes, "automatic_https": map[string]any{"disable": true}},
	}
	if len(routes) > 0 {
		servers["https"] = map[string]any{"listen": []string{":443"}, "routes": httpsRoutes}
	}
	if len(challengeRoutes) > 0 {
		servers["challenges"] = map[string]any{"listen": []string{":80"}, "routes": challengeRoutes}
	}
	apps := map[string]any{"http": map[string]any{"servers": servers}}
	if len(hosts) > 0 {
		issuer := map[string]any{"module": "acme", "challenges": map[string]any{"tls-alpn": map[string]any{"disabled": true}}}
		if v.InternalCA {
			issuer = map[string]any{"module": "internal"}
		} else if v.ACMEDir != "" {
			issuer["ca"] = v.ACMEDir
			issuer["trusted_roots_pem_files"] = v.ACMERoots
		}
		apps["tls"] = map[string]any{"automation": map[string]any{"policies": []any{map[string]any{"subjects": hosts, "issuers": []any{issuer}}}}}
	}
	return json.Marshal(map[string]any{"@id": v.Version, "admin": map[string]any{"listen": "127.0.0.1:2019"}, "apps": apps})
}
