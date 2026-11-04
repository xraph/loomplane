package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	drt "github.com/xraph/dispatch/durable/runtime"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

const testNodeID = "node_11111111-2222-4333-8444-555555555555"

// fakeNodeStreams stands in for the nodes hub. Each docker stream it opens
// reaches daemon, the address of a fake Docker daemon, unless err is set,
// which Open returns instead, as the hub does for an offline node or a
// refused endpoint.
type fakeNodeStreams struct {
	t      *testing.T
	daemon string
	err    error
	// failing, when set, is how many Opens fail with err before the node
	// comes back. Zero with err set fails every Open.
	failing int

	mu     sync.Mutex
	opened []nodeproto.StreamHeader
}

func (f *fakeNodeStreams) Open(ctx context.Context, orgID, nodeID string, hdr nodeproto.StreamHeader) (net.Conn, error) {
	f.mu.Lock()
	f.opened = append(f.opened, hdr)
	f.mu.Unlock()
	if orgID != "org_fixture" || nodeID != testNodeID {
		f.t.Errorf("a stream went to node %s of %s", nodeID, orgID)
		return nil, errors.New("another node")
	}
	f.mu.Lock()
	fail := f.err != nil && (f.failing == 0 || len(f.opened) <= f.failing)
	f.mu.Unlock()
	if fail {
		return nil, f.err
	}
	if hdr.Endpoint != nodeproto.EndpointDocker || hdr.Target != nodeproto.DockerTargetSocket {
		f.t.Errorf("unexpected node stream %+v", hdr)
		return nil, errors.New("unexpected stream")
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", f.daemon)
}

func (f *fakeNodeStreams) streams() []nodeproto.StreamHeader {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]nodeproto.StreamHeader(nil), f.opened...)
}

// nodeConnection is a ready server connection that reaches testNodeID's
// Docker, as adoption makes one, with its synthesized credential.
func nodeConnection(connectionID, name string, extra map[string]string) (*connections.Connection, []byte) {
	details := map[string]string{"node_id": testNodeID, "identity_generation": "1", "docker_socket": "/var/run/docker.sock", "docker_id": "node-daemon", "architecture": "amd64"}
	maps.Copy(details, extra)
	return &connections.Connection{ID: connectionID, OrgID: "org_fixture", Cloud: connections.Server, Method: connections.MethodNode, Name: name, Status: connections.StatusReady, Details: details},
		server.NodeCredential("org_fixture", testNodeID)
}

func credentialsFor(conn *connections.Connection, raw []byte) buildCredentialFunc {
	return func(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
		if org != conn.OrgID || id != conn.ID {
			return nil, nil, connections.ErrNotFound
		}
		return conn, raw, nil
	}
}

// nodeLabels are an instance's labels on conn, with its reviewed settings.
func nodeLabels(t *testing.T, conn *connections.Connection) map[string]string {
	t.Helper()
	reviewed, err := json.Marshal(planning.ReviewedConnectionSettings(conn.Cloud, conn.Details))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{owned.LabelOrg: conn.OrgID, owned.LabelEnvironment: "env_fixture", owned.LabelProject: "proj_fixture", owned.LabelConnection: conn.ID,
		owned.LabelSettings: string(reviewed), owned.LabelRuntime: "vault_reference", owned.LabelPublic: "false"}
}

// nodeSessionError is what a server session on a node connection returns
// when the hub's Open fails with err.
func nodeSessionError(t *testing.T, err error) error {
	t.Helper()
	conn, raw := nodeConnection("conn_node", "web-1", nil)
	target := &OwnedTarget{Cloud: connections.Server, Credentials: credentialsFor(conn, raw), NodeStreams: &fakeNodeStreams{t: t, err: err}}
	client, sessionErr := target.serverSession(conn.OrgID, conn.ID)(t.Context(), nodeLabels(t, conn))
	if sessionErr == nil {
		_ = client.Close()
		t.Fatal("a session reached a node whose streams fail")
	}
	return sessionErr
}

