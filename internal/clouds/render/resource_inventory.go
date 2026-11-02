package render

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/xraph/loomplane/pkg/provider"
)

type inventoryDatastore struct {
	ID, Name, Status, Region, CreatedAt, Version, DatabaseName string
	Owner                                                      struct{ ID string }
}

// ResourceInventory reads one workspace without reading connection URLs or passwords.
func (c *Client) ResourceInventory(ctx context.Context, key Key) (provider.ResourceInventory, error) {
	out := provider.ResourceInventory{Scope: "owner/" + key.OwnerID + "/all-regions", Resources: []provider.ResourceObservation{}}
	fail := func() (provider.ResourceInventory, error) {
		return out, errors.New("render resource inventory could not be read completely")
	}
	if key.OwnerID == "" {
		return fail()
	}
	ids := map[string]bool{}
	for _, kind := range []string{Postgres, KeyValue} {
		cursor := ""
		seen := map[string]bool{}
		for n := 0; n < 100; n++ {
			if seen[cursor] {
				return fail()
			}
			seen[cursor] = true
			var page []struct {
				Postgres *inventoryDatastore `json:"postgres"`
				KeyValue *inventoryDatastore `json:"keyValue"`
				Cursor   string
			}
			q := url.Values{"ownerId": {key.OwnerID}, "limit": {"100"}}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			if err := c.do(ctx, key, http.MethodGet, "/"+kind, q, nil, &page); err != nil {
				return fail()
			}
			if len(page) == 0 {
				break
			}
			for _, item := range page {
				d := item.Postgres
				if kind == KeyValue {
					d = item.KeyValue
				}
				if d == nil || d.ID == "" || ids[d.ID] || d.Owner.ID != key.OwnerID {
					return fail()
				}
				ids[d.ID] = true
				k := "postgres"
				if kind == KeyValue {
					k = "redis"
				}
				out.Resources = append(out.Resources, provider.ResourceObservation{ID: d.ID, Incarnation: d.CreatedAt, Name: d.Name, Kind: k, Version: d.Version, Region: d.Region, Status: d.Status, Database: d.DatabaseName})
			}
			cursor = page[len(page)-1].Cursor
			if cursor == "" || n == 99 {
				return fail()
			}
		}
	}
	out.Complete = true
	return out, nil
}
