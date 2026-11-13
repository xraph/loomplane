package catalogconnect

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/recipes"
)

func falkorAccess() EngineAccess {
	return EngineAccess{Engine: "falkordb", Host: "graph.example", Username: "fixture:app", Password: "fake-secret *2 $4 :1", TLS: TLSDisable}
}

func falkorRaw(t *testing.T, a EngineAccess) string {
	t.Helper()
	// Marshal directly so the probe tests exercise the production parser.
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal("encode test access")
	}
	return string(raw)
}

type falkorProbeContextKey struct{}

type falkorForwardedConn struct{ net.Conn }

var errFalkorFixtureDeadline = errors.New("fixture forwarding channel does not support deadlines")

func (falkorForwardedConn) SetDeadline(time.Time) error      { return errFalkorFixtureDeadline }
func (falkorForwardedConn) SetReadDeadline(time.Time) error  { return errFalkorFixtureDeadline }
func (falkorForwardedConn) SetWriteDeadline(time.Time) error { return errFalkorFixtureDeadline }

func falkorTestDial(t *testing.T, address string, serve func(net.Conn)) DialFunc {
	t.Helper()
	var calls atomic.Int32
	return func(ctx context.Context, network, target string) (net.Conn, error) {
		deadline, ok := ctx.Deadline()
		if network != "tcp" || target != address || calls.Add(1) != 1 || !ok || time.Until(deadline) > 10*time.Second {
			t.Error("probe escaped the supplied authority, deadline or single dial")
			return nil, errors.New("unexpected dial")
		}
		client, server := net.Pipe()
		t.Cleanup(func() {
			_ = client.Close()
			_ = server.Close()
		})
		done := make(chan struct{})
		t.Cleanup(func() {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("fixture peer did not observe the probe close")
				_ = server.Close()
				_ = client.Close()
				<-done
			}
		})
		go func() {
			defer close(done)
			defer server.Close()
			_ = server.SetDeadline(deadline)
			serve(server)
		}()
		return falkorForwardedConn{client}, nil
	}
}

func falkorTestExchange(t *testing.T, conn net.Conn, a EngineAccess, hello, config string) {
	t.Helper()
	reader := bufio.NewReader(conn)
	command, err := readCommand(reader)
	if err != nil || !slices.Equal(command, []string{"HELLO", "2", "AUTH", a.Username, a.Password}) {
		t.Error("expected one HELLO with the supplied credentials")
		return
	}
	if _, err = io.WriteString(conn, hello); err != nil {
		t.Error("HELLO reply write failed")
		return
	}
	if config != "" {
		command, err = readCommand(reader)
		if err != nil || !slices.Equal(command, []string{"GRAPH.CONFIG", "GET", "RESULTSET_SIZE"}) {
			t.Error("expected one fixed configuration read")
			return
		}
		if _, err = io.WriteString(conn, config); err != nil {
			t.Error("configuration reply write failed")
			return
		}
	}
	if _, err = reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Error("probe did not close the connection at the reply boundary")
	}
}

func TestFalkorProbeUsesSuppliedTargetAndOnlyFixedCommands(t *testing.T) {
	a := falkorAccess()
	for _, minVersion := range []string{"4.2", "4.3"} {
		t.Run(minVersion, func(t *testing.T) {
			dial := falkorTestDial(t, "graph.example:6379", func(conn net.Conn) {
				falkorTestExchange(t, conn, a, falkorTestHello(falkorTestModule("graph", 40202, a.Password)), falkorTestConfig(73))
			})
			guarded := func(ctx context.Context, network, address string) (net.Conn, error) {
				if ctx.Value(falkorProbeContextKey{}) != "target" {
					t.Error("target context was lost")
				}
				return dial(ctx, network, address)
			}
			ctx := context.WithValue(t.Context(), falkorProbeContextKey{}, "target")
			r, err := (FalkorDBProber{}).Probe(ctx, guarded, falkorRaw(t, a), recipes.Requirement{Engine: "falkordb", MinVersion: minVersion})
			if err != nil || r == nil || r.Version != "4.2.2" || r.Result().OK != (minVersion == "4.2") {
				t.Fatal("probe did not report the validated module version and minimum-version result")
			}
			if strings.Contains(fmt.Sprint(r), a.Password) {
				t.Fatal("report exposed private module metadata")
			}
		})
	}
}

