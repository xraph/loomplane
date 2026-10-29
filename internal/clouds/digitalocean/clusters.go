package digitalocean

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

var _ provider.ClusterReader = (*Client)(nil)

func (c *Client) clusterIdentity(ctx context.Context, access provider.AccountAccess) error {
	return c.infrastructureIdentity(ctx, provider.InfrastructureCredential{Provider: access.Provider, AccountID: access.AccountID, Token: string(access.Credential)})
}

// clusterPage accepts numbered cursors only. Remote links never become request URLs.
func clusterPage(cursor string) (int, error) {
	if cursor == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(cursor)
	if err != nil || n < 1 || n > infrastructureMaxPages || strconv.Itoa(n) != cursor {
		return 0, provider.ErrInfrastructurePagination
	}
	return n, nil
}
func clusterNext(next string, page int) (string, error) {
	if next == "" {
		return "", nil
	}
	n, err := doksLinkPage(next)
	if err != nil || n != page+1 {
		return "", provider.ErrInfrastructurePagination
	}
	return strconv.Itoa(n), nil
}
func clusterRef(access provider.AccountAccess, ref provider.ClusterRef) error {
	if ref.AccountID != access.AccountID {
		return provider.ErrInfrastructureIdentity
	}
	if ref.ID == "" || strings.ContainsAny(ref.ID, "/\\?#% \t\r\n") || ref.ID == "." || ref.ID == ".." {
		return provider.ErrInfrastructureInvalid
	}
	return nil
}

type doksCluster struct {
	Name      string `json:"name"`
	ID        string `json:"id"`
	Region    string `json:"region"`
	Version   string `json:"version"`
	Endpoint  string `json:"endpoint"`
	CreatedAt string `json:"created_at"`
	Status    struct {
		State string `json:"state"`
	} `json:"status"`
	NodePools []struct {
		Name      string                   `json:"name"`
		Size      string                   `json:"size"`
		Count     int                      `json:"count"`
		AutoScale bool                     `json:"auto_scale"`
		MinNodes  int                      `json:"min_nodes"`
		MaxNodes  int                      `json:"max_nodes"`
		Labels    map[string]string        `json:"labels"`
		Taints    []provider.NodePoolTaint `json:"taints"`
	} `json:"node_pools"`
}

func (d doksCluster) observation(account string) (provider.ClusterObservation, error) {
	if d.ID == "" || d.Region == "" || d.CreatedAt == "" || d.Status.State == "" {
		return provider.ClusterObservation{}, provider.ErrInfrastructureInvalid
	}
	if _, err := time.Parse(time.RFC3339, d.CreatedAt); err != nil {
		return provider.ClusterObservation{}, provider.ErrInfrastructureInvalid
	}
	out := provider.ClusterObservation{Name: d.Name, Ref: provider.ClusterRef{ID: d.ID, AccountID: account, Region: d.Region, Incarnation: d.CreatedAt}, Endpoint: d.Endpoint, Version: d.Version, State: d.Status.State, ObservedAt: time.Now().UTC(), Pools: []provider.NodePoolSpec{}}
	for _, p := range d.NodePools {
		out.Pools = append(out.Pools, provider.NodePoolSpec{Name: p.Name, Size: p.Size, Count: p.Count, Min: p.MinNodes, Max: p.MaxNodes, Autoscale: p.AutoScale, Labels: p.Labels, Taints: p.Taints})
	}
	return out, nil
}

// ListClusters returns one bounded page and preserves the continuation marker.
func (c *Client) ListClusters(ctx context.Context, access provider.AccountAccess, cursor string) (provider.ClusterPage, error) {
	out := provider.ClusterPage{Items: []provider.ClusterObservation{}}
	page, err := clusterPage(cursor)
	if err != nil {
		return out, err
	}
	if identityErr := c.clusterIdentity(ctx, access); identityErr != nil {
		return out, identityErr
	}
	var response struct {
		Clusters []doksCluster `json:"kubernetes_clusters"`
		doksPageEvidence
	}
	q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
	if err = c.do(ctx, string(access.Credential), http.MethodGet, "/v2/kubernetes/clusters", q, nil, &response); err != nil {
		return out, infrastructureError(err, "kubernetes:read")
	}
	if response.Clusters == nil || len(response.Clusters) > pageSize {
		return out, provider.ErrInfrastructurePagination
	}
	state := doksPageState{}
	out.NextCursor, err = state.next(response.doksPageEvidence, page, len(response.Clusters))
	if err != nil {
		return out, err
	}
	seen := map[string]bool{}
	for _, cluster := range response.Clusters {
		if seen[cluster.ID] {
			return out, provider.ErrInfrastructurePagination
		}
		seen[cluster.ID] = true
		observation, e := cluster.observation(access.AccountID)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, observation)
	}
	return out, nil
}
func (c *Client) GetCluster(ctx context.Context, access provider.AccountAccess, ref provider.ClusterRef) (provider.ClusterObservation, error) {
	if err := clusterRef(access, ref); err != nil {
		return provider.ClusterObservation{}, err
	}
	if err := c.clusterIdentity(ctx, access); err != nil {
		return provider.ClusterObservation{}, err
	}
	var response struct {
		Cluster doksCluster `json:"kubernetes_cluster"`
	}
	if err := c.do(ctx, string(access.Credential), http.MethodGet, "/v2/kubernetes/clusters/"+url.PathEscape(ref.ID), nil, nil, &response); err != nil {
		return provider.ClusterObservation{}, infrastructureError(err, "kubernetes:read")
	}
	out, err := response.Cluster.observation(access.AccountID)
	if err != nil {
		return out, err
	}
	if out.Ref.ID != ref.ID || (ref.Region != "" && ref.Region != out.Ref.Region) || (ref.Incarnation != "" && ref.Incarnation != out.Ref.Incarnation) {
		return provider.ClusterObservation{}, provider.ErrInfrastructureIdentity
	}
	return out, nil
}
