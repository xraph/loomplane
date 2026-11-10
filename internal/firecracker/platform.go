package firecracker

import (
	"fmt"
	"regexp"
	"slices"
	"sync/atomic"
)

const PlatformConnection = "platform"

var MailPorts = []int{25, 465, 587}

var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// PlatformPeer is the platform control plane a platform-mode host answers.
// Fingerprint is the hex SHA-256 of the client certificate it presents.
//
// Rotating that certificate takes two edits. First Next is set to the new
// certificate's fingerprint, so the host trusts both while the control plane
// switches over. Then Fingerprint becomes the new one and Next goes. The
// first time that happens, Identity is set to the fingerprint the host
// started with and stays that way for good.
type PlatformPeer struct {
	Fingerprint string
	// Next is a second certificate trusted alongside Fingerprint while the
	// control plane rotates to it.
	Next string `json:",omitempty"`
	// Identity is what the runtime identity hashes in place of Fingerprint.
	// It keeps the runtime identity, and every VM, route and tunnel bound to
	// it, the same across rotations. It grants no trust.
	Identity string `json:",omitempty"`
}

// Trusted is every fingerprint the host accepts as the platform, sorted.
func (p *PlatformPeer) Trusted() []string {
	if p == nil {
		return nil
	}
	out := []string{p.Fingerprint}
	if p.Next != "" {
		out = append(out, p.Next)
	}
	slices.Sort(out)
	return out
}

// identity is the fingerprint the runtime identity covers.
func (p *PlatformPeer) identity() string {
	if p.Identity != "" {
		return p.Identity
	}
	return p.Fingerprint
}

// PlatformTrust is the set of platform fingerprints a running agent accepts.
// It is swapped whole when the agent reloads its platform trust, so a
// request sees either the old set or the new one.
type PlatformTrust struct{ set atomic.Pointer[[]string] }

// NewPlatformTrust returns a trust set holding fingerprints.
func NewPlatformTrust(fingerprints ...string) *PlatformTrust {
	t := &PlatformTrust{}
	t.Store(fingerprints)
	return t
}

// Store replaces the trusted fingerprints.
func (t *PlatformTrust) Store(fingerprints []string) {
	set := slices.Clone(fingerprints)
	slices.Sort(set)
	t.set.Store(&set)
}

// Fingerprints returns a copy of the trusted fingerprints, sorted.
func (t *PlatformTrust) Fingerprints() []string {
	if t == nil {
		return nil
	}
	set := t.set.Load()
	if set == nil {
		return nil
	}
	return slices.Clone(*set)
}

// Trusts reports whether fp is one of the trusted fingerprints.
func (t *PlatformTrust) Trusts(fp string) bool {
	if t == nil || fp == "" {
		return false
	}
	set := t.set.Load()
	return set != nil && slices.Contains(*set, fp)
}

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
	p := cfg.Platform
	if !fingerprintPattern.MatchString(p.Fingerprint) {
		return fmt.Errorf("%w: platform fingerprint must be 64 hex characters", ErrInvalid)
	}
	if p.Next != "" && (!fingerprintPattern.MatchString(p.Next) || p.Next == p.Fingerprint) {
		return fmt.Errorf("%w: the next platform fingerprint must be 64 hex characters and differ from the current one", ErrInvalid)
	}
	if p.Identity != "" && !fingerprintPattern.MatchString(p.Identity) {
		return fmt.Errorf("%w: the platform identity must be 64 hex characters", ErrInvalid)
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
