package deployments

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

func (b *ImageBuilder) buildInKubernetes(ctx context.Context, job *Job, state *ImageArtifact, save func() error) (reference string, err error) {
	if locker, ok := job.BuildCredentials.(interface {
		WithRegistryLock(context.Context, string, string, func() error) error
	}); ok {
		err = locker.WithRegistryLock(ctx, job.Deployment.OrgID, job.Action.Service.Target, func() error {
			var buildErr error
			reference, buildErr = b.buildInKubernetesLocked(ctx, job, state, save)
			return buildErr
		})
		return reference, err
	}
	return b.buildInKubernetesLocked(ctx, job, state, save)
}

func (b *ImageBuilder) buildInKubernetesLocked(ctx context.Context, job *Job, state *ImageArtifact, save func() error) (reference string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	cfg := job.Action.Service
	collector := job.Collector("kubernetes-build", job.LogAttempt())
	defer func() { err = collector.safeError(err) }()
	connection, raw, err := b.connection(ctx, job, cfg.Target, connections.Kubernetes, cfg.BuildTarget.BuilderSettings, false)
	if err != nil {
		return "", err
	}
	collector.addCredential(raw)
	config, err := kubernetes.ParseConfig(raw, connection.Details)
	if err != nil {
		return "", err
	}
	client, err := kubernetes.New(config)
	if err != nil {
		return "", err
	}
	checked, err := client.Check(ctx)
	if err != nil {
		return "", err
	}
	if len(checked.Missing) != 0 || checked.Details["managed_builds"] != "ready" || !reflect.DeepEqual(planning.ReviewedBuildSettings(connections.Kubernetes, checked.Details), cfg.BuildTarget.BuilderSettings) {
		return "", errors.New("the cluster identity or managed build setup changed; check the connection and plan again")
	}
	client.Config.Details = checked.Details
	job.Progress(StepBuilding, "Preparing the cluster's image registry")
	registry, err := client.EnsureManagedRegistry(ctx, job.Deployment.OrgID, cfg.Target)
	if err != nil {
		return "", err
	}
	collector.addCredential(registry.DockerConfig)
	collector.addCredential([]byte(registry.Password))
	if checkErr := client.WaitManagedRegistry(ctx, registry); checkErr != nil {
		return "", fmt.Errorf("wait for registry storage and TLS: %w", checkErr)
	}
	if reused := reusedImage(job); reused != "" {
		if !strings.HasPrefix(reused, registry.Host+"/loomplane/") {
			return "", errors.New("the reused image belongs to another registry")
		}
		_, digest, _ := strings.Cut(reused, "@")
		if !imageDigest.MatchString(digest) {
			return "", errNoDigest
		}
		state.Reference, state.Digest, state.Registry, state.Architecture = reused, digest, cfg.ImageRegistry(), cfg.Artifact.Architecture
		return reused, save()
	}
	if job.SourceID == "" || job.Source == nil {
		return "", errors.New("the approved source artifact is missing")
	}
	if state.Reference != "" {
		if state.SourceID != job.SourceID || state.Builder != cfg.Target || state.Registry != cfg.ImageRegistry() || !strings.HasPrefix(state.Reference, registry.Host+"/loomplane/") || !imageDigest.MatchString(state.Digest) || !strings.HasSuffix(state.Reference, "@"+state.Digest) {
			return "", errors.New("the saved image belongs to another source or cluster build target")
		}
		return state.Reference, nil
	}
	if state.Operation == "" {
		operation, generateErr := typeid.Generate("build")
		if generateErr != nil {
			return "", generateErr
		}
		state.Operation = operation.String()
	}
	state.SourceID, state.Builder, state.Registry, state.Attempt, state.Architecture = job.SourceID, cfg.Target, cfg.ImageRegistry(), job.LogAttempt(), cfg.TargetSettings["architecture"]
	tag := sha256.Sum256([]byte(job.SourceID + "/" + state.Operation + "/" + imageOwner(job)))
	state.Tag = registry.Host + "/loomplane/" + imageName(job) + ":" + fmt.Sprintf("%x", tag[:12])
	if checkErr := save(); checkErr != nil {
		return "", checkErr
	}
	collector = job.Collector(state.Operation, state.Attempt)
	collector.addCredential(raw)
	collector.addCredential(registry.DockerConfig)
	collector.addCredential([]byte(registry.Password))
	collector.RecordState(ctx, "", "collecting", "Building in Kubernetes")
	defer func() {
		if err != nil {
			collector.RecordState(context.WithoutCancel(ctx), "", "incomplete", err.Error())
		}
	}()
	source, _, err := job.Source(ctx)
	if err != nil {
		return "", err
	}
	prepared, prepareErr := prepareImageContext(source, cfg)
	closeErr := source.Close()
	if checkErr := errors.Join(prepareErr, closeErr); checkErr != nil {
		return "", checkErr
	}
	job.Progress(StepBuilding, "Building and publishing inside Kubernetes")
	output := &clusterBuildOutput{ctx: ctx, collector: collector}
	builder := &kubernetes.ClusterBuilder{Client: client}
	digest, err := builder.Build(ctx, kubernetes.ClusterImageBuild{Org: job.Deployment.OrgID, Connection: cfg.Target, Operation: state.Operation, Dockerfile: prepared.Dockerfile, Image: state.Tag, Source: bytes.NewReader(prepared.Tar)}, registry, output)
	output.flush()
	if err != nil {
		return "", err
	}
	reference, err = byDigest(state.Tag, digest)
	if err != nil {
		return "", err
	}
	state.Reference, state.Digest = reference, digest
	if checkErr := save(); checkErr != nil {
		return "", checkErr
	}
	collector.RecordState(ctx, "", "complete", "Image ready: "+reference)
	return reference, nil
}

// Build logs cannot abort a deployment or grow without a bound when a build
// prints an enormous line. The Collector redacts before retaining each line.
type clusterBuildOutput struct {
	ctx       context.Context
	collector *Collector
	line      []byte
	truncated bool
}

func (w *clusterBuildOutput) Write(p []byte) (int, error) {
	for _, ch := range p {
		switch {
		case ch == '\n':
			w.flush()
		case len(w.line) < 32<<10:
			w.line = append(w.line, ch)
		default:
			w.truncated = true
		}
	}
	return len(p), nil
}
func (w *clusterBuildOutput) flush() {
	if len(w.line) == 0 && !w.truncated {
		return
	}
	_ = w.collector.Emit(w.ctx, buildLine{Stream: "stdout", Source: "buildkit", Message: string(w.line), Truncated: w.truncated}) //nolint:errcheck // The collector records failures; log retention cannot fail the build.
	w.line, w.truncated = w.line[:0], false
}
