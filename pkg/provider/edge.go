package provider

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// EdgeRule applies to a path prefix. Later rules win. A public rule overrides
// origin TTLs and is suitable only for responses shared by every visitor.
type EdgeRule struct {
	PathPrefix string `json:"path_prefix"`
	Mode       string `json:"mode"`
	EdgeTTL    int    `json:"edge_ttl"`
	BrowserTTL int    `json:"browser_ttl"`
}

type EdgePolicy struct {
	Enabled            bool       `json:"enabled"`
	InvalidateOnDeploy bool       `json:"invalidate_on_deploy"`
	Rules              []EdgeRule `json:"rules"`
}

func (p EdgePolicy) Validate() error {
	if len(p.Rules) > 6 {
		return fmt.Errorf("%w: at most six cache rules", ErrInfrastructureInvalid)
	}
	for _, r := range p.Rules {
		if !ValidEdgePath(r.PathPrefix) || (r.Mode != "origin" && r.Mode != "bypass" && r.Mode != "public") {
			return fmt.Errorf("%w: use a path prefix and origin, bypass or public mode", ErrInfrastructureInvalid)
		}
		if r.Mode == "public" {
			if r.EdgeTTL < 60 || r.EdgeTTL > 31536000 || r.BrowserTTL < 0 || r.BrowserTTL > r.EdgeTTL {
				return fmt.Errorf("%w: edge TTL must be 60 to 31536000 seconds and browser TTL between zero and edge TTL", ErrInfrastructureInvalid)
			}
		} else if r.EdgeTTL != 0 || r.BrowserTTL != 0 {
			return fmt.Errorf("%w: TTL overrides require public mode", ErrInfrastructureInvalid)
		}
	}
	return nil
}

func ValidEdgePath(path string) bool {
	if !strings.HasPrefix(path, "/") || len(path) > 256 || strings.ContainsAny(path, "\"\\%?#*\r\n\t ") || strings.Contains(path, "//") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	for _, c := range path {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}

// ValidateEdgePurge confines an exact-URL purge to the attached hostname.
// An empty list means all cached content for that hostname, never the zone.
func ValidateEdgePurge(host string, urls []string) error {
	if len(urls) > 30 {
		return fmt.Errorf("%w: purge at most 30 URLs", ErrInfrastructureInvalid)
	}
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || len(raw) > 4096 || (u.Scheme != "https" && u.Scheme != "http") || u.Host != host || u.User != nil || u.Fragment != "" || strings.ContainsAny(raw, "\r\n") {
			return fmt.Errorf("%w: purge URLs must belong to %s", ErrInfrastructureInvalid, host)
		}
	}
	return nil
}

type EdgeAdapter interface {
	ApplyEdge(context.Context, InfrastructureCredential, DNSZone, string, string, EdgePolicy, []string) error
	PurgeEdge(context.Context, InfrastructureCredential, DNSZone, string, []string) error
	VerifyEdgeDNS(context.Context, InfrastructureCredential, DNSZone, DNSRecord) error
}
