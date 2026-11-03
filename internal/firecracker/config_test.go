package firecracker

import (
	"errors"
	"strings"
	"testing"
)

func TestNetworkPolicyRejectsHostAndSpoofedSources(t *testing.T) {
	policy, err := networkPolicy("tap0", "192.0.2.2", []string{"198.51.100.1"}, []string{"0.0.0.0/0"}, []string{"192.0.2.3"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{"ip saddr != 192.0.2.2 drop", "ip daddr 198.51.100.1 drop", "169.254.0.0/16", "policy drop", "ip daddr 192.0.2.3 accept"} {
		if !strings.Contains(policy, rule) {
			t.Fatal("missing policy", rule)
		}
	}
	if _, err = networkPolicy("tap\";bad", "192.0.2.2", nil, nil, nil, nil); err == nil {
		t.Fatal("unsafe device accepted")
	}
}

func TestNetworkPolicyDropsDeniedPortsBeforeEgress(t *testing.T) {
	policy, err := networkPolicy("tap0", "192.0.2.2", nil, []string{"0.0.0.0/0"}, nil, []int{25, 465, 587})
	if err != nil {
		t.Fatal(err)
	}
	drop := strings.Index(policy, "tcp dport { 25, 465, 587 } drop")
	accept := strings.Index(policy, "meta l4proto { tcp, udp, icmp } accept")
	if drop < 0 || accept < 0 || drop > accept {
		t.Fatal("mail ports are not dropped before egress is accepted", drop, accept)
	}
	for _, port := range []int{0, 70000} {
		if _, err = networkPolicy("tap0", "192.0.2.2", nil, nil, nil, []int{port}); !errors.Is(err, ErrInvalid) {
			t.Fatal("bad port accepted", port, err)
		}
	}
}
