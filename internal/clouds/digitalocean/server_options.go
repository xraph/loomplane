package digitalocean

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

func (c *Client) ServerOptions(ctx context.Context, credential provider.InfrastructureCredential) (provider.ServerOptions, error) {
	out := provider.ServerOptions{Regions: []provider.ServerRegion{}, Sizes: []provider.ServerSize{}, Images: []provider.ServerImage{}}
	if err := c.infrastructureIdentity(ctx, credential); err != nil {
		return out, err
	}
	for _, resource := range []string{"regions", "sizes", "images"} {
		seen := map[string]bool{}
		for page := 1; page <= infrastructureMaxPages; page++ {
			var response struct {
				Regions []struct {
					Slug, Name string
					Available  bool
				} `json:"regions"`
				Sizes []struct {
					Slug                string
					Memory, Vcpus, Disk int
					Available           bool
					PriceHourly         *float64 `json:"price_hourly"`
					PriceMonthly        float64  `json:"price_monthly"`
					Regions             []string
				} `json:"sizes"`
				Images []struct {
					MinDiskSize              int `json:"min_disk_size"`
					ID                       int64
					Slug, Name, Distribution string
					Public                   bool
					Regions                  []string
				} `json:"images"`
				Links infrastructureLinks `json:"links"`
			}
			q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
			if resource == "images" {
				q.Set("type", "distribution")
			}
			if err := c.do(ctx, credential.Token, http.MethodGet, "/v2/"+resource, q, nil, &response); err != nil {
				return out, infrastructureError(err, resource+":read")
			}
			check := func(id string) bool {
				if id == "" || seen[id] {
					return false
				}
				seen[id] = true
				return true
			}
			for _, region := range response.Regions {
				if !check(region.Slug) {
					return out, provider.ErrInfrastructurePagination
				}
				if region.Available {
					out.Regions = append(out.Regions, provider.ServerRegion{ID: region.Slug, Name: region.Name})
				}
			}
			for _, size := range response.Sizes {
				if !check(size.Slug) {
					return out, provider.ErrInfrastructurePagination
				}
				if size.Available {
					price := 0.0
					if size.PriceHourly != nil {
						price = *size.PriceHourly
					}
					out.Sizes = append(out.Sizes, provider.ServerSize{HourlyPriceKnown: size.PriceHourly != nil, ID: size.Slug, CPUs: size.Vcpus, MemoryMiB: size.Memory, DiskGB: size.Disk, HourlyUSD: price, MonthlyUSD: size.PriceMonthly, Regions: size.Regions})
				}
			}
			for _, im := range response.Images {
				id := im.Slug
				if id == "" {
					id = strconv.FormatInt(im.ID, 10)
				}
				if !check(id) {
					return out, provider.ErrInfrastructurePagination
				}
				if im.Public {
					architecture := ""
					if strings.HasSuffix(im.Slug, "-x64") {
						architecture = "amd64"
					}
					if strings.HasSuffix(im.Slug, "-arm64") {
						architecture = "arm64"
					}
					out.Images = append(out.Images, provider.ServerImage{MinDiskGB: im.MinDiskSize, Architecture: architecture, ID: id, Name: im.Name, Regions: im.Regions, Bootstrap: strings.EqualFold(im.Distribution, "Ubuntu") && strings.HasPrefix(im.Slug, "ubuntu-24-04-")})
				}
			}
			if response.Links.Pages.Next == "" {
				break
			}
			if page == infrastructureMaxPages {
				return out, provider.ErrInfrastructurePagination
			}
		}
	}
	return out, nil
}
