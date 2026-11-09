package planning

import (
	"fmt"

	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// ValidateWorkloadCapabilities requires explicit runtime support for pod features.
func ValidateWorkloadCapabilities(spec manifest.Service, capabilities provider.WorkloadCapabilities) error {
	if spec.PodGroup == nil {
		return nil
	}
	if err := provider.ValidatePodGroup(spec.PodGroup); err != nil {
		return err
	}
	if spec.Size == nil {
		return fmt.Errorf("%w: pod groups require explicit main container resources", provider.ErrWorkloadSpec)
	}
	if _, _, _, err := provider.PodGroupResources(int64(spec.Size.CPU*1000), int64(spec.Size.Memory), spec.PodGroup); err != nil {
		return err
	}
	if capabilities.Revision == "" || !capabilities.PodGroups || len(spec.PodGroup.Scratch) > 0 && !capabilities.ScratchVolumes {
		return fmt.Errorf("%w: the selected runtime does not support this pod group", provider.ErrWorkloadSpec)
	}
	return nil
}
func (p *planner) podGroupSettings(name, base string, set manifest.Settings, prov *provider.Provider, cfg *ServiceConfig) {
	if set.PodGroup == nil {
		return
	}
	var capabilities provider.WorkloadCapabilities
	if prov.Workloads != nil {
		capabilities = *prov.Workloads
	}
	if err := ValidateWorkloadCapabilities(manifest.Service{Settings: set}, capabilities); err != nil {
		p.problem(Finding{Code: "workload-capability", Subject: ServiceSubject, Name: name, Path: base + ".pod_group", Message: err.Error()})
		return
	}
	cfg.PodGroup = provider.ClonePodGroup(set.PodGroup)
	cfg.WorkloadCapabilityRevision = capabilities.Revision
}
