package deployments

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/registry"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/platformcloud"
)

// platformSourceBuild reports whether a service builds its image from source
// on Loomplane Cloud, into the platform registry. A prebuilt image, or one
// pulled through the workspace's own registry, isn't.
func platformSourceBuild(target *planning.BuildTarget) bool {
	return target != nil && target.BuilderCloud == platformProvider && target.Builder == platformProvider && target.Image == "" && target.Registry == ""
}

// buildOnPlatform builds the approved source on Loomplane Cloud: in a fresh
// build VM on a platform host in the service's region, pushed to the
// workspace's own repository in the platform registry. The build placement
// counts against the workspace's concurrent builds and goes back when the VM
// exits. Source goes to the host only after a fresh check of the host passes
// the platform policy.
func (b *ImageBuilder) buildOnPlatform(ctx context.Context, job *Job, state *ImageArtifact, save func() error, reg *registry.Config) (reference string, err error) {
	if b.Platform == nil || b.FirecrackerAuthority == nil || reg == nil || job.Source == nil || job.SourceID == "" || job.Deployment == nil {
		return "", errors.New("Loomplane Cloud builds need approved source, the platform registry and durable authority") //nolint:staticcheck // Preserve the product name in this existing user-facing error.
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Minute)
	defer cancel()
	target := job.Action.Service.BuildTarget
	if state.Reference != "" {
		expected, e := byDigest(state.Tag, state.Digest)
		if e != nil || expected != state.Reference || state.SourceID != job.SourceID || state.Builder != target.Builder || state.Registry != target.Registry {
			return "", fc.ErrConflict
		}
		return state.Reference, nil
	}
	if state.SourceID != "" && (state.SourceID != job.SourceID || state.Builder != target.Builder || state.Registry != target.Registry) {
		return "", fc.ErrConflict
	}
	if state.Operation == "" {
		operation, e := typeid.Generate("build")
		if e != nil {
			return "", e
		}
		state.Operation = operation.String()
		// Saved before the host is reserved, so a retry reserves under the
		// same operation and finds its placement.
		if saveErr := save(); saveErr != nil {
			return "", saveErr
		}
	}
	d := job.Deployment
	req := platformcloud.Request{OrgID: d.OrgID, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, Resource: state.Operation, Region: job.Action.Region, Kind: "build",
		VCPUs: platformcloud.BuildVCPUs, MemoryMiB: platformcloud.BuildMemoryMiB, DiskMiB: platformcloud.BuildDiskMiB}
	client, scope, p, err := b.Platform.Session(ctx, d.OrgID, d.ProjectID, d.EnvironmentID, state.Operation, req)
	if err != nil {
		return "", err
	}
	defer client.Close()
	exit := &firecrackerBuildExit{}
	defer b.platformBuildExited(context.WithoutCancel(ctx), p, exit)
	// Checked again right before anything is sent: the session's check may
	// be a moment old.
	caps, err := client.Check(ctx)
	if err != nil {
		return "", platformRedact(ctx, "build check", err)
	}
	if checkErr := platformcloud.CheckBuildHost(ctx, d.OrgID, caps); checkErr != nil {
		return "", checkErr
	}
	state.SourceID, state.Builder, state.Registry, state.Attempt, state.Architecture = job.SourceID, target.Builder, target.Registry, job.LogAttempt(), client.Details["architecture"]
	return b.runFirecrackerBuild(ctx, job, state, save, reg, firecrackerBuildHost{client: client, scope: scope, exit: exit,
		redact: func(err error) error { return platformRedact(ctx, "build", err) }})
}

// platformBuildExited gives the build placement back once its VM has exited
// and meters the time the VM ran. A VM that may still be on its host keeps
// the placement's instance, so the platform cleanup finds and removes it.
func (b *ImageBuilder) platformBuildExited(ctx context.Context, p *platformcloud.Placement, exit *firecrackerBuildExit) {
	if exit.applied && !exit.started.IsZero() {
		if err := b.Platform.RecordBuild(ctx, *p, exit.started, exit.ended); err != nil {
			slog.ErrorContext(ctx, "meter a platform build", "org", p.OrgID, "placement", p.ID, "error", err.Error())
		}
	}
	var err error
	if !exit.applied || exit.removed {
		err = b.Platform.Removed(ctx, p.OrgID, p.ID)
	} else {
		err = b.Platform.Release(ctx, p.ID)
	}
	if err != nil {
		slog.ErrorContext(ctx, "release a platform build placement", "org", p.OrgID, "placement", p.ID, "error", err.Error())
	}
}

// platformPull gives the host a pull token for an image Loomplane Cloud
// built, scoped to that image's repository under the workspace's own. An
// image outside the workspace's repository is refused.
func (e *OwnedTarget) platformPull(ctx context.Context, labels map[string]string, values owned.RuntimeValues) (owned.RuntimeValues, error) {
	image := labels[labelImage]
	if image == "" {
		return values, nil // The source build has not produced its image yet.
	}
	if e.platformRegistry == nil {
		return values, errors.New("Loomplane Cloud images aren't available here") //nolint:staticcheck // Preserve the product name in this existing user-facing error.
	}
	reg, err := e.platformRegistry(ctx, labels[owned.LabelOrg])
	if err != nil {
		return values, err
	}
	endpoint, err := url.Parse(reg.Endpoint)
	if err != nil {
		return values, err
	}
	repository, ok := platformRepository(image, endpoint.Host, reg.Repository)
	if !ok {
		return values, platformRefusalError("The image is outside the workspace's Loomplane Cloud repository.")
	}
	auth, err := registry.New(reg).DockerAuth(ctx, repository, "pull")
	if err != nil {
		return values, err
	}
	values.PullAuth = base64.URLEncoding.EncodeToString(auth)
	return values, nil
}

// canonicalRepository is a repository path as the distribution spec writes
// it: lowercase components joined by single separators.
var canonicalRepository = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)

// platformRepository reads the repository of image, a reference
// host/repository[:tag]@sha256:digest, and reports whether it is a canonical
// repository under base on host. Anything else, a tag-only reference
// included, is refused.
func platformRepository(image, host, base string) (string, bool) {
	rest, ok := strings.CutPrefix(image, host+"/")
	if !ok {
		return "", false
	}
	name, digest, ok := strings.Cut(rest, "@")
	if !ok || !imageDigest.MatchString(digest) {
		return "", false
	}
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name = name[:i]
	}
	if !canonicalRepository.MatchString(name) || !strings.HasPrefix(name, base+"/") {
		return "", false
	}
	return name, true
}