func TestFalkorProbeRejectsAuthProductAndCapabilityFailures(t *testing.T) {
	a := falkorAccess()
	hello := falkorTestHello(falkorTestModule("graph", 40202))
	for _, tc := range []struct{ name, hello, config, want string }{
		{"wrong password", "-WRONGPASS " + a.Password + "\r\n", "", "the FalkorDB credentials were refused"},
		{"missing authentication", "-NOAUTH " + a.Password + "\r\n", "", "the FalkorDB credentials were refused"},
		{"plain Redis", falkorTestHello(), "", "the endpoint did not declare FalkorDB 4.2.2"},
		{"wrong version", falkorTestHello(falkorTestModule("graph", 40203)), "", "the endpoint did not declare FalkorDB 4.2.2"},
		{"wrong HELLO type", ":17\r\n", "", "the FalkorDB response could not be verified"},
		{"pipelined reply", hello + falkorTestConfig(10000), "", "the FalkorDB response could not be verified"},
		{"config denied", hello, "-NOPERM " + a.Password + "\r\n", "the FalkorDB configuration read was denied"},
		{"config no auth", hello, "-NOAUTH " + a.Password + "\r\n", "the FalkorDB credentials were refused"},
		{"missing command", hello, "-ERR unknown command " + a.Password + "\r\n", "the FalkorDB configuration read was refused"},
		{"redirect", hello, "-MOVED 123 elsewhere.example:6379\r\n", "the FalkorDB configuration read was refused"},
		{"malformed config", hello, falkorTestBulk(a.Password), "the FalkorDB response could not be verified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dial := falkorTestDial(t, "graph.example:6379", func(conn net.Conn) {
				falkorTestExchange(t, conn, a, tc.hello, tc.config)
			})
			r, err := (FalkorDBProber{}).Probe(t.Context(), dial, falkorRaw(t, a), recipes.Requirement{})
			if r != nil || err == nil || err.Error() != tc.want {
				t.Fatal("expected a fixed, stage-specific probe failure")
			}
		})
	}
}

func TestFalkorProbeRejectsRequirementBeforeDial(t *testing.T) {
	a := falkorAccess()
	dial := func(context.Context, string, string) (net.Conn, error) {
		t.Error("rejected requirement reached the dialer")
		return nil, errors.New("unexpected dial")
	}
	for _, req := range []recipes.Requirement{
		{Engine: "redis"}, {TLS: []string{TLSVerifyFull}}, {TLS: []string{TLSRequire}},
		{Extensions: []string{"vector"}}, {Preload: []string{"library"}}, {CreateRole: true},
	} {
		r, err := (FalkorDBProber{}).Probe(t.Context(), dial, falkorRaw(t, a), req)
		if err == nil && (r == nil || r.Result().OK) {
			t.Fatal("unsupported requirement accepted")
		}
	}
	search := searchAccess(EngineElasticsearch)
	if _, err := (FalkorDBProber{}).Probe(t.Context(), dial, falkorRaw(t, search), recipes.Requirement{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("FalkorDB prober accepted search credentials")
	}
}

func TestSearchProberRejectsFalkorEnvelope(t *testing.T) {
	a := falkorAccess()
	// Require a valid Falkor envelope, so parser rejection can't mask a missing
	// engine check in SearchProber.
	raw, err := EncodeEngineAccess(a)
	if err != nil {
		t.Fatal("valid FalkorDB envelope rejected")
	}
	dial := func(context.Context, string, string) (net.Conn, error) {
		t.Error("search prober tried to dial using FalkorDB access")
		return nil, errors.New("unexpected dial")
	}
	if _, err = (SearchProber{}).Probe(t.Context(), dial, raw, recipes.Requirement{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("search prober accepted a FalkorDB envelope")
	}
}

func TestFalkorProbeTLSVerifiesHostAndCA(t *testing.T) {
	cert, ca := searchTestCA(t)
	for _, tc := range []struct {
		name, host, root string
		valid            bool
	}{
		{"trusted", "search.example", ca, true},
		{"wrong host", "graph.example", ca, false},
		{"untrusted", "search.example", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := falkorAccess()
			a.Host, a.TLS, a.CACert = tc.host, TLSVerifyFull, tc.root
			dial := falkorTestDial(t, tc.host+":6379", func(conn net.Conn) {
				server := tls.Server(conn, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})
				if err := server.HandshakeContext(t.Context()); err != nil {
					if tc.valid {
						t.Error("verified TLS handshake failed")
					}
					return
				}
				if !tc.valid {
					t.Error("unverified TLS handshake finished")
					return
				}
				falkorTestExchange(t, server, a, falkorTestHello(falkorTestModule("graph", 40202)), falkorTestConfig(-1))
			})
			r, err := (FalkorDBProber{}).Probe(t.Context(), dial, falkorRaw(t, a), recipes.Requirement{})
			if tc.valid {
				if err != nil || r == nil || !r.Result().OK {
					t.Fatal("verified TLS probe failed")
				}
			} else if r != nil || err == nil || err.Error() != "the FalkorDB TLS handshake could not be verified" {
				t.Fatal("expected a fixed TLS failure")
			}
		})
	}
}

