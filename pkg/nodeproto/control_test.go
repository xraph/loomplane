package nodeproto

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestControlRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	msgs := []any{
		Hello{Version: Version, NodeVersion: "0.1.0", Epoch: 4},
		Heartbeat{At: at, Usage: Usage{CPUPercent: 12.5, MemoryUsedMiB: 512, DiskUsedGiB: 20}},
		InventoryReport{Inventory: Inventory{
			OS: "linux", Kernel: "6.8", Arch: "amd64", CPUs: 8, MemoryMiB: 16384, DiskGiB: 200,
			Docker:          &Runtime{Socket: "/var/run/docker.sock", Version: "27.0"},
			GPUs:            []GPU{{Vendor: "nvidia", Model: "L4", MemoryMiB: 24576}},
			Endpoints:       []Endpoint{EndpointProbe, EndpointDocker},
			PublicAddresses: []string{"203.0.113.9"},
			Ports80443:      "free",
		}},
		Narrow{Endpoints: []Endpoint{EndpointProbe}},
		NarrowApplied{Endpoints: []Endpoint{EndpointProbe}},
		SetIngress{Mode: "direct"},
		IngressApplied{Mode: "direct"},
		Revoked{},
		Leave{},
		RotateKey{NewPublicKey: []byte{1, 2, 3}, Signature: []byte{4, 5, 6}},
	}
	for _, msg := range msgs {
		var buf bytes.Buffer
		if err := EncodeControl(&buf, msg); err != nil {
			t.Fatalf("%T: encode: %v", msg, err)
		}
		line := buf.Bytes()
		if line[len(line)-1] != '\n' || bytes.Count(line, []byte("\n")) != 1 {
			t.Fatalf("%T: want exactly one trailing newline, got %q", msg, line)
		}
		got, err := DecodeControl(bytes.TrimSpace(line))
		if err != nil {
			t.Fatalf("%T: decode: %v", msg, err)
		}
		if !reflect.DeepEqual(got, msg) {
			t.Errorf("%T: got %+v, want %+v", msg, got, msg)
		}
	}
}

func TestEncodeSetsType(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeControl(&buf, Revoked{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(buf.String()); got != `{"type":"revoked"}` {
		t.Fatalf("got %s", got)
	}
}

func TestEncodeRejectsUnknown(t *testing.T) {
	if err := EncodeControl(&bytes.Buffer{}, struct{}{}); err == nil {
		t.Fatal("expected an error for an unsupported message")
	}
	if err := EncodeControl(&bytes.Buffer{}, Unknown{Type: "x"}); err == nil {
		t.Fatal("expected an error encoding Unknown")
	}
}

func TestDecodeUnknownType(t *testing.T) {
	got, err := DecodeControl([]byte(`{"type":"future","x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Unknown{Type: "future"}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, in := range []string{``, `not json`, `{}`, `{"type":""}`, `{"type":"hello","version":"x"}`} {
		if _, err := DecodeControl([]byte(in)); err == nil {
			t.Errorf("%q: expected an error", in)
		}
	}
}
