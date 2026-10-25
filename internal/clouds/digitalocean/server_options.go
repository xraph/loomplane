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
					GPUInfo             *struct {
						Count int
						Model string
						VRAM  struct {
							Amount int
							Unit   string
						}
					} `json:"gpu_info"`
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
					row := provider.ServerSize{HourlyPriceKnown: size.PriceHourly != nil, ID: size.Slug, CPUs: size.Vcpus, MemoryMiB: size.Memory, DiskGB: size.Disk, HourlyUSD: price, MonthlyUSD: size.PriceMonthly, Regions: size.Regions}
					if g := size.GPUInfo; g != nil && g.Count > 0 {
						row.Accelerator = dropletAccelerator(g.Model, g.Count, g.VRAM.Amount, g.VRAM.Unit)
						row.Architectures = []string{"amd64"}
					}
					out.Sizes = append(out.Sizes, row)
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
					ubuntu := strings.EqualFold(im.Distribution, "Ubuntu")
					row := provider.ServerImage{MinDiskGB: im.MinDiskSize, Architecture: architecture, ID: id, Name: im.Name, Regions: im.Regions, Bootstrap: ubuntu && strings.HasPrefix(im.Slug, "ubuntu-24-04-")}
					if ubuntu && strings.HasPrefix(im.Slug, "gpu-") && strings.HasSuffix(im.Slug, "-base") {
						row.Accelerators = []string{provider.VendorNVIDIA}
						if strings.Contains(im.Slug, "amd") || strings.Contains(im.Slug, "mi300") {
							row.Accelerators = []string{provider.VendorAMD}
						}
						row.Architecture = "amd64"
						row.Bootstrap = true
					}
					out.Images = append(out.Images, row)
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

// dropletAccelerator turns a size's gpu_info into an Accelerator. The model is
// DigitalOcean's own string (such as "nvidia_h100"); vram is read as GiB only
// for the "gib" and "gb" units, otherwise the kind table supplies the memory.
func dropletAccelerator(model string, count, vram int, unit string) *provider.Accelerator {
	memoryMiB := 0
	switch strings.ToLower(unit) {
	case "gib", "gb":
		memoryMiB = vram * 1024
	}
	a := provider.Accelerator{Kind: provider.NormalizeAcceleratorKind(model, memoryMiB), Count: count, MemoryGiB: memoryMiB / 1024}
	if kind, ok := provider.LookupAcceleratorKind(a.Kind); ok {
		a.Vendor = kind.Vendor
		if a.MemoryGiB == 0 {
			a.MemoryGiB = kind.MemoryGiB
		}
	} else if m := strings.ToLower(model); strings.HasPrefix(m, "nvidia_") {
		a.Vendor = provider.VendorNVIDIA
	} else if strings.HasPrefix(m, "amd_") {
		a.Vendor = provider.VendorAMD
	}
	return &a
}
