package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

var _ provider.EdgeAdapter = (*Client)(nil)

type cacheRule struct {
	ID          string         `json:"id,omitempty"`
	Ref         string         `json:"ref"`
	Description string         `json:"description"`
	Expression  string         `json:"expression"`
	Action      string         `json:"action"`
	Parameters  map[string]any `json:"action_parameters"`
	Enabled     bool           `json:"enabled"`
}

type cacheRuleset struct {
	ID    string      `json:"id"`
	Rules []cacheRule `json:"rules"`
}

func edgeRef(owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return "loomplane_" + hex.EncodeToString(sum[:16]) + "_"
}

func edgeBypass() map[string]any {
	return map[string]any{"cache": false, "browser_ttl": map[string]any{"mode": "bypass"}}
}

func edgeRules(owner, host string, policy provider.EdgePolicy, blocked []string) []cacheRule {
	prefix := edgeRef(owner)
	hostExpr := "http.host eq " + strconv.Quote(host)
	rule := func(ref, expression string, params map[string]any) cacheRule {
		return cacheRule{Ref: prefix + ref, Description: "Loomplane cache for " + host, Expression: expression, Action: "set_cache_settings", Parameters: params, Enabled: true}
	}
	// Unmatched paths always go to the origin. Each configured rule then
	// overrides that decision, followed by the unconditional safety bypass.
	out := make([]cacheRule, 0, len(policy.Rules)+2)
	out = append(out, rule("base", hostExpr, edgeBypass()))
	for i, r := range policy.Rules {
		params := map[string]any{"cache": r.Mode != "bypass"}
		if r.Mode == "bypass" {
			params = edgeBypass()
		}
		if r.Mode == "origin" {
			params["edge_ttl"] = map[string]any{"mode": "bypass_by_default"}
			params["browser_ttl"] = map[string]any{"mode": "respect_origin"}
		}
		if r.Mode == "public" {
			params["edge_ttl"] = map[string]any{"mode": "override_origin", "default": r.EdgeTTL}
			params["browser_ttl"] = map[string]any{"mode": "override_origin", "default": r.BrowserTTL}
		}
		out = append(out, rule(strconv.Itoa(i), hostExpr+" and starts_with(http.request.uri.path, "+strconv.Quote(r.PathPrefix)+")", params))
	}
	//nolint:gosec // The expression tests for an authorization header; it contains no credential.
	bypass := `not (http.request.method in {"GET" "HEAD"}) or http.cookie ne "" or any(http.request.headers["authorization"][*] ne "") or starts_with(http.request.uri.path, "/.well-known/acme-challenge/") or any(http.request.headers["rsc"][*] ne "") or any(http.request.headers["next-router-prefetch"][*] ne "") or any(http.request.headers["accept"][*] contains "text/x-component")`
	for _, path := range blocked {
		bypass += " or starts_with(http.request.uri.path, " + strconv.Quote(path) + ")"
	}
	out = append(out, rule("guard", hostExpr+" and ("+bypass+")", edgeBypass()))
	return out
}

