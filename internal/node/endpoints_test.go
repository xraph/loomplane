package node

import (
	"bufio"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

func serveWith(t *testing.T, r *Registry, cfg *Config, hdr nodeproto.StreamHeader) (net.Conn, chan struct{}) {
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
	r := &Registry{}
	r.Register(eKube, func(context.Context, nodeproto.StreamHeader, net.Conn) error { t.Error("ran"); return nil })
	c, done := serveWith(t, r, &Config{Endpoints: []nodeproto.Endpoint{eDocker}}, nodeproto.StreamHeader{Endpoint: eKube})
	v, line := readRefusal(t, c)
	if v != 1 || line != "endpoint kube is not allowed on this node\n" {
		t.Fatalf("%d %q", v, line)
	}
	<-done
}

func TestServeRefusesUnregistered(t *testing.T) {
	r := &Registry{}
	c, done := serveWith(t, r, &Config{Endpoints: []nodeproto.Endpoint{eDocker}}, nodeproto.StreamHeader{Endpoint: eDocker})
	v, line := readRefusal(t, c)
	if v != 1 || line != "endpoint docker is not available in this node version\n" {
		t.Fatalf("%d %q", v, line)
	}
	<-done
}

func TestServeRunsHandler(t *testing.T) {
	r := &Registry{}
	r.Register(eDocker, func(_ context.Context, h nodeproto.StreamHeader, s net.Conn) error {
		_, err := s.Write([]byte("hi " + h.Target))
		return err
	})
	c, done := serveWith(t, r, &Config{Endpoints: []nodeproto.Endpoint{eDocker}}, nodeproto.StreamHeader{Endpoint: eDocker, Target: "x"})
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
	go func() { (&Registry{}).Serve(context.Background(), &Config{}, b); close(done) }()
	_ = a.Close()
	<-done
}

func TestServeProbeGatedLikeAnyOther(t *testing.T) {
	r := &Registry{}
	r.Register(eProbe, ProbeHandler(func() DetectOptions { return DetectOptions{} }))
	c, done := serveWith(t, r, &Config{}, nodeproto.StreamHeader{Endpoint: eProbe})
	if v, _ := readRefusal(t, c); v != 1 {
		t.Fatalf("probe should be refused when not allowed, got %d", v)
	}
	<-done
}
