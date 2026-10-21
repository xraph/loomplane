//go:build integration

package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os/exec"
	"strings"
	"testing"
	"time"

	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
)

type poolBuildCredentials struct {
	runtime, builder *connections.Connection
	raw              []byte
}

func (c *poolBuildCredentials) Credential(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
	for _, v := range []*connections.Connection{c.runtime, c.builder} {
		if v.OrgID == org && v.ID == id {
			return v, c.raw, nil
		}
	}
	return nil, nil, connections.ErrNotFound
}

type sourcePoolExecutor struct {
	poolExecutor
	owned   *deployments.OwnedTarget
	archive []byte
	built   chan string
}

func (e *sourcePoolExecutor) PreparePoolEffects(ctx context.Context, j *deployments.Job) error {
	return e.owned.PreparePoolEffects(ctx, j)
}
func (e *sourcePoolExecutor) CleanupPoolWorkload(ctx context.Context, j *deployments.Job, raw []byte, save func([]byte) error) error {
	return e.owned.CleanupPoolWorkload(ctx, j, raw, save)
}
func (e *sourcePoolExecutor) Apply(ctx context.Context, j *deployments.Job) (*deployments.Outcome, error) {
	j.SourceID = strings.Repeat("a", 40)
	j.Source = func(context.Context) (io.ReadCloser, int64, error) {
		return io.NopCloser(bytes.NewReader(e.archive)), int64(len(e.archive)), nil
	}
	state := struct {
		Image       deployments.ImageArtifact `json:"image"`
		EffectStage string                    `json:"effect_stage"`
	}{EffectStage: "building"}
	save := func() error {
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		return j.Checkpoint(raw)
	}
	if err := save(); err != nil {
		return nil, err
	}
	image, err := e.owned.Builder.Build(ctx, j, &state.Image, save)
	if err != nil {
		return nil, err
	}
	e.built <- image
	return nil, errors.New("fixture stops after image build, before runtime effects")
}

