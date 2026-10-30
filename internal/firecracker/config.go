package firecracker

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

type Asset struct{ Path, SHA256 string }
type HostConfig struct {
	CNIHashes                                           map[string]string
	DeniedCIDRs                                         []string
	HTTPListen, HTTPSListen, ACMEEmail, ACMEDirectory   string
	HostID, Listen, AdvertiseHost, StateDir, RuntimeDir string
	PublishAddress                                      string
	TLSCertificate, TLSKey, ClientCA, HostKey           string
	Firecracker, Jailer, Kernel, RootFS                 Asset
	CNIBinDir, CNICacheDir, Subnet                      string
	DNS, EgressCIDRs                                    []string
	UIDBase                                             int
	Limits                                              Limits
	Peers                                               []Peer
}

var deviceName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,15}$`)

func networkPolicy(device, ip string, hostIPs, egress, peers []string, denied ...string) (string, error) {
	if !deviceName.MatchString(device) {
		return "", ErrInvalid
	}
	source, err := netip.ParseAddr(ip)
	if err != nil || !source.Is4() {
		return "", ErrInvalid
	}
	var b strings.Builder
	fmt.Fprintf(&b, "table netdev loomplane {\n chain guest { type filter hook ingress device \"%s\" priority -500; policy drop;\n ether type arp accept\n ip saddr != %s drop\n", device, source)
	for _, v := range hostIPs {
		addr, e := netip.ParseAddr(v)
		if e != nil {
			return "", ErrInvalid
		}
		if addr.Is4() {
			fmt.Fprintf(&b, " ip daddr %s drop\n", addr)
		}
	}
	for _, v := range denied {
		prefix, e := netip.ParsePrefix(v)
		if e != nil || !prefix.Addr().Is4() {
			return "", ErrInvalid
		}
		fmt.Fprintf(&b, " ip daddr %s drop\n", prefix.Masked())
	}
	b.WriteString(" ip daddr 100.64.0.0/10 drop\n ip daddr 169.254.0.0/16 drop\n ip daddr 127.0.0.0/8 drop\n ip daddr 0.0.0.0/8 drop\n ip daddr 224.0.0.0/4 drop\n")
	for _, v := range peers {
		addr, e := netip.ParseAddr(v)
		if e != nil || !addr.Is4() {
			return "", ErrInvalid
		}
		fmt.Fprintf(&b, " ip daddr %s accept\n", addr)
	}
	b.WriteString(" ip daddr 10.0.0.0/8 drop\n ip daddr 172.16.0.0/12 drop\n ip daddr 192.168.0.0/16 drop\n")
	for _, v := range egress {
		prefix, e := netip.ParsePrefix(v)
		if e != nil || !prefix.Addr().Is4() {
			return "", ErrInvalid
		}
		fmt.Fprintf(&b, " ip daddr %s meta l4proto { tcp, udp, icmp } accept\n", prefix.Masked())
	}
	b.WriteString(" }\n}\n")
	return b.String(), nil
}
