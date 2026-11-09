package node

import (
	"bufio"
	"context"
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

func probeRegistry() (*Registry, *Live) {
	r := &Registry{Log: discardLog()}
	r.Register(eProbe, ProbeHandler(func() DetectOptions { return quietDetect() }))
	return r, NewLive(Config{Endpoints: []nodeproto.Endpoint{eProbe}})
}

func TestProbeInventoryTargetIsPhaseOne(t *testing.T) {
	r, live := probeRegistry()
	c, done := serveWith(t, r, live, nodeproto.StreamHeader{Endpoint: eProbe})
	br := bufio.NewReader(c)
	if v, err := br.ReadByte(); err != nil || v != 0 {
		t.Fatalf("verdict %d %v", v, err)
	}
	var inv nodeproto.Inventory
	if err := json.NewDecoder(br).Decode(&inv); err != nil || inv.Hostname != "box" {
		t.Fatalf("%+v %v", inv, err)
	}
	<-done
}

func TestProbeUnknownTargetIsRefused(t *testing.T) {
	r, live := probeRegistry()
	c, done := serveWith(t, r, live, nodeproto.StreamHeader{Endpoint: eProbe, Target: "kernel\x1b"})
	if v, line := readRefusal(t, c); v != 1 || line != "unknown probe target \"kernel\"\n" {
		t.Fatalf("%d %q", v, line)
	}
	<-done
}

func TestProbeFailureIsRefused(t *testing.T) {
	r := &Registry{Log: discardLog()}
	p := newHostProbes(func(context.Context) string { return "" })
	r.Register(eProbe, probeHandler(func() DetectOptions { return quietDetect() }, p))
	c, done := serveWith(t, r, NewLive(Config{Endpoints: []nodeproto.Endpoint{eProbe}}),
		nodeproto.StreamHeader{Endpoint: eProbe, Target: nodeproto.ProbeFilesystem, Arg: "relative"})
	if v, line := readRefusal(t, c); v != 1 || line[:len("probe filesystem: ")] != "probe filesystem: " {
		t.Fatalf("%d %q", v, line)
	}
	<-done
}
