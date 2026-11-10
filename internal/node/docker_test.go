//go:build linux || darwin

package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/xraph/loomplane/internal/clouds/server/fencerules"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// fakeDaemon is a Docker daemon on a Unix socket that answers the calls the
// docker endpoint makes and records every mutation that reaches it.
type fakeDaemon struct {
	id     string
	socket string

	mu         sync.Mutex
	mutations  []string
	bodies     []string
	execs      map[string]map[string]any
	containers map[string]map[string]any
	// hold, when set, makes POST /containers/create signal reached and then
	// wait for hold to close before it answers.
	hold    chan struct{}
	reached chan struct{}
	// output is what an exec start writes, already framed.
	output []byte
	// afterStart is what an exec's inspect says once it has started. Nil
	// means it finished with exit code 0.
	afterStart map[string]any
}

func startDaemon(t *testing.T, id string) *fakeDaemon {
	t.Helper()
	// macOS caps a Unix socket path at 104 bytes, and t.TempDir is long.
	dir, err := os.MkdirTemp("", "lpd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	d := &fakeDaemon{id: id, socket: filepath.Join(dir, "d.sock"),
		execs: map[string]map[string]any{}, containers: map[string]map[string]any{}}
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", d.socket)
	if err != nil {
		t.Fatal(err)
	}
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "OK") })
	mux.HandleFunc("GET /info", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]string{"ID": d.id}) })
	mux.HandleFunc("POST /containers/create", func(w http.ResponseWriter, r *http.Request) {
		d.record(r)
		d.mu.Lock()
		hold, reached := d.hold, d.reached
		d.mu.Unlock()
		if hold != nil {
			reached <- struct{}{}
			<-hold
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"Id":"c1"}`)
	})
	mux.HandleFunc("POST /containers/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		d.record(r)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /exec/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		e, ok := d.execs[r.PathValue("id")]
		var body []byte
		if ok {
			body, _ = json.Marshal(e)
		}
		d.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	})
	mux.HandleFunc("GET /containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		c, ok := d.containers[r.PathValue("id")]
		d.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, c)
	})
	mux.HandleFunc("POST /exec/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		d.record(r)
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		d.mu.Lock()
		output := d.output
		d.mu.Unlock()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/vnd.docker.raw-stream\r\n\r\n")
		_, _ = buf.Write(output)
		_ = buf.Flush()
		d.mu.Lock()
		if e := d.execs[r.PathValue("id")]; e != nil {
			e["Running"], e["ExitCode"] = false, 0
			for k, v := range d.afterStart {
				e[k] = v
			}
		}
		d.mu.Unlock()
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return d
}

func (d *fakeDaemon) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mutations = append(d.mutations, r.Method+" "+r.URL.Path)
	d.bodies = append(d.bodies, string(body))
}

func (d *fakeDaemon) seen() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.mutations)
}

func (d *fakeDaemon) seenBodies() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.bodies)
}

// set changes the daemon's behaviour under its lock.
func (d *fakeDaemon) set(f func(d *fakeDaemon)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f(d)
}

// yamuxPair is the hub and node ends of one node session.
func yamuxPair(t *testing.T) (hub, node *yamux.Session) {
	t.Helper()
	a, b := net.Pipe()
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	hub, err := yamux.Client(a, cfg)
	if err != nil {
		t.Fatal(err)
	}
	node, err = yamux.Server(b, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hub.Close(); _ = node.Close() })
	return hub, node
}

// openStream opens a stream from the hub and returns both of its ends.
func openStream(t *testing.T, hub, node *yamux.Session) (caller, served net.Conn) {
	t.Helper()
	accepted := make(chan net.Conn, 1)
	go func() {
		s, err := node.AcceptStream()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- s
	}()
	c, err := hub.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	s, ok := <-accepted
	if !ok {
		t.Fatal("node never accepted the stream")
	}
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	return c, s
}

// serveStream runs h on the node end and closes it afterwards, as Serve does.
func serveStream(h Handler, hdr nodeproto.StreamHeader, s net.Conn) chan error {
	done := make(chan error, 1)
	go func() {
		done <- h(context.Background(), hdr, s)
		_ = s.Close()
	}()
	return done
}

func wait(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the handler never returned")
		return nil
	}
}

func fence(mode, resource string, generation int64) nodeproto.Fence {
	return nodeproto.Fence{Resource: resource, Generation: generation, Mode: mode, Daemon: "daemon-1"}
}

func newRequest(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, "http://docker"+path, r) //nolint:noctx // written to a stream by hand
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// fencedResult is what one fenced request gave the caller and the handler.
type fencedResult struct {
	code    int
	body    string
	readErr error // reading the body; set when the response never framed completely
	err     error // the handler's
}

// fencedTry sends one fenced request, written with send, and reads the
// whole response.
func fencedTry(t *testing.T, h Handler, f nodeproto.Fence, req *http.Request, send func(io.Writer) error) fencedResult {
	t.Helper()
	hub, node := yamuxPair(t)
	c, s := openStream(t, hub, node)
	done := serveStream(h, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDocker, Target: nodeproto.DockerTargetFenced, Fence: &f}, s)
	if err := send(c); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(resp.Body)
	_ = c.Close()
	return fencedResult{code: resp.StatusCode, body: string(body), readErr: readErr, err: wait(t, done)}
}

// fencedDo sends one fenced request and reads the whole response. A
// response that does not frame completely, or a handler that fails after a
// success, fails the test.
func fencedDo(t *testing.T, h Handler, f nodeproto.Fence, req *http.Request) (int, string) {
	t.Helper()
	r := fencedTry(t, h, f, req, req.Write)
	if r.readErr != nil {
		t.Fatalf("reading a %d response: %v", r.code, r.readErr)
	}
	if r.err != nil && r.code < 300 {
		t.Fatalf("the handler failed after a %d: %v", r.code, r.err)
	}
	return r.code, r.body
}

func openJournal(t *testing.T) (*FenceJournal, string) {
	t.Helper()
	state := t.TempDir()
	j, err := OpenFenceJournal(state, "daemon-1")
	if err != nil {
		t.Fatal(err)
	}
	return j, state
}

func loadJournal(t *testing.T, j *FenceJournal) (fencerules.Journal, error) {
	t.Helper()
	if err := j.Lock(); err != nil {
		t.Fatal(err)
	}
	defer j.Unlock()
	return j.Load()
}

const deployOp = "org_1/deploy/app"

func TestDockerRawSplice(t *testing.T) {
	d := startDaemon(t, "daemon-1")
	hub, node := yamuxPair(t)
	c, s := openStream(t, hub, node)
	done := serveStream(DockerHandler(d.socket, nil), nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDocker}, s)
	req := newRequest(t, http.MethodGet, "/_ping", "")
	if err := req.Write(c); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != "OK" {
		t.Fatalf("%d %q %v", resp.StatusCode, body, err)
	}
	_ = c.Close()
	_ = wait(t, done)
}

func TestFencedAdvanceThenRequest(t *testing.T) {
	d := startDaemon(t, "daemon-1")
	j, _ := openJournal(t)
	h := DockerHandler(d.socket, j)
	if code, body := fencedDo(t, h, fence(nodeproto.FenceAdvance, deployOp, 2), newRequest(t, http.MethodPost, "/", "")); code != http.StatusOK || body != "" {
		t.Fatalf("advance: %d %q", code, body)
	}
	code, body := fencedDo(t, h, fence(nodeproto.FenceRequest, deployOp, 2), newRequest(t, http.MethodPost, "/containers/create?name=web", `{"Image":"nginx"}`))
	if code != http.StatusCreated || body != `{"Id":"c1"}` {
		t.Fatalf("request: %d %q", code, body)
	}
	if got := d.seen(); !slices.Equal(got, []string{"POST /containers/create"}) || d.seenBodies()[0] != `{"Image":"nginx"}` {
		t.Fatalf("daemon saw %v %q", got, d.seenBodies())
	}
	got, err := loadJournal(t, j)
	if err != nil || got.Dirty.Set || got.Epochs[fencerules.JournalKey(deployOp)] != 2 {
		t.Fatalf("journal %+v %v", got, err)
	}
}

func TestFencedRequestOldGenerationIs409(t *testing.T) {
	d := startDaemon(t, "daemon-1")
	j, _ := openJournal(t)
	h := DockerHandler(d.socket, j)
	create := func() *http.Request { return newRequest(t, http.MethodPost, "/containers/create", `{}`) }
	if code, _ := fencedDo(t, h, fence(nodeproto.FenceRequest, deployOp, 1), create()); code != http.StatusConflict {
		t.Fatalf("before any advance: %d", code)
	}
	if code, _ := fencedDo(t, h, fence(nodeproto.FenceAdvance, deployOp, 3), newRequest(t, http.MethodPost, "/", "")); code != http.StatusOK {
		t.Fatalf("advance: %d", code)
	}
	if code, body := fencedDo(t, h, fence(nodeproto.FenceRequest, deployOp, 2), create()); code != http.StatusConflict || body != "" {
		t.Fatalf("old generation: %d %q", code, body)
	}
	if code, _ := fencedDo(t, h, fence(nodeproto.FenceAdvance, deployOp, 2), newRequest(t, http.MethodPost, "/", "")); code != http.StatusConflict {
		t.Fatalf("advance backwards: %d", code)
	}
	if got := d.seen(); len(got) != 0 {
		t.Fatalf("daemon saw %v", got)
	}
}

func TestFencedDaemonMismatchIs409(t *testing.T) {
	d := startDaemon(t, "daemon-2")
	j, state := openJournal(t)
	h := DockerHandler(d.socket, j)
	if code, body := fencedDo(t, h, fence(nodeproto.FenceAdvance, deployOp, 2), newRequest(t, http.MethodPost, "/", "")); code != http.StatusConflict || body != "" {
		t.Fatalf("advance against another daemon: %d %q", code, body)
	}
	if _, err := os.Stat(filepath.Join(journalDir(state), "state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the journal was written: %v", err)
	}
}

func TestFencedStreamDropLeavesDirty(t *testing.T) {
	for name, drop := range map[string]func(caller net.Conn, hub *yamux.Session){
		"stream closed": func(c net.Conn, _ *yamux.Session) { _ = c.Close() },
		"session lost":  func(_ net.Conn, hub *yamux.Session) { _ = hub.Close() },
	} {
		t.Run(name, func(t *testing.T) {
			d := startDaemon(t, "daemon-1")
			j, _ := openJournal(t)
			h := DockerHandler(d.socket, j)
			if code, _ := fencedDo(t, h, fence(nodeproto.FenceAdvance, deployOp, 2), newRequest(t, http.MethodPost, "/", "")); code != http.StatusOK {
				t.Fatalf("advance: %d", code)
			}
			hold, reached := make(chan struct{}), make(chan struct{}, 1)
			d.set(func(d *fakeDaemon) { d.hold, d.reached = hold, reached })
			hub, node := yamuxPair(t)
			c, s := openStream(t, hub, node)
			f := fence(nodeproto.FenceRequest, deployOp, 2)
			done := serveStream(h, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDocker, Target: nodeproto.DockerTargetFenced, Fence: &f}, s)
			if err := newRequest(t, http.MethodPost, "/containers/create", `{}`).Write(c); err != nil {
				t.Fatal(err)
			}
			<-reached
			drop(c, hub)
			close(hold)
			if err := wait(t, done); err == nil {
				t.Fatal("the handler reported success for a dropped stream")
			}
			if !slices.Equal(d.seen(), []string{"POST /containers/create"}) {
				t.Fatalf("daemon saw %v; the request must still complete", d.seen())
			}
			if got, err := loadJournal(t, j); err != nil || got.Dirty != (fencerules.Dirty{Set: true}) {
				t.Fatalf("journal after the drop: %+v %v, want a bare dirty mark", got, err)
			}
			d.set(func(d *fakeDaemon) { d.hold = nil })
			if code, _ := fencedDo(t, h, fence(nodeproto.FenceAdvance, deployOp, 3), newRequest(t, http.MethodPost, "/", "")); code != http.StatusConflict {
				t.Fatalf("advance over a dirty journal: %d", code)
			}
		})
	}
}

// A response HTTP gives no body, a 204 or the answer to a HEAD, is relayed
// as its head alone. A caller reads no body after it, so chunked framing and
// a last chunk would wait unread, and a caller that closes after the head
// would leave the journal dirty. The journal ends clean once the head is
// written.
func TestFencedBodilessResponseIsHeadOnly(t *testing.T) {
	for _, tc := range []struct {
		name         string
		method, path string
		code         int
		// closeEarly closes the stream as soon as the head is read, as Go's
		// client does for a response with no body.
		closeEarly bool
	}{
		{name: "204", method: http.MethodPost, path: "/containers/c1/start", code: http.StatusNoContent},
		{name: "204 caller closes after the head", method: http.MethodPost, path: "/containers/c1/start", code: http.StatusNoContent, closeEarly: true},
		{name: "HEAD", method: http.MethodHead, path: "/_ping", code: http.StatusOK},
		{name: "HEAD caller closes after the head", method: http.MethodHead, path: "/_ping", code: http.StatusOK, closeEarly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := startDaemon(t, "daemon-1")
			j, _ := openJournal(t)
			h := DockerHandler(d.socket, j)
			if code, _ := fencedDo(t, h, fence(nodeproto.FenceAdvance, deployOp, 2), newRequest(t, http.MethodPost, "/", "")); code != http.StatusOK {
				t.Fatalf("advance: %d", code)
			}
			hub, node := yamuxPair(t)
			c, s := openStream(t, hub, node)
			f := fence(nodeproto.FenceRequest, deployOp, 2)
			done := serveStream(h, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDocker, Target: nodeproto.DockerTargetFenced, Fence: &f}, s)
			req := newRequest(t, tc.method, tc.path, "")
			if err := req.Write(c); err != nil {
				t.Fatal(err)
			}
			br := bufio.NewReader(c)
			resp, err := http.ReadResponse(br, req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.code || len(resp.TransferEncoding) != 0 {
				t.Fatalf("%d %v, want %d with no transfer encoding", resp.StatusCode, resp.TransferEncoding, tc.code)
			}
			if tc.closeEarly {
				_ = c.Close()
			} else {
				// The node closes the stream after the head: nothing follows it.
				rest, readErr := io.ReadAll(br)
				if readErr != nil || len(rest) != 0 {
					t.Fatalf("after the head: %q %v", rest, readErr)
				}
				_ = c.Close()
			}
			if err = wait(t, done); err != nil {
				t.Fatalf("handler: %v", err)
			}
			if got := d.seen(); tc.method == http.MethodPost && !slices.Equal(got, []string{"POST " + tc.path}) {
				t.Fatalf("daemon saw %v", got)
			}
			got, err := loadJournal(t, j)
			if err != nil || got.Dirty.Set || got.Epochs[fencerules.JournalKey(deployOp)] != 2 {
				t.Fatalf("journal %+v %v", got, err)
			}
		})
	}
}

func TestIngressExecRejectsForeignContainer(t *testing.T) {
	d := startDaemon(t, "daemon-1")
	j, _ := openJournal(t)
	h := DockerHandler(d.socket, j)
	execID, container := strings.Repeat("a", 64), strings.Repeat("b", 64)
	d.set(func(d *fakeDaemon) {
		d.execs[execID] = map[string]any{
			"ContainerID": container, "Running": false, "ExitCode": nil,
			"ProcessConfig": map[string]any{"entrypoint": "sh", "arguments": []string{"-c", fencerules.IngressProbeScript, "probe", "app.example.com"}, "tty": false, "privileged": false},
		}
		d.output = append([]byte{1, 0, 0, 0, 0, 0, 0, 6}, "hello\n"...)
	})
	owner := func(org string) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.containers[container] = map[string]any{
			"Id": container, "State": map[string]any{"Running": true},
			"Config": map[string]any{"Image": fencerules.LegacyIngressImage, "Labels": map[string]string{"loomplane.org": org, "loomplane.ingress": "srv_1"}},
		}
	}
	op := "org_1/ingress/srv_1"
	start := func() *http.Request {
		req := newRequest(t, http.MethodPost, "/exec/"+execID+"/start", `{"Detach":false,"Tty":false}`)
		req.Header.Set("Content-Type", "application/json")
		return req
	}
	if code, _ := fencedDo(t, h, fence(nodeproto.FenceAdvance, op, 2), newRequest(t, http.MethodPost, "/", "")); code != http.StatusOK {
		t.Fatalf("advance: %d", code)
	}

	owner("org_2")
	if code, body := fencedDo(t, h, fence(nodeproto.FenceIngressExec, op, 2), start()); code != http.StatusConflict || body != "" {
		t.Fatalf("foreign container: %d %q", code, body)
	}
	if got := d.seen(); len(got) != 0 {
		t.Fatalf("daemon saw %v", got)
	}
	if got, err := loadJournal(t, j); err != nil || got.Dirty.Set {
		t.Fatalf("journal %+v %v", got, err)
	}

	// The same exec in the operation's own proxy goes through, frames and all.
	owner("org_1")
	code, body := fencedDo(t, h, fence(nodeproto.FenceIngressExec, op, 2), start())
	if code != http.StatusOK || body != "\x01\x00\x00\x00\x00\x00\x00\x06hello\n" {
		t.Fatalf("owned container: %d %q", code, body)
	}
	if got, err := loadJournal(t, j); err != nil || got.Dirty.Set {
		t.Fatalf("journal after the exec %+v %v", got, err)
	}
}

func TestFencedServeRefusesHeaderWithoutFence(t *testing.T) {
	r := &Registry{Log: discardLog()}
	r.Register(nodeproto.EndpointDocker, DockerHandler("/nonexistent.sock", nil))
	c, done := serveWith(t, r, NewLive(Config{Endpoints: []nodeproto.Endpoint{eDocker}}),
		nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDocker, Target: nodeproto.DockerTargetFenced})
	v, line := readRefusal(t, c)
	if v != 1 || line != "nodeproto: a fenced docker stream needs a fence\n" {
		t.Fatalf("%d %q", v, line)
	}
	<-done
}

func TestRegisterDockerNeedsAllowAndSocket(t *testing.T) {
	d := startDaemon(t, "daemon-1")
	nowhere := []string{filepath.Join(t.TempDir(), "none.sock")}
	for _, tc := range []struct {
		name     string
		cfg      Config
		detected []string
		want     string
	}{
		{"not allowed", Config{Endpoints: []nodeproto.Endpoint{eProbe}, DockerSocket: d.socket}, nil, ""},
		{"configured", Config{Endpoints: []nodeproto.Endpoint{eDocker}, DockerSocket: d.socket}, nowhere, d.socket},
		{"detected", Config{Endpoints: []nodeproto.Endpoint{eDocker}}, []string{d.socket}, d.socket},
		{"no socket", Config{Endpoints: []nodeproto.Endpoint{eDocker}}, nowhere, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &Registry{}
			rt := &Runtime{st: State{Dir: t.TempDir()}, reg: reg, live: NewLive(tc.cfg), Log: discardLog(),
				detectBase: DetectOptions{DockerSockets: tc.detected, PodmanSockets: nowhere}}
			if got := rt.registerDocker(context.Background()); got != tc.want {
				t.Fatalf("socket %q, want %q", got, tc.want)
			}
			if registered := reg.handlers[nodeproto.EndpointDocker] != nil; registered != (tc.want != "") {
				t.Fatalf("registered %v", registered)
			}
		})
	}
}

// eofConn closes ended once a read on it has failed, which is when the
// handler's watcher has seen the caller go.
type eofConn struct {
	net.Conn
	once  sync.Once
	ended chan struct{}
}

func (c *eofConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err != nil {
		c.once.Do(func() { close(c.ended) })
	}
	return n, err
}

func TestFencedCallerGoneWhileQueuedRunsNothing(t *testing.T) {
	d := startDaemon(t, "daemon-1")
	j, _ := openJournal(t)
	h := DockerHandler(d.socket, j)
	if code, _ := fencedDo(t, h, fence(nodeproto.FenceAdvance, deployOp, 2), newRequest(t, http.MethodPost, "/", "")); code != http.StatusOK {
		t.Fatalf("advance: %d", code)
	}
	// Another fenced request holds the daemon, so this one queues.
	if err := j.Lock(); err != nil {
		t.Fatal(err)
	}
	hub, node := yamuxPair(t)
	c, s := openStream(t, hub, node)
	served := &eofConn{Conn: s, ended: make(chan struct{})}
	f := fence(nodeproto.FenceRequest, deployOp, 2)
	done := serveStream(h, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDocker, Target: nodeproto.DockerTargetFenced, Fence: &f}, served)
	if err := newRequest(t, http.MethodPost, "/containers/create", `{}`).Write(c); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	select {
	case <-served.ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never saw the caller go")
	}
	j.Unlock()
	if err := wait(t, done); err == nil {
		t.Fatal("the handler reported success for a caller that went away")
	}
	if got := d.seen(); len(got) != 0 {
		t.Fatalf("daemon saw %v; nobody was waiting for it", got)
	}
	got, err := loadJournal(t, j)
	if err != nil || got.Epochs[fencerules.JournalKey(deployOp)] != 2 {
		t.Fatalf("journal %+v %v, want clean at generation 2", got, err)
	}
	if code, _ := fencedDo(t, h, f, newRequest(t, http.MethodPost, "/containers/create", `{}`)); code != http.StatusCreated {
		t.Fatalf("the next request: %d", code)
	}
}

func TestFencedTrailingBytesAreRefused(t *testing.T) {
	d := startDaemon(t, "daemon-1")
	j, _ := openJournal(t)
	h := DockerHandler(d.socket, j)
	if code, _ := fencedDo(t, h, fence(nodeproto.FenceAdvance, deployOp, 2), newRequest(t, http.MethodPost, "/", "")); code != http.StatusOK {
		t.Fatalf("advance: %d", code)
	}
	req := newRequest(t, http.MethodPost, "/containers/create", `{}`)
	r := fencedTry(t, h, fence(nodeproto.FenceRequest, deployOp, 2), req, func(w io.Writer) error {
		var b strings.Builder
		if err := req.Write(&b); err != nil {
			return err
		}
		// One write, so the extra request arrives with the first.
		_, err := io.WriteString(w, b.String()+"GET /_ping HTTP/1.1\r\nHost: docker\r\n\r\n")
		return err
	})
	if r.code != http.StatusConflict || r.body != "" || r.err == nil {
		t.Fatalf("%d %q %v", r.code, r.body, r.err)
	}
	if got := d.seen(); len(got) != 0 {
		t.Fatalf("daemon saw %v", got)
	}
	if got, err := loadJournal(t, j); err != nil || got.Dirty.Set {
		t.Fatalf("journal %+v %v", got, err)
	}
}

func TestFencedStreamsForOneDaemonRunOneAtATime(t *testing.T) {
	d := startDaemon(t, "daemon-1")
	j, _ := openJournal(t)
	h := DockerHandler(d.socket, j)
	if code, _ := fencedDo(t, h, fence(nodeproto.FenceAdvance, deployOp, 2), newRequest(t, http.MethodPost, "/", "")); code != http.StatusOK {
		t.Fatalf("advance: %d", code)
	}
	hold, reached := make(chan struct{}), make(chan struct{}, 2)
	d.set(func(d *fakeDaemon) { d.hold, d.reached = hold, reached })
	codes := make(chan int, 2)
	for range 2 {
		go func() {
			req := newRequest(t, http.MethodPost, "/containers/create", `{}`)
			r := fencedTry(t, h, fence(nodeproto.FenceRequest, deployOp, 2), req, req.Write)
			if r.readErr != nil || r.err != nil {
				t.Errorf("%d %v %v", r.code, r.readErr, r.err)
			}
			codes <- r.code
		}()
	}
	<-reached
	select {
	case <-reached:
		t.Fatal("two fenced requests reached the daemon at once")
	case <-time.After(200 * time.Millisecond):
	}
	close(hold)
	for range 2 {
		select {
		case code := <-codes:
			if code != http.StatusCreated {
				t.Fatalf("request: %d", code)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a request never finished")
		}
	}
	if got := d.seen(); len(got) != 2 {
		t.Fatalf("daemon saw %v", got)
	}
	if got, err := loadJournal(t, j); err != nil || got.Dirty.Set {
		t.Fatalf("journal %+v %v", got, err)
	}
}

func TestFencedExecWaitsForTheExit(t *testing.T) {
	oldPolls, oldInterval := execExitPolls, execExitInterval
	execExitPolls, execExitInterval = 3, time.Millisecond
	t.Cleanup(func() { execExitPolls, execExitInterval = oldPolls, oldInterval })
	execID, container := strings.Repeat("c", 64), strings.Repeat("d", 64)
	frames := "\x02\x00\x00\x00\x00\x00\x00\x04oops"
	for _, tc := range []struct {
		name       string
		afterStart map[string]any
		clean      bool
	}{
		{"exited", nil, true},
		{"still running", map[string]any{"Running": true, "ExitCode": nil}, false},
		{"no exit code", map[string]any{"ExitCode": nil}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := startDaemon(t, "daemon-1")
			j, _ := openJournal(t)
			h := DockerHandler(d.socket, j)
			d.set(func(d *fakeDaemon) {
				d.execs[execID] = map[string]any{
					"ContainerID": container, "Running": false, "ExitCode": nil,
					"ProcessConfig": map[string]any{"entrypoint": "pg_dump", "arguments": []string{"-Fc"}, "tty": false, "privileged": false},
				}
				d.output = []byte(frames)
				d.afterStart = tc.afterStart
			})
			if code, _ := fencedDo(t, h, fence(nodeproto.FenceAdvance, deployOp, 2), newRequest(t, http.MethodPost, "/", "")); code != http.StatusOK {
				t.Fatalf("advance: %d", code)
			}
			req := newRequest(t, http.MethodPost, "/exec/"+execID+"/start", `{"Detach":false,"Tty":false}`)
			r := fencedTry(t, h, fence(nodeproto.FenceExec, deployOp, 2), req, req.Write)
			if r.code != http.StatusOK || r.body != frames {
				t.Fatalf("%d %q", r.code, r.body)
			}
			got, jerr := loadJournal(t, j)
			if tc.clean {
				if r.readErr != nil || r.err != nil || jerr != nil || got.Dirty.Set {
					t.Fatalf("read %v, handler %v, journal %+v %v", r.readErr, r.err, got, jerr)
				}
				return
			}
			// The outcome is unknown: no last chunk, and the journal stays
			// dirty with the exec it started.
			if r.readErr == nil || r.err == nil || jerr != nil || got.Dirty != fencerules.ExecMark(execID, container) {
				t.Fatalf("read %v, handler %v, journal %+v %v", r.readErr, r.err, got, jerr)
			}
		})
	}
}
