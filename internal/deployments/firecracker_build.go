package deployments

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/buildpolicy"
	"github.com/xraph/loomplane/internal/clouds/registry"
	"github.com/xraph/loomplane/internal/connections"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/platformcloud"
)

func (b *ImageBuilder) buildInFirecracker(ctx context.Context, job *Job, state *ImageArtifact, save func() error, reg *registry.Config) (reference string, err error) {
	if b.FirecrackerAuthority == nil || reg == nil || job.Source == nil || job.SourceID == "" {
		return "", errors.New("firecracker builds need approved source, a registry and durable authority")
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Minute)
	defer cancel()
	target := job.Action.Service.BuildTarget
	collector := job.Collector("firecracker-build", job.LogAttempt())
	defer func() { err = collector.safeError(err) }()
	connection, raw, err := b.connection(ctx, job, target.Builder, connections.Firecracker, target.BuilderSettings, false)
	if err != nil {
		return "", err
	}
	collector.addCredential(raw)
	client, err := fc.NewClient(raw, connection.Details)
	if err != nil {
		return "", err
	}
	defer client.Close()
	_, err = client.Check(ctx)
	if err != nil {
		return "", err
	}
	if !reflect.DeepEqual(planning.ReviewedBuildSettings(connections.Firecracker, client.Details), target.BuilderSettings) {
		return "", errors.New("firecracker builder identity changed; check the connection and plan again")
	}
	if policyErr := buildpolicy.CheckBackend(client.Details, connections.Firecracker, connection.Details["pool_id"] != ""); policyErr != nil {
		return "", policyErr
	}
	if state.Reference != "" {
		expected, e := byDigest(state.Tag, state.Digest)
		if e != nil || expected != state.Reference || state.SourceID != job.SourceID || state.Builder != target.Builder || state.Registry != target.Registry {
			return "", fc.ErrConflict
		}
		return state.Reference, nil
	}
	if state.Operation == "" {
		operation, e := typeid.Generate("build")
		if e != nil {
			return "", e
		}
		state.Operation = operation.String()
	}
	if state.SourceID != "" && (state.SourceID != job.SourceID || state.Builder != target.Builder || state.Registry != target.Registry) {
		return "", fc.ErrConflict
	}
	state.SourceID, state.Builder, state.Registry, state.Attempt, state.Architecture = job.SourceID, target.Builder, target.Registry, job.LogAttempt(), client.Details["architecture"]
	scope := fc.Scope{Org: job.Deployment.OrgID, Connection: target.Builder, Project: job.Deployment.ProjectID, Environment: job.Deployment.EnvironmentID, Resource: state.Operation}
	return b.runFirecrackerBuild(ctx, job, state, save, reg, firecrackerBuildHost{client: client, scope: scope, credential: raw})
}

// firecrackerBuildHost is the checked host a build runs on, the scope its VM
// runs under there, and what the build learns about how it ended.
type firecrackerBuildHost struct {
	client *fc.Client
	scope  fc.Scope
	// credential is the host credential, kept out of the build's logs.
	credential []byte
	// redact turns a failure into what the workspace may read, before the
	// collector flattens it. Nil passes it on.
	redact func(error) error
	// exit, when set, is told when the build VM ran and whether it is gone.
	exit *firecrackerBuildExit
}

// firecrackerBuildExit is how a build VM ended.
type firecrackerBuildExit struct {
	// applied is set once the VM was asked for, so one may be on the host.
	applied bool
	// started and ended bound the time the VM ran.
	started, ended time.Time
	// removed is set once the VM is known to be gone.
	removed bool
}

