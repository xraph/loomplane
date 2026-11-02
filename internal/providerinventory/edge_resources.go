package providerinventory

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

func (c *Clients) cloudflareResources(ctx context.Context, conn *connections.Connection, credential []byte) (provider.ResourceInventory, error) {
	if c.Cloudflare == nil {
		return provider.ResourceInventory{}, errors.New("cloudflare inventory reader unavailable")
	}
	account := conn.Details["account_id"]
	if _, err := c.Cloudflare.Account(ctx, string(credential), account); err != nil {
		var apiError *cloudflare.APIError
		if !errors.As(err, &apiError) || apiError.Status != 403 {
			return provider.ResourceInventory{}, err
		}
	}
	out := provider.ResourceInventory{Scope: "accounts/" + account, Complete: true, Resources: []provider.ResourceObservation{}}
	successes := 0
	for _, kind := range []cloudflare.ResourceKind{cloudflare.ResourceD1, cloudflare.ResourceKV, cloudflare.ResourceR2, cloudflare.ResourceQueue, cloudflare.ResourceVectorize, cloudflare.ResourceHyperdrive, cloudflare.ResourceDurableObject} {
		jurisdictions := []string{""}
		if kind == cloudflare.ResourceR2 {
			jurisdictions = []string{"default", "eu", "us", "fedramp", "fedramp-high"}
		}
		for _, jurisdiction := range jurisdictions {
			rows, err := c.Cloudflare.ListResourcesInJurisdiction(ctx, string(credential), account, kind, jurisdiction)
			if err != nil {
				out.Complete = false
				continue
			}
			successes++
			for _, r := range rows {
				name := r.Name
				if name == "" {
					name = r.ID
				}
				scope := out.Scope + "/" + string(kind)
				id, incarnation := r.ID, r.ID
				platformKind := "cloudflare-" + string(kind)
				if kind == cloudflare.ResourceDurableObject {
					platformKind = "cloudflare-durable-object"
					scope = out.Scope + "/durable-objects/scripts/" + url.PathEscape(r.Metadata["script"]) + "/classes/" + url.PathEscape(r.Metadata["class"])
				}
				if kind == cloudflare.ResourceR2 {
					platformKind = "object-storage"
					id = "r2/" + jurisdiction + "/" + name
					scope = out.Scope + "/r2/" + jurisdiction
					incarnation = r.CreatedAt
				}
				if kind == cloudflare.ResourceVectorize {
					incarnation = r.CreatedAt
				}
				out.Resources = append(out.Resources, provider.ResourceObservation{ID: id, Incarnation: incarnation, Scope: scope, Name: name, Kind: platformKind, Region: "auto", Status: "ready", Host: "api.cloudflare.com", Port: 443, Database: r.ID})
			}
		}
	}
	if successes == 0 {
		return out, errors.New("cloudflare resource metadata could not be read")
	}
	return out, nil
}

func (c *Clients) vercelResources(ctx context.Context, conn *connections.Connection, credential []byte) (provider.ResourceInventory, error) {
	if c.Vercel == nil {
		return provider.ResourceInventory{}, errors.New("vercel inventory reader unavailable")
	}
	tok := vercel.Token{Value: strings.TrimSpace(string(credential)), TeamID: conn.Details["team_id"]}
	who, err := c.Vercel.Check(ctx, tok)
	if err != nil {
		return provider.ResourceInventory{}, err
	}
	account := who.TeamID
	if account == "" {
		account = who.UserID
		if account != conn.Details["user_id"] {
			return provider.ResourceInventory{}, errors.New("vercel account changed")
		}
	}
	out := provider.ResourceInventory{Scope: "teams/" + account, Complete: true, Resources: []provider.ResourceObservation{}}
	configs, configErr := c.Vercel.ListGlobalConfigs(ctx, tok)
	stores, storeErr := c.Vercel.ListStorageResources(ctx, tok)
	if configErr != nil && storeErr != nil {
		return out, errors.New("vercel resource metadata could not be read")
	}
	out.Complete = configErr == nil && storeErr == nil
	seen := map[string]bool{}
	for _, r := range append(configs, stores...) {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		kind, host := "", "api.vercel.com"
		scope := out.Scope
		switch r.Type {
		case "global-config", "edge-config":
			kind = "vercel-global-config"
			host = "global-config.vercel.com"
			scope += "/global-config"
		case "blob":
			kind = "vercel-blob"
			host = "vercel.com"
			scope += "/blob"
		case "integration":
			kind = "vercel-integration"
			scope += "/integrations/" + url.PathEscape(r.Product.Integration.Slug) + "/" + url.PathEscape(r.Product.Slug) + "/" + url.PathEscape(r.ExternalResourceID)
		default:
			out.Complete = false
			continue
		}
		out.Resources = append(out.Resources, provider.ResourceObservation{ID: r.ID, Incarnation: r.ID, Scope: scope, Name: r.Name, Kind: kind, Region: r.Region, Status: "ready", Host: host, Port: 443, Database: r.ID})
	}
	return out, nil
}
