package firecracker

import (
	"errors"
	"strings"
	"testing"
)

func TestMeterPolicyIsSeparateFromVMPolicy(t *testing.T) {
	policy, err := meterPolicy("veth-x", "vm1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"table netdev loomplane_meter_vm1", "counter egress", "priority -600", "policy accept"} {
		if !strings.Contains(policy, want) {
			t.Fatal("missing", want)
		}
	}
	if strings.Contains(policy, "drop") {
		t.Fatal("meter drops traffic")
	}
	if _, err = meterPolicy("tap\";bad", "vm1"); !errors.Is(err, ErrInvalid) {
		t.Fatal("unsafe device accepted")
	}
	if _, err = meterPolicy("veth-x", "vm\";1"); !errors.Is(err, ErrInvalid) {
		t.Fatal("unsafe VM ID accepted")
	}
}

func TestParseMeterReadsEgressCounter(t *testing.T) {
	raw := `{"nftables":[{"metainfo":{"version":"1"}},{"table":{"name":"t"}},{"counter":{"name":"egress","packets":3,"bytes":4096}}]}`
	b, p, err := parseMeter([]byte(raw))
	if err != nil || b != 4096 || p != 3 {
		t.Fatal(b, p, err)
	}
}
