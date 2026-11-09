package azure

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

type inventoryResource struct {
	ID, Name, Location string
	SystemData         struct{ CreatedAt string }
	Properties         struct {
		State, ProvisioningState, Version, RedisVersion, FullyQualifiedDomainName, HostName string
		Port                                                                                int
	}
}

// ResourceInventory reads the configured resource group, across its locations.
func (s *Session) ResourceInventory(ctx context.Context) (provider.ResourceInventory, error) {
	out := provider.ResourceInventory{Scope: strings.ToLower(s.group()) + "/managed-resources", Resources: []provider.ResourceObservation{}}
	fail := func() (provider.ResourceInventory, error) {
		return out, errors.New("azure resource inventory could not be read completely")
	}
	ids := map[string]bool{}
	for _, kind := range []string{"postgres", "mysql", "redis"} {
		namespace, collection, version, port := "Microsoft.Cache", "redisEnterprise", managedRedisVersion, ManagedRedisPort
		if kind != "redis" {
			e := flexibleEngines[kind]
			namespace, collection, version = e.namespace, "flexibleServers", e.version
			port = 5432
			if kind == "mysql" {
				port = 3306
			}
		}
		path := s.group() + "/providers/" + namespace + "/" + collection
		list, err := s.inventoryPages(ctx, path, version)
		if err != nil {
			return fail()
		}
		for _, d := range list {
			if d.ID == "" || ids[strings.ToLower(d.ID)] || !strings.EqualFold(strings.TrimSuffix(d.ID, "/"+d.Name), path) || d.Name == "" {
				return fail()
			}
			ids[strings.ToLower(d.ID)] = true
			p := d.Properties
			host, status, v := p.FullyQualifiedDomainName, p.State, p.Version
			if kind == "redis" {
				host, status, v = p.HostName, p.ProvisioningState, p.RedisVersion
				port = 0
				databases, readErr := s.inventoryPages(ctx, d.ID+"/databases", managedRedisVersion)
				if readErr != nil {
					return fail()
				}
				databaseIDs := map[string]bool{}
				for _, db := range databases {
					if !strings.HasPrefix(strings.ToLower(db.ID), strings.ToLower(d.ID)+"/databases/") || databaseIDs[strings.ToLower(db.ID)] {
						return fail()
					}
					databaseIDs[strings.ToLower(db.ID)] = true
					if strings.EqualFold(db.ID, d.ID+"/databases/default") {
						port = db.Properties.Port
					}
				}
			}
			out.Resources = append(out.Resources, provider.ResourceObservation{ID: strings.ToLower(d.ID), Incarnation: d.SystemData.CreatedAt, Name: d.Name, Kind: kind, Version: v, Region: d.Location, Status: status, Host: host, Port: port})
		}
	}
	out.Complete = true
	return out, nil
}
func (s *Session) inventoryPages(ctx context.Context, path, version string) ([]inventoryResource, error) {
	var out []inventoryResource
	seen := map[string]bool{}
	base, err := url.Parse(s.c.management)
	if err != nil {
		return nil, errors.New("invalid inventory endpoint")
	}
	initialPath := path
	for range 100 {
		if seen[path] {
			return nil, errors.New("repeated inventory page")
		}
		seen[path] = true
		var page struct {
			Value    []inventoryResource
			NextLink string
		}
		if _, err := s.do(ctx, http.MethodGet, path, version, nil, &page); err != nil {
			return nil, err
		}
		if page.Value == nil {
			return nil, errors.New("inventory collection is missing")
		}
		out = append(out, page.Value...)
		if page.NextLink == "" {
			return out, nil
		}
		u, err := url.Parse(page.NextLink)
		if err != nil || u.User != nil || u.Fragment != "" || u.Host != base.Host || u.Scheme != base.Scheme || !strings.EqualFold(u.Path, initialPath) {
			return nil, errors.New("inventory page outside configured scope")
		}
		path = u.String()
		version = ""
	}
	return nil, errors.New("inventory page limit exceeded")
}
