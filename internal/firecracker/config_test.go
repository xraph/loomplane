package firecracker

import (
	"strings"
	"testing"
)

func TestNetworkPolicyRejectsHostAndSpoofedSources(t *testing.T) {
	policy, err := networkPolicy("tap0", "192.0.2.2", []string{"198.51.100.1"}, []string{"0.0.0.0/0"}, []string{"192.0.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{"ip saddr != 192.0.2.2 drop", "ip daddr 198.51.100.1 drop", "169.254.0.0/16", "policy drop", "ip daddr 192.0.2.3 accept"} {
		if !strings.Contains(policy, rule) {
			t.Fatal("missing policy", rule)
		}
	}
	if _, err = networkPolicy("tap\";bad", "192.0.2.2", nil, nil, nil); err == nil {
		t.Fatal("unsafe device accepted")
	}
}
