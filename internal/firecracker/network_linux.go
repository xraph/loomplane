//go:build linux

package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

func (d *linuxDriver) peerIPs(ctx context.Context, vm VM, excluded ...string) ([]string, error) {
	all, err := d.store.List(ctx)
	if err != nil {
		return nil, err
	}
	var peers []string
	for _, r := range all {
		for _, v := range r.NetworkVMs() {
			if v == nil || v.ID == vm.ID || v.Network != vm.Network || slices.Contains(excluded, v.ID) {
				continue
			}
			receipt, e := d.readReceipt(*v)
			if errors.Is(e, os.ErrNotExist) {
				continue
			}
			if e != nil {
				return nil, e
			}
			if receipt.IP == "" || receipt.PID == 0 {
				continue
			}
			start, e := processStart(receipt.PID)
			if e != nil || start != receipt.StartTime {
				continue
			}
			peers = append(peers, receipt.IP)
		}
	}
	return peers, nil
}
func (d *linuxDriver) installPolicy(ctx context.Context, vm VM, r runtimeReceipt, excluded ...string) error {
	peers, err := d.peerIPs(ctx, vm, excluded...)
	if err != nil {
		return err
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return err
	}
	hosts := make([]string, 0, len(addresses))
	for _, a := range addresses {
		ip, _, e := net.ParseCIDR(a.String())
		if e != nil {
			return e
		}
		hosts = append(hosts, ip.String())
	}
	ns, err := netns.GetFromPath(r.NetNS)
	if err != nil {
		return err
	}
	defer ns.Close()
	handle, err := netlink.NewHandleAt(ns)
	if err != nil {
		return err
	}
	defer handle.Close()
	link, err := handle.LinkByName("veth0")
	if err != nil {
		return err
	}
	peer, err := netlink.LinkByIndex(link.Attrs().ParentIndex)
	if err != nil {
		return err
	}
	policy, err := networkPolicy(peer.Attrs().Name, r.IP, hosts, d.config.EgressCIDRs, peers, d.config.DeniedCIDRs...)
	if err != nil {
		return err
	}
	// TC redirects the TAP before netfilter. Filter on the receiving host veth.
	table := "loomplane_" + vm.ID
	policy = strings.Replace(policy, "table netdev loomplane {", "table netdev "+table+" {", 1)
	check := exec.CommandContext(ctx, "nft", "list", "table", "netdev", table) //nolint:gosec // Table name is built from the validated VM identity, with no shell.
	check.Stdout = io.Discard
	check.Stderr = io.Discard
	if check.Run() == nil {
		policy = "delete table netdev " + table + "\n" + policy
	}
	cmd := exec.CommandContext(ctx, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(policy)
	return cmd.Run()
}
func (d *linuxDriver) refreshPeers(ctx context.Context, vm VM, _ runtimeReceipt, excluded ...string) error {
	all, err := d.store.List(ctx)
	if err != nil {
		return err
	}
	for _, r := range all {
		for _, v := range r.NetworkVMs() {
			if v == nil || v.Network != vm.Network || slices.Contains(excluded, v.ID) {
				continue
			}
			receipt, e := d.readReceipt(*v)
			if errors.Is(e, os.ErrNotExist) {
				continue
			}
			if e != nil {
				return e
			}
			if receipt.PID == 0 || receipt.IP == "" {
				continue
			}
			start, liveErr := processStart(receipt.PID)
			if liveErr != nil || start != receipt.StartTime {
				continue
			}
			if e := d.installPolicy(ctx, *v, receipt, excluded...); e != nil {
				return e
			}
		}
	}
	return nil
}

func (d *linuxDriver) networkValues(ctx context.Context, vm VM) (NetworkValues, error) {
	values := NetworkValues{Peers: map[string]string{}, DNS: d.config.DNS}
	records, err := d.store.List(ctx)
	if err != nil {
		return values, err
	}
	for _, r := range records {
		if !r.Serving() || r.Current.Network != vm.Network || r.Current.Spec.Kind != "app" {
			continue
		}
		receipt, e := d.readReceipt(*r.Current)
		if e != nil || receipt.PID == 0 || receipt.IP == "" {
			continue
		}
		start, liveErr := processStart(receipt.PID)
		if liveErr != nil || start != receipt.StartTime {
			continue
		}
		values.Peers[r.Current.Spec.Service+".loomplane"] = receipt.IP
	}
	return values, nil
}
func (d *linuxDriver) RefreshDiscovery(ctx context.Context) error {
	records, err := d.store.List(ctx)
	if err != nil {
		return err
	}
	for _, r := range records {
		if !r.Serving() {
			continue
		}
		network, e := d.networkValues(ctx, *r.Current)
		if e != nil {
			return e
		}
		raw, e := json.Marshal(network)
		if e != nil {
			return e
		}
		if _, e = d.guestCall(ctx, *r.Current, "peers", bytes.NewReader(raw)); e != nil {
			return e
		}
	}
	return nil
}

func (d *linuxDriver) RefreshNetwork(ctx context.Context, vm VM) error {
	return d.refreshPeers(ctx, vm, runtimeReceipt{})
}
