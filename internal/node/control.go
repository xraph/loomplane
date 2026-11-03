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

// controlResponse answers a request with one JSON line. Message, when set,
// is something the caller should print.
type controlResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
}

// errNoService means nothing listens on the control socket: no node is running
// on this state dir. An error the service itself sent is not this.
var errNoService = errors.New("no node service is listening on the control socket")

// serveControl listens on the control socket until ctx ends or the returned
// stop function runs, and removes the socket then. The socket is private to
// the service user, and each caller is checked against the node's own user
// and root. A node that cannot listen still runs: only the local allow and
// leave commands lose their way in, and they say so. A socket that someone
// answers on means another node runs on this state dir, and that is an error.
func (r *Runtime) serveControl(ctx context.Context) (stop func(), err error) {
	path := filepath.Join(r.st.Dir, controlSocket)
	// A socket left by a crash would refuse the bind, but a live one belongs
	// to a running node. Remove only a dead socket, never another file.
	if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode()&fs.ModeSocket != 0 {
		var d net.Dialer
		dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		c, derr := d.DialContext(dctx, "unix", path)
		cancel()
		if derr == nil {
			_ = c.Close()
			return nil, errors.New("another node service is running on this state directory")
		}
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
		return func() {}, nil
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
	}, nil
}

func (r *Runtime) serveControlConn(ctx context.Context, c net.Conn) {
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(controlTimeout)) //nolint:errcheck // a failed deadline only loses the timeout
	replyWith := func(note string, err error) {
		resp := controlResponse{OK: err == nil, Message: sanitize(note)}
		if err != nil {
			resp.Error = sanitize(err.Error())
		}
		b, _ := json.Marshal(resp)      //nolint:errcheck // plain fields
		_, _ = c.Write(append(b, '\n')) //nolint:errcheck // the client may be gone
	}
	reply := func(err error) { replyWith("", err) }
	if ok, perr := peerAllowed(c); perr != nil || !ok {
		reply(errors.New("this user may not use the node's control socket"))
		return
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
		replyWith(r.leave(ctx))
	default:
		reply(fmt.Errorf("unknown request %q", sanitize(req.Op)))
	}
}

// Leave asks Loomplane to revoke this node, through the live session, and
// waits up to five seconds for Run to decide the node was revoked: Loomplane
// said so, or closed the connection with the unauthorized code, or refused the
// key at the reconnect that followed the request. A connection that merely
// ended is not an answer, so Leave then returns an error and the node keeps
// its config.
func (r *Runtime) Leave(ctx context.Context) error {
	_, err := r.leave(ctx)
	return err
}

// leftUnannounced is what leave says when Loomplane refused this node's key,
// so it could not be asked to revoke the node.
const leftUnannounced = "Loomplane refused this node's key at its last connect, so it could not be told; if the node still shows in the dashboard, revoke it there"

// leave is Leave, with a note for the person who asked. A node whose key
// Loomplane refuses has no session to ask through, and can't connect again
// either, so it stops as revoked, and the note says Loomplane was not told.
func (r *Runtime) leave(ctx context.Context) (string, error) {
	r.mu.Lock()
	s := r.cur
	r.mu.Unlock()
	if s == nil {
		if !r.refused.Load() {
			return "", errors.New("this node is not connected to Loomplane right now")
		}
		select {
		case r.leaveNow <- struct{}{}:
		default:
		}
		t := time.NewTimer(leaveWait)
		defer t.Stop()
		select {
		case <-r.revoked:
			return leftUnannounced, nil
		case <-t.C:
			return "", errors.New("the node did not stop within " + leaveWait.String())
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	r.leaving.Store(true)
	if err := s.send(nodeproto.Leave{}); err != nil {
		r.leaving.Store(false)
		return "", fmt.Errorf("asking Loomplane to revoke this node: %w", err)
	}
	t := time.NewTimer(leaveWait)
	defer t.Stop()
	select {
	case <-r.revoked:
		return "", nil
	case <-t.C:
		// Not revoked as far as this node knows: a later refusal is not ours.
		r.leaving.Store(false)
		return "", errors.New("Loomplane did not confirm that it revoked this node within " + leaveWait.String())
	case <-ctx.Done():
		r.leaving.Store(false)
		return "", ctx.Err()
	}
}

// controlCall sends one request to the node running on the socket at path.
// It returns errNoService when nothing listens there.
func controlCall(ctx context.Context, path string, req controlRequest) error {
	_, err := controlCallNote(ctx, path, req)
	return err
}

// controlCallNote is controlCall, and returns the note the node sent back.
func controlCallNote(ctx context.Context, path string, req controlRequest) (string, error) {
	var d net.Dialer
	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return "", errNoService
		}
		return "", fmt.Errorf("reaching the node's control socket %s: %w", path, err)
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl) //nolint:errcheck // as above
	}
	b, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	if _, err := c.Write(append(b, '\n')); err != nil {
		return "", err
	}
	var resp controlResponse
	if err := json.NewDecoder(io.LimitReader(c, 64<<10)).Decode(&resp); err != nil {
		return "", fmt.Errorf("reading the node's answer: %w", err)
	}
	if !resp.OK {
		return "", errors.New(resp.Error)
	}
	return resp.Message, nil
}
