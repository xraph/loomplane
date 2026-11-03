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
	// JoinRateLimit is how many joins one address may send a minute. Zero
	// turns the limit off.
	JoinRateLimit int
	// ChallengeRateLimit is how many connect challenges one address may ask
	// for a minute. Every node behind one NAT shares it. Zero turns it off.
	ChallengeRateLimit int
}

// Defaults for the per-address limits. A join is typed by hand or run once
// per machine; a challenge is asked for on every reconnect, so a fleet behind
// one address after a restart needs room.
const (
	DefaultNodeJoinRateLimit      = 10
	DefaultNodeChallengeRateLimit = 120
)

// Validate refuses an advertise URL nodes would not accept.
func (n NodesConfig) Validate() error {
	if n.JoinRateLimit < 0 || n.ChallengeRateLimit < 0 {
		return fmt.Errorf("nodes.join_rate_limit and nodes.challenge_rate_limit can't be negative; use 0 to turn a limit off")
	}
	if n.AdvertiseURL == "" {
		return nil
	}
	if _, err := nodeproto.NormalizeAudience(n.AdvertiseURL); err != nil {
		return fmt.Errorf("nodes.advertise_url must be an absolute http or https URL with no user info, query or fragment, got %q", n.AdvertiseURL)
	}
	return nil
}
