//go:build linux || darwin

package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// fakeDockerAPI serves h on a Unix socket and returns the socket's path.
func fakeDockerAPI(t *testing.T, h http.Handler) string {
	t.Helper()
	// macOS caps a Unix socket path at 104 bytes, and t.TempDir is long.
	dir, err := os.MkdirTemp("", "lpd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return socket
}

type fakeEndpoint struct {
	NetworkID, IPAddress, GlobalIPv6Address string
}

type fakeContainer struct {
	ID       string
	State    string
	Networks map[string]fakeEndpoint
}

type fakeNetwork struct {
	ID, Name string
	Labels   map[string]string
}

// fakeNetworks is a daemon with networks and containers, which answers the
// two calls the dial guard makes the way Docker does.
type fakeNetworks struct {
	mu         sync.Mutex
	networks   []fakeNetwork
	containers []fakeContainer
	// ignoreFilter answers /networks as a daemon that drops the filter.
	ignoreFilter bool
	lists        atomic.Int32
}

func (f *fakeNetworks) start(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "OK") })
	mux.HandleFunc("GET /networks", func(w http.ResponseWriter, r *http.Request) {
		f.lists.Add(1)
		if got := r.URL.Query().Get("filters"); got != `{"label":["loomplane.org"]}` {
			http.Error(w, "unexpected filters "+got, http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []map[string]any{}
		for _, n := range f.networks {
			if _, ok := n.Labels["loomplane.org"]; ok || f.ignoreFilter {
				out = append(out, map[string]any{"Id": n.ID, "Name": n.Name, "Labels": n.Labels})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /containers/json", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []map[string]any{}
		for _, c := range f.containers {
			if c.State != "running" && r.URL.Query().Get("all") == "" {
				continue
			}
			out = append(out, map[string]any{"Id": c.ID, "State": c.State,
				"NetworkSettings": map[string]any{"Networks": c.Networks}})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	return fakeDockerAPI(t, mux)
}

// dialDaemon has one Loomplane network and Docker's default bridge. c1 is on
// both, c2 only on the bridge, and c3 is a stopped container on lp-net.
func dialDaemon() *fakeNetworks {
	return &fakeNetworks{
		networks: []fakeNetwork{
			{ID: "n1", Name: "lp-net", Labels: map[string]string{"loomplane.org": "org_1"}},
			{ID: "n0", Name: "bridge", Labels: map[string]string{}},
		},
		containers: []fakeContainer{
			{ID: "c1", State: "running", Networks: map[string]fakeEndpoint{
				"lp-net": {NetworkID: "n1", IPAddress: "172.18.0.5", GlobalIPv6Address: "fd00:18::5"},
				"bridge": {NetworkID: "n0", IPAddress: "172.17.0.9"},
			}},
			{ID: "c2", State: "running", Networks: map[string]fakeEndpoint{"bridge": {NetworkID: "n0", IPAddress: "172.17.0.2"}}},
			{ID: "c3", State: "exited", Networks: map[string]fakeEndpoint{"lp-net": {NetworkID: "n1", IPAddress: "172.18.0.7"}}},
		},
	}
}

// echoListener accepts on loopback and echoes what each connection sends.
func echoListener(t *testing.T) net.Listener {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return l
}

func dialRegistry(h Handler) (*Registry, *Live) {
	r := &Registry{Log: discardLog()}
	r.Register(nodeproto.EndpointDial, h)
	return r, NewLive(Config{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDial}})
}

// expectEcho reads the accept verdict and checks the stream reaches the
// echo listener.
func expectEcho(t *testing.T, c net.Conn) {
	t.Helper()
	var v [1]byte
	if _, err := io.ReadFull(c, v[:]); err != nil || v[0] != 0 {
		t.Fatalf("verdict %v %v", v, err)
	}
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "ping" {
		t.Fatalf("echo %q %v", got, err)
	}
}

func expectRefused(t *testing.T, h Handler, hdr nodeproto.StreamHeader, want string) {
	t.Helper()
	r, live := dialRegistry(h)
	hdr.Endpoint = nodeproto.EndpointDial
	c, done := serveWith(t, r, live, hdr)
	v, line := readRefusal(t, c)
	if v != 1 || line != want+"\n" {
		t.Fatalf("%s: verdict %d %q, want %q", hdr.Target, v, line, want)
	}
	<-done
}

func TestDialAllowsLoopback(t *testing.T) {
	l := echoListener(t)
	_, port, _ := net.SplitHostPort(l.Addr().String())
	h := DialHandler(dialDaemon().start(t))
	for _, target := range []string{l.Addr().String(), net.JoinHostPort("::ffff:127.0.0.1", port), net.JoinHostPort("localhost", port)} {
		r, live := dialRegistry(h)
		c, done := serveWith(t, r, live, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDial, Target: target})
		expectEcho(t, c)
		_ = c.Close()
		<-done
	}
}

func TestDialAllowsLabelledContainer(t *testing.T) {
	l := echoListener(t)
	g := newDialGuard(dialDaemon().start(t))
	var checked []string
	// Container IPs aren't routable from the test, so the connect step
	// sends the address the guard approved to the echo listener instead.
	g.connect = func(ctx context.Context, d *net.Dialer, network, address string) (net.Conn, error) {
		ip, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if err := d.Control(network, address, nil); err != nil {
			return nil, err
		}
		checked = append(checked, ip)
		return (&net.Dialer{}).DialContext(ctx, network, l.Addr().String())
	}
	for _, target := range []string{"172.18.0.5:5432", "[::ffff:172.18.0.5]:5432", "[fd00:18::5]:5432"} {
		r, live := dialRegistry(g.serve)
		c, done := serveWith(t, r, live, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDial, Target: target})
		expectEcho(t, c)
		_ = c.Close()
		<-done
	}
	if strings.Join(checked, ",") != "172.18.0.5,::ffff:172.18.0.5,fd00:18::5" {
		t.Fatalf("checked %v", checked)
	}
}

func TestDialRefusesUnlabelledContainer(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		d := dialDaemon()
		d.ignoreFilter = ignore
		h := DialHandler(d.start(t))
		// c1's address on the bridge, c2 on the bridge only, and a stopped
		// container on the Loomplane network.
		for _, ip := range []string{"172.17.0.9", "172.17.0.2", "172.18.0.7"} {
			expectRefused(t, h, nodeproto.StreamHeader{Target: ip + ":5432"},
				"address "+ip+" is not a Loomplane container or loopback")
		}
	}
}

func TestDialRefusesDockerBridgeGateway(t *testing.T) {
	expectRefused(t, DialHandler(dialDaemon().start(t)), nodeproto.StreamHeader{Target: "172.17.0.1:5432"},
		"address 172.17.0.1 is not a Loomplane container or loopback")
}

func TestDialRefusesMetadataIP(t *testing.T) {
	h := DialHandler(dialDaemon().start(t))
	for _, target := range []string{"169.254.169.254:80", "[::ffff:169.254.169.254]:80", "[::ffff:a9fe:a9fe]:80"} {
		expectRefused(t, h, nodeproto.StreamHeader{Target: target},
			"address 169.254.169.254 is not a Loomplane container or loopback")
	}
	expectRefused(t, h, nodeproto.StreamHeader{Target: "[fd00:ec2::254]:80"},
		"address fd00:ec2::254 is not a Loomplane container or loopback")
	expectRefused(t, h, nodeproto.StreamHeader{Target: "0.0.0.0:80"},
		"address 0.0.0.0 is not a Loomplane container or loopback")
}

func TestDialRefusesUDP(t *testing.T) {
	h := DialHandler(dialDaemon().start(t))
	expectRefused(t, h, nodeproto.StreamHeader{Target: "127.0.0.1:53", Arg: "udp"}, "the dial endpoint dials only TCP, not udp")
	expectRefused(t, h, nodeproto.StreamHeader{Target: "127.0.0.1"}, "dial target 127.0.0.1 is not host:port")
}

func TestDialRefusesWhenDockerDoesNotAnswer(t *testing.T) {
	h := DialHandler(filepath.Join(t.TempDir(), "none.sock"))
	r, live := dialRegistry(h)
	c, done := serveWith(t, r, live, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDial, Target: "172.18.0.5:5432"})
	v, line := readRefusal(t, c)
	if v != 1 || !strings.HasPrefix(line, "listing Loomplane containers: ") {
		t.Fatalf("%d %q", v, line)
	}
	<-done
}

func TestDialRefusalCarriesConnectError(t *testing.T) {
	// A loopback port with nothing listening is refused with the connect
	// error, as the SSH dialer returns it.
	l := echoListener(t)
	addr := l.Addr().String()
	_ = l.Close()
	r, live := dialRegistry(DialHandler(dialDaemon().start(t)))
	c, done := serveWith(t, r, live, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDial, Target: addr})
	if v, line := readRefusal(t, c); v != 1 || !strings.Contains(line, "connection refused") {
		t.Fatalf("%d %q", v, line)
	}
	<-done
}

