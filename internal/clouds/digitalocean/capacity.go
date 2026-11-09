package digitalocean

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

// CapacityInventory reads complete account inventories. It never returns a
// partially paginated snapshot that could make existing usage disappear.
func (c *Client) CapacityInventory(ctx context.Context, credential provider.InfrastructureCredential) (provider.CapacityInventory, error) {
	out := provider.CapacityInventory{Provider: "digitalocean", AccountID: credential.AccountID, CheckedAt: time.Now().UTC()}
	if err := c.infrastructureIdentity(ctx, credential); err != nil {
		return out, err
	}
	account, err := c.Account(ctx, credential.Token)
	if err != nil {
		return out, infrastructureError(err, "account:read")
	}
	if account.DropletLimit != nil && *account.DropletLimit < 0 {
		return out, provider.ErrInfrastructureInvalid
	}
	out.Quota.Servers = account.DropletLimit
	for _, kind := range []string{"droplets", "volumes"} {
		seen := map[string]bool{}
		for page := 1; page <= infrastructureMaxPages; page++ {
			var response struct {
				Droplets []struct {
					ID     int64                 `json:"id"`
					VCPUs  *int64                `json:"vcpus"`
					Memory *int64                `json:"memory"`
					Disk   *int64                `json:"disk"`
					Region struct{ Slug string } `json:"region"`
				} `json:"droplets"`
				Volumes []struct {
					ID         string  `json:"id"`
					Size       *int64  `json:"size_gigabytes"`
					DropletIDs []int64 `json:"droplet_ids"`
				} `json:"volumes"`
				Links infrastructureLinks `json:"links"`
			}
			q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
			if err = c.do(ctx, credential.Token, http.MethodGet, "/v2/"+kind, q, nil, &response); err != nil {
				scope := "droplet:read"
				if kind == "volumes" {
					scope = "block_storage:read"
				}
				return provider.CapacityInventory{}, infrastructureError(err, scope)
			}
			unique := func(id string) bool {
				if id == "" || seen[id] {
					return false
				}
				seen[id] = true
				return true
			}
			for _, d := range response.Droplets {
				id := strconv.FormatInt(d.ID, 10)
				if d.ID <= 0 || !unique(id) {
					return provider.CapacityInventory{}, provider.ErrInfrastructurePagination
				}
				cpu, e := capacityUnits(d.VCPUs, 1000)
				if e != nil {
					return provider.CapacityInventory{}, e
				}
				memory, e := capacityUnits(d.Memory, 1<<20)
				if e != nil {
					return provider.CapacityInventory{}, e
				}
				disk, e := capacityUnits(d.Disk, 1_000_000_000)
				if e != nil {
					return provider.CapacityInventory{}, e
				}
				out.Servers = append(out.Servers, provider.CapacityServer{ID: id, Region: d.Region.Slug, CPUMillis: cpu, MemoryBytes: memory, StorageBytes: disk})
			}
			for _, v := range response.Volumes {
				if !unique(v.ID) {
					return provider.CapacityInventory{}, provider.ErrInfrastructurePagination
				}
				disk, e := capacityUnits(v.Size, 1_000_000_000)
				if e != nil {
					return provider.CapacityInventory{}, e
				}
				volume := provider.CapacityVolume{ID: v.ID, StorageBytes: disk}
				for _, id := range v.DropletIDs {
					if id <= 0 {
						return provider.CapacityInventory{}, provider.ErrInfrastructureInvalid
					}
					volume.ServerIDs = append(volume.ServerIDs, strconv.FormatInt(id, 10))
				}
				out.Volumes = append(out.Volumes, volume)
			}
			if response.Links.Pages.Next == "" {
				break
			}
			if page == infrastructureMaxPages {
				return provider.CapacityInventory{}, provider.ErrInfrastructurePagination
			}
		}
	}
	return out, nil
}
func capacityUnits(value *int64, multiplier int64) (*int64, error) {
	if value == nil {
		return nil, nil
	}
	if *value < 0 || *value > math.MaxInt64/multiplier {
		return nil, provider.ErrInfrastructureInvalid
	}
	result := *value * multiplier
	return &result, nil
}

func (c *Client) ObserveClusterCapacity(ctx context.Context, access provider.AccountAccess, ref provider.ClusterRef) (provider.ProviderClusterCapacity, error) {
	out := provider.ProviderClusterCapacity{Provider: "digitalocean", Ref: ref, CheckedAt: time.Now().UTC()}
	if err := clusterRef(access, ref); err != nil {
		return out, err
	}
	if err := c.clusterIdentity(ctx, access); err != nil {
		return out, err
	}
	var response struct {
		Cluster doksCluster `json:"kubernetes_cluster"`
	}
	if err := c.do(ctx, string(access.Credential), http.MethodGet, "/v2/kubernetes/clusters/"+url.PathEscape(ref.ID), nil, nil, &response); err != nil {
		return out, infrastructureError(err, "kubernetes:read")
	}
	observation, err := response.Cluster.observation(access.AccountID)
	if err != nil {
		return out, err
	}
	if observation.Ref != ref {
		return out, provider.ErrInfrastructureIdentity
	}
	inventory, err := c.CapacityInventory(ctx, provider.InfrastructureCredential{Provider: access.Provider, AccountID: access.AccountID, Token: string(access.Credential)})
	if err != nil {
		return out, err
	}
	hosts := map[string]provider.CapacityServer{}
	for _, host := range inventory.Servers {
		hosts[host.ID] = host
	}
	seen := map[string]bool{}
	for _, pool := range response.Cluster.NodePools {
		if pool.Count != len(pool.Nodes) {
			return out, provider.ErrInfrastructurePagination
		}
		if pool.ID == "" {
			return out, provider.ErrInfrastructureIdentity
		}
		for _, node := range pool.Nodes {
			if node.ID == "" || node.DropletID == "" || seen[node.DropletID] {
				return out, provider.ErrInfrastructureIdentity
			}
			seen[node.DropletID] = true
			host, ok := hosts[node.DropletID]
			if !ok {
				return out, provider.ErrInfrastructureIdentity
			}
			out.Nodes = append(out.Nodes, provider.ClusterCapacityNode{ExternalVMID: node.DropletID, ProviderNodeID: node.ID, PoolID: pool.ID, Zone: host.Region, CPUMillis: host.CPUMillis, MemoryBytes: host.MemoryBytes, EphemeralStorageBytes: host.StorageBytes})
		}
	}
	out.Complete = true
	return out, nil
}
