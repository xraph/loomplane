package node

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xraph/loomplane/internal/clouds/server/fencerules"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// After an exec's output ends, the exec modes look for its exit this many
// times, this far apart, as the SSH helper does. Tests shorten it.
var (
	execExitPolls    = 120
	execExitInterval = 250 * time.Millisecond
)

const (
	// maxDockerJSON bounds what the fence reads of /info and an inspect.
	maxDockerJSON = 16 << 20
	// maxFrame is the largest exec output frame the fence relays.
	maxFrame   = 16 << 20
	relayChunk = 64 << 10
	// maxExecStart bounds the exec start body the fence reads to check it.
	maxExecStart = 64 << 10
	// maxRequestHead bounds a fenced request's line and headers, as Docker
	// bounds its own.
	maxRequestHead = 1 << 20
)

var (
	replyConflict = []byte("HTTP/1.1 409 Conflict\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	replyAdvanced = []byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	lastChunk     = []byte("0\r\n\r\n")
)

// errCallerGone ends a fenced request whose caller went away before the whole
// response reached it. The daemon call finished, but the journal stays dirty.
var errCallerGone = errors.New("the caller went away before the whole fenced response was relayed, so the fence journal stays dirty")

// errCallerGoneFirst ends a fenced request whose caller went away while it
// waited for the lock or its checks. Nothing reached the daemon, and the
// journal is unchanged.
var errCallerGoneFirst = errors.New("the caller went away before the fenced request ran, so nothing ran")

// hopHeaders are request headers the fence drops before it forwards a request:
// they describe the hop from the caller, not the request.
var hopHeaders = []string{"Connection", "Content-Length", "Expect", "Keep-Alive", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

// DockerHandler serves the docker endpoint on the daemon at socket.
//
// Target DockerTargetSocket splices the stream to the socket. Target
// DockerTargetFenced carries one HTTP request, which runs through journal the
// way the SSH helper's effect fence runs it: the daemon's /info ID must be
// Fence.Daemon, and then, under the journal's lock, advance records the
// generation and the other modes must hold the current one. A request that
// passes marks the journal dirty, reaches the daemon whatever then happens to
// the stream, and clears the marker only once the whole response has been
// relayed, chunked, and its last chunk written. A refusal is a 409 with no
// body. The caller keeps the stream open until it has read the last chunk;
// closing it sooner counts as going away. journal must be for the daemon at
// socket; a nil journal refuses every fenced request.
func DockerHandler(socket string, journal *FenceJournal) Handler {
	return dockerHandler(socket, func(daemonID string) (*FenceJournal, error) {
		if journal == nil || journal.daemonID != daemonID {
			return nil, errors.New("this node has no fence journal for the daemon")
		}
		return journal, nil
	})
}

// dockerHandler is DockerHandler with the journal looked up by the daemon's
// verified ID.
func dockerHandler(socket string, journalFor func(daemonID string) (*FenceJournal, error)) Handler {
	f := &dockerFence{socket: socket, journalFor: journalFor, api: dockerAPI(socket)}
	return func(ctx context.Context, hdr nodeproto.StreamHeader, stream net.Conn) error {
		switch hdr.Target {
		case nodeproto.DockerTargetSocket:
			return splice(ctx, socket, stream)
		case nodeproto.DockerTargetFenced:
			return f.serve(ctx, hdr, stream)
		}
		return fmt.Errorf("unknown docker target %q", sanitize(hdr.Target))
	}
}

// fenceJournals opens a journal per daemon ID under the state dir dir and
// keeps it, so every stream for one daemon shares its lock.
func fenceJournals(dir string) func(daemonID string) (*FenceJournal, error) {
	var mu sync.Mutex
	open := map[string]*FenceJournal{}
	return func(daemonID string) (*FenceJournal, error) {
		mu.Lock()
		defer mu.Unlock()
		if j := open[daemonID]; j != nil {
			return j, nil
		}
		j, err := OpenFenceJournal(dir, daemonID)
		if err != nil {
			return nil, err
		}
		open[daemonID] = j
		return j, nil
	}
}

// registerDocker registers the docker endpoint when this node may use docker
// and has a socket for it: the configured one, or else the first Docker, then
// Podman, socket that answers. It returns the socket, or "" when it left the
// registry alone, so docker streams are refused as unavailable. A Narrow that
// later drops docker needs nothing from here: Serve checks Live per stream.
func (r *Runtime) registerDocker(ctx context.Context) string {
	cfg := r.live.Load()
	if !cfg.Allows(nodeproto.EndpointDocker) {
		return ""
	}
	socket := findSocket(ctx, cfg.DockerSocket, r.detectBase)
	if socket == "" {
		r.log().Warn("loomplane-node: docker is allowed, but no Docker or Podman socket answered, so this node refuses docker streams until it restarts")
		return ""
	}
	r.reg.Register(nodeproto.EndpointDocker, dockerHandler(socket, fenceJournals(r.st.Dir)))
	r.log().Info("loomplane-node: serving docker", "socket", socket)
	return socket
}

// findSocket is the configured socket, or else the first Docker, then
// Podman, socket in o (the usual places when o leaves them nil) that answers.
// It is "" when none does.
func findSocket(ctx context.Context, configured string, o DetectOptions) string {
	if configured != "" {
		return configured
	}
	docker, podman := o.DockerSockets, o.PodmanSockets
	if docker == nil {
		docker = dockerSockets()
	}
	if podman == nil {
		podman = podmanSockets()
	}
	if rt := pingRuntime(ctx, docker); rt != nil {
		return rt.Socket
	}
	if rt := pingRuntime(ctx, podman); rt != nil {
		return rt.Socket
	}
	return ""
}

// splice copies the stream to the daemon socket and back. The caller closing
// its side half-closes the daemon's, so a response in flight still returns.
func splice(ctx context.Context, socket string, stream net.Conn) error {
	daemon, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	return pipe(stream, daemon)
}

// pipe copies stream to conn and back until conn's side ends, then closes
// conn. The caller closing its side half-closes conn's, so a response in
// flight still returns.
func pipe(stream, conn net.Conn) error {
	go func() {
		_, cerr := io.Copy(conn, stream)
		if hc, ok := conn.(interface{ CloseWrite() error }); ok && cerr == nil && hc.CloseWrite() == nil {
			return
		}
		_ = conn.Close()
	}()
	_, err := io.Copy(stream, conn)
	_ = conn.Close()
	return err
}

// dockerFence runs fenced requests against the daemon at socket.
type dockerFence struct {
	socket     string
	journalFor func(daemonID string) (*FenceJournal, error)
	// api makes the fence's own reads: /info and the inspects.
	api *http.Client
}

// serve reads the one request, runs it and refuses with a 409 if anything
// fails before the response has begun.
func (d *dockerFence) serve(ctx context.Context, hdr nodeproto.StreamHeader, stream net.Conn) error {
	out := &relay{stream: stream}
	if err := hdr.Validate(); err != nil {
		out.refuse()
		return err
	}
	head := &io.LimitedReader{R: stream, N: maxRequestHead}
	br := bufio.NewReader(head)
	req, err := http.ReadRequest(br)
	if err != nil {
		out.refuse()
		return fmt.Errorf("reading the fenced request: %w", err)
	}
	// The body has no cap, as the SSH helper's has none.
	head.N = math.MaxInt64
	// The whole request arrives before anything else happens, so a caller
	// lost mid-request has changed nothing.
	body, size, err := spool(req.Body)
	if err != nil {
		out.refuse()
		return fmt.Errorf("reading the fenced request body: %w", err)
	}
	defer func() { _ = body.Close() }()
	if br.Buffered() > 0 {
		out.refuse()
		return errors.New("a fenced stream carries one request")
	}
	out.watch(br)
	if err := d.run(ctx, *hdr.Fence, req, body, size, out); err != nil {
		out.refuse()
		return err
	}
	return nil
}

// run is the SSH helper's main, with its asserts as returned errors.
func (d *dockerFence) run(ctx context.Context, f nodeproto.Fence, req *http.Request, body *os.File, size int64, out *relay) error {
	uri := req.RequestURI
	if !strings.HasPrefix(uri, "/") || strings.HasPrefix(uri, "//") {
		return errors.New("a fenced request must name a daemon path")
	}
	var info struct{ ID string }
	if err := d.getJSON(ctx, "/info", &info); err != nil {
		return fmt.Errorf("reading the daemon's identity: %w", err)
	}
	if info.ID == "" || info.ID != f.Daemon {
		return errors.New("the daemon on this socket is not the one the fence names")
	}
	j, err := d.journalFor(info.ID)
	if err != nil {
		return err
	}
	err = j.Lock()
	if err != nil {
		return err
	}
	defer j.Unlock()
	state, err := j.Load()
	if err != nil {
		return err
	}
	if state.Dirty.Set {
		err = d.settle(ctx, state.Dirty)
		if err != nil {
			return err
		}
		state.Dirty = fencerules.Dirty{}
		err = j.Save(state)
		if err != nil {
			return err
		}
	}
	if f.Mode == nodeproto.FenceAdvance {
		err = fencerules.Advance(&state, f.Resource, f.Generation)
		if err != nil {
			return err
		}
		err = j.Save(state)
		if err != nil {
			return err
		}
		out.write(replyAdvanced)
		return nil
	}
	err = fencerules.Admit(state, f.Resource, f.Generation)
	if err != nil {
		return err
	}
	streamed := f.Mode == nodeproto.FenceExec || f.Mode == nodeproto.FenceIngressExec
	var inspect, container string
	if streamed {
		if inspect, container, err = d.checkExec(ctx, f, req, body); err != nil {
			return err
		}
	}
	// A caller that gave up while this request queued for the lock, or
	// during its checks, is waiting for nothing. Running the effect anyway
	// would leave the journal dirty for good, so it stops here instead.
	if out.lost() {
		return errCallerGoneFirst
	}
	// From the dirty marker on, the daemon call runs to its end whatever
	// happens to the stream, as the SSH helper ignores SIGHUP and SIGTERM. A
	// failure leaves the journal dirty.
	ctx = context.WithoutCancel(ctx)
	daemonReq, err := outgoing(ctx, req, body, size)
	if err != nil {
		return err
	}
	// An exec start records which exec it ran, so a later takeover can tell
	// once it has finished.
	state.Dirty = fencerules.Dirty{Set: true}
	if streamed {
		id, _ := execStartID(req.RequestURI)
		state.Dirty = fencerules.ExecMark(id, container)
	}
	err = j.Save(state)
	if err != nil {
		return err
	}
	bodiless, err := d.forward(ctx, daemonReq, streamed, inspect, container, out)
	if err != nil {
		return err
	}
	// A response with no body is whole once its head is written. Its caller
	// may close as soon as it has read the head, so only a failed write of it
	// counts.
	if !bodiless {
		if out.lost() {
			return errCallerGone
		}
		// A caller that read the last chunk may close at once, so only a
		// failed write of it counts now.
		out.write(lastChunk)
	}
	if out.failed {
		return errCallerGone
	}
	state.Dirty = fencerules.Dirty{}
	return j.Save(state)
}

// settle clears the way for a takeover of a dirty journal, as the SSH
// helper does: a mark that names an exec settles once Docker shows that
// exec finished, or no longer knows it, since a finished exec is a full
// quiescence receipt. Anything else, a bare mark included, stays dirty for
// an operator's reviewed repair.
func (d *dockerFence) settle(ctx context.Context, mark fencerules.Dirty) error {
	if mark.Exec == "" {
		return fmt.Errorf("%w: it holds an unknown Docker change; review it with loomctl fence-repair", fencerules.ErrDirty)
	}
	var e execJSON
	err := d.getJSON(ctx, "/exec/"+mark.Exec+"/json", &e)
	if errors.Is(err, errDockerNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: checking exec %s: %w", fencerules.ErrDirty, mark.Exec, err)
	}
	// Running comes first: Docker leaves ExitCode null until it has
	// processed the exit event, so only a stopped exec's code counts.
	if e.ContainerID != mark.Container || e.Running == nil || *e.Running || e.ExitCode == nil {
		return fmt.Errorf("%w: exec %s has not finished", fencerules.ErrDirty, mark.Exec)
	}
	return nil
}

// checkExec applies the exec modes' checks to an exec start and returns the
// exec's inspect path and its container.
func (d *dockerFence) checkExec(ctx context.Context, f nodeproto.Fence, req *http.Request, body *os.File) (inspect, container string, err error) {
	start, err := io.ReadAll(io.LimitReader(body, maxExecStart))
	if err != nil {
		return "", "", err
	}
	if _, err = body.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	var exec fencerules.ExecInspect
	// CheckExec refuses any other path; only a well-formed ID is inspected.
	if id, ok := execStartID(req.RequestURI); ok {
		inspect = "/exec/" + id + "/json"
		if exec, err = d.inspectExec(ctx, inspect); err != nil {
			return "", "", err
		}
	}
	err = fencerules.CheckExec(f.Mode, req.Method, req.RequestURI, start, exec)
	if err != nil {
		return "", "", err
	}
	if f.Mode == nodeproto.FenceIngressExec {
		var c struct {
			ID     string `json:"Id"`
			State  *struct{ Running bool }
			Config *struct {
				Image  string
				Labels map[string]string
			}
		}
		err = d.getJSON(ctx, "/containers/"+exec.ContainerID+"/json", &c)
		if err != nil {
			return "", "", err
		}
		if c.State == nil || c.Config == nil {
			return "", "", errors.New("the daemon's container inspect has no State or Config")
		}
		owned := fencerules.ContainerInspect{ID: c.ID, Image: c.Config.Image, Running: c.State.Running, Labels: c.Config.Labels}
		err = fencerules.CheckIngressExec(exec, owned, f.Resource)
		if err != nil {
			return "", "", err
		}
	}
	return inspect, exec.ContainerID, nil
}

// execStartID returns the exec ID of /exec/<64 hex>/start.
func execStartID(path string) (string, bool) {
	id, ok := strings.CutPrefix(path, "/exec/")
	if !ok {
		return "", false
	}
	if id, ok = strings.CutSuffix(id, "/start"); !ok || len(id) != 64 {
		return "", false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false
		}
	}
	return id, true
}

// execJSON is GET /exec/{id}/json. Like the SSH helper, the fence refuses an
// answer that leaves out a field it checks rather than assume a default.
type execJSON struct {
	ContainerID   string
	Running       *bool
	ExitCode      *int
	ProcessConfig *struct {
		Entrypoint string   `json:"entrypoint"`
		Arguments  []string `json:"arguments"`
		TTY        *bool    `json:"tty"`
		Privileged *bool    `json:"privileged"`
	}
}

func (d *dockerFence) inspectExec(ctx context.Context, path string) (fencerules.ExecInspect, error) {
	var e execJSON
	if err := d.getJSON(ctx, path, &e); err != nil {
		return fencerules.ExecInspect{}, err
	}
	p := e.ProcessConfig
	if e.Running == nil || p == nil || p.TTY == nil || p.Privileged == nil {
		return fencerules.ExecInspect{}, errors.New("the daemon's exec inspect lacks Running, tty or privileged")
	}
	return fencerules.ExecInspect{ContainerID: e.ContainerID, Running: *e.Running, Entrypoint: p.Entrypoint,
		Args: p.Arguments, TTY: *p.TTY, Privileged: *p.Privileged}, nil
}

// outgoing is the request to send the daemon: the caller's, without its hop
// headers, with the spooled body and its exact length.
func outgoing(ctx context.Context, in *http.Request, body *os.File, size int64) (*http.Request, error) {
	// The host is a placeholder: forward writes the request to the socket.
	req, err := http.NewRequestWithContext(ctx, in.Method, "http://docker"+in.RequestURI, http.NoBody) //nolint:gosec // G704: see above
	if err != nil {
		return nil, err
	}
	req.Header = in.Header.Clone()
	for _, h := range hopHeaders {
		req.Header.Del(h)
	}
	if size > 0 {
		req.Body = io.NopCloser(body)
	}
	req.ContentLength = size
	req.Close = true
	return req, nil
}

// forward sends req to the daemon on a connection of its own and relays the
// response. The response must have a length or chunked framing, except an
// exec's close-delimited output stream. A response that HTTP gives no body
// (1xx, 204, 304, or the answer to a HEAD) is relayed as its head alone,
// with no chunked framing, as a caller reads no body after it; forward then
// reports it bodiless and run writes no last chunk.
func (d *dockerFence) forward(ctx context.Context, req *http.Request, streamed bool, inspect, container string, out *relay) (bodiless bool, err error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", d.socket)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	err = req.Write(conn)
	if err != nil {
		return false, fmt.Errorf("sending the fenced request: %w", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return false, fmt.Errorf("reading the daemon's response: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	bodiless = noBody(req.Method, resp.StatusCode)
	chunked := len(resp.TransferEncoding) > 0 && resp.TransferEncoding[0] == "chunked"
	frames := streamed && resp.StatusCode == http.StatusOK
	if !bodiless && !chunked && resp.ContentLength < 0 && !frames {
		return false, errors.New("the daemon's response has no length")
	}

	out.responding = true
	var head strings.Builder
	head.WriteString("HTTP/1.1 " + resp.Status + "\r\n")
	for k, vs := range resp.Header {
		switch k {
		case "Transfer-Encoding", "Connection", "Content-Length":
			continue
		}
		for _, v := range vs {
			head.WriteString(k + ": " + v + "\r\n")
		}
	}
	if bodiless {
		head.WriteString("Connection: close\r\n\r\n")
		out.write([]byte(head.String()))
		return true, nil
	}
	head.WriteString("Transfer-Encoding: chunked\r\nConnection: close\r\n\r\n")
	out.write([]byte(head.String()))
	if frames {
		if err := relayFrames(resp.Body, out); err != nil {
			return false, err
		}
		return false, d.waitExit(ctx, inspect, container)
	}
	return false, relayBody(resp.Body, out)
}

// noBody reports whether HTTP gives the response to a method request with
// this status no body.
func noBody(method string, status int) bool {
	return method == http.MethodHead || status/100 == 1 || status == http.StatusNoContent || status == http.StatusNotModified
}

// relayBody relays a framed body. Go's reader refuses a short body and a
// chunked one that ends before its last chunk and trailer.
func relayBody(r io.Reader, out *relay) error {
	buf := make([]byte, relayChunk)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			out.chunk(buf[:n])
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the daemon's response: %w", err)
		}
	}
}

// relayFrames relays Docker's multiplexed exec output, complete stdout and
// stderr frames only.
func relayFrames(r io.Reader, out *relay) error {
	buf := make([]byte, relayChunk)
	for {
		var h [8]byte
		n, err := io.ReadFull(r, h[:])
		if n == 0 && errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading exec output: %w", err)
		}
		if (h[0] != 1 && h[0] != 2) || h[1] != 0 || h[2] != 0 || h[3] != 0 {
			return errors.New("exec output is not a stdout or stderr frame")
		}
		size := int(binary.BigEndian.Uint32(h[4:]))
		if size > maxFrame {
			return fmt.Errorf("exec output frame of %d bytes is over %d", size, maxFrame)
		}
		out.chunk(h[:])
		for size > 0 {
			n := min(size, len(buf))
			if _, err := io.ReadFull(r, buf[:n]); err != nil {
				return fmt.Errorf("reading exec output: %w", err)
			}
			out.chunk(buf[:n])
			size -= n
		}
	}
}

// waitExit waits for the exec to report its exit. Docker leaves ExitCode
// null until it has processed the exit event, which can land just after the
// output stream closes.
func (d *dockerFence) waitExit(ctx context.Context, inspect, container string) error {
	for range execExitPolls {
		var e execJSON
		if err := d.getJSON(ctx, inspect, &e); err != nil {
			return err
		}
		if e.ContainerID != container || e.Running == nil {
			return errors.New("the daemon's exec inspect changed under the fence")
		}
		if !*e.Running {
			if e.ExitCode == nil {
				return errors.New("the exec stopped without an exit code")
			}
			return nil
		}
		time.Sleep(execExitInterval)
	}
	return errors.New("the exec was still running after its output ended")
}

func (d *dockerFence) getJSON(ctx context.Context, path string, v any) error {
	return dockerGetJSON(ctx, d.api, path, v)
}

// dockerAPI is an HTTP client that reaches only the daemon at socket.
func dockerAPI(socket string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
		DisableCompression: true,
	}}
}