type falkorWriteSignal struct {
	net.Conn
	started chan struct{}
}

func (c *falkorWriteSignal) Write(p []byte) (int, error) {
	close(c.started)
	return c.Conn.Write(p)
}

func TestFalkorProbeCancellationClosesOriginalConnection(t *testing.T) {
	t.Run("already canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		dial := func(context.Context, string, string) (net.Conn, error) {
			t.Error("canceled probe reached the target")
			return nil, errors.New("unexpected fixture dial")
		}
		report, err := (FalkorDBProber{}).Probe(ctx, dial, falkorRaw(t, falkorAccess()), recipes.Requirement{})
		if report != nil || err == nil || err.Error() != "the FalkorDB check was canceled" {
			t.Fatal("probe did not return the fixed cancellation result")
		}
	})
	for _, mode := range []string{"cancel", "deadline"} {
		for _, stage := range []string{"dial", "tls", "write", "read"} {
			t.Run(mode+"/"+stage, func(t *testing.T) {
				checkFalkorProbeInterruption(t, stage, mode == "deadline")
			})
		}
	}
}

func checkFalkorProbeInterruption(t *testing.T, stage string, expires bool) {
	t.Helper()
	a := falkorAccess()
	if stage == "tls" {
		a.TLS = TLSVerifyFull
	}
	timeout := 2 * time.Second
	want := "the FalkorDB check was canceled"
	if expires {
		timeout = 250 * time.Millisecond
		want = "no answer within 10s"
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	entered := make(chan struct{})
	returned := make(chan struct{})
	peerResult := make(chan error, 1)
	calls := 0
	dial := func(dialCtx context.Context, network, address string) (net.Conn, error) {
		calls++
		if calls != 1 || network != "tcp" || address != "graph.example:6379" {
			return nil, errors.New("unexpected fixture dial")
		}
		if stage == "dial" {
			if !expires {
				cancel()
			}
			<-dialCtx.Done()
			return nil, dialCtx.Err()
		}
		client, server := net.Pipe()
		started := make(chan struct{})
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
		// Safety deadlines are only on the test pipe; the supplied connection
		// refuses all deadline methods.
		_ = client.SetDeadline(time.Now().Add(2 * time.Second))
		_ = server.SetDeadline(time.Now().Add(2 * time.Second))
		go func() {
			switch stage {
			case "write":
				select {
				case <-started:
				case <-ctx.Done():
					return
				}
			case "tls":
				if _, err := io.CopyN(io.Discard, server, 5); err != nil {
					return
				}
			case "read":
				if _, err := readCommand(bufio.NewReader(server)); err != nil {
					return
				}
			}
			close(entered)
			if !expires {
				cancel()
			}
			// Do not drain the stalled write before Probe has returned.
			if stage == "write" {
				<-returned
			}
			_, err := io.Copy(io.Discard, server)
			peerResult <- err
		}()
		forwarded := falkorForwardedConn{client}
		if stage == "write" {
			return &falkorWriteSignal{Conn: forwarded, started: started}, nil
		}
		return forwarded, nil
	}
	start := time.Now()
	report, err := (FalkorDBProber{}).Probe(ctx, dial, falkorRaw(t, a), recipes.Requirement{})
	close(returned)
	if report != nil || err == nil || err.Error() != want || calls != 1 || time.Since(start) > time.Second {
		t.Fatal("probe did not return the fixed interruption result promptly")
	}
	if stage == "dial" {
		return
	}
	select {
	case <-entered:
	default:
		t.Fatal("fixture did not reach the interrupted stage")
	}
	select {
	case peerErr := <-peerResult:
		if peerErr != nil {
			t.Fatal("original connection did not close cleanly")
		}
	case <-time.After(time.Second):
		t.Fatal("original connection remained open")
	}
}

type falkorDelayedForwardClose struct {
	falkorForwardedConn
	reading, readDone, closing, closed chan struct{}
	release                            <-chan struct{}
	reads, writes, closes              atomic.Int32
}

func (c *falkorDelayedForwardClose) Read(p []byte) (int, error) {
	if c.reads.Add(1) == 1 {
		close(c.reading)
		defer close(c.readDone)
	}
	return c.Conn.Read(p)
}

func (c *falkorDelayedForwardClose) Write(p []byte) (int, error) {
	c.writes.Add(1)
	return c.Conn.Write(p)
}

func (c *falkorDelayedForwardClose) Close() error {
	if c.closes.Add(1) == 1 {
		close(c.closing)
		defer close(c.closed)
	}
	<-c.release
	return c.Conn.Close()
}

func TestFalkorProbeDeadlineDoesNotWaitForForwardClose(t *testing.T) {
	a := falkorAccess()
	raw := falkorRaw(t, a)
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	client, peer := net.Pipe()
	transportClosed := make(chan struct{})
	closeTransport := sync.OnceFunc(func() { close(transportClosed) })
	t.Cleanup(func() { closeTransport(); _ = client.Close(); _ = peer.Close() })
	conn := &falkorDelayedForwardClose{
		falkorForwardedConn: falkorForwardedConn{client}, release: transportClosed,
		reading: make(chan struct{}), readDone: make(chan struct{}), closing: make(chan struct{}), closed: make(chan struct{}),
	}
	commands := make(chan []string, 1)
	go func() {
		command, _ := readCommand(bufio.NewReader(peer))
		commands <- command
	}()
	result := make(chan bool, 1)
	var dials atomic.Int32
	go func() {
		report, err := (FalkorDBProber{}).Probe(ctx, func(_ context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || address != "graph.example:6379" || dials.Add(1) != 1 {
				return nil, errors.New("unexpected fixture dial")
			}
			return conn, nil
		}, raw, recipes.Requirement{})
		result <- report == nil && errors.Is(err, errTimedOut)
	}()
	select {
	case <-conn.reading:
	case <-conn.closing:
		t.Fatal("probe closed the forwarding channel before attempting a reply")
	case <-time.After(time.Second):
		t.Fatal("probe did not reach the blocked reply")
	}
	select {
	case valid := <-result:
		if !valid {
			t.Fatal("blocked forwarding did not return the fixed timeout")
		}
	case <-time.After(time.Second):
		t.Fatal("probe waited for the forwarding channel to close")
	}
	select {
	case <-conn.closing:
	case <-time.After(time.Second):
		t.Fatal("context did not start closing the forwarding channel")
	}
	select {
	case <-conn.readDone:
		t.Fatal("forwarded read ended before its transport closed")
	default:
	}
	// TargetDial's owner can close the SSH transport only after Probe returns.
	closeTransport()
	for _, done := range []<-chan struct{}{conn.readDone, conn.closed} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("transport shutdown did not release forwarding I/O")
		}
	}
	if command := <-commands; !slices.Equal(command, []string{"HELLO", "2", "AUTH", a.Username, a.Password}) ||
		dials.Load() != 1 || conn.writes.Load() != 1 || conn.closes.Load() != 1 {
		t.Fatal("interrupted probe retried or sent an unexpected command")
	}
}

func TestFalkorProbeDialFailureAndTimeoutDoNotEchoCredentials(t *testing.T) {
	a := falkorAccess()
	raw := falkorRaw(t, a)
	for _, tc := range []struct {
		dial DialFunc
		want string
	}{
		{nil, "the FalkorDB check requires a guarded dialer"},
		{func(context.Context, string, string) (net.Conn, error) { return nil, nil }, "the FalkorDB connection could not be opened"},
		{func(context.Context, string, string) (net.Conn, error) { return nil, errors.New(raw) }, "the FalkorDB connection could not be opened"},
		{func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }, "no answer within 10s"},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		r, err := (FalkorDBProber{}).Probe(ctx, tc.dial, raw, recipes.Requirement{})
		cancel()
		if r != nil || err == nil || err.Error() != tc.want {
			t.Fatal("expected a fixed dial or deadline failure")
		}
	}
}
