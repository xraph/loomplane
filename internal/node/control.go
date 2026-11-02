package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// controlSocket is the file in the state dir where a running node takes
// requests from `node allow` and `node leave` on the same machine.
const controlSocket = "control.sock"

// leaveWait is how long Leave waits for Loomplane to answer.
var leaveWait = 5 * time.Second

// controlTimeout bounds one request on the socket, leave's wait included.
const controlTimeout = 20 * time.Second

// controlRequest is the one JSON line a client sends: {"op":"allow",
// "endpoints":[...]} or {"op":"leave"}.
type controlRequest struct {
	Op        string               `json:"op"`
	Endpoints []nodeproto.Endpoint `json:"endpoints,omitempty"`
}

// controlResponse answers a request with one JSON line.
type controlResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// errNoService means nothing listens on the control socket: no node is running
// on this state dir. An error the service itself sent is not this.
var errNoService = errors.New("no node service is listening on the control socket")

// serveControl listens on the control socket until ctx ends or the returned
// stop function runs, and removes the socket then. The socket is private to
// the service user. A node that cannot listen still runs: only the local
// allow and leave commands lose their way in, and they say so.
func (r *Runtime) serveControl(ctx context.Context) (stop func()) {
	path := filepath.Join(r.st.Dir, controlSocket)
	// A socket left by a crash would refuse the bind. Remove only a socket,
	// never another kind of file.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSocket != 0 {
		_ = os.Remove(path)
	}
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "unix", path)
	if err == nil {
		err = os.Chmod(path, 0o600)
		if err != nil {
			_ = l.Close()
		}
	}
	if err != nil {
		r.log().Warn("loomplane-node: no control socket, so node allow and node leave cannot reach this node", "path", path, "error", err)
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, aerr := l.Accept()
			if aerr != nil {
				return
			}
			go r.serveControlConn(ctx, c)
		}
	}()
	stopped := context.AfterFunc(ctx, func() { _ = l.Close() })
	return func() {
		cancel()
		stopped()
		_ = l.Close()
		<-done
		_ = os.Remove(path)
	}
}

func (r *Runtime) serveControlConn(ctx context.Context, c net.Conn) {
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(controlTimeout)) //nolint:errcheck // a failed deadline only loses the timeout
	reply := func(err error) {
		resp := controlResponse{OK: err == nil}
		if err != nil {
			resp.Error = sanitize(err.Error())
		}
		b, _ := json.Marshal(resp)      //nolint:errcheck // two plain fields
		_, _ = c.Write(append(b, '\n')) //nolint:errcheck // the client may be gone
	}
	line, err := bufio.NewReaderSize(io.LimitReader(c, 64<<10), 4<<10).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		reply(fmt.Errorf("reading the request: %w", err))
		return
	}
	var req controlRequest
	if err := json.Unmarshal(line, &req); err != nil {
		reply(errors.New("the request is not JSON"))
		return
	}
	switch req.Op {
	case "allow":
		if len(req.Endpoints) == 0 {
			reply(errors.New("allow needs endpoints"))
			return
		}
		reply(r.Allow(req.Endpoints))
	case "leave":
		reply(r.Leave(ctx))
	default:
		reply(fmt.Errorf("unknown request %q", sanitize(req.Op)))
	}
}

// Leave asks Loomplane to revoke this node, through the live session, and
// waits up to five seconds for the answer: the session ends once Loomplane
// revokes the node, and Run then removes node.json and returns ErrRevoked.
func (r *Runtime) Leave(ctx context.Context) error {
	r.mu.Lock()
	s := r.cur
	r.mu.Unlock()
	if s == nil {
		return errors.New("this node is not connected to Loomplane right now")
	}
	r.leaving.Store(true)
	if err := s.send(nodeproto.Leave{}); err != nil {
		r.leaving.Store(false)
		return fmt.Errorf("asking Loomplane to revoke this node: %w", err)
	}
	t := time.NewTimer(leaveWait)
	defer t.Stop()
	select {
	case <-s.done:
		return nil
	case <-t.C:
		return errors.New("Loomplane did not answer the request to leave within " + leaveWait.String())
	case <-ctx.Done():
		return ctx.Err()
	}
}

// controlCall sends one request to the node running on the socket at path.
// It returns errNoService when nothing listens there.
func controlCall(ctx context.Context, path string, req controlRequest) error {
	var d net.Dialer
	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return errNoService
		}
		return fmt.Errorf("reaching the node's control socket %s: %w", path, err)
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl) //nolint:errcheck // as above
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := c.Write(append(b, '\n')); err != nil {
		return err
	}
	var resp controlResponse
	if err := json.NewDecoder(io.LimitReader(c, 64<<10)).Decode(&resp); err != nil {
		return fmt.Errorf("reading the node's answer: %w", err)
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	return nil
}
