//go:build integration

package deployments

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	cpauth "github.com/xraph/ctrlplane/auth"
	cpdeploy "github.com/xraph/ctrlplane/deploy"
	"github.com/xraph/ctrlplane/deploy/strategies"
	"github.com/xraph/ctrlplane/event"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"github.com/xraph/ctrlplane/store/memory"
	"golang.org/x/crypto/ssh"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func releaseDocker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("Docker %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func releaseRuntimeFixture(t *testing.T) (*server.Client, string, []byte) {
	t.Helper()
	if os.Getenv("LOOMPLANE_TEST_ROUTED_RUNTIME") != "1" {
		t.Skip("set LOOMPLANE_TEST_ROUTED_RUNTIME=1 for disposable real Docker/SSH ingress")
	}
	name := fmt.Sprintf("loomplane-release-test-%d", time.Now().UnixNano())
	releaseDocker(t, "run", "-d", "--privileged", "--name", name, "--label", "loomplane.test=routed-release", "-e", "DOCKER_TLS_CERTDIR=", "-p", "127.0.0.1::22", "docker:29-dind", "dockerd", "--host=unix:///var/run/docker.sock")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "docker", "rm", "-fv", name).CombinedOutput(); err != nil {
			t.Errorf("remove owned ingress fixture: %v %s", err, out)
		}
	})
	for range 60 {
		if exec.CommandContext(t.Context(), "docker", "exec", name, "docker", "info").Run() == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	releaseDocker(t, "exec", name, "sh", "-c", "apk add --no-cache openssh-server python3 && ssh-keygen -A && passwd -d root && mkdir -p /root/.ssh && chmod 700 /root/.ssh")
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "docker", "exec", "-i", name, "sh", "-c", "cat > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys && /usr/sbin/sshd -o PermitRootLogin=yes -o PasswordAuthentication=no -o AllowTcpForwarding=yes")
	cmd.Stdin = bytes.NewReader(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("start SSH: %v %s", runErr, out)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	port := strings.TrimPrefix(releaseDocker(t, "port", name, "22/tcp"), "127.0.0.1:")
	cfg, err := server.ParseConfig(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), map[string]string{"host": "127.0.0.1", "port": port, "user": "root", "host_key": releaseDocker(t, "exec", name, "cat", "/etc/ssh/ssh_host_ed25519_key.pub")})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Details["docker_id"] = releaseDocker(t, "exec", name, "docker", "info", "--format", "{{.ID}}")
	c, err := server.Dial(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.RequireFence = true
	t.Cleanup(func() { _ = c.Close() })
	return c, name, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

type releaseRuntime struct {
	*server.Runtime
	config *server.Config
}

func (r *releaseRuntime) ArmEffectFence(ctx context.Context, f server.EffectFence) error {
	c, err := server.Dial(ctx, r.config)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.ArmEffectFence(ctx, f)
}

type releaseRoutesFixture struct {
	pending bool
	calls   int
}

func (*releaseRoutesFixture) ValidateRuntimeRemoval(context.Context, string, string, string) error {
	return nil
}
func (r *releaseRoutesFixture) ReconcileReleaseRoutes(context.Context, security.Principal, cloudinfra.ReleaseRouteRequest) error {
	r.calls++
	if r.pending {
		return cloudinfra.ErrPending
	}
	return nil
}

type lostReleaseCreate struct {
	instance.Service
	lose bool
}

func (s *lostReleaseCreate) Create(ctx context.Context, r instance.CreateRequest) (*instance.Instance, error) {
	inst, err := s.Service.Create(ctx, r)
	if err == nil && s.lose {
		s.lose = false
		return nil, errors.New("fixture loses create response")
	}
	return inst, err
}

func TestRoutedReleaseRetainsRealSourceThroughDependentRetry(t *testing.T) {
	testRetainedRuntime(t, false)
}
func TestSharedSyncRetainsRealSourceThroughRoutingRetry(t *testing.T) {
	testRetainedRuntime(t, true)
}

type releaseCloudAuthority struct{}

func (releaseCloudAuthority) ValidateCloudSync(context.Context, *planning.Plan) error { return nil }
func (releaseCloudAuthority) ValidateCloudSyncRoutes(context.Context, *planning.Plan) (security.Principal, error) {
	return security.Principal{OrgID: "org", UserID: "usr"}, nil
}
func (releaseCloudAuthority) ValidateCloudSyncSourceCleanup(context.Context, *planning.Plan) (security.Principal, error) {
	return security.Principal{OrgID: "org", UserID: "usr"}, nil
}

type retainedRuntimeCloud struct{ *cloudResolver }

func (c retainedRuntimeCloud) RetainedAssignment(ctx context.Context, org, project, env string) (*cloudprofiles.Assignment, error) {
	return c.Resolve(ctx, org, project, env)
}

func testRetainedRuntime(t *testing.T, shared bool) {
	client, host, _ := releaseRuntimeFixture(t)
	ctx := server.WithEffectFence(t.Context(), server.EffectFence{Operation: "org/fixture/api", Generation: 1})
	if err := client.ArmEffectFence(ctx, server.EffectFence{Operation: "org/fixture/api", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	releaseDocker(t, "exec", host, "docker", "pull", "nginx:alpine")
	image := releaseDocker(t, "exec", host, "docker", "image", "inspect", "nginx:alpine", "--format", "{{index .RepoDigests 0}}")
	backend := &releaseRuntime{config: client.Config}
	backend.Runtime = &server.Runtime{Session: func(ctx context.Context, _ map[string]string) (*server.Client, error) {
		c, e := server.Dial(ctx, client.Config)
		if e == nil {
			c.RequireFence = true
		}
		return c, e
	}}
	controllers := memory.New()
	registry := cpprovider.NewRegistry()
	events := event.NewInMemoryBus()
	instances := &lostReleaseCreate{Service: instance.NewService(controllers, registry, events, &cpauth.NoopProvider{}, nil)}
	deploys := cpdeploy.NewService(controllers, controllers, registry, events, &cpauth.NoopProvider{}, nil)
	deploys.RegisterStrategy(strategies.NewRolling())
	secrets := &memSecrets{m: map[string][]byte{}}
	executor := &OwnedTarget{Cloud: connections.Server, Ctrl: CtrlPlane{Instances: instances, Deploys: deploys, Providers: registry}, Secrets: secrets, Backend: func(string, string) owned.Backend { return backend }}
	cfg := &planning.ServiceConfig{Provider: "server", Target: "runtime", Service: "api", Port: 80, Workload: provider.Worker, BuildTarget: &planning.BuildTarget{Image: image}, CloudPlacement: &planning.CloudPlacement{CloudID: "cloud"}}
	conn := &connections.Connection{ID: "runtime", OrgID: "org", Cloud: "server", Status: "ready", Account: "fixture", Details: map[string]string{"docker_id": client.Config.Details["docker_id"]}}
	cfg.TargetSettings = planning.ReviewedConnectionSettings(conn.Cloud, conn.Details)
	cfg.CloudPlacement = &planning.CloudPlacement{CloudID: "cloud", CloudRevision: 1, AssignmentRevision: 1, TargetID: "target", TargetRevision: 1, TargetName: "runtime", Provider: "server", ConnectionID: "runtime", AccountIdentity: connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details)}
	job := &Job{Deployment: &Deployment{ID: "source-deployment", StartedBy: "usr", PlanID: "source", OrgID: "org", ProjectID: "proj", EnvironmentID: "env"}, Plan: &planning.Plan{ID: "source"}, Connection: &connections.Connection{ID: "runtime"}, Action: planning.Action{ID: "service:api", Name: "api", Subject: planning.ServiceSubject, Provider: "server", ConnectionID: "runtime", Service: cfg}, Progress: func(string, string) {}}
	var checkpoint []byte
	job.Checkpoint = func(raw []byte) error { checkpoint = append([]byte(nil), raw...); return nil }
	source, err := executor.Apply(ctx, job)
	if err != nil {
		t.Fatal("source", err)
	}
	raw, _ := json.Marshal(cfg)
	old := &planning.Binding{ID: "binding", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "api", Provider: "server", ConnectionID: "runtime", ExternalID: source.ExternalID, PlanID: "source", Config: raw}
	job.Deployment = &Deployment{ID: "candidate-deployment", StartedBy: "usr", PlanID: "candidate", OrgID: "org", ProjectID: "proj", EnvironmentID: "env"}
	job.Plan = &planning.Plan{ID: "candidate"}
	job.Action.Op = planning.Replace
	job.Action.Routes = []cloudinfra.Route{{ID: "route"}}
	job.Replaces = old
	job.Resume = nil
	instances.lose = true
	if _, err = executor.Apply(ctx, job); err == nil || !strings.Contains(err.Error(), "fixture loses create response") {
		t.Fatal("lost create response was not reached", err)
	}
	var saved ownedState
	if err = json.Unmarshal(checkpoint, &saved); err != nil || saved.CandidateName == "" {
		t.Fatal("candidate identity not saved", err)
	}
	candidateID := releaseDocker(t, "exec", host, "docker", "ps", "-q", "--filter", "label=loomplane.revision=candidate-deployment/service:api")
	if candidateID == "" {
		t.Fatal("lost response removed candidate")
	}
	job.Resume = checkpoint
	candidate, err := executor.Apply(ctx, job)
	if err != nil {
		t.Fatal("retry candidate", err)
	}
	if candidate.ExternalID == source.ExternalID {
		t.Fatal("source overwritten")
	}
	job.Resume = checkpoint
	again, err := executor.Apply(ctx, job)
	if err != nil || again.ExternalID != candidate.ExternalID {
		t.Fatal("restart duplicated candidate", again, err)
	}
	current := *old
	current.PlanID = "candidate"
	current.ExternalID = candidate.ExternalID
	current.Replaced = []planning.Binding{*old}
	plan := &planning.Plan{ID: "candidate", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Actions: []planning.Action{job.Action, {ID: "service:consumer", Subject: planning.ServiceSubject, Name: "consumer", Op: planning.Update}}}
	d := &Deployment{ID: "candidate-deployment", StartedBy: "usr", PlanID: plan.ID, OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Status: StatusRunning, Steps: []Step{{ActionID: "service:api", Status: StepSucceeded, Replaced: old}, {ActionID: "service:consumer", Status: StepFailed}}}
	store := &memStore{d: map[string]*Deployment{d.ID: d}}
	plans := &fakePlans{plan: plan, bindings: map[string]*planning.Binding{"service:api": &current}}
	routes := &releaseRoutesFixture{pending: true}
	runner := NewRunner(RunnerDeps{Store: store, Plans: plans, Routes: routes, Providers: providers.Registry(), Executors: map[string]Executor{"server": executor}, Credentials: buildCredentialFunc(func(context.Context, string, string) (*connections.Connection, []byte, error) {
		return conn, nil, nil
	})})
	finalize := func() error { return runner.finishRoutedRelease(ctx, d) }
	if shared {
		plan.CloudSyncID = "sync"
		plan.Actions[1].Provider = "server"
		plan.Actions[1].ConnectionID = "runtime"
		plan.Actions[1].Service = cfg
		consumer := current
		consumer.Subject, consumer.Name = planning.ServiceSubject, "consumer"
		consumer.Replaced = nil
		plans.bindings["service:consumer"] = &consumer
		runner.cloudSyncAuth = releaseCloudAuthority{}
		runner.cloudProfiles = retainedRuntimeCloud{&cloudResolver{assignment: &cloudprofiles.Assignment{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", CloudID: "cloud", CloudRevision: 1, Revision: 1, Targets: []cloudprofiles.Target{{ID: "target", OrgID: "org", CloudID: "cloud", Name: "runtime", Provider: "server", ConnectionID: "runtime", Revision: 1}}}}}
		finalize = func() error {
			if routeErr := runner.ReconcileCloudPlanRoutes(ctx, "org", plan.ID, nil, nil); routeErr != nil {
				return routeErr
			}
			// Ready routes alone must not remove the retained source runtime.
			if n := releaseDocker(t, "exec", host, "docker", "ps", "-q", "--filter", "label=loomplane.environment=env"); len(strings.Fields(n)) != 2 {
				t.Fatal("shared route phase removed the source", n)
			}
			return runner.CleanupCloudPlan(ctx, "org", plan.ID, nil)
		}
	}
	assertTwo := func() {
		t.Helper()
		if n := releaseDocker(t, "exec", host, "docker", "ps", "-q", "--filter", "label=loomplane.environment=env"); len(strings.Fields(n)) != 2 {
			t.Fatal("source/candidate not retained", n)
		}
	}
	if err = finalize(); err == nil || routes.calls != 0 {
		t.Fatal("failed dependent allowed cleanup", err)
	}
	assertTwo()
	d.Steps[1].Status = StepSucceeded
	if err = finalize(); !errors.Is(err, cloudinfra.ErrPending) {
		t.Fatal("route readiness refusal", err)
	}
	assertTwo()
	routes.pending = false
	if err = finalize(); err != nil {
		t.Fatal("cleanup after retry", err)
	}
	if n := releaseDocker(t, "exec", host, "docker", "ps", "-q", "--filter", "label=loomplane.environment=env"); len(strings.Fields(n)) != 1 {
		t.Fatal("exact source cleanup", n)
	}
	if len(plans.bindings["service:api"].Replaced) != 0 {
		t.Fatal("source reservation reference retained")
	}
	// The next reviewed release retains this candidate as its exact source.
	retained := *plans.bindings["service:api"]
	job.Deployment = &Deployment{ID: "future-deployment", PlanID: "future", StartedBy: "usr", OrgID: "org", ProjectID: "proj", EnvironmentID: "env"}
	job.Plan = &planning.Plan{ID: "future"}
	job.Replaces = &retained
	job.Resume = nil
	future, err := executor.Apply(ctx, job)
	if err != nil || future.ExternalID == candidate.ExternalID || future.ExternalID == source.ExternalID {
		t.Fatal("future release identity", future, err)
	}
	assertTwo()
	futureBinding := retained
	futureBinding.PlanID = "future"
	futureBinding.ExternalID = future.ExternalID
	futureBinding.Replaced = []planning.Binding{retained}
	plans.bindings["service:api"] = &futureBinding
	calls := routes.calls
	if err = finalize(); err == nil || routes.calls != calls {
		t.Fatal("stale finalizer touched newer binding", err)
	}
	futurePlan := &planning.Plan{ID: "future", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Actions: []planning.Action{job.Action}}
	futureDeployment := &Deployment{ID: "future-deployment", PlanID: "future", StartedBy: "usr", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Status: StatusRunning, Steps: []Step{{ActionID: "service:api", Status: StepSucceeded, Replaced: &retained}}}
	plans.plan = futurePlan
	store.d[futureDeployment.ID] = futureDeployment
	if err = runner.finishRoutedRelease(ctx, futureDeployment); err != nil {
		t.Fatal("future release cleanup", err)
	}
	if n := releaseDocker(t, "exec", host, "docker", "ps", "-q", "--filter", "label=loomplane.environment=env"); len(strings.Fields(n)) != 1 {
		t.Fatal("future exact cleanup", n)
	}
	t.Log("Actual SSH/Docker and Ctrl Plane lifecycle; in-memory controller/deployment stores and route-readiness fixture. Public DNS/TLS is covered separately, not by this test.")
}
