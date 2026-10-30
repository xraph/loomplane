package digitalocean

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

// Options joins DOKS's size allowlist with live capacity facts and existing VPCs.
func (c *Client) Options(ctx context.Context, access provider.AccountAccess, region string) (provider.ClusterOptions, error) {
	checkedAt := time.Now().UTC()
	out := provider.ClusterOptions{Versions: []string{}, Regions: []string{}, NodeSizes: []string{}, Networks: []string{}, NodeSizeFacts: []provider.ClusterNodeSize{}}
	if err := c.clusterIdentity(ctx, access); err != nil {
		return out, err
	}
	var response struct {
		Options *struct {
			Regions []struct {
				Slug string `json:"slug"`
			} `json:"regions"`
			Versions []struct {
				Slug string `json:"slug"`
			} `json:"versions"`
			Sizes []struct {
				Slug string `json:"slug"`
			} `json:"sizes"`
		} `json:"options"`
	}
	if err := c.do(ctx, string(access.Credential), http.MethodGet, "/v2/kubernetes/options", nil, nil, &response); err != nil {
		return out, infrastructureError(err, "kubernetes:read")
	}
	if response.Options == nil || len(response.Options.Regions) == 0 || len(response.Options.Versions) == 0 || len(response.Options.Sizes) == 0 {
		return out, provider.ErrInfrastructureInvalid
	}
	for _, v := range response.Options.Regions {
		if v.Slug == "" || slices.Contains(out.Regions, v.Slug) {
			return out, provider.ErrInfrastructureInvalid
		}
		out.Regions = append(out.Regions, v.Slug)
	}
	for _, v := range response.Options.Versions {
		if v.Slug == "" || slices.Contains(out.Versions, v.Slug) {
			return out, provider.ErrInfrastructureInvalid
		}
		out.Versions = append(out.Versions, v.Slug)
	}
	if region != "" && !slices.Contains(out.Regions, region) {
		return out, provider.ErrInfrastructureInvalid
	}
	allow := map[string]bool{}
	for _, v := range response.Options.Sizes {
		if v.Slug == "" || allow[v.Slug] {
			return out, provider.ErrInfrastructureInvalid
		}
		allow[v.Slug] = true
	}
	if err := c.clusterSizeFacts(ctx, access, region, allow, &out); err != nil {
		return provider.ClusterOptions{}, err
	}
	if err := c.clusterNetworks(ctx, access, region, &out); err != nil {
		return provider.ClusterOptions{}, err
	}
	slices.Sort(out.Regions)
	slices.Sort(out.Versions)
	slices.Sort(out.NodeSizes)
	slices.Sort(out.Networks)
	slices.SortFunc(out.NodeSizeFacts, func(a, b provider.ClusterNodeSize) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	data, err := json.Marshal(struct {
		Account, Region string
		Options         provider.ClusterOptions
	}{access.AccountID, region, out})
	if err != nil {
		return provider.ClusterOptions{}, provider.ErrInfrastructureInvalid
	}
	digest := sha256.Sum256(data)
	out.Digest = hex.EncodeToString(digest[:])
	out.CheckedAt = checkedAt
	return out, nil
}
func (c *Client) clusterSizeFacts(ctx context.Context, access provider.AccountAccess, region string, allow map[string]bool, out *provider.ClusterOptions) error {
	seen := map[string]bool{}
	state := doksPageState{}
	for page := 1; page <= infrastructureMaxPages; page++ {
		var response struct {
			Sizes []struct {
				Slug                string
				Available           bool
				Vcpus, Memory, Disk int
				Regions             []string
				PriceHourly         *float64 `json:"price_hourly"`
				PriceMonthly        *float64 `json:"price_monthly"`
			} `json:"sizes"`
			doksPageEvidence
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
		if err := c.do(ctx, string(access.Credential), http.MethodGet, "/v2/sizes", q, nil, &response); err != nil {
			return infrastructureError(err, "sizes:read")
		}
		if response.Sizes == nil || len(response.Sizes) > pageSize {
			return provider.ErrInfrastructurePagination
		}
		for _, v := range response.Sizes {
			if v.Slug == "" || seen[v.Slug] {
				return provider.ErrInfrastructurePagination
			}
			seen[v.Slug] = true
			if !allow[v.Slug] || !v.Available || (region != "" && !slices.Contains(v.Regions, region)) {
				continue
			}
			if v.Vcpus <= 0 || v.Memory <= 0 || v.Disk <= 0 || (v.PriceHourly != nil && *v.PriceHourly < 0) || (v.PriceMonthly != nil && *v.PriceMonthly < 0) {
				return provider.ErrInfrastructureInvalid
			}
			slices.Sort(v.Regions)
			out.NodeSizes = append(out.NodeSizes, v.Slug)
			out.NodeSizeFacts = append(out.NodeSizeFacts, provider.ClusterNodeSize{ID: v.Slug, CPUs: v.Vcpus, MemoryMiB: v.Memory, DiskGB: v.Disk, Regions: v.Regions, HourlyUSD: v.PriceHourly, MonthlyUSD: v.PriceMonthly})
		}
		next, err := state.next(response.doksPageEvidence, page, len(response.Sizes))
		if err != nil {
			return err
		}
		if next == "" {
			return nil
		}
	}
	return provider.ErrInfrastructurePagination
}
func (c *Client) clusterNetworks(ctx context.Context, access provider.AccountAccess, region string, out *provider.ClusterOptions) error {
	seen := map[string]bool{}
	state := doksPageState{}
	for page := 1; page <= infrastructureMaxPages; page++ {
		var response struct {
			VPCs []struct{ ID, Region string } `json:"vpcs"`
			doksPageEvidence
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
		if err := c.do(ctx, string(access.Credential), http.MethodGet, "/v2/vpcs", q, nil, &response); err != nil {
			return infrastructureError(err, "vpc:read")
		}
		if response.VPCs == nil || len(response.VPCs) > pageSize {
			return provider.ErrInfrastructurePagination
		}
		for _, v := range response.VPCs {
			if v.ID == "" || v.Region == "" || seen[v.ID] {
				return provider.ErrInfrastructurePagination
			}
			seen[v.ID] = true
			if region == "" || v.Region == region {
				out.Networks = append(out.Networks, v.ID)
			}
		}
		next, err := state.next(response.doksPageEvidence, page, len(response.VPCs))
		if err != nil {
			return err
		}
		if next == "" {
			return nil
		}
	}
	return provider.ErrInfrastructurePagination
}
