package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// nodeStub is a NodeStreams that records each stream it opens and serves it
// with serve, as a node does once it has accepted the stream. Open has
// already read the accept verdict, as the hub's does, so serve starts with
// the stream's payload.
type nodeStub struct {
	t     *testing.T
	err   error
	serve func(hdr nodeproto.StreamHeader, conn net.Conn)

	mu      sync.Mutex
	targets []string
	headers []nodeproto.StreamHeader
}

func (s *nodeStub) Open(_ context.Context, orgID, nodeID string, hdr nodeproto.StreamHeader) (net.Conn, error) {
	s.mu.Lock()
	s.targets = append(s.targets, orgID+"/"+nodeID)
	s.headers = append(s.headers, hdr)
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	caller, node := net.Pipe()
	go func() {
		defer node.Close()
		if s.serve == nil {
			s.t.Errorf("unexpected node stream %+v", hdr)
			return
		}
		s.serve(hdr, node)
	}()
	return &noHalfClose{Conn: caller, t: s.t}, nil
}

func (s *nodeStub) opened() ([]string, []nodeproto.StreamHeader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.targets), slices.Clone(s.headers)
}

// noHalfClose fails the test on a half-close. The node reads anything after
// a fenced request, EOF included, as the caller going away.
type noHalfClose struct {
	net.Conn
	t *testing.T
}

func (c *noHalfClose) CloseWrite() error {
	c.t.Error("the caller half-closed a node stream")
	return nil
}

var nodeTestTarget = NodeTarget{OrgID: "org_1", NodeID: "node_abc", IdentityGeneration: 2}

