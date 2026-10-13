package gcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// The Google Cloud side of a link: an HA VPN gateway on the project's
// default network, a Cloud Router that speaks BGP over it, the other
// cloud's VPN endpoints as an external gateway, and four tunnels.

// DefaultNetwork is the network a link joins, and the one Cloud Run's
// Direct VPC egress uses.
const DefaultNetwork = "global/networks/default"

func (s *Session) computePath(path string) string {
	return s.c.url("compute.googleapis.com", "/compute/v1/projects/"+s.key.ProjectID+path)
}

type computeOperation struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Region string `json:"region"`
	Error  *struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"error"`
}

// waitCompute polls a Compute Engine operation until it's done.
func (s *Session) waitCompute(ctx context.Context, region string, op computeOperation, message string) error {
	where := "/global/operations/"
	if region != "" {
		where = "/regions/" + url.PathEscape(region) + "/operations/"
	}
	for op.Status != "DONE" {
		progress(ctx, message)
		if err := s.wait(ctx); err != nil {
			return err
		}
		if err := s.do(ctx, http.MethodGet, s.computePath(where+url.PathEscape(op.Name)), nil, &op); err != nil {
			return err
		}
	}
	if op.Error != nil && len(op.Error.Errors) > 0 {
		msgs := make([]string, 0, len(op.Error.Errors))
		for _, e := range op.Error.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("compute engine: %s", strings.Join(msgs, "; "))
	}
	return nil
}

// collection is a Compute Engine collection's path: regional, or global
// when region is empty.
func collection(region, name string) string {
	if region == "" {
		return "/global/" + name
	}
	return "/regions/" + url.PathEscape(region) + "/" + name
}

// insert creates a resource unless one by that name exists, and waits.
func (s *Session) insert(ctx context.Context, region, kind string, body map[string]any, message string) error {
	var op computeOperation
	err := s.do(ctx, http.MethodPost, s.computePath(collection(region, kind)), body, &op)
	if IsConflict(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create %s %v: %w", kind, body["name"], err)
	}
	return s.waitCompute(ctx, region, op, message)
}

// remove deletes a resource, and waits. One that's already gone is not an
// error.
func (s *Session) remove(ctx context.Context, region, kind, name string) error {
	var op computeOperation
	err := s.do(ctx, http.MethodDelete, s.computePath(collection(region, kind)+"/"+url.PathEscape(name)), nil, &op)
	if IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete %s %s: %w", kind, name, err)
	}
	return s.waitCompute(ctx, region, op, "Deleting "+name)
}

func (s *Session) ref(region, kind, name string) string {
	return "projects/" + s.key.ProjectID + collection(region, kind) + "/" + name
}

// EnsureVPNGateway makes an HA VPN gateway on the default network and
// returns its two interfaces' addresses.
func (s *Session) EnsureVPNGateway(ctx context.Context, region, name string) ([]string, error) {
	err := s.insert(ctx, region, "vpnGateways", map[string]any{
		"name": name, "network": DefaultNetwork, "stackType": "IPV4_ONLY",
	}, "Making the HA VPN gateway")
	if err != nil {
		return nil, err
	}
	var gw struct {
		Interfaces []struct {
			ID        int    `json:"id"`
			IPAddress string `json:"ipAddress"`
		} `json:"vpnInterfaces"`
	}
	if err := s.do(ctx, http.MethodGet, s.computePath(collection(region, "vpnGateways")+"/"+url.PathEscape(name)), nil, &gw); err != nil {
		return nil, err
	}
	ips := make([]string, 2)
	for _, i := range gw.Interfaces {
		if i.ID >= 0 && i.ID < 2 {
			ips[i.ID] = i.IPAddress
		}
	}
	if ips[0] == "" || ips[1] == "" {
		return nil, fmt.Errorf("vpn gateway %s has no addresses yet", name)
	}
	return ips, nil
}

// EnsureRouter makes a Cloud Router on the default network with a BGP
// ASN of its own.
func (s *Session) EnsureRouter(ctx context.Context, region, name string, asn int) error {
	return s.insert(ctx, region, "routers", map[string]any{
		"name": name, "network": DefaultNetwork, "bgp": map[string]any{"asn": asn},
	}, "Making the Cloud Router")
}

// EnsurePeerGateway describes the other cloud's four tunnel endpoints.
func (s *Session) EnsurePeerGateway(ctx context.Context, name string, ips []string) error {
	interfaces := make([]map[string]any, 0, len(ips))
	for i, ip := range ips {
		interfaces = append(interfaces, map[string]any{"id": i, "ipAddress": ip})
	}
	return s.insert(ctx, "", "externalVpnGateways", map[string]any{
		"name": name, "redundancyType": "FOUR_IPS_REDUNDANCY", "interfaces": interfaces,
	}, "Describing the other cloud's VPN")
}

// Tunnel is one IPsec tunnel from an HA VPN gateway interface to one of
// the peer's endpoints.
type Tunnel struct {
	Name      string
	Gateway   string
	Interface int
	Peer      string
	PeerIndex int
	Secret    string
	Router    string
}

// EnsureTunnel makes a tunnel.
func (s *Session) EnsureTunnel(ctx context.Context, region string, t Tunnel) error {
	return s.insert(ctx, region, "vpnTunnels", map[string]any{
		"name": t.Name, "ikeVersion": 2, "sharedSecret": t.Secret,
		"vpnGateway": s.ref(region, "vpnGateways", t.Gateway), "vpnGatewayInterface": t.Interface,
		"peerExternalGateway": s.ref("", "externalVpnGateways", t.Peer), "peerExternalGatewayInterface": t.PeerIndex,
		"router": s.ref(region, "routers", t.Router),
	}, "Opening tunnel "+t.Name)
}

// BGPPeer is a BGP session over a tunnel: the router's address on it and
// the peer's, both link-local.
type BGPPeer struct {
	Name    string
	Tunnel  string
	IPRange string // the router's side, such as 169.254.10.2/30
	PeerIP  string
	PeerASN int
}

// SetPeers adds an interface and a BGP peer on the router for each
// tunnel. Ones the router already has, by name, are left alone.
func (s *Session) SetPeers(ctx context.Context, region, router string, peers []BGPPeer) error {
	path := s.computePath(collection(region, "routers") + "/" + url.PathEscape(router))
	var r struct {
		Interfaces []map[string]any `json:"interfaces"`
		BGPPeers   []map[string]any `json:"bgpPeers"`
	}
	if err := s.do(ctx, http.MethodGet, path, nil, &r); err != nil {
		return err
	}
	has := map[string]bool{}
	for _, i := range r.Interfaces {
		has["if:"+fmt.Sprint(i["name"])] = true
	}
	for _, b := range r.BGPPeers {
		has["peer:"+fmt.Sprint(b["name"])] = true
	}
	changed := false
	for _, p := range peers {
		if !has["if:"+p.Name] {
			r.Interfaces = append(r.Interfaces, map[string]any{
				"name": p.Name, "ipRange": p.IPRange, "linkedVpnTunnel": s.ref(region, "vpnTunnels", p.Tunnel),
			})
			changed = true
		}
		if !has["peer:"+p.Name] {
			r.BGPPeers = append(r.BGPPeers, map[string]any{
				"name": p.Name, "interfaceName": p.Name, "peerIpAddress": p.PeerIP, "peerAsn": p.PeerASN,
			})
			changed = true
		}
	}
	if !changed {
		return nil
	}
	var op computeOperation
	if err := s.do(ctx, http.MethodPatch, path, map[string]any{"interfaces": r.Interfaces, "bgpPeers": r.BGPPeers}, &op); err != nil {
		return fmt.Errorf("set the router's BGP peers: %w", err)
	}
	return s.waitCompute(ctx, region, op, "Setting up BGP")
}

// TunnelStatus is a tunnel's state: ESTABLISHED once IPsec is up. The
// empty string is a tunnel that doesn't exist.
func (s *Session) TunnelStatus(ctx context.Context, region, name string) (string, error) {
	var t struct {
		Status string `json:"status"`
	}
	err := s.do(ctx, http.MethodGet, s.computePath(collection(region, "vpnTunnels")+"/"+url.PathEscape(name)), nil, &t)
	if IsNotFound(err) {
		return "", nil
	}
	return t.Status, err
}

// VPNGatewayExists reports whether an HA VPN gateway exists.
func (s *Session) VPNGatewayExists(ctx context.Context, region, name string) (bool, error) {
	err := s.do(ctx, http.MethodGet, s.computePath(collection(region, "vpnGateways")+"/"+url.PathEscape(name)), nil, nil)
	if IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// DeleteVPN deletes a link's Google Cloud side: its tunnels, then its
// router, peer gateway and HA VPN gateway. What's already gone is
// skipped.
func (s *Session) DeleteVPN(ctx context.Context, region string, tunnels []string, router, peer, gateway string) error {
	for _, t := range tunnels {
		if err := s.remove(ctx, region, "vpnTunnels", t); err != nil {
			return err
		}
	}
	if err := s.remove(ctx, region, "routers", router); err != nil {
		return err
	}
	if err := s.remove(ctx, "", "externalVpnGateways", peer); err != nil {
		return err
	}
	return s.remove(ctx, region, "vpnGateways", gateway)
}