// A separate real Docker daemon participates as the source builder. Its Unix
// proxy creates a deterministic network delay before forwarding /build.
func verifyPoolSourceBuildFence(t *testing.T, host string, cfg *server.Config, raw []byte) {
	t.Helper()
	infraDocker(t, "exec", "-d", host, "dockerd", "--host=unix:///tmp/builder.sock", "--data-root=/tmp/builder-data", "--exec-root=/tmp/builder-exec", "--pidfile=/tmp/builder.pid", "--bridge=none", "--iptables=false")
	ready := false
	for attempt := 0; attempt < 100; attempt++ {
		if exec.CommandContext(t.Context(), "docker", "exec", host, "docker", "-H", "unix:///tmp/builder.sock", "info").Run() == nil {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("isolated builder daemon did not start")
	}
	infraDockerFile(t, host, "/tmp/builder-proxy.py", []byte(poolBuilderProxy))
	infraDocker(t, "exec", "-d", host, "python3", "/tmp/builder-proxy.py")
	for attempt := 0; attempt < 100; attempt++ {
		if exec.CommandContext(t.Context(), "docker", "exec", host, "test", "-S", "/tmp/builder-gate.sock").Run() == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	builderSettings := maps.Clone(cfg.Details)
	builderSettings["docker_socket"] = "/tmp/builder-gate.sock"
	builderConfig, err := server.ParseConfig(raw, builderSettings)
	if err != nil {
		t.Fatal(err)
	}
	builderClient, err := server.Dial(t.Context(), builderConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer builderClient.Close()
	details, missing, err := builderClient.Check(t.Context())
	if err != nil || len(missing) > 0 {
		t.Fatal(missing, err)
	}
	if details["docker_id"] == cfg.Details["docker_id"] {
		t.Fatal("builder is not a separate daemon")
	}
	builderConfig.Details = details
	runtimeConfig, err := server.ParseConfig(raw, cfg.Details)
	if err != nil {
		t.Fatal(err)
	}
	runtimeClient, err := server.Dial(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	runtimeDetails, runtimeMissing, err := runtimeClient.Check(t.Context())
	_ = runtimeClient.Close()
	if err != nil || len(runtimeMissing) > 0 {
		t.Fatal(runtimeMissing, err)
	}
	cfg.Details = runtimeDetails
	f := newPoolFixture(t)
	cs := connections.NewPgStore(f.db)
	runtime, err := cs.GetConnection(t.Context(), "org", "runtime-1")
	if err != nil {
		t.Fatal(err)
	}
	runtime.Details = maps.Clone(cfg.Details)
	body, _ := json.Marshal(runtime.Details)
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET details=$1 WHERE id='runtime-1'`, body); err != nil {
		t.Fatal(err)
	}
	credentials := &poolBuildCredentials{runtime: runtime, builder: &connections.Connection{ID: "builder", OrgID: "org", Name: "Isolated builder", Cloud: "server", Status: "ready", Details: details}, raw: raw}
	if err = cs.CreateConnection(t.Context(), credentials.builder); err != nil {
		t.Fatal(err)
	}
	request := f.request("env", "api")
	placement, err := f.store.PreviewPlacement(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	pin := f.plan(t, "build", request, placement)
	pin.AccountIdentity = connections.AccountIdentity("server", runtime.Account, runtime.Details)
	plan, err := f.plans.GetPlan(t.Context(), "org", "build")
	if err != nil {
		t.Fatal(err)
	}
	action := plan.Actions[0]
	action.Service.CloudPlacement = pin
	action.Service.TargetSettings = planning.ReviewedConnectionSettings("server", runtime.Details)
	action.Service.BuildTarget = &planning.BuildTarget{Builder: "builder", BuilderSettings: planning.ReviewedConnectionSettings("server", details)}
	action.Service.Image = "Dockerfile"
	action.Service.Context = "."
	actionRaw, _ := json.Marshal(action)
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions,0}',$1) WHERE id='build'`, actionRaw); err != nil {
		t.Fatal(err)
	}
	f.accept(t, "build")
	var archive bytes.Buffer
	zip := gzip.NewWriter(&archive)
	tw := tar.NewWriter(zip)
	for name, contents := range map[string]string{"Dockerfile": "FROM scratch\nCOPY marker /marker\n", "marker": "approved-source"} {
		if err = tw.WriteHeader(&tar.Header{Name: "repo-sha/" + name, Mode: 0600, Size: int64(len(contents))}); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = zip.Close(); err != nil {
		t.Fatal(err)
	}
	owned := &deployments.OwnedTarget{Cloud: "server", Credentials: credentials, Builder: &deployments.ImageBuilder{}, Ctrl: deployments.CtrlPlane{Providers: cpprovider.NewRegistry()}}
	executor := &sourcePoolExecutor{owned: owned, archive: archive.Bytes(), built: make(chan string, 2)}
	runner := deployments.NewRunner(deployments.RunnerDeps{Store: deployments.NewPgStore(f.db), Plans: f.plans, CloudProfiles: f.clouds, Credentials: credentials, Variables: poolVars{}, Providers: providers.Registry(), Executors: map[string]deployments.Executor{"server": executor}})
	dep, _, err := runner.Start(t.Context(), "org", "build")
	if err != nil {
		t.Fatal(err)
	}
	old := make(chan error, 1)
	go func() {
		old <- runner.Run(t.Context(), "org", dep, "service:api", nil, func([]byte) error { return nil })
	}()
	for attempt := 0; attempt < 100; attempt++ {
		if exec.CommandContext(t.Context(), "docker", "exec", host, "test", "-f", "/tmp/build-entered").Run() == nil {
			break
		}
		select {
		case err = <-old:
			t.Fatal("source build failed before barrier", err)
		default:
		}
		if attempt == 99 {
			t.Fatal("source build never entered real builder")
		}
		time.Sleep(50 * time.Millisecond)
	}
	killPoolLockSession(t, f)
	assertBusyRouteAttachment(t, f, "api")
	resumed := make(chan error, 1)
	go func() {
		resumed <- runner.Run(t.Context(), "org", dep, "service:api", nil, func([]byte) error { return nil })
	}()
	waitPoolSQL(t, f, `SELECT generation=2 FROM loomplane_pool_execution WHERE plan_id='build'`)
	select {
	case err = <-resumed:
		t.Fatal("resume did not wait for builder", err)
	case <-time.After(100 * time.Millisecond):
	}
	infraDocker(t, "exec", host, "touch", "/tmp/build-release")
	if err = <-old; err == nil {
		t.Fatal("old build published after generation change")
	}
	if err = <-resumed; err == nil {
		t.Fatal("fixture should stop before runtime")
	}
	select {
	case image := <-executor.built:
		if !strings.HasPrefix(image, "sha256:") {
			t.Fatal("source build did not produce Docker image", image)
		}
	default:
		t.Fatal("resumed production ImageBuilder never completed")
	}
	// A delayed request carries the previous epoch and cannot reach this builder.
	response, err := builderClient.Request(server.WithEffectFence(t.Context(), server.EffectFence{Operation: "org/build/api", Generation: 1}), "POST", "/build", bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 409 {
		t.Fatal("delayed old build escaped fence", response.StatusCode)
	}
	if _, err = f.clouds.SetProjectGrant(t.Context(), "org", "cloud", "proj", nil, true); err != nil {
		t.Fatal(err)
	}
	svc := poolRecoveryService(f, runner)
	in := poolReview(t, svc, cloudinfra.RecoveryInput{PlanID: "build", Service: "api"})
	acceptedDetails := credentials.builder.Details
	credentials.builder.Details = maps.Clone(acceptedDetails)
	credentials.builder.Details["docker_id"] = "substituted-daemon"
	blocked, blockErr := svc.RecoverReservation(t.Context(), security.Principal{OrgID: "org", UserID: "manager"}, "cloud", in)
	if blockErr == nil || blocked == nil || blocked.Phase != "blocked" {
		t.Fatal("substituted builder authorized cleanup", blocked, blockErr)
	}
	var held int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_capacity_reservations WHERE plan_id='build'`).Scan(&held); err != nil || held != 1 {
		t.Fatal("blocked builder lost capacity", held, err)
	}
	credentials.builder.Details = acceptedDetails
	credentials.builder.Generation++
	in = poolReview(t, svc, cloudinfra.RecoveryInput{PlanID: "build", Service: "api"})
	out, err := svc.RecoverReservation(t.Context(), security.Principal{OrgID: "org", UserID: "manager"}, "cloud", in)
	if err != nil || out.Phase != "released" || !out.StorageRetained {
		t.Fatalf("build-only cleanup %+v: %v", out, err)
	}
	if images := infraDocker(t, "exec", host, "docker", "-H", "unix:///tmp/builder.sock", "images", "-q"); images == "" {
		t.Fatal("builder image was silently deleted")
	}
	runtimeClient, err = server.Dial(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeClient.Close()
	for _, participant := range []*server.Client{runtimeClient, builderClient} {
		response, err = participant.Request(server.WithEffectFence(t.Context(), server.EffectFence{Operation: "org/build/api", Generation: 2}), "POST", "/networks/create", strings.NewReader(`{"Name":"stale-build"}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 409 {
			t.Fatal("recovery did not fence every participant", response.StatusCode)
		}
	}
	t.Log("Production ImageBuilder on separate Docker daemon survived lost PG session, rejected stale build, and released only compute after builder/runtime drain")
}

const poolBuilderProxy = `import socketserver,http.server,http.client,socket,os,time
class UnixHTTP(http.client.HTTPConnection):
 def connect(self):
  self.sock=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM);self.sock.connect('/tmp/builder.sock')
class Handler(http.server.BaseHTTPRequestHandler):
 def do_GET(self): self.forward()
 def do_POST(self): self.forward()
 def log_message(self,*args): pass
 def forward(self):
  body=self.rfile.read(int(self.headers.get('Content-Length','0')))
  if self.path.startswith('/build?'):
   open('/tmp/build-entered','w').close()
   while not os.path.exists('/tmp/build-release'): time.sleep(.01)
  c=UnixHTTP('docker');c.request(self.command,self.path,body=body,headers=dict(self.headers));r=c.getresponse();data=r.read()
  self.send_response(r.status);self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data);c.close()
class Server(socketserver.ThreadingMixIn,socketserver.UnixStreamServer): pass
s=Server('/tmp/builder-gate.sock',Handler);os.chmod('/tmp/builder-gate.sock',0o666);s.serve_forever()
`
