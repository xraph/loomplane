package deployments

import (
	"context"

	"github.com/xraph/loomplane/internal/clouds/registry"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// imageBuild is one call to ImageBuilder.Build: what every backend shares.
type imageBuild struct {
	job   *Job
	state *ImageArtifact
	save  func() error
	// registry is the reviewed registry the image is pushed to, or the
	// workspace's repository in the platform registry. Nil when the
	// service has none.
	registry *registry.Config
	// collector makes Build's error safe to show when it returns. A
	// backend that starts a collector of its own for the build operation
	// puts it here, so the error is redacted with that one's credentials.
	collector *Collector
}

// builderBackend is one way to turn approved source into an image.
type builderBackend struct {
	// name says which backend this is, for tests.
	name string
	// owns reports whether this backend builds cfg's image.
	owns func(cfg *planning.ServiceConfig) bool
	// whole means the backend takes the build before Build reads a registry,
	// a reused image or the gate: a managed Kubernetes build resolves the
	// cluster's own registry, and the deploy gates refuse it first.
	whole bool
	// platformRegistry swaps the reviewed registry for the workspace's
	// repository in the platform registry.
	platformRegistry bool
	// gated runs the workspace build gate before source leaves Loomplane.
	gated bool
	build func(ctx context.Context, b *ImageBuilder, run *imageBuild) (string, error)
}

// builderBackends are tried in order and the first that owns a service
// builds it. The last owns every service, so there is always one.
var builderBackends = []builderBackend{
	{
		name:  "kubernetes",
		owns:  func(cfg *planning.ServiceConfig) bool { return cfg.ManagedKubernetesBuild() },
		whole: true,
		build: func(ctx context.Context, b *ImageBuilder, run *imageBuild) (string, error) {
			return b.buildInKubernetes(ctx, run.job, run.state, run.save)
		},
	},
	{
		name:             "platform",
		owns:             func(cfg *planning.ServiceConfig) bool { return platformSourceBuild(cfg.BuildTarget) },
		platformRegistry: true,
		build: func(ctx context.Context, b *ImageBuilder, run *imageBuild) (string, error) {
			return b.buildOnPlatform(ctx, run.job, run.state, run.save, run.registry)
		},
	},
	{
		name:  "firecracker",
		owns:  func(cfg *planning.ServiceConfig) bool { return cfg.BuildTarget.BuilderCloud == connections.Firecracker },
		gated: true,
		build: func(ctx context.Context, b *ImageBuilder, run *imageBuild) (string, error) {
			return b.buildInFirecracker(ctx, run.job, run.state, run.save, run.registry)
		},
	},
	{
		name:  "runner",
		owns:  func(cfg *planning.ServiceConfig) bool { return cfg.BuildTarget.BuilderCloud == connections.Runner },
		gated: true,
		build: func(ctx context.Context, b *ImageBuilder, run *imageBuild) (string, error) {
			return b.buildOnRunner(ctx, run.job, run.state, run.save, run.registry)
		},
	},
	{
		name:  "codebuild",
		owns:  func(cfg *planning.ServiceConfig) bool { return cfg.BuildTarget.BuilderCloud == connections.AWS },
		gated: true,
		build: func(ctx context.Context, b *ImageBuilder, run *imageBuild) (string, error) {
			return b.buildOnCodeBuild(ctx, run.job, run.state, run.save, run.registry)
		},
	},
	{
		name:  "docker",
		owns:  func(*planning.ServiceConfig) bool { return true },
		gated: true,
		build: func(ctx context.Context, b *ImageBuilder, run *imageBuild) (string, error) {
			return b.buildWithDocker(ctx, run)
		},
	},
}

// builderBackendFor is the backend that builds cfg's image. cfg and its
// BuildTarget must be set.
func builderBackendFor(cfg *planning.ServiceConfig) builderBackend {
	for _, backend := range builderBackends {
		if backend.owns(cfg) {
			return backend
		}
	}
	return builderBackends[len(builderBackends)-1]
}