// nodeClient is a Client reaching nodeTestTarget through streams.
func nodeClient(t *testing.T, streams NodeStreams, dockerID string) *Client {
	t.Helper()
	cfg, err := ParseConfig(NodeCredential(nodeTestTarget.OrgID, nodeTestTarget.NodeID), map[string]string{"identity_generation": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if dockerID != "" {
		cfg.Details["docker_id"] = dockerID
	}
	c, err := Connect(t.Context(), cfg, streams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// proxyTo splices conn to the TCP address, standing in for the node's
// docker socket target.
func proxyTo(t *testing.T, address string, conn net.Conn) {
	daemon, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address)
	if err != nil {
		t.Errorf("reach the fake daemon: %v", err)
		return
	}
	go func() {
		_, _ = io.Copy(daemon, conn)
		_ = daemon.Close()
	}()
	_, _ = io.Copy(conn, daemon)
	_ = daemon.Close()
}

// fencedCall is one fenced request as the node read it.
type fencedCall struct {
	req  *http.Request
	body []byte
	// gone is set when the caller sends anything after its request or
	// closes the stream, which a node takes as the caller going away.
	gone *atomic.Bool
}

// readFenced reads one fenced request the way the node does and starts
// watching for the caller going away.
func readFenced(t *testing.T, conn net.Conn) (fencedCall, bool) {
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		t.Errorf("read the fenced request: %v", err)
		return fencedCall{}, false
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Errorf("read the fenced body: %v", err)
		return fencedCall{}, false
	}
	if br.Buffered() > 0 {
		t.Error("a fenced stream carried more than one request")
	}
	gone := new(atomic.Bool)
	go func() {
		var b [1]byte
		_, _ = br.Read(b[:])
		gone.Store(true)
	}()
	return fencedCall{req: req, body: body, gone: gone}, true
}

// finishChunked writes the last chunk unless the caller already went away,
// as the node does, and reports whether the node's journal stays dirty.
func finishChunked(call fencedCall, conn net.Conn) (dirty bool) {
	if call.gone.Load() {
		return true
	}
	_, err := conn.Write([]byte("0\r\n\r\n"))
	return err != nil
}

func TestNodeConnectReachesDockerThroughStreams(t *testing.T) {
	daemon := fakeDaemon(t, identityDaemon)
	stub := &nodeStub{t: t, serve: func(hdr nodeproto.StreamHeader, conn net.Conn) {
		if hdr.Endpoint != nodeproto.EndpointDocker || hdr.Target != nodeproto.DockerTargetSocket {
			t.Errorf("Docker went over %+v", hdr)
			return
		}
		proxyTo(t, daemon, conn)
	}}
	c := nodeClient(t, stub, "")
	if _, ok := c.access.(*nodeAccess); !ok {
		t.Fatalf("a node config reached the host through %T", c.access)
	}
	details, missing, err := c.Check(t.Context())
	if err != nil || len(missing) != 0 || details["docker_id"] != "fake-daemon" {
		t.Fatalf("check: %v %v %v", details, missing, err)
	}
	targets, headers := stub.opened()
	if len(headers) == 0 {
		t.Fatal("no stream opened")
	}
	for i, hdr := range headers {
		if targets[i] != "org_1/node_abc" || hdr.Fence != nil || hdr.Arg != "" {
			t.Fatalf("stream %d: %s %+v", i, targets[i], hdr)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNodeDialSendsTarget(t *testing.T) {
	stub := &nodeStub{t: t, serve: func(_ nodeproto.StreamHeader, conn net.Conn) {
		_, _ = io.Copy(conn, conn)
	}}
	access := newNodeAccess(stub, nodeTestTarget, "/var/run/docker.sock")
	conn, err := access.DialContext(t.Context(), "tcp", "127.0.0.1:5432")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(conn, echo); err != nil || string(echo) != "ping" {
		t.Fatalf("echo %q: %v", echo, err)
	}
	targets, headers := stub.opened()
	want := nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDial, Target: "127.0.0.1:5432", Arg: "tcp"}
	if len(headers) != 1 || targets[0] != "org_1/node_abc" || !reflect.DeepEqual(headers[0], want) {
		t.Fatalf("opened %v %+v", targets, headers)
	}

	for _, network := range []string{"unix", "udp", ""} {
		if _, err := access.DialContext(t.Context(), network, "/var/run/docker.sock"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: %v", network, err)
		}
	}
	if _, headers := stub.opened(); len(headers) != 1 {
		t.Fatalf("a non-TCP dial opened a stream: %+v", headers)
	}
}

func TestNodeFencedHeader(t *testing.T) {
	type outcome struct {
		mode  string
		dirty bool
	}
	finished := make(chan outcome, 4)
	stub := &nodeStub{t: t, serve: func(hdr nodeproto.StreamHeader, conn net.Conn) {
		call, ok := readFenced(t, conn)
		if !ok {
			return
		}
		switch hdr.Fence.Mode {
		case nodeproto.FenceAdvance:
			if call.req.Method != http.MethodPost || call.req.RequestURI != "/" || len(call.body) != 0 {
				t.Errorf("advance request %s %s %q", call.req.Method, call.req.RequestURI, call.body)
			}
			_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			finished <- outcome{hdr.Fence.Mode, false}
		case nodeproto.FenceRequest:
			if call.req.Method != http.MethodPost || call.req.RequestURI != "/containers/create?name=web" || string(call.body) != `{"Image":"nginx"}` {
				t.Errorf("request %s %s %q", call.req.Method, call.req.RequestURI, call.body)
			}
			if call.req.Header.Get("Content-Type") != "application/json" || call.req.Header.Get("X-Registry-Auth") != "token" {
				t.Errorf("headers %v", call.req.Header)
			}
			if _, set := call.req.Header["User-Agent"]; set {
				t.Errorf("the request gained a User-Agent the SSH helper never sends: %v", call.req.Header)
			}
			_, _ = io.WriteString(conn, "HTTP/1.1 201 Created\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n")
			out := `{"Id":"abc"}` + "\n"
			_, _ = io.WriteString(conn, strconv.FormatInt(int64(len(out)), 16)+"\r\n"+out+"\r\n")
			finished <- outcome{hdr.Fence.Mode, finishChunked(call, conn)}
		default:
			t.Errorf("mode %q", hdr.Fence.Mode)
		}
	}}
	c := nodeClient(t, stub, "daemon-1")
	fence := EffectFence{Operation: "org_1/runtime/web", Generation: 7}
	if err := c.ArmEffectFence(t.Context(), fence); err != nil {
		t.Fatal(err)
	}
	if got := <-finished; got.dirty {
		t.Fatalf("advance left the journal dirty")
	}

	// The caller decodes the JSON and closes the body before reading the
	// last chunk. The stream must stay open until it has, or the node
	// leaves its journal dirty.
	ctx := WithEffectFence(t.Context(), fence)
	response, err := c.RequestHeaders(ctx, http.MethodPost, "/containers/create?name=web", strings.NewReader(`{"Image":"nginx"}`), http.Header{"X-Registry-Auth": []string{"token"}})
	if err != nil {
		t.Fatal(err)
	}
	var created struct{ ID string }
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil || created.ID != "abc" || response.StatusCode != http.StatusCreated {
		t.Fatalf("%d %+v %v", response.StatusCode, created, err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if got := <-finished; got.dirty {
		t.Fatal("closing the body before the last chunk left the node's journal dirty")
	}

	targets, headers := stub.opened()
	for i, mode := range []string{nodeproto.FenceAdvance, nodeproto.FenceRequest} {
		want := nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDocker, Target: nodeproto.DockerTargetFenced,
			Fence: &nodeproto.Fence{Resource: "org_1/runtime/web", Generation: 7, Mode: mode, Daemon: "daemon-1"}}
		if targets[i] != "org_1/node_abc" || !reflect.DeepEqual(headers[i], want) {
			t.Fatalf("stream %d: %s %+v %+v", i, targets[i], headers[i], headers[i].Fence)
		}
	}

	// The node serves the socket it found. A fenced request naming another
	// can't be honoured, so it never leaves.
	access := newNodeAccess(stub, nodeTestTarget, "/var/run/docker.sock")
	if response, err := access.FencedRequest(t.Context(), "request", fence, "daemon-1", "/run/other.sock", http.MethodPost, "/", nil, nil); err == nil {
		_ = response.Body.Close()
		t.Fatal("a fenced request for another socket was sent")
	}
	if response, err := access.FencedRequest(t.Context(), "request", EffectFence{Operation: "x"}, "daemon-1", "/var/run/docker.sock", http.MethodPost, "/", nil, nil); err == nil {
		_ = response.Body.Close()
		t.Fatal("a fence without a generation was sent")
	}
	if _, headers := stub.opened(); len(headers) != 2 {
		t.Fatalf("refused requests opened streams: %d", len(headers))
	}
}

func TestNodeFencedRefusalIsQuiescenceFailure(t *testing.T) {
	stub := &nodeStub{t: t, serve: func(_ nodeproto.StreamHeader, conn net.Conn) {
		if _, ok := readFenced(t, conn); ok {
			_, _ = io.WriteString(conn, "HTTP/1.1 409 Conflict\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		}
	}}
	c := nodeClient(t, stub, "daemon-1")
	fence := EffectFence{Operation: "org_1/runtime/web", Generation: 3}
	if err := c.ArmEffectFence(t.Context(), fence); err == nil || err.Error() != "runtime effect fence could not establish quiescence" {
		t.Fatalf("advance refused: %v", err)
	}
	response, err := c.RequestHeaders(WithEffectFence(t.Context(), fence), http.MethodDelete, "/containers/abc", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status %d", response.StatusCode)
	}
}

func TestNodeFencedUnknownOutcome(t *testing.T) {
	const unknown = "runtime effect fence outcome is unknown"
	fence := EffectFence{Operation: "org_1/runtime/web", Generation: 3}
	for _, tc := range []struct {
		name  string
		reply string
		// atBody is whether the outcome turns unknown while the body is
		// read, rather than before the response returns.
		atBody bool
	}{
		{"no response", "", false},
		{"cut head", "HTTP/1.1 200 OK\r\nContent-Ty", false},
		{"no framing", "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\nhello", false},
		{"no last chunk", "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n", true},
		{"cut trailer", "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n", true},
		{"short body", "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\nhello", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &nodeStub{t: t, serve: func(_ nodeproto.StreamHeader, conn net.Conn) {
				if _, ok := readFenced(t, conn); ok {
					_, _ = io.WriteString(conn, tc.reply)
				}
			}}
			c := nodeClient(t, stub, "daemon-1")
			response, err := c.RequestHeaders(WithEffectFence(t.Context(), fence), http.MethodPost, "/containers/abc/start", nil, nil)
			if !tc.atBody {
				if err == nil || err.Error() != unknown {
					t.Fatalf("got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = io.ReadAll(response.Body)
			if err == nil || err.Error() != unknown {
				t.Fatalf("body: %v", err)
			}
			if err := response.Body.Close(); err == nil || err.Error() != unknown {
				t.Fatalf("close: %v", err)
			}
		})
	}

	t.Run("cancelled", func(t *testing.T) {
		read := make(chan struct{})
		stub := &nodeStub{t: t, serve: func(_ nodeproto.StreamHeader, conn net.Conn) {
			if _, ok := readFenced(t, conn); ok {
				close(read)
				_, _ = io.Copy(io.Discard, conn)
			}
		}}
		c := nodeClient(t, stub, "daemon-1")
		ctx, cancel := context.WithCancel(WithEffectFence(t.Context(), fence))
		go func() {
			<-read
			cancel()
		}()
		done := make(chan error, 1)
		go func() {
			response, err := c.RequestHeaders(ctx, http.MethodPost, "/containers/abc/start", nil, nil)
			if err == nil {
				_ = response.Body.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || err.Error() != unknown {
				t.Fatalf("got %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cancelling didn't end the fenced request")
		}
	})
}

// sshGPUFixture is TestParseGPUProbe's ready-through-runtime SSH output.
const sshGPUFixture = `{"smi_exit":0,"smi":"0, GPU-aaa, NVIDIA L4, 23034\n1, GPU-bbb, NVIDIA L4, 23034\n","nvidia_pci":true,"toolkit":false}`

func TestNodeGPUProbeParsesLikeSSH(t *testing.T) {
	daemon := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"ID":"fake-daemon","Runtimes":{"nvidia":{},"runc":{}}}`)
	})
	stub := &nodeStub{t: t, serve: func(hdr nodeproto.StreamHeader, conn net.Conn) {
		switch {
		case hdr.Endpoint == nodeproto.EndpointDocker:
			proxyTo(t, daemon, conn)
		case hdr.Endpoint == nodeproto.EndpointProbe && hdr.Target == nodeproto.ProbeGPU:
			var probe nodeproto.GPUProbe
			if err := json.Unmarshal([]byte(sshGPUFixture), &probe); err != nil {
				t.Error(err)
				return
			}
			_ = json.NewEncoder(conn).Encode(probe)
		default:
			t.Errorf("unexpected stream %+v", hdr)
		}
	}}
	c := nodeClient(t, stub, "fake-daemon")
	got, err := c.GPUs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want, err := parseGPUProbe([]byte(sshGPUFixture), map[string]json.RawMessage{"nvidia": json.RawMessage(`{}`), "runc": json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	got.CheckedAt = want.CheckedAt
	if !reflect.DeepEqual(got, want) || len(got.Devices) != 2 {
		t.Fatalf("node %+v, SSH %+v", got, want)
	}
}

func TestNodeProbesReadTheirTargets(t *testing.T) {
	replies := map[string]string{
		nodeproto.ProbeCgroup:     `{"cpu":1500,"memory":2147483648}`,
		nodeproto.ProbeFilesystem: `{"total_bytes":100,"free_bytes":40}`,
		nodeproto.ProbeListeners:  `{"ports":[443,80,443]}`,
	}
	var mu sync.Mutex
	stub := &nodeStub{t: t, serve: func(hdr nodeproto.StreamHeader, conn net.Conn) {
		mu.Lock()
		reply, ok := replies[hdr.Target]
		mu.Unlock()
		if hdr.Endpoint != nodeproto.EndpointProbe || !ok {
			t.Errorf("unexpected stream %+v", hdr)
			return
		}
		_, _ = io.WriteString(conn, reply+"\n")
	}}
	access := newNodeAccess(stub, nodeTestTarget, "/var/run/docker.sock")
	cpu, memory, err := access.CgroupLimits(t.Context())
	if err != nil || cpu != 1500 || memory != 2147483648 {
		t.Fatalf("cgroup %d %d %v", cpu, memory, err)
	}
	fs, err := access.Filesystem(t.Context(), "/var/lib/docker")
	if err != nil || fs != (FilesystemCapacity{TotalBytes: 100, FreeBytes: 40}) {
		t.Fatalf("filesystem %+v %v", fs, err)
	}
	ports, err := access.IngressListeners(t.Context())
	if err != nil || !slices.Equal(ports, []int{80, 443}) {
		t.Fatalf("listeners %v %v", ports, err)
	}
	targets, headers := stub.opened()
	want := []nodeproto.StreamHeader{
		{Endpoint: nodeproto.EndpointProbe, Target: nodeproto.ProbeCgroup},
		{Endpoint: nodeproto.EndpointProbe, Target: nodeproto.ProbeFilesystem, Arg: "/var/lib/docker"},
		{Endpoint: nodeproto.EndpointProbe, Target: nodeproto.ProbeListeners},
	}
	if !reflect.DeepEqual(headers, want) || !slices.Equal(targets, []string{"org_1/node_abc", "org_1/node_abc", "org_1/node_abc"}) {
		t.Fatalf("opened %v %+v", targets, headers)
	}

	// A root the SSH probe would refuse never leaves.
	if _, err := access.Filesystem(t.Context(), "var/lib/docker"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative root: %v", err)
	}
	if _, headers := stub.opened(); len(headers) != 3 {
		t.Fatal("a refused root opened a stream")
	}

	// Replies the SSH parsers would refuse are refused here too.
	for target, reply := range map[string]string{
		nodeproto.ProbeFilesystem: `{"total_bytes":100,"free_bytes":400}`,
		nodeproto.ProbeListeners:  `{"ports":[0]}`,
		nodeproto.ProbeCgroup:     `not json`,
	} {
		mu.Lock()
		replies[target] = reply
		mu.Unlock()
	}
	if _, err := access.Filesystem(t.Context(), "/var/lib/docker"); err == nil {
		t.Fatal("free over total passed")
	}
	if _, err := access.IngressListeners(t.Context()); err == nil || err.Error() != "cannot verify ingress listener availability" {
		t.Fatalf("port 0: %v", err)
	}
	if _, _, err := access.CgroupLimits(t.Context()); err == nil || err.Error() != "effective Docker cgroup limits are unavailable" {
		t.Fatalf("garbage cgroup: %v", err)
	}
	if err := access.Close(); err != nil {
		t.Fatal(err)
	}
}

// fakeOfflineError stands in for nodes.ErrNodeOffline, a value struct.
type fakeOfflineError struct{ Name string }

func (e fakeOfflineError) Error() string { return "node " + e.Name + " is offline" }

// errFakeRefused and fakeRefusedError stand in for nodes.ErrEndpointRefused and
// the hub's refusal that wraps it.
var errFakeRefused = errors.New("the node refused the stream")

type fakeRefusedError struct{ reason string }

func (e *fakeRefusedError) Error() string { return errFakeRefused.Error() + ": " + e.reason }
func (e *fakeRefusedError) Unwrap() error { return errFakeRefused }

func TestNodeOfflinePassesThrough(t *testing.T) {
	for _, openErr := range []error{fakeOfflineError{Name: "box"}, &fakeRefusedError{reason: "endpoint docker is not allowed on this node"}} {
		var off fakeOfflineError
		offline := errors.As(openErr, &off)
		stub := &nodeStub{t: t, err: openErr}
		access := newNodeAccess(stub, nodeTestTarget, "/var/run/docker.sock")
		fence := EffectFence{Operation: "org_1/runtime/web", Generation: 1}
		errs := map[string]error{}
		_, errs["dial"] = access.DialContext(t.Context(), "tcp", "127.0.0.1:80")
		_, errs["docker"] = access.DialDocker(t.Context())
		var response *http.Response
		response, errs["fenced"] = access.FencedRequest(t.Context(), "advance", fence, "daemon-1", "/var/run/docker.sock", http.MethodPost, "/", nil, nil)
		if response != nil {
			_ = response.Body.Close()
		}
		_, _, errs["cgroup"] = access.CgroupLimits(t.Context())
		_, errs["gpu"] = access.GPUProbe(t.Context())
		_, errs["filesystem"] = access.Filesystem(t.Context(), "/var/lib/docker")
		_, errs["listeners"] = access.IngressListeners(t.Context())

		c := nodeClient(t, stub, "daemon-1")
		errs["arm"] = c.ArmEffectFence(t.Context(), fence)
		_, _, errs["capacity"] = c.EffectiveCapacity(t.Context(), 4, 1<<30)
		_, errs["client dial"] = c.DialContext(t.Context(), "tcp", "127.0.0.1:80")
		for name, err := range errs {
			// Identity, not errors.Is: the Open error must come back unwrapped.
			if err != openErr { //nolint:errorlint // see above
				t.Fatalf("%s: %v (%T) is not the Open error", name, err, err)
			}
			if offline && (!errors.As(err, &off) || off.Name != "box") {
				t.Fatalf("%s: errors.As lost the offline node: %v", name, err)
			}
			if !offline && !errors.Is(err, errFakeRefused) {
				t.Fatalf("%s: errors.Is lost the refusal: %v", name, err)
			}
		}
	}
}

func TestNodeFencingUnavailableNamesNoSSH(t *testing.T) {
	c := nodeClient(t, &nodeStub{t: t}, "")
	err := c.ArmEffectFence(t.Context(), EffectFence{Operation: "org_1/runtime/web", Generation: 1})
	if err == nil || err.Error() != "verified runtime fencing is unavailable" {
		t.Fatalf("node: %v", err)
	}
	// The SSH path keeps its words.
	ssh := newClient(&Config{Details: map[string]string{}}, &fakeAccess{t: t})
	err = ssh.ArmEffectFence(t.Context(), EffectFence{Operation: "org_1/runtime/web", Generation: 1})
	if err == nil || err.Error() != "verified SSH runtime fencing is unavailable" {
		t.Fatalf("SSH: %v", err)
	}
}