// runFirecrackerBuild builds the approved source in a fresh build VM on a
// checked host and pushes it to reg. The VM is removed when the build ends.
func (b *ImageBuilder) runFirecrackerBuild(ctx context.Context, job *Job, state *ImageArtifact, save func() error, reg *registry.Config, host firecrackerBuildHost) (reference string, err error) {
	client := host.client
	endpoint, err := url.Parse(reg.Endpoint)
	if err != nil {
		return "", err
	}
	tag := sha256.Sum256([]byte(job.SourceID + "/" + state.Operation + "/" + imageOwner(job)))
	state.Tag = endpoint.Host + "/" + reg.Repository + "/loomplane/" + imageName(job) + ":" + fmt.Sprintf("%x", tag[:12])
	if saveErr := save(); saveErr != nil {
		return "", saveErr
	}
	collector := job.Collector(state.Operation, state.Attempt)
	defer func() {
		if err != nil && host.redact != nil {
			err = host.redact(err)
		}
		err = collector.safeError(err)
	}()
	collector.addCredential(host.credential)
	collector.addCredential([]byte(reg.Password))
	collector.addCredential([]byte(reg.Token))
	auth, err := registry.New(reg).DockerAuth(ctx, reg.Repository+"/loomplane/"+imageName(job), "pull,push")
	if err != nil {
		return "", err
	}
	config, err := firecrackerRegistry(auth)
	if err != nil {
		return "", err
	}
	collector.addCredential(config)
	source, _, sourceErr := job.Source(ctx)
	if sourceErr != nil {
		return "", sourceErr
	}
	prepared, prepareErr := prepareImageContext(source, job.Action.Service)
	closeErr := source.Close()
	if sourceErr := errors.Join(prepareErr, closeErr); sourceErr != nil {
		return "", sourceErr
	}
	sourceDigest := sha256.Sum256(prepared.Tar)
	q := fc.Request{Scope: host.scope, RuntimeIdentity: client.Details["runtime_identity"], Spec: fc.Spec{Kind: "build", SourceDigest: fmt.Sprintf("%x", sourceDigest), VCPUs: platformcloud.BuildVCPUs, MemoryMiB: platformcloud.BuildMemoryMiB, DiskMiB: platformcloud.BuildDiskMiB, Dockerfile: ".loomplane-build/Dockerfile", BuildImage: state.Tag}, Values: fc.Values{RegistryConfig: config}}
	renew := func() error {
		var e error
		q.Authority, e = b.FirecrackerAuthority.IssueFirecracker(ctx, q.Scope, "build/"+state.Operation, firecrackerDigest(q.Spec))
		return e
	}
	if renewErr := renew(); renewErr != nil {
		return "", renewErr
	}
	exit := host.exit
	if exit == nil {
		exit = &firecrackerBuildExit{}
	}
	exit.applied = true
	_, err = client.Apply(ctx, q)
	if err != nil {
		return "", err
	}
	exit.started = time.Now()
	defer func() {
		exit.ended = time.Now()
		cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer stop()
		cleanup := q
		cleanup.Values = fc.Values{}
		var cleanupErr error
		cleanup.Authority, cleanupErr = b.FirecrackerAuthority.IssueFirecracker(cleanupCtx, q.Scope, "remove/"+state.Operation, firecrackerDigest(q.Spec))
		if cleanupErr == nil {
			_, cleanupErr = client.Mutate(cleanupCtx, cleanup, "remove")
		}
		if cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("build VM cleanup remains pending: %w", cleanupErr))
			return
		}
		exit.removed = true
	}()
	status, err := client.Guest(ctx, q, "status", nil)
	if err != nil {
		return "", err
	}
	if status.State == "awaiting-source" {
		if stepErr := renew(); stepErr != nil {
			return "", stepErr
		}
		status, err = client.Guest(ctx, q, "source", bytes.NewReader(prepared.Tar))
		if err != nil {
			return "", err
		}
	}
	collector.RecordState(ctx, "", "collecting", "Building in an isolated Firecracker VM")
	output := &clusterBuildOutput{ctx: ctx, collector: collector}
	previousOutput := ""
	for {
		delta := strings.TrimPrefix(status.Output, previousOutput)
		if delta != "" {
			if _, writeErr := output.Write([]byte(delta)); writeErr != nil {
				return "", writeErr
			}
		}
		previousOutput = status.Output
		output.flush()
		switch status.State {
		case "succeeded":
			reference, err = byDigest(state.Tag, status.Digest)
			if err != nil {
				return "", err
			}
			state.Reference, state.Digest = reference, status.Digest
			if stepErr := save(); stepErr != nil {
				return "", stepErr
			}
			collector.RecordState(ctx, "", "complete", "Image ready: "+reference)
			return reference, nil
		case "failed":
			return "", errors.New("the isolated build failed: " + status.Output)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
		if time.Until(q.Authority.Expires) < time.Minute {
			if stepErr := renew(); stepErr != nil {
				return "", stepErr
			}
		}
		status, err = client.Guest(ctx, q, "status", nil)
		if err != nil {
			return "", err
		}
	}
}
