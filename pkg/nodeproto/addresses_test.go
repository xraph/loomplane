package nodeproto

import (
	"slices"
	"testing"
)

func TestPublicAddressesKeepsOnlyPublicOnes(t *testing.T) {
	got := PublicAddresses([]string{
		"10.0.0.5", "172.17.0.1", "192.168.1.2", "100.64.0.1", "127.0.0.1", "169.254.1.1",
		"fd00::1", "fe80::1", "::1", "0.0.0.1", "198.18.0.1", "240.0.0.1", "2002::1", "not an ip",
		"2001:db8::7", "203.0.113.7", "::ffff:198.51.100.9", "203.0.113.7", " 192.0.2.4 ",
	})
	want := []string{"203.0.113.7", "198.51.100.9", "192.0.2.4", "2001:db8::7"}
	if !slices.Equal(got, want) {
		t.Fatalf("PublicAddresses = %v, want %v", got, want)
	}
}
