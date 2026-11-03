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
	for _, want := range []string{"table netdev loomplane_meter_vm1", "counter egress", "priority -400", "policy accept"} {
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

func TestMeterSampleSkipsResetAndUnreadableCounters(t *testing.T) {
	raw := []byte(`{"nftables":[{"counter":{"name":"egress","packets":3,"bytes":4096}}]}`)
	got, err := meterSample("vm1", "c1", "c1", raw)
	if err != nil || got.VMID != "vm1" || got.CounterID != "c1" || got.Bytes != 4096 || got.Packets != 3 {
		t.Fatal(got, err)
	}
	if _, err = meterSample("vm1", "c1", "c2", raw); !errors.Is(err, errMeterReset) {
		t.Fatal("sample from a recreated table was reported under the old ID", err)
	}
	if _, err = meterSample("vm1", "c1", "c1", []byte("not json")); err == nil || errors.Is(err, errMeterReset) {
		t.Fatal("parse failure not reported", err)
	}
}

func TestMeterInstallFailureIsFatalOnlyOnPlatformHosts(t *testing.T) {
	boom := errors.New("nft failed")
	if _, err := meterInstallResult(true, "", boom); !errors.Is(err, boom) {
		t.Fatal("platform host started a VM without a meter")
	}
	if id, err := meterInstallResult(false, "", boom); err != nil || id != "" {
		t.Fatal("per-org host failed on a meter error", id, err)
	}
	if id, err := meterInstallResult(true, "c1", nil); err != nil || id != "c1" {
		t.Fatal(id, err)
	}
}
