package firecracker

import (
	"fmt"
	"regexp"
)

const PlatformConnection = "platform"

var MailPorts = []int{25, 465, 587}

var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type PlatformPeer struct{ Fingerprint string }

func ValidatePlatform(cfg HostConfig) error {
	// A half-set backstop would make every apply fail, on any host.
	o := cfg.Limits.PerOrg
	if o.VMs < 0 || o.VCPUs < 0 || o.MemoryMiB < 0 || (o != OrgLimits{} && (o.VMs == 0 || o.VCPUs == 0 || o.MemoryMiB == 0)) {
		return fmt.Errorf("%w: per-org limits must set VMs, vCPUs and memory together", ErrInvalid)
	}
	if cfg.Platform == nil {
		return nil
	}
	if len(cfg.Peers) > 0 {
		return fmt.Errorf("%w: platform mode takes no per-org peers", ErrInvalid)
	}
	if !fingerprintPattern.MatchString(cfg.Platform.Fingerprint) {
		return fmt.Errorf("%w: platform fingerprint must be 64 hex characters", ErrInvalid)
	}
	denied := map[int]bool{}
	for _, p := range cfg.DeniedPorts {
		if p < 1 || p > 65535 {
			return fmt.Errorf("%w: denied port %d out of range", ErrInvalid, p)
		}
		denied[p] = true
	}
	for _, p := range MailPorts {
		if !denied[p] {
			return fmt.Errorf("%w: mail port %d must be denied", ErrInvalid, p)
		}
	}
	l := cfg.Limits
	if o.VMs < 1 || o.VCPUs < 1 || o.MemoryMiB < 1 {
		return fmt.Errorf("%w: platform mode needs a per-org backstop", ErrInvalid)
	}
	if o.VMs > l.VMs || o.VCPUs > l.VCPUs || o.MemoryMiB > l.MemoryMiB {
		return fmt.Errorf("%w: per-org backstop exceeds host limits", ErrInvalid)
	}
	return nil
}
