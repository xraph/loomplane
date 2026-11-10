package firecracker

import "encoding/json"

// identityInput is the JSON the runtime identity hash covers. Platform and
// DeniedPorts are omitted when unset, and Limits.PerOrg when zero, so a
// non-platform host hashes exactly what it did before platform mode existed.
// Platform is the platform peer's Identity, or its Fingerprint until a
// rotation sets one, so rotating the platform certificate leaves the hash as
// it was.
func identityInput(cfg HostConfig) ([]byte, error) {
	fingerprint := ""
	if cfg.Platform != nil {
		fingerprint = cfg.Platform.identity()
	}
	return json.Marshal(struct {
		Assets              []Asset
		CNI                 map[string]string
		Subnet              string
		DNS, Egress, Denied []string
		UID                 int
		Limits              Limits
		Platform            string `json:",omitempty"`
		DeniedPorts         []int  `json:",omitempty"`
	}{[]Asset{cfg.Firecracker, cfg.Jailer, cfg.Kernel, cfg.RootFS}, cfg.CNIHashes, cfg.Subnet, cfg.DNS, cfg.EgressCIDRs, cfg.DeniedCIDRs, cfg.UIDBase, cfg.Limits, fingerprint, cfg.DeniedPorts})
}
