package firecracker

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePlatform(t *testing.T) {
	ok := HostConfig{
		Platform:    &PlatformPeer{Fingerprint: strings.Repeat("a", 64)},
		DeniedPorts: []int{25, 465, 587, 6667},
		Limits:      Limits{VMs: 20, VCPUs: 32, MemoryMiB: 65536, DiskMiB: 1 << 20, PerOrg: OrgLimits{VMs: 10, VCPUs: 8, MemoryMiB: 16384}},
	}
	if err := ValidatePlatform(ok); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*HostConfig){
		"peers too":       func(c *HostConfig) { c.Peers = []Peer{{Fingerprint: strings.Repeat("b", 64), Org: "org"}} },
		"bad fingerprint": func(c *HostConfig) { c.Platform.Fingerprint = "short" },
		"mail open":       func(c *HostConfig) { c.DeniedPorts = []int{25, 587} },
		"no backstop":     func(c *HostConfig) { c.Limits.PerOrg = OrgLimits{} },
		"backstop > host": func(c *HostConfig) { c.Limits.PerOrg.VCPUs = 64 },
		"port range":      func(c *HostConfig) { c.DeniedPorts = []int{25, 465, 587, 70000} },
	}
	for name, mutate := range cases {
		c := ok
		c.Platform = &PlatformPeer{Fingerprint: ok.Platform.Fingerprint}
		mutate(&c)
		if !errors.Is(ValidatePlatform(c), ErrInvalid) {
			t.Fatal(name, "accepted")
		}
	}
	if ValidatePlatform(HostConfig{}) != nil {
		t.Fatal("non-platform config rejected")
	}
}

func TestPlatformRefusesSMT(t *testing.T) {
	platform := HostConfig{Platform: &PlatformPeer{Fingerprint: strings.Repeat("a", 64)}}
	if err := platformReady(platform, Capabilities{SMTActive: true}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("SMT accepted on a platform host", err)
	}
	if err := platformReady(platform, Capabilities{}); err != nil {
		t.Fatal(err)
	}
	if err := platformReady(HostConfig{}, Capabilities{SMTActive: true}); err != nil {
		t.Fatal("SMT refused on a per-org host", err)
	}
}
