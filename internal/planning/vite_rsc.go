package planning

import (
	"fmt"
	"slices"

	"github.com/xraph/loomplane/pkg/discovery"

	"github.com/xraph/loomplane/pkg/provider"
)

// checkViteRSC requires a production server when RSC is deployed dynamically.
// Static exports remain an explicit manifest kind override.
func (p *planner) checkViteRSC(name, base string, cfg *ServiceConfig, requirements []discovery.Requirement) {
	usesRSC := cfg.Framework == "vite-rsc" || slices.ContainsFunc(requirements, func(r discovery.Requirement) bool { return r.Name == "@vitejs/plugin-rsc" })
	if !usesRSC || cfg.Workload == provider.StaticSite {
		return
	}
	if cfg.Provider == "vercel" {
		p.problem(Finding{Code: "vite-rsc-runtime", Subject: ServiceSubject, Name: name, Path: base + ".provider",
			Message: fmt.Sprintf("%s uses Vite RSC, which needs a server runtime. Choose a Node or container target; Loomplane has no Vercel adapter for this plugin.", name)})
	}
	if cfg.Image == "" && (cfg.BuildTarget == nil || cfg.BuildTarget.Image == "") && cfg.Start == "" {
		p.problem(Finding{Code: "vite-rsc-start", Subject: ServiceSubject, Name: name, Path: base + ".source",
			Message: fmt.Sprintf("Add a production start script to %s's package.json, or provide a Dockerfile. Vite RSC's build does not define a production server command.", name)})
	}
}
