package node

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestLeaveWaitsForRevokeAndTimesOut(t *testing.T) {
	r, rt, cfg, done := liveNode(t)
	_ = cfg
	_ = r
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
	// Not connected: say so at once.
	r2 := newRig(t)
	r2.st = State{Dir: filepath.Join(shortDir(t), "state")}
	r2.join(t, nodeproto.EndpointProbe)
	rt2 := r2.runtime(t)
	if err := rt2.Leave(t.Context()); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("Leave while offline: %v", err)
	}
}
