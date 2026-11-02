package aiagent

import (
	cortexengine "github.com/xraph/cortex/engine"

	"github.com/xraph/loomplane/internal/agentops"
)

// registerOps registers every operation the agent can call. Cortex fixes
// its tools when it builds its engine, so this is the one place every
// plan's operations go in; 3c and 3d each add a line.
func registerOps(r *agentops.Registry, d *agentops.Deps) {
	agentops.Builtins(r, d)
}

// toolOptions give cortex the agent's operations, one tool each.
func (e *Extension) toolOptions() []cortexengine.Option {
	return e.ops.EngineOptions()
}