// errDockerNotFound is the daemon answering 404.
var errDockerNotFound = errors.New("not found")

// dockerGetJSON decodes the daemon's answer to GET path into v. Anything but
// a 200 is an error, errDockerNotFound for a 404.
func dockerGetJSON(ctx context.Context, api *http.Client, path string, v any) error {
	// The host is a placeholder: api dials only the daemon socket.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, http.NoBody) //nolint:gosec // G704: see above
	if err != nil {
		return err
	}
	resp, err := api.Do(req) //nolint:gosec // G704: as above
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return fmt.Errorf("GET %s: %w", path, errDockerNotFound)
	default:
		return fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxDockerJSON)).Decode(v)
}

// spool reads the request body into an unlinked temp file, as the SSH helper
// does, and returns it rewound.
func spool(r io.Reader) (*os.File, int64, error) {
	f, err := os.CreateTemp("", "loomplane-fence-*")
	if err != nil {
		return nil, 0, err
	}
	// The open file keeps the body; nothing is left on disk.
	_ = os.Remove(f.Name())
	n, err := io.Copy(f, r)
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, n, nil
}

// relay writes the fenced response to the caller. Once a write fails, or the
// caller is gone, it writes nothing more, and the daemon call runs on.
type relay struct {
	stream net.Conn
	// gone is set when the caller closes the stream or sends anything after
	// its request.
	gone atomic.Bool
	// failed is set by the first failed or skipped write.
	failed bool
	// responding is set once the daemon's response has begun, after which a
	// failure can no longer be a 409.
	responding bool
}

// watch notes the caller going away and unblocks a write in progress.
func (r *relay) watch(rd io.Reader) {
	go func() {
		var b [1]byte
		_, _ = rd.Read(b[:]) //nolint:errcheck // any return, data or error, means the caller is done
		r.gone.Store(true)
		_ = r.stream.SetWriteDeadline(time.Unix(1, 0)) //nolint:errcheck // a stream without deadlines fails its next write instead
	}()
}

func (r *relay) lost() bool { return r.failed || r.gone.Load() }

func (r *relay) write(b []byte) {
	if r.lost() {
		r.failed = true
		return
	}
	if _, err := r.stream.Write(b); err != nil {
		r.failed = true
	}
}

func (r *relay) chunk(b []byte) {
	buf := make([]byte, 0, len(b)+12)
	buf = fmt.Appendf(buf, "%x\r\n", len(b))
	buf = append(buf, b...)
	r.write(append(buf, "\r\n"...))
}

// refuse answers 409 unless the response has begun.
func (r *relay) refuse() {
	if !r.responding {
		r.write(replyConflict)
	}
}
