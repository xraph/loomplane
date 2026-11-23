package deployments

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/buildpolicy"
	"github.com/xraph/loomplane/internal/clouds/registry"
	"github.com/xraph/loomplane/internal/connections"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/planning"
)

// RunnerPools reach the runners of a runner pool for a build. The runner
// pool service fills it.
type RunnerPools interface {
	// BuildRunners are the runners of the pool behind connection that may
	// take a build now, best first: online, checked, and running the
	// toolchain the pool pins.
	BuildRunners(ctx context.Context, orgID, connection string) ([]string, error)
	// RunnerClient reaches one runner's Firecracker host over its node
	// stream.
	RunnerClient(ctx context.Context, orgID, nodeID string) (*fc.Client, error)
	// RecordBuild counts a build a runner finished.
	RecordBuild(ctx context.Context, orgID, nodeID string) error
	// BuildStarted and BuildEnded bracket a build on a runner, so an
	// on-demand pool's cold runner takes one build and then goes.
	BuildStarted(ctx context.Context, orgID, nodeID string) error
	BuildEnded(ctx context.Context, orgID, nodeID string) error
	// PoolDetails are a platform pool's details, for a build on global
	// runners, which have no connection.
	PoolDetails(ctx context.Context, orgID, builder string) (map[string]string, error)
	// MeterBuild meters a build on global runners as Loomplane Cloud
	// build minutes. Other builds record nothing.
	MeterBuild(ctx context.Context, orgID, builder, operation string, started, ended time.Time) error
	// Wait records a build waiting for a runner of pool and says how many
	// builds wait ahead of it; Unwait removes it.
	Wait(ctx context.Context, orgID, pool, operation string) (ahead int, err error)
	Unwait(ctx context.Context, operation string) error
}

// runnerWait is how long a build waits for a runner to be free before its
// step fails, and runnerPoll how often it looks again.
var (
	runnerWait = 20 * time.Minute
	runnerPoll = 10 * time.Second
)

// errNoRunner is a pool with no runner that may take a build.
var errNoRunner = errors.New("no runner in the pool is online with the toolchain it pins")

// buildOnRunner builds the approved source on a runner in the reviewed pool,
// in a fresh Firecracker microVM, exactly as a Firecracker host builds it:
// the same requests, authority, limits and cleanup, over the runner's node
// stream. The runner is saved before the VM is asked for, so a retry goes
// back to the runner that holds it.
func (b *ImageBuilder) buildOnRunner(ctx context.Context, job *Job, state *ImageArtifact, save func() error, reg *registry.Config) (reference string, err error) {
	if b.Runners == nil {
		return "", errors.New("runner builds aren't available here")
	}
	if b.FirecrackerAuthority == nil || reg == nil || job.Source == nil || job.SourceID == "" {
		return "", errors.New("runner builds need approved source, a registry and durable authority")
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Minute+runnerWait)
	defer cancel()
	target := job.Action.Service.BuildTarget
	pool, global, err := b.runnerPool(ctx, job)
	if err != nil {
		return "", err
	}
	if policyErr := buildpolicy.CheckBackend(pool.Details, connections.Runner, false); policyErr != nil {
		return "", policyErr
	}
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
	}
	scope := fc.Scope{Org: job.Deployment.OrgID, Connection: target.Builder, Project: job.Deployment.ProjectID, Environment: job.Deployment.EnvironmentID, Resource: state.Operation}
	deadline := time.Now().Add(runnerWait)
	defer func() { _ = b.Runners.Unwait(context.WithoutCancel(ctx), state.Operation) }() //nolint:errcheck // a stale wait expires on its own
	var tried []string
	for {
		candidates := []string{state.Runner}
		if state.Runner == "" {
			if candidates, err = b.Runners.BuildRunners(ctx, job.Deployment.OrgID, target.Builder); err != nil {
				return "", err
			}
		}
		for _, nodeID := range candidates {
			if nodeID == "" {
				continue
			}
			ref, done, buildErr := b.buildOnOneRunner(ctx, job, state, save, reg, pool.Details, nodeID, scope, global)
			if done {
				return ref, buildErr
			}
			tried = append(tried, fmt.Sprintf("%s: %v", nodeID, buildErr))
		}
		if time.Now().After(deadline) {
			if len(tried) == 0 {
				return "", fmt.Errorf("%w; start one with loomctl runner start, then retry the deployment", errNoRunner)
			}
			return "", fmt.Errorf("no runner in the pool took the build within %s: %s", runnerWait, strings.Join(tried, "; "))
		}
		message := "Waiting for a free runner in " + pool.Name
		if ahead, werr := b.Runners.Wait(ctx, job.Deployment.OrgID, target.Builder, state.Operation); werr == nil && ahead > 0 {
			message += fmt.Sprintf(" (%d builds ahead)", ahead)
		}
		job.Progress(StepBuilding, message)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(runnerPoll):
		}
		tried = tried[:0]
	}
}

