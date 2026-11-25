package planning

import (
	"slices"

	"github.com/xraph/loomplane/internal/buildpolicy"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// appPlatformRegistryEndpoint is DigitalOcean's container registry, the
// only one App Platform pulls from without a credential of its own.
const appPlatformRegistryEndpoint = "https://registry.digitalocean.com"

// registryMissing says how to give App Platform a registry to pull from.
const registryMissing = "the DigitalOcean account's container registry, added under Integrations as a registry (named in build.registry when there are several)"

// appPlatformGitHubNote is what a plan says about a service App Platform
// builds from its repository itself.
const appPlatformGitHubNote = " App Platform builds it from the repository through DigitalOcean's own GitHub or GitLab connection, which only someone signed in to DigitalOcean can set up, and DigitalOcean's API can't. Before deploying, connect the repository's account in DigitalOcean's control panel."

// configureAppPlatform decides how DigitalOcean App Platform gets a
// service's code.
//
// A service or worker Loomplane can build, one with a Dockerfile or a Go
// module, is built by Loomplane's builder and pushed to the DigitalOcean
// account's container registry, and App Platform runs that image. Nothing
// at DigitalOcean needs access to the repository, and the image is the
// approved commit by construction.
//
// App Platform builds the rest itself from the repository: static sites,
// which it can't run from an image, and services it builds with its own
// buildpacks. So do services where no builder or DigitalOcean registry is
// set up yet, as every service did before, with a warning that says what
// to add. Those need DigitalOcean's GitHub or GitLab connection.
func (p *planner) configureAppPlatform(name, base string, prov *provider.Provider, set manifest.Settings, cfg *ServiceConfig) {
	b := set.Build
	if b == nil {
		b = &manifest.Build{}
	}
	named := b.Builder != "" || b.Registry != "" || b.Image != ""
	if set.Target != "" || set.Endpoint != nil {
		p.problem(Finding{Code: "target-unsupported", Subject: ServiceSubject, Name: name, Path: base,
			Message: "Target and endpoint settings need a server or Kubernetes provider."})
	}
	p.serviceGPU(name, base, prov, set, cfg, nil)

	if cfg.Execution == provider.Static || (cfg.Image == "" && cfg.Ecosystem != "go") {
		what := "This static site can't run from an image."
		if cfg.Execution != provider.Static {
			what = "Loomplane builds a container image from a Dockerfile or a Go module, and this service has neither."
		}
		if named {
			p.problem(Finding{Code: "app-platform-build", Subject: ServiceSubject, Name: name, Path: base + ".build",
				Message: what + " App Platform builds it from the repository, so remove the build settings, or add a Dockerfile to build it with Loomplane."})
			return
		}
		p.warn(Finding{Code: "app-platform-repository", Subject: ServiceSubject, Name: name, Path: base,
			Message: what + appPlatformGitHubNote})
		return
	}

	build := &BuildTarget{Builder: b.Builder, Registry: b.Registry, Image: b.Image}
	if build.Image == "" {
		if build.Builder == "" {
			if d := p.in.BuildDefault; d != nil && d.Builder != "" {
				if d.Connection != nil && !slices.ContainsFunc(p.in.Connections, func(o ConnectionOption) bool { return o.ID == d.Connection.ID }) {
					p.in.Connections = append(p.in.Connections, *d.Connection)
				}
				build.Builder = d.Builder
			}
		}
		if build.Registry == "" {
			build.Registry = p.appPlatformRegistry()
		}
		if !named && (build.Builder == "" || build.Registry == "") {
			missing := "a runner pool to build on"
			switch {
			case build.Builder != "":
				missing = registryMissing
			case build.Registry == "":
				missing = "a runner pool to build on and " + registryMissing
			}
			p.warn(Finding{Code: "app-platform-repository", Subject: ServiceSubject, Name: name, Path: base + ".build",
				Message: "Loomplane can build this service itself, and App Platform would run the image without access to the repository, once there is " + missing + "." + appPlatformGitHubNote})
			return
		}
		builderCloud := p.builderCloud(build.Builder)
		if builderCloud != "server" {
			build.BuilderCloud = builderCloud
		}
		if builder := p.targetConnection(name, base+".build.builder", "builder", builderCloud, build.Builder); builder != nil {
			build.BuilderSettings = ReviewedBuildSettings(builder.Cloud, builder.Details)
			if err := buildpolicy.CheckBackend(builder.Details, builder.Cloud, builder.Pooled); err != nil {
				p.problem(Finding{Code: "build-isolation-required", Subject: ServiceSubject, Name: name, Path: base + ".build.builder", Message: err.Error()})
			}
			if arch := builder.Details["architecture"]; arch != "" && arch != "amd64" {
				p.problem(Finding{Code: "build-architecture", Subject: ServiceSubject, Name: name, Path: base + ".build.builder",
					Message: "App Platform runs amd64 images. Choose a builder with the amd64 architecture."})
			}
		}
		if builderCloud == "aws" {
			p.problem(Finding{Code: "app-platform-registry", Subject: ServiceSubject, Name: name, Path: base + ".build.builder",
				Message: "CodeBuild pushes only to Amazon ECR, and App Platform pulls from DigitalOcean's container registry. Choose a runner pool or a server to build on."})
		}
	}
	role := "registry"
	if build.Image != "" {
		role = "registry-pull"
	}
	if build.Registry == "" {
		p.problem(Finding{Code: "app-platform-registry", Subject: ServiceSubject, Name: name, Path: base + ".build.registry",
			Message: "App Platform pulls images only from the DigitalOcean account's container registry. Add it under Integrations as a registry, from the DigitalOcean account, and choose it here."})
	} else if registry := p.targetConnection(name, base+".build.registry", role, "registry", build.Registry); registry != nil {
		build.RegistrySettings = ReviewedConnectionSettings(registry.Cloud, registry.Details)
		if registry.Details["endpoint"] != appPlatformRegistryEndpoint {
			p.problem(Finding{Code: "app-platform-registry", Subject: ServiceSubject, Name: name, Path: base + ".build.registry",
				Message: "App Platform pulls images only from DigitalOcean's container registry. Choose a registry connection made from the DigitalOcean account the service runs in."})
		}
	}
	cfg.BuildTarget = build
	if cfg.Image == "" && b.Context != "" {
		cfg.Context = b.Context
	}
}

// appPlatformImage reports whether a deploys on App Platform from an image
// Loomplane builds, rather than App Platform building the repository.
func appPlatformImage(a Action) bool {
	return a.Provider == "digitalocean" && a.Service != nil && a.Service.BuildTarget != nil
}

// appPlatformRegistry is the one ready DigitalOcean registry connection,
// or "" when there are none or several to choose between.
func (p *planner) appPlatformRegistry() string {
	found := ""
	for _, c := range p.in.Connections {
		if c.Cloud != "registry" || !c.Ready || c.Details["endpoint"] != appPlatformRegistryEndpoint || c.Details["push"] != "ready" {
			continue
		}
		if found != "" {
			return ""
		}
		found = c.ID
	}
	return found
}
