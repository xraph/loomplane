package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/xraph/loomplane/pkg/provider"
)

// ResourceInventory reads the token's managed database clusters across all regions.
func (c *Client) ResourceInventory(ctx context.Context, token string) (provider.ResourceInventory, error) {
	out := provider.ResourceInventory{Scope: "account/all-regions/databases", Resources: []provider.ResourceObservation{}}
	fail := func() (provider.ResourceInventory, error) {
		return out, errors.New("DigitalOcean resource inventory could not be read completely")
	}
	seen := map[string]bool{}
	pages := map[string]bool{}
	page := "1"
	count := 0
	var total *int
	lastPage := 0
	for range 100 {
		if pages[page] {
			return fail()
		}
		pages[page] = true
		var res struct {
			Databases []struct {
				ID, Name, Engine, Version, Region, Status string
				Created                                   string `json:"created_at"`
				Connection                                *struct {
					Host, Database string
					Port           int
				}
			}
			Links struct{ Pages struct{ Next, Last string } }
			Meta  struct{ Total *int }
		}
		if err := c.do(ctx, token, http.MethodGet, "/v2/databases", url.Values{"page": {page}, "per_page": {"200"}}, nil, &res); err != nil {
			return fail()
		}
		if res.Databases == nil {
			return fail()
		}
		count += len(res.Databases)
		for _, d := range res.Databases {
			if d.ID == "" || seen[d.ID] {
				return fail()
			}
			seen[d.ID] = true
			kind := map[string]string{"pg": "postgres", "mysql": "mysql", "redis": "redis", "valkey": "redis"}[d.Engine]
			if kind == "" {
				continue
			}
			r := provider.ResourceObservation{ID: d.ID, Incarnation: d.ID, Name: d.Name, Kind: kind, Version: d.Version, Region: d.Region, Status: d.Status}
			if d.Created != "" {
				r.Incarnation = d.Created
			}
			if d.Connection != nil {
				r.Host, r.Port, r.Database = d.Connection.Host, d.Connection.Port, d.Connection.Database
			}
			out.Resources = append(out.Resources, r)
		}

		if res.Meta.Total != nil {
			if *res.Meta.Total < 0 || (total != nil && *total != *res.Meta.Total) {
				return fail()
			}
			total = res.Meta.Total
		}
		if res.Links.Pages.Last != "" {
			lastURL, parseErr := url.Parse(res.Links.Pages.Last)
			if parseErr != nil || lastURL.Path != "/v2/databases" {
				return fail()
			}
			value, parseErr := strconv.Atoi(lastURL.Query().Get("page"))
			if parseErr != nil || value < len(pages) || value > 100 || (lastPage != 0 && lastPage != value) {
				return fail()
			}
			lastPage = value
		}
		if lastPage != 0 && ((lastPage > len(pages)) != (res.Links.Pages.Next != "")) {
			return fail()
		}
		if res.Links.Pages.Next == "" {
			if total != nil && *total != count {
				return fail()
			}
			out.Complete = true
			return out, nil
		}
		next, err := url.Parse(res.Links.Pages.Next)
		if err != nil || next.Path != "/v2/databases" || next.User != nil {
			return fail()
		}
		query, err := url.ParseQuery(next.RawQuery)
		if err != nil || len(query["page"]) != 1 || (query.Get("per_page") != "" && query.Get("per_page") != "200") {
			return fail()
		}
		page = query.Get("page")
		n, err := strconv.Atoi(page)
		if err != nil || n != len(pages)+1 {
			return fail()
		}
	}
	return fail()
}
