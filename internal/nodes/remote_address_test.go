package nodes

import (
	"slices"
	"strconv"
	"testing"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

func TestRemoteAddressJoinsTheCandidates(t *testing.T) {
	reported := nodeproto.Inventory{OS: "linux", PublicAddresses: []string{"198.51.100.4"}}
	for _, tc := range []struct {
		name, remote string
		want         []string
	}{
		{"a public IPv4 goes after the node's own", "203.0.113.9:52811", []string{"198.51.100.4", "203.0.113.9"}},
		{"a public IPv6", "[2001:db8::9]:443", []string{"198.51.100.4", "2001:db8::9"}},
		{"one the node reported already", "198.51.100.4:1", []string{"198.51.100.4"}},
		{"a private address", "10.1.2.3:4000", []string{"198.51.100.4"}},
		{"loopback, as from a proxy on the same machine", "127.0.0.1:4000", []string{"198.51.100.4"}},
		{"CGNAT", "100.64.0.9:4000", []string{"198.51.100.4"}},
		{"no port", "203.0.113.9", []string{"198.51.100.4"}},
		{"not an address", "pipe", []string{"198.51.100.4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := withRemoteAddress(reported, tc.remote)
			if !slices.Equal(got.PublicAddresses, tc.want) {
				t.Fatalf("candidates %v, want %v", got.PublicAddresses, tc.want)
			}
			if !slices.Equal(reported.PublicAddresses, []string{"198.51.100.4"}) {
				t.Fatalf("the reported inventory changed: %v", reported.PublicAddresses)
			}
		})
	}
	// A node that reported the most candidates there is room for keeps its own.
	full := nodeproto.Inventory{}
	for i := range 40 {
		full.PublicAddresses = append(full.PublicAddresses, "198.51.100."+strconv.Itoa(i+1))
	}
	full = full.Bounded()
	if got := withRemoteAddress(full, "203.0.113.9:1"); len(got.PublicAddresses) != len(full.PublicAddresses) || slices.Contains(got.PublicAddresses, "203.0.113.9") {
		t.Fatalf("a full list grew or lost its own: %d %v", len(got.PublicAddresses), got.PublicAddresses[len(got.PublicAddresses)-1])
	}
}
