package planning

import (
	"encoding/json"

	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// instanceExecutionManifest makes the runtime policy explicit without rewriting
// a published template or an already sealed instance snapshot.
func instanceExecutionManifest(input manifest.Manifest) (*manifest.Manifest, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var result manifest.Manifest
	if decodeErr := json.Unmarshal(raw, &result); decodeErr != nil {
		return nil, decodeErr
	}
	for name, service := range result.Services {
		if service.PodGroup == nil {
			service.PodGroup = &manifest.PodGroup{SurgeReplicas: 0}
		}
		if service.PodGroup.RuntimeStorageMiB == 0 {
			service.PodGroup.RuntimeStorageMiB = provider.DefaultInstanceContainerStorageMiB
		}
		if service.PodGroup.RuntimeStorageMiB < provider.DefaultInstanceContainerStorageMiB {
			return nil, provider.ErrWorkloadSpec
		}
		service.PodGroup.IsolationRevision = provider.InstanceIsolationRevision
		result.Services[name] = service
	}
	return &result, nil
}
