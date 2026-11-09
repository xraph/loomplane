package deployments

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/registry"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/planning"
)

type refuseGate struct{ kinds []metering.Kind }

func (g *refuseGate) CanRun(_ context.Context, _ string, k metering.Kind, owned bool) error {
	g.kinds = append(g.kinds, k)
	if owned {
		return errors.New("platform work reached a customer gate")
	}
	return &metering.BlockedError{Reason: "Add a card"}
}

func TestRunnerStartRefusedByTheGateRecordsNothing(t *testing.T) {
	plan := &planning.Plan{ID: "plan", OrgID: "org", ProjectID: "project", Status: planning.StatusApproved, Executable: true,
		Actions: []planning.Action{{ID: "service:api", Op: planning.Create, Subject: planning.ServiceSubject}}}
	m := &memStore{d: map[string]*Deployment{}}
	r := NewRunner(RunnerDeps{Store: m, Plans: &fakePlans{plan: plan}, Gate: &refuseGate{}})
	if _, _, err := r.Start(t.Context(), "org", "plan"); !errors.Is(err, metering.ErrBlocked) {
		t.Fatalf("Start: %v, want ErrBlocked", err)
	}
	if len(m.d) != 0 {
		t.Fatalf("a refused start recorded %d deployments", len(m.d))
	}
}

// sourceBuildJob is a job that builds from source on a builder of the given
// cloud, with no reused image.
func sourceBuildJob(t *testing.T, cloud string) (*Job, *ImageBuilder) {
	t.Helper()
	job, builder, _, _ := imageBuildFixture(t, false)
	job.Action.Service.BuildTarget.BuilderCloud = cloud
	return job, builder
}

func TestImageBuilderGatesCustomerBuildsBeforeConnecting(t *testing.T) {
	for _, cloud := range []string{connections.Firecracker, connections.Server} {
		t.Run(cloud, func(t *testing.T) {
			g := &refuseGate{}
			job, b := sourceBuildJob(t, cloud)
			dialed := false
			b.Gate = g
			b.Connect = func(context.Context, *server.Config, server.NodeStreams) (*server.Client, error) {
				dialed = true
				return nil, errors.New("unreachable")
			}
			_, err := b.Build(t.Context(), job, &ImageArtifact{}, func() error { return nil })
			if !errors.Is(err, metering.ErrBlocked) || dialed {
				t.Fatalf("Build: %v dialed=%v", err, dialed)
			}
			if len(g.kinds) != 1 || g.kinds[0] != metering.KindBuild {
				t.Fatalf("kinds = %v", g.kinds)
			}
		})
	}
}

type registryOnlyPlatform struct{ PlatformHosts }

func (registryOnlyPlatform) Registry(context.Context, string) (*registry.Config, error) {
	return &registry.Config{}, nil
}

func TestImageBuilderNeverGatesPlatformSourceBuilds(t *testing.T) {
	g := &refuseGate{}
	job, b := sourceBuildJob(t, platformProvider)
	job.Action.Service.BuildTarget.Builder = platformProvider
	job.Action.Service.BuildTarget.Registry = ""
	job.Action.Service.BuildTarget.RegistrySettings = nil
	b.Gate, b.Platform = g, registryOnlyPlatform{}
	_, err := b.Build(t.Context(), job, &ImageArtifact{}, func() error { return nil })
	if errors.Is(err, metering.ErrBlocked) || len(g.kinds) != 0 {
		t.Fatalf("a platform build reached the gate: %v kinds=%v", err, g.kinds)
	}
}

func TestRunnerStartNeverRefusesATeardown(t *testing.T) {
	plan := &planning.Plan{ID: "plan", OrgID: "org", ProjectID: "project", Status: planning.StatusApproved, Executable: true,
		Actions: []planning.Action{{ID: "service:api", Op: planning.Delete, Subject: planning.ServiceSubject}}}
	g := &refuseGate{}
	r := NewRunner(RunnerDeps{Store: &memStore{d: map[string]*Deployment{}}, Plans: &fakePlans{plan: plan}, Gate: g})
	_, _, err := r.Start(t.Context(), "org", "plan")
	if errors.Is(err, metering.ErrBlocked) || len(g.kinds) != 0 {
		t.Fatalf("a teardown reached the gate: %v kinds=%v", err, g.kinds)
	}
}
