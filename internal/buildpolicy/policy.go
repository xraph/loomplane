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
