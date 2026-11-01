package provider

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
)

const DefaultInstanceContainerStorageMiB int64 = 64

const InstanceIsolationRevision = "kubernetes-instance-isolation-v1"

// InstanceIsolationPolicy is derived from reviewed placement and resource grants.
// It describes enforcement; it does not itself prove that a cluster enforces it.
type InstanceIsolationPolicy struct {
	Revision           string                    `json:"revision"`
	PodSecurityVersion string                    `json:"pod_security_version"`
	CPUMillis          int64                     `json:"cpu_millis"`
	MemoryMiB          int64                     `json:"memory_mib"`
	EphemeralMiB       int64                     `json:"ephemeral_mib"`
	MaxPods            int64                     `json:"max_pods"`
	Egress             []InstanceNetworkEndpoint `json:"egress,omitempty"`
	IngressPeers       []InstanceNamespacePeer   `json:"ingress_peers,omitempty"`
	EgressPeers        []InstanceNamespacePeer   `json:"egress_peers,omitempty"`
}

type InstanceNetworkPort struct {
	Protocol string `json:"protocol"`
	Port     int32  `json:"port"`
}

type InstanceNetworkEndpoint struct {
	CIDR  string                `json:"cidr"`
	Ports []InstanceNetworkPort `json:"ports"`
}

// Peer rules are reserved for a future qualified namespace identity mechanism.
type InstanceNamespacePeer struct {
	Namespace    string                `json:"namespace"`
	NamespaceUID string                `json:"namespace_uid"`
	PodSelector  map[string]string     `json:"pod_selector"`
	Ports        []InstanceNetworkPort `json:"ports"`
}

var instanceSecurityVersion = regexp.MustCompile(`^v1\.(\d{2})$`)

func (p InstanceIsolationPolicy) Validate() error {
	version := instanceSecurityVersion.FindStringSubmatch(p.PodSecurityVersion)
	if p.Revision != InstanceIsolationRevision || len(version) != 2 || p.CPUMillis < 1 || p.CPUMillis > 1_000_000_000 || p.MemoryMiB < 1 || p.MemoryMiB > 1_000_000_000 || p.EphemeralMiB < 1 || p.EphemeralMiB > 1_000_000_000 || p.MaxPods < 1 || p.MaxPods > 10000 || len(p.Egress) > 64 || len(p.IngressPeers) > 16 || len(p.EgressPeers) > 16 {
		return ErrWorkloadSpec
	}
	minor, err := strconv.Atoi(version[1])
	if err != nil || minor < 25 {
		return ErrWorkloadSpec
	}
	for _, endpoint := range p.Egress {
		prefix, parseErr := netip.ParsePrefix(endpoint.CIDR)
		if parseErr != nil || prefix.Bits() != prefix.Addr().BitLen() || prefix.Addr().IsUnspecified() || prefix.Addr().IsLoopback() || prefix.Addr().IsMulticast() || prefix.Addr().IsLinkLocalUnicast() || prefix.Addr().Is4In6() || !validInstancePorts(endpoint.Ports) {
			return ErrWorkloadSpec
		}
	}
	if len(p.IngressPeers) != 0 || len(p.EgressPeers) != 0 {
		return fmt.Errorf("%w: cross-namespace peers require qualified identity enforcement", ErrWorkloadSpec)
	}
	return nil
}
func validInstancePorts(ports []InstanceNetworkPort) bool {
	if len(ports) < 1 || len(ports) > 16 {
		return false
	}
	seen := map[InstanceNetworkPort]bool{}
	for _, port := range ports {
		if port.Port < 1 || port.Port > 65535 || port.Protocol != "TCP" && port.Protocol != "UDP" || seen[port] {
			return false
		}
		seen[port] = true
	}
	return true
}
func (p InstanceIsolationPolicy) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), nil
}
