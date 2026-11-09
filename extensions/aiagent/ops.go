package aiagent

import (
	cortexengine "github.com/xraph/cortex/engine"

	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentreports/reportops"
	"github.com/xraph/loomplane/internal/releaseprep"
)

// registerOps registers every operation the agent can call. Cortex fixes
// its tools when it builds its engine, so this is the one place every
// plan's operations go in; 3c and 3d each add a line.
func registerOps(r *agentops.Registry, d *agentops.Deps) {
	agentops.Builtins(r, d)
	reportops.Register(r, d)
	releaseprep.RegisterOps(r, d, releaseprep.Installed)
}

// toolOptions give cortex the agent's operations, one tool each.
func (e *Extension) toolOptions() []cortexengine.Option {
	return e.ops.EngineOptions()
}
