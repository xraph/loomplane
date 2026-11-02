package aiagent

import cortexengine "github.com/xraph/cortex/engine"

// toolOptions are the engine options that give cortex the agent's
// operations, one tool each. Cortex fixes its tools when it builds its
// engine, so this is the one place every plan's operations go in. The
// operation registry (Task 12) fills it; 3c and 3d each add their own.
func (e *Extension) toolOptions() []cortexengine.Option {
	return nil
}
