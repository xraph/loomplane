package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

type inventoryItem struct {
	ID, Name, DisplayName, Project, Region, Location, CreateTime, TimeCreated, DatabaseVersion, RedisVersion, State, Host, DNSName, ConnectionName string
	Port                                                                                                                                           int
	IPAddresses                                                                                                                                    []struct{ Type, IPAddress string }
}

// ResourceInventory reads Cloud SQL, Memorystore and buckets in the session's project.
func (s *Session) ResourceInventory(ctx context.Context) (provider.ResourceInventory, error) {
	p := s.key.ProjectID
	out := provider.ResourceInventory{Scope: "projects/" + p + "/all-regions/managed-resources", Resources: []provider.ResourceObservation{}}
	fail := func() (provider.ResourceInventory, error) {
		return out, errors.New("google cloud resource inventory could not be read completely")
	}
	if p == "" || strings.ContainsAny(p, "/\\?#") {
		return fail()
	}
	ids := map[string]bool{}
	for _, kind := range []string{"sql", "redis", "bucket"} {
		host, path := "sqladmin.googleapis.com", "/sql/v1beta4/projects/"+url.PathEscape(p)+"/instances"
		q := url.Values{"maxResults": {"100"}}
		if kind == "redis" {
			host, path = "redis.googleapis.com", "/v1/projects/"+url.PathEscape(p)+"/locations/-/instances"
			q = url.Values{"pageSize": {"100"}}
		}
		if kind == "bucket" {
			host, path = "storage.googleapis.com", "/storage/v1/b"
			q = url.Values{"project": {p}, "maxResults": {"100"}, "projection": {"noAcl"}}
		}
		cursor := ""
		seen := map[string]bool{}
		for n := 0; n < 100; n++ {
			if seen[cursor] {
				return fail()
			}
			seen[cursor] = true
			if cursor != "" {
				q.Set("pageToken", cursor)
			}
			var page struct {
				Items, Instances []inventoryItem
				NextPageToken    string
				Unreachable      []string
				Warnings         []json.RawMessage
			}
			if err := s.do(ctx, http.MethodGet, s.c.url(host, path)+"?"+q.Encode(), nil, &page); err != nil {
				return fail()
			}
			if len(page.Unreachable) > 0 || len(page.Warnings) > 0 {
				return fail()
			}
			items := page.Items
			if kind == "redis" {
				items = page.Instances
			}
			for _, d := range items {
				r := provider.ResourceObservation{Name: d.Name, Region: d.Region, Status: d.State, Incarnation: d.CreateTime}
				switch kind {
				case "sql":
					if d.Project != p || d.Name == "" || strings.ContainsAny(d.Name, "/\\?#") {
						return fail()
					}
					r.ID = "projects/" + p + "/instances/" + d.Name
					r.Incarnation = ""
					native := &SQLInstance{Name: d.Name, Project: d.Project, Region: d.Region, ConnectionName: d.ConnectionName, CreateTime: d.CreateTime}
					if incarnation, proofErr := native.Incarnation(p, d.Region, d.Name); proofErr == nil {
						r.Incarnation = incarnation
					}
					r.Version = d.DatabaseVersion
					r.Port = 5432
					switch {
					case strings.HasPrefix(d.DatabaseVersion, "POSTGRES_"):
						r.Kind = "postgres"
					case strings.HasPrefix(d.DatabaseVersion, "MYSQL_"):
						r.Kind = "mysql"
						r.Port = 3306
					default:
						continue
					}
					r.Host = d.DNSName
					if r.Host == "" {
						for _, ip := range d.IPAddresses {
							if ip.Type == "PRIMARY" || r.Host == "" {
								r.Host = ip.IPAddress
							}
						}
					}
				case "redis":
					prefix := "projects/" + p + "/locations/"
					parts := strings.Split(d.Name, "/")
					if !strings.HasPrefix(d.Name, prefix) || len(parts) != 6 || parts[4] != "instances" || parts[5] == "" {
						return fail()
					}
					r.ID = d.Name
					r.Name = d.DisplayName
					if r.Name == "" {
						r.Name = parts[5]
					}
					r.Kind, r.Version, r.Region, r.Host, r.Port = "redis", d.RedisVersion, parts[3], d.Host, d.Port
				case "bucket":
					if d.Name == "" || strings.ContainsAny(d.Name, "/\\?#") {
						return fail()
					}
					r.ID = "projects/" + p + "/buckets/" + d.Name
					r.Kind, r.Region, r.Incarnation = "object-storage", strings.ToLower(d.Location), d.TimeCreated
				}
				if ids[r.ID] {
					return fail()
				}
				ids[r.ID] = true
				out.Resources = append(out.Resources, r)
			}
			cursor = page.NextPageToken
			if cursor == "" {
				break
			}
			if n == 99 {
				return fail()
			}
		}
	}
	out.Complete = true
	return out, nil
}