// ApplyEdge changes only rules with this route's stable refs. It never replaces
// a zone's ruleset, so another project's rules and settings remain intact.
func (c *Client) ApplyEdge(ctx context.Context, cred provider.InfrastructureCredential, zone provider.DNSZone, owner, host string, policy provider.EdgePolicy, blocked []string) error {
	if checkErr := policy.Validate(); checkErr != nil {
		return checkErr
	}
	if host == "" || strings.ContainsAny(host, "*\"\\\r\n") || owner == "" || (host != zone.Name && !strings.HasSuffix(host, "."+zone.Name)) {
		return provider.ErrInfrastructureInvalid
	}
	base := "/zones/" + url.PathEscape(zone.ID)
	if policy.Enabled {
		var ssl struct {
			Value string `json:"value"`
		}
		if checkErr := c.json(ctx, cred.Token, http.MethodGet, base+"/settings/ssl", nil, nil, &ssl); checkErr != nil {
			return infrastructureError(checkErr, "Zone Settings:Read")
		}
		if ssl.Value != "strict" {
			return fmt.Errorf("%w: set Cloudflare SSL/TLS to Full (strict) before enabling CDN", provider.ErrInfrastructureInvalid)
		}
	}
	var set cacheRuleset
	err := c.json(ctx, cred.Token, http.MethodGet, base+"/rulesets/phases/http_request_cache_settings/entrypoint", nil, nil, &set)
	var api *APIError
	if errors.As(err, &api) && api.Status == http.StatusNotFound {
		if !policy.Enabled {
			return nil
		}
		err = c.json(ctx, cred.Token, http.MethodPost, base+"/rulesets", nil, map[string]any{"name": "Loomplane cache", "kind": "zone", "phase": "http_request_cache_settings", "rules": []cacheRule{}}, &set)
	}
	if err != nil {
		return infrastructureError(err, "Cache Rules:Edit")
	}
	if set.ID == "" {
		return provider.ErrInfrastructureAmbiguous
	}
	path := base + "/rulesets/" + url.PathEscape(set.ID) + "/rules"
	wanted := []cacheRule{}
	if policy.Enabled {
		wanted = edgeRules(owner, host, policy, blocked)
	}
	prefix := edgeRef(owner)
	// Rebuild this route's small rule group in order when any rule differs.
	// Disable the cache with its base bypass before replacing public rules.
	owned := []cacheRule{}
	for _, r := range set.Rules {
		if strings.HasPrefix(r.Ref, prefix) {
			owned = append(owned, r)
		}
	}
	if sameCacheRules(owned, wanted) {
		return nil
	}
	if !policy.Enabled {
		// The caller restores direct DNS first. Cleanup needs no extra rule
		// slot, so disabling works when the account has hit its limit.
		for _, r := range owned {
			if deleteErr := c.json(ctx, cred.Token, http.MethodDelete, path+"/"+url.PathEscape(r.ID), nil, nil, nil); deleteErr != nil {
				return infrastructureError(deleteErr, "Cache Rules:Edit")
			}
		}
		return c.PurgeEdge(ctx, cred, zone, host, nil)
	}
	// Establish a final bypass before any multi-request change. A retry finds
	// it by ref even when the original POST response was lost.
	guard := cacheRule{Ref: prefix + "updating", Description: "Loomplane cache update for " + host, Expression: "http.host eq " + strconv.Quote(host), Action: "set_cache_settings", Parameters: edgeBypass(), Enabled: true}
	var guardID string
	for _, r := range owned {
		if r.Ref == guard.Ref {
			guardID = r.ID
		}
	}
	if guardID == "" {
		if checkErr := c.json(ctx, cred.Token, http.MethodPost, path, nil, guard, &set); checkErr != nil {
			return infrastructureError(checkErr, "Cache Rules:Edit")
		}
		for _, r := range set.Rules {
			if r.Ref == guard.Ref {
				guardID = r.ID
			}
		}
		if guardID == "" {
			return provider.ErrInfrastructureAmbiguous
		}
	}
	for _, r := range owned {
		if r.ID == guardID {
			continue
		}
		if checkErr := c.json(ctx, cred.Token, http.MethodDelete, path+"/"+url.PathEscape(r.ID), nil, nil, nil); checkErr != nil {
			return infrastructureError(checkErr, "Cache Rules:Edit")
		}
	}
	for _, r := range wanted {
		// Insert immediately before the temporary bypass so partial updates
		// cannot start caching a path before the permanent guard exists.
		body := map[string]any{"ref": r.Ref, "description": r.Description, "expression": r.Expression, "action": r.Action, "action_parameters": r.Parameters, "enabled": true, "position": map[string]any{"before": guardID}}
		if checkErr := c.json(ctx, cred.Token, http.MethodPost, path, nil, body, nil); checkErr != nil {
			return infrastructureError(checkErr, "Cache Rules:Edit")
		}
	}
	if checkErr := c.PurgeEdge(ctx, cred, zone, host, nil); checkErr != nil {
		return checkErr
	}
	if checkErr := c.json(ctx, cred.Token, http.MethodDelete, path+"/"+url.PathEscape(guardID), nil, nil, nil); checkErr != nil {
		return infrastructureError(checkErr, "Cache Rules:Edit")
	}
	if readErr := c.json(ctx, cred.Token, http.MethodGet, base+"/rulesets/phases/http_request_cache_settings/entrypoint", nil, nil, &set); readErr != nil {
		return infrastructureError(readErr, "Cache Rules:Read")
	}
	actual := []cacheRule{}
	for _, r := range set.Rules {
		if strings.HasPrefix(r.Ref, prefix) {
			actual = append(actual, r)
		}
	}
	if !sameCacheRules(actual, wanted) {
		return provider.ErrInfrastructureAmbiguous
	}
	return nil
}

func sameCacheRules(a, b []cacheRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		// The HTTP decoder represents numeric parameters as float64.
		if a[i].Ref != b[i].Ref || a[i].Expression != b[i].Expression || a[i].Action != b[i].Action || a[i].Enabled != b[i].Enabled || normalizeParams(a[i].Parameters) != normalizeParams(b[i].Parameters) {
			return false
		}
	}
	return true
}

func normalizeParams(in map[string]any) string {
	raw, err := json.Marshal(in)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (c *Client) PurgeEdge(ctx context.Context, cred provider.InfrastructureCredential, zone provider.DNSZone, host string, urls []string) error {
	if host == "" || strings.ContainsAny(host, "*\"\\") || (host != zone.Name && !strings.HasSuffix(host, "."+zone.Name)) {
		return provider.ErrInfrastructureInvalid
	}
	if checkErr := provider.ValidateEdgePurge(host, urls); checkErr != nil {
		return checkErr
	}
	body := map[string]any{"hosts": []string{host}}
	if len(urls) > 0 {
		body = map[string]any{"files": urls}
	}
	return infrastructureError(c.json(ctx, cred.Token, http.MethodPost, "/zones/"+url.PathEscape(zone.ID)+"/purge_cache", nil, body, nil), "Cache Purge:Purge")
}
