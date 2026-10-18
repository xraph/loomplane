package server

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func TestDockerStreamSeparatesDiagnosticsAndRejectsTruncation(t *testing.T) {
	frame := func(channel byte, data string) []byte {
		header := make([]byte, 8, 8+len(data))
		header[0] = channel
		binary.BigEndian.PutUint32(header[4:], uint32(len(data)))
		return append(header, []byte(data)...)
	}
	stream := append(frame(1, "dump"), frame(2, "diagnostic")...)
	var stdout bytes.Buffer
	if err := copyDockerOutput(bytes.NewReader(stream), &stdout); err != nil || stdout.String() != "dump" {
		t.Fatalf("output %q: %v", stdout.String(), err)
	}
	for _, truncated := range [][]byte{stream[:2], stream[:len(stream)-1], frame(3, "bad")} {
		if err := copyDockerOutput(bytes.NewReader(truncated), io.Discard); err == nil {
			t.Fatal("accepted incomplete or invalid stream")
		}
	}
}
