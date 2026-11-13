package catalogconnect

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/xraph/loomplane/internal/recipes"
)

var errRedisForwardDeadline = errors.New("the forwarding channel does not support deadlines")

type redisForwardedConn struct {
	net.Conn
	release               <-chan struct{}
	reads, writes, closes atomic.Int32
}

func (*redisForwardedConn) SetDeadline(time.Time) error      { return errRedisForwardDeadline }
func (*redisForwardedConn) SetReadDeadline(time.Time) error  { return errRedisForwardDeadline }
func (*redisForwardedConn) SetWriteDeadline(time.Time) error { return errRedisForwardDeadline }

func (c *redisForwardedConn) Read(p []byte) (int, error) {
	c.reads.Add(1)
	return c.Conn.Read(p)
}

func (c *redisForwardedConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	return c.Conn.Write(p)
}

func (c *redisForwardedConn) Close() error {
	c.closes.Add(1)
	if c.release != nil {
		<-c.release
	}
	return c.Conn.Close()
}

func TestRedisProberSSHForwarding(t *testing.T) {
	checkRedisForwardedExchange(t, "redis://app:fixture-password@cache.example/4", "cache.example:6379", nil)
}

func TestRedisProberSSHForwardingTLS(t *testing.T) {
	const child = "LOOMPLANE_REDIS_SSH_TLS_CHILD"
	if os.Getenv(child) != "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRedisProberSSHForwardingTLS$")
		cmd.Env = append(os.Environ(), child+"=1", "GODEBUG=x509usefallbackroots=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("verified Redis TLS over forwarding failed: %s (%v)", output, err)
		}
		return
	}

	cert, ca := searchTestCA(t)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(ca)) {
		t.Fatal("could not load fixture trust roots")
	}
	x509.SetFallbackRoots(roots)
	config := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	checkRedisForwardedExchange(t, "rediss://app:fixture-password@search.example:6380/4", "search.example:6380", config)
}

func checkRedisForwardedExchange(t *testing.T, dsn, address string, config *tls.Config) {
	t.Helper()
	client, peer := net.Pipe()
	wire := &redisForwardedConn{Conn: client}
	server := peer
	if config != nil {
		server = tls.Server(peer, config)
	}
	peerResult := make(chan error, 1)
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		defer peer.Close()
		peerResult <- serveRedisForwardedExchange(server)
	}()
	defer func() {
		_ = client.Close()
		_ = peer.Close()
		<-peerDone
	}()

	var dials int
	dial := func(_ context.Context, network, target string) (net.Conn, error) {
		dials++
		if dials != 1 || network != "tcp" || target != address {
			return nil, errors.New("unexpected forwarding dial")
		}
		return wire, nil
	}
	report, err := (RedisProber{}).Probe(t.Context(), dial, dsn, recipes.Requirement{Engine: EngineRedis, MinVersion: "7"})
	if err != nil {
		t.Fatalf("Redis probe over forwarding failed: %v", err)
	}
	if report == nil || report.Version != "7.2.5" || !report.Result().OK {
		t.Fatalf("unexpected report: %+v", report)
	}
	select {
	case err = <-peerResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Redis peer did not see the connection close")
	}
	if wire.closes.Load() != 1 {
		t.Fatalf("raw connection closed %d times", wire.closes.Load())
	}
}

func serveRedisForwardedExchange(conn net.Conn) error {
	reader := bufio.NewReader(conn)
	const info = "# Server\r\nredis_version:7.2.5\r\n"
	for _, step := range []struct {
		command []string
		reply   string
	}{
		{[]string{"AUTH", "app", "fixture-password"}, "+OK\r\n"},
		{[]string{"PING"}, "+PONG\r\n"},
		{[]string{"INFO", "server"}, fmt.Sprintf("$%d\r\n%s\r\n", len(info), info)},
	} {
		command, err := readCommand(reader)
		if err != nil {
			return err
		}
		if !slices.Equal(command, step.command) {
			return fmt.Errorf("unexpected command before %s", step.command[0])
		}
		if _, err = io.WriteString(conn, step.reply); err != nil {
			return err
		}
	}
	if _, err := readCommand(reader); !errors.Is(err, io.EOF) {
		return fmt.Errorf("expected EOF after INFO server, got %w", err)
	}
	return nil
}

