// Package buildpolicy gates source execution on administrator-owned worker settings.
package buildpolicy

import (
	"errors"
	"strconv"
	"strings"
)

// ValidateWorkers accepts an explicit dedicated worker or a shared worker.
// Unclassified workers are shared until their administrator classifies them.
func ValidateWorkers(value string) error {
	if value != "" && value != "shared" && value != "dedicated" {
		return errors.New("build_workers must be shared or dedicated")
	}
	return nil
}

// Check treats every source build as untrusted. A repository cannot opt out.
// pooled comes from platform inventory and overrides a dedicated declaration.
func Check(details map[string]string, kubernetes, pooled bool) error {
	if err := ValidateWorkers(details["build_workers"]); err != nil {
		return err
	}
	class := details["build_runtime_class"]
	if class != "" {
		if !kubernetes {
			return errors.New("SSH/Docker builds do not support a Kata runtime; use a dedicated builder")
		}
		if details["build_isolation"] != "ready" || details["build_runtime_identity"] == "" {
			return errors.New("the selected Kata build runtime must pass its connection check")
		}
		return nil
	}
	if pooled || details["build_workers"] != "dedicated" {
		if !kubernetes {
			return errors.New("untrusted builds need a dedicated Docker builder outside a shared server pool")
		}
		return errors.New("untrusted builds on shared or unclassified workers require Kata isolation; configure a Kata build runtime or use an explicitly dedicated builder")
	}
	return nil
}

// CheckBackend accepts only isolation evidence produced by the selected backend.
func CheckBackend(details map[string]string, backend string, pooled bool) error {
	switch backend {
	case "firecracker":
		if !microVMEvidence(details) {
			return errors.New("the Firecracker host must pass its isolation check before accepting source")
		}
		return nil
	case "runner":
		return CheckRunner(details)
	case "aws":
		// CodeBuild isolates each build itself, in the reviewed account.
		// It isn't a Loomplane runner, so the microVM rule doesn't apply.
		return nil
	}
	return Check(details, backend == "kubernetes", pooled)
}

// microVMEvidence reports whether details carry the evidence a Firecracker
// host's own check produces: isolation ready, its runtime identity and its
// host ID.
func microVMEvidence(details map[string]string) bool {
	return details["build_isolation"] == "ready" && len(details["runtime_identity"]) == 64 && details["host_id"] != ""
}

// CheckRunner accepts a runner pool only with microVM evidence: every runner
// build runs in its own Firecracker microVM, and there is no container
// fallback. A pool that serves every workspace (scope "platform") must also
// pass the platform host policy, because the next build on it may belong to
// another workspace.
func CheckRunner(details map[string]string) error {
	if details["isolation"] != "firecracker" || !microVMEvidence(details) {
		return errors.New("runner builds run only in Firecracker microVMs; the pool needs a runner that passed its KVM and microVM check")
	}
	if details["pool_scope"] == "platform" {
		return CheckPlatform(RunnerPlatformEvidence(details))
	}
	return nil
}

// RunnerPlatformEvidence reads the platform host evidence a runner reports in
// its pool's details.
func RunnerPlatformEvidence(details map[string]string) PlatformEvidence {
	e := PlatformEvidence{
		Isolation:       microVMEvidence(details),
		RuntimeIdentity: details["runtime_identity"],
		HostID:          details["host_id"],
		Platform:        details["platform"] == "true",
		SMTActive:       details["smt_active"] != "false",
		PerOrgVMs:       atoi(details["per_org_vms"]),
		PerOrgVCPUs:     atoi(details["per_org_vcpus"]),
		PerOrgMemoryMiB: atoi(details["per_org_memory_mib"]),
	}
	for _, p := range strings.Split(details["denied_ports"], ",") {
		if n := atoi(strings.TrimSpace(p)); n > 0 {
			e.DeniedPorts = append(e.DeniedPorts, n)
		}
	}
	return e
}

// atoi is a non-negative decimal, or 0 when s isn't one.
func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// PlatformEvidence is what a shared platform host reports about itself. Plan 2
// maps firecracker.Capabilities into it, so this package stays free of that import.
type PlatformEvidence struct {
	Isolation                               bool
	RuntimeIdentity, HostID                 string
	Platform, SMTActive                     bool
	DeniedPorts                             []int
	PerOrgVMs, PerOrgVCPUs, PerOrgMemoryMiB int
}

// CheckPlatform refuses a host that cannot safely serve every workspace.
// Private ranges and the host's own addresses are not evidence here, because
// networkPolicy always drops them.
func CheckPlatform(e PlatformEvidence) error {
	if !e.Isolation || len(e.RuntimeIdentity) != 64 || e.HostID == "" {
		return errors.New("the platform host must pass its isolation check before accepting source")
	}
	if !e.Platform {
		return errors.New("the platform host must run in platform mode")
	}
	if e.SMTActive {
		return errors.New("the platform host must run with SMT off")
	}
	denied := map[int]bool{}
	for _, p := range e.DeniedPorts {
		denied[p] = true
	}
	for _, p := range []int{25, 465, 587} { // Mirrors firecracker.MailPorts.
		if !denied[p] {
			return errors.New("the platform host must deny outbound mail ports 25, 465 and 587")
		}
	}
	if e.PerOrgVMs < 1 || e.PerOrgVCPUs < 1 || e.PerOrgMemoryMiB < 1 {
		return errors.New("the platform host must set a per-organization limit on VMs, vCPUs and memory")
	}
	return nil
}
