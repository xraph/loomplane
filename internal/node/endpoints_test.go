package node

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func serveWith(t *testing.T, r *Registry, cfg *Live, hdr nodeproto.StreamHeader) (net.Conn, chan struct{}) {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() {
		r.Serve(context.Background(), cfg, b)
		close(done)
	}()
	_ = a.SetDeadline(time.Now().Add(5 * time.Second))
	if err := nodeproto.WriteHeader(a, hdr); err != nil {
		t.Fatal(err)
	}
	return a, done
}

func readRefusal(t *testing.T, c net.Conn) (byte, string) {
	t.Helper()
	br := bufio.NewReader(c)
	v, err := br.ReadByte()
	if err != nil {
		t.Fatal(err)
	}
	line, _ := br.ReadString('\n')
	return v, line
}

func TestServeRefusesDisallowed(t *testing.T) {
	r := &Registry{Log: discardLog()}
	r.Register(eKube, func(context.Context, nodeproto.StreamHeader, net.Conn) error { t.Error("ran"); return nil })
	c, done := serveWith(t, r, NewLive(Config{Endpoints: []nodeproto.Endpoint{eDocker}}), nodeproto.StreamHeader{Endpoint: eKube})
	v, line := readRefusal(t, c)
	// Not allowed has a verdict of its own, so the hub can tell it from
	// any other refusal.
	if v != nodeproto.VerdictNotAllowed || line != "endpoint kube is not allowed on this node\n" {
		t.Fatalf("%d %q", v, line)
	}
	<-done
}

func TestServeRefusesUnregistered(t *testing.T) {
	r := &Registry{Log: discardLog()}
	c, done := serveWith(t, r, NewLive(Config{Endpoints: []nodeproto.Endpoint{eDocker}}), nodeproto.StreamHeader{Endpoint: eDocker})
	v, line := readRefusal(t, c)
	if v != nodeproto.VerdictRefused || line != "endpoint docker is not available in this node version\n" {
		t.Fatalf("%d %q", v, line)
	}
	<-done
}

func TestServeRunsHandler(t *testing.T) {
	r := &Registry{Log: discardLog()}
	r.Register(eDocker, func(_ context.Context, h nodeproto.StreamHeader, s net.Conn) error {
		_, err := s.Write([]byte("hi " + h.Target))
		return err
	})
	c, done := serveWith(t, r, NewLive(Config{Endpoints: []nodeproto.Endpoint{eDocker}}), nodeproto.StreamHeader{Endpoint: eDocker, Target: "x"})
	var v [1]byte
	if _, err := io.ReadFull(c, v[:]); err != nil || v[0] != 0 {
		t.Fatalf("verdict %v %v", v, err)
	}
	b, _ := io.ReadAll(c)
	if string(b) != "hi x" {
		t.Fatalf("%q", b)
	}
	<-done
}

func TestServeBadHeaderClosesQuietly(*testing.T) {
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() { (&Registry{Log: discardLog()}).Serve(context.Background(), NewLive(Config{}), b); close(done) }()
	_ = a.Close()
	<-done
}

func TestServeProbeGatedLikeAnyOther(t *testing.T) {
	r := &Registry{Log: discardLog()}
	r.Register(eProbe, ProbeHandler(func() DetectOptions { return DetectOptions{} }))
	c, done := serveWith(t, r, NewLive(Config{}), nodeproto.StreamHeader{Endpoint: eProbe})
	if v, _ := readRefusal(t, c); v != nodeproto.VerdictNotAllowed {
		t.Fatalf("probe should be refused when not allowed, got %d", v)
	}
	<-done
}

func TestServeRefusesAfterNarrowAndLogs(t *testing.T) {
	var buf bytes.Buffer
	r := &Registry{Log: slog.New(slog.NewTextHandler(&buf, nil))}
	r.Register(eKube, func(context.Context, nodeproto.StreamHeader, net.Conn) error { t.Error("ran"); return nil })
	live := NewLive(Config{Endpoints: []nodeproto.Endpoint{eDocker, eKube}})
	live.Update(func(c *Config) { c.Narrow([]nodeproto.Endpoint{eDocker}) })
	c, done := serveWith(t, r, live, nodeproto.StreamHeader{Endpoint: eKube})
	if v, line := readRefusal(t, c); v != nodeproto.VerdictNotAllowed || !strings.Contains(line, "not allowed") {
		t.Fatalf("%d %q", v, line)
	}
	<-done
	if !strings.Contains(buf.String(), "not allowed on this node") {
		t.Fatalf("refusal not logged: %q", buf.String())
	}
}

func TestRefusalReasonIsSanitized(t *testing.T) {
	r := &Registry{Log: discardLog()}
	c, done := serveWith(t, r, NewLive(Config{}), nodeproto.StreamHeader{Endpoint: "x\x1b[0m\ny"})
	_, line := readRefusal(t, c)
	if strings.ContainsAny(line[:len(line)-1], "\x1b\n") {
		t.Fatalf("%q", line)
	}
	<-done
}

func TestServeDropsStreamWithNoHeader(t *testing.T) {
	old := headerTimeout
	headerTimeout = 50 * time.Millisecond
	defer func() { headerTimeout = old }()
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	done := make(chan struct{})
	go func() {
		(&Registry{Log: discardLog()}).Serve(context.Background(), NewLive(Config{}), b)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve waited forever for a header")
	}
}