func TestServerSessionNodeMarker(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	var mu sync.Mutex
	created, started := false, false
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/version":
			_, _ = io.WriteString(w, `{"Version":"29.8.2","APIVersion":"1.54","Os":"linux","Arch":"amd64"}`)
		case r.URL.Path == "/info":
			_, _ = io.WriteString(w, `{"ID":"node-daemon","OSType":"linux","Architecture":"x86_64"}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
			if !created {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "container_one", "Image": "sha256:" + strings.Repeat("a", 64), "Config": map[string]any{"Labels": map[string]string{owned.LabelInstance: iid.String(), owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture"}}, "State": map[string]any{"Running": started, "Status": "running"}})
		case r.URL.Path == "/networks/"+server.NetworkName("org_fixture", "env_fixture"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "network_one", "Labels": map[string]string{owned.LabelOrg: "org_fixture", owned.LabelEnvironment: "env_fixture"}})
		case r.Method == http.MethodPost && r.URL.Path == "/containers/create":
			created = true
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"container_one"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			started = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(daemon.Close)
	conn, raw := nodeConnection("conn_node", "web-1", nil)
	streams := &fakeNodeStreams{t: t, daemon: daemon.Listener.Addr().String()}
	target := &OwnedTarget{Cloud: connections.Server, Credentials: credentialsFor(conn, raw), NodeStreams: streams}
	runtime := &server.Runtime{Session: target.serverSession(conn.OrgID, conn.ID), Values: func(context.Context, map[string]string) (owned.RuntimeValues, error) {
		return owned.RuntimeValues{Env: map[string]string{}}, nil
	}}
	result, err := runtime.Apply(t.Context(), iid, cpprovider.ServiceSpec{Name: "main", Image: "sha256:" + strings.Repeat("a", 64), Ports: []cpprovider.PortSpec{{Container: 8080}}}, nodeLabels(t, conn))
	if err != nil {
		t.Fatal(err)
	}
	if !created || !started || len(result.Endpoints) != 1 {
		t.Fatalf("the node's Docker didn't run the container: created %v, started %v, %+v", created, started, result)
	}
	if len(streams.streams()) == 0 {
		t.Fatal("the session didn't reach Docker through the node's streams")
	}
}

func TestBuildThroughNode(t *testing.T) {
	rig := newImageBuildRig(t, false)
	save := func() error { return nil }
	// The same source built on an SSH builder gives the context's digest.
	if _, err := rig.builder.Build(t.Context(), rig.job, &ImageArtifact{}, save); err != nil {
		t.Fatal(err)
	}
	conn, raw := nodeConnection("conn_builder", "build-1", map[string]string{"build_workers": "dedicated", "docker_id": "build-fixture"})
	rig.job.Action.Service.BuildTarget.BuilderSettings = planning.ReviewedBuildSettings(conn.Cloud, conn.Details)
	rig.job.BuildCredentials = credentialsFor(conn, raw)
	streams := &fakeNodeStreams{t: t, daemon: rig.daemon}
	image, err := (&ImageBuilder{NodeStreams: streams}).Build(t.Context(), rig.job, &ImageArtifact{}, save)
	if err != nil || image == "" {
		t.Fatalf("build on the node: %q %v", image, err)
	}
	tars := *rig.tars
	if *rig.builds != 2 || len(tars) != 2 || tars[0] != tars[1] {
		t.Fatalf("the build context didn't arrive intact through the node: %d builds, digests %v", *rig.builds, tars)
	}
	if len(streams.streams()) == 0 {
		t.Fatal("the build didn't go through the node's streams")
	}
}

func TestDedicatedNodeBuilderAllowed(t *testing.T) {
	for _, tc := range []struct {
		workers string
		allowed bool
	}{{"dedicated", true}, {"", false}, {"shared", false}} {
		t.Run("workers="+tc.workers, func(t *testing.T) {
			rig := newImageBuildRig(t, false)
			conn, raw := nodeConnection("conn_builder", "build-1", map[string]string{"build_workers": tc.workers, "docker_id": "build-fixture"})
			rig.job.Action.Service.BuildTarget.BuilderSettings = planning.ReviewedBuildSettings(conn.Cloud, conn.Details)
			rig.job.BuildCredentials = credentialsFor(conn, raw)
			_, err := (&ImageBuilder{NodeStreams: &fakeNodeStreams{t: t, daemon: rig.daemon}}).Build(t.Context(), rig.job, &ImageArtifact{}, func() error { return nil })
			if tc.allowed && err != nil {
				t.Fatalf("a dedicated node builder refused an untrusted source: %v", err)
			}
			if !tc.allowed && (err == nil || !strings.Contains(err.Error(), "untrusted builds") || *rig.builds != 0) {
				t.Fatalf("a %q node builder took an untrusted source: %d builds, %v", tc.workers, *rig.builds, err)
			}
		})
	}
}

// instantReconnect is a reconnect wait that never sleeps, and counts how
// often it would have.
func instantReconnect(window time.Duration, slept *int) nodeReconnect {
	return nodeReconnect{window: window, every: 5 * time.Second, sleep: func(ctx context.Context, _ time.Duration) error {
		*slept++
		return ctx.Err()
	}}
}

// buildExecutor runs an action by building job's image on builder, the
// way OwnedTarget.Apply does before it deploys.
type buildExecutor struct {
	builder *ImageBuilder
	job     *Job
}

func (e *buildExecutor) Apply(ctx context.Context, _ *Job) (*Outcome, error) {
	image, err := e.builder.Build(ctx, e.job, &ImageArtifact{}, func() error { return nil })
	if err != nil {
		return nil, err
	}
	return &Outcome{ExternalID: "ext-api", Image: image}, nil
}

func (*buildExecutor) Remove(context.Context, *Job, *planning.Binding) error { return nil }

func (*buildExecutor) Observe(context.Context, *connections.Connection, []byte, *planning.Binding) (*Observation, error) {
	return &Observation{Exists: true}, nil
}

// nodeBuild is a build of rig's job on node build-1, through streams.
func nodeBuild(t *testing.T, streams *fakeNodeStreams, reconnect nodeReconnect) (*buildExecutor, *imageBuildRig) {
	t.Helper()
	rig := newImageBuildRig(t, false)
	conn, raw := nodeConnection("conn_builder", "build-1", map[string]string{"build_workers": "dedicated", "docker_id": "build-fixture"})
	rig.job.Action.Service.BuildTarget.BuilderSettings = planning.ReviewedBuildSettings(conn.Cloud, conn.Details)
	rig.job.BuildCredentials = credentialsFor(conn, raw)
	streams.daemon = rig.daemon
	return &buildExecutor{builder: &ImageBuilder{NodeStreams: streams, reconnect: reconnect}, job: rig.job}, rig
}

// A build's log collector redacts its error, which once dropped the node's
// refusal: the action recorded the raw stream error and was retried.
func TestBuildOnRefusingNodeIsFinal(t *testing.T) {
	slept := 0
	exec, rig := nodeBuild(t, &fakeNodeStreams{t: t, err: nodes.Refusal("endpoint docker is not allowed on this node", true)}, instantReconnect(time.Minute, &slept))
	step, runErr := runOnce(t, exec)
	want := "node build-1 doesn't allow docker; allow it on the machine with: loomctl node allow docker"
	if step.Status != StepFailed || step.Message != want {
		t.Fatalf("the action recorded %q (%s)", step.Message, step.Status)
	}
	var final *drt.ApplicationError
	if !errors.As(runErr, &final) || !final.NonRetryable || final.Message != want || !errors.Is(runErr, nodes.ErrEndpointRefused) {
		t.Fatalf("a refused build endpoint isn't final: %+v (%v)", final, runErr)
	}
	if slept != 0 || *rig.builds != 0 {
		t.Fatalf("a refusal waited %d times, or built %d times", slept, *rig.builds)
	}
}

func TestBuildOnOfflineNodeWaitsThenFails(t *testing.T) {
	since := time.Date(2026, 11, 3, 14, 2, 0, 0, time.UTC)
	streams := &fakeNodeStreams{t: t, err: nodes.ErrNodeOffline{NodeID: testNodeID, Name: "hetzner-1", Since: since}}
	slept := 0
	exec, rig := nodeBuild(t, streams, instantReconnect(15*time.Second, &slept))
	step, runErr := runOnce(t, exec)
	want := "node hetzner-1 is offline since 14:02 UTC; its apps keep running. Run the deploy again once it reconnects"
	if step.Status != StepFailed || step.Message != want || runErr == nil || runErr.Error() != want {
		t.Fatalf("the action recorded %q (%s), run returned %v", step.Message, step.Status, runErr)
	}
	var final *drt.ApplicationError
	if errors.As(runErr, &final) && final.NonRetryable {
		t.Fatalf("an offline build node's failure was final: %+v", final)
	}
	// It waited out the window, 15 s at 5 s a try, then stopped trying.
	if slept != 3 || len(streams.streams()) != 4 || *rig.builds != 0 {
		t.Fatalf("waited %d times over %d opens, built %d times", slept, len(streams.streams()), *rig.builds)
	}
}

func TestBuildWaitsForOfflineNodeToReconnect(t *testing.T) {
	streams := &fakeNodeStreams{t: t, err: nodes.ErrNodeOffline{NodeID: testNodeID, Name: "hetzner-1", Since: time.Now()}, failing: 2}
	slept := 0
	exec, rig := nodeBuild(t, streams, instantReconnect(nodeReconnectWindow, &slept))
	image, err := exec.builder.Build(security.WithDeploymentRun(t.Context()), exec.job, &ImageArtifact{}, func() error { return nil })
	if err != nil || image == "" || *rig.builds != 1 {
		t.Fatalf("the build didn't carry on once the node was back: %q %d %v", image, *rig.builds, err)
	}
	if slept != 2 {
		t.Fatalf("waited %d times for a node that was back on the third try", slept)
	}
}

// Outside a deploy, such as an API read, an offline node answers at once.
func TestOfflineNodeWaitsOnlyInADeploy(t *testing.T) {
	slept := 0
	exec, _ := nodeBuild(t, &fakeNodeStreams{t: t, err: nodes.ErrNodeOffline{NodeID: testNodeID, Name: "hetzner-1", Since: time.Now()}}, instantReconnect(time.Minute, &slept))
	var off nodes.ErrNodeOffline
	if _, err := exec.builder.Build(t.Context(), exec.job, &ImageArtifact{}, func() error { return nil }); !errors.As(err, &off) || slept != 0 {
		t.Fatalf("waited %d times, got %v", slept, err)
	}
}

// scriptedStreams answers each Open with the next of errs, then opens.
type scriptedStreams struct {
	errs  []error
	opens int
}

func (s *scriptedStreams) Open(context.Context, string, string, nodeproto.StreamHeader) (net.Conn, error) {
	s.opens++
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	a, b := net.Pipe()
	_ = b.Close()
	return a, nil
}

// A refusal other than not-allowed, such as a probe that failed, explains
// a failed check but isn't latched: an offline node met afterwards still
// gets the wait, and a stream that opens forgets it. Not-allowed holds.
func TestNodeSessionLatchesOnlyNotAllowed(t *testing.T) {
	ctx := security.WithDeploymentRun(t.Context())
	probe := nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe}
	offline := nodes.ErrNodeOffline{NodeID: testNodeID, Name: "web-1", Since: time.Now()}
	slept := 0
	streams := &scriptedStreams{errs: []error{nodes.Refusal("probe filesystem: no such file", false), offline, nil}}
	s := &nodeSession{streams: streams, name: "web-1", reconnect: instantReconnect(time.Minute, &slept).orDefault()}
	_, err := s.Open(ctx, "org_1", testNodeID, probe)
	var refused *endpointRefusedError
	if !errors.As(err, &refused) || refused.notAllowed || !errors.Is(s.unreachable(), nodes.ErrEndpointRefused) {
		t.Fatalf("probe refusal: %v, unreachable %v", err, s.unreachable())
	}
	conn, err := s.Open(ctx, "org_1", testNodeID, probe)
	if err != nil || slept != 1 {
		t.Fatalf("after a probe refusal an offline node got no wait: %v, slept %d", err, slept)
	}
	_ = conn.Close()
	if s.unreachable() != nil {
		t.Fatalf("an opened stream left %v", s.unreachable())
	}

	slept = 0
	streams = &scriptedStreams{errs: []error{nodes.Refusal("endpoint docker is not allowed on this node", true), offline}}
	s = &nodeSession{streams: streams, name: "web-1", reconnect: instantReconnect(time.Minute, &slept).orDefault()}
	if _, err = s.Open(ctx, "org_1", testNodeID, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDocker}); !errors.As(err, &refused) || !refused.notAllowed {
		t.Fatalf("not allowed: %v", err)
	}
	if _, err = s.Open(ctx, "org_1", testNodeID, probe); slept != 0 || !errors.As(s.unreachable(), &refused) || !refused.notAllowed {
		t.Fatalf("not allowed wasn't kept: %v, slept %d, unreachable %v", err, slept, s.unreachable())
	}
}
