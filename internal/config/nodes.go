package config

import (
	"fmt"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// NodesConfig is how replicas of one Loomplane reach each other for nodes.
// Settings are read by key (nodes.advertise_url and so on), like the rest.
type NodesConfig struct {
	// AdvertiseURL is where the other replicas reach this one's hop endpoint
	// (/_/nodes/hop). Empty means a single replica: no other replica can hop
	// to this one.
	AdvertiseURL string
	// HopInsecure allows replica hops over plain http. Only for a private network.
	HopInsecure bool
	// HopCAFile is a PEM file of the roots that verify other replicas'
	// certificates on https hops. Empty means the system roots.
	HopCAFile string
}

// Validate refuses an advertise URL nodes would not accept.
func (n NodesConfig) Validate() error {
	if n.AdvertiseURL == "" {
		return nil
	}
	if _, err := nodeproto.NormalizeAudience(n.AdvertiseURL); err != nil {
		return fmt.Errorf("nodes.advertise_url must be an absolute http or https URL with no user info, query or fragment, got %q", n.AdvertiseURL)
	}
	return nil
}
