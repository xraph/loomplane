// Package buildpolicy gates source execution on administrator-owned worker settings.
package buildpolicy

import "errors"

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
		return errors.New("untrusted builds on shared or unclassified workers require Kata isolation; configure a Kata build runtime or use an explicitly dedicated builder")
	}
	return nil
}

// CheckBackend accepts only isolation evidence produced by the selected backend.
func CheckBackend(details map[string]string, backend string, pooled bool) error {
	if backend == "firecracker" {
		if details["build_isolation"] != "ready" || len(details["runtime_identity"]) != 64 || details["host_id"] == "" {
			return errors.New("the Firecracker host must pass its isolation check before accepting source")
		}
		return nil
	}
	return Check(details, backend == "kubernetes", pooled)
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