func TestDialCachesLabelledContainers(t *testing.T) {
	d := dialDaemon()
	g := newDialGuard(d.start(t))
	now := time.Unix(1000, 0)
	var clock sync.Mutex
	g.now = func() time.Time { clock.Lock(); defer clock.Unlock(); return now }
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := g.check(ctx, "172.18.0.5:1"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := d.lists.Load(); n != 1 {
		t.Fatalf("listed networks %d times, want once", n)
	}
	// c2 joins the Loomplane network; the guard sees it once the cache ends.
	d.mu.Lock()
	d.containers[1].Networks["lp-net"] = fakeEndpoint{NetworkID: "n1", IPAddress: "172.18.0.6"}
	d.mu.Unlock()
	var denied *deniedAddrError
	if err := g.check(ctx, "172.18.0.6:1"); !errors.As(err, &denied) {
		t.Fatalf("cached answer: %v", err)
	}
	clock.Lock()
	now = now.Add(6 * time.Second)
	clock.Unlock()
	if err := g.check(ctx, "172.18.0.6:1"); err != nil {
		t.Fatalf("after the cache ends: %v", err)
	}
	if n := d.lists.Load(); n != 2 {
		t.Fatalf("listed networks %d times, want twice", n)
	}
}

func TestRegisterDialNeedsAllowAndSocket(t *testing.T) {
	sock := dialDaemon().start(t)
	nowhere := []string{filepath.Join(t.TempDir(), "none.sock")}
	for _, tc := range []struct {
		name     string
		cfg      Config
		detected []string
		want     string
	}{
		{"not allowed", Config{Endpoints: []nodeproto.Endpoint{eDocker}, DockerSocket: sock}, nil, ""},
		{"configured", Config{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDial}, DockerSocket: sock}, nowhere, sock},
		{"detected", Config{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDial}}, []string{sock}, sock},
		{"no socket", Config{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDial}}, nowhere, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &Registry{}
			rt := &Runtime{st: State{Dir: t.TempDir()}, reg: reg, live: NewLive(tc.cfg), Log: discardLog(),
				detectBase: DetectOptions{DockerSockets: tc.detected, PodmanSockets: nowhere}}
			if got := rt.registerDial(context.Background(), ""); got != tc.want {
				t.Fatalf("socket %q, want %q", got, tc.want)
			}
			if registered := reg.handlers[nodeproto.EndpointDial] != nil; registered != (tc.want != "") {
				t.Fatalf("registered %v", registered)
			}
		})
	}
}
