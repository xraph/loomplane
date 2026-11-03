package nodeproto

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
)

func TestHeaderRoundTrip(t *testing.T) {
	in := StreamHeader{
		Endpoint:    EndpointDocker,
		Target:      "unix:///var/run/docker.sock",
		Arg:         "x",
		OrgID:       "org_1",
		OperationID: "op_1",
		Generation:  7,
		Fence:       &Fence{Resource: "svc_1", Generation: 3, Mode: "exclusive", Daemon: "docker"},
	}
	var buf bytes.Buffer
	if err := WriteHeader(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadHeader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("got %+v, want %+v", out, in)
	}
}

func TestHeaderTooLarge(t *testing.T) {
	body := bytes.Repeat([]byte("a"), MaxHeaderSize+1)
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(body)))
	buf.Write(body)
	if _, err := ReadHeader(&buf); err == nil {
		t.Fatal("expected an error for an oversized header")
	}
	if err := WriteHeader(&bytes.Buffer{}, StreamHeader{Target: strings.Repeat("a", MaxHeaderSize)}); err == nil {
		t.Fatal("expected WriteHeader to refuse an oversized header")
	}
}

func TestReadHeaderTruncated(t *testing.T) {
	if _, err := ReadHeader(bytes.NewReader([]byte{0, 0, 0, 9, '{'})); err == nil {
		t.Fatal("expected an error for a truncated header")
	}
}

func TestEndpoints(t *testing.T) {
	for _, e := range []Endpoint{EndpointControl, EndpointProbe, EndpointDocker, EndpointDial, EndpointKube, EndpointFirecracker, EndpointPublic} {
		if !e.Valid() {
			t.Errorf("%q should be valid", e)
		}
	}
	if Endpoint("nope").Valid() {
		t.Error("unknown endpoint should be invalid")
	}
	for _, e := range AllowableEndpoints() {
		if e == EndpointControl || e == EndpointPublic {
			t.Errorf("%q must not be allowable", e)
		}
	}
	if got := len(AllowableEndpoints()); got != 5 {
		t.Errorf("got %d allowable endpoints, want 5", got)
	}
}