// buildOnOneRunner builds on nodeID. done is false when the runner couldn't
// take the build and nothing reached it, so another may: it is offline,
// shows another toolchain than the pool pins, or is full.
func (b *ImageBuilder) buildOnOneRunner(ctx context.Context, job *Job, state *ImageArtifact, save func() error, reg *registry.Config, pool map[string]string, nodeID string, scope fc.Scope, global bool) (reference string, done bool, err error) {
	client, err := b.Runners.RunnerClient(ctx, job.Deployment.OrgID, nodeID)
	if err != nil {
		return "", state.Runner != "", err
	}
	defer client.Close()
	caps, err := client.Check(ctx)
	if err != nil {
		return "", state.Runner != "", err
	}
	// A global runner shows, right before it gets source, that it still
	// passes the platform policy: SMT off, mail denied, per-workspace limits.
	if global {
		if policyErr := buildpolicy.CheckPlatform(buildpolicy.PlatformEvidence{Isolation: caps.Isolation, RuntimeIdentity: caps.RuntimeIdentity, HostID: caps.HostID,
			Platform: caps.Platform, SMTActive: caps.SMTActive, DeniedPorts: caps.DeniedPorts,
			PerOrgVMs: caps.Limits.PerOrg.VMs, PerOrgVCPUs: caps.Limits.PerOrg.VCPUs, PerOrgMemoryMiB: caps.Limits.PerOrg.MemoryMiB}); policyErr != nil {
			return "", state.Runner != "", policyErr
		}
	}
	// The pool pins one toolchain and architecture. A runner showing another
	// never gets source, whatever the pool's list said a moment ago.
	if client.Details["runtime_identity"] != pool["runtime_identity"] || client.Details["architecture"] != pool["architecture"] || !reflect.DeepEqual(planning.ReviewedBuildSettings(connections.Runner, pool), job.Action.Service.BuildTarget.BuilderSettings) {
		return "", state.Runner != "", errors.New("the runner shows another toolchain than the pool pins")
	}
	fresh := state.Runner == ""
	state.Runner = nodeID
	state.SourceID, state.Builder, state.Registry, state.Attempt, state.Architecture = job.SourceID, job.Action.Service.BuildTarget.Builder, job.Action.Service.BuildTarget.Registry, job.LogAttempt(), client.Details["architecture"]
	if saveErr := save(); saveErr != nil {
		return "", true, saveErr
	}
	exit := &firecrackerBuildExit{}
	if startErr := b.Runners.BuildStarted(ctx, job.Deployment.OrgID, nodeID); startErr != nil {
		job.Progress(StepBuilding, "Building; the runner's build count wasn't updated")
	}
	defer func() {
		if endErr := b.Runners.BuildEnded(context.WithoutCancel(ctx), job.Deployment.OrgID, nodeID); endErr != nil {
			job.Progress(StepBuilding, "The runner's build count wasn't updated")
		}
	}()
	reference, err = b.runFirecrackerBuild(ctx, job, state, save, reg, firecrackerBuildHost{client: client, scope: scope, exit: exit})
	if !exit.started.IsZero() {
		ended := exit.ended
		if ended.IsZero() {
			ended = time.Now()
		}
		if meterErr := b.Runners.MeterBuild(context.WithoutCancel(ctx), job.Deployment.OrgID, job.Action.Service.BuildTarget.Builder, state.Operation, exit.started, ended); meterErr != nil {
			job.Progress(StepBuilding, "The build's minutes weren't metered yet; they're retried with the next build")
		}
	}
	if err != nil && fresh && exit.started.IsZero() && errors.Is(err, fc.ErrConflict) {
		// The runner refused the VM before starting it, as a full runner
		// does. Nothing reached it, so another runner may take the build.
		state.Runner = ""
		if saveErr := save(); saveErr != nil {
			return "", true, saveErr
		}
		return "", false, errors.New("the runner is full")
	}
	if err == nil {
		if recordErr := b.Runners.RecordBuild(context.WithoutCancel(ctx), job.Deployment.OrgID, nodeID); recordErr != nil {
			job.Progress(StepBuilding, "Built; the runner's build count wasn't updated")
		}
	}
	return reference, true, err
}

// runnerPool is the reviewed pool a runner build goes to, as a connection:
// the workspace's own pool connection, or a platform pool of global runners,
// whose details come from the pool itself. global reports the second,
// whose builds are platform usage, gated and metered as Loomplane Cloud's.
func (b *ImageBuilder) runnerPool(ctx context.Context, job *Job) (pool *connections.Connection, global bool, err error) {
	target := job.Action.Service.BuildTarget
	if !strings.HasPrefix(target.Builder, "rpool_") {
		c, _, cerr := b.connection(ctx, job, target.Builder, connections.Runner, target.BuilderSettings, false)
		return c, false, cerr
	}
	if gateErr := b.gate().CanRun(ctx, job.Deployment.OrgID, metering.KindBuild, true); gateErr != nil {
		return nil, true, gateErr
	}
	details, err := b.Runners.PoolDetails(ctx, job.Deployment.OrgID, target.Builder)
	if err != nil {
		return nil, true, err
	}
	if !reflect.DeepEqual(planning.ReviewedBuildSettings(connections.Runner, details), target.BuilderSettings) {
		return nil, true, errors.New("the global runner pool changed since review; plan again")
	}
	return &connections.Connection{ID: target.Builder, Cloud: connections.Runner, Name: "Loomplane's global runners", Details: details}, true, nil
}
