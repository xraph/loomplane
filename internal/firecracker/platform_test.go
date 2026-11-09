package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

func TestCapabilitiesOmitPlatformFieldsWhenUnset(t *testing.T) {
	raw, err := json.Marshal(Capabilities{HostID: "h", Limits: Limits{VMs: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Platform", "SMTActive", "DeniedPorts", "PerOrg"} {
		if strings.Contains(string(raw), key) {
			t.Fatal("older control planes would reject", key, string(raw))
		}
	}
	raw, err = json.Marshal(Capabilities{Platform: true, SMTActive: true, DeniedPorts: []int{25}, Limits: Limits{PerOrg: OrgLimits{VMs: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Platform", "SMTActive", "DeniedPorts", "PerOrg"} {
		if !strings.Contains(string(raw), key) {
			t.Fatal("missing", key, string(raw))
		}
	}
}

func TestRunHostRefusesPlatformWithPeers(t *testing.T) {
	cfg := HostConfig{
		HostKey:  strings.Repeat("ab", 32),
		Platform: &PlatformPeer{Fingerprint: strings.Repeat("a", 64)},
		Peers:    []Peer{{Fingerprint: strings.Repeat("b", 64), Org: "org"}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "host.json")
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = RunHost(context.Background(), path); !errors.Is(err, ErrInvalid) {
		t.Fatal("platform config with per-org peers started", err)
	}
}

func TestValidatePlatformRejectsPartialPerOrg(t *testing.T) {
	for _, o := range []OrgLimits{{VMs: 2}, {VMs: 2, VCPUs: 2}, {VMs: -1, VCPUs: 1, MemoryMiB: 1}} {
		if !errors.Is(ValidatePlatform(HostConfig{Limits: Limits{PerOrg: o}}), ErrInvalid) {
			t.Fatal("accepted", o)
		}
	}
	if ValidatePlatform(HostConfig{Limits: Limits{PerOrg: OrgLimits{VMs: 2, VCPUs: 2, MemoryMiB: 512}}}) != nil {
		t.Fatal("complete per-org limits rejected")
	}
}

func TestIdentityInputUnchangedForNonPlatformHosts(t *testing.T) {
	cfg := HostConfig{
		Firecracker: Asset{Path: "/fc", SHA256: "f"}, Jailer: Asset{Path: "/j", SHA256: "j"},
		Kernel: Asset{Path: "/k", SHA256: "k"}, RootFS: Asset{Path: "/r", SHA256: "r"},
		CNIHashes: map[string]string{"ptp": "p"}, Subnet: "10.9.0.0/16",
		DNS: []string{"1.1.1.1"}, EgressCIDRs: []string{"0.0.0.0/0"}, DeniedCIDRs: []string{"10.0.0.0/8"},
		UIDBase: 20000, Limits: Limits{VMs: 4, VCPUs: 8, MemoryMiB: 8192, DiskMiB: 32768},
	}
	// Literal output of the ed649bd4 hash struct for this config.
	want := `{"Assets":[{"Path":"/fc","SHA256":"f"},{"Path":"/j","SHA256":"j"},{"Path":"/k","SHA256":"k"},{"Path":"/r","SHA256":"r"}],"CNI":{"ptp":"p"},"Subnet":"10.9.0.0/16","DNS":["1.1.1.1"],"Egress":["0.0.0.0/0"],"Denied":["10.0.0.0/8"],"UID":20000,"Limits":{"VMs":4,"VCPUs":8,"MemoryMiB":8192,"DiskMiB":32768}}`
	got, err := identityInput(cfg)
	if err != nil || string(got) != want {
		t.Fatal(string(got), err)
	}
	cfg.Platform = &PlatformPeer{Fingerprint: strings.Repeat("a", 64)}
	cfg.DeniedPorts = []int{25}
	if got, _ = identityInput(cfg); string(got) == want {
		t.Fatal("platform config shares the per-org identity")
	}
}