func TestRedisProberSSHInterruption(t *testing.T) {
	for _, stage := range []string{"handshake", "read", "write"} {
		for _, mode := range []string{"cancel", "deadline"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					checkRedisForwardedInterruption(t, stage, mode == "deadline")
				})
			})
		}
	}
}

func checkRedisForwardedInterruption(t *testing.T, stage string, expires bool) {
	t.Helper()
	const timeout = time.Second
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	wire := &redisForwardedConn{Conn: client}
	if stage != "write" {
		go func() { _, _ = io.Copy(io.Discard, peer) }()
	}
	dsn := "redis://cache.example"
	if stage == "handshake" {
		dsn = "rediss://cache.example"
	}
	result := startRedisForwardedProbe(ctx, wire, dsn)
	synctest.Wait()
	if wire.writes.Load() == 0 || (wire.reads.Load() > 0) != (stage != "write") {
		t.Fatal("probe did not reach the stalled I/O")
	}
	want := context.Canceled
	if expires {
		want = errTimedOut
		time.Sleep(timeout)
	} else {
		cancel()
	}
	synctest.Wait()
	assertRedisForwardedInterruption(t, result, want)
	if wire.closes.Load() != 1 {
		t.Fatalf("raw connection closed %d times", wire.closes.Load())
	}
}

func TestRedisProberSSHReturnsBeforeTargetTeardown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		client, peer := net.Pipe()
		defer client.Close()
		defer peer.Close()
		transportClosed := make(chan struct{})
		closeTransport := sync.OnceFunc(func() { close(transportClosed) })
		defer closeTransport()
		wire := &redisForwardedConn{Conn: client, release: transportClosed}
		result := startRedisForwardedProbe(ctx, wire, "redis://:fixture-password@cache.example")
		commands := make(chan []string, 1)
		go func() {
			command, _ := readCommand(bufio.NewReader(peer))
			commands <- command
		}()
		synctest.Wait()
		select {
		case command := <-commands:
			if !slices.Equal(command, []string{"AUTH", "fixture-password"}) {
				t.Fatalf("expected AUTH before cancellation, got %q", command)
			}
		default:
			t.Fatal("probe did not send AUTH")
		}
		if wire.reads.Load() != 1 {
			t.Fatal("probe did not block waiting for the AUTH reply")
		}

		cancel()
		synctest.Wait()
		assertRedisForwardedInterruption(t, result, context.Canceled)
		if wire.closes.Load() != 1 {
			t.Fatalf("expected one pending channel close, got %d", wire.closes.Load())
		}

		// Deliver a late AUTH reply while the channel still waits for teardown.
		if _, err := io.WriteString(peer, "+OK\r\n"); err != nil {
			t.Fatal(err)
		}
		closeTransport()
		synctest.Wait()
		if wire.writes.Load() != 1 || wire.closes.Load() != 1 {
			t.Fatalf("after cancel, writes=%d closes=%d", wire.writes.Load(), wire.closes.Load())
		}
	})
}

type redisForwardedResult struct {
	report *Report
	err    error
}

func startRedisForwardedProbe(ctx context.Context, wire net.Conn, dsn string) <-chan redisForwardedResult {
	result := make(chan redisForwardedResult, 1)
	go func() {
		report, err := (RedisProber{}).Probe(ctx, func(context.Context, string, string) (net.Conn, error) {
			return wire, nil
		}, dsn, recipes.Requirement{})
		result <- redisForwardedResult{report: report, err: err}
	}()
	return result
}

func assertRedisForwardedInterruption(t *testing.T, result <-chan redisForwardedResult, want error) {
	t.Helper()
	select {
	case got := <-result:
		if got.report != nil || !errors.Is(got.err, want) {
			t.Fatalf("probe returned (%+v, %v), want %v", got.report, got.err, want)
		}
	default:
		t.Fatal("probe did not return after the context ended")
	}
}

func TestRedisProberSSHMissingDialConnection(t *testing.T) {
	for name, dial := range map[string]DialFunc{
		"nil dialer": nil,
		"nil connection": func(context.Context, string, string) (net.Conn, error) {
			return nil, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			report, err := (RedisProber{}).Probe(t.Context(), dial, "redis://cache.example", recipes.Requirement{})
			if report != nil || err == nil {
				t.Fatalf("probe returned (%+v, %v), want a dial failure", report, err)
			}
		})
	}
}
