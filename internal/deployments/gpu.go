package deployments

import (
	"encoding/json"
	"strconv"

	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/provider"
)

// gpuSpec writes the GPU, shared memory and weight cache settings into a spec and its labels.
// The runtime turns the GPU label into devices; sp.Resources.GPU is only for display. A
// weight cache asks for a size on Kubernetes alone, since servers have no volume quotas.
func gpuSpec(sp *cpprovider.ServiceSpec, labels map[string]string, gpu *provider.GPUPlacement, shmMB int, cache *recipes.WeightCache, providerName string) {
	if gpu != nil {
		raw, _ := json.Marshal(gpu) //nolint:errcheck // a placement always encodes
		labels[owned.LabelGPU] = string(raw)
		sp.Resources.GPU = gpu.Kind + ":" + strconv.Itoa(gpu.Count)
	}
	if shmMB > 0 {
		labels[owned.LabelShmMB] = strconv.Itoa(shmMB)
	}
	if cache != nil {
		volume := cpprovider.VolumeSpec{Name: "weights", Type: "persistent", MountPath: cache.MountPath}
		if providerName == "kubernetes" {
			volume.SizeMB = cache.KubernetesSizeMB
		}
		sp.Volumes = append(sp.Volumes, volume)
		labels[owned.LabelStateful] = "true"
	}
}
