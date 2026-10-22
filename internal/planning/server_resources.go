package planning

import (
	"fmt"
	"math"

	"github.com/xraph/loomplane/pkg/manifest"
)

// ServerResources is shared by pool admission and actual Docker limits.
func ServerResources(size *manifest.Size) (cpuMillis, memoryBytes int64, err error) {
	if size == nil {
		return 1000, 512 << 20, nil
	}
	cpu := size.CPU * 1000
	if math.IsNaN(cpu) || math.IsInf(cpu, 0) || cpu < 1 || cpu > 1e9 || math.Trunc(cpu) != cpu || size.Memory <= 0 || int64(size.Memory) > 1<<40 {
		return 0, 0, fmt.Errorf("server size requires positive CPU millis and memory MiB")
	}
	return int64(cpu), int64(size.Memory) << 20, nil
}
