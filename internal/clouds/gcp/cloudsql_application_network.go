package gcp

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

// ErrSQLApplicationNetworkUnavailable indicates a subnet that cannot establish the reviewed route.
var ErrSQLApplicationNetworkUnavailable = errors.New("cloud sql application subnet does not match the reviewed route")

// SQLApplicationNetwork records the subnet observed for a Cloud Run PSC route.
// Its identity and range can be compared before rollout and after a restart.
// It does not establish free address capacity, IAM, DNS or firewall access.
type SQLApplicationNetwork struct {
	Route  provider.CloudSQLApplicationNetwork `json:"route"`
	ID     string                              `json:"id"`
	IPCIDR string                              `json:"ip_cidr"`
}

type sqlApplicationSubnet struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	SelfLink  string `json:"selfLink"`
	Region    string `json:"region"`
	Network   string `json:"network"`
	IPCIDR    string `json:"ipCidrRange"`
	Purpose   string `json:"purpose"`
	StackType string `json:"stackType"`
	State     string `json:"state"`
}

var cloudRunSubnetRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("240.0.0.0/4"),
}

func (s sqlApplicationSubnet) matches(route provider.CloudSQLApplicationNetwork, region string) bool {
	id, idErr := strconv.ParseUint(s.ID, 10, 64)
	prefix, prefixErr := netip.ParsePrefix(s.IPCIDR)
	if idErr != nil || id == 0 || strconv.FormatUint(id, 10) != s.ID || prefixErr != nil || !prefix.Addr().Is4() || prefix.Bits() > 26 || prefix != prefix.Masked() {
		return false
	}
	allowed := false
	for _, pool := range cloudRunSubnetRanges {
		if prefix.Bits() >= pool.Bits() && pool.Contains(prefix.Addr()) {
			allowed = true
			break
		}
	}
	parts := strings.Split(route.Subnetwork, "/")
	return allowed && s.Kind == "compute#subnetwork" && s.Name == parts[5] && computeResource(s.SelfLink) == route.Subnetwork &&
		computeResource(s.Network) == route.Network && computeResource(s.Region) == "projects/"+route.ConsumerProject+"/regions/"+region &&
		(s.Purpose == "PRIVATE" || s.Purpose == "PRIVATE_RFC_1918") &&
		(s.StackType == "" || s.StackType == "IPV4_ONLY" || s.StackType == "IPV4_IPV6") && s.State == "READY"
}

// ReadSQLApplicationNetwork observes only the declared subnet. It never creates
// a network or selects a same-name default. The consumer may be a Shared VPC host
// project distinct from the Cloud Run service's project.
func (s *Session) ReadSQLApplicationNetwork(ctx context.Context, region string, route provider.CloudSQLApplicationNetwork, check func(context.Context) error) (*SQLApplicationNetwork, error) {
	if !route.Valid(region) || check == nil {
		return nil, ErrSQLApplicationNetworkUnavailable
	}
	var first sqlApplicationSubnet
	for n := range 2 {
		if err := check(ctx); err != nil {
			return nil, err
		}
		var subnet sqlApplicationSubnet
		if err := s.do(ctx, http.MethodGet, s.c.url("compute.googleapis.com", "/compute/v1/"+route.Subnetwork), nil, &subnet); err != nil {
			return nil, err
		}
		if !subnet.matches(route, region) || n > 0 && subnet != first {
			return nil, ErrSQLApplicationNetworkUnavailable
		}
		first = subnet
	}
	if err := check(ctx); err != nil {
		return nil, err
	}
	return &SQLApplicationNetwork{Route: route, ID: first.ID, IPCIDR: first.IPCIDR}, nil
}
