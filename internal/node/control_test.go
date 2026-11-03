package node

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

func TestControlSocketIsPrivateAndRemovedOnExit(t *testing.T) {
	r, _, _, _ := liveNode(t)
	sock := filepath.Join(r.st.Dir, "control.sock")
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || fi.Mode()&fs.ModeSocket == 0 {
		t.Fatalf("socket mode %v", fi.Mode())
	}
}

func TestControlSocketRemovedWhenRunEnds(t *testing.T) {
	r := newRig(t)
	r.st = State{Dir: filepath.Join(shortDir(t), "state")}
	cfg := r.join(t, nodeproto.EndpointProbe)
	rt := r.runtime(t)
	cancel, done := startCancelable(t, rt)
	eventually(t, "the node to be online", func() bool { return r.online(cfg.NodeID) })
	sock := filepath.Join(r.st.Dir, "control.sock")
	if _, err := os.Stat(sock); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
	if _, err := os.Stat(sock); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the socket stayed: %v", err)
	}
}

func TestControlSocketReplacesAStaleOne(t *testing.T) {
	r := newRig(t)
	r.st = State{Dir: filepath.Join(shortDir(t), "state")}
	cfg := r.join(t, nodeproto.EndpointProbe)
	sock := filepath.Join(r.st.Dir, "control.sock")
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "unix", sock) // a crashed service's leftover
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()
	start(t, r.runtime(t))
	eventually(t, "the node to be online", func() bool { return r.online(cfg.NodeID) })
	eventually(t, "the socket to answer", func() bool {
		err := controlCall(t.Context(), sock, controlRequest{Op: "nonsense"})
		return err != nil && !errors.Is(err, errNoService)
	})
}

func TestControlRejectsBadRequests(t *testing.T) {
	r, _, _, _ := liveNode(t)
	sock := filepath.Join(r.st.Dir, "control.sock")
	for _, req := range []controlRequest{{Op: "nonsense"}, {Op: "allow"}, {Op: "allow", Endpoints: []nodeproto.Endpoint{"public"}}} {
		if err := controlCall(t.Context(), sock, req); err == nil || errors.Is(err, errNoService) {
			t.Errorf("%+v: %v", req, err)
		}
	}
	// A client that sends junk gets an error line, not a hang.
	var d net.Dialer
	c, err := d.DialContext(t.Context(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, werr := c.Write([]byte("not json\n")); werr != nil {
		t.Fatal(werr)
	}
	buf := make([]byte, 256)
	n, err := c.Read(buf)
	if err != nil || !strings.Contains(string(buf[:n]), `"error"`) {
		t.Fatalf("got %q, %v", buf[:n], err)
	}
}

func TestControlCallWithNoServiceIsErrNoService(t *testing.T) {
	err := controlCall(t.Context(), filepath.Join(shortDir(t), "control.sock"), controlRequest{Op: "allow"})
	if !errors.Is(err, errNoService) {
		t.Fatalf("got %v", err)
	}
}

func TestLeaveReturnsOnceRevoked(t *testing.T) {
	_, rt, _, done := liveNode(t)
	if err := rt.Leave(t.Context()); err != nil {
		t.Fatalf("Leave: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrRevoked) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept going after Leave")
	}
}

func TestLeaveWhileOfflineSaysSo(t *testing.T) {
	r := newRig(t)
	r.st = State{Dir: filepath.Join(shortDir(t), "state")}
	r.join(t, nodeproto.EndpointProbe)
	if err := r.runtime(t).Leave(t.Context()); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("Leave while offline: %v", err)
	}
}

// A session that ends with no Revoked and no unauthorized close is a network
// blip, not a revoke: Leave must say it could not confirm.
func TestLeaveIsAnErrorWhenTheSessionEndsWithoutRevoked(t *testing.T) {
	old := leaveWait
	leaveWait = 300 * time.Millisecond
	t.Cleanup(func() { leaveWait = old })

	nodeEnd, hubEnd := net.Pipe()
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	hub, err := yamux.Client(hubEnd, cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux, err := yamux.Server(nodeEnd, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hub.Close(); _ = mux.Close() })
	hubCtrl, err := hub.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	nodeCtrl, err := mux.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{revoked: make(chan struct{})}
	s := &session{r: rt, mux: mux, ctrl: nodeCtrl, done: make(chan struct{})}
	rt.attach(s)
	go func() { // the "hub": reads the Leave, then drops the connection
		_, _ = bufio.NewReader(hubCtrl).ReadBytes('\n')
		_ = hub.Close()
		close(s.done)
	}()
	err = rt.Leave(t.Context())
	if err == nil || !strings.Contains(err.Error(), "did not confirm") {
		t.Fatalf("Leave: %v", err)
	}
	select {
	case <-rt.revoked:
		t.Fatal("a dropped connection was taken for a revoke")
	default:
	}
}

func TestControlRefusesToStartBesideALiveNode(t *testing.T) {
	r := newRig(t)
	r.st = State{Dir: filepath.Join(shortDir(t), "state")}
	cfg := r.join(t, nodeproto.EndpointProbe)
	first := r.runtime(t)
	start(t, first)
	eventually(t, "the node to be online", func() bool { return r.online(cfg.NodeID) })
	second := r.runtime(t)
	err := second.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "another node service is running") {
		t.Fatalf("second Run: %v", err)
	}
	// The first node's socket is still there and still answers.
	if err := controlCall(t.Context(), filepath.Join(r.st.Dir, controlSocket), controlRequest{Op: "nonsense"}); err == nil || errors.Is(err, errNoService) {
		t.Fatalf("the first node lost its socket: %v", err)
	}
}
