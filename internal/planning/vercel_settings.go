package planning

import "github.com/xraph/loomplane/pkg/manifest"

func (p *planner) configureVercel(name, base string, set manifest.Settings, cfg *ServiceConfig) {
	if set.Commands == nil && set.Vercel == nil {
		return
	}
	if cfg.Provider != "vercel" {
		p.problem(Finding{Code: "vercel-settings-provider", Subject: ServiceSubject, Name: name, Path: base,
			Message: "Command overrides and Vercel Services settings require the Vercel provider."})
		return
	}
	cfg.Commands, cfg.Vercel = set.Commands, set.Vercel
	if set.Vercel == nil || !set.Vercel.Services {
		return
	}
	if set.Commands.HasOverrides() || set.Build != nil || set.Worker != nil || set.PodGroup != nil {
		p.problem(Finding{Code: "vercel-services-build", Subject: ServiceSubject, Name: name, Path: base + ".vercel",
			Message: "Vercel Services builds each member from vercel.json. Set per-service commands in vercel.service_commands and remove application-level build settings."})
	}
	cfg.Source = set.Vercel.Root
	if cfg.Source == "" {
		cfg.Source = "."
	}
	cfg.Framework, cfg.Build, cfg.Image, cfg.Context = "", "", "", ""
}
